package content

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/NATIVE/NATIVE/internal/platform/pg"
	"github.com/google/uuid"
)

// ErrNotFound — объект не найден. Сервис превращает его в 404.
var ErrNotFound = errors.New("content: не найдено")

// Store — доступ к схеме content.
type Store struct{}

func NewStore() *Store { return &Store{} }

// ---------------------------------------------------------------------------
// Уроки
// ---------------------------------------------------------------------------

const lessonColumns = `id, org_id, author_id, title, lang_code, level, status,
	source_kind, source_material_id, created_at, updated_at`

func scanLesson(row pg.Row) (Lesson, error) {
	var l Lesson
	err := row.Scan(&l.ID, &l.OrgID, &l.AuthorID, &l.Title, &l.LangCode, &l.Level,
		&l.Status, &l.SourceKind, &l.SourceMaterialID, &l.CreatedAt, &l.UpdatedAt)
	return l, err
}

func (s *Store) CreateLesson(ctx context.Context, q pg.Querier, l Lesson) (Lesson, error) {
	row := q.QueryRow(ctx, `
		INSERT INTO content.lessons
			(id, org_id, author_id, title, lang_code, level, status, source_kind,
			 source_material_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING `+lessonColumns,
		l.ID, l.OrgID, l.AuthorID, l.Title, l.LangCode, l.Level, l.Status, l.SourceKind,
		l.SourceMaterialID)
	out, err := scanLesson(row)
	if err != nil {
		return Lesson{}, fmt.Errorf("content: не удалось создать урок: %w", err)
	}
	return out, nil
}

func (s *Store) LessonByID(ctx context.Context, q pg.Querier, orgID, id uuid.UUID) (Lesson, error) {
	l, err := scanLesson(q.QueryRow(ctx, `
		SELECT `+lessonColumns+` FROM content.lessons
		WHERE id = $1 AND org_id = $2 AND deleted_at IS NULL`, id, orgID))
	if pg.IsNoRows(err) {
		return Lesson{}, ErrNotFound
	}
	if err != nil {
		return Lesson{}, fmt.Errorf("content: не удалось прочитать урок: %w", err)
	}
	return l, nil
}

// LessonFilter — отбор для библиотеки.
type LessonFilter struct {
	Level    string
	LangCode string
	Status   string
	Query    string
	Limit    int
	Offset   int
}

// ListLessons отдаёт уроки автора с фильтрами библиотеки.
//
// Пустой фильтр означает «любое»: строить для каждого сочетания отдельный
// запрос — верный способ забыть про org_id в одном из них.
// LessonTitles — названия уроков пачкой, одним запросом.
func (s *Store) LessonTitles(ctx context.Context, q pg.Querier, orgID uuid.UUID,
	ids []uuid.UUID) (map[uuid.UUID]string, error) {
	out := make(map[uuid.UUID]string, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `
		SELECT id, title FROM content.lessons
		WHERE org_id = $1 AND id = ANY($2)`, orgID, ids)
	if err != nil {
		return nil, fmt.Errorf("content: не удалось прочитать названия уроков: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id uuid.UUID
		var title string
		if err := rows.Scan(&id, &title); err != nil {
			return nil, fmt.Errorf("content: не удалось прочитать названия уроков: %w", err)
		}
		out[id] = title
	}
	return out, rows.Err()
}

func (s *Store) ListLessons(ctx context.Context, q pg.Querier, orgID, authorID uuid.UUID,
	f LessonFilter) ([]Lesson, error) {
	rows, err := q.Query(ctx, `
		SELECT `+lessonColumns+`
		FROM content.lessons
		WHERE org_id = $1 AND author_id = $2 AND deleted_at IS NULL
		  AND ($3 = '' OR level = $3)
		  AND ($4 = '' OR lang_code = $4)
		  AND ($5 = '' OR status = $5)
		  AND ($6 = '' OR title ILIKE '%' || $6 || '%')
		ORDER BY created_at DESC
		LIMIT $7 OFFSET $8`,
		orgID, authorID, f.Level, f.LangCode, f.Status, f.Query, f.Limit, f.Offset)
	if err != nil {
		return nil, fmt.Errorf("content: не удалось прочитать библиотеку: %w", err)
	}
	defer rows.Close()

	var out []Lesson
	for rows.Next() {
		l, err := scanLesson(rows)
		if err != nil {
			return nil, fmt.Errorf("content: не удалось прочитать библиотеку: %w", err)
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *Store) UpdateLessonMeta(ctx context.Context, q pg.Querier, orgID, id uuid.UUID,
	title, level string) error {
	tag, err := q.Exec(ctx, `
		UPDATE content.lessons SET title = $3, level = $4
		WHERE id = $1 AND org_id = $2 AND deleted_at IS NULL`, id, orgID, title, level)
	if err != nil {
		return fmt.Errorf("content: не удалось обновить урок: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) SetLessonStatus(ctx context.Context, q pg.Querier, orgID, id uuid.UUID, status string) error {
	tag, err := q.Exec(ctx, `
		UPDATE content.lessons SET status = $3
		WHERE id = $1 AND org_id = $2 AND deleted_at IS NULL`, id, orgID, status)
	if err != nil {
		return fmt.Errorf("content: не удалось изменить статус урока: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SoftDeleteLesson помечает урок удалённым.
//
// Мягко, а не DELETE: у версий могут быть ответы учеников через назначения,
// и жёсткое удаление превратило бы их в сироты.
func (s *Store) SoftDeleteLesson(ctx context.Context, q pg.Querier, orgID, id uuid.UUID) error {
	tag, err := q.Exec(ctx, `
		UPDATE content.lessons SET deleted_at = now()
		WHERE id = $1 AND org_id = $2 AND deleted_at IS NULL`, id, orgID)
	if err != nil {
		return fmt.Errorf("content: не удалось удалить урок: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------------------------------------------------------------------------
// Версии
// ---------------------------------------------------------------------------

const versionColumns = `id, org_id, lesson_id, version, status, title, level,
	refined_text, frank_text, grammar_raw, note, published_at, created_by,
	created_at, updated_at, quality_warnings`

func scanVersion(row pg.Row) (LessonVersion, error) {
	var v LessonVersion
	err := row.Scan(&v.ID, &v.OrgID, &v.LessonID, &v.Version, &v.Status, &v.Title,
		&v.Level, &v.RefinedText, &v.FrankText, &v.GrammarRaw, &v.Note,
		&v.PublishedAt, &v.CreatedBy, &v.CreatedAt, &v.UpdatedAt, &v.QualityWarnings)
	return v, err
}

// CreateVersion добавляет версию с номером «следующий после максимального».
//
// Вызывающий держит LockLesson до конца транзакции: MAX(version)+1 без
// блокировки мог бы выдать один номер двум параллельным созданиям.
func (s *Store) CreateVersion(ctx context.Context, q pg.Querier, v LessonVersion) (LessonVersion, error) {
	if len(v.QualityWarnings) == 0 {
		v.QualityWarnings = json.RawMessage("[]")
	}
	row := q.QueryRow(ctx, `
		INSERT INTO content.lesson_versions
			(id, org_id, lesson_id, version, status, title, level,
			 refined_text, frank_text, grammar_raw, note, created_by, quality_warnings)
		SELECT $1, $2, $3,
		       COALESCE(MAX(version), 0) + 1,
		       $4, $5, $6, $7, $8, $9, $10, $11, $12
		FROM content.lesson_versions WHERE lesson_id = $3 AND org_id = $2
		RETURNING `+versionColumns,
		v.ID, v.OrgID, v.LessonID, v.Status, v.Title, v.Level,
		v.RefinedText, v.FrankText, v.GrammarRaw, v.Note, v.CreatedBy, v.QualityWarnings)
	out, err := scanVersion(row)
	if err != nil {
		return LessonVersion{}, fmt.Errorf("content: не удалось создать версию: %w", err)
	}
	return out, nil
}

func (s *Store) VersionByID(ctx context.Context, q pg.Querier, orgID, id uuid.UUID) (LessonVersion, error) {
	v, err := scanVersion(q.QueryRow(ctx,
		`SELECT `+versionColumns+` FROM content.lesson_versions WHERE id = $1 AND org_id = $2`, id, orgID))
	if pg.IsNoRows(err) {
		return LessonVersion{}, ErrNotFound
	}
	if err != nil {
		return LessonVersion{}, fmt.Errorf("content: не удалось прочитать версию: %w", err)
	}
	return v, nil
}

// LockLesson сериализует создание версий одного урока.
func (s *Store) LockLesson(ctx context.Context, q pg.Querier, orgID, id uuid.UUID) error {
	var found uuid.UUID
	err := q.QueryRow(ctx, `SELECT id FROM content.lessons
		WHERE id = $1 AND org_id = $2 AND deleted_at IS NULL FOR UPDATE`, id, orgID).Scan(&found)
	if pg.IsNoRows(err) {
		return ErrNotFound
	}
	return err
}

// LockVersion соединяет проверку статуса и изменения содержимого в одну
// критическую секцию; q должен быть транзакцией.
func (s *Store) LockVersion(ctx context.Context, q pg.Querier, orgID, id uuid.UUID) (LessonVersion, error) {
	v, err := scanVersion(q.QueryRow(ctx, `SELECT `+versionColumns+`
		FROM content.lesson_versions WHERE id = $1 AND org_id = $2 FOR UPDATE`, id, orgID))
	if pg.IsNoRows(err) {
		return LessonVersion{}, ErrNotFound
	}
	return v, err
}

// VersionsByIDs — версии пачкой. Нужен спискам назначений: без него
// вкладка «Уроки» превращается в N+1.
func (s *Store) VersionsByIDs(ctx context.Context, q pg.Querier, orgID uuid.UUID,
	ids []uuid.UUID) (map[uuid.UUID]LessonVersion, error) {
	out := make(map[uuid.UUID]LessonVersion, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx,
		`SELECT `+versionColumns+` FROM content.lesson_versions WHERE org_id = $1 AND id = ANY($2)`,
		orgID, ids)
	if err != nil {
		return nil, fmt.Errorf("content: не удалось прочитать версии: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		v, err := scanVersion(rows)
		if err != nil {
			return nil, fmt.Errorf("content: не удалось прочитать версии: %w", err)
		}
		out[v.ID] = v
	}
	return out, rows.Err()
}

// LatestVersions — последняя неархивная версия каждого урока, одним запросом.
//
// DISTINCT ON, а не запрос на урок: в библиотеке полсотни строк, и номер
// версии на каждую превратил бы её открытие в N+1.
//
// Архивные версии пропускаем: номер в библиотеке должен совпадать с тем, что
// учитель может назначить, а архивную назначить нельзя. Урок, у которого все
// версии в архиве, в карту не попадает вовсе — вызывающий увидит нулевой
// номер, и это честно: показывать нечего.
func (s *Store) LatestVersions(ctx context.Context, q pg.Querier, orgID uuid.UUID,
	lessonIDs []uuid.UUID) (map[uuid.UUID]VersionMark, error) {
	out := make(map[uuid.UUID]VersionMark, len(lessonIDs))
	if len(lessonIDs) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `
		SELECT DISTINCT ON (lesson_id) lesson_id, version, status
		FROM content.lesson_versions
		WHERE org_id = $1 AND lesson_id = ANY($2) AND status <> $3
		ORDER BY lesson_id, version DESC`, orgID, lessonIDs, VersionArchived)
	if err != nil {
		return nil, fmt.Errorf("content: не удалось прочитать версии уроков: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var lessonID uuid.UUID
		var m VersionMark
		if err := rows.Scan(&lessonID, &m.Version, &m.Status); err != nil {
			return nil, fmt.Errorf("content: не удалось прочитать версии уроков: %w", err)
		}
		out[lessonID] = m
	}
	return out, rows.Err()
}

// PublishedVersions — опубликованная версия каждого урока, пачкой.
//
// Библиотека и карта урока показывают статус: без этой выборки они брали
// его у самой свежей версии и писали «черновик» об уроке, который прямо
// сейчас проходит класс.
func (s *Store) PublishedVersions(ctx context.Context, q pg.Querier, orgID uuid.UUID,
	lessonIDs []uuid.UUID) (map[uuid.UUID]VersionMark, error) {
	out := make(map[uuid.UUID]VersionMark, len(lessonIDs))
	if len(lessonIDs) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `
		SELECT DISTINCT ON (lesson_id) lesson_id, version, status
		FROM content.lesson_versions
		WHERE org_id = $1 AND lesson_id = ANY($2) AND status = 'published'
		ORDER BY lesson_id, version DESC`, orgID, lessonIDs)
	if err != nil {
		return nil, fmt.Errorf("content: не удалось прочитать версии уроков: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var lessonID uuid.UUID
		var m VersionMark
		if err := rows.Scan(&lessonID, &m.Version, &m.Status); err != nil {
			return nil, fmt.Errorf("content: не удалось прочитать версии уроков: %w", err)
		}
		out[lessonID] = m
	}
	return out, rows.Err()
}

func (s *Store) ListVersions(ctx context.Context, q pg.Querier, orgID, lessonID uuid.UUID) ([]LessonVersion, error) {
	rows, err := q.Query(ctx, `
		SELECT `+versionColumns+` FROM content.lesson_versions
		WHERE org_id = $1 AND lesson_id = $2 ORDER BY version DESC`, orgID, lessonID)
	if err != nil {
		return nil, fmt.Errorf("content: не удалось прочитать версии: %w", err)
	}
	defer rows.Close()

	var out []LessonVersion
	for rows.Next() {
		v, err := scanVersion(rows)
		if err != nil {
			return nil, fmt.Errorf("content: не удалось прочитать версии: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// LatestPublished — последняя опубликованная версия урока.
// Именно её предлагают назначить по умолчанию.
func (s *Store) LatestPublished(ctx context.Context, q pg.Querier, orgID, lessonID uuid.UUID) (LessonVersion, error) {
	v, err := scanVersion(q.QueryRow(ctx, `
		SELECT `+versionColumns+` FROM content.lesson_versions
		WHERE org_id = $1 AND lesson_id = $2 AND status = 'published'
		ORDER BY version DESC LIMIT 1`, orgID, lessonID))
	if pg.IsNoRows(err) {
		return LessonVersion{}, ErrNotFound
	}
	if err != nil {
		return LessonVersion{}, fmt.Errorf("content: не удалось прочитать версию: %w", err)
	}
	return v, nil
}

// UpdateVersionContent правит тексты версии.
//
// Опубликованную версию база не даст изменить: триггер
// content.guard_published_version поднимет restrict_violation. Проверка
// стоит в базе, а не только в коде, потому что неизменяемость версии —
// обещание, данное уже выданным назначениям.
func (s *Store) UpdateVersionContent(ctx context.Context, q pg.Querier, orgID, id uuid.UUID,
	title, level, refined, frank, grammarRaw, note string, warnings json.RawMessage) error {
	// grammar_raw переписывается только непустым значением.
	//
	// Это сырой разбор грамматики от модели: он приходит один раз при сборке
	// урока и учителю не показывается — в contentDTO поля нет вовсе. Значит
	// фронт физически не может вернуть его обратно и присылает пустую строку
	// при любом сохранении блока. Без NULLIF первая же правка любого блока
	// стирала разбор навсегда. Пустая строка здесь означает «не прислали»,
	// а не «сотри»: стирать этот разбор некому и незачем.
	//
	// С заметкой то же: её задают в диалоге публикации, а экраны блоков её
	// не присылают, и сохранение любого блока стирало бы её.
	tag, err := q.Exec(ctx, `
		UPDATE content.lesson_versions
		SET title = $3, level = $4, refined_text = $5, frank_text = $6,
		    grammar_raw = COALESCE(NULLIF($7, ''), grammar_raw),
		    note = COALESCE(NULLIF($8, ''), note),
		    quality_warnings = COALESCE($9::jsonb, quality_warnings)
		WHERE id = $1 AND org_id = $2`,
		id, orgID, title, level, refined, frank, grammarRaw, note, warnings)
	if err != nil {
		return fmt.Errorf("content: не удалось обновить версию: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetVersionStatus меняет статус версии и, если переданы, название с заметкой.
//
// Название и заметку учитель задаёт в момент публикации — отдельным запросом
// их не записать: опубликованная версия неизменяема. Здесь же старый статус
// ещё `ready`, и триггер content.guard_published_version пропускает запись.
//
// nil означает «не трогать»: COALESCE оставляет прежнее значение. Благодаря
// этому архивация опубликованной версии не меняет ни название, ни заметку —
// а значит проходит проверку триггера, которая требует их неизменности.
func (s *Store) SetVersionStatus(ctx context.Context, q pg.Querier, orgID, id uuid.UUID,
	status string, title, note *string) error {
	tag, err := q.Exec(ctx, `
		UPDATE content.lesson_versions
		SET status = $3,
		    title = COALESCE($4, title),
		    note  = COALESCE($5, note),
		    published_at = CASE WHEN $3 = 'published' THEN now() ELSE published_at END
		WHERE id = $1 AND org_id = $2`, id, orgID, status, title, note)
	if err != nil {
		return fmt.Errorf("content: не удалось изменить статус версии: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------------------------------------------------------------------------
// Содержимое версии
// ---------------------------------------------------------------------------

// ReplaceLexicon переписывает список выражений версии целиком.
//
// Целиком, а не по одному: порядок значим (position с уникальным индексом),
// и пересчитывать его дифференциально — источник дыр в нумерации.
func (s *Store) ReplaceLexicon(ctx context.Context, q pg.Querier, orgID, versionID uuid.UUID,
	items []LexicalUnit) error {
	if _, err := q.Exec(ctx,
		`DELETE FROM content.lexical_units WHERE lesson_version_id = $1`, versionID); err != nil {
		return fmt.Errorf("content: не удалось очистить выражения: %w", err)
	}
	for i, it := range items {
		_, err := q.Exec(ctx, `
			INSERT INTO content.lexical_units
				(id, org_id, lesson_version_id, position, phrase, translation, origin)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			it.ID, orgID, versionID, i+1, it.Phrase, it.Translation, it.Origin)
		if err != nil {
			return fmt.Errorf("content: не удалось сохранить выражение: %w", err)
		}
	}
	return nil
}

func (s *Store) ListLexicon(ctx context.Context, q pg.Querier, versionID uuid.UUID) ([]LexicalUnit, error) {
	rows, err := q.Query(ctx, `
		SELECT id, position, phrase, translation, origin
		FROM content.lexical_units WHERE lesson_version_id = $1 ORDER BY position`, versionID)
	if err != nil {
		return nil, fmt.Errorf("content: не удалось прочитать выражения: %w", err)
	}
	defer rows.Close()

	var out []LexicalUnit
	for rows.Next() {
		var u LexicalUnit
		if err := rows.Scan(&u.ID, &u.Position, &u.Phrase, &u.Translation, &u.Origin); err != nil {
			return nil, fmt.Errorf("content: не удалось прочитать выражения: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) ReplaceGrammar(ctx context.Context, q pg.Querier, orgID, versionID uuid.UUID,
	items []GrammarTopic) error {
	if _, err := q.Exec(ctx,
		`DELETE FROM content.grammar_topics WHERE lesson_version_id = $1`, versionID); err != nil {
		return fmt.Errorf("content: не удалось очистить грамматику: %w", err)
	}
	for i, it := range items {
		examples, err := json.Marshal(nonNilExamples(it.Examples))
		if err != nil {
			return fmt.Errorf("content: не удалось сохранить примеры: %w", err)
		}
		practice, err := json.Marshal(nonNilTasks(it.Practice))
		if err != nil {
			return fmt.Errorf("content: не удалось сохранить задания: %w", err)
		}
		_, err = q.Exec(ctx, `
			INSERT INTO content.grammar_topics
				(id, org_id, lesson_version_id, position, title, example, origin,
				 explanation, examples, practice)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			it.ID, orgID, versionID, i+1, it.Title, it.Example, it.Origin,
			it.Explanation, examples, practice)
		if err != nil {
			return fmt.Errorf("content: не удалось сохранить грамматику: %w", err)
		}
	}
	return nil
}

func (s *Store) ListGrammar(ctx context.Context, q pg.Querier, versionID uuid.UUID) ([]GrammarTopic, error) {
	rows, err := q.Query(ctx, `
		SELECT id, position, title, example, origin, explanation, examples, practice
		FROM content.grammar_topics WHERE lesson_version_id = $1 ORDER BY position`, versionID)
	if err != nil {
		return nil, fmt.Errorf("content: не удалось прочитать грамматику: %w", err)
	}
	defer rows.Close()

	var out []GrammarTopic
	for rows.Next() {
		var g GrammarTopic
		var examples, practice []byte
		if err := rows.Scan(&g.ID, &g.Position, &g.Title, &g.Example, &g.Origin,
			&g.Explanation, &examples, &practice); err != nil {
			return nil, fmt.Errorf("content: не удалось прочитать грамматику: %w", err)
		}
		// Разбор мог не записаться в старых уроках — это не повод не отдать
		// саму конструкцию: экран покажет её без примеров, а не упадёт.
		_ = json.Unmarshal(examples, &g.Examples)
		_ = json.Unmarshal(practice, &g.Practice)
		out = append(out, g)
	}
	return out, rows.Err()
}

// ReplaceObjectives переписывает цели версии целиком.
//
// Как лексика и грамматика: список маленький, порядок важен, а точечные
// правки по одной строке дали бы гонку между двумя вкладками учителя.
func (s *Store) ReplaceObjectives(ctx context.Context, q pg.Querier, orgID, versionID uuid.UUID,
	items []Objective) error {
	if _, err := q.Exec(ctx,
		`DELETE FROM content.learning_objectives WHERE lesson_version_id = $1`, versionID); err != nil {
		return fmt.Errorf("content: не удалось очистить цели: %w", err)
	}
	for i, it := range items {
		_, err := q.Exec(ctx, `
			INSERT INTO content.learning_objectives (id, org_id, lesson_version_id, position, text)
			VALUES ($1, $2, $3, $4, $5)`, it.ID, orgID, versionID, i+1, it.Text)
		if err != nil {
			return fmt.Errorf("content: не удалось сохранить цели: %w", err)
		}
	}
	return nil
}

func (s *Store) ListObjectives(ctx context.Context, q pg.Querier, versionID uuid.UUID) ([]Objective, error) {
	rows, err := q.Query(ctx, `
		SELECT id, position, text FROM content.learning_objectives
		WHERE lesson_version_id = $1 ORDER BY position`, versionID)
	if err != nil {
		return nil, fmt.Errorf("content: не удалось прочитать цели: %w", err)
	}
	defer rows.Close()

	var out []Objective
	for rows.Next() {
		var o Objective
		if err := rows.Scan(&o.ID, &o.Position, &o.Text); err != nil {
			return nil, fmt.Errorf("content: не удалось прочитать цели: %w", err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (s *Store) ReplaceExercises(ctx context.Context, q pg.Querier, orgID, versionID uuid.UUID,
	items []Exercise) error {
	if _, err := q.Exec(ctx,
		`DELETE FROM content.exercises WHERE lesson_version_id = $1`, versionID); err != nil {
		return fmt.Errorf("content: не удалось очистить упражнения: %w", err)
	}
	for i, it := range items {
		_, err := q.Exec(ctx, `
			INSERT INTO content.exercises
				(id, org_id, lesson_version_id, type, position, title, instruction,
				 payload, answer_key, raw_source)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			it.ID, orgID, versionID, it.Type, i+1, it.Title, it.Instruction,
			jsonOrEmpty(it.Payload), jsonOrEmpty(it.AnswerKey), it.RawSource)
		if err != nil {
			return fmt.Errorf("content: не удалось сохранить упражнение: %w", err)
		}
	}
	return nil
}

// ListExercises отдаёт упражнения версии.
//
// withAnswers=false обнуляет ключ ответов ещё в SQL. Это для ручек ученика:
// решать «не забыть вырезать» на уровне сериализации ненадёжно, а не
// выбранное из базы поле утечь не может.
func (s *Store) ListExercises(ctx context.Context, q pg.Querier, versionID uuid.UUID,
	withAnswers bool) ([]Exercise, error) {
	answerCol := `'{}'::jsonb`
	if withAnswers {
		answerCol = `answer_key`
	}
	rows, err := q.Query(ctx, `
		SELECT id, type, position, title, instruction, payload, `+answerCol+`, raw_source
		FROM content.exercises WHERE lesson_version_id = $1 ORDER BY position`, versionID)
	if err != nil {
		return nil, fmt.Errorf("content: не удалось прочитать упражнения: %w", err)
	}
	defer rows.Close()

	var out []Exercise
	for rows.Next() {
		var e Exercise
		if err := rows.Scan(&e.ID, &e.Type, &e.Position, &e.Title, &e.Instruction,
			&e.Payload, &e.AnswerKey, &e.RawSource); err != nil {
			return nil, fmt.Errorf("content: не удалось прочитать упражнения: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// jsonOrEmpty подставляет пустой объект вместо nil: колонка jsonb NOT NULL.
func jsonOrEmpty(b []byte) []byte {
	if len(b) == 0 {
		return []byte("{}")
	}
	return b
}

// ---------------------------------------------------------------------------
// Каталог материалов
// ---------------------------------------------------------------------------

const materialColumns = `id, org_id, uploaded_by, title, topic, lang_code, kind,
	storage_key, mime, size_bytes, duration_ms, checksum,
	extracted_text, extracted_at, curated, created_at, updated_at`

// ---------------------------------------------------------------------------
// Ссылки «Поделиться уроком»
// ---------------------------------------------------------------------------

const shareColumns = `id, org_id, lesson_id, token, created_by, created_at, revoked_at`
