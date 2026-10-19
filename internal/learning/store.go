package learning

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/NATIVE/NATIVE/internal/platform/pg"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var ErrNotFound = errors.New("learning: не найдено")

type Store struct{}

func NewStore() *Store { return &Store{} }

func (s *Store) HasVersionAssignments(ctx context.Context, q pg.Querier, orgID, versionID uuid.UUID) (bool, error) {
	var found bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM learning.assignments
		WHERE org_id = $1 AND lesson_version_id = $2)`, orgID, versionID).Scan(&found)
	return found, err
}

// ---------------------------------------------------------------------------
// Назначения
// ---------------------------------------------------------------------------

const assignmentColumns = `id, org_id, lesson_version_id, class_id, target_type,
	opens_at, closes_at, closed_at, state, created_by, created_at, updated_at`

func scanAssignment(row pg.Row) (Assignment, error) {
	var a Assignment
	err := row.Scan(&a.ID, &a.OrgID, &a.LessonVersionID, &a.ClassID, &a.TargetType,
		&a.OpensAt, &a.ClosesAt, &a.ClosedAt, &a.State, &a.CreatedBy, &a.CreatedAt, &a.UpdatedAt)
	return a, err
}

func (s *Store) CreateAssignment(ctx context.Context, q pg.Querier, a Assignment) (Assignment, error) {
	row := q.QueryRow(ctx, `
		INSERT INTO learning.assignments
			(id, org_id, lesson_version_id, class_id, target_type, opens_at, closes_at, state, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING `+assignmentColumns,
		a.ID, a.OrgID, a.LessonVersionID, a.ClassID, a.TargetType,
		a.OpensAt, a.ClosesAt, a.State, a.CreatedBy)
	out, err := scanAssignment(row)
	if err != nil {
		return Assignment{}, fmt.Errorf("learning: не удалось создать назначение: %w", err)
	}
	return out, nil
}

func (s *Store) AssignmentByID(ctx context.Context, q pg.Querier, orgID, id uuid.UUID) (Assignment, error) {
	a, err := scanAssignment(q.QueryRow(ctx,
		`SELECT `+assignmentColumns+` FROM learning.assignments WHERE id = $1 AND org_id = $2`, id, orgID))
	if pg.IsNoRows(err) {
		return Assignment{}, ErrNotFound
	}
	if err != nil {
		return Assignment{}, fmt.Errorf("learning: не удалось прочитать назначение: %w", err)
	}
	return a, nil
}

// ListByClass отдаёт назначения класса. state="" означает любые.
func (s *Store) ListByClass(ctx context.Context, q pg.Querier, orgID, classID uuid.UUID,
	state string) ([]Assignment, error) {
	rows, err := q.Query(ctx, `
		SELECT `+assignmentColumns+`
		FROM learning.assignments
		WHERE org_id = $1 AND class_id = $2 AND ($3 = '' OR state = $3)
		ORDER BY created_at DESC`, orgID, classID, state)
	if err != nil {
		return nil, fmt.Errorf("learning: не удалось прочитать назначения: %w", err)
	}
	defer rows.Close()

	var out []Assignment
	for rows.Next() {
		a, err := scanAssignment(rows)
		if err != nil {
			return nil, fmt.Errorf("learning: не удалось прочитать назначения: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ListByVersion — куда выдана конкретная версия урока.
// Нужно библиотеке: перед удалением урока учитель должен увидеть список классов.
func (s *Store) ListByVersion(ctx context.Context, q pg.Querier, orgID uuid.UUID,
	versionIDs []uuid.UUID) ([]Assignment, error) {
	if len(versionIDs) == 0 {
		return nil, nil
	}
	rows, err := q.Query(ctx, `
		SELECT `+assignmentColumns+`
		FROM learning.assignments
		WHERE org_id = $1 AND lesson_version_id = ANY($2)
		ORDER BY created_at DESC`, orgID, versionIDs)
	if err != nil {
		return nil, fmt.Errorf("learning: не удалось прочитать назначения: %w", err)
	}
	defer rows.Close()

	var out []Assignment
	for rows.Next() {
		a, err := scanAssignment(rows)
		if err != nil {
			return nil, fmt.Errorf("learning: не удалось прочитать назначения: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ListForClasses — назначения нескольких классов одним запросом.
//
// Нужно ученику: он может состоять в нескольких классах, и запрос на класс
// превратился бы в N+1 на экране «мои уроки».
func (s *Store) ListForClasses(ctx context.Context, q pg.Querier, orgID uuid.UUID,
	classIDs []uuid.UUID) ([]Assignment, error) {
	if len(classIDs) == 0 {
		return nil, nil
	}
	rows, err := q.Query(ctx, `
		SELECT `+assignmentColumns+`
		FROM learning.assignments
		WHERE org_id = $1 AND class_id = ANY($2)
		ORDER BY COALESCE(closes_at, 'infinity'::timestamptz), created_at DESC`, orgID, classIDs)
	if err != nil {
		return nil, fmt.Errorf("learning: не удалось прочитать назначения: %w", err)
	}
	defer rows.Close()

	var out []Assignment
	for rows.Next() {
		a, err := scanAssignment(rows)
		if err != nil {
			return nil, fmt.Errorf("learning: не удалось прочитать назначения: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// OverlappingExists ищет незакрытое назначение той же версии тому же классу
// с пересекающимся периодом.
//
// Пересечение считается по правилу «начало одного раньше конца другого»:
// два одинаковых урока, доступных одновременно, — это ошибка учителя, а не
// сценарий. Последовательные периоды при этом разрешены.
func (s *Store) OverlappingExists(ctx context.Context, q pg.Querier, orgID, classID,
	versionID uuid.UUID, opensAt, closesAt *time.Time, excludeID uuid.UUID) (bool, error) {
	var exists bool
	err := q.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM learning.assignments
			WHERE org_id = $1 AND class_id = $2 AND lesson_version_id = $3
			  AND state <> 'closed'
			  AND id <> $6
			  AND COALESCE($4::timestamptz, '-infinity') < COALESCE(closes_at, 'infinity')
			  AND COALESCE(opens_at, '-infinity') < COALESCE($5::timestamptz, 'infinity')
		)`, orgID, classID, versionID, opensAt, closesAt, excludeID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("learning: не удалось проверить пересечение: %w", err)
	}
	return exists, nil
}

func (s *Store) UpdateWindow(ctx context.Context, q pg.Querier, orgID, id uuid.UUID,
	opensAt, closesAt *time.Time, state string) error {
	tag, err := q.Exec(ctx, `
		UPDATE learning.assignments SET opens_at = $3, closes_at = $4, state = $5,
			closed_at = CASE WHEN $5 = 'closed' THEN COALESCE(closed_at, now()) ELSE NULL END
		WHERE id = $1 AND org_id = $2`, id, orgID, opensAt, closesAt, state)
	if err != nil {
		return fmt.Errorf("learning: не удалось обновить назначение: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetState меняет состояние назначения. Закрытие запоминает и момент: без
// него страница класса может сказать только «закрыт», а спрашивают, когда.
func (s *Store) SetState(ctx context.Context, q pg.Querier, orgID, id uuid.UUID, state string) error {
	tag, err := q.Exec(ctx, `
		UPDATE learning.assignments
		SET state = $3,
		    closed_at = CASE WHEN $3 = 'closed' THEN COALESCE(closed_at, now()) ELSE NULL END
		WHERE id = $1 AND org_id = $2`, id, orgID, state)
	if err != nil {
		return fmt.Errorf("learning: не удалось изменить состояние: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteAssignment удаляет назначение целиком.
// Разрешено только когда попыток не было — иначе ответы стали бы сиротами.
func (s *Store) DeleteAssignment(ctx context.Context, q pg.Querier, orgID, id uuid.UUID) error {
	tag, err := q.Exec(ctx, `DELETE FROM learning.assignments WHERE id = $1 AND org_id = $2`, id, orgID)
	if err != nil {
		return fmt.Errorf("learning: не удалось удалить назначение: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SyncStates переводит запланированные назначения в активные, когда
// наступило время открытия. Возвращает число изменённых.
//
// Одним запросом на всю таблицу, а не по строке: задача идемпотентна,
// повторный запуск ничего не меняет — условие WHERE отсекает уже
// переведённые записи.
//
// По сроку назначения больше не закрываются: с выдачей по заданиям срок
// живёт у выдачи и проверяется при чтении, а закрытое назначение значит
// «урок снят с класса». Закрой задача урок по старому closes_at — учитель
// не смог бы продлить перенесённую домашку.
func (s *Store) SyncStates(ctx context.Context, q pg.Querier, now time.Time) (int64, error) {
	tag, err := q.Exec(ctx, `
		UPDATE learning.assignments
		SET state = 'active'
		WHERE state = 'scheduled' AND (opens_at IS NULL OR opens_at <= $1)`, now)
	if err != nil {
		return 0, fmt.Errorf("learning: не удалось обновить состояния назначений: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ---------------------------------------------------------------------------
// Попытки
// ---------------------------------------------------------------------------

const attemptColumns = `id, org_id, assignment_id, student_id, current_step, score,
	correct_answers, total_answers, total_questions, answers, completed,
	started_at, last_seen_at, finished_at`

func scanAttempt(row pg.Row) (Attempt, error) {
	var a Attempt
	var answers []byte
	err := row.Scan(&a.ID, &a.OrgID, &a.AssignmentID, &a.StudentID, &a.CurrentStep,
		&a.Score, &a.CorrectAnswers, &a.TotalAnswers, &a.TotalQuestions, &answers, &a.Completed,
		&a.StartedAt, &a.LastSeenAt, &a.FinishedAt)
	if err == nil && len(answers) > 0 {
		// Разбор ответов — не повод не отдать попытку: без них урок
		// откроется заново, а не сломается.
		_ = json.Unmarshal(answers, &a.Answers)
	}
	if a.Answers == nil {
		a.Answers = map[string]GivenAnswer{}
	}
	return a, err
}

// StartAttempt создаёт попытку или возвращает начатую.
//
// ON CONFLICT DO UPDATE, а не «сначала прочитать, потом вставить»: двойной
// клик по «начать урок» не должен создавать вторую попытку, а гонку между
// двумя устройствами ученика уникальный индекс поймает и без нас.
func (s *Store) StartAttempt(ctx context.Context, q pg.Querier, a Attempt) (Attempt, error) {
	row := q.QueryRow(ctx, `
		INSERT INTO learning.attempts (id, org_id, assignment_id, student_id, total_questions)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (assignment_id, student_id) DO UPDATE
		SET last_seen_at = now(),
		    -- Число вопросов урока могло стать известно только сейчас:
		    -- у попыток, начатых до этой правки, оно нулевое.
		    total_questions = GREATEST(learning.attempts.total_questions, EXCLUDED.total_questions)
		RETURNING `+attemptColumns,
		a.ID, a.OrgID, a.AssignmentID, a.StudentID, a.TotalQuestions)
	out, err := scanAttempt(row)
	if err != nil {
		return Attempt{}, fmt.Errorf("learning: не удалось начать попытку: %w", err)
	}
	return out, nil
}

func (s *Store) AttemptByID(ctx context.Context, q pg.Querier, orgID, id uuid.UUID) (Attempt, error) {
	a, err := scanAttempt(q.QueryRow(ctx,
		`SELECT `+attemptColumns+` FROM learning.attempts WHERE id = $1 AND org_id = $2`, id, orgID))
	if pg.IsNoRows(err) {
		return Attempt{}, ErrNotFound
	}
	if err != nil {
		return Attempt{}, fmt.Errorf("learning: не удалось прочитать попытку: %w", err)
	}
	return a, nil
}

func (s *Store) AttemptsOfStudent(ctx context.Context, q pg.Querier, orgID, studentID uuid.UUID) ([]Attempt, error) {
	rows, err := q.Query(ctx, `
		SELECT `+attemptColumns+` FROM learning.attempts
		WHERE org_id = $1 AND student_id = $2 ORDER BY last_seen_at DESC`, orgID, studentID)
	if err != nil {
		return nil, fmt.Errorf("learning: не удалось прочитать попытки: %w", err)
	}
	defer rows.Close()

	var out []Attempt
	for rows.Next() {
		a, err := scanAttempt(rows)
		if err != nil {
			return nil, fmt.Errorf("learning: не удалось прочитать попытки: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AttemptOfStudent — попытка ученика по назначению. Она одна: уникальный
// индекс (assignment_id, student_id). ErrNotFound — ученик урок не открывал.
func (s *Store) AttemptOfStudent(ctx context.Context, q pg.Querier, orgID, assignmentID,
	studentID uuid.UUID) (Attempt, error) {
	a, err := scanAttempt(q.QueryRow(ctx, `
		SELECT `+attemptColumns+` FROM learning.attempts
		WHERE org_id = $1 AND assignment_id = $2 AND student_id = $3`, orgID, assignmentID, studentID))
	if pg.IsNoRows(err) {
		return Attempt{}, ErrNotFound
	}
	if err != nil {
		return Attempt{}, fmt.Errorf("learning: не удалось прочитать попытку: %w", err)
	}
	return a, nil
}

// AttemptsOfAssignment — прогресс класса по одному назначению.
func (s *Store) AttemptsOfAssignment(ctx context.Context, q pg.Querier, orgID, assignmentID uuid.UUID) ([]Attempt, error) {
	rows, err := q.Query(ctx, `
		SELECT `+attemptColumns+` FROM learning.attempts
		WHERE org_id = $1 AND assignment_id = $2 ORDER BY last_seen_at DESC`, orgID, assignmentID)
	if err != nil {
		return nil, fmt.Errorf("learning: не удалось прочитать попытки: %w", err)
	}
	defer rows.Close()

	var out []Attempt
	for rows.Next() {
		a, err := scanAttempt(rows)
		if err != nil {
			return nil, fmt.Errorf("learning: не удалось прочитать попытки: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// CountAttempts — сколько учеников начало проходить назначение.
// Нужно, чтобы решить, можно ли назначение удалить или только закрыть.
func (s *Store) CountAttempts(ctx context.Context, q pg.Querier, orgID, assignmentID uuid.UUID) (int, error) {
	var n int
	err := q.QueryRow(ctx,
		`SELECT count(*) FROM learning.attempts WHERE org_id = $1 AND assignment_id = $2`,
		orgID, assignmentID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("learning: не удалось посчитать попытки: %w", err)
	}
	return n, nil
}

// AdvanceAttempt обновляет прогресс.
//
// Счётчики прибавляются в SQL, а не читаются и записываются в Go: два
// одновременных ответа с двух устройств иначе затирали бы друг друга.
// AdvanceAttempt засчитывает ответ, если этот вопрос ещё не отвечен.
//
// Условие `NOT (answers ? $6)` и `finished_at IS NULL` стоят прямо в запросе,
// а не проверяются кодом заранее: между проверкой и записью помещается
// второй такой же запрос из соседней вкладки, и счёт вырастал на каждом
// повторном нажатии того же варианта.
func (s *Store) AdvanceAttempt(ctx context.Context, q pg.Querier, orgID, id uuid.UUID,
	step, scoreDelta int, correct bool, key string, given GivenAnswer) (Attempt, error) {
	correctDelta := 0
	if correct {
		correctDelta = 1
	}
	answer, err := json.Marshal(map[string]GivenAnswer{key: given})
	if err != nil {
		return Attempt{}, fmt.Errorf("learning: не удалось записать ответ: %w", err)
	}
	row := q.QueryRow(ctx, `
		UPDATE learning.attempts
		SET current_step = GREATEST(current_step, $3),
		    score = score + $4,
		    correct_answers = correct_answers + $5,
		    total_answers = total_answers + 1,
		    answers = answers || $7::jsonb,
		    last_seen_at = now()
		WHERE id = $1 AND org_id = $2 AND finished_at IS NULL AND NOT (answers ? $6)
		RETURNING `+attemptColumns,
		id, orgID, step, scoreDelta, correctDelta, key, answer)
	out, err := scanAttempt(row)
	if pg.IsNoRows(err) {
		return Attempt{}, ErrNotFound
	}
	if err != nil {
		return Attempt{}, fmt.Errorf("learning: не удалось обновить попытку: %w", err)
	}
	return out, nil
}

// SetAttemptStep запоминает, до какого шага дошёл ученик.
//
// GREATEST, а не присваивание: ученик листает и назад, и вперёд, а вопрос,
// на который отвечает это поле, — «где он остановился», а не «что открыто
// прямо сейчас».
func (s *Store) SetAttemptStep(ctx context.Context, q pg.Querier, orgID, id uuid.UUID, step int) error {
	tag, err := q.Exec(ctx, `
		UPDATE learning.attempts SET current_step = GREATEST(current_step, $3), last_seen_at = now()
		WHERE id = $1 AND org_id = $2`, id, orgID, step)
	if err != nil {
		return fmt.Errorf("learning: не удалось сохранить шаг: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// FinishAttempt завершает попытку и решает, пройден урок или пролистан.
//
// Порог — половина вопросов урока: девять нажатий «Дальше» без единого
// ответа это не пройденный урок, а учитель по этой отметке решает, закрыта
// тема или нет.
func (s *Store) FinishAttempt(ctx context.Context, q pg.Querier, orgID, id uuid.UUID) error {
	tag, err := q.Exec(ctx, `
		UPDATE learning.attempts
		SET finished_at = COALESCE(finished_at, now()),
		    completed = (total_questions = 0
		                 OR (SELECT count(*) FROM jsonb_object_keys(answers)) * 2 >= total_questions),
		    last_seen_at = now()
		WHERE id = $1 AND org_id = $2`, id, orgID)
	if err != nil {
		return fmt.Errorf("learning: не удалось завершить попытку: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------------------------------------------------------------------------
// События ответов
// ---------------------------------------------------------------------------

func (s *Store) InsertAnswerEvent(ctx context.Context, q pg.Querier, e AnswerEvent) error {
	_, err := q.Exec(ctx, `
		INSERT INTO learning.answer_events
			(id, org_id, attempt_id, student_id, exercise_id, step, answer, is_correct,
			 score_delta, exercise_kind)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		e.ID, e.OrgID, e.AttemptID, e.StudentID, e.ExerciseID, e.Step, e.Answer,
		e.IsCorrect, e.ScoreDelta, e.Kind)
	if err != nil {
		return fmt.Errorf("learning: не удалось записать событие ответа: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Реплики диалога с аватаром
// ---------------------------------------------------------------------------

// InsertDialogTurn записывает одну реплику.
//
// Сам по себе client_msg_id не уникален в секционированном журнале.
// Идемпотентность обеспечивают RecordLiveTurn и аренда текстовой операции.
func (s *Store) InsertDialogTurn(ctx context.Context, q pg.Querier, t DialogTurn) error {
	_, err := q.Exec(ctx, `
		INSERT INTO learning.dialog_turns
			(id, org_id, attempt_id, student_id, role, content, client_msg_id)
		VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, ''))`,
		t.ID, t.OrgID, t.AttemptID, t.StudentID, t.Role, t.Content, t.ClientMsgID)
	if err != nil {
		return fmt.Errorf("learning: не удалось записать реплику: %w", err)
	}
	return nil
}

// DialogTurns отдаёт историю разговора по попытке.
func (s *Store) DialogTurns(ctx context.Context, q pg.Querier, orgID, attemptID uuid.UUID) ([]DialogTurn, error) {
	rows, err := q.Query(ctx, `
		SELECT id, org_id, attempt_id, student_id, role, content,
		       COALESCE(client_msg_id, ''), occurred_at
		FROM learning.dialog_turns
		WHERE org_id = $1 AND attempt_id = $2
		ORDER BY occurred_at`, orgID, attemptID)
	if err != nil {
		return nil, fmt.Errorf("learning: не удалось прочитать диалог: %w", err)
	}
	defer rows.Close()

	var out []DialogTurn
	for rows.Next() {
		var t DialogTurn
		if err := rows.Scan(&t.ID, &t.OrgID, &t.AttemptID, &t.StudentID, &t.Role,
			&t.Content, &t.ClientMsgID, &t.OccurredAt); err != nil {
			return nil, fmt.Errorf("learning: не удалось прочитать диалог: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// AvatarProgress — состояние разговора при попытке.
//
// Всё в одной строке: состояние llm_service, счётчик реплик, число открытых
// голосовых сессий и кеш разбора. Читается одним запросом, потому что
// спрашивают их всегда вместе.
type AvatarProgress struct {
	State         []byte
	Turns         int
	Sessions      int
	Feedback      string
	FeedbackTurns int
}

// AvatarProgress читает состояние разговора целиком.
func (s *Store) AvatarProgress(ctx context.Context, q pg.Querier, orgID, attemptID uuid.UUID) (AvatarProgress, error) {
	var out AvatarProgress
	err := q.QueryRow(ctx,
		`SELECT avatar_state, avatar_turns, avatar_sessions, avatar_feedback, avatar_feedback_turns
		 FROM learning.attempts WHERE id = $1 AND org_id = $2`,
		attemptID, orgID).Scan(&out.State, &out.Turns, &out.Sessions, &out.Feedback, &out.FeedbackTurns)
	if pg.IsNoRows(err) {
		return AvatarProgress{}, ErrNotFound
	}
	if err != nil {
		return AvatarProgress{}, fmt.Errorf("learning: не удалось прочитать состояние диалога: %w", err)
	}
	return out, nil
}

// AddAvatarTurn засчитывает реплику ученика в голосовом разговоре, где
// состояния llm_service нет, и возвращает новое число реплик.
func (s *Store) AddAvatarTurn(ctx context.Context, q pg.Querier, orgID, attemptID uuid.UUID) (int, error) {
	var turns int
	err := q.QueryRow(ctx,
		`UPDATE learning.attempts SET avatar_turns = avatar_turns + 1, last_seen_at = now()
		 WHERE id = $1 AND org_id = $2 RETURNING avatar_turns`, attemptID, orgID).Scan(&turns)
	if pg.IsNoRows(err) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("learning: не удалось засчитать реплику: %w", err)
	}
	return turns, nil
}

// AddLiveSession засчитывает открытую голосовую сессию.
//
// Сессия платная и живёт вне сервера: без этого счётчика цикл «открой
// сессию» тратит деньги, не оставляя следа ни в одной реплике.
func (s *Store) AddLiveSession(ctx context.Context, q pg.Querier, orgID, attemptID uuid.UUID) (int, error) {
	var sessions int
	err := q.QueryRow(ctx,
		`UPDATE learning.attempts SET avatar_sessions = avatar_sessions + 1, last_seen_at = now()
		 WHERE id = $1 AND org_id = $2 RETURNING avatar_sessions`, attemptID, orgID).Scan(&sessions)
	if pg.IsNoRows(err) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("learning: не удалось открыть голосовой разговор: %w", err)
	}
	return sessions, nil
}

// ReturnLiveSession возвращает место сессии, которая не открылась: отказ
// шлюза («линии заняты»), таймаут, уход ученика. Иначе несколько неудачных
// попыток в час пик закрывали голос в уроке навсегда — «исчерпан».
func (s *Store) ReturnLiveSession(ctx context.Context, q pg.Querier, orgID, attemptID uuid.UUID) error {
	_, err := q.Exec(ctx,
		`UPDATE learning.attempts SET avatar_sessions = GREATEST(avatar_sessions - 1, 0)
		 WHERE id = $1 AND org_id = $2`, attemptID, orgID)
	if err != nil {
		return fmt.Errorf("learning: не удалось вернуть место голосовой сессии: %w", err)
	}
	return nil
}

// SaveAvatarFeedback запоминает разбор разговора и длину, на которой он
// посчитан: пока разговор не продолжился, повторный вызов модели не нужен.
func (s *Store) SaveAvatarFeedback(ctx context.Context, q pg.Querier, orgID, attemptID uuid.UUID,
	feedback string, turns int) error {
	_, err := q.Exec(ctx,
		`UPDATE learning.attempts SET avatar_feedback = $3, avatar_feedback_turns = $4
		 WHERE id = $1 AND org_id = $2`, attemptID, orgID, feedback, turns)
	if err != nil {
		return fmt.Errorf("learning: не удалось сохранить разбор разговора: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Карточка ученика
// ---------------------------------------------------------------------------

// AttemptsInClass — попытки ученика по урокам одного класса.
//
// Фильтр по классу здесь, а не после выборки в Go: ученик бывает в двух
// классах, и карточка одного класса не должна показывать работу из другого.
// Подзапрос в своей же схеме, границу модулей он не пересекает.
func (s *Store) AttemptsInClass(ctx context.Context, q pg.Querier, orgID, classID, studentID uuid.UUID) ([]Attempt, error) {
	rows, err := q.Query(ctx, `
		SELECT `+attemptColumns+` FROM learning.attempts
		WHERE org_id = $1 AND student_id = $3
		  AND assignment_id IN (SELECT id FROM learning.assignments WHERE org_id = $1 AND class_id = $2)`,
		orgID, classID, studentID)
	if err != nil {
		return nil, fmt.Errorf("learning: не удалось прочитать попытки: %w", err)
	}
	defer rows.Close()

	var out []Attempt
	for rows.Next() {
		a, err := scanAttempt(rows)
		if err != nil {
			return nil, fmt.Errorf("learning: не удалось прочитать попытки: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Аналитика класса
//
// Таблица «ученики × уроки» читает попытки класса без ответов: клетке нужны
// счётчики попытки, а не сами ответы. Ответы и события ответов дочитываются
// только при открытии клетки. org_id стоит в каждом запросе, включая
// вложенные: чужой тенант не должен находиться даже по известному
// идентификатору класса.
// ---------------------------------------------------------------------------

// ClassAttempt — попытка ученика в таблице результатов класса.
type ClassAttempt struct {
	ID             uuid.UUID
	AssignmentID   uuid.UUID
	StudentID      uuid.UUID
	Completed      bool
	CorrectAnswers int
	TotalQuestions int
	LastSeenAt     time.Time
}

// ClassAttempts — все попытки по урокам класса одним запросом.
func (s *Store) ClassAttempts(ctx context.Context, q pg.Querier, orgID, classID uuid.UUID) ([]ClassAttempt, error) {
	rows, err := q.Query(ctx, `
		SELECT id, assignment_id, student_id, completed, correct_answers, total_questions, last_seen_at
		FROM learning.attempts
		WHERE org_id = $1
		  AND assignment_id IN (SELECT id FROM learning.assignments WHERE org_id = $1 AND class_id = $2)`,
		orgID, classID)
	if err != nil {
		return nil, fmt.Errorf("learning: не удалось прочитать попытки класса: %w", err)
	}
	defer rows.Close()

	var out []ClassAttempt
	for rows.Next() {
		var a ClassAttempt
		if err := rows.Scan(&a.ID, &a.AssignmentID, &a.StudentID, &a.Completed,
			&a.CorrectAnswers, &a.TotalQuestions, &a.LastSeenAt); err != nil {
			return nil, fmt.Errorf("learning: не удалось прочитать попытки класса: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AssignedClass — пара «версия урока — класс» из журнала назначений.
type AssignedClass struct {
	VersionID uuid.UUID
	ClassID   uuid.UUID
}

// AssignedClasses отдаёт все пары «версия — класс» организации.
//
// Закрытые назначения тоже считаются: урок в классе был, и библиотека обязана
// это показать, прежде чем учитель нажмёт «удалить». Снятое назначение строки
// не оставляет — его удаляет Unassign.
//
// DISTINCT и две колонки вместо строк назначений: пар столько же, сколько
// выдач урока в класс, и всё это — один индексный проход по org_id.
func (s *Store) AssignedClasses(ctx context.Context, q pg.Querier, orgID uuid.UUID) ([]AssignedClass, error) {
	rows, err := q.Query(ctx, `
		SELECT DISTINCT lesson_version_id, class_id
		FROM learning.assignments
		WHERE org_id = $1
		ORDER BY lesson_version_id, class_id`, orgID)
	if err != nil {
		return nil, fmt.Errorf("learning: не удалось прочитать назначения: %w", err)
	}
	defer rows.Close()

	var out []AssignedClass
	for rows.Next() {
		var p AssignedClass
		if err := rows.Scan(&p.VersionID, &p.ClassID); err != nil {
			return nil, fmt.Errorf("learning: не удалось прочитать назначения: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// errDialogEnded — разговор попытки уже закрыт: новые реплики не принимаются.
var errDialogEnded = errors.New("разговор закончен")

// RecordLiveTurn сохраняет реплику и счётчик атомарно. Блокировка попытки
// делает повтор после потери HTTP-ответа идемпотентным без индекса по партициям.
func (s *Store) RecordLiveTurn(ctx context.Context, pool *pg.Pool, t DialogTurn, maxTurns int, issueID uuid.UUID) (int, error) {
	taken := 0
	err := pg.InTx(ctx, pool, func(tx pgx.Tx) error {
		if err := s.LockOpenIssue(ctx, tx, t.OrgID, issueID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT avatar_turns FROM learning.attempts
            WHERE id=$1 AND org_id=$2 FOR UPDATE`, t.AttemptID, t.OrgID).Scan(&taken); err != nil {
			return err
		}
		if t.ClientMsgID != "" {
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM learning.dialog_turns
                WHERE org_id=$1 AND attempt_id=$2 AND client_msg_id=$3)`, t.OrgID, t.AttemptID, t.ClientMsgID).Scan(&exists); err != nil {
				return err
			}
			if exists {
				return nil
			}
		}
		if t.Role == RoleStudent && taken >= maxTurns {
			return errDialogEnded
		}
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM (SELECT 1 FROM learning.dialog_turns
			WHERE org_id=$1 AND attempt_id=$2 LIMIT $3) AS turns`, t.OrgID, t.AttemptID, liveTurnsCap(maxTurns)).Scan(&count); err != nil {
			return err
		}
		if count >= liveTurnsCap(maxTurns) {
			return errDialogEnded
		}
		if err := s.InsertDialogTurn(ctx, tx, t); err != nil {
			return err
		}
		if t.Role == RoleStudent {
			var err error
			taken, err = s.AddAvatarTurn(ctx, tx, t.OrgID, t.AttemptID)
			return err
		}
		return nil
	})
	return taken, err
}
