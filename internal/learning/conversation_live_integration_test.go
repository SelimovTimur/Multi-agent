package learning

// Живой режим разговора по целям на настоящей базе (договор, раздел 10):
// открытие сессии с воротами и пределом, указание сессии на ответ и его
// повтор, закрытие на N-м ответе, реплики аватара из расшифровки с
// переводом, прощание в закрытый разговор и разбор после него, переход к
// голосовым сообщениям, проходы progress и check. Движок и голосовой
// контур — подделки портов.
//
// Без NATIVE_TEST_DATABASE_URL пропускаются, как и остальные тесты с базой.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/NATIVE/NATIVE/internal/iam"
	"github.com/NATIVE/NATIVE/internal/platform/httpx"
	"github.com/google/uuid"
)

const testSDP = "v=0\r\nm=audio 9 UDP/TLS/RTP/SAVPF 111\r\n"

// fakeLive — голосовой контур: открывает «сессии» и запоминает, с чем.
type fakeLive struct {
	mu       sync.Mutex
	opened   []AvatarBrief
	released []string
	fail     error
}

func (l *fakeLive) Supports(language string) bool { return language == "tatar" }

func (l *fakeLive) Open(context.Context, uuid.UUID, uuid.UUID, string, AvatarBrief) (LiveAnswer, error) {
	return LiveAnswer{}, httpx.Unavailable("прежний голосовой диалог в этих тестах не открывается")
}

func (l *fakeLive) OpenConversation(_ context.Context, _, _ uuid.UUID, sdp string, brief AvatarBrief) (LiveAnswer, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fail != nil {
		return LiveAnswer{}, l.fail
	}
	l.opened = append(l.opened, brief)
	return LiveAnswer{SessionID: fmt.Sprintf("live-%d", len(l.opened)), SDP: "v=0 answer to " + sdp[:3]}, nil
}

func (l *fakeLive) Release(_ context.Context, sessionID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.released = append(l.released, sessionID)
}

func (l *fakeLive) sessions() []AvatarBrief {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]AvatarBrief(nil), l.opened...)
}

type fakeGuide struct{}

func (fakeGuide) LevelGuidance(_ context.Context, _, level string) string {
	return "указания " + level
}

// withLive подключает к фикстуре голосовой контур и указания уровня.
func withLive(f *fixture) *fakeLive {
	voice := &fakeLive{}
	f.svc.live = voice
	f.svc.guide = fakeGuide{}
	return voice
}

func (f *fixture) answerLive(student iam.Actor, attempt Attempt, id, text string) (ConversationStep, error) {
	return f.svc.AnswerConversation(f.ctx, student, attempt.ID, ConversationInput{
		ClientMsgID: id, Text: text, Input: InputVoice, ASRText: text, ASRConfidence: new(0.93), Mode: ModeLive,
	})
}

func (f *fixture) avatarLine(student iam.Actor, attempt Attempt, id string, no int, text string) (string, error) {
	return f.svc.RecordAvatarLine(f.ctx, student, attempt.ID, AvatarLineInput{ClientMsgID: id, TurnNo: &no, Content: text})
}

// avatarLineSteer — реплика аватара с номером указания, по которому она шла.
func (f *fixture) avatarLineSteer(student iam.Actor, attempt Attempt, id string, no, steer int, text string) (string, error) {
	return f.svc.RecordAvatarLine(f.ctx, student, attempt.ID,
		AvatarLineInput{ClientMsgID: id, TurnNo: &no, Content: text, SteerTurn: &steer})
}

// lastObserve — последний вызов observe движка.
func (e *fakeEngine) lastObserve() EngineObserve {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.observes[len(e.observes)-1]
}

func (f *fixture) conv(t *testing.T, attempt Attempt, maxTurns int) conversation {
	t.Helper()
	c, err := f.svc.loadConversation(f.ctx, f.pool, f.org, attempt.ID, maxTurns)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (f *fixture) openLive(t *testing.T, student iam.Actor, attempt Attempt) LiveConversation {
	t.Helper()
	s, err := f.svc.OpenLiveConversation(f.ctx, student, attempt.ID, testSDP)
	if err != nil {
		t.Fatalf("живая сессия: %v", err)
	}
	return s
}

// Сквозной сценарий живого режима: сессия, вступление из расшифровки,
// ответы с указаниями и их повтор, закрытие на N-м ответе, прощание в
// закрытый разговор, разбор с репликами сессии, итог и отчёт.
func TestLiveConversationFlow(t *testing.T) {
	f, engine, a := conversationFixture(t, 2)
	voice := withLive(f)
	aidar := f.students[0]
	attempt := f.start(t, a, aidar)

	v, err := f.svc.Conversation(f.ctx, aidar, attempt.ID)
	if err != nil || !v.LiveAvailable || v.Status != ConversationNotStarted {
		t.Fatalf("разговор до начала: %+v %v", v, err)
	}
	_, err = f.answerLive(aidar, attempt, "x", "Сәлам")
	wantErr(t, err, http.StatusConflict, msgConversationNotStarted)
	_, err = f.avatarLine(aidar, attempt, "a-0", 0, "Исәнме!")
	wantErr(t, err, http.StatusConflict, msgConversationNotStarted)

	open := f.openLive(t, aidar, attempt)
	if open.Answer.SessionID != "live-1" || open.Answer.SDP == "" || open.TurnsTaken != 0 || open.MaxTurns != 2 ||
		open.Status != ConversationActive {
		t.Fatalf("живая сессия: %+v", open)
	}
	// Метку «Turn guidance» ставит Go: правила сессии ссылаются на неё.
	if open.Steer != "Turn guidance: greet the learner and lead to p1" {
		t.Errorf("указание на вступление: %q", open.Steer)
	}
	if starts, _, _ := engine.counts(); starts != 1 || engine.starts[0].ReplyMode != ReplySteer {
		t.Fatalf("начало в режиме steer: %+v", engine.starts)
	}
	// Вступление не записано: его произнесёт сессия, а запишет расшифровка.
	if c := f.conv(t, attempt, 2); len(c.turns) != 0 || !c.row.HasEngine || c.row.Sessions != 1 {
		t.Fatalf("после открытия сессии: реплик %d, движок %v, сессий %d", len(c.turns), c.row.HasEngine, c.row.Sessions)
	}
	brief := voice.sessions()[0]
	if len(brief.Goals) != 2 || brief.Goals[0].Note != "Меня зовут" || brief.LevelGuidance != "указания A2" ||
		len(brief.History) != 0 {
		t.Errorf("что ушло в политику сессии: %+v", brief)
	}

	// Вступление из расшифровки сессии — с переводом из observe.
	tr, err := f.avatarLine(aidar, attempt, "a-0", 0, "Исәнме! Синең исемең ничек?")
	if err != nil || tr != "Перевод: Исәнме! Синең исемең ничек?" {
		t.Fatalf("вступление сессии: %q %v", tr, err)
	}
	if row := f.attemptRow(t, attempt.ID); !strings.Contains(row.engine, `"observed": 0`) {
		t.Errorf("реплика аватара не попала в состояние движка: %s", row.engine)
	}
	observes := len(engine.observes)
	again, err := f.avatarLine(aidar, attempt, "a-0", 0, "Исәнме! Синең исемең ничек?")
	if err != nil || again != tr || len(engine.observes) != observes {
		t.Fatalf("повтор реплики аватара: %q %v, observe %d → %d", again, err, observes, len(engine.observes))
	}
	_, err = f.avatarLine(aidar, attempt, "a-1", 1, "Әйбәт!")
	wantErr(t, err, http.StatusConflict, msgAvatarAhead)

	first, err := f.answerLive(aidar, attempt, "s-1", "Минем исемем Айдар")
	if err != nil || first.Steer != "Turn guidance: lead to g1 after answer 1 (call 1)" || first.Reply != "" ||
		first.Translation != "" || first.TurnNo != 1 || first.TurnsTaken != 1 || first.Status != ConversationActive {
		t.Fatalf("первый ответ: %+v %v", first, err)
	}
	if in := engine.turns[0]; in.ReplyMode != ReplySteer || in.Final || len(in.History) != 1 ||
		in.History[0].Role != RoleAvatar || in.History[0].TurnNo != 0 {
		t.Errorf("что ушло в ход: %+v", in)
	}
	repeat, err := f.answerLive(aidar, attempt, "s-1", "Минем исемем Айдар")
	if err != nil || repeat != first {
		t.Fatalf("повтор ответа: %+v %v", repeat, err)
	}
	if _, turns, _ := engine.counts(); turns != 1 {
		t.Errorf("повтор с готовым указанием позвал движок: %d", turns)
	}
	v, err = f.svc.Conversation(f.ctx, aidar, attempt.ID)
	if err != nil || v.Pending != nil || len(v.Turns) != 2 || v.TurnsTaken != 1 {
		t.Fatalf("ответ с указанием не висит: %+v %v", v, err)
	}

	// Две реплики аватара на один ответ — обе пишутся.
	for i, text := range []string{"Әйбәт, Айдар!", "Син кайда яшисең?"} {
		if _, err := f.avatarLine(aidar, attempt, fmt.Sprintf("a-1-%d", i), 1, text); err != nil {
			t.Fatal(err)
		}
	}

	last, err := f.answerLive(aidar, attempt, "s-2", "Мин Казанда яшим")
	if err != nil || last.Status != ConversationClosed || last.TurnsTaken != 2 ||
		!strings.HasPrefix(last.Steer, "Turn guidance: thank the learner and say goodbye") {
		t.Fatalf("N-й ответ: %+v %v", last, err)
	}
	if in := engine.turns[1]; !in.Final || in.ReplyMode != ReplySteer {
		t.Errorf("последний ход — прощание в режиме steer: %+v", in)
	}
	if row := f.attemptRow(t, attempt.ID); !row.closed || row.turns != 2 {
		t.Fatalf("закрытие на N-м ответе: %+v", row)
	}
	// В живом режиме прощание произносит сессия: разбор ждёт его записи.
	f.waitReports(t)
	if row := f.attemptRow(t, attempt.ID); row.status != "" {
		t.Errorf("разбор начался раньше прощания: %+v", row)
	}
	_, err = f.svc.OpenLiveConversation(f.ctx, aidar, attempt.ID, testSDP)
	wantErr(t, err, http.StatusConflict, msgConversationFinished)
	_, err = f.answerLive(aidar, attempt, "s-3", "Тагын")
	wantErr(t, err, http.StatusConflict, msgConversationFinished)

	// Инструменты после закрытия: разговор кончен, проверяется прощание.
	p, err := f.svc.ConversationProgress(f.ctx, aidar, attempt.ID)
	if err != nil || !p.Finished || p.AnswersLeft != 0 {
		t.Errorf("ход разговора после закрытия: %+v %v", p, err)
	}
	if _, err := f.svc.CheckConversationReply(f.ctx, aidar, attempt.ID, "Рәхмәт! Сау бул!"); err != nil {
		t.Fatal(err)
	}
	if in := engine.checks[len(engine.checks)-1]; !in.Final {
		t.Errorf("проверка прощания без final: %+v", in)
	}

	// Прощание из расшифровки пишется и в закрытый разговор и запускает
	// разбор — в снимок оно уже попадает.
	tr, err = f.avatarLine(aidar, attempt, "a-2", 2, "Рәхмәт, Айдар! Сау бул!")
	if err != nil || tr != "Перевод: Рәхмәт, Айдар! Сау бул!" {
		t.Fatalf("прощание: %q %v", tr, err)
	}
	f.waitReports(t)
	if row := f.attemptRow(t, attempt.ID); row.status != ReportReady {
		t.Fatalf("разбор после прощания: %+v", row)
	}
	if in := engine.analyzes[0]; len(in.Transcript) != 6 || in.State == nil {
		t.Errorf("что ушло в разбор: %+v", in)
	}
	card, err := f.svc.StudentConversation(f.ctx, f.teacher, a.ID, aidar.StudentID)
	if err != nil || card.Report == nil {
		t.Fatalf("карточка ученика: %+v %v", card, err)
	}
	var avatars []ReportTurn
	for _, rt := range card.Report.Turns {
		if rt.Role == RoleAvatar {
			avatars = append(avatars, rt)
		}
	}
	if len(card.Report.Turns) != 6 || len(avatars) != 4 || avatars[3].TurnNo != 2 ||
		avatars[3].Translation != "Перевод: Рәхмәт, Айдар! Сау бул!" || avatars[1].TurnNo != 1 || avatars[2].TurnNo != 1 {
		t.Errorf("реплики сессии в отчёте учителя: %+v", card.Report.Turns)
	}
	sum, err := f.svc.ConversationSummary(f.ctx, aidar, attempt.ID)
	if err != nil || sum.Status != ReportReady || sum.Summary == nil {
		t.Fatalf("итог ученика: %+v %v", sum, err)
	}
}

// Шесть вкладок с одним ответом: засчитан один раз, указание у всех одно —
// то, что записано первым, даже если движок успели позвать несколько раз.
func TestLiveAnswerIsIdempotentAcrossTabs(t *testing.T) {
	f, _, a := conversationFixture(t, 3)
	withLive(f)
	aidar := f.students[0]
	attempt := f.start(t, a, aidar)
	f.openLive(t, aidar, attempt)

	var wg sync.WaitGroup
	results := make(chan ConversationStep, 6)
	errs := make(chan error, 6)
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			step, err := f.answerLive(aidar, attempt, "same", "Минем исемем Айдар")
			results <- step
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("ответ из вкладки: %v", err)
		}
	}
	var steer string
	for step := range results {
		if steer == "" {
			steer = step.Steer
		}
		if step.Steer == "" || step.Steer != steer || step.TurnNo != 1 || step.TurnsTaken != 1 {
			t.Errorf("вкладки получили разные указания: %+v (ожидали %q)", step, steer)
		}
	}
	if row := f.attemptRow(t, attempt.ID); row.turns != 1 {
		t.Errorf("ответ засчитан %d раз", row.turns)
	}
	c := f.conv(t, attempt, 3)
	if len(c.turns) != 1 || c.turns[0].Steer != steer {
		t.Errorf("записано: %+v", c.turns)
	}
}

// Движок не ответил: ответ сохранён и засчитан, висит, повтор с тем же
// client_msg_id получает указание на тот же ход.
func TestLiveEngineFailureKeepsTheAnswer(t *testing.T) {
	f, engine, a := conversationFixture(t, 3)
	withLive(f)
	aidar := f.students[0]
	attempt := f.start(t, a, aidar)
	f.openLive(t, aidar, attempt)
	engine.failTurns[1] = 1

	_, err := f.answerLive(aidar, attempt, "s-1", "Минем исемем Айдар")
	wantAvatarUnavailable(t, err)
	if row := f.attemptRow(t, attempt.ID); row.turns != 1 || row.incidents != 1 {
		t.Fatalf("ответ засчитан, сбой записан: %+v", row)
	}
	v, err := f.svc.Conversation(f.ctx, aidar, attempt.ID)
	if err != nil || v.Pending == nil || v.Pending.ClientMsgID != "s-1" || v.Pending.TurnNo != 1 {
		t.Fatalf("висящий ответ живого режима: %+v %v", v.Pending, err)
	}
	step, err := f.answerLive(aidar, attempt, "s-1", "Минем исемем Айдар")
	if err != nil || step.TurnNo != 1 || step.TurnsTaken != 1 || !strings.Contains(step.Steer, "after answer 1") {
		t.Fatalf("повтор после сбоя: %+v %v", step, err)
	}
	if v, _ := f.svc.Conversation(f.ctx, aidar, attempt.ID); v.Pending != nil {
		t.Errorf("указание получено — висящего нет: %+v", v.Pending)
	}
}

// Ворота сессии — как у разговора; предел сессий — как у голосового диалога
// прежнего режима; движок лежит — сессия не открывается и не считается.
func TestLiveSessionGatesAndLimit(t *testing.T) {
	f := newFixture(t)
	engine := newFakeEngine()
	f.svc.material = goalMaterial{brief: AvatarBrief{Enabled: true, MaxTurns: 3, Language: "tatar", Level: "A2",
		Goals: testGoals()}}
	f.svc.conversation = engine
	voice := withLive(f)
	t.Cleanup(func() { _ = f.svc.WaitReports(context.Background()) })
	a := f.assign(t)
	f.issue(t, a, IssueClass, "", TaskText)
	aidar := f.students[0]
	attempt := f.start(t, a, aidar)

	_, err := f.svc.OpenLiveConversation(f.ctx, aidar, attempt.ID, testSDP)
	wantErr(t, err, http.StatusConflict, msgNotIssued)
	f.issue(t, a, IssueClass, "", TaskDialog)

	_, err = f.svc.OpenLiveConversation(f.ctx, aidar, attempt.ID, "  ")
	if he := httpx.AsError(err); he == nil || he.Status != http.StatusUnprocessableEntity || he.Fields["sdp"] == "" {
		t.Errorf("пустое предложение соединения: %v", err)
	}

	setSessions := func(n int) {
		t.Helper()
		if _, err := f.pool.Exec(f.ctx, `UPDATE learning.attempts SET avatar_sessions = $2 WHERE id = $1`,
			attempt.ID, n); err != nil {
			t.Fatal(err)
		}
	}
	setSessions(maxLiveSessions(3))
	if v, err := f.svc.Conversation(f.ctx, aidar, attempt.ID); err != nil || v.LiveAvailable {
		t.Errorf("сессии исчерпаны — живого режима нет: %+v %v", v, err)
	}
	_, err = f.svc.OpenLiveConversation(f.ctx, aidar, attempt.ID, testSDP)
	wantErr(t, err, http.StatusConflict, msgLiveExhausted)
	if starts, _, _ := engine.counts(); starts != 0 || len(voice.sessions()) != 0 {
		t.Errorf("исчерпанный предел позвал движок или провайдера: %d %d", starts, len(voice.sessions()))
	}
	setSessions(0)

	engine.failStart = 1
	_, err = f.svc.OpenLiveConversation(f.ctx, aidar, attempt.ID, testSDP)
	if he := httpx.AsError(err); he == nil || he.Status != http.StatusServiceUnavailable ||
		he.Code != CodeAvatarUnavailable || he.Message != msgAvatarNoStart {
		t.Fatalf("движок не начал разговор: %v", err)
	}
	if c := f.conv(t, attempt, 3); c.row.Sessions != 0 || c.row.HasEngine || len(voice.sessions()) != 0 {
		t.Errorf("сессия при лежащем движке: сессий %d, движок %v", c.row.Sessions, c.row.HasEngine)
	}
	if row := f.attemptRow(t, attempt.ID); row.incidents != 1 {
		t.Errorf("сбой начала не в журнале: %+v", row)
	}

	f.openLive(t, aidar, attempt)
	// Состояние уже есть: указание — из хода разговора, второго начала нет.
	second := f.openLive(t, aidar, attempt)
	if starts, _, _ := engine.counts(); starts != 2 || len(engine.progresses) != 1 {
		t.Errorf("второе открытие начало разговор заново: start %d, progress %d", starts, len(engine.progresses))
	}
	if !strings.HasPrefix(second.Steer, "Turn guidance: open the conversation") ||
		!strings.Contains(second.Steer, "«Минем исемем …»") || !strings.Contains(second.Steer, "answers left: 3") {
		t.Errorf("указание из хода разговора: %q", second.Steer)
	}
	if c := f.conv(t, attempt, 3); c.row.Sessions != 2 {
		t.Errorf("сессий %d, ожидали 2", c.row.Sessions)
	}

	// Сбой провайдера доходит до ученика как есть, а место сессии
	// возвращается: несостоявшаяся сессия ничего не стоила, и в час пик
	// несколько отказов «линии заняты» не должны закрывать голос в уроке.
	voice.fail = httpx.Unavailable("Голосовой сервис сейчас занят. Попробуйте чуть позже")
	_, err = f.svc.OpenLiveConversation(f.ctx, aidar, attempt.ID, testSDP)
	if he := httpx.AsError(err); he == nil || he.Message != "Голосовой сервис сейчас занят. Попробуйте чуть позже" {
		t.Errorf("сбой провайдера: %v", err)
	}
	if c := f.conv(t, attempt, 3); c.row.Sessions != 2 {
		t.Errorf("несостоявшаяся сессия съела место: сессий %d, ожидали 2", c.row.Sessions)
	}

	// Чужая попытка — 404.
	_, err = f.svc.OpenLiveConversation(f.ctx, f.students[1], attempt.ID, testSDP)
	if he := httpx.AsError(err); he == nil || he.Status != http.StatusNotFound {
		t.Errorf("чужая попытка: %v", err)
	}

	// Голос не для этого языка и без голосового контура.
	other := newFixture(t)
	other.svc.material = goalMaterial{brief: AvatarBrief{Enabled: true, MaxTurns: 3, Language: "bashkir", Goals: testGoals()}}
	other.svc.conversation = newFakeEngine()
	withLive(other)
	oa := other.assign(t)
	other.issue(t, oa, IssueClass, "", TaskDialog)
	oat := other.start(t, oa, other.students[0])
	_, err = other.svc.OpenLiveConversation(other.ctx, other.students[0], oat.ID, testSDP)
	if he := httpx.AsError(err); he == nil || he.Status != http.StatusServiceUnavailable || he.Message != msgLiveLanguage {
		t.Errorf("язык без голоса: %v", err)
	}
	if v, err := other.svc.Conversation(other.ctx, other.students[0], oat.ID); err != nil || v.LiveAvailable {
		t.Errorf("live_available для языка без голоса: %+v %v", v, err)
	}
	other.svc.live = nil
	_, err = other.svc.OpenLiveConversation(other.ctx, other.students[0], oat.ID, testSDP)
	if he := httpx.AsError(err); he == nil || he.Status != http.StatusServiceUnavailable || he.Message != msgLiveOff {
		t.Errorf("без голосового контура: %v", err)
	}
}

// Сессия не установилась, и ученик перешёл к голосовым сообщениям: начало
// живого режима ничего не сказало, и start пишет текстовое вступление
// поверх. Сказала — start отдаёт её вступление без модели.
func TestLiveFallbackToVoiceMessages(t *testing.T) {
	f, engine, a := conversationFixture(t, 3)
	withLive(f)
	aidar, alsu := f.students[0], f.students[1]
	attempt := f.start(t, a, aidar)
	f.openLive(t, aidar, attempt)

	step, err := f.svc.StartConversation(f.ctx, aidar, attempt.ID)
	if err != nil || step.Reply != "Исәнме! Синең исемең ничек?" || step.TurnNo != 0 || step.Status != ConversationActive {
		t.Fatalf("текстовое вступление после молчаливой сессии: %+v %v", step, err)
	}
	if starts, _, _ := engine.counts(); starts != 2 || engine.starts[1].ReplyMode != ReplyText {
		t.Errorf("начала: %+v", engine.starts)
	}
	if row := f.attemptRow(t, attempt.ID); strings.Contains(row.engine, "steer") {
		t.Errorf("состояние молчаливой сессии не заменено: %s", row.engine)
	}
	if again, err := f.svc.StartConversation(f.ctx, aidar, attempt.ID); err != nil || again.Reply != step.Reply {
		t.Fatalf("повторный start: %+v %v", again, err)
	}
	if starts, _, _ := engine.counts(); starts != 2 {
		t.Errorf("повторный start позвал движок: %d", starts)
	}
	if next, err := f.answerConv(aidar, attempt, "t-1", "Минем исемем Айдар"); err != nil || next.Reply != "Җавап 1" {
		t.Fatalf("разговор продолжается голосовыми сообщениями: %+v %v", next, err)
	}

	other := f.start(t, a, alsu)
	f.openLive(t, alsu, other)
	if _, err := f.avatarLine(alsu, other, "a-0", 0, "Сәлам! Ничек яшисең?"); err != nil {
		t.Fatal(err)
	}
	step, err = f.svc.StartConversation(f.ctx, alsu, other.ID)
	if err != nil || step.Reply != "Сәлам! Ничек яшисең?" || step.Translation != "Перевод: Сәлам! Ничек яшисең?" {
		t.Fatalf("вступление сессии в режиме голосовых сообщений: %+v %v", step, err)
	}
	if starts, _, _ := engine.counts(); starts != 3 {
		t.Errorf("сказанное сессией вступление заменено новым: %d", starts)
	}
}

// Сессия говорит, пока движок оценивает ответ: observe её реплики успевает
// раньше хода. Ход всё равно пишет своё состояние — ответ последний, — а
// реплику переигрывает поверх.
func TestLiveTurnStateSurvivesObserve(t *testing.T) {
	f, engine, a := conversationFixture(t, 4)
	withLive(f)
	aidar := f.students[0]
	attempt := f.start(t, a, aidar)
	f.openLive(t, aidar, attempt)
	if _, err := f.avatarLine(aidar, attempt, "a-0", 0, "Исәнме!"); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	engine.hold[1] = release
	slow := make(chan error, 1)
	go func() {
		_, err := f.answerLive(aidar, attempt, "s-1", "Минем исемем Айдар")
		slow <- err
	}()
	if no := <-engine.entered; no != 1 {
		t.Fatalf("в движок вошёл ход %d", no)
	}
	// Сессия ответила по указанию вступления (0): указание на ответ 1 ещё
	// не пришло — движок оценивает этот ответ прямо сейчас.
	if _, err := f.avatarLineSteer(aidar, attempt, "a-1", 1, 0, "Әйбәт, Айдар!"); err != nil {
		t.Fatal(err)
	}
	if row := f.attemptRow(t, attempt.ID); !strings.Contains(row.engine, `"observed": 1`) {
		t.Fatalf("observe во время хода: %s", row.engine)
	}
	close(release)
	if err := <-slow; err != nil {
		t.Fatal(err)
	}
	row := f.attemptRow(t, attempt.ID)
	if !strings.Contains(row.engine, `"applied": 1`) || !strings.Contains(row.engine, `"observed": 1`) {
		t.Errorf("ход или реплика аватара потеряны в состоянии: %s", row.engine)
	}
	// Переигранный observe — без перевода и с тем же номером указания, что
	// пришёл с репликой: он хранится с ней.
	if last := engine.lastObserve(); last.Translate || last.TurnNo != 1 || last.SteerTurn == nil || *last.SteerTurn != 0 {
		t.Errorf("реплика переиграна поверх хода: %+v", last)
	}
}

// steer_turn — номер указания, по которому шла реплика: уходит в observe,
// хранится с репликой; отрицательный и указание на незасчитанный ответ —
// отказ; без поля — в движок поле не уходит.
func TestLiveAvatarLineSteerTurn(t *testing.T) {
	f, engine, a := conversationFixture(t, 3)
	withLive(f)
	aidar := f.students[0]
	attempt := f.start(t, a, aidar)
	f.openLive(t, aidar, attempt)

	if _, err := f.avatarLine(aidar, attempt, "a-0", 0, "Исәнме!"); err != nil {
		t.Fatal(err)
	}
	if last := engine.lastObserve(); last.SteerTurn != nil {
		t.Errorf("без steer_turn во входе поле ушло в движок: %+v", last)
	}
	if _, err := f.answerLive(aidar, attempt, "s-1", "Минем исемем Айдар"); err != nil {
		t.Fatal(err)
	}

	for _, bad := range []int{-1, 2} {
		_, err := f.avatarLineSteer(aidar, attempt, fmt.Sprintf("bad-%d", bad), 1, bad, "Әйбәт!")
		if he := httpx.AsError(err); he == nil || he.Status != http.StatusUnprocessableEntity || he.Fields["steer_turn"] == "" {
			t.Errorf("steer_turn %d: %v", bad, err)
		}
	}
	if c := f.conv(t, attempt, 3); c.avatarLines() != 1 {
		t.Errorf("отказ записал реплику: %d реплик аватара", c.avatarLines())
	}

	if _, err := f.avatarLineSteer(aidar, attempt, "a-1", 1, 0, "Әйбәт, Айдар!"); err != nil {
		t.Fatal(err)
	}
	if last := engine.lastObserve(); last.SteerTurn == nil || *last.SteerTurn != 0 || last.TurnNo != 1 || !last.Translate {
		t.Errorf("что ушло в observe: %+v", last)
	}
	c := f.conv(t, attempt, 3)
	line, ok := c.avatarLineOf("a-1")
	if !ok || line.SteerTurn == nil || *line.SteerTurn != 0 {
		t.Fatalf("номер указания не сохранён с репликой: %+v", line)
	}
	// Повтор с другим номером ничего не меняет: реплика уже записана.
	if _, err := f.avatarLineSteer(aidar, attempt, "a-1", 1, 1, "Әйбәт, Айдар!"); err != nil {
		t.Fatal(err)
	}
	if line, _ := f.conv(t, attempt, 3).avatarLineOf("a-1"); *line.SteerTurn != 0 {
		t.Errorf("повтор переписал номер указания: %d", *line.SteerTurn)
	}
}

// Расшифровка голосовой модели с казахскими буквами и турецкой латиницей
// записывается татарской кириллицей: её видят ученик и учитель, её
// переводит движок (тесты с учителями 05.10.2026).
func TestLiveAvatarLineIsStoredInTatarCyrillic(t *testing.T) {
	f, engine, a := conversationFixture(t, 3)
	withLive(f)
	aidar := f.students[0]
	attempt := f.start(t, a, aidar)
	f.openLive(t, aidar, attempt)

	if _, err := f.avatarLine(aidar, attempt, "a-0", 0, "Тағы бір рет айт. Anlamadım?"); err != nil {
		t.Fatal(err)
	}
	const want = "Тагы бер рет айт. Анламадым?"
	if line, ok := f.conv(t, attempt, 3).avatarLineOf("a-0"); !ok || line.Content != want {
		t.Errorf("записанная реплика: %q, нужна %q", line.Content, want)
	}
	if last := engine.lastObserve(); last.Reply != want {
		t.Errorf("в движок ушло %q, нужно %q", last.Reply, want)
	}
}

// Ход ответа, который разговор уже обогнал, состояние не трогает — как в
// режиме голосовых сообщений; его указание всё равно записано.
func TestLiveOlderTurnDoesNotOverwriteState(t *testing.T) {
	f, engine, a := conversationFixture(t, 4)
	withLive(f)
	aidar := f.students[0]
	attempt := f.start(t, a, aidar)
	f.openLive(t, aidar, attempt)
	release := make(chan struct{})
	engine.hold[1] = release
	slow := make(chan ConversationStep, 1)
	go func() {
		step, _ := f.answerLive(aidar, attempt, "s-1", "Минем исемем Айдар")
		slow <- step
	}()
	<-engine.entered
	fast, err := f.answerLive(aidar, attempt, "s-2", "Мин Казанда яшим")
	if err != nil || fast.TurnNo != 2 {
		t.Fatalf("второй ответ: %+v %v", fast, err)
	}
	<-engine.entered
	close(release)
	step := <-slow
	if step.TurnNo != 1 || !strings.Contains(step.Steer, "after answer 1") {
		t.Errorf("указание на обогнанный ответ: %+v", step)
	}
	if row := f.attemptRow(t, attempt.ID); !strings.Contains(row.engine, `"applied": 2`) {
		t.Errorf("ход 1 перетёр состояние хода 2: %s", row.engine)
	}
	repeat, err := f.answerLive(aidar, attempt, "s-1", "Минем исемем Айдар")
	if err != nil || repeat.Steer != step.Steer {
		t.Errorf("повтор обогнанного ответа: %+v %v", repeat, err)
	}
}

// Реплик аватара на разговор — не больше потолка: каждая стоит перевода.
// Перевод не удался — реплика всё равно записана, повтор её переводит.
func TestLiveAvatarLinesLimitAndRetry(t *testing.T) {
	f, engine, a := conversationFixture(t, 1)
	withLive(f)
	aidar := f.students[0]
	attempt := f.start(t, a, aidar)
	f.openLive(t, aidar, attempt)

	engine.failObs = 1
	_, err := f.avatarLine(aidar, attempt, "a-0", 0, "Исәнме!")
	if he := httpx.AsError(err); he == nil || he.Code != CodeAvatarUnavailable || he.Message != msgNoTranslation {
		t.Fatalf("перевод не удался: %v", err)
	}
	if c := f.conv(t, attempt, 1); len(c.turns) != 1 || c.turns[0].Translation != "" {
		t.Fatalf("реплика без перевода записана: %+v", c.turns)
	}
	tr, err := f.avatarLine(aidar, attempt, "a-0", 0, "Исәнме!")
	if err != nil || tr != "Перевод: Исәнме!" {
		t.Fatalf("повтор переводит: %q %v", tr, err)
	}
	if c := f.conv(t, attempt, 1); len(c.turns) != 1 || c.turns[0].Translation != tr {
		t.Fatalf("повтор не дублирует реплику: %+v", c.turns)
	}

	for i := 1; i < avatarLinesCap(1); i++ {
		if _, err := f.avatarLine(aidar, attempt, fmt.Sprintf("a-%d", i), 0, "Сәлам"); err != nil {
			t.Fatalf("реплика %d: %v", i, err)
		}
	}
	_, err = f.avatarLine(aidar, attempt, "over", 0, "Сәлам")
	wantErr(t, err, http.StatusConflict, msgAvatarLinesLimit)
	// Повтор уже записанной реплики потолок не трогает.
	if tr, err := f.avatarLine(aidar, attempt, "a-0", 0, "Исәнме!"); err != nil || tr != "Перевод: Исәнме!" {
		t.Errorf("повтор на потолке: %q %v", tr, err)
	}
	if _, err := f.avatarLine(f.students[1], attempt, "x", 0, "Сәлам"); httpx.AsError(err).Status != http.StatusNotFound {
		t.Errorf("чужая попытка — 404: %v", err)
	}
}

// Инструменты делегата — проход к движку: ход разговора со счётом Go и
// проверка реплики с final по закрытию разговора.
func TestLiveToolsPassThrough(t *testing.T) {
	f, engine, a := conversationFixture(t, 3)
	voice := withLive(f)
	aidar := f.students[0]
	attempt := f.start(t, a, aidar)

	_, err := f.svc.ConversationProgress(f.ctx, aidar, attempt.ID)
	wantErr(t, err, http.StatusConflict, msgConversationNotStarted)
	_, err = f.svc.CheckConversationReply(f.ctx, aidar, attempt.ID, "Сәлам")
	wantErr(t, err, http.StatusConflict, msgConversationNotStarted)

	f.openLive(t, aidar, attempt)
	if _, err := f.answerLive(aidar, attempt, "s-1", "Минем исемем Айдар"); err != nil {
		t.Fatal(err)
	}
	p, err := f.svc.ConversationProgress(f.ctx, aidar, attempt.ID)
	if err != nil || p.TargetGoal != "p1" || len(p.Goals) != 2 || p.Goals[1].Status != "closed" ||
		p.AnswersLeft != 2 || p.Finished || len(p.AvoidPhrases) != 1 || p.Corrections[0].CorrectForm != "Казанда" {
		t.Fatalf("ход разговора: %+v %v", p, err)
	}
	if got := string(engine.progresses[len(engine.progresses)-1]); got != f.attemptRow(t, attempt.ID).engine {
		t.Errorf("в движок ушло не то состояние: %s", got)
	}

	r, err := f.svc.CheckConversationReply(f.ctx, aidar, attempt.ID, "Минем исемем Айдар, ә синеке?")
	if err != nil || r.OK || len(r.Problems) != 1 {
		t.Fatalf("проверка реплики: %+v %v", r, err)
	}
	r, err = f.svc.CheckConversationReply(f.ctx, aidar, attempt.ID, "  Әйбәт!  ")
	if err != nil || !r.OK || r.Problems == nil || len(r.Problems) != 0 {
		t.Fatalf("годная реплика: %+v %v", r, err)
	}
	if in := engine.checks[len(engine.checks)-1]; in.Final || in.Text != "Әйбәт!" {
		t.Errorf("что ушло в проверку: %+v", in)
	}
	_, err = f.svc.CheckConversationReply(f.ctx, aidar, attempt.ID, "   ")
	if he := httpx.AsError(err); he == nil || he.Status != http.StatusUnprocessableEntity || he.Fields["text"] == "" {
		t.Errorf("пустая реплика: %v", err)
	}
	if _, err := f.svc.ConversationProgress(f.ctx, f.students[1], attempt.ID); httpx.AsError(err).Status != http.StatusNotFound {
		t.Errorf("чужая попытка — 404: %v", err)
	}

	// Конец сессии освобождает место на ключе.
	if err := f.svc.EndLiveDialog(f.ctx, aidar, attempt.ID, "live-1"); err != nil {
		t.Fatal(err)
	}
	voice.mu.Lock()
	released := append([]string(nil), voice.released...)
	voice.mu.Unlock()
	if len(released) != 1 || released[0] != "live-1" {
		t.Errorf("освобождено: %v", released)
	}
}
