// Package logger ведёт журнал работы программы.
//
// Журнал пишется в текстовый файл рядом с настройками и одновременно
// держится в памяти, чтобы интерфейс мог показать последние события.
// Пароли и прочие секреты в журнал не попадают: сообщения формируются
// из уже обезличенных данных.
package logger

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Уровни сообщений.
const (
	LevelInfo   = "info"
	LevelWarn   = "warn"
	LevelError  = "error"
	LevelAction = "action"
)

// maxFileSize — размер файла журнала, после которого выполняется ротация.
const maxFileSize = 2 * 1024 * 1024

// Entry — одна запись журнала.
type Entry struct {
	Time    string `json:"time"`
	Unix    int64  `json:"unix"`
	Level   string `json:"level"`
	Message string `json:"message"`
}

// Logger — журнал с кольцевым буфером в памяти и файлом на диске.
type Logger struct {
	mu   sync.Mutex
	buf  []Entry
	keep int
	path string
	f    *os.File
}

// New открывает журнал. Пустой path отключает запись в файл.
func New(path string, keep int) *Logger {
	if keep <= 0 {
		keep = 500
	}
	l := &Logger{keep: keep, path: path}
	if path != "" {
		l.openLocked()
	}
	return l
}

func (l *Logger) openLocked() {
	if l.path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return
	}
	l.rotateLocked()
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return
	}
	l.f = f
}

func (l *Logger) rotateLocked() {
	st, err := os.Stat(l.path)
	if err != nil || st.Size() < maxFileSize {
		return
	}
	_ = os.Rename(l.path, l.path+".1")
}

// Path — путь к файлу журнала.
func (l *Logger) Path() string { return l.path }

// Close закрывает файл журнала.
func (l *Logger) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f != nil {
		_ = l.f.Close()
		l.f = nil
	}
}

// Info пишет информационное сообщение.
func (l *Logger) Info(format string, args ...interface{}) { l.add(LevelInfo, format, args...) }

// Infof — синоним Info; удобно передавать как функцию в другие пакеты.
func (l *Logger) Infof(format string, args ...interface{}) { l.add(LevelInfo, format, args...) }

// Warn пишет предупреждение.
func (l *Logger) Warn(format string, args ...interface{}) { l.add(LevelWarn, format, args...) }

// Errorf пишет ошибку.
func (l *Logger) Errorf(format string, args ...interface{}) { l.add(LevelError, format, args...) }

// Action пишет значимое действие (например, завершение сеанса).
func (l *Logger) Action(format string, args ...interface{}) { l.add(LevelAction, format, args...) }

func (l *Logger) add(level, format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	msg = sanitize(msg)
	now := time.Now()
	e := Entry{
		Time:    now.Format("2006-01-02 15:04:05"),
		Unix:    now.Unix(),
		Level:   level,
		Message: msg,
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf = append(l.buf, e)
	if len(l.buf) > l.keep {
		l.buf = l.buf[len(l.buf)-l.keep:]
	}
	if l.f != nil {
		fmt.Fprintf(l.f, "%s [%s] %s\n", e.Time, strings.ToUpper(level), msg)
	}
}

// sanitize подстраховывается от случайной записи секрета в журнал.
func sanitize(msg string) string {
	msg = strings.ReplaceAll(msg, "\r", " ")
	msg = strings.ReplaceAll(msg, "\n", " | ")
	return msg
}

// Recent возвращает последние n записей журнала, от старых к новым.
func (l *Logger) Recent(n int) []Entry {
	l.mu.Lock()
	defer l.mu.Unlock()
	if n <= 0 || n > len(l.buf) {
		n = len(l.buf)
	}
	out := make([]Entry, n)
	copy(out, l.buf[len(l.buf)-n:])
	return out
}

// Text возвращает журнал в виде текста — для сохранения или отправки разработчику.
func (l *Logger) Text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var b strings.Builder
	for _, e := range l.buf {
		fmt.Fprintf(&b, "%s [%s] %s\n", e.Time, strings.ToUpper(e.Level), e.Message)
	}
	return b.String()
}
