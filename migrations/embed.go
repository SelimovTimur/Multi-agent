// Package migrations содержит SQL-миграции, вшитые в бинарник.
//
// Файлы называются NNNN_name.up.sql и NNNN_name.down.sql. У каждой миграции
// обязана быть обратная: без неё откат выкатки превращается в ручную работу
// на проде. Отсутствие пары ловится тестом.
//
// Применяются командой:
//
//	go run ./cmd/migrate up
//	go run ./cmd/migrate down 1
//	go run ./cmd/migrate status
package migrations

import "embed"

// FS — все файлы миграций.
//
//go:embed *.sql
var FS embed.FS
