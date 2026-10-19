package learning

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/NATIVE/NATIVE/internal/platform/pg"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Отказы хранилища, которые сервис переводит в слова контракта.
var (
	// ErrTaskTaken — задание уже лежит в другой выдаче (первичный ключ
	// issue_tasks).
	ErrTaskTaken = errors.New("learning: задание уже выдано")
	// ErrHomeTaken — у урока уже есть домашняя работа (issues_home_uq).
	ErrHomeTaken = errors.New("learning: домашняя работа уже задана")
	// ErrIssueGone — выдачу отменили, пока ученик открывал задание.
	ErrIssueGone = errors.New("learning: выдачи больше нет")
	// ErrIssueStarted — выдачу начали, отменить нельзя.
	ErrIssueStarted  = errors.New("learning: выдачу уже начали")
	ErrClassFinished = errors.New(msgClassFinished)
	ErrHomeExpired   = errors.New(msgHomeExpired)
)

func isFKViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

// ---------------------------------------------------------------------------
// Выдачи
// ---------------------------------------------------------------------------

// Выдача читается вместе с ключами заданий одним запросом: на экране
// учителя они нужны всегда вместе.
const issueSelect = `
	SELECT i.id, i.org_id, i.assignment_id, i.kind, i.opened_at, i.due_at, i.closed_at,
	       i.legacy, i.created_by, i.created_at, i.updated_at,
	       COALESCE(array_agg(t.task_key ORDER BY t.task_key)
	                FILTER (WHERE t.task_key IS NOT NULL), '{}')
	FROM learning.issues i
	LEFT JOIN learning.issue_tasks t ON t.issue_id = i.id`

func scanIssue(row pg.Row) (Issue, error) {
	var is Issue
	err := row.Scan(&is.ID, &is.OrgID, &is.AssignmentID, &is.Kind, &is.OpenedAt, &is.DueAt,
		&is.ClosedAt, &is.Legacy, &is.CreatedBy, &is.CreatedAt, &is.UpdatedAt, &is.TaskKeys)
	return is, err
}

// IssuesOfAssignments — выдачи нескольких назначений одним запросом.
//
// Одним, потому что «Мои занятия» и вкладка «Уроки» показывают выдачи в
// каждой строке, а запрос на строку — это N+1 на экране, который ученик
// открывает первым.
func (s *Store) IssuesOfAssignments(ctx context.Context, q pg.Querier, orgID uuid.UUID,
	assignmentIDs []uuid.UUID) (map[uuid.UUID][]Issue, error) {
	out := make(map[uuid.UUID][]Issue, len(assignmentIDs))
	if len(assignmentIDs) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, issueSelect+`
		WHERE i.org_id = $1 AND i.assignment_id = ANY($2)
		GROUP BY i.id
		ORDER BY i.opened_at, i.created_at, i.id`, orgID, assignmentIDs)
	if err != nil {
		return nil, fmt.Errorf("learning: не удалось прочитать выдачи: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		is, err := scanIssue(rows)
		if err != nil {
			return nil, fmt.Errorf("learning: не удалось прочитать выдачи: %w", err)
		}
		out[is.AssignmentID] = append(out[is.AssignmentID], is)
	}
	return out, rows.Err()
}

func (s *Store) IssueByID(ctx context.Context, q pg.Querier, orgID, id uuid.UUID) (Issue, error) {
	is, err := scanIssue(q.QueryRow(ctx, issueSelect+`
		WHERE i.id = $1 AND i.org_id = $2
		GROUP BY i.id`, id, orgID))
	if pg.IsNoRows(err) {
		return Issue{}, ErrNotFound
	}
	if err != nil {
		return Issue{}, fmt.Errorf("learning: не удалось прочитать выдачу: %w", err)
	}
	return is, nil
}

// LockIssue держит выдачу открытой до конца короткой транзакции записи.
// FOR SHARE конфликтует и с закрытием без изменения ключа, и с отменой.
// Проверки внешнего ключа (FOR KEY SHARE) для закрытия недостаточно.
func (s *Store) LockIssue(ctx context.Context, q pg.Querier, orgID, id uuid.UUID) (Issue, error) {
	var is Issue
	err := q.QueryRow(ctx, `SELECT kind, opened_at, due_at, closed_at FROM learning.issues
		WHERE id=$1 AND org_id=$2 FOR SHARE`, id, orgID).Scan(&is.Kind, &is.OpenedAt, &is.DueAt, &is.ClosedAt)
	if pg.IsNoRows(err) {
		return Issue{}, ErrIssueGone
	}
	return is, err
}

func (s *Store) LockOpenIssue(ctx context.Context, q pg.Querier, orgID, id uuid.UUID) error {
	is, err := s.LockIssue(ctx, q, orgID, id)
	if err != nil {
		return err
	}
	now := time.Now()
	if is.Open(now) {
		return nil
	}
	if !is.Closed(now) {
		return ErrIssueGone
	}
	if is.Kind == IssueClass {
		return ErrClassFinished
	}
	return ErrHomeExpired
}

// HasIssues — выдано ли по уроку хоть что-нибудь. Пока нет, ученик урока не
// видит.
func (s *Store) HasIssues(ctx context.Context, q pg.Querier, orgID, assignmentID uuid.UUID) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM learning.issues WHERE org_id = $1 AND assignment_id = $2)`,
		orgID, assignmentID).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("learning: не удалось проверить выдачи: %w", err)
	}
	return ok, nil
}

// CreateIssue записывает выдачу и её задания одной транзакцией.
//
// Правила «одна домашняя на урок» и «задание выдаётся один раз» держит база:
// уникальный индекс и первичный ключ. Проверка в коде до этого — только ради
// понятного отказа; две вкладки учителя, нажавшие одновременно, всё равно
// упрутся в ключ, и вторая получит отказ, а не вторую выдачу.
func (s *Store) CreateIssue(ctx context.Context, pool *pg.Pool, is Issue, keys []string) error {
	return pg.InTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO learning.issues
				(id, org_id, assignment_id, kind, opened_at, due_at, legacy, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, false, $7)`,
			is.ID, is.OrgID, is.AssignmentID, is.Kind, is.OpenedAt, is.DueAt, is.CreatedBy)
		if pg.IsUniqueViolation(err) {
			return ErrHomeTaken
		}
		if err != nil {
			return fmt.Errorf("learning: не удалось создать выдачу: %w", err)
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO learning.issue_tasks (org_id, assignment_id, issue_id, task_key)
			SELECT $1, $2, $3, unnest($4::text[])`,
			is.OrgID, is.AssignmentID, is.ID, keys)
		if pg.IsUniqueViolation(err) {
			return ErrTaskTaken
		}
		if err != nil {
			return fmt.Errorf("learning: не удалось записать задания выдачи: %w", err)
		}
		return nil
	})
}

// FinishIssue закрывает выдачу на уроке. false — закрывать нечего: выдача
// уже закрыта руками или по концу дня.
//
// Условие стоит в самом UPDATE: два нажатия «Завершить» из двух вкладок
// закрывают выдачу один раз, и момент закрытия не переписывается.
func (s *Store) FinishIssue(ctx context.Context, q pg.Querier, orgID, id uuid.UUID, now time.Time) (bool, error) {
	tag, err := q.Exec(ctx, `
		UPDATE learning.issues SET closed_at = $3
		WHERE id = $1 AND org_id = $2 AND kind = 'class' AND closed_at IS NULL
		  AND (due_at IS NULL OR due_at > $3)`, id, orgID, now)
	if err != nil {
		return false, fmt.Errorf("learning: не удалось завершить выдачу: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// ExtendIssue переносит срок домашней работы. false — новый срок не позже
// прежнего (проверка в запросе: две правки подряд не сдвигают срок назад).
func (s *Store) ExtendIssue(ctx context.Context, q pg.Querier, orgID, id uuid.UUID, dueAt time.Time) (bool, error) {
	tag, err := q.Exec(ctx, `
		UPDATE learning.issues SET due_at = $3
		WHERE id = $1 AND org_id = $2 AND kind = 'home' AND closed_at IS NULL
		  AND due_at IS NOT NULL AND due_at < $3`, id, orgID, dueAt)
	if err != nil {
		return false, fmt.Errorf("learning: не удалось продлить выдачу: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// CancelIssue удаляет выдачу, если её никто не начал.
//
// Строка выдачи блокируется до проверки прогресса. Ученик, открывающий
// задание в ту же секунду, проверяет внешний ключ на эту строку и ждёт:
// либо отмена увидит его прогресс и откажет, либо его вставка упрётся в
// удалённую выдачу. Вдвоём они не проходят.
func (s *Store) CancelIssue(ctx context.Context, pool *pg.Pool, orgID, id uuid.UUID) error {
	return pg.InTx(ctx, pool, func(tx pgx.Tx) error {
		var found uuid.UUID
		err := tx.QueryRow(ctx, `
			SELECT id FROM learning.issues WHERE id = $1 AND org_id = $2 FOR UPDATE`,
			id, orgID).Scan(&found)
		if pg.IsNoRows(err) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("learning: не удалось прочитать выдачу: %w", err)
		}
		var started bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM learning.task_progress WHERE issue_id = $1)`,
			id).Scan(&started); err != nil {
			return fmt.Errorf("learning: не удалось проверить выдачу: %w", err)
		}
		if started {
			return ErrIssueStarted
		}
		if _, err := tx.Exec(ctx,
			`DELETE FROM learning.issues WHERE id = $1 AND org_id = $2`, id, orgID); err != nil {
			return fmt.Errorf("learning: не удалось отменить выдачу: %w", err)
		}
		return nil
	})
}

// CloseOpenIssues закрывает всё, что ещё открыто: урок сняли с класса.
// Выдачи, закрывшиеся по сроку, не трогаем — у них момент закрытия свой.
func (s *Store) CloseOpenIssues(ctx context.Context, q pg.Querier, orgID, assignmentID uuid.UUID,
	now time.Time) error {
	_, err := q.Exec(ctx, `
		UPDATE learning.issues SET closed_at = $3
		WHERE org_id = $1 AND assignment_id = $2 AND closed_at IS NULL
		  AND (due_at IS NULL OR due_at > $3)`, orgID, assignmentID, now)
	if err != nil {
		return fmt.Errorf("learning: не удалось закрыть выдачи: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Прогресс по заданиям
// ---------------------------------------------------------------------------

const progressColumns = `p.attempt_id, p.task_key, p.issue_id, p.opened_at, p.done_at`

func scanProgress(rows pgx.Rows) ([]TaskProgress, error) {
	defer rows.Close()
	var out []TaskProgress
	for rows.Next() {
		var p TaskProgress
		if err := rows.Scan(&p.AttemptID, &p.TaskKey, &p.IssueID, &p.OpenedAt, &p.DoneAt); err != nil {
			return nil, fmt.Errorf("learning: не удалось прочитать прогресс: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ProgressOfAttempts — прогресс нескольких попыток одним запросом.
func (s *Store) ProgressOfAttempts(ctx context.Context, q pg.Querier, orgID uuid.UUID,
	attemptIDs []uuid.UUID) ([]TaskProgress, error) {
	if len(attemptIDs) == 0 {
		return nil, nil
	}
	rows, err := q.Query(ctx, `
		SELECT `+progressColumns+` FROM learning.task_progress p
		WHERE p.org_id = $1 AND p.attempt_id = ANY($2)`, orgID, attemptIDs)
	if err != nil {
		return nil, fmt.Errorf("learning: не удалось прочитать прогресс: %w", err)
	}
	return scanProgress(rows)
}

// ProgressOfAssignment — прогресс всего класса по уроку. Таблица учителя
// опрашивает его раз в 5 секунд, поэтому один запрос по индексам попыток
// (assignment_id, …) и прогресса (attempt_id, …).
func (s *Store) ProgressOfAssignment(ctx context.Context, q pg.Querier, orgID,
	assignmentID uuid.UUID) ([]TaskProgress, error) {
	rows, err := q.Query(ctx, `
		SELECT `+progressColumns+`
		FROM learning.task_progress p
		JOIN learning.attempts a ON a.id = p.attempt_id
		WHERE a.org_id = $1 AND a.assignment_id = $2 AND p.org_id = $1`, orgID, assignmentID)
	if err != nil {
		return nil, fmt.Errorf("learning: не удалось прочитать прогресс класса: %w", err)
	}
	return scanProgress(rows)
}

// OpenTaskProgress отмечает, что ученик открыл задание. Повтор ничего не
// меняет: время открытия — первое, от него считаются минуты на задание.
func (s *Store) OpenTaskProgress(ctx context.Context, q pg.Querier, orgID, attemptID uuid.UUID,
	key string, issueID uuid.UUID) error {
	_, err := q.Exec(ctx, `
		INSERT INTO learning.task_progress (org_id, attempt_id, task_key, issue_id)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (attempt_id, task_key) DO NOTHING`, orgID, attemptID, key, issueID)
	if isFKViolation(err) {
		return ErrIssueGone
	}
	if err != nil {
		return fmt.Errorf("learning: не удалось отметить задание: %w", err)
	}
	return nil
}

// MarkTaskProgress — задание открыто и, если done, сделано.
//
// Момент «сделано» не переписывается: COALESCE оставляет первый, и повтор
// «Дальше» не растягивает минуты на задание.
func (s *Store) MarkTaskProgress(ctx context.Context, q pg.Querier, orgID, attemptID uuid.UUID,
	key string, issueID uuid.UUID, done bool) error {
	_, err := q.Exec(ctx, `
		INSERT INTO learning.task_progress (org_id, attempt_id, task_key, issue_id, done_at)
		VALUES ($1, $2, $3, $4, CASE WHEN $5::boolean THEN now() END)
		ON CONFLICT (attempt_id, task_key) DO UPDATE
		SET done_at = CASE WHEN $5::boolean
		                   THEN COALESCE(learning.task_progress.done_at, now())
		                   ELSE learning.task_progress.done_at END,
		    issue_id = COALESCE(learning.task_progress.issue_id, EXCLUDED.issue_id)`,
		orgID, attemptID, key, issueID, done)
	if isFKViolation(err) {
		return ErrIssueGone
	}
	if err != nil {
		return fmt.Errorf("learning: не удалось отметить задание: %w", err)
	}
	return nil
}

// LockAttempt читает попытку с блокировкой строки.
//
// Нужна там, где по ответам и прогрессу решается «сделаны ли все задания»:
// последний ответ и последнее «Дальше», пришедшие одновременно, иначе
// посчитали бы каждый без другого, и урок так и остался бы незавершённым.
func (s *Store) LockAttempt(ctx context.Context, tx pgx.Tx, orgID, id uuid.UUID) (Attempt, error) {
	a, err := scanAttempt(tx.QueryRow(ctx,
		`SELECT `+attemptColumns+` FROM learning.attempts WHERE id = $1 AND org_id = $2 FOR UPDATE`,
		id, orgID))
	if pg.IsNoRows(err) {
		return Attempt{}, ErrNotFound
	}
	if err != nil {
		return Attempt{}, fmt.Errorf("learning: не удалось прочитать попытку: %w", err)
	}
	return a, nil
}

// CompleteAttempt завершает попытку: сделаны все задания урока. По ней
// считается аналитика класса, поэтому completed — сразу true: все вопросы
// отвечены, порог «половина ответов» пройден заведомо.
func (s *Store) CompleteAttempt(ctx context.Context, q pg.Querier, orgID, id uuid.UUID) error {
	_, err := q.Exec(ctx, `
		UPDATE learning.attempts
		SET finished_at = COALESCE(finished_at, now()), completed = true, last_seen_at = now()
		WHERE id = $1 AND org_id = $2`, id, orgID)
	if err != nil {
		return fmt.Errorf("learning: не удалось завершить попытку: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Назначения: что нужно выдаче по заданиям
// ---------------------------------------------------------------------------

// AssignmentsByIDs — назначения пачкой. Карточке ученика: у него за год
// десятки попыток, и назначение на каждую было бы N+1.
func (s *Store) AssignmentsByIDs(ctx context.Context, q pg.Querier, orgID uuid.UUID,
	ids []uuid.UUID) (map[uuid.UUID]Assignment, error) {
	out := make(map[uuid.UUID]Assignment, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `
		SELECT `+assignmentColumns+` FROM learning.assignments
		WHERE org_id = $1 AND id = ANY($2)`, orgID, ids)
	if err != nil {
		return nil, fmt.Errorf("learning: не удалось прочитать назначения: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		a, err := scanAssignment(rows)
		if err != nil {
			return nil, fmt.Errorf("learning: не удалось прочитать назначения: %w", err)
		}
		out[a.ID] = a
	}
	return out, rows.Err()
}

// LockClassAssignments — назначения класса меняются по одному.
//
// Правило «урок назначается классу один раз» — про урок, а не про версию,
// и уникальным индексом его не выразить: версия у двух назначений разная.
// Поэтому проверка и вставка идут под блокировкой класса до конца
// транзакции, и двойной клик «Назначить» с разными версиями не проходит.
func (s *Store) LockClassAssignments(ctx context.Context, tx pgx.Tx, classID uuid.UUID) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::text, 18))`, classID)
	if err != nil {
		return fmt.Errorf("learning: не удалось заблокировать класс: %w", err)
	}
	return nil
}

// OpenAssignmentVersions — версии уроков, назначенных классу и не снятых.
func (s *Store) OpenAssignmentVersions(ctx context.Context, q pg.Querier, orgID,
	classID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := q.Query(ctx, `
		SELECT lesson_version_id FROM learning.assignments
		WHERE org_id = $1 AND class_id = $2 AND state <> 'closed'`, orgID, classID)
	if err != nil {
		return nil, fmt.Errorf("learning: не удалось прочитать назначения класса: %w", err)
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("learning: не удалось прочитать назначения класса: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// «В сети»
// ---------------------------------------------------------------------------

// presenceStep — чаще этого отметка не обновляется. Экран урока опрашивает
// сервер раз в 5 секунд, а «в сети» считается за 2 минуты: обновлять строку
// на каждом опросе — это запись в базу ради точности, которой никто не
// увидит.
const presenceStep = "20 seconds"

// TouchPresence отмечает, что ученик активен.
//
// Прореживание — в самом запросе: строка обновляется, только если отметка
// старше presenceStep, а свежая не пишется вовсе (UPDATE с ложным условием
// ничего не блокирует и не пишет). Первый раз строка вставляется.
func (s *Store) TouchPresence(ctx context.Context, q pg.Querier, orgID, studentID uuid.UUID) error {
	_, err := q.Exec(ctx, `
		WITH touched AS (
			UPDATE learning.presence SET seen_at = now()
			WHERE student_id = $2 AND org_id = $1 AND seen_at < now() - interval '`+presenceStep+`'
			RETURNING 1
		)
		INSERT INTO learning.presence (org_id, student_id)
		SELECT $1, $2
		WHERE NOT EXISTS (SELECT 1 FROM learning.presence WHERE student_id = $2)
		ON CONFLICT (student_id) DO NOTHING`, orgID, studentID)
	if err != nil {
		return fmt.Errorf("learning: не удалось отметить активность: %w", err)
	}
	return nil
}

// PresenceOf — когда ученики были активны последний раз.
func (s *Store) PresenceOf(ctx context.Context, q pg.Querier, orgID uuid.UUID,
	studentIDs []uuid.UUID) (map[uuid.UUID]time.Time, error) {
	out := make(map[uuid.UUID]time.Time, len(studentIDs))
	if len(studentIDs) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `
		SELECT student_id, seen_at FROM learning.presence
		WHERE org_id = $1 AND student_id = ANY($2)`, orgID, studentIDs)
	if err != nil {
		return nil, fmt.Errorf("learning: не удалось прочитать активность: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var seen time.Time
		if err := rows.Scan(&id, &seen); err != nil {
			return nil, fmt.Errorf("learning: не удалось прочитать активность: %w", err)
		}
		out[id] = seen
	}
	return out, rows.Err()
}
