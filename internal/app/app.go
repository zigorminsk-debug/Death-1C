// Package app — прикладной слой программы: настройки, подключение к серверу 1С,
// анализ сеансов и выполнение очистки. Именно с этим слоем работает интерфейс,
// поэтому здесь же действует правило «секреты наружу не выходят».
package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/zigorminsk-debug/death-1c/internal/buildinfo"
	"github.com/zigorminsk-debug/death-1c/internal/cleaner"
	"github.com/zigorminsk-debug/death-1c/internal/config"
	"github.com/zigorminsk-debug/death-1c/internal/logger"
	"github.com/zigorminsk-debug/death-1c/internal/platform"
	"github.com/zigorminsk-debug/death-1c/internal/ras"
	"github.com/zigorminsk-debug/death-1c/internal/securestore"
	"github.com/zigorminsk-debug/death-1c/internal/winsvc"
)

// ErrNotConfigured — подключение к серверу 1С не настроено.
var ErrNotConfigured = errors.New("подключение к серверу 1С не настроено: запустите программу с удерживаемым Shift")

// App — состояние программы.
type App struct {
	mu sync.Mutex

	store    *securestore.Store
	cfg      config.Config
	log      *logger.Logger
	dataDir  string
	firstRun bool

	mock     *ras.MockServer
	lastPlan *cleaner.Plan
	lastSnap *ras.Snapshot
	lastErr  string
	started  time.Time
}

// New загружает настройки и готовит приложение к работе.
func New(dataDir string) (*App, error) {
	if dataDir == "" {
		dataDir = securestore.DefaultDir()
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("не удалось создать каталог %s: %w", dataDir, err)
	}
	securestore.ProtectDir(dataDir)
	store := securestore.New(filepath.Join(dataDir, securestore.FileName))
	log := logger.New(filepath.Join(dataDir, "app.log"), 800)

	cfg, err := store.Load()
	firstRun := false
	if errors.Is(err, securestore.ErrNotFound) {
		firstRun = true
		cfg = config.Default()
		log.Info("настройки не найдены — первый запуск, используется конфигурация по умолчанию")
	} else if err != nil {
		cfg = config.Default()
		log.Errorf("не удалось прочитать сохранённые настройки: %v", err)
	}
	cfg.Normalize()

	a := &App{
		store:    store,
		cfg:      cfg,
		log:      log,
		dataDir:  dataDir,
		firstRun: firstRun,
		started:  time.Now(),
	}
	log.Info("запуск: %s", buildinfo.OneLine())
	log.Info("компьютер %s, пользователь %s, права администратора: %v",
		platform.Hostname(), platform.CurrentUser(), platform.IsElevated())
	if !platform.IsElevated() {
		log.Warn("программа запущена без прав администратора — часть операций может быть недоступна")
	}
	return a, nil
}

// protectionDescription описывает способ защиты сохранённых секретов.
// Текст показывается в настройках, чтобы администратор понимал, где лежат
// учётные данные и чем они защищены.
func protectionDescription() string {
	if runtime.GOOS == "windows" {
		return "AES-256-GCM, ключ защищён DPAPI в режиме «только этот компьютер»; " +
			"доступ к каталогу разрешён только Администраторам и SYSTEM"
	}
	return "AES-256-GCM (режим разработки: защита ключа DPAPI доступна только в Windows)"
}

// Log — журнал приложения.
func (a *App) Log() *logger.Logger { return a.log }

// DataDir — каталог данных программы.
func (a *App) DataDir() string { return a.dataDir }

// FirstRun — программа запущена впервые (настройки ещё не сохранялись).
func (a *App) FirstRun() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.firstRun
}

// StorePath — путь к файлу с зашифрованными настройками.
func (a *App) StorePath() string { return a.store.Path() }

// Config возвращает полную конфигурацию.
// ВНИМАНИЕ: содержит секреты, поэтому наружу (в HTTP-интерфейс) передаётся
// только MaskedConfig().
func (a *App) Config() config.Config {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg
}

// MaskedConfig — конфигурация без секретов, пригодная для интерфейса.
func (a *App) MaskedConfig() config.Config {
	a.mu.Lock()
	cfg := a.cfg
	a.mu.Unlock()
	return cfg.Masked()
}

// SecretHints сообщает интерфейсу, заданы ли секреты, не раскрывая их.
type SecretHints struct {
	OneCUserMasked  string `json:"onec_user_masked"`
	OneCPasswordSet bool   `json:"onec_password_set"`
	WindowsUserMask string `json:"windows_user_masked"`
	WindowsPassSet  bool   `json:"windows_password_set"`
	StorePath       string `json:"store_path"`
	StoreExists     bool   `json:"store_exists"`
	Protection      string `json:"protection"`
}

// Secrets — сведения о сохранённых секретах без их раскрытия.
func (a *App) Secrets() SecretHints {
	a.mu.Lock()
	cfg := a.cfg
	a.mu.Unlock()
	return SecretHints{
		OneCUserMasked:  config.MaskedLogin(cfg.OneC.User),
		OneCPasswordSet: cfg.OneC.Password != "",
		WindowsUserMask: config.MaskedLogin(cfg.Windows.User),
		WindowsPassSet:  cfg.Windows.Password != "",
		StorePath:       a.store.Path(),
		StoreExists:     a.store.Exists(),
		Protection:      protectionDescription(),
	}
}

// SaveConfig сохраняет настройки.
//
// Пустое поле пароля означает «оставить сохранённое значение»: интерфейс
// никогда не получает секрет обратно, поэтому прислать его просто не может.
// Специальное значение clearPassword очищает секрет.
func (a *App) SaveConfig(incoming config.Config, clearOneCPass, clearWindowsPass bool) error {
	a.mu.Lock()
	old := a.cfg
	incoming.Normalize()

	if strings.TrimSpace(incoming.OneC.Password) == "" && !clearOneCPass {
		incoming.OneC.Password = old.OneC.Password
	}
	if clearOneCPass {
		incoming.OneC.Password = ""
	}
	if strings.TrimSpace(incoming.Windows.Password) == "" && !clearWindowsPass {
		incoming.Windows.Password = old.Windows.Password
	}
	if clearWindowsPass {
		incoming.Windows.Password = ""
	}
	// Имена пользователей в интерфейсе не показываются: поле пустое либо
	// содержит маску. И то, и другое означает «оставить сохранённое значение».
	// Стереть логин можно только кнопкой «Забыть все учётные данные».
	if isMasked(incoming.OneC.User) || strings.TrimSpace(incoming.OneC.User) == "" {
		incoming.OneC.User = old.OneC.User
	}
	if isMasked(incoming.Windows.User) || strings.TrimSpace(incoming.Windows.User) == "" {
		incoming.Windows.User = old.Windows.User
	}

	if err := incoming.Validate(); err != nil {
		a.mu.Unlock()
		return err
	}

	if err := a.store.Save(incoming); err != nil {
		a.mu.Unlock()
		return fmt.Errorf("не удалось сохранить настройки: %w", err)
	}
	wasDemo := old.OneC.DemoMode
	a.cfg = incoming
	a.firstRun = false
	a.lastPlan = nil
	a.lastSnap = nil
	a.mu.Unlock()

	a.log.Info("настройки сохранены: сервер 1С %s, администратор кластера %s, Windows-сервер %s",
		incoming.OneC.Address(), config.MaskedLogin(incoming.OneC.User), incoming.Windows.Host)
	if wasDemo && !incoming.OneC.DemoMode {
		a.stopMock()
	}
	return nil
}

func isMasked(s string) bool { return strings.Contains(s, "***") }

// WipeCredentials удаляет сохранённые учётные данные безвозвратно.
func (a *App) WipeCredentials() error {
	a.mu.Lock()
	cfg := a.cfg
	cfg.OneC.User = ""
	cfg.OneC.Password = ""
	cfg.Windows.User = ""
	cfg.Windows.Password = ""
	err := a.store.Save(cfg)
	a.cfg = cfg
	a.mu.Unlock()
	if err != nil {
		return err
	}
	a.log.Warn("сохранённые учётные данные удалены из хранилища")
	return nil
}

// State — сводное состояние для интерфейса.
type State struct {
	Product       string          `json:"product"`
	Version       string          `json:"version"`
	Build         string          `json:"build"`
	BuildNumber   int             `json:"build_number"`
	BuildLabel    string          `json:"build_label"`
	Commit        string          `json:"commit"`
	BuildDate     string          `json:"build_date"`
	Channel       string          `json:"channel"`
	Title         string          `json:"title"`
	OneLine       string          `json:"one_line"`
	Developer     string          `json:"developer"`
	Email         string          `json:"email"`
	EmailLink     string          `json:"email_link"`
	Phone         string          `json:"phone"`
	PhoneLink     string          `json:"phone_link"`
	Elevated      bool            `json:"elevated"`
	CurrentUser   string          `json:"current_user"`
	Hostname      string          `json:"hostname"`
	DataDir       string          `json:"data_dir"`
	Configured    bool            `json:"configured"`
	FirstRun      bool            `json:"first_run"`
	DemoMode      bool            `json:"demo_mode"`
	Address       string          `json:"address"`
	AgentVersion  string          `json:"agent_version"`
	LastFetch     string          `json:"last_fetch"`
	LastError     string          `json:"last_error"`
	Summary       cleaner.Summary `json:"summary"`
	SessionCount  int             `json:"session_count"`
	Uptime        string          `json:"uptime"`
	HasWindowsCrd bool            `json:"has_windows_credentials"`
}

// State собирает данные для заголовка и панели состояния интерфейса.
func (a *App) State() State {
	a.mu.Lock()
	cfg := a.cfg
	plan := a.lastPlan
	snap := a.lastSnap
	lastErr := a.lastErr
	firstRun := a.firstRun
	a.mu.Unlock()

	st := State{
		Product:       buildinfo.ProductName,
		Version:       buildinfo.Version,
		Build:         buildinfo.Build,
		BuildNumber:   buildinfo.Number(),
		BuildLabel:    buildinfo.BuildLabel(),
		Commit:        buildinfo.Commit,
		BuildDate:     buildinfo.Date,
		Channel:       buildinfo.Channel,
		Title:         buildinfo.Title(),
		OneLine:       buildinfo.OneLine(),
		Developer:     buildinfo.DeveloperName,
		Email:         buildinfo.DeveloperEmail,
		EmailLink:     buildinfo.EmailLink,
		Phone:         buildinfo.PhonePretty,
		PhoneLink:     buildinfo.PhoneLink,
		Elevated:      platform.IsElevated(),
		CurrentUser:   platform.CurrentUser(),
		Hostname:      platform.Hostname(),
		DataDir:       a.dataDir,
		FirstRun:      firstRun,
		DemoMode:      cfg.OneC.DemoMode,
		Address:       cfg.OneC.Address(),
		LastError:     lastErr,
		HasWindowsCrd: cfg.HasWindowsCredentials(),
		Uptime:        humanDuration(time.Since(a.started)),
	}
	st.Configured = cfg.OneC.DemoMode || strings.TrimSpace(cfg.OneC.Host) != ""
	if snap != nil {
		st.AgentVersion = snap.AgentVersion
		st.LastFetch = snap.FetchedAt.Format(time.RFC3339)
		st.SessionCount = len(snap.Sessions)
		if snap.AgentVersion != "" {
			st.Address = snap.Addr
		}
	}
	if plan != nil {
		st.Summary = plan.Summary
	}
	return st
}

// Refresh подключается к серверу 1С, опрашивает сеансы и строит план очистки.
func (a *App) Refresh(ctx context.Context) (*cleaner.Plan, error) {
	a.mu.Lock()
	cfg := a.cfg
	a.mu.Unlock()

	if cfg.OneC.DemoMode {
		addr, err := a.ensureMock()
		if err != nil {
			a.setErr(err)
			return nil, err
		}
		cfg.OneC.Host = addr
		cfg.OneC.Port = 0
		cfg.OneC.User = ""
		cfg.OneC.Password = ""
	}
	if err := cfg.Validate(); err != nil {
		a.setErr(err)
		return nil, err
	}

	timeout := time.Duration(cfg.OneC.TimeoutSec) * time.Second
	a.log.Info("подключение к серверу администрирования 1С %s", cfg.OneC.Address())
	client, err := ras.Dial(ctx, cfg.OneC.Address(), timeout)
	if err != nil {
		a.setErr(err)
		return nil, err
	}
	defer client.Close()

	snap, err := ras.CollectSnapshot(ctx, client, cfg.OneC.User, cfg.OneC.Password)
	if err != nil {
		a.setErr(err)
		return nil, err
	}

	plan := cleaner.Build(snap.Sessions, cfg.Policy, time.Now(), snap.Errors)
	a.mu.Lock()
	a.lastSnap = snap
	a.lastPlan = plan
	a.lastErr = ""
	a.mu.Unlock()

	a.log.Info("получено сеансов: %d (пользователей: %d, дубликатов: %d, к завершению: %d), версия агента %s",
		plan.Summary.Total, plan.Summary.Users, plan.Summary.Duplicates, plan.Summary.ToTerminate, snap.AgentVersion)
	for _, e := range snap.Errors {
		a.log.Warn("частичная ошибка опроса: %s", e)
	}
	return plan, nil
}

func (a *App) setErr(err error) {
	a.mu.Lock()
	a.lastErr = err.Error()
	a.mu.Unlock()
	a.log.Errorf("%v", err)
}

// LastPlan возвращает последний построенный план без обращения к серверу.
func (a *App) LastPlan() *cleaner.Plan {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lastPlan
}

// ResultItem — итог по одному сеансу.
type ResultItem struct {
	UUID     string `json:"uuid"`
	User     string `json:"user"`
	Infobase string `json:"infobase"`
	Host     string `json:"host"`
	IdleMin  int    `json:"idle_min"`
	Status   string `json:"status"`
	Detail   string `json:"detail"`
}

// Result — итог операции завершения сеансов.
type Result struct {
	Requested  int          `json:"requested"`
	Terminated int          `json:"terminated"`
	Failed     int          `json:"failed"`
	Skipped    int          `json:"skipped"`
	Took       string       `json:"took"`
	Items      []ResultItem `json:"items"`
	Message    string       `json:"message"`
}

// Execute завершает перечисленные сеансы.
//
// Перед выполнением список сверяется со свежим снимком: сеанс завершается,
// только если он и сейчас признан устаревшим. Это защищает от ситуации,
// когда план устарел, а пользователь уже закрыл «лишние» окна сам.
func (a *App) Execute(ctx context.Context, uuids []string, force bool) (*Result, error) {
	start := time.Now()
	plan, err := a.Refresh(ctx)
	if err != nil {
		return nil, err
	}

	want := map[string]bool{}
	for _, u := range uuids {
		if strings.TrimSpace(u) != "" {
			want[strings.TrimSpace(u)] = true
		}
	}
	targets := []cleaner.Item{}
	for _, it := range plan.Items {
		if len(want) == 0 {
			if it.Action == cleaner.ActionTerminate {
				targets = append(targets, it)
			}
			continue
		}
		if !want[it.UUID] {
			continue
		}
		if it.Action == cleaner.ActionTerminate || force {
			targets = append(targets, it)
		} else {
			targets = append(targets, cleaner.Item{
				UUID: it.UUID, UserName: it.UserName, InfobaseName: it.InfobaseName,
				Host: it.Host, IdleMin: it.IdleMin, Action: cleaner.ActionSkip,
				Reason: "сеанс больше не считается устаревшим — пропущен (" + it.Reason + ")",
			})
		}
	}

	res := &Result{Requested: len(uuids), Items: make([]ResultItem, 0, len(targets))}
	if len(uuids) == 0 {
		res.Requested = len(targets)
	}

	a.mu.Lock()
	cfg := a.cfg
	a.mu.Unlock()

	toTerminate := make([]cleaner.Item, 0, len(targets))
	for _, t := range targets {
		if t.Action == cleaner.ActionTerminate {
			toTerminate = append(toTerminate, t)
		} else {
			res.Skipped++
			res.Items = append(res.Items, ResultItem{
				UUID: t.UUID, User: t.UserName, Infobase: t.InfobaseName, Host: t.Host,
				IdleMin: t.IdleMin, Status: "skipped", Detail: t.Reason,
			})
		}
	}
	if len(toTerminate) == 0 {
		res.Message = "Устаревших сеансов нет: завершать нечего."
		res.Took = humanDuration(time.Since(start))
		a.log.Info("очистка: завершать нечего")
		return res, nil
	}

	if cfg.OneC.DemoMode {
		addr, err := a.ensureMock()
		if err != nil {
			return nil, err
		}
		cfg.OneC.Host = addr
		cfg.OneC.Port = 0
	}
	timeout := time.Duration(cfg.OneC.TimeoutSec) * time.Second
	client, err := ras.Dial(ctx, cfg.OneC.Address(), timeout)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	message := cfg.Policy.TerminateMessage
	a.log.Action("начало завершения %d сеансов", len(toTerminate))
	for _, t := range toTerminate {
		item := ResultItem{
			UUID: t.UUID, User: t.UserName, Infobase: t.InfobaseName,
			Host: t.Host, IdleMin: t.IdleMin,
		}
		err := client.TerminateSession(ctx, t.ClusterID, t.UUID, message)
		if err != nil {
			item.Status = "failed"
			item.Detail = err.Error()
			res.Failed++
			a.log.Errorf("сеанс %s (%s, %s): не завершён — %v", t.UUID, t.UserName, t.InfobaseName, err)
		} else {
			item.Status = "terminated"
			item.Detail = t.Reason
			res.Terminated++
			a.log.Action("завершён сеанс %s: %s / %s / %s (простой %d мин)",
				t.UUID, t.UserName, t.InfobaseName, t.Host, t.IdleMin)
		}
		res.Items = append(res.Items, item)
	}

	res.Took = humanDuration(time.Since(start))
	res.Message = fmt.Sprintf("Завершено сеансов: %d, ошибок: %d, пропущено: %d. Время: %s.",
		res.Terminated, res.Failed, res.Skipped, res.Took)
	a.log.Action("очистка завершена: %d сеансов, %d ошибок, %s", res.Terminated, res.Failed, res.Took)
	return res, nil
}

// TestResult — итог проверки подключения.
type TestResult struct {
	OK       bool     `json:"ok"`
	Message  string   `json:"message"`
	Details  []string `json:"details"`
	Clusters int      `json:"clusters"`
	Sessions int      `json:"sessions"`
	Version  string   `json:"version"`
}

// TestOneC проверяет подключение к серверу администрирования 1С.
func (a *App) TestOneC(ctx context.Context) TestResult {
	cfg := a.Config()
	res := TestResult{Details: []string{}}
	if cfg.OneC.DemoMode {
		addr, err := a.ensureMock()
		if err != nil {
			res.Message = err.Error()
			return res
		}
		res.OK = true
		res.Message = "Демонстрационный режим: учебный сервер 1С запущен на " + addr
		res.Details = append(res.Details, "Боевой сервер не требуется — данные учебные")
		return res
	}
	if err := cfg.Validate(); err != nil {
		res.Message = err.Error()
		return res
	}
	timeout := time.Duration(cfg.OneC.TimeoutSec) * time.Second
	start := time.Now()
	client, err := ras.Dial(ctx, cfg.OneC.Address(), timeout)
	if err != nil {
		res.Message = err.Error()
		res.Details = append(res.Details, hintFor(err))
		return res
	}
	defer client.Close()

	snap, err := ras.CollectSnapshot(ctx, client, cfg.OneC.User, cfg.OneC.Password)
	if err != nil {
		res.Message = err.Error()
		res.Details = append(res.Details, hintFor(err))
		return res
	}
	res.OK = true
	res.Clusters = len(snap.Clusters)
	res.Sessions = len(snap.Sessions)
	res.Version = snap.AgentVersion
	res.Message = fmt.Sprintf("Подключение успешно: кластеров %d, сеансов %d, версия агента %s, время %s",
		len(snap.Clusters), len(snap.Sessions), snap.AgentVersion, humanDuration(time.Since(start)))
	res.Details = append(res.Details, fmt.Sprintf("Версия сервиса администрирования: %d", client.EndpointVersion()))
	res.Details = append(res.Details, snap.Errors...)
	return res
}

// TestWindows проверяет учётные данные администратора Windows-сервера.
func (a *App) TestWindows(ctx context.Context) TestResult {
	cfg := a.Config()
	res := TestResult{Details: []string{}}
	if !cfg.HasWindowsCredentials() {
		res.Message = "Не заданы сервер, логин или пароль администратора Windows"
		return res
	}
	mgr := winsvc.New(cfg.Windows.Host, cfg.Windows.User, cfg.Windows.Password, a.log.Infof)
	if err := mgr.Connect(); err != nil {
		res.Message = err.Error()
		res.Details = append(res.Details,
			"Проверьте логин (формат ДОМЕН\\пользователь или .\\пользователь), пароль и доступ по SMB (TCP 445)")
		return res
	}
	list, err := mgr.List()
	if err != nil {
		res.Message = err.Error()
		return res
	}
	res.OK = true
	res.Message = fmt.Sprintf("Учётные данные приняты сервером %s. Найдено служб 1С: %d", cfg.Windows.Host, len(list))
	for _, s := range list {
		state := "остановлена"
		if s.Running {
			state = "работает"
		}
		res.Details = append(res.Details, fmt.Sprintf("%s — %s (%s)", s.Display, state, s.Name))
	}
	return res
}

// Services возвращает список служб 1С на Windows-сервере.
func (a *App) Services(ctx context.Context) ([]winsvc.Service, error) {
	cfg := a.Config()
	if !cfg.HasWindowsCredentials() {
		return nil, winsvc.ErrNotConfigured
	}
	mgr := winsvc.New(cfg.Windows.Host, cfg.Windows.User, cfg.Windows.Password, a.log.Infof)
	return mgr.List()
}

// ServiceAction выполняет start/stop/restart службы на сервере 1С.
func (a *App) ServiceAction(ctx context.Context, name, action string) error {
	cfg := a.Config()
	if !cfg.HasWindowsCredentials() {
		return winsvc.ErrNotConfigured
	}
	mgr := winsvc.New(cfg.Windows.Host, cfg.Windows.User, cfg.Windows.Password, a.log.Infof)
	switch strings.ToLower(action) {
	case "start":
		return mgr.Start(name)
	case "stop":
		return mgr.Stop(name)
	case "restart":
		return mgr.Restart(name)
	}
	return fmt.Errorf("неизвестное действие со службой: %s", action)
}

// ensureMock поднимает учебный сервер 1С, если он ещё не запущен.
func (a *App) ensureMock() (string, error) {
	a.mu.Lock()
	if a.mock != nil {
		addr := a.mock.Addr()
		a.mu.Unlock()
		return addr, nil
	}
	a.mu.Unlock()

	srv, err := ras.NewMockServer(ras.MockOptions{})
	if err != nil {
		return "", fmt.Errorf("не удалось запустить учебный сервер 1С: %w", err)
	}
	a.mu.Lock()
	if a.mock != nil {
		addr := a.mock.Addr()
		a.mu.Unlock()
		srv.Close()
		return addr, nil
	}
	a.mock = srv
	a.mu.Unlock()
	a.log.Warn("демонстрационный режим: запущен учебный сервер 1С на %s (данные ненастоящие)", srv.Addr())
	return srv.Addr(), nil
}

func (a *App) stopMock() {
	a.mu.Lock()
	srv := a.mock
	a.mock = nil
	a.mu.Unlock()
	if srv != nil {
		srv.Close()
		a.log.Info("учебный сервер 1С остановлен")
	}
}

// Close освобождает ресурсы перед выходом.
func (a *App) Close() {
	a.stopMock()
	if a.log != nil {
		a.log.Info("завершение работы программы")
		a.log.Close()
	}
}

// hintFor подсказывает администратору, что проверить при типичных отказах.
func hintFor(err error) string {
	text := strings.ToLower(err.Error())
	switch {
	case strings.Contains(text, "авториз") || strings.Contains(text, "access") || strings.Contains(text, "auth"):
		return "Проверьте имя и пароль администратора кластера 1С (они задаются в консоли администрирования серверов 1С)"
	case strings.Contains(text, "не удалось подключиться") || strings.Contains(text, "refused") || strings.Contains(text, "timeout") || strings.Contains(text, "i/o timeout"):
		return "Проверьте, что на сервере запущена служба RAS (порт 1545) и он доступен по сети; запустить её можно на вкладке «Службы Windows»"
	case strings.Contains(text, "не зарегистрировано ни одного кластера"):
		return "Служба RAS запущена, но к агенту не подключён ни один кластер — проверьте параметры службы ras.exe"
	case strings.Contains(text, "endpoint"):
		return "Сервер администрирования отверг запрос: возможно, версия RAS не совпадает с версией платформы 1С"
	}
	return ""
}

func humanDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%d мс", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1f с", d.Seconds())
	}
	m := int(d.Minutes())
	if m < 60 {
		return fmt.Sprintf("%d мин", m)
	}
	return fmt.Sprintf("%d ч %d мин", m/60, m%60)
}
