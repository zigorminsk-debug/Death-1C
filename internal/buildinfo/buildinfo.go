// Package buildinfo хранит сведения о сборке и о разработчике.
//
// Часть переменных подставляется линковщиком при сборке в GitHub Actions:
//
//	go build -ldflags "-X github.com/zigorminsk-debug/death-1c/internal/buildinfo.Version=1.0.7 \
//	                   -X github.com/zigorminsk-debug/death-1c/internal/buildinfo.Build=7 ..."
package buildinfo

import (
	"fmt"
	"runtime"
	"strings"
)

// Подставляемые при сборке значения. Значения по умолчанию используются
// только при локальном запуске «go run».
var (
	// Version — маркетинговая версия продукта, например 1.0.7.
	Version = "1.0.0"
	// Build — порядковый номер сборки (github.run_number из Actions).
	Build = "0"
	// Commit — короткий хеш коммита git.
	Commit = "dev"
	// Date — дата сборки в формате RFC3339.
	Date = "unknown"
	// Channel — канал сборки: main, beta, dev.
	Channel = "dev"
)

// Сведения о разработчике. Контакты обязаны быть кликабельными в интерфейсе,
// поэтому рядом с «человеческим» значением лежит готовая ссылка.
const (
	ProductName    = "Очистка сеансов 1С"
	ProductShort   = "Session Cleaner for 1C"
	ProductExe     = "SessionCleaner.exe"
	Company        = "Zakharevich Igor"
	DeveloperName  = "Zakharevich Igor"
	DeveloperEmail = "ziv@csl.by"
	DeveloperPhone = "+375293371412"
	Copyright      = "© Zakharevich Igor. Все права защищены."
	LicenseName    = "MIT License"

	EmailLink = "mailto:" + DeveloperEmail
	// Телефон в формате E.164 без пробелов и скобок — именно так его понимает ссылка tel:.
	PhoneLink = "tel:" + DeveloperPhone
	// Телефон «для человека» — с привычным форматированием.
	PhonePretty = "+375 (29) 337-14-12"
)

// Number возвращает порядковый номер сборки как число.
// Если номер не удалось разобрать, возвращается 0.
func Number() int {
	var n int
	for _, r := range Build {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// FullVersion — «1.0.7 (сборка 7)».
func FullVersion() string {
	if Number() > 0 {
		return fmt.Sprintf("%s (сборка %d)", Version, Number())
	}
	return Version
}

// BuildLabel — «Сборка №7».
func BuildLabel() string {
	if Number() > 0 {
		return fmt.Sprintf("Сборка №%d", Number())
	}
	return "Сборка не присвоена (локальный запуск)"
}

// Title — заголовок главного окна.
func Title() string {
	return fmt.Sprintf("%s — %s", ProductName, BuildLabel())
}

// OneLine — короткая строка для лога и для окна «О программе».
func OneLine() string {
	return strings.Join([]string{
		ProductName + " " + FullVersion(),
		"канал: " + Channel,
		"коммит: " + Commit,
		"дата сборки: " + Date,
		"go: " + runtime.Version() + " " + runtime.GOOS + "/" + runtime.GOARCH,
	}, " | ")
}

// Contacts — контакты разработчика в виде «метка → ссылка».
// Порядок сохраняется, чтобы интерфейс показывал их одинаково.
func Contacts() []Contact {
	return []Contact{
		{Kind: "name", Label: DeveloperName, Value: DeveloperName, Link: ""},
		{Kind: "email", Label: "Электронная почта", Value: DeveloperEmail, Link: EmailLink},
		{Kind: "phone", Label: "Телефон", Value: PhonePretty, Link: PhoneLink},
	}
}

// Contact — одна строка контакта. Link пустой, если контакт не кликабельный.
type Contact struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
	Value string `json:"value"`
	Link  string `json:"link"`
}
