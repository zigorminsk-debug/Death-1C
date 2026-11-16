//go:build windows

// Package winsvc управляет службами Windows на удалённом сервере 1С,
// используя сохранённые учётные данные администратора Windows.
//
// Это единственное место, где программе нужны именно «виндовс-админские»
// логин и пароль: по ним устанавливается защищённое соединение IPC$ с сервером,
// после чего можно посмотреть состояние служб 1С и запустить сервер
// администрирования (RAS), если он остановлен. Без запущенного RAS программа
// не может ни показать сеансы, ни завершить их.
//
// Пароль никогда не передаётся в командной строке — только через стандартный
// ввод команды net use, поэтому он не виден в списке процессов.
package winsvc

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/zigorminsk-debug/death-1c/internal/winexec"
)

// Service — служба Windows на сервере 1С.
type Service struct {
	Name    string `json:"name"`
	Display string `json:"display"`
	State   string `json:"state"`
	Running bool   `json:"running"`
	Kind    string `json:"kind"`
}

// Manager — операции со службами удалённого сервера.
type Manager struct {
	mu        sync.Mutex
	host      string
	user      string
	password  string
	connected bool
	logf      func(format string, args ...interface{})
}

// New создаёт менеджер служб.
func New(host, user, password string, logf func(format string, args ...interface{})) *Manager {
	if logf == nil {
		logf = func(string, ...interface{}) {}
	}
	return &Manager{host: strings.TrimSpace(host), user: user, password: password, logf: logf}
}

// Host — адрес сервера.
func (m *Manager) Host() string { return m.host }

// Enabled сообщает, достаточно ли данных для удалённого управления.
func (m *Manager) Enabled() bool {
	return m.host != "" && strings.TrimSpace(m.user) != "" && m.password != ""
}

// ErrNotConfigured — учётные данные администратора Windows не заданы.
var ErrNotConfigured = errors.New("не заданы учётные данные администратора Windows-сервера")

func (m *Manager) ipcPath() string { return `\\` + m.host + `\IPC$` }

func isLocal(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	switch h {
	case "", "localhost", "127.0.0.1", ".", "::1", "local":
		return true
	}
	return false
}

// Connect проверяет учётные данные и устанавливает соединение IPC$ с сервером.
// Для локального сервера соединение не нужно — достаточно прав текущего процесса.
func (m *Manager) Connect() error {
	if !m.Enabled() {
		return ErrNotConfigured
	}
	if isLocal(m.host) {
		m.setConnected(true)
		return nil
	}
	// Сначала убираем возможное старое соединение, иначе Windows ответит
	// ошибкой 1219 «множественные подключения к одному ресурсу».
	_, _ = m.runSilent("net", "use", m.ipcPath(), "/delete", "/y")

	cmd := winexec.Command("net", "use", m.ipcPath(), "/user:"+m.user, "*", "/persistent:no")
	cmd.Stdin = strings.NewReader(m.password + "\n")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("подключение к %s от имени %s не выполнено: %s",
			m.host, maskUser(m.user), firstLine(string(out), err))
	}
	m.setConnected(true)
	m.logf("установлено соединение IPC$ с %s", m.host)
	return nil
}

// Disconnect разрывает соединение IPC$.
func (m *Manager) Disconnect() error {
	if !m.isConnected() || isLocal(m.host) {
		return nil
	}
	m.setConnected(false)
	_, err := m.runSilent("net", "use", m.ipcPath(), "/delete", "/y")
	return err
}

// TestCredentials проверяет учётные данные администратора Windows.
func (m *Manager) TestCredentials() error {
	if !m.Enabled() {
		return ErrNotConfigured
	}
	if err := m.Connect(); err != nil {
		return err
	}
	if _, err := m.List(); err != nil {
		return err
	}
	return nil
}

var lineRe = regexp.MustCompile(`^([^|]+)\|([A-Za-z]+)\|(.*)$`)

// List возвращает службы сервера, относящиеся к 1С.
//
// Основной способ — PowerShell: его вывод не зависит от языка системы.
// Запасной способ — точечные запросы sc.exe по известным именам служб 1С.
func (m *Manager) List() ([]Service, error) {
	if err := m.ensureConnected(); err != nil {
		return nil, err
	}
	if isLocal(m.host) {
		return m.listLocal()
	}
	script := fmt.Sprintf(
		"Get-Service -ComputerName '%s' | ForEach-Object { '{0}|{1}|{2}' -f $_.Name, $_.Status, $_.DisplayName }",
		strings.ReplaceAll(m.host, "'", "''"))
	out, err := m.runSilent("powershell", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-Command", script)
	if err == nil {
		if list := parseServiceLines(out); len(list) > 0 || strings.TrimSpace(out) == "" {
			return filterRelevant(list), nil
		}
	} else {
		m.logf("PowerShell недоступен или отклонил запрос (%v) — переключаюсь на sc.exe", err)
	}
	return m.listByKnownNames()
}

func (m *Manager) listLocal() ([]Service, error) {
	out, err := m.runSilent("powershell", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-Command",
		"Get-Service | ForEach-Object { '{0}|{1}|{2}' -f $_.Name, $_.Status, $_.DisplayName }")
	if err != nil {
		return m.listByKnownNames()
	}
	return filterRelevant(parseServiceLines(out)), nil
}

// knownServiceNames — типовые имена служб платформы 1С:Предприятие.
var knownServiceNames = []string{
	"1C:Enterprise 8.3 Server Agent",
	"1C:Enterprise 8.3 Remote Server",
	"1C:Enterprise 8.3 Administration Server",
	"1CV8",
	"1CV82",
	"1CV83",
	"1CRAS",
	"ragent",
	"ras",
	"rmngr",
	"rphost",
}

func (m *Manager) listByKnownNames() ([]Service, error) {
	out := make([]Service, 0, len(knownServiceNames))
	target := ""
	if !isLocal(m.host) {
		target = `\\` + m.host
	}
	for _, name := range knownServiceNames {
		args := []string{}
		if target != "" {
			args = append(args, target)
		}
		args = append(args, "query", name)
		text, err := m.runSilent("sc", args...)
		if err != nil {
			continue
		}
		state := parseScState(text)
		if state == "" {
			continue
		}
		out = append(out, Service{Name: name, Display: name, State: state, Running: state == "Running"})
	}
	return filterRelevant(out), nil
}

// scStateRe ищет строку состояния службы в выводе sc.exe.
// Число всегда одно (4 — работает, 1 — остановлена), что позволяет
// распознать состояние независимо от языка операционной системы.
var scStateRe = regexp.MustCompile(`(?m)^\s*[^\r\n:]+:\s+([1-7])\s+([A-Za-zА-Яа-я_]+)`)

func parseScState(text string) string {
	all := scStateRe.FindAllStringSubmatch(text, -1)
	if len(all) == 0 {
		return ""
	}
	// В блоке службы строка состояния идёт после строки типа,
	// поэтому берём последнее совпадение.
	m := all[len(all)-1]
	switch m[1] {
	case "4":
		return "Running"
	case "1":
		return "Stopped"
	case "2":
		return "StartPending"
	case "3":
		return "StopPending"
	case "7":
		return "Paused"
	}
	return "Unknown"
}

func parseServiceLines(text string) []Service {
	out := []Service{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		m := lineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		out = append(out, Service{
			Name:    strings.TrimSpace(m[1]),
			State:   strings.TrimSpace(m[2]),
			Display: strings.TrimSpace(m[3]),
			Running: strings.EqualFold(strings.TrimSpace(m[2]), "Running"),
		})
	}
	return out
}

// filterRelevant оставляет только службы, имеющие отношение к 1С.
func filterRelevant(in []Service) []Service {
	out := []Service{}
	for _, s := range in {
		kind := classify(s.Name + " " + s.Display)
		if kind == "" {
			continue
		}
		s.Kind = kind
		out = append(out, s)
	}
	return out
}

func classify(text string) string {
	t := strings.ToLower(text)
	switch {
	case strings.Contains(t, "remote server"), strings.Contains(t, "administration server"),
		strings.HasSuffix(strings.TrimSpace(t), "ras"), strings.Contains(t, " ras "):
		return "ras"
	case strings.Contains(t, "server agent"), strings.Contains(t, "ragent"):
		return "agent"
	case strings.Contains(t, "1c"), strings.Contains(t, "1cv8"), strings.Contains(t, "enterprise 8"),
		strings.Contains(t, "1с"), strings.Contains(t, "предприяти"), strings.Contains(t, "v8"):
		return "1c"
	}
	return ""
}

// Start запускает службу.
func (m *Manager) Start(name string) error { return m.control(name, "start") }

// Stop останавливает службу.
func (m *Manager) Stop(name string) error { return m.control(name, "stop") }

// Restart перезапускает службу: остановка, ожидание, запуск.
func (m *Manager) Restart(name string) error {
	if err := m.control(name, "stop"); err != nil {
		m.logf("остановка службы %s завершилась с сообщением: %v", name, err)
	}
	for i := 0; i < 15; i++ {
		time.Sleep(time.Second)
		list, err := m.List()
		if err != nil {
			break
		}
		stopped := true
		for _, s := range list {
			if strings.EqualFold(s.Name, name) && s.Running {
				stopped = false
			}
		}
		if stopped {
			break
		}
	}
	return m.control(name, "start")
}

func (m *Manager) control(name, action string) error {
	if err := m.ensureConnected(); err != nil {
		return err
	}
	if strings.TrimSpace(name) == "" {
		return errors.New("не указано имя службы")
	}
	args := []string{}
	if !isLocal(m.host) {
		args = append(args, `\\`+m.host)
	}
	args = append(args, action, name)
	out, err := m.runSilent("sc", args...)
	if err != nil {
		return fmt.Errorf("sc %s %s: %s", action, name, firstLine(out, err))
	}
	m.logf("служба %s на %s: команда %s выполнена", name, m.host, action)
	return nil
}

func (m *Manager) ensureConnected() error {
	if m.isConnected() {
		return nil
	}
	return m.Connect()
}

func (m *Manager) isConnected() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.connected
}

func (m *Manager) setConnected(v bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.connected = v
}

func (m *Manager) runSilent(name string, args ...string) (string, error) {
	cmd := winexec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func firstLine(out string, err error) string {
	for _, line := range strings.Split(strings.ReplaceAll(out, "\r", ""), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	if err != nil {
		return err.Error()
	}
	return "неизвестная ошибка"
}

func maskUser(u string) string {
	u = strings.TrimSpace(u)
	r := []rune(u)
	if len(r) <= 2 {
		return "***"
	}
	return string(r[0]) + "***" + string(r[len(r)-1])
}
