//go:build !windows

// Package winexec запускает внешние команды. Вне Windows подавлять
// консольное окно не нужно, поэтому это обычная обёртка exec.Command.
package winexec

import "os/exec"

// Command создаёт команду.
func Command(name string, args ...string) *exec.Cmd {
	return exec.Command(name, args...)
}
