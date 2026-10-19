package learning

// Ручки разговора по целям урока. Поля и коды — дословно по договору
// docs/product/avatar-goals/contract.md, разделы 6 и 10.2: по нему
// параллельно пишутся экраны ученика и учителя, и расхождение здесь ломает
// их молча.

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/NATIVE/NATIVE/internal/iam"
	"github.com/NATIVE/NATIVE/internal/platform/httpx"
	"github.com/go-chi/chi/v5"
)

func (a *API) mountConversation(r chi.Router) {
	g := a.guard
	// Ученик. Прав из матрицы у него нет — принадлежность попытки проверяет
	// сервис.
	g.Handle(r, http.MethodGet, "/my/attempts/{attemptID}/conversation", iam.PermAuthenticated, a.myConversation)
	g.Handle(r, http.MethodPost, "/my/attempts/{attemptID}/conversation/start", iam.PermAuthenticated, a.conversationStart)
	g.Handle(r, http.MethodPost, "/my/attempts/{attemptID}/conversation/answer", iam.PermAuthenticated, a.conversationAnswer)
	g.Handle(r, http.MethodPost, "/my/attempts/{attemptID}/conversation/incident", iam.PermAuthenticated, a.conversationIncident)
	g.Handle(r, http.MethodGet, "/my/attempts/{attemptID}/conversation/summary", iam.PermAuthenticated, a.conversationSummary)

	// Живой режим (раздел 10.2 договора): одна сессия GPT Live, реплики
	// аватара из её расшифровки и инструменты делегата сессии.
	g.Handle(r, http.MethodPost, "/my/attempts/{attemptID}/conversation/live", iam.PermAuthenticated, a.conversationLive)
	g.Handle(r, http.MethodPost, "/my/attempts/{attemptID}/conversation/live/end", iam.PermAuthenticated, a.conversationLiveEnd)
	g.Handle(r, http.MethodPost, "/my/attempts/{attemptID}/conversation/avatar", iam.PermAuthenticated, a.conversationAvatar)
	g.Handle(r, http.MethodGet, "/my/attempts/{attemptID}/conversation/progress", iam.PermAuthenticated, a.conversationProgress)
	g.Handle(r, http.MethodPost, "/my/attempts/{attemptID}/conversation/check", iam.PermAuthenticated, a.conversationCheck)

	// Учитель. Отчёт класса — работа с уроком, как экран «Урок в классе»;
	// карточка ученика — его ответы, право как у панели ученика.
	g.Handle(r, http.MethodGet, "/assignments/{assignmentID}/conversation", iam.PermLessonsOwn, a.classConversation)
	g.Handle(r, http.MethodGet, "/assignments/{assignmentID}/students/{studentID}/conversation",
		iam.PermStudentsOwn, a.studentConversation)
}

// ---------------------------------------------------------------------------
// DTO
// ---------------------------------------------------------------------------

type conversationTurnDTO struct {
	TurnNo      int    `json:"turn_no"`
	Role        string `json:"role"`
	Content     string `json:"content"`
	Translation string `json:"translation"`
	Input       string `json:"input"`
	Edited      bool   `json:"edited"`
}

type pendingAnswerDTO struct {
	ClientMsgID string `json:"client_msg_id"`
	TurnNo      int    `json:"turn_no"`
	Text        string `json:"text"`
}

type conversationDTO struct {
	Enabled           bool                  `json:"enabled"`
	Language          string                `json:"language"`
	Level             string                `json:"level"`
	Status            string                `json:"status"`
	MaxTurns          int                   `json:"max_turns"`
	TurnsTaken        int                   `json:"turns_taken"`
	Turns             []conversationTurnDTO `json:"turns"`
	PendingAnswer     *pendingAnswerDTO     `json:"pending_answer"`
	ReportStatus      string                `json:"report_status"`
	DictionaryEnabled bool                  `json:"dictionary_enabled"`
	Phrases           []string              `json:"phrases"`
	LiveAvailable     bool                  `json:"live_available"`
}

// startStepDTO — ответ start: без статуса разбора, разговор только начат.
type startStepDTO struct {
	Reply       string `json:"reply"`
	Translation string `json:"translation"`
	TurnNo      int    `json:"turn_no"`
	TurnsTaken  int    `json:"turns_taken"`
	MaxTurns    int    `json:"max_turns"`
	Status      string `json:"status"`
}

type answerStepDTO struct {
	startStepDTO
	ReportStatus string `json:"report_status"`
}

// liveAnswerDTO — ответ на ответ живого режима: указание сессии вместо
// реплики, reply и translation пустые.
type liveAnswerDTO struct {
	answerStepDTO
	Steer string `json:"steer"`
}

func toStartStepDTO(s ConversationStep) startStepDTO {
	return startStepDTO{Reply: s.Reply, Translation: s.Translation, TurnNo: s.TurnNo,
		TurnsTaken: s.TurnsTaken, MaxTurns: s.MaxTurns, Status: s.Status}
}

type goalRefDTO struct {
	Key   string `json:"key"`
	Title string `json:"title"`
}

type nextTimeDTO struct {
	TextRU  string `json:"text_ru"`
	Example string `json:"example"`
}

type incidentCountDTO struct {
	Kind  string `json:"kind"`
	Count int    `json:"count"`
}

type studentSummaryDTO struct {
	CreditedPhrases []goalRefDTO       `json:"credited_phrases"`
	AppliedThemes   []goalRefDTO       `json:"applied_themes"`
	PhrasesTotal    int                `json:"phrases_total"`
	ThemesTotal     int                `json:"themes_total"`
	NextTime        *nextTimeDTO       `json:"next_time"`
	Incidents       []incidentCountDTO `json:"incidents"`
}

func goalRefsDTO(in []GoalRef) []goalRefDTO {
	out := make([]goalRefDTO, 0, len(in))
	for _, r := range in {
		out = append(out, goalRefDTO{Key: r.Key, Title: r.Title})
	}
	return out
}

// studentRefDTO — ученик в отчёте: как в /assignments/{id}/live.
type studentRefDTO struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Tag  string `json:"tag"`
}

func toStudentRef(m ClassMember) studentRefDTO {
	return studentRefDTO{ID: m.ID.String(), Name: m.DisplayName, Tag: shortTag(m.PublicTag)}
}

type goalDTO struct {
	Key   string `json:"key"`
	Kind  string `json:"kind"`
	Title string `json:"title"`
	Note  string `json:"note"`
}

type analysisGoalDTO struct {
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

type reportTurnDTO struct {
	TurnNo      int     `json:"turn_no"`
	Role        string  `json:"role"`
	Content     string  `json:"content"`
	Translation string  `json:"translation"`
	Input       string  `json:"input"`
	Edited      bool    `json:"edited"`
	ASRText     *string `json:"asr_text"`
}

type markDTO struct {
	TurnNo      int     `json:"turn_no"`
	GoalKey     string  `json:"goal_key"`
	Verdict     string  `json:"verdict"`
	ErrorKind   *string `json:"error_kind"`
	Quote       string  `json:"quote"`
	CorrectForm string  `json:"correct_form"`
	NoteRU      string  `json:"note_ru"`
}

type generalErrorDTO struct {
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

// rawOr — сырой JSON из отчёта или заглушка, если его нет: null в
// speech_level и [] в incidents читаются экраном проще пустоты.
func rawOr(raw json.RawMessage, fallback string) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(fallback)
	}
	return raw
}

// ---------------------------------------------------------------------------
// Ученик
// ---------------------------------------------------------------------------

func (a *API) myConversation(w http.ResponseWriter, r *http.Request) {
	actor, id, ok := a.studentAttempt(w, r)
	if !ok {
		return
	}
	v, err := a.svc.Conversation(r.Context(), actor, id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	turns := make([]conversationTurnDTO, 0, len(v.Turns))
	for _, t := range v.Turns {
		turns = append(turns, conversationTurnDTO{
			TurnNo: t.No, Role: t.Role, Content: t.Content, Translation: t.Translation,
			Input: t.Input, Edited: t.Edited,
		})
	}
	out := conversationDTO{
		Enabled: v.Enabled, Language: v.Language, Level: v.Level, Status: v.Status,
		MaxTurns: v.MaxTurns, TurnsTaken: v.TurnsTaken, Turns: turns, ReportStatus: v.ReportStatus,
		DictionaryEnabled: v.DictionaryEnabled, Phrases: v.Phrases, LiveAvailable: v.LiveAvailable,
	}
	if out.Phrases == nil {
		out.Phrases = []string{}
	}
	if p := v.Pending; p != nil {
		out.PendingAnswer = &pendingAnswerDTO{ClientMsgID: p.ClientMsgID, TurnNo: p.TurnNo, Text: p.Text}
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) conversationStart(w http.ResponseWriter, r *http.Request) {
	actor, id, ok := a.studentAttempt(w, r)
	if !ok {
		return
	}
	step, err := a.svc.StartConversation(r.Context(), actor, id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toStartStepDTO(step))
}

func (a *API) conversationAnswer(w http.ResponseWriter, r *http.Request) {
	actor, id, ok := a.studentAttempt(w, r)
	if !ok {
		return
	}
	var in struct {
		ClientMsgID   string   `json:"client_msg_id"`
		Text          string   `json:"text"`
		Input         string   `json:"input"`
		ASRText       string   `json:"asr_text"`
		ASRConfidence *float64 `json:"asr_confidence"`
		Edited        bool     `json:"edited"`
		Mode          string   `json:"mode"`
	}
	if err := httpx.DecodeJSON(r, &in, 32*1024); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	input := ConversationInput{
		ClientMsgID: in.ClientMsgID, Text: in.Text, Input: in.Input,
		ASRText: in.ASRText, ASRConfidence: in.ASRConfidence, Edited: in.Edited,
		// Форма ответа зависит от режима, и решается она тем же значением,
		// что проверит сервис.
		Mode: strings.TrimSpace(in.Mode),
	}
	step, err := a.svc.AnswerConversation(r.Context(), actor, id, input)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out := answerStepDTO{startStepDTO: toStartStepDTO(step), ReportStatus: step.ReportStatus}
	if input.live() {
		httpx.JSON(w, http.StatusOK, liveAnswerDTO{answerStepDTO: out, Steer: step.Steer})
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// conversationLive открывает живую сессию разговора по целям: браузер
// присылает предложение WebRTC-соединения, сервер отвечает ответом
// провайдера и указанием на первый ход сессии.
func (a *API) conversationLive(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	actor, id, ok := a.studentAttempt(w, r)
	if !ok {
		return
	}
	var in struct {
		SDP string `json:"sdp"`
	}
	if err := httpx.DecodeJSON(r, &in, 64<<10); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	s, err := a.svc.OpenLiveConversation(r.Context(), actor, id, in.SDP)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	// 201, как у /dialog/live и /live/sessions: создана сессия.
	httpx.JSON(w, http.StatusCreated, map[string]any{
		"session":     map[string]string{"id": s.Answer.SessionID},
		"transport":   map[string]string{"type": "webrtc", "sdp": s.Answer.SDP},
		"steer":       s.Steer,
		"turns_taken": s.TurnsTaken,
		"max_turns":   s.MaxTurns,
		"status":      s.Status,
	})
}

// conversationLiveEnd — живая сессия закончена: место на ключе провайдера
// освобождается, как у /dialog/live/end. 204 и тогда, когда освобождать
// нечего: браузер шлёт это и при остановке, и при уходе со страницы.
func (a *API) conversationLiveEnd(w http.ResponseWriter, r *http.Request) {
	actor, id, ok := a.studentAttempt(w, r)
	if !ok {
		return
	}
	var in struct {
		SessionID string `json:"session_id"`
	}
	if err := httpx.DecodeJSON(r, &in, 4*1024); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if err := a.svc.EndLiveDialog(r.Context(), actor, id, in.SessionID); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// conversationAvatar записывает реплику аватара из расшифровки живой сессии
// и отдаёт её перевод для «Подсказки».
func (a *API) conversationAvatar(w http.ResponseWriter, r *http.Request) {
	actor, id, ok := a.studentAttempt(w, r)
	if !ok {
		return
	}
	var in struct {
		ClientMsgID string `json:"client_msg_id"`
		TurnNo      *int   `json:"turn_no"`
		Content     string `json:"content"`
		SteerTurn   *int   `json:"steer_turn"`
	}
	if err := httpx.DecodeJSON(r, &in, 32*1024); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	translation, err := a.svc.RecordAvatarLine(r.Context(), actor, id, AvatarLineInput{
		ClientMsgID: in.ClientMsgID, TurnNo: in.TurnNo, Content: in.Content, SteerTurn: in.SteerTurn,
	})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"translation": translation})
}

type progressGoalDTO struct {
	Key    string `json:"key"`
	Kind   string `json:"kind"`
	Title  string `json:"title"`
	Note   string `json:"note"`
	Status string `json:"status"`
}

type correctionDTO struct {
	Quote       string `json:"quote"`
	CorrectForm string `json:"correct_form"`
}

// conversationProgress — инструмент делегата lesson_progress. Поля — явным
// списком, как у итога ученика: что бы движок ни добавил в ответ, в браузер
// уходит только это.
func (a *API) conversationProgress(w http.ResponseWriter, r *http.Request) {
	actor, id, ok := a.studentAttempt(w, r)
	if !ok {
		return
	}
	p, err := a.svc.ConversationProgress(r.Context(), actor, id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	goals := make([]progressGoalDTO, 0, len(p.Goals))
	for _, g := range p.Goals {
		goals = append(goals, progressGoalDTO{Key: g.Key, Kind: g.Kind, Title: g.Title, Note: g.Note, Status: g.Status})
	}
	corrections := make([]correctionDTO, 0, len(p.Corrections))
	for _, c := range p.Corrections {
		corrections = append(corrections, correctionDTO{Quote: c.Quote, CorrectForm: c.CorrectForm})
	}
	avoid := append([]string{}, p.AvoidPhrases...)
	var target *string
	if p.TargetGoal != "" {
		target = &p.TargetGoal
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, http.StatusOK, map[string]any{
		"goals":         goals,
		"target_goal":   target,
		"avoid_phrases": avoid,
		"corrections":   corrections,
		"answers_left":  p.AnswersLeft,
		"finished":      p.Finished,
	})
}

// conversationCheck — инструмент делегата check_reply.
func (a *API) conversationCheck(w http.ResponseWriter, r *http.Request) {
	actor, id, ok := a.studentAttempt(w, r)
	if !ok {
		return
	}
	var in struct {
		Text string `json:"text"`
	}
	if err := httpx.DecodeJSON(r, &in, 16*1024); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	res, err := a.svc.CheckConversationReply(r.Context(), actor, id, in.Text)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	problems := append([]string{}, res.Problems...)
	httpx.JSON(w, http.StatusOK, map[string]any{"ok": res.OK, "problems": problems})
}

func (a *API) conversationIncident(w http.ResponseWriter, r *http.Request) {
	actor, id, ok := a.studentAttempt(w, r)
	if !ok {
		return
	}
	var in struct {
		Kind    string `json:"kind"`
		TurnNo  *int   `json:"turn_no"`
		Message string `json:"message"`
	}
	if err := httpx.DecodeJSON(r, &in, 8*1024); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if err := a.svc.ConversationIncident(r.Context(), actor, id,
		IncidentInput{Kind: in.Kind, TurnNo: in.TurnNo, Message: in.Message}); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) conversationSummary(w http.ResponseWriter, r *http.Request) {
	actor, id, ok := a.studentAttempt(w, r)
	if !ok {
		return
	}
	s, err := a.svc.ConversationSummary(r.Context(), actor, id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var summary *studentSummaryDTO
	if sm := s.Summary; sm != nil {
		summary = &studentSummaryDTO{
			CreditedPhrases: goalRefsDTO(sm.CreditedPhrases), AppliedThemes: goalRefsDTO(sm.AppliedThemes),
			PhrasesTotal: sm.PhrasesTotal, ThemesTotal: sm.ThemesTotal,
			Incidents: make([]incidentCountDTO, 0, len(sm.Incidents)),
		}
		if n := sm.NextTime; n != nil {
			summary.NextTime = &nextTimeDTO{TextRU: n.TextRU, Example: n.Example}
		}
		for _, c := range sm.Incidents {
			summary.Incidents = append(summary.Incidents, incidentCountDTO{Kind: c.Kind, Count: c.Count})
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, http.StatusOK, map[string]any{"status": s.Status, "summary": summary})
}

// ---------------------------------------------------------------------------
// Учитель
// ---------------------------------------------------------------------------

func (a *API) classConversation(w http.ResponseWriter, r *http.Request) {
	id, err := pathUUID(r, "assignmentID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	v, err := a.svc.ClassConversation(r.Context(), iam.MustActor(r.Context()), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}

	type studentRow struct {
		Student      studentRefDTO `json:"student"`
		Status       string        `json:"status"`
		TurnsTaken   int           `json:"turns_taken"`
		ReportStatus string        `json:"report_status"`
		Credited     int           `json:"credited"`
		GoalsTotal   int           `json:"goals_total"`
		Errors       int           `json:"errors"`
		Misses       int           `json:"misses"`
	}
	type themeStudent struct {
		Student     studentRefDTO `json:"student"`
		CorrectUses int           `json:"correct_uses"`
		Errors      int           `json:"errors"`
		Misses      int           `json:"misses"`
	}
	type theme struct {
		Key              string         `json:"key"`
		Kind             string         `json:"kind"`
		Title            string         `json:"title"`
		CorrectUses      int            `json:"correct_uses"`
		Errors           int            `json:"errors"`
		CreditedStudents int            `json:"credited_students"`
		Students         []themeStudent `json:"students"`
	}
	type quote struct {
		TurnNo      int    `json:"turn_no"`
		Quote       string `json:"quote"`
		CorrectForm string `json:"correct_form"`
	}
	type errorStudent struct {
		Student studentRefDTO `json:"student"`
		Quotes  []quote       `json:"quotes"`
	}
	type commonError struct {
		GroupKey string         `json:"group_key"`
		Label    string         `json:"label"`
		Type     string         `json:"type"`
		Level    string         `json:"level"`
		Count    int            `json:"count"`
		Students []errorStudent `json:"students"`
	}

	goals := make([]goalDTO, 0, len(v.Goals))
	for _, g := range v.Goals {
		goals = append(goals, goalDTO{Key: g.Key, Kind: g.Kind, Title: g.Title, Note: g.Note})
	}
	students := make([]studentRow, 0, len(v.Students))
	for _, s := range v.Students {
		students = append(students, studentRow{
			Student: toStudentRef(s.Member), Status: s.Status, TurnsTaken: s.TurnsTaken,
			ReportStatus: s.ReportStatus, Credited: s.Credited, GoalsTotal: s.GoalsTotal,
			Errors: s.Errors, Misses: s.Misses,
		})
	}
	themes := make([]theme, 0, len(v.Themes))
	for _, t := range v.Themes {
		th := theme{Key: t.Goal.Key, Kind: t.Goal.Kind, Title: t.Goal.Title, CorrectUses: t.CorrectUses,
			Errors: t.Errors, CreditedStudents: t.CreditedStudents, Students: make([]themeStudent, 0, len(t.Students))}
		for _, s := range t.Students {
			th.Students = append(th.Students, themeStudent{Student: toStudentRef(s.Member),
				CorrectUses: s.CorrectUses, Errors: s.Errors, Misses: s.Misses})
		}
		themes = append(themes, th)
	}
	errs := make([]commonError, 0, len(v.CommonErrors))
	for _, g := range v.CommonErrors {
		ce := commonError{GroupKey: g.GroupKey, Label: g.Label, Type: g.Type, Level: g.Level, Count: g.Count,
			Students: make([]errorStudent, 0, len(g.Students))}
		for _, s := range g.Students {
			es := errorStudent{Student: toStudentRef(s.Member), Quotes: make([]quote, 0, len(s.Quotes))}
			for _, q := range s.Quotes {
				es.Quotes = append(es.Quotes, quote{TurnNo: q.TurnNo, Quote: q.Quote, CorrectForm: q.CorrectForm})
			}
			ce.Students = append(ce.Students, es)
		}
		errs = append(errs, ce)
	}

	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, http.StatusOK, map[string]any{
		"enabled":   v.Enabled,
		"level":     v.Level,
		"max_turns": v.MaxTurns,
		"goals":     goals,
		"counts": map[string]int{
			"not_started": v.Counts.NotStarted,
			"in_progress": v.Counts.InProgress,
			"completed":   v.Counts.Completed,
		},
		"students":      students,
		"themes":        themes,
		"common_errors": errs,
	})
}

func (a *API) studentConversation(w http.ResponseWriter, r *http.Request) {
	assignmentID, err := pathUUID(r, "assignmentID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	studentID, err := pathUUID(r, "studentID")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	v, err := a.svc.StudentConversation(r.Context(), iam.MustActor(r.Context()), assignmentID, studentID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}

	var report any
	if rp := v.Report; rp != nil {
		goals := make([]analysisGoalDTO, 0, len(rp.Goals))
		for _, g := range rp.Goals {
			goals = append(goals, analysisGoalDTO{Key: g.Key, Kind: g.Kind, Title: g.Title, Note: g.Note,
				Credited: g.Credited, CreditedTurn: g.CreditedTurn, CorrectUses: g.CorrectUses,
				Errors: g.Errors, Misses: g.Misses})
		}
		turns := make([]reportTurnDTO, 0, len(rp.Turns))
		for _, t := range rp.Turns {
			turns = append(turns, reportTurnDTO{TurnNo: t.TurnNo, Role: t.Role, Content: t.Content,
				Translation: t.Translation, Input: t.Input, Edited: t.Edited, ASRText: t.ASRText})
		}
		marks := make([]markDTO, 0, len(rp.Marks))
		for _, m := range rp.Marks {
			marks = append(marks, markDTO{TurnNo: m.TurnNo, GoalKey: m.GoalKey, Verdict: m.Verdict,
				ErrorKind: m.ErrorKind, Quote: m.Quote, CorrectForm: m.CorrectForm, NoteRU: m.NoteRU})
		}
		general := make([]generalErrorDTO, 0, len(rp.GeneralErrors))
		for _, e := range rp.GeneralErrors {
			general = append(general, generalErrorDTO{TurnNo: e.TurnNo, Quote: e.Quote, Type: e.Type,
				CorrectForm: e.CorrectForm, RuleRU: e.RuleRU, Level: e.Level, LevelSource: e.LevelSource,
				Visibility: e.Visibility, GroupKey: e.GroupKey, GroupLabel: e.GroupLabel})
		}
		report = map[string]any{
			"closed_at":         a.tsp(rp.ClosedAt),
			"level":             rp.Level,
			"goals":             goals,
			"turns":             turns,
			"marks":             marks,
			"general_errors":    general,
			"above_level_count": rp.AboveLevelCount,
			"speech_level":      rawOr(rp.SpeechLevel, "null"),
			"incidents":         rawOr(rp.Incidents, "[]"),
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, http.StatusOK, map[string]any{
		"student":       toStudentRef(v.Member),
		"status":        v.Status,
		"turns_taken":   v.TurnsTaken,
		"max_turns":     v.MaxTurns,
		"report_status": v.ReportStatus,
		"report":        report,
	})
}
