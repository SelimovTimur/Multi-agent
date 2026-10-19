// Package idgen выдаёт идентификаторы.
//
// UUID v7 вместо v4: он монотонно растёт по времени, поэтому вставки ложатся
// в конец B-tree индекса, а не размазываются по нему. На таблицах событий это
// разница между последовательной записью и постоянным расщеплением страниц.
package idgen

import "github.com/google/uuid"

// New возвращает UUID v7. При отказе генератора падает обратно на v4:
// идентификатор нужен всегда, потеря монотонности — не повод ронять запрос.
func New() uuid.UUID {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.New()
	}
	return id
}

// NewString — то же самое строкой.
func NewString() string { return New().String() }
