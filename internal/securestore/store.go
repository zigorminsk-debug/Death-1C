// Package securestore хранит настройки программы в зашифрованном виде.
//
// Секреты (пароли администратора Windows и администратора кластера 1С)
// шифруются AES-256-GCM. Ключ AES, в свою очередь, защищается штатным
// механизмом операционной системы: в Windows — DPAPI в режиме
// CRYPTPROTECT_LOCAL_MACHINE, то есть расшифровать файл можно только
// на том же компьютере. Каталог с настройками дополнительно получает
// жёсткий ACL: доступ только у «Администраторы» и SYSTEM.
//
// Прочитать сохранённые пароли через интерфейс программы нельзя:
// слой config.Masked() вырезает их до передачи данных в GUI.
package securestore

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/zigorminsk-debug/death-1c/internal/config"
)

// AppFolder — имя каталога с данными программы.
const AppFolder = "1C Session Cleaner"

// FileName — имя файла с зашифрованными настройками.
const FileName = "settings.dat"

// KeyFileName — имя файла с защищённым ключом (используется как резерв,
// основной путь — единый файл настроек).
const KeyFileName = "master.key"

var fileMagic = [4]byte{'1', 'C', 'S', 'C'}

const formatVersion byte = 1

// ErrNotFound — файл настроек отсутствует.
var ErrNotFound = errors.New("настройки ещё не сохранены")

// ErrTampered — файл настроек повреждён или изменён.
var ErrTampered = errors.New("файл настроек повреждён или подменён")

// Store — хранилище настроек на диске.
type Store struct {
	mu   sync.Mutex
	path string
}

// New создаёт хранилище для указанного файла.
func New(path string) *Store { return &Store{path: path} }

// DefaultDir возвращает каталог для данных программы.
// Windows: %ProgramData%\1C Session Cleaner — настройки общие для всех
// администраторов сервера и не зависят от профиля пользователя.
func DefaultDir() string {
	if base := os.Getenv("ProgramData"); base != "" {
		return filepath.Join(base, AppFolder)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".local", "share", AppFolder)
	}
	return filepath.Join(os.TempDir(), AppFolder)
}

// Default открывает хранилище в стандартном каталоге.
func Default() *Store { return New(filepath.Join(DefaultDir(), FileName)) }

// Path — полный путь к файлу настроек.
func (s *Store) Path() string { return s.path }

// Exists сообщает, есть ли сохранённые настройки.
func (s *Store) Exists() bool {
	_, err := os.Stat(s.path)
	return err == nil
}

// Load читает и расшифровывает настройки.
// Если файла нет, возвращаются значения по умолчанию и ErrNotFound.
func (s *Store) Load() (config.Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return config.Default(), ErrNotFound
		}
		return config.Default(), err
	}
	plain, err := decryptFile(raw)
	if err != nil {
		return config.Default(), err
	}
	cfg, err := config.ParseJSON(plain)
	if err != nil {
		return config.Default(), fmt.Errorf("не удалось разобрать сохранённые настройки: %w", err)
	}
	cfg.Normalize()
	return cfg, nil
}

// Save шифрует и атомарно записывает настройки.
func (s *Store) Save(cfg config.Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cfg.Normalize()
	plain, err := config.ToJSON(cfg)
	if err != nil {
		return err
	}
	raw, err := encryptFile(plain)
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("не удалось создать каталог %s: %w", dir, err)
	}
	ProtectDir(dir)

	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	ProtectDir(dir)
	return nil
}

// Wipe удаляет сохранённые настройки вместе с ключом.
func (s *Store) Wipe() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := os.Remove(s.path)
	_ = os.Remove(filepath.Join(filepath.Dir(s.path), KeyFileName))
	_ = wipeKeyCache()
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// encryptFile готовит содержимое файла:
// magic(4) | version(1) | keyLen(4) | keyBlob | nonce(12) | ciphertext+tag.
func encryptFile(plain []byte) ([]byte, error) {
	key, blob, err := loadOrCreateKey()
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	sealed := gcm.Seal(nil, nonce, plain, aad())

	out := make([]byte, 0, len(blob)+len(sealed)+32)
	out = append(out, fileMagic[:]...)
	out = append(out, formatVersion)
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(blob)))
	out = append(out, lenBuf[:]...)
	out = append(out, blob...)
	out = append(out, nonce...)
	out = append(out, sealed...)
	return out, nil
}

func decryptFile(raw []byte) ([]byte, error) {
	if len(raw) < 4+1+4+12+16 {
		return nil, ErrTampered
	}
	if raw[0] != fileMagic[0] || raw[1] != fileMagic[1] || raw[2] != fileMagic[2] || raw[3] != fileMagic[3] {
		return nil, ErrTampered
	}
	if raw[4] != formatVersion {
		return nil, fmt.Errorf("неподдерживаемая версия формата настроек: %d", raw[4])
	}
	keyLen := binary.BigEndian.Uint32(raw[5:9])
	if keyLen == 0 || int(keyLen) > len(raw)-9 {
		return nil, ErrTampered
	}
	off := 9
	blob := raw[off : off+int(keyLen)]
	off += int(keyLen)
	if off+12 > len(raw) {
		return nil, ErrTampered
	}
	nonce := raw[off : off+12]
	off += 12
	sealed := raw[off:]

	key, err := unprotectKey(blob)
	if err != nil {
		return nil, fmt.Errorf("не удалось расшифровать ключ настроек: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plain, err := gcm.Open(nil, nonce, sealed, aad())
	if err != nil {
		return nil, ErrTampered
	}
	return plain, nil
}

// aad — дополнительно аутентифицируемые данные: привязывают файл к программе.
func aad() []byte {
	h := sha256.Sum256([]byte("death-1c/session-cleaner/settings/v1"))
	return h[:]
}

// keyCache хранит ключ в памяти процесса, чтобы не расшифровывать его
// на каждое сохранение.
var (
	keyMu    sync.Mutex
	keyCache []byte
)

func wipeKeyCache() error {
	keyMu.Lock()
	defer keyMu.Unlock()
	for i := range keyCache {
		keyCache[i] = 0
	}
	keyCache = nil
	return nil
}

// loadOrCreateKey возвращает действующий ключ AES и его защищённое представление.
func loadOrCreateKey() ([]byte, []byte, error) {
	keyMu.Lock()
	defer keyMu.Unlock()
	if len(keyCache) == 32 {
		blob, err := protectKey(keyCache)
		if err != nil {
			return nil, nil, err
		}
		return keyCache, blob, nil
	}
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, nil, err
	}
	blob, err := protectKey(key)
	if err != nil {
		return nil, nil, err
	}
	keyCache = key
	return key, blob, nil
}

func unprotectKey(blob []byte) ([]byte, error) {
	keyMu.Lock()
	if len(keyCache) == 32 {
		k := keyCache
		keyMu.Unlock()
		return k, nil
	}
	keyMu.Unlock()
	key, err := unprotectKeyPlatform(blob)
	if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, ErrTampered
	}
	keyMu.Lock()
	keyCache = append([]byte(nil), key...)
	keyMu.Unlock()
	return key, nil
}
