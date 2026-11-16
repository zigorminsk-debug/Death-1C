package securestore

import (
	"crypto/sha256"
	"fmt"
	"syscall"
	"unsafe"

	"github.com/zigorminsk-debug/death-1c/internal/winexec"
)

var (
	crypt32  = syscall.NewLazyDLL("crypt32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	procProtect   = crypt32.NewProc("CryptProtectData")
	procUnprotect = crypt32.NewProc("CryptUnprotectData")
	procLocalFree = kernel32.NewProc("LocalFree")
)

// Флаги DPAPI.
const (
	cryptProtectUIForbidden  = 0x1
	cryptProtectLocalMachine = 0x4
)

// dataBlob — структура DATA_BLOB из wincrypt.h.
type dataBlob struct {
	size uint32
	data *byte
}

func newBlob(b []byte) dataBlob {
	if len(b) == 0 {
		return dataBlob{}
	}
	return dataBlob{size: uint32(len(b)), data: &b[0]}
}

// entropy — дополнительная «соль» приложения. Она постоянна между запусками,
// поэтому чужой процесс, даже запущенный от администратора на этом же сервере,
// без знания состава энтропии и без прав на каталог настроек ключ не снимет.
func entropy() []byte {
	h := sha256.Sum256([]byte("death-1c/session-cleaner/dpapi-entropy/v1"))
	return h[:16]
}

// protectKey защищает ключ AES средствами DPAPI в режиме «только этот компьютер».
func protectKey(key []byte) ([]byte, error) { return dpapi(key, true) }

// unprotectKeyPlatform снимает защиту DPAPI.
func unprotectKeyPlatform(blob []byte) ([]byte, error) { return dpapi(blob, false) }

func dpapi(in []byte, protect bool) ([]byte, error) {
	if len(in) == 0 {
		return nil, fmt.Errorf("DPAPI: пустые данные")
	}
	ent := entropy()
	inBlob := newBlob(in)
	entBlob := newBlob(ent)
	var outBlob dataBlob

	proc := procUnprotect
	flags := uintptr(cryptProtectUIForbidden)
	if protect {
		proc = procProtect
		flags |= cryptProtectLocalMachine
	}

	r1, _, callErr := proc.Call(
		uintptr(unsafe.Pointer(&inBlob)),
		0,
		uintptr(unsafe.Pointer(&entBlob)),
		0,
		0,
		flags,
		uintptr(unsafe.Pointer(&outBlob)),
	)
	if r1 == 0 || outBlob.data == nil {
		return nil, fmt.Errorf("DPAPI: %w", callErr)
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(outBlob.data)))

	out := make([]byte, outBlob.size)
	if outBlob.size > 0 {
		copy(out, unsafe.Slice(outBlob.data, outBlob.size))
	}
	return out, nil
}

// ProtectDir ограничивает доступ к каталогу настроек: наследование снимается,
// права остаются только у «Администраторов» (S-1-5-32-544) и SYSTEM (S-1-5-18).
// Идентификаторы безопасности заданы числами, поэтому команда не зависит
// от языка установленной Windows. Ошибки игнорируются: это дополнительный
// рубеж защиты, а не обязательное условие работы.
func ProtectDir(dir string) {
	cmd := winexec.Command("icacls.exe", dir,
		"/inheritance:r",
		"/grant:r", "*S-1-5-32-544:(OI)(CI)F",
		"/grant:r", "*S-1-5-18:(OI)(CI)F",
	)
	_ = cmd.Run()
}
