// Package config описывает настройки программы: подключение к серверу 1С,
// учётные данные администратора Windows-сервера и правила очистки сеансов.
//
// Настройки делятся на открытые и секретные. Секретные значения (пароли и имена
// пользователей) никогда не возвращаются в интерфейс: для этого есть Masked().
package config

import (
	"fmt"
	"strconv"
	"strings"
)

// DefaultRasPort — порт сервера администрирования 1С по умолчанию.
const DefaultRasPort = 1545

// Config — полный набор настроек.
type Config struct {
	OneC    OneC    `json:"onec"`
	Windows Windows `json:"windows"`
	Policy  Policy  `json:"policy"`
	UI      UI      `json:"ui"`
}

// OneC — параметры подключения к серверу администрирования 1С (RAS).
type OneC struct {
	Host       string `json:"host"`
	Port       int    `json:"port"`
	User       string `json:"user"`
	Password   string `json:"password"`
	TimeoutSec int    `json:"timeout_sec"`
	// DemoMode включает учебный сервер 1С внутри программы:
	// можно посмотреть работу интерфейса без боевого сервера.
	DemoMode bool `json:"demo_mode"`
}

// Address возвращает «хост:порт» для подключения.
func (o OneC) Address() string {
	host := strings.TrimSpace(o.Host)
	if host == "" {
		host = "localhost"
	}
	port := o.Port
	if port <= 0 {
		port = DefaultRasPort
	}
	if strings.Contains(host, ":") && !strings.Contains(host, "]") {
		return host
	}
	return netJoin(host, port)
}

func netJoin(host string, port int) string {
	return host + ":" + strconv.Itoa(port)
}

// Windows — учётные данные администратора Windows-сервера, на котором работает 1С.
// Используются, чтобы проверить и при необходимости запустить службу RAS
// и службу агента сервера 1С на удалённой машине.
type Windows struct {
	Host           string `json:"host"`
	User           string `json:"user"`
	Password       string `json:"password"`
	ManageServices bool   `json:"manage_services"`
}

// Policy — правила выбора «старых» и задублированных сеансов.
type Policy struct {
	// GroupBy: "user_ib" — пользователь в разрезе базы (по умолчанию),
	// "user" — пользователь по всему кластеру.
	GroupBy string `json:"group_by"`
	// KeepPerGroup — сколько самых актуальных сеансов оставить в группе.
	KeepPerGroup int `json:"keep_per_group"`
	// MinIdleMin — завершать только сеансы с простоем не менее N минут (0 — без ограничения).
	MinIdleMin int `json:"min_idle_min"`
	// MaxAgeHours — завершать только сеансы старше N часов (0 — без ограничения).
	MaxAgeHours int `json:"max_age_hours"`
	// ExcludeAppIDs — служебные приложения, сеансы которых не трогаем.
	ExcludeAppIDs []string `json:"exclude_app_ids"`
	// ExcludeUsers — защищённые пользователи (служебные учётные записи).
	ExcludeUsers []string `json:"exclude_users"`
	// SkipEmptyUser — не трогать сеансы без имени пользователя.
	SkipEmptyUser bool `json:"skip_empty_user"`
	// TerminateMessage — текст, который увидит пользователь при завершении сеанса.
	TerminateMessage string `json:"terminate_message"`
	// Confirm — требовать подтверждение перед завершением сеансов.
	Confirm bool `json:"confirm"`
	// SortBy: "activity" (по умолчанию) — по последней активности,
	// "started" — по времени запуска сеанса.
	SortBy string `json:"sort_by"`
}

// UI — параметры интерфейса.
type UI struct {
	// AutoRefreshSec — периодичность автообновления списка сеансов, 0 — выключено.
	AutoRefreshSec int `json:"auto_refresh_sec"`
	// WindowMode: "app" — отдельное окно (Edge/Chrome в режиме приложения),
	// "browser" — вкладка браузера по умолчанию.
	WindowMode string `json:"window_mode"`
	// ShowServiceSessions — показывать в таблице служебные сеансы (они не завершаются).
	ShowServiceSessions bool `json:"show_service_sessions"`
}

// DefaultExcludeAppIDs — служебные приложения 1С по умолчанию.
var DefaultExcludeAppIDs = []string{
	"BackgroundJob",
	"SrvrConsole",
	"COMConnection",
	"JobScheduler",
	"Designer",
	"WebServerExtension",
	"RAS",
	"1C:Enterprise 8.3 Remote Server",
}

// DefaultTerminateMessage — сообщение сеансу по умолчанию.
const DefaultTerminateMessage = "Сеанс завершён автоматически: обнаружен более актуальный сеанс этого пользователя. Программа «Очистка сеансов 1С»."

// Default возвращает настройки по умолчанию. Пароли пустые — их вводит
// администратор один раз при первом запуске.
func Default() Config {
	return Config{
		OneC: OneC{
			Host:       "localhost",
			Port:       DefaultRasPort,
			TimeoutSec: 30,
		},
		Windows: Windows{
			Host:           "localhost",
			ManageServices: true,
		},
		Policy: Policy{
			GroupBy:          "user_ib",
			KeepPerGroup:     1,
			ExcludeAppIDs:    append([]string(nil), DefaultExcludeAppIDs...),
			SkipEmptyUser:    true,
			TerminateMessage: DefaultTerminateMessage,
			Confirm:          true,
			SortBy:           "activity",
		},
		UI: UI{
			AutoRefreshSec:      0,
			WindowMode:          "app",
			ShowServiceSessions: true,
		},
	}
}

// Normalize приводит настройки к допустимому виду: заполняет пропуски
// значениями по умолчанию и обрезает бессмысленные значения.
func (c *Config) Normalize() {
	def := Default()

	c.OneC.Host = strings.TrimSpace(c.OneC.Host)
	if c.OneC.Host == "" {
		c.OneC.Host = def.OneC.Host
	}
	if c.OneC.Port <= 0 || c.OneC.Port > 65535 {
		c.OneC.Port = DefaultRasPort
	}
	if c.OneC.TimeoutSec < 3 || c.OneC.TimeoutSec > 600 {
		c.OneC.TimeoutSec = def.OneC.TimeoutSec
	}

	c.Windows.Host = strings.TrimSpace(c.Windows.Host)
	if c.Windows.Host == "" {
		c.Windows.Host = def.Windows.Host
	}

	switch c.Policy.GroupBy {
	case "user", "user_ib":
	default:
		c.Policy.GroupBy = def.Policy.GroupBy
	}
	if c.Policy.KeepPerGroup < 1 {
		c.Policy.KeepPerGroup = 1
	}
	if c.Policy.KeepPerGroup > 50 {
		c.Policy.KeepPerGroup = 50
	}
	if c.Policy.MinIdleMin < 0 {
		c.Policy.MinIdleMin = 0
	}
	if c.Policy.MaxAgeHours < 0 {
		c.Policy.MaxAgeHours = 0
	}
	if len(c.Policy.ExcludeAppIDs) == 0 {
		c.Policy.ExcludeAppIDs = nil
	}
	c.Policy.ExcludeAppIDs = cleanList(c.Policy.ExcludeAppIDs)
	c.Policy.ExcludeUsers = cleanList(c.Policy.ExcludeUsers)
	if strings.TrimSpace(c.Policy.TerminateMessage) == "" {
		c.Policy.TerminateMessage = def.Policy.TerminateMessage
	}
	if len(c.Policy.TerminateMessage) > 500 {
		c.Policy.TerminateMessage = c.Policy.TerminateMessage[:500]
	}
	switch c.Policy.SortBy {
	case "activity", "started":
	default:
		c.Policy.SortBy = def.Policy.SortBy
	}

	if c.UI.AutoRefreshSec < 0 {
		c.UI.AutoRefreshSec = 0
	}
	if c.UI.AutoRefreshSec > 0 && c.UI.AutoRefreshSec < 5 {
		c.UI.AutoRefreshSec = 5
	}
	switch c.UI.WindowMode {
	case "app", "browser":
	default:
		c.UI.WindowMode = def.UI.WindowMode
	}
}

func cleanList(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		key := strings.ToLower(v)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Validate проверяет, достаточно ли данных для подключения к серверу 1С.
func (c Config) Validate() error {
	if c.OneC.DemoMode {
		return nil
	}
	if strings.TrimSpace(c.OneC.Host) == "" {
		return fmt.Errorf("не указан адрес сервера администрирования 1С (RAS)")
	}
	return nil
}

// HasOneCCredentials сообщает, заданы ли учётные данные администратора кластера.
// Пустые значения допустимы: у кластера может не быть администраторов.
func (c Config) HasOneCCredentials() bool {
	return strings.TrimSpace(c.OneC.User) != "" || c.OneC.Password != ""
}

// HasWindowsCredentials сообщает, заданы ли учётные данные администратора Windows.
func (c Config) HasWindowsCredentials() bool {
	return strings.TrimSpace(c.Windows.Host) != "" &&
		strings.TrimSpace(c.Windows.User) != "" &&
		c.Windows.Password != ""
}

// Masked возвращает копию настроек, пригодную для передачи в интерфейс.
// Пароли удаляются полностью, имена пользователей сокращаются до намёка —
// посмотреть сохранённые учётные данные через интерфейс нельзя.
func (c Config) Masked() Config {
	out := c
	out.OneC.Password = ""
	out.Windows.Password = ""
	out.OneC.User = maskLogin(c.OneC.User)
	out.Windows.User = maskLogin(c.Windows.User)
	out.OneC.Host = c.OneC.Host
	out.Windows.Host = c.Windows.Host
	out.Policy.ExcludeAppIDs = append([]string(nil), c.Policy.ExcludeAppIDs...)
	out.Policy.ExcludeUsers = append([]string(nil), c.Policy.ExcludeUsers...)
	return out
}

// maskLogin превращает «Администратор» в «А***р»: ни полный логин,
// ни его длина не раскрываются.
func maskLogin(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	r := []rune(s)
	if len(r) == 1 {
		return string(r[0]) + "***"
	}
	if len(r) == 2 {
		return string(r[0]) + "***"
	}
	return string(r[0]) + "***" + string(r[len(r)-1])
}

// MaskedLogin — намёк на сохранённый логин (для подписи в интерфейсе).
func MaskedLogin(s string) string { return maskLogin(s) }
