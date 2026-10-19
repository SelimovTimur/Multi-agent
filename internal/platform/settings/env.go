// Package settings читает окружение поверх общих значений из settings.py.
package settings

import (
	"os"
	"strconv"
)

func String(name string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return Default(name)
}

func Int(name string) int {
	if value, err := strconv.Atoi(String(name)); err == nil {
		return value
	}
	value, _ := strconv.Atoi(Default(name))
	return value
}

// BoolOr сохраняет зависимые от APP_ENV значения при пустой настройке.
func BoolOr(name string, fallback bool) bool {
	if value, err := strconv.ParseBool(String(name)); err == nil {
		return value
	}
	return fallback
}
