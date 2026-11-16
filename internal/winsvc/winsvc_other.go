//go:build !windows

// Package winsvc — управление службами Windows на сервере 1С.
//
// Вне Windows функции возвращают ошибку: управление службами доступно
// только в операционной системе, на которой работает сервер 1С.
// Файл нужен, чтобы пакет собирался и тестировался в CI на Linux.
package winsvc

import "errors"

// Service — служба Windows на сервере 1С.
type Service struct {
	Name    string `json:"name"`
	Display string `json:"display"`
	State   string `json:"state"`
	Running bool   `json:"running"`
	Kind    string `json:"kind"`
}

// ErrNotConfigured — учётные данные администратора Windows не заданы.
var ErrNotConfigured = errors.New("не заданы учётные данные администратора Windows-сервера")

// ErrWindowsOnly — операция доступна только в Windows.
var ErrWindowsOnly = errors.New("управление службами Windows доступно только в Windows")

// Manager — операции со службами удалённого сервера.
type Manager struct {
	host string
}

// New создаёт менеджер служб.
func New(host, user, password string, logf func(format string, args ...interface{})) *Manager {
	return &Manager{host: host}
}

// Host — адрес сервера.
func (m *Manager) Host() string { return m.host }

// Enabled — всегда false вне Windows.
func (m *Manager) Enabled() bool { return false }

// Connect не поддерживается.
func (m *Manager) Connect() error { return ErrWindowsOnly }

// Disconnect не поддерживается.
func (m *Manager) Disconnect() error { return nil }

// TestCredentials не поддерживается.
func (m *Manager) TestCredentials() error { return ErrWindowsOnly }

// List не поддерживается.
func (m *Manager) List() ([]Service, error) { return nil, ErrWindowsOnly }

// Start не поддерживается.
func (m *Manager) Start(name string) error { return ErrWindowsOnly }

// Stop не поддерживается.
func (m *Manager) Stop(name string) error { return ErrWindowsOnly }

// Restart не поддерживается.
func (m *Manager) Restart(name string) error { return ErrWindowsOnly }
