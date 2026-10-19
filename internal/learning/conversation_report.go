package learning

// Разбор закрытого разговора, итог ученику и отчёт учителю.
//
// Разбор (analyze) идёт в фоне с арендой в базе: avatar_report_status =
// 'pending' и avatar_report_started_at. Процесс API можно убить посреди
// разбора — аренда истечёт, и следующий запрос итога или отчёта учителя
// запустит разбор заново (И-1). Готовый отчёт пишется один раз и больше не
// меняется: учитель не должен видеть, как оценка ученика переписывается
// задним числом.

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/NATIVE/NATIVE/internal/iam"
	"github.com/NATIVE/NATIVE/internal/platform/httpx"
	"github.com/NATIVE/NATIVE/internal/platform/logx"
	"github.com/google/uuid"
)

const (
	// reportLease — разбор, начатый раньше, считается зависшим и
	// перезапускается. Больше reportBudget с запасом: живой разбор не
	// перехватывается.
	reportLease = 3 * time.Minute
	// reportRetry — неудачный разбор перезапускается не чаще раза в минуту:
	// итог ученик опрашивает часто, а каждый разбор — вызов модели на весь
	// разговор.
	reportRetry = time.Minute
	// reportBudget — сколько разбор может идти вместе с ожиданием слота в
	// пуле. Меньше аренды: разбор обязан закончиться (успехом или отказом)
	// раньше, чем его перезапустит следующий запрос.
	reportBudget = 170 * time.Second
	// reportConcurrency — разборов одновременно из одного процесса.
	reportConcurrency = 4
	// reportVersion — версия формы снимка отчёта.
	reportVersion = 1
)

// Видимость общей ошибки (раздел 1 договора).
const (
	VisibilityMain         = "main"
	VisibilityAboveLevel   = "above_level"
	VisibilityUnknownLevel = "unknown_level"
)

// Отметки по цели.
const (
	VerdictCredited = "credited"
	VerdictError    = "error"
	VerdictMiss     = "miss"
)

// ---------------------------------------------------------------------------
// Разбор: что Go читает из ответа analyze
// ---------------------------------------------------------------------------
//
// Снимок отчёта хранит ответ движка целиком; эти типы — только то, что нужно
// итогу ученика и отчёту учителя. Поле, которого здесь нет, из снимка не
// пропадает.

// Analysis — разбор разговора.
type Analysis struct {
	Goals         []AnalysisGoal  `json:"goals"`
	Marks         []GoalMark      `json:"marks"`
	GeneralErrors []GeneralError  `json:"general_errors"`
	Summary       AnalysisSummary `json:"summary"`
	SpeechLevel   json.RawMessage `json:"speech_level"`
}

// AnalysisGoal — цель с итогом по ней.
type AnalysisGoal struct {
	Key          string `json:"key"`
	Kind         string `json:"kind"`
	Title        string `json:"title"`
	Note         string `json:"note"`
	Credited     bool   `json:"credited"`
	CreditedTurn *int   `json:"credited_turn"`
	CorrectUses  int    `json:"correct_uses"`
	Errors       int    `json:"errors"`
	Misses       int    `json:"misses"`
}

// GoalMark — отметка по цели с цитатой из ответа ученика.
type GoalMark struct {
	TurnNo      int     `json:"turn_no"`
	GoalKey     string  `json:"goal_key"`
	Verdict     string  `json:"verdict"`
	ErrorKind   *string `json:"error_kind"`
	Quote       string  `json:"quote"`
	CorrectForm string  `json:"correct_form"`
	NoteRU      string  `json:"note_ru"`
}

// GeneralError — ошибка вне целей урока.
type GeneralError struct {
	TurnNo      int    `json:"turn_no"`
	Quote       string `json:"quote"`
	Type        string `json:"type"`
	CorrectForm string `json:"correct_form"`
	RuleRU      string `json:"rule_ru"`
	Level       string `json:"level"`
	LevelSource string `json:"level_source"`
	Visibility  string `json:"visibility"`
	GroupKey    string `json:"group_key"`
	GroupLabel  string `json:"group_label"`
}

// GoalRef — цель в итоге ученика.
type GoalRef struct {
	Key   string `json:"key"`
	Title string `json:"title"`
}

// AnalysisSummary — итог ученика, как его считает движок.
type AnalysisSummary struct {
	CreditedPhrases []GoalRef `json:"credited_phrases"`
	AppliedThemes   []GoalRef `json:"applied_themes"`
	PhrasesTotal    int       `json:"phrases_total"`
	ThemesTotal     int       `json:"themes_total"`
	NextTime        *NextTime `json:"next_time"`
}

// NextTime — одна вещь на следующий раз.
type NextTime struct {
	Source  string `json:"source"`
	TextRU  string `json:"text_ru"`
	Example string `json:"example"`
}

// parseAnalysis проверяет ответ analyze: это объект нужной формы. Сломанный
// ответ в неизменяемый отчёт не ложится — разбор отмечается неудачным и
// перезапустится.
func parseAnalysis(raw json.RawMessage) (Analysis, error) {
	var a Analysis
	trimmed := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(trimmed, "{") {
		return Analysis{}, errors.New("learning: разбор разговора не объект")
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return Analysis{}, err
	}
	return a, nil
}

// ---------------------------------------------------------------------------
// Снимок отчёта
// ---------------------------------------------------------------------------

// reportSnapshot — avatar_report: всё, что учитель увидит, на момент разбора.
type reportSnapshot struct {
	Version   int             `json:"version"`
	ClosedAt  *time.Time      `json:"closed_at"`
	Level     string          `json:"level"`
	MaxTurns  int             `json:"max_turns"`
	Turns     []ReportTurn    `json:"turns"`
	Analysis  json.RawMessage `json:"analysis"`
	Incidents json.RawMessage `json:"incidents"`
}

// ReportTurn — реплика в расшифровке отчёта.
type ReportTurn struct {
	TurnNo      int     `json:"turn_no"`
	Role        string  `json:"role"`
	Content     string  `json:"content"`
	Translation string  `json:"translation"`
	Input       string  `json:"input"`
	Edited      bool    `json:"edited"`
	ASRText     *string `json:"asr_text"`
}

func (c conversation) snapshot(brief AvatarBrief, analysis json.RawMessage) reportSnapshot {
	turns := make([]ReportTurn, 0, len(c.turns))
	for _, t := range c.turns {
		rt := ReportTurn{
			TurnNo: t.No, Role: t.Role, Content: t.Content, Translation: t.Translation,
			Input: t.Input, Edited: t.Edited,
		}
		if t.ASRText != "" {
			asr := t.ASRText
			rt.ASRText = &asr
		}
		turns = append(turns, rt)
	}
	incidents := json.RawMessage(c.row.Incidents)
	if len(incidents) == 0 {
		incidents = json.RawMessage("[]")
	}
	return reportSnapshot{
		Version: reportVersion, ClosedAt: c.row.ClosedAt, Level: levelCode(brief.Level),
		MaxTurns: c.max, Turns: turns, Analysis: analysis, Incidents: incidents,
	}
}

// readyReport — снимок и разбор готового отчёта; false — отчёта нет или он
// не читается.
func readyReport(row ConversationRow) (reportSnapshot, Analysis, bool) {
	if row.ReportStatus != ReportReady || len(row.Report) == 0 {
		return reportSnapshot{}, Analysis{}, false
	}
	var snap reportSnapshot
	if err := json.Unmarshal(row.Report, &snap); err != nil {
		return reportSnapshot{}, Analysis{}, false
	}
	a, err := parseAnalysis(snap.Analysis)
	if err != nil {
		return reportSnapshot{}, Analysis{}, false
	}
	return snap, a, true
}

// ---------------------------------------------------------------------------
// Фоновой разбор
// ---------------------------------------------------------------------------

// reportDue — разбор пора запускать: разговор закрыт, отчёта нет, и разбор
// не идёт — не начинался, завис или упал достаточно давно. То же условие
// стоит в ClaimReport; здесь оно только избавляет от лишних запросов.
func reportDue(row ConversationRow, closed bool, now time.Time) bool {
	if !closed || len(row.Report) > 0 {
		return false
	}
	switch row.ReportStatus {
	case "":
		return true
	case ReportPending:
		return row.ReportStartedAt == nil || now.Sub(*row.ReportStartedAt) > reportLease
	case ReportFailed:
		return row.ReportStartedAt == nil || now.Sub(*row.ReportStartedAt) > reportRetry
	}
	return false
}

// kickReport запускает разбор в фоне, если аренда свободна. true — разбор
// запущен этим вызовом.
//
// Место в ограничителе занимается до аренды: аренда без свободного места
// означала бы три минуты «идёт разбор», которого на самом деле нет. Нет
// места — разбор запустит следующий запрос итога.
func (s *Service) kickReport(ctx context.Context, orgID, attemptID, studentID uuid.UUID, brief AvatarBrief) bool {
	if s.conversation == nil {
		return false
	}
	select {
	case s.reportSlots <- struct{}{}:
	default:
		return false
	}
	lease, ok, err := s.store.ClaimReport(ctx, s.pool, orgID, attemptID, brief.MaxTurns, reportLease, reportRetry)
	if err != nil || !ok {
		<-s.reportSlots
		if err != nil {
			logx.From(ctx).Error("не удалось начать разбор разговора", "attempt_id", attemptID, "err", err)
		}
		return false
	}
	s.reports.Add(1)
	// Отвязано от запроса: ответ ученику уходит сразу, а разбор живёт своим
	// таймаутом и пишет ошибку в лог.
	jobCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), reportBudget)
	go func() {
		defer s.reports.Done()
		defer func() { <-s.reportSlots }()
		defer cancel()
		s.runReport(jobCtx, orgID, attemptID, studentID, brief, lease)
	}()
	return true
}

// runReport — разбор и запись отчёта; неудача отмечается своей арендой.
func (s *Service) runReport(ctx context.Context, orgID, attemptID, studentID uuid.UUID, brief AvatarBrief,
	lease time.Time) {
	err := s.writeReport(ctx, orgID, attemptID, studentID, brief)
	if err == nil {
		return
	}
	logx.From(ctx).Error("разбор разговора не удался", "attempt_id", attemptID, "err", err)
	// Таймаут разбора мог уже истечь — отметке нужен свой.
	failCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.store.FailReport(failCtx, s.pool, orgID, attemptID, lease); err != nil {
		logx.From(ctx).Error("не удалось отметить сбой разбора", "attempt_id", attemptID, "err", err)
	}
}

func (s *Service) writeReport(ctx context.Context, orgID, attemptID, studentID uuid.UUID, brief AvatarBrief) error {
	conv, err := s.loadConversation(ctx, s.pool, orgID, attemptID, brief.MaxTurns)
	if err != nil {
		return err
	}
	var state json.RawMessage
	if conv.row.HasEngine {
		state = conv.row.Engine
	}
	raw, err := s.conversation.Analyze(ctx, orgID, studentID, EngineAnalyze{
		Language: brief.Language, Level: levelCode(brief.Level), Goals: brief.Goals,
		State: state, Transcript: conv.transcript(),
	})
	if err != nil {
		return err
	}
	analysis, err := parseAnalysis(raw)
	if err != nil {
		return err
	}
	data, err := json.Marshal(conv.snapshot(brief, raw))
	if err != nil {
		return err
	}
	// false — отчёт уже записал другой разбор: он и останется.
	sum := analysis.Summary
	_, err = s.store.SaveReport(ctx, s.pool, orgID, attemptID, data, len(sum.CreditedPhrases), sum.PhrasesTotal)
	return err
}

// WaitReports ждёт фоновые разборы этого процесса — при остановке, чтобы
// разбор успел записать отчёт до закрытия пула. Не дождался — не беда:
// аренда истечёт, и разбор перезапустится.
func (s *Service) WaitReports(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		s.reports.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ---------------------------------------------------------------------------
// Итог ученику
// ---------------------------------------------------------------------------

// Статусы итога.
const (
	SummaryNotClosed = "not_closed"
)

// ConversationSummary — итог разговора ученику.
type ConversationSummary struct {
	// Status — not_closed, pending, ready или failed.
	Status string
	// Summary — только при ready.
	Summary *StudentSummary
}

// StudentSummary — что получилось и одна вещь на следующий раз. Ни списка
// ошибок, ни ошибок выше уровня или без уровня: итог для того, чтобы понять,
// куда двигаться, а не чтобы расстроиться (Уч-5).
type StudentSummary struct {
	CreditedPhrases []GoalRef
	AppliedThemes   []GoalRef
	PhrasesTotal    int
	ThemesTotal     int
	// NextTime — nil, если подсказать нечего.
	NextTime *NextTime
	// Incidents — технические сбои, отдельно от качества речи.
	Incidents []IncidentCount
}

// IncidentCount — сколько было сбоев одного вида.
type IncidentCount struct {
	Kind  string
	Count int
}

// ConversationSummary — итог ученику. Незакрытый разговор итога не имеет;
// закрытый без готового отчёта запускает разбор, если тот не идёт.
func (s *Service) ConversationSummary(ctx context.Context, actor iam.Actor, attemptID uuid.UUID) (ConversationSummary, error) {
	if s.material == nil {
		return ConversationSummary{}, httpx.Unavailable(msgConversationOff)
	}
	attempt, err := s.ownAttempt(ctx, actor, attemptID)
	if err != nil {
		return ConversationSummary{}, err
	}
	brief, err := s.briefFor(ctx, actor, attempt)
	if err != nil {
		return ConversationSummary{}, err
	}
	conv, err := s.readConversation(ctx, actor.OrgID, attempt.ID, brief.MaxTurns)
	if err != nil {
		return ConversationSummary{}, err
	}
	if !conv.closed() {
		return ConversationSummary{Status: SummaryNotClosed}, nil
	}
	if brief.Enabled && conv.needsClosing() {
		if conv, err = s.settle(ctx, actor, attempt, brief); err != nil {
			return ConversationSummary{}, err
		}
	}
	if _, analysis, ok := readyReport(conv.row); ok {
		return ConversationSummary{Status: ReportReady, Summary: studentSummary(analysis, conv.row.Incidents)}, nil
	}
	if reportDue(conv.row, true, time.Now()) && s.kickReport(ctx, actor.OrgID, attempt.ID, attempt.StudentID, brief) {
		return ConversationSummary{Status: ReportPending}, nil
	}
	if conv.row.ReportStatus == ReportFailed {
		return ConversationSummary{Status: ReportFailed}, nil
	}
	return ConversationSummary{Status: ReportPending}, nil
}

// studentSummary — проекция разбора для ученика.
//
// Собирается из явного списка полей, а не из разбора целиком: всё, чего
// здесь нет, — ошибки, цитаты, уровни — к ученику не попадает, сколько бы
// полей движок ни добавил.
func studentSummary(a Analysis, incidents []byte) *StudentSummary {
	out := &StudentSummary{
		CreditedPhrases: refs(a.Summary.CreditedPhrases),
		AppliedThemes:   refs(a.Summary.AppliedThemes),
		PhrasesTotal:    a.Summary.PhrasesTotal,
		ThemesTotal:     a.Summary.ThemesTotal,
		Incidents:       incidentCounts(incidents),
	}
	if n := a.Summary.NextTime; n != nil && (strings.TrimSpace(n.TextRU) != "" || strings.TrimSpace(n.Example) != "") {
		out.NextTime = &NextTime{TextRU: n.TextRU, Example: n.Example}
	}
	return out
}

func refs(in []GoalRef) []GoalRef {
	out := make([]GoalRef, 0, len(in))
	for _, r := range in {
		out = append(out, GoalRef{Key: r.Key, Title: r.Title})
	}
	return out
}

// incidentCounts — сбои по видам, в порядке первого появления.
func incidentCounts(raw []byte) []IncidentCount {
	out := []IncidentCount{}
	var items []struct {
		Kind string `json:"kind"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &items) != nil {
		return out
	}
	for _, it := range items {
		if it.Kind == "" {
			continue
		}
		i := slices.IndexFunc(out, func(c IncidentCount) bool { return c.Kind == it.Kind })
		if i < 0 {
			out = append(out, IncidentCount{Kind: it.Kind})
			i = len(out) - 1
		}
		out[i].Count++
	}
	return out
}

// ---------------------------------------------------------------------------
// Отчёт учителю
// ---------------------------------------------------------------------------

// Статусы ученика в отчёте учителя.
const (
	StudentNotStarted = "not_started" // ответов нет — «не приступал»
	StudentInProgress = "in_progress" // «разговор не завершён»
	StudentCompleted  = "completed"   // «завершил»
)

// ClassConversation — отчёт по разговору для всего класса.
type ClassConversation struct {
	Enabled      bool
	Level        string
	MaxTurns     int
	Goals        []AvatarGoal
	Counts       ConversationCounts
	Students     []ConversationStudent
	Themes       []ConversationTheme
	CommonErrors []CommonErrorGroup
}

// ConversationCounts — сколько учеников в каждом статусе.
type ConversationCounts struct {
	NotStarted int
	InProgress int
	Completed  int
}

// ConversationStudent — строка «по ученикам».
type ConversationStudent struct {
	Member       ClassMember
	Status       string
	TurnsTaken   int
	ReportStatus string
	Credited     int
	GoalsTotal   int
	// Errors и Misses — ошибки и промахи по целям урока. Общие ошибки сюда
	// не входят: они отдельным списком класса, как и требует У-4.
	Errors int
	Misses int
}

// ConversationTheme — строка «по темам»: одна цель по всему классу.
type ConversationTheme struct {
	Goal             AvatarGoal
	CorrectUses      int
	Errors           int
	CreditedStudents int
	Students         []ThemeStudent
}

// ThemeStudent — ученик в теме.
type ThemeStudent struct {
	Member      ClassMember
	CorrectUses int
	Errors      int
	Misses      int
}

// CommonErrorGroup — общая ошибка класса, сгруппированная по слову или
// конструкции.
type CommonErrorGroup struct {
	GroupKey string
	Label    string
	Type     string
	// Level — "" у ошибок, уровень которых определить нельзя: экран пишет
	// «уровень не определён».
	Level    string
	Count    int
	Students []CommonErrorStudent
}

// CommonErrorStudent — ученик и его цитаты в группе.
type CommonErrorStudent struct {
	Member ClassMember
	Quotes []ErrorQuote
}

// ErrorQuote — цитата из ответа ученика.
type ErrorQuote struct {
	TurnNo      int
	Quote       string
	CorrectForm string
}

// studentStatus — статус разговора ученика для учителя.
func studentStatus(row *ConversationRow, maxTurns int) string {
	switch {
	case row == nil || row.Turns == 0 && row.ClosedAt == nil:
		return StudentNotStarted
	case row.ClosedAt != nil || (maxTurns > 0 && row.Turns >= maxTurns):
		return StudentCompleted
	}
	return StudentInProgress
}

// teacherReportStatus — статус разбора для учителя: как у ученика,
// закрытый разговор без запущенного разбора — «pending».
func teacherReportStatus(row *ConversationRow, maxTurns int) string {
	if row == nil || studentStatus(row, maxTurns) != StudentCompleted {
		return ""
	}
	if row.ReportStatus == "" {
		return ReportPending
	}
	return row.ReportStatus
}

// ClassConversation — отчёт учителя по разговору класса.
//
// Права — как у остальных ручек выдачи: урок своего класса, чужой — 404.
// Зависшие и упавшие разборы перезапускаются отсюда же (договор).
func (s *Service) ClassConversation(ctx context.Context, actor iam.Actor, assignmentID uuid.UUID) (ClassConversation, error) {
	if s.material == nil {
		return ClassConversation{}, httpx.Unavailable(msgConversationOff)
	}
	a, class, err := s.ownedAssignment(ctx, actor, assignmentID)
	if err != nil {
		return ClassConversation{}, err
	}
	brief, err := s.briefOfVersion(ctx, actor.OrgID, a.LessonVersionID)
	if err != nil {
		return ClassConversation{}, err
	}
	members, err := s.classes.Roster(ctx, actor, class.ID)
	if err != nil {
		return ClassConversation{}, err
	}
	rows, err := s.store.ConversationsOfAssignment(ctx, s.pool, actor.OrgID, a.ID)
	if err != nil {
		return ClassConversation{}, httpx.Internal("Не удалось прочитать разговоры").Wrap(err)
	}
	byStudent := make(map[uuid.UUID]*ConversationRow, len(rows))
	for i := range rows {
		byStudent[rows[i].StudentID] = &rows[i]
	}
	now := time.Now()
	for _, m := range members {
		row := byStudent[m.ID]
		if row == nil {
			continue
		}
		closed := studentStatus(row, brief.MaxTurns) == StudentCompleted
		if reportDue(*row, closed, now) && s.kickReport(ctx, actor.OrgID, row.AttemptID, row.StudentID, brief) {
			row.ReportStatus = ReportPending
		}
	}
	return buildClassConversation(brief, sortedMembers(members), byStudent), nil
}

// buildClassConversation — арифметика отчёта без базы. В темы и общие
// ошибки идут только готовые отчёты; общие ошибки — видимые учителю
// (main и unknown_level), выше уровня урока — нет.
func buildClassConversation(brief AvatarBrief, members []ClassMember,
	rows map[uuid.UUID]*ConversationRow) ClassConversation {
	out := ClassConversation{
		Enabled: brief.Enabled, Level: levelCode(brief.Level), MaxTurns: brief.MaxTurns,
		Goals: brief.Goals, Students: []ConversationStudent{}, CommonErrors: []CommonErrorGroup{},
	}
	if out.Goals == nil {
		out.Goals = []AvatarGoal{}
	}
	themes := make([]ConversationTheme, len(out.Goals))
	themeIdx := make(map[string]int, len(out.Goals))
	for i, g := range out.Goals {
		themes[i] = ConversationTheme{Goal: g, Students: []ThemeStudent{}}
		themeIdx[g.Key] = i
	}
	groups := map[string]*CommonErrorGroup{}
	var order []string

	for _, m := range members {
		row := rows[m.ID]
		st := ConversationStudent{
			Member: m, Status: studentStatus(row, brief.MaxTurns),
			ReportStatus: teacherReportStatus(row, brief.MaxTurns), GoalsTotal: len(out.Goals),
		}
		if row != nil {
			st.TurnsTaken = row.Turns
		}
		switch st.Status {
		case StudentNotStarted:
			out.Counts.NotStarted++
		case StudentInProgress:
			out.Counts.InProgress++
		default:
			out.Counts.Completed++
		}
		var snap reportSnapshot
		var analysis Analysis
		ready := false
		if row != nil && st.Status == StudentCompleted {
			snap, analysis, ready = readyReport(*row)
		}
		if ready {
			byKey := make(map[string]AnalysisGoal, len(analysis.Goals))
			for _, g := range analysis.Goals {
				byKey[g.Key] = g
				if g.Credited {
					st.Credited++
				}
				st.Errors += g.Errors
				st.Misses += g.Misses
			}
			for i := range themes {
				g := byKey[themes[i].Goal.Key]
				themes[i].CorrectUses += g.CorrectUses
				themes[i].Errors += g.Errors
				if g.Credited {
					themes[i].CreditedStudents++
				}
				themes[i].Students = append(themes[i].Students, ThemeStudent{
					Member: m, CorrectUses: g.CorrectUses, Errors: g.Errors, Misses: g.Misses,
				})
			}
			for _, e := range visibleErrors(analysis.GeneralErrors, snap.Turns) {
				if e.Visibility == VisibilityAboveLevel {
					continue
				}
				key := groupKey(e)
				g, ok := groups[key]
				if !ok {
					g = &CommonErrorGroup{GroupKey: e.GroupKey, Label: groupLabel(e), Type: e.Type}
					if g.GroupKey == "" {
						g.GroupKey = key
					}
					groups[key] = g
					order = append(order, key)
				}
				if g.Level == "" && e.Visibility == VisibilityMain {
					g.Level = levelCode(e.Level)
				}
				g.Count++
				i := slices.IndexFunc(g.Students, func(cs CommonErrorStudent) bool { return cs.Member.ID == m.ID })
				if i < 0 {
					g.Students = append(g.Students, CommonErrorStudent{Member: m})
					i = len(g.Students) - 1
				}
				g.Students[i].Quotes = append(g.Students[i].Quotes, ErrorQuote{
					TurnNo: e.TurnNo, Quote: e.Quote, CorrectForm: e.CorrectForm,
				})
			}
		}
		out.Students = append(out.Students, st)
	}
	out.Themes = themes
	for _, key := range order {
		out.CommonErrors = append(out.CommonErrors, *groups[key])
	}
	// Частые ошибки класса — первыми: по ним учитель планирует урок.
	sort.SliceStable(out.CommonErrors, func(i, j int) bool {
		if out.CommonErrors[i].Count != out.CommonErrors[j].Count {
			return out.CommonErrors[i].Count > out.CommonErrors[j].Count
		}
		return nameKey(out.CommonErrors[i].Label) < nameKey(out.CommonErrors[j].Label)
	})
	return out
}

// groupKey — по чему складываются общие ошибки класса: ключ группы от
// движка (лемма или конструкция), без него — верная форма или цитата.
func groupKey(e GeneralError) string {
	if k := strings.TrimSpace(e.GroupKey); k != "" {
		return k
	}
	text := e.CorrectForm
	if strings.TrimSpace(text) == "" {
		text = e.Quote
	}
	return e.Type + ":" + strings.ToLower(strings.TrimSpace(text))
}

func groupLabel(e GeneralError) string {
	for _, s := range []string{e.GroupLabel, e.CorrectForm, e.Quote} {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return e.GroupKey
}

// quoted — цитата действительно есть в ответе ученика с этим номером.
//
// Движок обязан отбрасывать отметки без цитаты, но отчёт учителя — то, по
// чему ставят оценку (У-3, У-4: «выдуманных цитат нет»), поэтому вторая
// проверка стоит здесь. Пустая цитата допустима: отметка всё равно ведёт к
// ответу по номеру.
func quoted(turns []ReportTurn, turnNo int, quote string) bool {
	for _, t := range turns {
		if t.Role == RoleStudent && t.TurnNo == turnNo {
			return quote == "" || strings.Contains(t.Content, quote)
		}
	}
	return false
}

func visibleMarks(marks []GoalMark, turns []ReportTurn) []GoalMark {
	out := make([]GoalMark, 0, len(marks))
	for _, m := range marks {
		if quoted(turns, m.TurnNo, m.Quote) {
			out = append(out, m)
		}
	}
	return out
}

func visibleErrors(errs []GeneralError, turns []ReportTurn) []GeneralError {
	out := make([]GeneralError, 0, len(errs))
	for _, e := range errs {
		if quoted(turns, e.TurnNo, e.Quote) {
			out = append(out, e)
		}
	}
	return out
}

// StudentConversation — карточка ученика: расшифровка с отметками.
type StudentConversation struct {
	Member       ClassMember
	Status       string
	TurnsTaken   int
	MaxTurns     int
	ReportStatus string
	// Report — только у завершённого разговора с готовым разбором.
	Report *TeacherReport
}

// TeacherReport — отчёт по ученику для учителя.
type TeacherReport struct {
	ClosedAt *time.Time
	Level    string
	Goals    []AnalysisGoal
	Turns    []ReportTurn
	Marks    []GoalMark
	// GeneralErrors — без ошибок выше уровня урока: их только число.
	GeneralErrors   []GeneralError
	AboveLevelCount int
	SpeechLevel     json.RawMessage
	Incidents       json.RawMessage
}

// StudentConversation — разговор одного ученика для учителя. Ученик ищется
// в нынешнем составе класса, как и в панели ученика: чужой — 404.
func (s *Service) StudentConversation(ctx context.Context, actor iam.Actor, assignmentID,
	studentID uuid.UUID) (StudentConversation, error) {
	if s.material == nil {
		return StudentConversation{}, httpx.Unavailable(msgConversationOff)
	}
	a, class, err := s.ownedAssignment(ctx, actor, assignmentID)
	if err != nil {
		return StudentConversation{}, err
	}
	members, err := s.classes.Roster(ctx, actor, class.ID)
	if err != nil {
		return StudentConversation{}, err
	}
	idx := slices.IndexFunc(members, func(m ClassMember) bool { return m.ID == studentID })
	if idx < 0 {
		return StudentConversation{}, httpx.NotFound("Ученик не найден")
	}
	brief, err := s.briefOfVersion(ctx, actor.OrgID, a.LessonVersionID)
	if err != nil {
		return StudentConversation{}, err
	}
	var row *ConversationRow
	switch r, err := s.store.ConversationOfStudent(ctx, s.pool, actor.OrgID, a.ID, studentID); {
	case errors.Is(err, ErrNotFound):
	case err != nil:
		return StudentConversation{}, httpx.Internal("Не удалось прочитать разговор").Wrap(err)
	default:
		row = &r
	}
	if row != nil {
		closed := studentStatus(row, brief.MaxTurns) == StudentCompleted
		if reportDue(*row, closed, time.Now()) && s.kickReport(ctx, actor.OrgID, row.AttemptID, row.StudentID, brief) {
			row.ReportStatus = ReportPending
		}
	}
	return buildStudentConversation(members[idx], brief, row), nil
}

func buildStudentConversation(m ClassMember, brief AvatarBrief, row *ConversationRow) StudentConversation {
	out := StudentConversation{
		Member: m, Status: studentStatus(row, brief.MaxTurns), MaxTurns: brief.MaxTurns,
		ReportStatus: teacherReportStatus(row, brief.MaxTurns),
	}
	if row == nil {
		return out
	}
	out.TurnsTaken = row.Turns
	if out.Status != StudentCompleted {
		return out
	}
	snap, a, ok := readyReport(*row)
	if !ok {
		return out
	}
	r := &TeacherReport{
		ClosedAt: snap.ClosedAt, Level: snap.Level, Goals: a.Goals, Turns: snap.Turns,
		Marks: visibleMarks(a.Marks, snap.Turns), GeneralErrors: []GeneralError{},
		SpeechLevel: a.SpeechLevel, Incidents: snap.Incidents,
	}
	if r.Goals == nil {
		r.Goals = []AnalysisGoal{}
	}
	if r.Turns == nil {
		r.Turns = []ReportTurn{}
	}
	for _, e := range visibleErrors(a.GeneralErrors, snap.Turns) {
		if e.Visibility == VisibilityAboveLevel {
			r.AboveLevelCount++
			continue
		}
		r.GeneralErrors = append(r.GeneralErrors, e)
	}
	out.Report = r
	return out
}
