//go:build !windows

// Package platform — платформенно-зависимые операции.
//
// Этот файл используется при сборке и тестах вне Windows (например, в CI
// на ubuntu-latest). Боевой запуск программы выполняется только под Windows,
// где работает platform_windows.go.
package platform

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

// ErrWindowsOnly — операция доступна только в Windows.
var ErrWindowsOnly = errors.New("операция доступна только в Windows")

// IsElevated вне Windows проверяет root.
func IsElevated() bool { return os.Geteuid() == 0 }

// ShiftPressed вне Windows определить нельзя.
func ShiftPressed() bool { return false }

// ShiftPressedWithin вне Windows всегда возвращает false.
func ShiftPressedWithin(_ time.Duration) bool { return false }

// RelaunchElevated вне Windows не поддерживается.
func RelaunchElevated(_ ...string) error { return ErrWindowsOnly }

// SingleInstance вне Windows использует файл-замок в каталоге данных.
func SingleInstance(key string) (func(), bool) {
	path := filepath.Join(DataDir(), key+".lock")
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return func() {}, false
	}
	_, _ = f.WriteString(strconv.Itoa(os.Getpid()))
	return func() {
		_ = f.Close()
		_ = os.Remove(path)
	}, true
}

// OpenWindow вне Windows открывает адрес в браузере по умолчанию.
func OpenWindow(url, _ string) error { return openWithSystem(url) }

// OpenExternal открывает внешнюю ссылку системным обработчиком.
func OpenExternal(url string) error { return openWithSystem(url) }

func openWithSystem(url string) error {
	for _, name := range []string{"xdg-open", "open"} {
		if p, err := exec.LookPath(name); err == nil {
			return exec.Command(p, url).Start()
		}
	}
	return ErrWindowsOnly
}

// CurrentUser — имя текущего пользователя.
func CurrentUser() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	return os.Getenv("USERNAME")
}

// Hostname — имя компьютера.
func Hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	return h
}

// DataDir — каталог данных программы.
func DataDir() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".local", "share", "1C Session Cleaner")
	}
	return filepath.Join(os.TempDir(), "1C Session Cleaner")
}

// MessageBox вне Windows печатает сообщение в стандартный поток ошибок.
func MessageBox(title, text string) {
	_, _ = os.Stderr.WriteString(title + ": " + text + "\n")
}

// AttachConsole вне Windows не требуется: консоль уже есть.
func AttachConsole() bool { return true }
