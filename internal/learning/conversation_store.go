package learning

// SQL разговора по целям урока: колонки миграции 0021 в learning.attempts и
// learning.dialog_turns и колонки живого режима (0022).

import (
	"context"
	"fmt"
	"time"

	"github.com/NATIVE/NATIVE/internal/platform/pg"
	"github.com/google/uuid"
)

// ConversationRow — разговор по целям в строке попытки.
type ConversationRow struct {
	AttemptID uuid.UUID
	StudentID uuid.UUID
	// Engine — состояние движка как есть; nil — движок не запускался.
	// Читается только там, где оно нужно: у отчёта учителя по классу его нет,
	// там достаточно HasEngine.
	Engine    []byte
	HasEngine bool
	// Turns — засчитанные ответы ученика (avatar_turns): общий счётчик со
	// старыми режимами, по нему и решается, исчерпан ли разговор.
	Turns int
	// Sessions — открытые голосовые сессии (avatar_sessions): общий счётчик
	// живого режима и голосового диалога прежнего режима.
	Sessions        int
	ClosedAt        *time.Time
	Report          []byte
	ReportStatus    string
	ReportStartedAt *time.Time
	Incidents       []byte
}

const conversationColumns = `id, student_id, avatar_engine, avatar_engine IS NOT NULL, avatar_turns,
	avatar_sessions, avatar_closed_at, avatar_report, avatar_report_status, avatar_report_started_at,
	avatar_incidents`

// Отчёт учителя по классу состояние движка не читает: оно бывает крупным, а
// нужно ему только «начат ли разговор».
const classConversationColumns = `id, student_id, NULL::jsonb, avatar_engine IS NOT NULL, avatar_turns,
	avatar_sessions, avatar_closed_at, avatar_report, avatar_report_status, avatar_report_started_at,
	avatar_incidents`

func scanConversation(row pg.Row) (ConversationRow, error) {
	var c ConversationRow
	err := row.Scan(&c.AttemptID, &c.StudentID, &c.Engine, &c.HasEngine, &c.Turns, &c.Sessions, &c.ClosedAt,
		&c.Report, &c.ReportStatus, &c.ReportStartedAt, &c.Incidents)
	return c, err
}

// ConversationOf — разговор по целям при попытке. В транзакции после
// LockAttempt читает уже заблокированную строку.
func (s *Store) ConversationOf(ctx context.Context, q pg.Querier, orgID, attemptID uuid.UUID) (ConversationRow, error) {
	c, err := scanConversation(q.QueryRow(ctx, `
		SELECT `+conversationColumns+` FROM learning.attempts
		WHERE id = $1 AND org_id = $2`, attemptID, orgID))
	if pg.IsNoRows(err) {
		return ConversationRow{}, ErrNotFound
	}
	if err != nil {
		return ConversationRow{}, fmt.Errorf("learning: не удалось прочитать разговор: %w", err)
	}
	return c, nil
}

// ConversationsOfAssignment — разговоры всех попыток урока одним запросом:
// отчёт учителя по классу не должен ходить в базу на каждого ученика.
func (s *Store) ConversationsOfAssignment(ctx context.Context, q pg.Querier, orgID,
	assignmentID uuid.UUID) ([]ConversationRow, error) {
	rows, err := q.Query(ctx, `
		SELECT `+classConversationColumns+` FROM learning.attempts
		WHERE org_id = $1 AND assignment_id = $2`, orgID, assignmentID)
	if err != nil {
		return nil, fmt.Errorf("learning: не удалось прочитать разговоры класса: %w", err)
	}
	defer rows.Close()
	var out []ConversationRow
	for rows.Next() {
		c, err := scanConversation(rows)
		if err != nil {
			return nil, fmt.Errorf("learning: не удалось прочитать разговоры класса: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ConversationsOfAttempts — разговоры нескольких попыток одним запросом:
// карточке ученика нужен итог разговора в каждой строке урока, и запрос на
// строку превратил бы её в N+1.
func (s *Store) ConversationsOfAttempts(ctx context.Context, q pg.Querier, orgID uuid.UUID,
	attemptIDs []uuid.UUID) ([]ConversationRow, error) {
	if len(attemptIDs) == 0 {
		return nil, nil
	}
	rows, err := q.Query(ctx, `
		SELECT `+classConversationColumns+` FROM learning.attempts
		WHERE org_id = $1 AND id = ANY($2)`, orgID, attemptIDs)
	if err != nil {
		return nil, fmt.Errorf("learning: не удалось прочитать разговоры: %w", err)
	}
	defer rows.Close()
	var out []ConversationRow
	for rows.Next() {
		c, err := scanConversation(rows)
		if err != nil {
			return nil, fmt.Errorf("learning: не удалось прочитать разговоры: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ConversationsOfClass — разговоры всех попыток по урокам класса одним
// запросом: таблица результатов показывает итог разговора в каждой клетке.
func (s *Store) ConversationsOfClass(ctx context.Context, q pg.Querier, orgID,
	classID uuid.UUID) ([]ConversationRow, error) {
	rows, err := q.Query(ctx, `
		SELECT `+classConversationColumns+` FROM learning.attempts
		WHERE org_id = $1
		  AND assignment_id IN (SELECT id FROM learning.assignments WHERE org_id = $1 AND class_id = $2)
		  AND (avatar_turns > 0 OR avatar_engine IS NOT NULL)`, orgID, classID)
	if err != nil {
		return nil, fmt.Errorf("learning: не удалось прочитать разговоры класса: %w", err)
	}
	defer rows.Close()
	var out []ConversationRow
	for rows.Next() {
		c, err := scanConversation(rows)
		if err != nil {
			return nil, fmt.Errorf("learning: не удалось прочитать разговоры класса: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ConversationOfStudent — разговор одного ученика по уроку. ErrNotFound —
// ученик урок не открывал.
func (s *Store) ConversationOfStudent(ctx context.Context, q pg.Querier, orgID, assignmentID,
	studentID uuid.UUID) (ConversationRow, error) {
	c, err := scanConversation(q.QueryRow(ctx, `
		SELECT `+classConversationColumns+` FROM learning.attempts
		WHERE org_id = $1 AND assignment_id = $2 AND student_id = $3`, orgID, assignmentID, studentID))
	if pg.IsNoRows(err) {
		return ConversationRow{}, ErrNotFound
	}
	if err != nil {
		return ConversationRow{}, fmt.Errorf("learning: не удалось прочитать разговор: %w", err)
	}
	return c, nil
}

const conversationTurnColumns = `id, org_id, attempt_id, student_id, role, content,
	COALESCE(client_msg_id, ''), occurred_at, turn_no, input, COALESCE(asr_text, ''),
	asr_confidence, edited, COALESCE(translation, ''), COALESCE(steer, ''), steer_turn`

// ConversationTurns — все реплики попытки со всеми полями, включая реплики
// прежних режимов. Порядок — по времени; номера ходов раскладывает сервис.
func (s *Store) ConversationTurns(ctx context.Context, q pg.Querier, orgID, attemptID uuid.UUID) ([]DialogTurn, error) {
	rows, err := q.Query(ctx, `
		SELECT `+conversationTurnColumns+`
		FROM learning.dialog_turns
		WHERE org_id = $1 AND attempt_id = $2
		ORDER BY occurred_at, id`, orgID, attemptID)
	if err != nil {
		return nil, fmt.Errorf("learning: не удалось прочитать разговор: %w", err)
	}
	defer rows.Close()
	var out []DialogTurn
	for rows.Next() {
		var t DialogTurn
		if err := rows.Scan(&t.ID, &t.OrgID, &t.AttemptID, &t.StudentID, &t.Role, &t.Content,
			&t.ClientMsgID, &t.OccurredAt, &t.TurnNo, &t.Input, &t.ASRText, &t.ASRConfidence,
			&t.Edited, &t.Translation, &t.Steer, &t.SteerTurn); err != nil {
			return nil, fmt.Errorf("learning: не удалось прочитать разговор: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// InsertConversationTurn записывает реплику разговора по целям со всеми
// полями. Старые режимы пишут через InsertDialogTurn и получают умолчания
// новых колонок.
func (s *Store) InsertConversationTurn(ctx context.Context, q pg.Querier, t DialogTurn) error {
	input := t.Input
	if input == "" {
		input = InputText
	}
	_, err := q.Exec(ctx, `
		INSERT INTO learning.dialog_turns
			(id, org_id, attempt_id, student_id, role, content, client_msg_id,
			 turn_no, input, asr_text, asr_confidence, edited, translation, steer, steer_turn)
		VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, ''), $8, $9, NULLIF($10, ''), $11, $12, NULLIF($13, ''),
			NULLIF($14, ''), $15)`,
		t.ID, t.OrgID, t.AttemptID, t.StudentID, t.Role, t.Content, t.ClientMsgID,
		t.TurnNo, input, t.ASRText, t.ASRConfidence, t.Edited, t.Translation, t.Steer, t.SteerTurn)
	if err != nil {
		return fmt.Errorf("learning: не удалось записать реплику: %w", err)
	}
	return nil
}

// SetAnswerSteer записывает указание живой сессии при ответе ученика. Один
// раз: указание уже ушло ученику, и повтор ответа обязан отдать то же.
// false — указание уже было (его записал соседний запрос).
//
// occurred_at — ключ секции: без него запрос обходил бы все недели таблицы.
func (s *Store) SetAnswerSteer(ctx context.Context, q pg.Querier, orgID, attemptID, turnID uuid.UUID,
	occurredAt time.Time, steer string) (bool, error) {
	tag, err := q.Exec(ctx, `
		UPDATE learning.dialog_turns SET steer = $5
		WHERE org_id = $1 AND attempt_id = $2 AND id = $3 AND occurred_at = $4
		  AND role = 'student' AND steer IS NULL`, orgID, attemptID, turnID, occurredAt, steer)
	if err != nil {
		return false, fmt.Errorf("learning: не удалось записать указание сессии: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// SetTurnTranslation — перевод реплики аватара, полученный после записи
// реплики: реплика из живой сессии пишется сразу, перевод приходит позже.
func (s *Store) SetTurnTranslation(ctx context.Context, q pg.Querier, orgID, attemptID, turnID uuid.UUID,
	occurredAt time.Time, translation string) error {
	_, err := q.Exec(ctx, `
		UPDATE learning.dialog_turns SET translation = NULLIF($5, '')
		WHERE org_id = $1 AND attempt_id = $2 AND id = $3 AND occurred_at = $4 AND role = 'avatar'`,
		orgID, attemptID, turnID, occurredAt, translation)
	if err != nil {
		return fmt.Errorf("learning: не удалось записать перевод реплики: %w", err)
	}
	return nil
}

// CountAnswer засчитывает ответ ученика и, если он N-й, закрывает разговор
// тем же запросом. Возвращает число ответов и момент закрытия.
//
// Прибавление в SQL, а не запись прочитанного: вызывается под блокировкой
// попытки, но счётчик общий со старыми режимами, которые блокировку не
// берут.
func (s *Store) CountAnswer(ctx context.Context, q pg.Querier, orgID, attemptID uuid.UUID,
	maxTurns int) (int, *time.Time, error) {
	var turns int
	var closedAt *time.Time
	err := q.QueryRow(ctx, `
		UPDATE learning.attempts
		SET avatar_turns = avatar_turns + 1,
		    avatar_closed_at = CASE WHEN avatar_turns + 1 >= $3
		                            THEN COALESCE(avatar_closed_at, now())
		                            ELSE avatar_closed_at END,
		    last_seen_at = now()
		WHERE id = $1 AND org_id = $2
		RETURNING avatar_turns, avatar_closed_at`, attemptID, orgID, maxTurns).Scan(&turns, &closedAt)
	if pg.IsNoRows(err) {
		return 0, nil, ErrNotFound
	}
	if err != nil {
		return 0, nil, fmt.Errorf("learning: не удалось засчитать ответ: %w", err)
	}
	return turns, closedAt, nil
}

// StartEngine кладёт первое состояние движка. false — его уже положила
// соседняя вкладка: начинать второй раз нельзя, вступление уже записано.
func (s *Store) StartEngine(ctx context.Context, q pg.Querier, orgID, attemptID uuid.UUID, state []byte) (bool, error) {
	tag, err := q.Exec(ctx, `
		UPDATE learning.attempts SET avatar_engine = $3, last_seen_at = now()
		WHERE id = $1 AND org_id = $2 AND avatar_engine IS NULL`, attemptID, orgID, state)
	if err != nil {
		return false, fmt.Errorf("learning: не удалось начать разговор: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// SetEngine — новое состояние движка. Решение, можно ли его писать, сервис
// принимает под блокировкой попытки.
func (s *Store) SetEngine(ctx context.Context, q pg.Querier, orgID, attemptID uuid.UUID, state []byte) error {
	_, err := q.Exec(ctx, `
		UPDATE learning.attempts SET avatar_engine = $3, last_seen_at = now()
		WHERE id = $1 AND org_id = $2`, attemptID, orgID, state)
	if err != nil {
		return fmt.Errorf("learning: не удалось сохранить разговор: %w", err)
	}
	return nil
}

// CloseConversation закрывает разговор. Повтор ничего не меняет: момент
// закрытия — первый.
func (s *Store) CloseConversation(ctx context.Context, q pg.Querier, orgID, attemptID uuid.UUID) error {
	_, err := q.Exec(ctx, `
		UPDATE learning.attempts SET avatar_closed_at = COALESCE(avatar_closed_at, now())
		WHERE id = $1 AND org_id = $2`, attemptID, orgID)
	if err != nil {
		return fmt.Errorf("learning: не удалось закрыть разговор: %w", err)
	}
	return nil
}

// AddIncident дописывает сбой в журнал попытки. Потолок — в самом запросе:
// журнал пополняет и браузер, и верить ему в числе записей нельзя.
func (s *Store) AddIncident(ctx context.Context, q pg.Querier, orgID, attemptID uuid.UUID,
	incident []byte, limit int) (bool, error) {
	tag, err := q.Exec(ctx, `
		UPDATE learning.attempts
		SET avatar_incidents = avatar_incidents || jsonb_build_array($3::jsonb)
		WHERE id = $1 AND org_id = $2 AND jsonb_array_length(avatar_incidents) < $4`,
		attemptID, orgID, incident, limit)
	if err != nil {
		return false, fmt.Errorf("learning: не удалось записать сбой разговора: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// ClaimReport берёт аренду разбора. Возвращает момент аренды — по нему
// неудачу отмечает только тот, кто разбор и вёл.
//
// Всё решает один UPDATE: разбор нужен (разговор закрыт или длина
// исчерпана, отчёта нет), и он не идёт прямо сейчас — либо не начинался,
// либо завис дольше lease, либо упал не позже retry назад. Две вкладки и
// два процесса, пришедшие разом, получат аренду один раз. Исчерпанный, но
// не закрытый разговор (прежние режимы) закрывается тем же запросом.
func (s *Store) ClaimReport(ctx context.Context, q pg.Querier, orgID, attemptID uuid.UUID, maxTurns int,
	lease, retry time.Duration) (time.Time, bool, error) {
	var at time.Time
	err := q.QueryRow(ctx, `
		UPDATE learning.attempts
		SET avatar_closed_at = COALESCE(avatar_closed_at, now()),
		    avatar_report_status = 'pending',
		    avatar_report_started_at = now()
		WHERE id = $1 AND org_id = $2
		  AND avatar_report IS NULL
		  AND (avatar_closed_at IS NOT NULL OR ($3 > 0 AND avatar_turns >= $3))
		  AND (avatar_report_status = ''
		       OR (avatar_report_status = 'pending' AND (avatar_report_started_at IS NULL
		           OR avatar_report_started_at < now() - make_interval(secs => $4)))
		       OR (avatar_report_status = 'failed' AND (avatar_report_started_at IS NULL
		           OR avatar_report_started_at < now() - make_interval(secs => $5))))
		RETURNING avatar_report_started_at`,
		attemptID, orgID, maxTurns, lease.Seconds(), retry.Seconds()).Scan(&at)
	if pg.IsNoRows(err) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("learning: не удалось начать разбор разговора: %w", err)
	}
	return at, true, nil
}

// SaveReport пишет готовый отчёт. Один раз: отчёт учителя после
// завершения разговора не меняется, и второй разбор, закончившийся позже
// (аренда истекла, пока первый ещё шёл), ничего не перепишет. false —
// отчёт уже был.
//
// Рядом с отчётом — счёт целевых выражений: сколько засчитано и сколько их
// было. Из него горит звезда «Слова в разговоре», а «Мои занятия» читают его
// раз в 5 секунд — разбирать для этого отчёт целиком было бы дорого.
func (s *Store) SaveReport(ctx context.Context, q pg.Querier, orgID, attemptID uuid.UUID, report []byte,
	credited, total int) (bool, error) {
	tag, err := q.Exec(ctx, `
		UPDATE learning.attempts
		SET avatar_report = $3, avatar_report_status = 'ready',
		    avatar_phrases_credited = $4, avatar_phrases_total = $5
		WHERE id = $1 AND org_id = $2 AND avatar_report IS NULL`, attemptID, orgID, report, credited, total)
	if err != nil {
		return false, fmt.Errorf("learning: не удалось сохранить отчёт разговора: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// ConversationWords — счёт целевых выражений разговоров пачкой, по
// попыткам. Попытки без разговора в ответе есть: у них «разговора не было».
func (s *Store) ConversationWords(ctx context.Context, q pg.Querier, orgID uuid.UUID,
	attemptIDs []uuid.UUID) (map[uuid.UUID]ConversationWords, error) {
	out := make(map[uuid.UUID]ConversationWords, len(attemptIDs))
	if len(attemptIDs) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `
		SELECT id, avatar_closed_at IS NOT NULL, avatar_report_status,
		       avatar_phrases_credited, avatar_phrases_total
		FROM learning.attempts
		WHERE org_id = $1 AND id = ANY($2)`, orgID, attemptIDs)
	if err != nil {
		return nil, fmt.Errorf("learning: не удалось прочитать итоги разговоров: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var closed bool
		var status string
		var credited, total *int
		if err := rows.Scan(&id, &closed, &status, &credited, &total); err != nil {
			return nil, fmt.Errorf("learning: не удалось прочитать итоги разговоров: %w", err)
		}
		out[id] = conversationWords(closed, status, credited, total)
	}
	return out, rows.Err()
}

// conversationWords — итог разговора для звезды. Разбор готов — его счёт;
// разговор закрыт, а разбора ещё нет — «считаем»; иначе разговора не было.
func conversationWords(closed bool, status string, credited, total *int) ConversationWords {
	switch {
	case status == ReportReady && credited != nil && total != nil:
		return ConversationWords{State: WordsReady, Used: *credited, Total: *total}
	case closed || status != "":
		return ConversationWords{State: WordsPending}
	}
	return ConversationWords{State: WordsNone}
}

// FailReport отмечает неудачный разбор. Только своей арендой: если её уже
// перехватил следующий разбор, его «идёт» не превращается в «упал».
func (s *Store) FailReport(ctx context.Context, q pg.Querier, orgID, attemptID uuid.UUID, lease time.Time) error {
	_, err := q.Exec(ctx, `
		UPDATE learning.attempts SET avatar_report_status = 'failed'
		WHERE id = $1 AND org_id = $2 AND avatar_report IS NULL
		  AND avatar_report_status = 'pending' AND avatar_report_started_at = $3`, attemptID, orgID, lease)
	if err != nil {
		return fmt.Errorf("learning: не удалось отметить сбой разбора: %w", err)
	}
	return nil
}
