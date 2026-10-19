package mlgw

import "github.com/jackc/pgx/v5"

// pgxIdentity — имя таблицы для COPY. Обёртка нужна, чтобы recorder.go не
// импортировал pgx ради одного типа.
type pgxIdentity = pgx.Identifier

// copyRows отдаёт заранее собранные строки в COPY.
//
// COPY, а не пачка INSERT: учёт пишется на каждый вызов модели, и на пиковой
// нагрузке это тысячи строк в минуту. COPY на порядок дешевле по круговым
// обходам сети.
func copyRows(rows [][]any) pgx.CopyFromSource {
	return pgx.CopyFromRows(rows)
}
