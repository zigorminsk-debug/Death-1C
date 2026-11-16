//go:build windows

// Package platform содержит платформенно-зависимые операции:
// проверку прав администратора, определение удерживаемого Shift,
// открытие окна интерфейса и запуск единственного экземпляра программы.
package platform

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/zigorminsk-debug/death-1c/internal/winexec"
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	procGetAsyncKeyState = user32.NewProc("GetAsyncKeyState")
	procShellExecute     = shell32.NewProc("ShellExecuteW")
	procCreateMutex      = kernel32.NewProc("CreateMutexW")
	procCloseHandle      = kernel32.NewProc("CloseHandle")
	procMessageBox       = user32.NewProc("MessageBoxW")
	procAttachConsole    = kernel32.NewProc("AttachConsole")
	procGetStdHandle     = kernel32.NewProc("GetStdHandle")
)

// Виртуальные клавиши Shift.
const (
	vkShift  = 0x10
	vkLShift = 0xA0
	vkRShift = 0xA1
)

const (
	swShowNormal   = 1
	createNoWindow = 0x08000000
)

// tokenElevation — ответ GetTokenInformation для класса TokenElevation.
type tokenElevation struct {
	TokenIsElevated int32
}

// IsElevated сообщает, запущен ли процесс с правами администратора.
// Стандартная библиотека syscall не имеет готового Token.IsElevated,
// поэтому запрашиваем TOKEN_INFORMATION_CLASS = TokenElevation (20) вручную.
func IsElevated() bool {
	token, err := syscall.OpenCurrentProcessToken()
	if err != nil {
		return false
	}
	defer token.Close()
	var elev tokenElevation
	var retLen uint32
	err = syscall.GetTokenInformation(token, syscall.TokenElevation,
		(*byte)(unsafe.Pointer(&elev)), uint32(unsafe.Sizeof(elev)), &retLen)
	return err == nil && elev.TokenIsElevated != 0
}

// ShiftPressed сообщает, удерживается ли Shift в момент вызова.
// Именно так программа отличает обычный запуск от запуска с настройками.
func ShiftPressed() bool {
	for _, vk := range []int{vkShift, vkLShift, vkRShift} {
		r, _, _ := procGetAsyncKeyState.Call(uintptr(vk))
		if r&0x8000 != 0 {
			return true
		}
	}
	return false
}

// ShiftPressedWithin опрашивает Shift в течение заданного промежутка.
// Небольшое окно опроса страхует от ситуации, когда пользователь нажал Shift
// на долю секунды позже двойного щелчка по ярлыку.
func ShiftPressedWithin(d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		if ShiftPressed() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(40 * time.Millisecond)
	}
}

// RelaunchElevated перезапускает программу с запросом прав администратора (UAC).
// Дополнительные аргументы добавляются к текущей командной строке: так в
// повышенный процесс передаётся признак «открыть настройки» (Shift при запуске).
func RelaunchElevated(extra ...string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	all := append(append([]string{}, os.Args[1:]...), extra...)
	args := strings.Join(all, " ")
	verb, _ := syscall.UTF16PtrFromString("runas")
	file, _ := syscall.UTF16PtrFromString(exe)
	params, _ := syscall.UTF16PtrFromString(args)
	dir, _ := syscall.UTF16PtrFromString(filepath.Dir(exe))
	r, _, callErr := procShellExecute.Call(0, uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(file)), uintptr(unsafe.Pointer(params)),
		uintptr(unsafe.Pointer(dir)), uintptr(swShowNormal))
	if r <= 32 {
		return fmt.Errorf("не удалось запустить программу от имени администратора: %w", callErr)
	}
	return nil
}

// SingleInstance занимает именованный мьютекс.
// Возвращает функцию освобождения и признак «это первый экземпляр».
func SingleInstance(key string) (func(), bool) {
	name, err := syscall.UTF16PtrFromString("Global\\" + key)
	if err != nil {
		return func() {}, true
	}
	h, _, callErr := procCreateMutex.Call(0, 1, uintptr(unsafe.Pointer(name)))
	if h == 0 {
		return func() {}, true
	}
	if errno, ok := callErr.(syscall.Errno); ok && errno == syscall.ERROR_ALREADY_EXISTS {
		// Программа уже запущена: мьютекс чужой, закрывать его не нужно.
		return func() {}, false
	}
	release := func() {
		_, _, _ = procCloseHandle.Call(h)
	}
	return release, true
}

// OpenWindow показывает интерфейс пользователю.
//
// mode = "app" — отдельное окно без адресной строки (Edge или Chrome в режиме
// приложения): выглядит как обычная программа. Если ни тот, ни другой не найден,
// адрес открывается в браузере по умолчанию.
// mode = "browser" — сразу вкладка браузера по умолчанию.
func OpenWindow(url, mode string) error {
	if mode == "app" {
		if browser := findAppBrowser(); browser != "" {
			cmd := exec.Command(browser,
				"--app="+url,
				"--user-data-dir="+browserProfileDir(),
				"--no-first-run",
				"--no-default-browser-check",
				"--disable-features=Translate",
				"--window-size=1440,920",
			)
			cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
			if err := cmd.Start(); err == nil {
				return nil
			}
		}
	}
	// Запасной путь — браузер по умолчанию.
	cmd := winexec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("не удалось открыть окно интерфейса: %w", err)
	}
	return nil
}

// OpenExternal открывает внешнюю ссылку (mailto:, tel:) в системном обработчике.
func OpenExternal(url string) error {
	cmd := winexec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	if err := cmd.Start(); err != nil {
		return err
	}
	return nil
}

func browserProfileDir() string {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "1C Session Cleaner", "browser-profile")
}

func findAppBrowser() string {
	pf := os.Getenv("ProgramFiles")
	if pf == "" {
		pf = `C:\Program Files`
	}
	pfx86 := os.Getenv("ProgramFiles(x86)")
	if pfx86 == "" {
		pfx86 = `C:\Program Files (x86)`
	}
	local := os.Getenv("LOCALAPPDATA")
	candidates := []string{}
	if local != "" {
		candidates = append(candidates,
			filepath.Join(local, `Microsoft\Edge\Application\msedge.exe`),
			filepath.Join(local, `Google\Chrome\Application\chrome.exe`))
	}
	candidates = append(candidates,
		filepath.Join(pfx86, `Microsoft\Edge\Application\msedge.exe`),
		filepath.Join(pf, `Microsoft\Edge\Application\msedge.exe`),
		filepath.Join(pf, `Google\Chrome\Application\chrome.exe`),
		filepath.Join(pfx86, `Google\Chrome\Application\chrome.exe`),
	)
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	for _, name := range []string{"msedge.exe", "chrome.exe"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}

// CurrentUser — имя текущего пользователя Windows в виде «ДОМЕН\пользователь».
func CurrentUser() string {
	user := os.Getenv("USERNAME")
	domain := os.Getenv("USERDOMAIN")
	if user == "" {
		return ""
	}
	if domain != "" {
		return domain + `\` + user
	}
	return user
}

// Hostname — имя компьютера.
func Hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	return h
}

// DataDir — каталог общих данных программы (%ProgramData%).
func DataDir() string {
	if base := os.Getenv("ProgramData"); base != "" {
		return filepath.Join(base, "1C Session Cleaner")
	}
	return filepath.Join(os.TempDir(), "1C Session Cleaner")
}

// MessageBox показывает системное сообщение. Нужно до появления окна интерфейса:
// например, когда программа уже запущена или не удалось получить права администратора.
func MessageBox(title, text string) {
	t, err1 := syscall.UTF16PtrFromString(title)
	x, err2 := syscall.UTF16PtrFromString(text)
	if err1 != nil || err2 != nil {
		return
	}
	// MB_ICONWARNING (0x30) | MB_TOPMOST (0x40000)
	const flags = 0x40030
	_, _, _ = procMessageBox.Call(0, uintptr(unsafe.Pointer(x)), uintptr(unsafe.Pointer(t)), uintptr(flags))
}

// AttachConsole подключает процесс к консоли родителя и переподключает
// стандартные потоки. Используется в консольном режиме (-clean), когда
// программа запускается из планировщика заданий или из командной строки.
func AttachConsole() bool {
	r, _, _ := procAttachConsole.Call(^uintptr(0))
	if r == 0 {
		return false
	}
	if h, _, _ := procGetStdHandle.Call(stdOutputHandle); h != 0 && h != ^uintptr(0) {
		os.Stdout = os.NewFile(h, "stdout")
	}
	if h, _, _ := procGetStdHandle.Call(stdErrorHandle); h != 0 && h != ^uintptr(0) {
		os.Stderr = os.NewFile(h, "stderr")
	}
	return true
}

// STD_OUTPUT_HANDLE и STD_ERROR_HANDLE как беззнаковые DWORD:
// отрицательные константы в uintptr не помещаются.
const (
	stdOutputHandle = uintptr(0xFFFFFFF5) // -11
	stdErrorHandle  = uintptr(0xFFFFFFF6) // -12
)
