package pg

// Row повторяет pgx.Row. Отдельный тип нужен, чтобы репозитории модулей
// не импортировали pgx напрямую ради одной сигнатуры сканирования.
type Row interface {
	Scan(dest ...any) error
}
