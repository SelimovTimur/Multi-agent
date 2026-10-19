package classroom

import (
	"context"
	"errors"
	"fmt"

	"github.com/NATIVE/NATIVE/internal/platform/pg"
	"github.com/google/uuid"
)

// ErrNotFound — объект не найден. Сервис превращает его в 404.
var ErrNotFound = errors.New("classroom: не найдено")

// Store — доступ к схеме classroom.
//
// Соединение приходит параметром (pg.Querier), поэтому один и тот же метод
// работает и на пуле, и внутри транзакции. Состояния у Store нет.
type Store struct{}

func NewStore() *Store { return &Store{} }

const classColumns = `id, org_id, teacher_id, title, subject, lang_code,
	default_level, join_code, join_open, status, created_at, updated_at`

func scanClass(row pg.Row) (Class, error) {
	var c Class
	err := row.Scan(&c.ID, &c.OrgID, &c.TeacherID, &c.Title, &c.Subject, &c.LangCode,
		&c.DefaultLevel, &c.JoinCode, &c.JoinOpen, &c.Status, &c.CreatedAt, &c.UpdatedAt)
	return c, err
}

// CreateClass вставляет класс и возвращает его же с проставленными базой
// временами: иначе только что созданный класс уезжал бы на фронт с нулевой
// датой, отличаясь от того, что тот получит при следующем чтении.
func (s *Store) CreateClass(ctx context.Context, q pg.Querier, c Class) (Class, error) {
	row := q.QueryRow(ctx, `
		INSERT INTO classroom.classes
			(id, org_id, teacher_id, title, subject, lang_code, default_level,
			 join_code, join_open, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING `+classColumns,
		c.ID, c.OrgID, c.TeacherID, c.Title, c.Subject, c.LangCode, c.DefaultLevel,
		c.JoinCode, c.JoinOpen, c.Status)
	out, err := scanClass(row)
	if err != nil {
		return Class{}, fmt.Errorf("classroom: не удалось создать класс: %w", err)
	}
	return out, nil
}

// ClassByID читает класс в пределах организации.
//
// org_id в условии, а не только id: запрос из чужого тенанта не должен
// находить объект даже при известном идентификаторе (инвариант мультитенантности).
func (s *Store) ClassByID(ctx context.Context, q pg.Querier, orgID, id uuid.UUID) (Class, error) {
	c, err := scanClass(q.QueryRow(ctx,
		`SELECT `+classColumns+` FROM classroom.classes WHERE id = $1 AND org_id = $2`, id, orgID))
	if pg.IsNoRows(err) {
		return Class{}, ErrNotFound
	}
	if err != nil {
		return Class{}, fmt.Errorf("classroom: не удалось прочитать класс: %w", err)
	}
	return c, nil
}

// ClassesByIDs читает классы пачкой, в пределах организации.
//
// Зеркало content.Store.VersionsByIDs и по той же причине: столбец «в какие
// классы выдан урок» рисуется в каждой строке библиотеки, и запрос на класс
// превратил бы её открытие в N+1.
//
// Классов, которых нет, в карте просто нет — в том числе чужих: org_id в
// условии, как и в ClassByID. Вызывающий сам решает, что показать на месте
// удалённого класса.
func (s *Store) ClassesByIDs(ctx context.Context, q pg.Querier, orgID uuid.UUID,
	ids []uuid.UUID) (map[uuid.UUID]Class, error) {
	out := make(map[uuid.UUID]Class, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx,
		`SELECT `+classColumns+` FROM classroom.classes WHERE org_id = $1 AND id = ANY($2)`,
		orgID, ids)
	if err != nil {
		return nil, fmt.Errorf("classroom: не удалось прочитать классы: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		c, err := scanClass(rows)
		if err != nil {
			return nil, fmt.Errorf("classroom: не удалось прочитать классы: %w", err)
		}
		out[c.ID] = c
	}
	return out, rows.Err()
}

// ListClasses отдаёт классы учителя, свежие сверху.
func (s *Store) ListClasses(ctx context.Context, q pg.Querier, orgID, teacherID uuid.UUID,
	includeArchived bool) ([]Class, error) {
	rows, err := q.Query(ctx, `
		SELECT `+classColumns+`
		FROM classroom.classes
		WHERE org_id = $1 AND teacher_id = $2
		  AND ($3 OR status = 'active')
		ORDER BY created_at DESC`, orgID, teacherID, includeArchived)
	if err != nil {
		return nil, fmt.Errorf("classroom: не удалось прочитать классы: %w", err)
	}
	defer rows.Close()

	var out []Class
	for rows.Next() {
		c, err := scanClass(rows)
		if err != nil {
			return nil, fmt.Errorf("classroom: не удалось прочитать классы: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// UpdateClass меняет то, что учитель правит на экране класса.
func (s *Store) UpdateClass(ctx context.Context, q pg.Querier, orgID, id uuid.UUID,
	title, subject, level string) error {
	tag, err := q.Exec(ctx, `
		UPDATE classroom.classes
		SET title = $3, subject = $4, default_level = $5
		WHERE id = $1 AND org_id = $2`, id, orgID, title, subject, level)
	if err != nil {
		return fmt.Errorf("classroom: не удалось обновить класс: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetClassStatus архивирует или возвращает класс.
//
// Удаления нет: у класса есть ответы учеников и назначенные уроки, а
// удалённый класс превратил бы их в сирот.
func (s *Store) SetClassStatus(ctx context.Context, q pg.Querier, orgID, id uuid.UUID, status string) error {
	tag, err := q.Exec(ctx,
		`UPDATE classroom.classes SET status = $3 WHERE id = $1 AND org_id = $2`, id, orgID, status)
	if err != nil {
		return fmt.Errorf("classroom: не удалось изменить статус класса: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// CountActive считает учеников в каждом из переданных классов.
//
// Одним запросом на список, а не по запросу на класс: на экране кабинета
// классов может быть пять, а на школьном аккаунте — пятьдесят.
func (s *Store) CountActive(ctx context.Context, q pg.Querier, classIDs []uuid.UUID) (map[uuid.UUID]int, error) {
	out := make(map[uuid.UUID]int, len(classIDs))
	if len(classIDs) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `
		SELECT class_id, count(*)
		FROM classroom.enrollments
		WHERE class_id = ANY($1) AND status = 'active'
		GROUP BY class_id`, classIDs)
	if err != nil {
		return nil, fmt.Errorf("classroom: не удалось посчитать учеников: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, fmt.Errorf("classroom: не удалось посчитать учеников: %w", err)
		}
		out[id] = n
	}
	return out, rows.Err()
}

const enrollmentColumns = `id, org_id, class_id, student_id, teacher_id, status,
	joined_at, removed_at, last_seen_at`

func scanEnrollment(row pg.Row) (Enrollment, error) {
	var e Enrollment
	err := row.Scan(&e.ID, &e.OrgID, &e.ClassID, &e.StudentID, &e.TeacherID, &e.Status,
		&e.JoinedAt, &e.RemovedAt, &e.LastSeenAt)
	return e, err
}

// UpsertEnrollment записывает ученика в класс или возвращает исключённого.
//
// Повторное присоединение переиспользует строку: иначе у ученика в одном
// классе накапливались бы дубли, а история ответов рвалась бы на части.
func (s *Store) UpsertEnrollment(ctx context.Context, q pg.Querier, e Enrollment) (Enrollment, error) {
	row := q.QueryRow(ctx, `
		INSERT INTO classroom.enrollments
			(id, org_id, class_id, student_id, teacher_id, status, last_seen_at)
		VALUES ($1, $2, $3, $4, $5, 'active', now())
		ON CONFLICT (class_id, student_id) DO UPDATE
		SET status = 'active', removed_at = NULL, last_seen_at = now()
		RETURNING `+enrollmentColumns,
		e.ID, e.OrgID, e.ClassID, e.StudentID, e.TeacherID)
	out, err := scanEnrollment(row)
	if err != nil {
		return Enrollment{}, fmt.Errorf("classroom: не удалось записать ученика в класс: %w", err)
	}
	return out, nil
}

func (s *Store) ListEnrollments(ctx context.Context, q pg.Querier, orgID, classID uuid.UUID,
	includeRemoved bool) ([]Enrollment, error) {
	rows, err := q.Query(ctx, `
		SELECT `+enrollmentColumns+`
		FROM classroom.enrollments
		WHERE org_id = $1 AND class_id = $2 AND ($3 OR status = 'active')
		ORDER BY joined_at`, orgID, classID, includeRemoved)
	if err != nil {
		return nil, fmt.Errorf("classroom: не удалось прочитать состав класса: %w", err)
	}
	defer rows.Close()

	var out []Enrollment
	for rows.Next() {
		e, err := scanEnrollment(rows)
		if err != nil {
			return nil, fmt.Errorf("classroom: не удалось прочитать состав класса: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) EnrollmentOf(ctx context.Context, q pg.Querier, orgID, classID, studentID uuid.UUID) (Enrollment, error) {
	e, err := scanEnrollment(q.QueryRow(ctx, `
		SELECT `+enrollmentColumns+`
		FROM classroom.enrollments
		WHERE org_id = $1 AND class_id = $2 AND student_id = $3`, orgID, classID, studentID))
	if pg.IsNoRows(err) {
		return Enrollment{}, ErrNotFound
	}
	if err != nil {
		return Enrollment{}, fmt.Errorf("classroom: не удалось прочитать запись ученика: %w", err)
	}
	return e, nil
}

func (s *Store) SetEnrollmentStatus(ctx context.Context, q pg.Querier, orgID, classID,
	studentID uuid.UUID, status string) error {
	tag, err := q.Exec(ctx, `
		UPDATE classroom.enrollments
		SET status = $4,
		    removed_at = CASE WHEN $4 = 'removed' THEN now() ELSE NULL END
		WHERE org_id = $1 AND class_id = $2 AND student_id = $3`,
		orgID, classID, studentID, status)
	if err != nil {
		return fmt.Errorf("classroom: не удалось изменить статус ученика: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ClassesOfStudent отдаёт активные классы ученика.
// Нужен экрану ученика: в какой класс он вошёл и что там задано.
func (s *Store) ClassesOfStudent(ctx context.Context, q pg.Querier, studentID uuid.UUID) ([]Class, error) {
	rows, err := q.Query(ctx, `
		SELECT `+prefixed(classColumns, "c")+`
		FROM classroom.classes c
		JOIN classroom.enrollments e ON e.class_id = c.id
		WHERE e.student_id = $1 AND e.status = 'active' AND c.status = 'active'
		ORDER BY e.joined_at DESC`, studentID)
	if err != nil {
		return nil, fmt.Errorf("classroom: не удалось прочитать классы ученика: %w", err)
	}
	defer rows.Close()

	var out []Class
	for rows.Next() {
		c, err := scanClass(rows)
		if err != nil {
			return nil, fmt.Errorf("classroom: не удалось прочитать классы ученика: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// TouchEnrollment отмечает активность ученика в классе.
// Ошибку не возвращает наверх как фатальную: это метрика, а не бизнес-факт.
func (s *Store) TouchEnrollment(ctx context.Context, q pg.Querier, classID, studentID uuid.UUID) error {
	_, err := q.Exec(ctx, `
		UPDATE classroom.enrollments SET last_seen_at = now()
		WHERE class_id = $1 AND student_id = $2 AND status = 'active'`, classID, studentID)
	return err
}

// prefixed дописывает алиас таблицы к списку колонок: "id, org_id" → "c.id, c.org_id".
// Нужен там, где к запросу добавляется join и голые имена становятся неоднозначными.
func prefixed(columns, alias string) string {
	var b []byte
	for _, part := range splitColumns(columns) {
		if len(b) > 0 {
			b = append(b, ',', ' ')
		}
		b = append(b, alias...)
		b = append(b, '.')
		b = append(b, part...)
	}
	return string(b)
}

func splitColumns(columns string) []string {
	var out []string
	cur := make([]byte, 0, 32)
	flush := func() {
		if len(cur) > 0 {
			out = append(out, string(cur))
			cur = cur[:0]
		}
	}
	for i := 0; i < len(columns); i++ {
		switch c := columns[i]; c {
		case ',', ' ', '\t', '\n':
			flush()
		default:
			cur = append(cur, c)
		}
	}
	flush()
	return out
}
