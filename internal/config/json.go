// Package json содержит сериализацию настроек. Вынесено отдельно,
// чтобы пакет config не тянул encoding/json во все импортирующие пакеты
// и чтобы формат файла настроек был описан в одном месте.
package config

import "encoding/json"

// ToJSON сериализует настройки в форматированный JSON.
func ToJSON(c Config) ([]byte, error) {
	return json.MarshalIndent(c, "", "  ")
}

// ParseJSON разбирает JSON с настройками.
func ParseJSON(data []byte) (Config, error) {
	cfg := Default()
	if len(data) == 0 {
		return cfg, nil
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Default(), err
	}
	cfg.Normalize()
	return cfg, nil
}
