//go:build !windows

package securestore

import (
	"crypto/sha256"
	"os"
)

// Режим только для разработки и для тестов в CI (Linux/macOS):
// в Windows ключ защищает DPAPI, здесь применяется обратимая маска.
// Настоящей защиты она не даёт — боевой запуск программы выполняется
// только под Windows, где работает protect_windows.go.

func maskPad() []byte {
	h := sha256.Sum256([]byte("death-1c/session-cleaner/dev-mask/v1"))
	return h[:]
}

func protectKey(key []byte) ([]byte, error) { return xorPad(key), nil }

func unprotectKeyPlatform(blob []byte) ([]byte, error) { return xorPad(blob), nil }

func xorPad(in []byte) []byte {
	pad := maskPad()
	out := make([]byte, len(in))
	for i := range in {
		out[i] = in[i] ^ pad[i%len(pad)]
	}
	return out
}

// ProtectDir выставляет права 0700 на каталог настроек.
func ProtectDir(dir string) {
	_ = os.Chmod(dir, 0o700)
}
