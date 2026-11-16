// Package ui поднимает локальный веб-интерфейс программы.
//
// Интерфейс — обычная HTML-страница со стилями и скриптом, которая отдаётся
// встроенным HTTP-сервером только на адрес 127.0.0.1 и открывается отдельным
// окном (Edge/Chrome в режиме приложения). Каждый запрос к API обязан содержать
// одноразовый токен, который выдаётся только вместе со страницей, поэтому
// соседние процессы и сайты не могут управлять программой.
package ui

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/zigorminsk-debug/death-1c/internal/app"
	"github.com/zigorminsk-debug/death-1c/internal/buildinfo"
	"github.com/zigorminsk-debug/death-1c/internal/config"
	"github.com/zigorminsk-debug/death-1c/internal/platform"
)

//go:embed static
var staticFS embed.FS

// Server — локальный HTTP-сервер интерфейса.
type Server struct {
	app       *app.App
	token     string
	ln        net.Listener
	http      *http.Server
	startView string
	baseURL   string

	mu          sync.Mutex
	lastBeat    time.Time
	allowRemote bool

	quit     chan struct{}
	quitOnce sync.Once
}

// New создаёт сервер интерфейса.
// startView: "main" — список сеансов, "settings" — настройки (запуск с Shift).
func New(a *app.App, startView string) (*Server, error) {
	tok, err := randomToken()
	if err != nil {
		return nil, err
	}
	if startView != "settings" {
		startView = "main"
	}
	return &Server{
		app:       a,
		token:     tok,
		startView: startView,
		lastBeat:  time.Now(),
		quit:      make(chan struct{}),
	}, nil
}

func randomToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("не удалось сгенерировать токен сессии: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// Start слушает указанный адрес и возвращает URL интерфейса.
// Пустой адрес означает «127.0.0.1, свободный порт».
func (s *Server) Start(addr string) (string, error) {
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = "127.0.0.1"
	}
	if host != "127.0.0.1" && host != "localhost" && host != "::1" && host != "[::1]" {
		s.allowRemote = true
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", fmt.Errorf("не удалось запустить локальный сервер интерфейса: %w", err)
	}
	s.ln = ln
	port := ln.Addr().(*net.TCPAddr).Port
	s.baseURL = fmt.Sprintf("http://127.0.0.1:%d/", port)

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/app.css", s.handleStatic("static/app.css", "text/css; charset=utf-8"))
	mux.HandleFunc("/app.js", s.handleStatic("static/app.js", "application/javascript; charset=utf-8"))
	mux.HandleFunc("/favicon.ico", s.handleStatic("static/favicon.ico", "image/x-icon"))
	mux.HandleFunc("/favicon.svg", s.handleStatic("static/favicon.svg", "image/svg+xml"))
	mux.HandleFunc("/bootstrap.js", s.handleBootstrap)
	mux.HandleFunc("/api/", s.handleAPI)

	s.http = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      300 * time.Second,
		IdleTimeout:       90 * time.Second,
	}
	go func() {
		if err := s.http.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.app.Log().Errorf("локальный сервер интерфейса остановлен: %v", err)
		}
	}()
	s.app.Log().Info("интерфейс запущен: %s (стартовый экран: %s)", s.baseURL, s.startView)
	return s.baseURL, nil
}

// URL — адрес интерфейса.
func (s *Server) URL() string { return s.baseURL }

// Quit возвращает канал, который закрывается по команде «Выход» из интерфейса.
func (s *Server) Quit() <-chan struct{} { return s.quit }

// RequestQuit запрашивает завершение программы.
func (s *Server) RequestQuit() {
	s.quitOnce.Do(func() { close(s.quit) })
}

// LastActivity — момент последнего обращения интерфейса (для сторожа простоя).
func (s *Server) LastActivity() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastBeat
}

func (s *Server) touch() {
	s.mu.Lock()
	s.lastBeat = time.Now()
	s.mu.Unlock()
}

// Shutdown останавливает сервер.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.http == nil {
		return nil
	}
	return s.http.Shutdown(ctx)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	s.touch()
	if r.URL.Path != "/" && r.URL.Path != "/index.html" {
		http.NotFound(w, r)
		return
	}
	data, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		http.Error(w, "страница интерфейса отсутствует в сборке", http.StatusInternalServerError)
		return
	}
	page := strings.ReplaceAll(string(data), "{{TITLE}}", htmlEscape(buildinfo.Title()))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Frame-Options", "DENY")
	_, _ = io.WriteString(w, page)
}

// bootstrapData готовит данные, которые страница получает до первого запроса:
// токен сессии, стартовый экран, сведения о сборке и замаскированные настройки.
func (s *Server) bootstrapData() map[string]interface{} {
	state := s.app.State()
	return map[string]interface{}{
		"token":      s.token,
		"startView":  s.startView,
		"state":      state,
		"config":     s.app.MaskedConfig(),
		"secrets":    s.app.Secrets(),
		"contacts":   buildinfo.Contacts(),
		"firstRun":   s.app.FirstRun(),
		"elevated":   platform.IsElevated(),
		"storePath":  s.app.StorePath(),
		"dataDir":    s.app.DataDir(),
		"remoteMode": s.allowRemote,
	}
}

// handleBootstrap отдаёт стартовые данные отдельным скриптом: так страница
// остаётся без встроенного JavaScript и работает под жёстким CSP.
func (s *Server) handleBootstrap(w http.ResponseWriter, r *http.Request) {
	s.touch()
	raw, err := json.Marshal(s.bootstrapData())
	if err != nil {
		http.Error(w, "не удалось подготовить данные окна", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, "window.APP = "+string(raw)+";\n")
}

func (s *Server) handleStatic(name, contentType string) http.HandlerFunc {
	data, err := staticFS.ReadFile(name)
	if err != nil {
		return func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }
	}
	return func(w http.ResponseWriter, r *http.Request) {
		s.touch()
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(data)
	}
}

func (s *Server) handleAPI(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		writeJSON(w, http.StatusForbidden, map[string]interface{}{
			"error": "запрос к интерфейсу без токена сессии отклонён",
		})
		return
	}
	s.touch()

	path := strings.TrimPrefix(r.URL.Path, "/api/")
	switch path {
	case "heartbeat":
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
	case "state":
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"state":   s.app.State(),
			"config":  s.app.MaskedConfig(),
			"secrets": s.app.Secrets(),
		})
	case "refresh":
		s.apiRefresh(w, r)
	case "execute":
		s.apiExecute(w, r)
	case "config":
		s.apiConfig(w, r)
	case "wipe":
		s.apiWipe(w, r)
	case "test/onec":
		s.apiTestOneC(w, r)
	case "test/windows":
		s.apiTestWindows(w, r)
	case "services":
		s.apiServices(w, r)
	case "service-action":
		s.apiServiceAction(w, r)
	case "log":
		writeJSON(w, http.StatusOK, map[string]interface{}{"entries": s.app.Log().Recent(300)})
	case "log.txt":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, s.app.Log().Text())
	case "open":
		s.apiOpen(w, r)
	case "quit":
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "message": "программа завершается"})
		s.RequestQuit()
	default:
		writeJSON(w, http.StatusNotFound, map[string]interface{}{"error": "неизвестный запрос: " + path})
	}
}

// authorized проверяет токен сессии и источник запроса.
func (s *Server) authorized(r *http.Request) bool {
	if r.Header.Get("X-Session-Token") != s.token {
		return false
	}
	if s.allowRemote {
		return true
	}
	host := r.Host
	if i := strings.LastIndex(host, ":"); i > 0 && !strings.Contains(host[i:], "]") {
		host = host[:i]
	}
	host = strings.Trim(host, "[]")
	switch host {
	case "127.0.0.1", "localhost", "::1":
		return true
	}
	return false
}

func (s *Server) apiRefresh(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()
	plan, err := s.app.Refresh(ctx)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]interface{}{
			"error": err.Error(),
			"state": s.app.State(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"plan": plan, "state": s.app.State()})
}

func (s *Server) apiExecute(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UUIDs []string `json:"uuids"`
		Force bool     `json:"force"`
	}
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 300*time.Second)
	defer cancel()
	res, err := s.app.Execute(ctx, req.UUIDs, req.Force)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]interface{}{"error": err.Error()})
		return
	}
	plan := s.app.LastPlan()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"result": res,
		"plan":   plan,
		"state":  s.app.State(),
	})
}

func (s *Server) apiConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Config               config.Config `json:"config"`
		ClearOneCPassword    bool          `json:"clear_onec_password"`
		ClearWindowsPassword bool          `json:"clear_windows_password"`
	}
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
		return
	}
	if err := s.app.SaveConfig(req.Config, req.ClearOneCPassword, req.ClearWindowsPassword); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":      true,
		"message": "Настройки сохранены и зашифрованы",
		"config":  s.app.MaskedConfig(),
		"secrets": s.app.Secrets(),
		"state":   s.app.State(),
	})
}

func (s *Server) apiWipe(w http.ResponseWriter, r *http.Request) {
	if err := s.app.WipeCredentials(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":      true,
		"message": "Учётные данные удалены из хранилища",
		"config":  s.app.MaskedConfig(),
		"secrets": s.app.Secrets(),
	})
}

func (s *Server) apiTestOneC(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, s.app.TestOneC(ctx))
}

func (s *Server) apiTestWindows(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, s.app.TestWindows(ctx))
}

func (s *Server) apiServices(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	list, err := s.app.Services(ctx)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]interface{}{"error": err.Error(), "services": []interface{}{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"services": list})
}

func (s *Server) apiServiceAction(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name   string `json:"name"`
		Action string `json:"action"`
	}
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 180*time.Second)
	defer cancel()
	if err := s.app.ServiceAction(ctx, req.Name, req.Action); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]interface{}{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"ok":      true,
		"message": fmt.Sprintf("Команда «%s» для службы «%s» выполнена", req.Action, req.Name),
	})
}

// apiOpen открывает внешнюю ссылку (mailto:, tel:) системным обработчиком.
// В окне приложения такие ссылки обычно работают и сами, но запасной путь
// гарантирует, что контакты разработчика кликабельны всегда.
func (s *Server) apiOpen(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL string `json:"url"`
	}
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
		return
	}
	u := strings.TrimSpace(req.URL)
	if !(strings.HasPrefix(u, "mailto:") || strings.HasPrefix(u, "tel:")) {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "разрешены только ссылки mailto: и tel:"})
		return
	}
	if err := platform.OpenExternal(u); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
}

func readJSON(r *http.Request, into interface{}) error {
	if r.Body == nil {
		return errors.New("пустое тело запроса")
	}
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, 2*1024*1024))
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return nil
	}
	return json.Unmarshal(body, into)
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;")
	return r.Replace(s)
}
