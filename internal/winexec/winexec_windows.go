// Package winexec запускает внешние команды Windows так, чтобы они не
// мелькали консольным окном. Программа собирается с флагом -H=windowsgui,
// поэтому любой дочерний консольный процесс по умолчанию создал бы видимое
// окно — здесь оно подавляется.
package winexec

import (
	"os/exec"
	"syscall"
)

// createNoWindow — флаг CREATE_NO_WINDOW из winbase.h.
const createNoWindow = 0x08000000

// Command создаёт команду, скрытую от пользователя.
func Command(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
	}
	return cmd
}
