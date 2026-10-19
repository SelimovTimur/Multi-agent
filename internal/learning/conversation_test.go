package learning

// Арифметика разговора по целям без базы: номера ходов, повтор ответа,
// статусы, итог ученику и отчёт учителю. Правила здесь — из договора
// docs/product/avatar-goals/contract.md, и проверяются они тестами, а не на
// уроке.

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/NATIVE/NATIVE/internal/platform/httpx"
	"github.com/google/uuid"
)

func intp(n int) *int { return &n }

func turn(role, content string, no *int, clientMsgID string) DialogTurn {
	return DialogTurn{ID: uuid.New(), Role: role, Content: content, TurnNo: no, ClientMsgID: clientMsgID}
}

// Реплики прежних режимов получают номера по правилу договора, а новые
// ответы продолжают их — номер хода не повторяется никогда: по нему движок
// решает, применён ли ход.
func TestTurnNumberingContinuesLegacyConversation(t *testing.T) {
	rows := []DialogTurn{
		turn(RoleAvatar, "Исәнме!", nil, ""),
		turn(RoleStudent, "Исәнмесез", nil, ""),
		turn(RoleAvatar, "Ничек яшисең?", nil, ""),
		turn(RoleStudent, "Әйбәт", nil, ""),
		// Ответ, на который прежний режим не ответил (модель упала) и
		// который счётчик не засчитал.
		turn(RoleStudent, "Рәхмәт", nil, ""),
	}
	c := newConversation(ConversationRow{Turns: 2}, rows, 8)
	got := []int{}
	for _, t := range c.turns {
		got = append(got, t.No)
	}
	if jsonOf(got) != "[0,1,1,2,3]" {
		t.Errorf("номера прежних реплик: %v", got)
	}
	// Записанных ответов больше засчитанных: номер следующего — после них,
	// а не по счётчику, иначе он совпал бы с уже бывшим.
	if n := c.nextTurnNo(); n != 4 {
		t.Errorf("следующий ответ: %d, ожидали 4", n)
	}
	// Обычный разговор: номер следующего — счётчик + 1.
	fresh := newConversation(ConversationRow{Turns: 2}, []DialogTurn{
		turn(RoleAvatar, "Исәнме!", intp(0), ""),
		turn(RoleStudent, "Сәлам", intp(1), "a"),
		turn(RoleAvatar, "…", intp(1), ""),
		turn(RoleStudent, "Әйе", intp(2), "b"),
	}, 8)
	if n := fresh.nextTurnNo(); n != 3 {
		t.Errorf("следующий ответ: %d, ожидали 3", n)
	}
}

func jsonOf(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

// Две вкладки пишут реплики вперемешку по времени, а разговор читается по
// ходам: ответ ученика, затем ответ аватара на него.
func TestTurnsAreOrderedByTurn(t *testing.T) {
	rows := []DialogTurn{
		turn(RoleAvatar, "вступление", intp(0), ""),
		turn(RoleStudent, "ответ 1", intp(1), "a"),
		turn(RoleStudent, "ответ 2", intp(2), "b"),
		turn(RoleAvatar, "реплика 2", intp(2), ""),
		turn(RoleAvatar, "реплика 1", intp(1), ""),
	}
	c := newConversation(ConversationRow{Turns: 2, HasEngine: true}, rows, 8)
	var order []string
	for _, t := range c.turns {
		order = append(order, t.Content)
	}
	if got := strings.Join(order, "|"); got != "вступление|ответ 1|реплика 1|ответ 2|реплика 2" {
		t.Errorf("порядок: %s", got)
	}
	// История к ответу 2 — всё, что было до него, включая реплику 1.
	hist := c.before(2)
	if len(hist) != 3 || hist[2].Content != "реплика 1" {
		t.Errorf("история к ответу 2: %+v", hist)
	}
}

// pending_answer — последний ответ без ответа аватара. Клиент повторяет его
// с тем же client_msg_id и продолжает с того же места после перезагрузки.
func TestPendingAnswerIsTheLastUnansweredOne(t *testing.T) {
	rows := []DialogTurn{
		turn(RoleAvatar, "вступление", intp(0), ""),
		turn(RoleStudent, "ответ 1", intp(1), "a"),
		turn(RoleAvatar, "реплика 1", intp(1), ""),
		turn(RoleStudent, "ответ 2", intp(2), "b"),
	}
	c := newConversation(ConversationRow{Turns: 2, HasEngine: true}, rows, 8)
	p := c.pending()
	if p == nil || p.ClientMsgID != "b" || p.TurnNo != 2 || p.Text != "ответ 2" {
		t.Fatalf("незавершённый ответ: %+v", p)
	}
	answered := newConversation(ConversationRow{Turns: 1, HasEngine: true}, rows[:3], 8)
	if answered.pending() != nil {
		t.Error("отвеченный ответ не висит")
	}
	// Ответ прежнего режима повторить через новую ручку нельзя — его и не
	// предлагаем.
	legacy := newConversation(ConversationRow{Turns: 1}, []DialogTurn{turn(RoleStudent, "сәлам", nil, "x")}, 8)
	if legacy.pending() != nil {
		t.Error("ответ прежнего режима не pending")
	}
}

func TestConversationStatuses(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name   string
		row    ConversationRow
		status string
		report string
	}{
		{"не начат", ConversationRow{}, ConversationNotStarted, ""},
		{"идёт", ConversationRow{HasEngine: true, Turns: 3}, ConversationActive, ""},
		{"закрыт, разбор не начат", ConversationRow{HasEngine: true, Turns: 8, ClosedAt: &now}, ConversationClosed, ReportPending},
		{"закрыт, разбор готов", ConversationRow{Turns: 8, ClosedAt: &now, ReportStatus: ReportReady}, ConversationClosed, ReportReady},
		{"закрыт, разбор упал", ConversationRow{Turns: 8, ClosedAt: &now, ReportStatus: ReportFailed}, ConversationClosed, ReportFailed},
		// Разговор прежних режимов, дошедший до конца: закрыт, хоть момент
		// закрытия ещё не записан.
		{"исчерпан прежним режимом", ConversationRow{Turns: 8}, ConversationClosed, ReportPending},
	}
	for _, c := range cases {
		conv := newConversation(c.row, nil, 8)
		if conv.status() != c.status || conv.reportStatus() != c.report {
			t.Errorf("%s: %s/%q, ожидали %s/%q", c.name, conv.status(), conv.reportStatus(), c.status, c.report)
		}
	}
	if !newConversation(ConversationRow{Turns: 8}, nil, 8).needsClosing() {
		t.Error("исчерпанный разговор без закрытия нужно закрыть")
	}
}

func TestAnswerValidation(t *testing.T) {
	ok := ConversationInput{ClientMsgID: " id-1 ", Text: " Мин Казанда яшим ", Input: "voice",
		ASRText: "Мин Казанда яшем", ASRConfidence: new(0.71), Edited: true}
	got, err := ok.normalize()
	if err != nil || got.ClientMsgID != "id-1" || got.Text != "Мин Казанда яшим" || got.ASRText == "" || !got.Edited {
		t.Fatalf("годный ответ: %+v %v", got, err)
	}
	// Текстом — запасной ввод: распознавания не было.
	typed, err := ConversationInput{ClientMsgID: "x", Text: "Әйе", ASRText: "…", ASRConfidence: new(0.5), Edited: true}.normalize()
	if err != nil || typed.Input != InputText || typed.ASRText != "" || typed.ASRConfidence != nil || typed.Edited {
		t.Errorf("ответ текстом: %+v %v", typed, err)
	}

	bad := []struct {
		in    ConversationInput
		field string
	}{
		{ConversationInput{Text: "Әйе"}, "client_msg_id"},
		{ConversationInput{ClientMsgID: strings.Repeat("я", 65), Text: "Әйе"}, "client_msg_id"},
		{ConversationInput{ClientMsgID: "x", Text: "   "}, "text"},
		{ConversationInput{ClientMsgID: "x", Text: strings.Repeat("ә", 1001)}, "text"},
		{ConversationInput{ClientMsgID: "x", Text: "Әйе", Input: "video"}, "input"},
		{ConversationInput{ClientMsgID: "x", Text: "Әйе", Input: "voice", ASRConfidence: new(1.5)}, "asr_confidence"},
		{ConversationInput{ClientMsgID: "x", Text: "Әйе", Input: "voice", ASRConfidence: new(math.NaN())}, "asr_confidence"},
	}
	for _, b := range bad {
		_, err := b.in.normalize()
		if he := httpx.AsError(err); he == nil || he.Fields[b.field] == "" {
			t.Errorf("%+v: ожидали отказ по %s, получили %v", b.in, b.field, err)
		}
	}
	// Ровно 64 символа и ровно 1000 — можно.
	if _, err := (ConversationInput{ClientMsgID: strings.Repeat("я", 64), Text: strings.Repeat("ә", 1000)}).normalize(); err != nil {
		t.Errorf("предельные длины отклонены: %v", err)
	}
}

func TestReportDue(t *testing.T) {
	now := time.Now()
	ago := func(d time.Duration) *time.Time { at := now.Add(-d); return &at }
	cases := []struct {
		name   string
		row    ConversationRow
		closed bool
		due    bool
	}{
		{"не закрыт", ConversationRow{}, false, false},
		{"закрыт, не начинался", ConversationRow{}, true, true},
		{"идёт минуту", ConversationRow{ReportStatus: ReportPending, ReportStartedAt: ago(time.Minute)}, true, false},
		{"завис", ConversationRow{ReportStatus: ReportPending, ReportStartedAt: ago(4 * time.Minute)}, true, true},
		{"упал только что", ConversationRow{ReportStatus: ReportFailed, ReportStartedAt: ago(10 * time.Second)}, true, false},
		{"упал давно", ConversationRow{ReportStatus: ReportFailed, ReportStartedAt: ago(2 * time.Minute)}, true, true},
		{"готов", ConversationRow{ReportStatus: ReportReady, Report: []byte(`{}`)}, true, false},
	}
	for _, c := range cases {
		if got := reportDue(c.row, c.closed, now); got != c.due {
			t.Errorf("%s: %v, ожидали %v", c.name, got, c.due)
		}
	}
}

// sampleAnalysis — разбор с ошибками всех видов: по цели, общей видимой,
// выше уровня и без уровня. Ученик не должен увидеть ни одной из них.
const sampleAnalysis = `{
 "goals":[
  {"key":"p1","kind":"phrase","title":"Минем исемем …","note":"Меня зовут","credited":true,"credited_turn":1,"correct_uses":2,"errors":0,"misses":0},
  {"key":"g1","kind":"grammar","title":"Местный падеж","note":"","credited":false,"credited_turn":null,"correct_uses":0,"errors":1,"misses":1}],
 "marks":[
  {"turn_no":1,"goal_key":"p1","verdict":"credited","error_kind":null,"quote":"Минем исемем","correct_form":"","note_ru":""},
  {"turn_no":2,"goal_key":"g1","verdict":"error","error_kind":"wrong_form","quote":"Казанга","correct_form":"Казанда","note_ru":"СЕКРЕТ-ОШИБКА-ЦЕЛИ"},
  {"turn_no":2,"goal_key":"g1","verdict":"credited","error_kind":null,"quote":"выдуманная цитата","correct_form":"","note_ru":""}],
 "general_errors":[
  {"turn_no":2,"quote":"яшим","type":"grammar","correct_form":"яшәм","rule_ru":"СЕКРЕТ-ОБЩАЯ","level":"a1","level_source":"A1","visibility":"main","group_key":"verb_present","group_label":"Настоящее время"},
  {"turn_no":2,"quote":"Казанга","type":"lexical","correct_form":"","rule_ru":"СЕКРЕТ-ВЫШЕ","level":"b2","level_source":"B2","visibility":"above_level","group_key":"kazan","group_label":"Казан"},
  {"turn_no":1,"quote":"Алсу","type":"lexical","correct_form":"","rule_ru":"СЕКРЕТ-БЕЗ-УРОВНЯ","level":"","level_source":"","visibility":"unknown_level","group_key":"","group_label":""}],
 "summary":{"credited_phrases":[{"key":"p1","title":"Минем исемем …"}],"applied_themes":[],
  "phrases_total":1,"themes_total":1,
  "next_time":{"source":"goal","text_ru":"Где ты живёшь? — в Казани: Казанда","example":"Мин Казанда яшим"}},
 "speech_level":{"estimated":"a1","shares":{"a1":0.9},"unknown_share":0.1}
}`

var sampleTurns = []ReportTurn{
	{TurnNo: 0, Role: RoleAvatar, Content: "Исәнме! Синең исемең ничек?", Input: InputText},
	{TurnNo: 1, Role: RoleStudent, Content: "Минем исемем Алсу", Input: InputVoice},
	{TurnNo: 1, Role: RoleAvatar, Content: "Син кайда яшисең?", Input: InputText},
	{TurnNo: 2, Role: RoleStudent, Content: "Мин Казанга яшим", Input: InputVoice, Edited: true},
}

func readyRow(t *testing.T, studentID uuid.UUID) ConversationRow {
	t.Helper()
	closed := time.Now()
	snap := reportSnapshot{Version: 1, ClosedAt: &closed, Level: "a2", MaxTurns: 2, Turns: sampleTurns,
		Analysis: json.RawMessage(sampleAnalysis), Incidents: json.RawMessage(`[{"kind":"asr"}]`)}
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	return ConversationRow{AttemptID: uuid.New(), StudentID: studentID, HasEngine: true, Turns: 2,
		ClosedAt: &closed, Report: raw, ReportStatus: ReportReady, Incidents: []byte(`[{"kind":"asr"}]`)}
}

// Итог ученику — что получилось и одна вещь на следующий раз. Ни ошибок,
// ни цитат, ни уровней: собран явным списком полей, а не из разбора целиком.
func TestStudentSummaryCarriesNoErrors(t *testing.T) {
	a, err := parseAnalysis(json.RawMessage(sampleAnalysis))
	if err != nil {
		t.Fatal(err)
	}
	s := studentSummary(a, []byte(`[{"kind":"asr"},{"kind":"tts"},{"kind":"asr"},{"kind":"avatar"}]`))
	raw, _ := json.Marshal(s)
	for _, leak := range []string{"СЕКРЕТ", "Казанга", "above_level", "unknown_level", "wrong_form", "goal\""} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("в итог ученика попало %q: %s", leak, raw)
		}
	}
	if s.NextTime == nil || s.NextTime.TextRU == "" || s.NextTime.Example != "Мин Казанда яшим" || s.NextTime.Source != "" {
		t.Errorf("одна вещь на следующий раз: %+v", s.NextTime)
	}
	if len(s.CreditedPhrases) != 1 || s.PhrasesTotal != 1 || s.ThemesTotal != 1 || len(s.AppliedThemes) != 0 {
		t.Errorf("зачтённое: %+v", s)
	}
	if got := jsonOf(s.Incidents); got != `[{"Kind":"asr","Count":2},{"Kind":"tts","Count":1},{"Kind":"avatar","Count":1}]` {
		t.Errorf("сбои: %s", got)
	}
	// Нечего подсказать — next_time пуст, а не пустой объект.
	a.Summary.NextTime = &NextTime{Source: "goal"}
	if studentSummary(a, nil).NextTime != nil {
		t.Error("пустая подсказка должна быть null")
	}
}

// Отчёт учителю: ошибки выше уровня не показываются — только их число;
// отметка с цитатой, которой нет в ответе, не показывается вовсе.
func TestTeacherReportHidesAboveLevelAndInventedQuotes(t *testing.T) {
	m := ClassMember{ID: uuid.New(), DisplayName: "Алсу", PublicTag: "#M2QP"}
	row := readyRow(t, m.ID)
	v := buildStudentConversation(m, AvatarBrief{MaxTurns: 2, Level: "A2"}, &row)
	if v.Status != StudentCompleted || v.ReportStatus != ReportReady || v.Report == nil {
		t.Fatalf("карточка: %+v", v)
	}
	r := v.Report
	if r.AboveLevelCount != 1 || len(r.GeneralErrors) != 2 {
		t.Errorf("общие ошибки: %+v, выше уровня %d", r.GeneralErrors, r.AboveLevelCount)
	}
	for _, e := range r.GeneralErrors {
		if e.Visibility == VisibilityAboveLevel {
			t.Errorf("ошибка выше уровня в отчёте: %+v", e)
		}
	}
	if len(r.Marks) != 2 {
		t.Errorf("отметка с выдуманной цитатой осталась: %+v", r.Marks)
	}
	if len(r.Turns) != 4 || !r.Turns[3].Edited {
		t.Errorf("расшифровка: %+v", r.Turns)
	}

	// Незавершённый разговор отчёта не имеет — учитель не видит разговор в
	// реальном времени.
	open := ConversationRow{HasEngine: true, Turns: 1}
	if v := buildStudentConversation(m, AvatarBrief{MaxTurns: 2}, &open); v.Report != nil || v.Status != StudentInProgress {
		t.Errorf("незавершённый разговор: %+v", v)
	}
	if v := buildStudentConversation(m, AvatarBrief{MaxTurns: 2}, nil); v.Status != StudentNotStarted || v.ReportStatus != "" {
		t.Errorf("не приступал: %+v", v)
	}
}

func TestClassReportAggregates(t *testing.T) {
	aidar := ClassMember{ID: uuid.New(), DisplayName: "Айдар", PublicTag: "#KZ4F"}
	alsu := ClassMember{ID: uuid.New(), DisplayName: "Алсу", PublicTag: "#M2QP"}
	bulat := ClassMember{ID: uuid.New(), DisplayName: "Булат", PublicTag: "#B7TT"}
	rows := map[uuid.UUID]*ConversationRow{}
	r1, r2 := readyRow(t, aidar.ID), readyRow(t, alsu.ID)
	rows[aidar.ID], rows[alsu.ID] = &r1, &r2
	// Булат начал, но не закончил: в темы и ошибки не идёт.
	rows[bulat.ID] = &ConversationRow{HasEngine: true, Turns: 1}
	brief := AvatarBrief{Enabled: true, Level: "A2", MaxTurns: 2, Goals: []AvatarGoal{
		{Key: "p1", Kind: GoalPhrase, Title: "Минем исемем …", Note: "Меня зовут"},
		{Key: "g1", Kind: GoalGrammar, Title: "Местный падеж"},
	}}
	members := sortedMembers([]ClassMember{bulat, alsu, aidar, {ID: uuid.New(), DisplayName: "Гөлназ"}})
	v := buildClassConversation(brief, members, rows)

	if v.Counts != (ConversationCounts{NotStarted: 1, InProgress: 1, Completed: 2}) {
		t.Errorf("статусы: %+v", v.Counts)
	}
	if v.Level != "a2" || len(v.Goals) != 2 || v.Goals[0].Note != "Меня зовут" {
		t.Errorf("шапка: %+v", v)
	}
	if s := v.Students[0]; s.Member.ID != aidar.ID || s.Credited != 1 || s.GoalsTotal != 2 || s.Errors != 1 || s.Misses != 1 {
		t.Errorf("строка ученика: %+v", s)
	}
	p1, g1 := v.Themes[0], v.Themes[1]
	if p1.CorrectUses != 4 || p1.CreditedStudents != 2 || len(p1.Students) != 2 || g1.Errors != 2 || g1.CreditedStudents != 0 {
		t.Errorf("по темам: %+v / %+v", p1, g1)
	}
	// Общие ошибки: видимая и без уровня, по две (у двух учеников), выше
	// уровня — нет.
	if len(v.CommonErrors) != 2 {
		t.Fatalf("общие ошибки: %+v", v.CommonErrors)
	}
	var verb CommonErrorGroup
	for _, g := range v.CommonErrors {
		if g.GroupKey == "verb_present" {
			verb = g
		}
	}
	if verb.Count != 2 || len(verb.Students) != 2 || verb.Label != "Настоящее время" || verb.Type != "grammar" ||
		len(verb.Students[0].Quotes) != 1 || verb.Students[0].Quotes[0].Quote != "яшим" ||
		verb.Students[0].Quotes[0].CorrectForm != "яшәм" || verb.Students[0].Member.ID != aidar.ID {
		t.Errorf("группа «по конструкции»: %+v", verb)
	}
	for _, g := range v.CommonErrors {
		if g.GroupKey == "kazan" {
			t.Error("ошибка выше уровня попала в общие ошибки класса")
		}
		if g.GroupKey == "verb_present" && g.Level != "a1" {
			t.Errorf("уровень видимой ошибки: %q", g.Level)
		}
		if g.GroupKey != "verb_present" && g.Level != "" {
			t.Errorf("ошибка без уровня получила уровень: %+v", g)
		}
	}
}
