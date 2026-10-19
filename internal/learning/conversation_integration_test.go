package learning

// Разговор по целям на настоящей базе: идемпотентность ответа, сбой модели
// и повтор, закрытие на N-м ответе, аренда разбора и однократная запись
// отчёта, итог ученику и отчёт учителю. Движок разговора — подделка порта:
// проверяется learning — его SQL, транзакции и правила, а не модель.
//
// Без NATIVE_TEST_DATABASE_URL пропускаются, как и остальные тесты с базой.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NATIVE/NATIVE/internal/iam"
	"github.com/NATIVE/NATIVE/internal/platform/httpx"
	"github.com/NATIVE/NATIVE/internal/platform/idgen"
	"github.com/NATIVE/NATIVE/internal/platform/telemetry"
	"github.com/google/uuid"
)

// fakeEngine — движок разговора: отвечает по номеру хода, умеет падать и
// придерживать ход, запоминает, что ему прислали. Живой режим (observe,
// progress, check) — в conversation_live_integration_test.go.
type fakeEngine struct {
	mu         sync.Mutex
	starts     []EngineStart
	turns      []EngineTurn
	analyzes   []EngineAnalyze
	observes   []EngineObserve
	progresses []json.RawMessage
	checks     []EngineCheck
	failStart  int
	failTurns  map[int]int
	failObs    int
	hold       map[int]chan struct{}
	entered    chan int
	analysis   string
	analyzeErr error
}

func newFakeEngine() *fakeEngine {
	return &fakeEngine{failTurns: map[int]int{}, hold: map[int]chan struct{}{},
		entered: make(chan int, 64), analysis: sampleAnalysis}
}

var errModelDown = httpx.Unavailable("ML-сервис недоступен, попробуйте ещё раз")

func (e *fakeEngine) Start(_ context.Context, _, _ uuid.UUID, in EngineStart) (EngineReply, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.starts = append(e.starts, in)
	if e.failStart > 0 {
		e.failStart--
		return EngineReply{}, errModelDown
	}
	if in.ReplyMode == ReplySteer {
		// Режим steer: реплики нет, есть указание сессии — без метки, её
		// ставит Go.
		return EngineReply{State: json.RawMessage(fmt.Sprintf(`{"applied": 0, "history": %d, "mode": "steer"}`,
			len(in.History))), Steer: "greet the learner and lead to p1", TargetGoal: "p1"}, nil
	}
	return EngineReply{State: json.RawMessage(fmt.Sprintf(`{"applied": 0, "history": %d}`, len(in.History))),
		Reply: "Исәнме! Синең исемең ничек?", ReplyRU: "Привет! Как тебя зовут?", TargetGoal: "p1"}, nil
}

func (e *fakeEngine) Turn(_ context.Context, _, _ uuid.UUID, in EngineTurn) (EngineReply, error) {
	no := in.Answer.TurnNo
	e.mu.Lock()
	e.turns = append(e.turns, in)
	call := len(e.turns)
	hold := e.hold[no]
	fail := e.failTurns[no] > 0
	if fail {
		e.failTurns[no]--
	}
	e.mu.Unlock()
	e.entered <- no
	if hold != nil {
		<-hold
	}
	if fail {
		return EngineReply{}, errModelDown
	}
	if in.ReplyMode == ReplySteer {
		// Номер вызова в указании: повтор, позвавший движок второй раз,
		// получил бы другое указание — тест это заметит.
		steer := fmt.Sprintf("lead to g1 after answer %d (call %d)", no, call)
		if in.Final {
			steer = fmt.Sprintf("thank the learner and say goodbye without a question (call %d)", call)
		}
		return EngineReply{State: json.RawMessage(fmt.Sprintf(`{"applied": %d}`, no)), Steer: steer}, nil
	}
	reply := fmt.Sprintf("Җавап %d", no)
	if in.Final {
		reply = "Рәхмәт! Сау бул!"
	}
	return EngineReply{State: json.RawMessage(fmt.Sprintf(`{"applied": %d}`, no)),
		Reply: reply, ReplyRU: fmt.Sprintf("Ответ %d", no)}, nil
}

func (e *fakeEngine) Analyze(_ context.Context, _, _ uuid.UUID, in EngineAnalyze) (json.RawMessage, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.analyzes = append(e.analyzes, in)
	if e.analyzeErr != nil {
		return nil, e.analyzeErr
	}
	return json.RawMessage(e.analysis), nil
}

func (e *fakeEngine) counts() (int, int, int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.starts), len(e.turns), len(e.analyzes)
}

// Observe запоминает реплику: новое состояние оборачивает прежнее, и по нему
// видно, какие реплики в него попали.
func (e *fakeEngine) Observe(_ context.Context, _, _ uuid.UUID, in EngineObserve) (EngineObservation, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.observes = append(e.observes, in)
	if e.failObs > 0 {
		e.failObs--
		return EngineObservation{}, errModelDown
	}
	state, _ := json.Marshal(map[string]any{"observed": in.TurnNo, "reply": in.Reply, "before": in.State})
	out := EngineObservation{State: state}
	if in.Translate {
		out.ReplyRU = "Перевод: " + in.Reply
	}
	return out, nil
}

func (e *fakeEngine) Progress(_ context.Context, _, _ uuid.UUID, state json.RawMessage) (EngineProgress, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.progresses = append(e.progresses, state)
	return EngineProgress{
		Goals: []ProgressGoal{
			{Key: "p1", Kind: GoalPhrase, Title: "Минем исемем …", Note: "Меня зовут", Status: "open"},
			{Key: "g1", Kind: GoalGrammar, Title: "Местный падеж", Note: "-да/-дә", Status: "closed"},
		},
		TargetGoal:   "p1",
		AvoidPhrases: []string{"Минем исемем …"},
		Corrections:  []Correction{{Quote: "Казанга", CorrectForm: "Казанда"}},
		// Счёт движка нарочно не совпадает со счётом Go: ответ отдаёт Go.
		AnswersLeft: 99,
	}, nil
}

func (e *fakeEngine) Check(_ context.Context, _, _ uuid.UUID, in EngineCheck) (EngineCheckResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.checks = append(e.checks, in)
	if strings.Contains(in.Text, "Минем исемем") {
		return EngineCheckResult{Problems: []string{"фокусная фраза «Минем исемем …» ещё не зачтена"}}, nil
	}
	return EngineCheckResult{OK: true}, nil
}

type goalMaterial struct{ brief AvatarBrief }

func (m goalMaterial) AvatarBriefFor(context.Context, uuid.UUID, uuid.UUID) (AvatarBrief, error) {
	return m.brief, nil
}

func (m goalMaterial) TeacherAvatarBrief(ctx context.Context, actor iam.Actor, versionID uuid.UUID) (AvatarBrief, error) {
	return m.AvatarBriefFor(ctx, actor.OrgID, versionID)
}

func testGoals() []AvatarGoal {
	return []AvatarGoal{
		{Key: "p1", Kind: GoalPhrase, Title: "Минем исемем …", Note: "Меня зовут"},
		{Key: "g1", Kind: GoalGrammar, Title: "Местный падеж", Note: "-да/-дә"},
	}
}

// conversationFixture — класс с уроком, где выдан на уроке «Диалог»
// длиной maxTurns ответов.
func conversationFixture(t *testing.T, maxTurns int) (*fixture, *fakeEngine, Assignment) {
	t.Helper()
	f := newFixture(t)
	engine := newFakeEngine()
	f.svc.material = goalMaterial{brief: AvatarBrief{
		Enabled: true, MaxTurns: maxTurns, Language: "tatar", Level: "A2",
		Text: "Минем исемем Алсу. Мин Казанда яшим.", Collocations: "исем — имя",
		Goals: testGoals(), DictionaryEnabled: true, PracticePhrases: []string{"Минем исемем …"},
	}}
	f.svc.conversation = engine
	// Разборы идут в фоне: ждём их до того, как чистка базы удалит попытку.
	t.Cleanup(func() { _ = f.svc.WaitReports(context.Background()) })
	a := f.assign(t)
	f.issue(t, a, IssueClass, "", TaskDialog)
	return f, engine, a
}

func (f *fixture) answerConv(student iam.Actor, attempt Attempt, id, text string) (ConversationStep, error) {
	return f.svc.AnswerConversation(f.ctx, student, attempt.ID, ConversationInput{
		ClientMsgID: id, Text: text, Input: InputVoice, ASRText: text, ASRConfidence: new(0.9),
	})
}

func (f *fixture) waitReports(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
	defer cancel()
	if err := f.svc.WaitReports(ctx); err != nil {
		t.Fatalf("разбор не закончился: %v", err)
	}
}

// attemptRow — колонки разговора прямо из базы.
type attemptRow struct {
	turns     int
	closed    bool
	status    string
	report    string
	incidents int
	engine    string
}

func (f *fixture) attemptRow(t *testing.T, attemptID uuid.UUID) attemptRow {
	t.Helper()
	var r attemptRow
	var report, engine *string
	err := f.pool.QueryRow(f.ctx, `
		SELECT avatar_turns, avatar_closed_at IS NOT NULL, avatar_report_status, avatar_report::text,
		       jsonb_array_length(avatar_incidents), avatar_engine::text
		FROM learning.attempts WHERE id = $1`, attemptID).
		Scan(&r.turns, &r.closed, &r.status, &report, &r.incidents, &engine)
	if err != nil {
		t.Fatal(err)
	}
	if report != nil {
		r.report = *report
	}
	if engine != nil {
		r.engine = *engine
	}
	return r
}

func wantAvatarUnavailable(t *testing.T, err error) {
	t.Helper()
	he := httpx.AsError(err)
	if he == nil || he.Status != http.StatusServiceUnavailable || he.Code != CodeAvatarUnavailable || he.Message != msgAvatarNoAnswer {
		t.Fatalf("ожидали 503 avatar_unavailable, получили %v", err)
	}
}

// Сквозной сценарий: начать, ответить, повторить, закрыть N-м ответом,
// получить итог и отчёт.
func TestConversationFlow(t *testing.T) {
	f, engine, a := conversationFixture(t, 2)
	aidar := f.students[0]
	attempt := f.start(t, a, aidar)

	v, err := f.svc.Conversation(f.ctx, aidar, attempt.ID)
	if err != nil || v.Status != ConversationNotStarted || !v.Enabled || v.Level != "a2" || v.Language != "tatar" ||
		v.MaxTurns != 2 || len(v.Turns) != 0 || len(v.Phrases) != 1 {
		t.Fatalf("разговор до начала: %+v %v", v, err)
	}
	_, err = f.answerConv(aidar, attempt, "x", "Сәлам")
	wantErr(t, err, http.StatusConflict, msgConversationNotStarted)

	start, err := f.svc.StartConversation(f.ctx, aidar, attempt.ID)
	if err != nil || start.TurnNo != 0 || start.Status != ConversationActive || start.Reply == "" ||
		start.Translation != "Привет! Как тебя зовут?" || start.TurnsTaken != 0 || start.MaxTurns != 2 {
		t.Fatalf("начало: %+v %v", start, err)
	}
	again, err := f.svc.StartConversation(f.ctx, aidar, attempt.ID)
	if err != nil || again.Reply != start.Reply || again.TurnNo != 0 {
		t.Fatalf("повторный start: %+v %v", again, err)
	}
	if starts, _, _ := engine.counts(); starts != 1 {
		t.Errorf("повторный start позвал модель: %d", starts)
	}
	// Цели и уровень ушли в движок строкой договора.
	if in := engine.starts[0]; in.Level != "a2" || len(in.Goals) != 2 || in.Goals[0].Note != "Меня зовут" || in.MaxTurns != 2 {
		t.Errorf("что ушло в start: %+v", in)
	}

	first, err := f.answerConv(aidar, attempt, "x", "Минем исемем Алсу")
	if err != nil || first.TurnNo != 1 || first.TurnsTaken != 1 || first.Reply != "Җавап 1" ||
		first.Status != ConversationActive || first.ReportStatus != "" {
		t.Fatalf("первый ответ: %+v %v", first, err)
	}
	repeat, err := f.answerConv(aidar, attempt, "x", "Минем исемем Алсу")
	if err != nil || repeat != first {
		t.Fatalf("повтор того же client_msg_id: %+v %v", repeat, err)
	}
	if _, turns, _ := engine.counts(); turns != 1 {
		t.Errorf("повтор с готовым ответом позвал модель: %d", turns)
	}

	// N-й ответ из нескольких вкладок разом: засчитан один раз.
	var wg sync.WaitGroup
	results := make(chan ConversationStep, 6)
	errs := make(chan error, 6)
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			step, err := f.svc.AnswerConversation(f.ctx, aidar, attempt.ID, ConversationInput{
				ClientMsgID: "y", Text: "Мин Казанга яшим", Input: InputVoice,
				ASRText: "Мин Казанга яшем", ASRConfidence: new(0.6), Edited: true,
			})
			results <- step
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("повтор N-го ответа: %v", err)
		}
	}
	for step := range results {
		if step.TurnNo != 2 || step.TurnsTaken != 2 || step.Status != ConversationClosed || step.Reply != "Рәхмәт! Сау бул!" {
			t.Errorf("N-й ответ: %+v", step)
		}
	}
	row := f.attemptRow(t, attempt.ID)
	if row.turns != 2 || !row.closed {
		t.Fatalf("после N-го ответа: %+v", row)
	}
	turns, err := f.svc.store.ConversationTurns(f.ctx, f.pool, f.org, attempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	var students, avatars int
	for _, tr := range turns {
		if tr.Role == RoleStudent {
			students++
		} else {
			avatars++
		}
	}
	if students != 2 || avatars != 3 {
		t.Errorf("реплик ученика %d, аватара %d — повторы записаны дважды?", students, avatars)
	}
	last := turns[len(turns)-2]
	if last.Role != RoleStudent || last.Input != InputVoice || last.ASRText != "Мин Казанга яшем" || !last.Edited ||
		last.ASRConfidence == nil || *last.ASRConfidence < 0.59 {
		t.Errorf("поля голосового ответа: %+v", last)
	}

	// Финальный ход ушёл в движок с final, первый — без.
	engine.mu.Lock()
	for _, in := range engine.turns {
		if in.Final != (in.Answer.TurnNo == 2) {
			t.Errorf("final на ходе %d: %v", in.Answer.TurnNo, in.Final)
		}
		if in.Answer.TurnNo == 2 && (len(in.History) != 3 || in.History[1].Content != "Минем исемем Алсу") {
			t.Errorf("история к последнему ходу: %+v", in.History)
		}
	}
	engine.mu.Unlock()

	// Закрытие отметило «Диалог» сделанным.
	progress, err := f.svc.store.ProgressOfAttempts(f.ctx, f.pool, f.org, []uuid.UUID{attempt.ID})
	if err != nil {
		t.Fatal(err)
	}
	done := false
	for _, p := range progress {
		if p.TaskKey == TaskDialog && p.DoneAt != nil {
			done = true
		}
	}
	if !done {
		t.Errorf("задание «Диалог» не отмечено сделанным: %+v", progress)
	}

	_, err = f.answerConv(aidar, attempt, "z", "Тагын бер җавап")
	wantErr(t, err, http.StatusConflict, msgConversationFinished)

	f.waitReports(t)
	row = f.attemptRow(t, attempt.ID)
	if row.status != ReportReady || row.report == "" {
		t.Fatalf("отчёт не готов: %+v", row)
	}
	_, _, analyzes := engine.counts()
	if analyzes != 1 {
		t.Fatalf("разборов %d, ожидали 1", analyzes)
	}
	if in := engine.analyzes[0]; in.State == nil || len(in.Transcript) != 5 || in.Level != "a2" ||
		!in.Transcript[3].Edited || in.Transcript[3].Input != InputVoice {
		t.Errorf("что ушло в разбор: %+v", in)
	}

	sum, err := f.svc.ConversationSummary(f.ctx, aidar, attempt.ID)
	if err != nil || sum.Status != ReportReady || sum.Summary == nil || len(sum.Summary.CreditedPhrases) != 1 ||
		sum.Summary.NextTime == nil {
		t.Fatalf("итог: %+v %v", sum, err)
	}
	// Отчёт неизменен: повторный итог разбор не запускает.
	if _, err := f.svc.ConversationSummary(f.ctx, aidar, attempt.ID); err != nil {
		t.Fatal(err)
	}
	f.waitReports(t)
	if _, _, analyzes := engine.counts(); analyzes != 1 {
		t.Errorf("готовый отчёт разобран повторно: %d", analyzes)
	}

	// Учитель: класс и карточка ученика.
	cls, err := f.svc.ClassConversation(f.ctx, f.teacher, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cls.Counts != (ConversationCounts{NotStarted: 1, Completed: 1}) || len(cls.Students) != 2 ||
		cls.Students[0].Status != StudentCompleted || cls.Students[0].Credited != 1 || len(cls.Themes) != 2 {
		t.Errorf("отчёт класса: %+v", cls)
	}
	card, err := f.svc.StudentConversation(f.ctx, f.teacher, a.ID, aidar.StudentID)
	if err != nil || card.Report == nil || card.Report.AboveLevelCount != 1 || len(card.Report.Marks) != 2 ||
		len(card.Report.Turns) != 5 || card.ReportStatus != ReportReady {
		t.Fatalf("карточка ученика: %+v %v", card, err)
	}
	other, err := f.svc.StudentConversation(f.ctx, f.teacher, a.ID, f.students[1].StudentID)
	if err != nil || other.Status != StudentNotStarted || other.Report != nil {
		t.Errorf("не приступавший ученик: %+v %v", other, err)
	}
	if _, err := f.svc.StudentConversation(f.ctx, f.teacher, a.ID, uuid.New()); httpx.AsError(err).Status != http.StatusNotFound {
		t.Errorf("чужой ученик — 404: %v", err)
	}
	if _, err := f.svc.ClassConversation(f.ctx, iam.Actor{Kind: "user", UserID: uuid.New(), OrgID: f.org}, a.ID); httpx.AsError(err).Status != http.StatusNotFound {
		t.Errorf("чужой учитель — 404: %v", err)
	}
}

// Модель не ответила: ответ сохранён и засчитан, ученик видит понятное
// сообщение, сбой — в журнале попытки, повтор с тем же client_msg_id
// получает ответ на тот же ход и не засчитывается второй раз.
func TestAvatarFailureKeepsTheAnswer(t *testing.T) {
	f, engine, a := conversationFixture(t, 4)
	aidar := f.students[0]
	attempt := f.start(t, a, aidar)
	if _, err := f.svc.StartConversation(f.ctx, aidar, attempt.ID); err != nil {
		t.Fatal(err)
	}
	engine.failTurns[1] = 1

	_, err := f.answerConv(aidar, attempt, "x", "Минем исемем Алсу")
	wantAvatarUnavailable(t, err)
	row := f.attemptRow(t, attempt.ID)
	if row.turns != 1 || row.incidents != 1 {
		t.Fatalf("ответ засчитан и сбой записан: %+v", row)
	}
	v, err := f.svc.Conversation(f.ctx, aidar, attempt.ID)
	if err != nil || v.Pending == nil || v.Pending.ClientMsgID != "x" || v.Pending.TurnNo != 1 ||
		v.Pending.Text != "Минем исемем Алсу" || v.TurnsTaken != 1 {
		t.Fatalf("после сбоя ученик продолжает с того же места: %+v %v", v, err)
	}

	step, err := f.answerConv(aidar, attempt, "x", "Минем исемем Алсу")
	if err != nil || step.TurnNo != 1 || step.TurnsTaken != 1 || step.Reply != "Җавап 1" {
		t.Fatalf("повтор после сбоя: %+v %v", step, err)
	}
	engine.mu.Lock()
	if len(engine.turns) != 2 || engine.turns[0].Answer.TurnNo != 1 || engine.turns[1].Answer.TurnNo != 1 {
		t.Errorf("повтор генерирует тот же ход: %+v", engine.turns)
	}
	engine.mu.Unlock()
	if v, _ := f.svc.Conversation(f.ctx, aidar, attempt.ID); v.Pending != nil || v.TurnsTaken != 1 {
		t.Errorf("ответ получен — висящего нет: %+v", v.Pending)
	}

	// Сбой начала — тот же код и запись в журнал.
	alsu := f.students[1]
	other := f.start(t, a, alsu)
	engine.failStart = 1
	_, err = f.svc.StartConversation(f.ctx, alsu, other.ID)
	if he := httpx.AsError(err); he == nil || he.Code != CodeAvatarUnavailable {
		t.Fatalf("сбой начала: %v", err)
	}
	if r := f.attemptRow(t, other.ID); r.incidents != 1 || r.engine != "" {
		t.Errorf("сбой начала в журнале, разговор не начат: %+v", r)
	}

	// Сбои распознавания и озвучки от браузера — с потолком.
	if err := f.svc.ConversationIncident(f.ctx, aidar, attempt.ID, IncidentInput{Kind: IncidentTTS, TurnNo: intp(1), Message: "audio error"}); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.ConversationIncident(f.ctx, aidar, attempt.ID, IncidentInput{Kind: "video"}); httpx.AsError(err).Status != http.StatusUnprocessableEntity {
		t.Errorf("неизвестный вид сбоя: %v", err)
	}
	for range incidentLimit + 5 {
		if err := f.svc.ConversationIncident(f.ctx, aidar, attempt.ID, IncidentInput{Kind: IncidentASR}); err != nil {
			t.Fatal(err)
		}
	}
	if r := f.attemptRow(t, attempt.ID); r.incidents != incidentLimit {
		t.Errorf("журнал сбоев вырос до %d", r.incidents)
	}
	if err := f.svc.ConversationIncident(f.ctx, alsu, attempt.ID, IncidentInput{Kind: IncidentASR}); httpx.AsError(err).Status != http.StatusNotFound {
		t.Errorf("чужая попытка — 404: %v", err)
	}
}

// N-й ответ закрывает разговор сразу, даже если прощания аватар не сказал:
// разбор не ждёт повтора, а повтор потом получает прощание.
func TestNthAnswerClosesEvenWithoutFarewell(t *testing.T) {
	f, engine, a := conversationFixture(t, 2)
	aidar := f.students[0]
	attempt := f.start(t, a, aidar)
	if _, err := f.svc.StartConversation(f.ctx, aidar, attempt.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.answerConv(aidar, attempt, "a", "Минем исемем Алсу"); err != nil {
		t.Fatal(err)
	}
	engine.failTurns[2] = 1
	_, err := f.answerConv(aidar, attempt, "b", "Мин Казанга яшим")
	wantAvatarUnavailable(t, err)

	v, err := f.svc.Conversation(f.ctx, aidar, attempt.ID)
	if err != nil || v.Status != ConversationClosed || v.Pending == nil || v.Pending.TurnNo != 2 {
		t.Fatalf("закрыт без прощания: %+v %v", v, err)
	}
	f.waitReports(t)
	before := f.attemptRow(t, attempt.ID)
	if before.status != ReportReady {
		t.Fatalf("разбор не дождался прощания? %+v", before)
	}

	step, err := f.answerConv(aidar, attempt, "b", "Мин Казанга яшим")
	if err != nil || step.TurnNo != 2 || step.Reply != "Рәхмәт! Сау бул!" || step.Status != ConversationClosed ||
		step.ReportStatus != ReportReady {
		t.Fatalf("повтор последнего ответа: %+v %v", step, err)
	}
	f.waitReports(t)
	if after := f.attemptRow(t, attempt.ID); after.report != before.report {
		t.Error("отчёт изменился после прощания")
	}
}

// Две вкладки: ход, посчитанный от старого состояния, не перетирает
// состояние, которое успела продвинуть соседняя.
func TestEngineStateMovesOnlyForward(t *testing.T) {
	f, engine, a := conversationFixture(t, 4)
	aidar := f.students[0]
	attempt := f.start(t, a, aidar)
	if _, err := f.svc.StartConversation(f.ctx, aidar, attempt.ID); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	engine.hold[1] = release

	slow := make(chan error, 1)
	go func() {
		_, err := f.answerConv(aidar, attempt, "tab-a", "Минем исемем Алсу")
		slow <- err
	}()
	if no := <-engine.entered; no != 1 {
		t.Fatalf("первым в движок вошёл ход %d", no)
	}
	fast, err := f.answerConv(aidar, attempt, "tab-b", "Мин Казанда яшим")
	if err != nil || fast.TurnNo != 2 {
		t.Fatalf("вторая вкладка: %+v %v", fast, err)
	}
	<-engine.entered
	if got := f.attemptRow(t, attempt.ID).engine; !strings.Contains(got, `"applied": 2`) {
		t.Fatalf("состояние после хода 2: %s", got)
	}
	close(release)
	if err := <-slow; err != nil {
		t.Fatalf("первая вкладка: %v", err)
	}
	row := f.attemptRow(t, attempt.ID)
	if !strings.Contains(row.engine, `"applied": 2`) {
		t.Errorf("ход 1 перетёр состояние хода 2: %s", row.engine)
	}
	if row.turns != 2 {
		t.Errorf("засчитано %d ответов", row.turns)
	}
	// Реплика на ход 1 всё равно записана — ученик её получил.
	conv, err := f.svc.loadConversation(f.ctx, f.pool, f.org, attempt.ID, 4)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := conv.replyTo(1); !ok {
		t.Error("реплика на ход 1 не записана")
	}
}

// Аренда разбора: один разбор за раз, зависший перезапускается через три
// минуты, упавший — не чаще раза в минуту, отчёт пишется один раз.
func TestReportLeaseAndSingleWrite(t *testing.T) {
	f, _, a := conversationFixture(t, 2)
	attempt := f.start(t, a, f.students[0])
	st := f.svc.store
	claim := func() (time.Time, bool) {
		t.Helper()
		at, ok, err := st.ClaimReport(f.ctx, f.pool, f.org, attempt.ID, 2, reportLease, reportRetry)
		if err != nil {
			t.Fatal(err)
		}
		return at, ok
	}
	backdate := func(d time.Duration) {
		t.Helper()
		if _, err := f.pool.Exec(f.ctx, `UPDATE learning.attempts
			SET avatar_report_started_at = avatar_report_started_at - make_interval(secs => $2)
			WHERE id = $1`, attempt.ID, d.Seconds()); err != nil {
			t.Fatal(err)
		}
	}

	if _, ok := claim(); ok {
		t.Fatal("незакрытый разговор не разбирается")
	}
	if err := st.CloseConversation(f.ctx, f.pool, f.org, attempt.ID); err != nil {
		t.Fatal(err)
	}
	first, ok := claim()
	if !ok {
		t.Fatal("закрытый разговор разбирается")
	}
	if _, ok := claim(); ok {
		t.Fatal("вторая аренда при живой первой")
	}
	backdate(4 * time.Minute)
	second, ok := claim()
	if !ok {
		t.Fatal("зависший разбор не перезапущен")
	}
	// Опоздавший первый разбор не отмечает неудачу за второй.
	if err := st.FailReport(f.ctx, f.pool, f.org, attempt.ID, first.Add(-4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if r := f.attemptRow(t, attempt.ID); r.status != ReportPending {
		t.Fatalf("чужая аренда отметила сбой: %+v", r)
	}
	if err := st.FailReport(f.ctx, f.pool, f.org, attempt.ID, second); err != nil {
		t.Fatal(err)
	}
	if r := f.attemptRow(t, attempt.ID); r.status != ReportFailed {
		t.Fatalf("сбой своей аренды: %+v", r)
	}
	if _, ok := claim(); ok {
		t.Fatal("упавший разбор перезапущен раньше минуты")
	}
	backdate(2 * time.Minute)
	if _, ok := claim(); !ok {
		t.Fatal("упавший разбор не перезапущен через минуту")
	}

	saved, err := st.SaveReport(f.ctx, f.pool, f.org, attempt.ID, []byte(`{"version":1,"analysis":{"goals":[]}}`), 2, 6)
	if err != nil || !saved {
		t.Fatalf("первая запись отчёта: %v %v", saved, err)
	}
	saved, err = st.SaveReport(f.ctx, f.pool, f.org, attempt.ID, []byte(`{"version":1,"analysis":{"other":true}}`), 5, 6)
	if err != nil || saved {
		t.Fatalf("вторая запись отчёта прошла: %v %v", saved, err)
	}
	r := f.attemptRow(t, attempt.ID)
	if r.status != ReportReady || strings.Contains(r.report, "other") {
		t.Errorf("отчёт переписан: %+v", r)
	}
	// Счёт выражений для звезды пишется вместе с отчётом и так же один раз.
	words, err := st.ConversationWords(f.ctx, f.pool, f.org, []uuid.UUID{attempt.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got := words[attempt.ID]; got != (ConversationWords{State: WordsReady, Used: 2, Total: 6}) {
		t.Errorf("счёт выражений разговора: %+v", got)
	}
	backdate(10 * time.Minute)
	if _, ok := claim(); ok {
		t.Error("готовый отчёт разбирается заново")
	}
}

// Упавший разбор: итог говорит «failed», перезапуск — не раньше минуты,
// потом разбор доходит до готового отчёта.
func TestFailedReportIsRetried(t *testing.T) {
	f, engine, a := conversationFixture(t, 1)
	aidar := f.students[0]
	attempt := f.start(t, a, aidar)
	if _, err := f.svc.StartConversation(f.ctx, aidar, attempt.ID); err != nil {
		t.Fatal(err)
	}
	engine.analyzeErr = errors.New("модель не ответила")
	if _, err := f.answerConv(aidar, attempt, "a", "Минем исемем Алсу"); err != nil {
		t.Fatal(err)
	}
	f.waitReports(t)
	sum, err := f.svc.ConversationSummary(f.ctx, aidar, attempt.ID)
	if err != nil || sum.Status != ReportFailed || sum.Summary != nil {
		t.Fatalf("упавший разбор: %+v %v", sum, err)
	}
	if _, _, n := engine.counts(); n != 1 {
		t.Fatalf("перезапуск раньше минуты: %d разборов", n)
	}
	if _, err := f.pool.Exec(f.ctx, `UPDATE learning.attempts
		SET avatar_report_started_at = now() - interval '2 minutes' WHERE id = $1`, attempt.ID); err != nil {
		t.Fatal(err)
	}
	engine.mu.Lock()
	engine.analyzeErr = nil
	engine.mu.Unlock()
	sum, err = f.svc.ConversationSummary(f.ctx, aidar, attempt.ID)
	if err != nil || sum.Status != ReportPending {
		t.Fatalf("перезапуск: %+v %v", sum, err)
	}
	f.waitReports(t)
	sum, err = f.svc.ConversationSummary(f.ctx, aidar, attempt.ID)
	if err != nil || sum.Status != ReportReady || sum.Summary == nil {
		t.Fatalf("после перезапуска: %+v %v", sum, err)
	}
	if s, _ := f.svc.ConversationSummary(f.ctx, f.students[1], attempt.ID); s.Summary != nil {
		t.Error("итог чужой попытки")
	}
}

// Разговор прежних режимов продолжается: движок получает его реплики, номера
// ходов продолжают их. Исчерпанный — закрывается и разбирается без движка.
func TestLegacyConversation(t *testing.T) {
	f, engine, a := conversationFixture(t, 3)
	aidar, alsu := f.students[0], f.students[1]
	st := f.svc.store

	legacy := func(attempt Attempt, student iam.Actor, lines ...string) {
		t.Helper()
		for i, text := range lines {
			role := RoleAvatar
			if i%2 == 1 {
				role = RoleStudent
			}
			if err := st.InsertDialogTurn(f.ctx, f.pool, DialogTurn{ID: idgen.New(), OrgID: f.org,
				AttemptID: attempt.ID, StudentID: student.StudentID, Role: role, Content: text}); err != nil {
				t.Fatal(err)
			}
			if role == RoleStudent {
				if _, err := st.AddAvatarTurn(f.ctx, f.pool, f.org, attempt.ID); err != nil {
					t.Fatal(err)
				}
			}
			time.Sleep(2 * time.Millisecond) // порядок реплик — по времени записи
		}
	}

	first := f.start(t, a, aidar)
	legacy(first, aidar, "Исәнме!", "Исәнмесез", "Ничек яшисең?")
	start, err := f.svc.StartConversation(f.ctx, aidar, first.ID)
	if err != nil || start.TurnNo != 1 || start.TurnsTaken != 1 || start.Status != ConversationActive {
		t.Fatalf("продолжение прежнего разговора: %+v %v", start, err)
	}
	in := engine.starts[0]
	if len(in.History) != 3 || in.History[1].TurnNo != 1 || in.History[1].Role != RoleStudent || in.History[2].TurnNo != 1 {
		t.Errorf("история прежнего разговора: %+v", in.History)
	}
	step, err := f.answerConv(aidar, first, "n", "Әйбәт")
	if err != nil || step.TurnNo != 2 || step.TurnsTaken != 2 {
		t.Fatalf("ответ после прежних реплик: %+v %v", step, err)
	}

	second := f.start(t, a, alsu)
	legacy(second, alsu, "Исәнме!", "Сәлам", "Ничек?", "Әйбәт", "Нишлисең?", "Укыйм")
	v, err := f.svc.Conversation(f.ctx, alsu, second.ID)
	if err != nil || v.Status != ConversationClosed || v.TurnsTaken != 3 {
		t.Fatalf("исчерпанный прежний разговор закрыт: %+v %v", v, err)
	}
	if r := f.attemptRow(t, second.ID); !r.closed {
		t.Errorf("закрытие исчерпанного разговора не записано: %+v", r)
	}
	start, err = f.svc.StartConversation(f.ctx, alsu, second.ID)
	if err != nil || start.Status != ConversationClosed || start.Reply != "Нишлисең?" {
		t.Fatalf("start исчерпанного разговора: %+v %v", start, err)
	}
	_, err = f.answerConv(alsu, second, "late", "Тагын")
	wantErr(t, err, http.StatusConflict, msgConversationNotStarted)
	f.waitReports(t)
	if r := f.attemptRow(t, second.ID); r.status != ReportReady {
		t.Fatalf("исчерпанный прежний разговор не разобран: %+v", r)
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	var found bool
	for _, an := range engine.analyzes {
		if an.State == nil && len(an.Transcript) == 6 {
			found = true
		}
	}
	if !found {
		t.Errorf("разбор прежнего разговора — без состояния и со всей расшифровкой: %+v", engine.analyzes)
	}
	if starts := len(engine.starts); starts != 1 {
		t.Errorf("исчерпанный разговор начат движком: %d", starts)
	}
}

// Ворота те же, что у старого диалога: задание выдано и открыто. Смотреть
// разговор и итог можно и после закрытия урока.
func TestConversationGates(t *testing.T) {
	f := newFixture(t)
	engine := newFakeEngine()
	f.svc.material = goalMaterial{brief: AvatarBrief{Enabled: true, MaxTurns: 3, Language: "tatar", Level: "A2", Goals: testGoals()}}
	f.svc.conversation = engine
	t.Cleanup(func() { _ = f.svc.WaitReports(context.Background()) })
	a := f.assign(t)
	f.issue(t, a, IssueClass, "", TaskText)
	aidar := f.students[0]
	attempt := f.start(t, a, aidar)

	_, err := f.svc.StartConversation(f.ctx, aidar, attempt.ID)
	wantErr(t, err, http.StatusConflict, msgNotIssued)

	class := f.issue(t, a, IssueClass, "", TaskDialog)
	if _, err := f.svc.StartConversation(f.ctx, aidar, attempt.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.FinishIssue(f.ctx, f.teacher, class.Issue.ID); err != nil {
		t.Fatal(err)
	}
	_, err = f.answerConv(aidar, attempt, "a", "Сәлам")
	wantErr(t, err, http.StatusConflict, msgClassFinished)
	if v, err := f.svc.Conversation(f.ctx, aidar, attempt.ID); err != nil || v.Status != ConversationActive {
		t.Errorf("разговор виден после закрытия урока: %+v %v", v, err)
	}
	if s, err := f.svc.ConversationSummary(f.ctx, aidar, attempt.ID); err != nil || s.Status != SummaryNotClosed {
		t.Errorf("итог незакрытого разговора: %+v %v", s, err)
	}

	// Учитель убрал разговор из урока.
	off := newFixture(t)
	off.svc.material = goalMaterial{brief: AvatarBrief{Enabled: false, MaxTurns: 3}}
	off.svc.conversation = engine
	oa := off.assign(t)
	off.issue(t, oa, IssueClass, "", TaskDialog)
	oat := off.start(t, oa, off.students[0])
	_, err = off.svc.StartConversation(off.ctx, off.students[0], oat.ID)
	wantErr(t, err, http.StatusConflict, msgNoDialog)
	if v, err := off.svc.Conversation(off.ctx, off.students[0], oat.ID); err != nil || v.Enabled {
		t.Errorf("выключенный разговор: %+v %v", v, err)
	}
}

// Ответ аватару виден продуктовой аналитике: без события ученик, который на
// неделе только разговаривал, выпадал бы из «Активных классов и учеников».
// Ключ события — хеш client_msg_id: повтор того же ответа даёт тот же ключ,
// и витрина считает его один раз.
func TestConversationAnswerEmitsDialogueTurn(t *testing.T) {
	f, _, a := conversationFixture(t, 3)
	aidar := f.students[0]
	attempt := f.start(t, a, aidar)

	var mu sync.Mutex
	var turns []telemetry.Event
	ctx := telemetry.WithEvents(f.ctx, func(e telemetry.Event) {
		if e.Name == "dialogue_turn" {
			mu.Lock()
			turns = append(turns, e)
			mu.Unlock()
		}
	})
	answer := func(id, text string) error {
		_, err := f.svc.AnswerConversation(ctx, aidar, attempt.ID, ConversationInput{
			ClientMsgID: id, Text: text, Input: InputText,
		})
		return err
	}

	// Отклонённый ответ не записан — и в аналитику не попадает.
	if err := answer("x", "Сәлам"); err == nil {
		t.Fatal("ответ до начала разговора принят")
	}
	if len(turns) != 0 {
		t.Fatalf("событие на отклонённый ответ: %+v", turns)
	}

	if _, err := f.svc.StartConversation(f.ctx, aidar, attempt.ID); err != nil {
		t.Fatal(err)
	}
	if err := answer("x", "Минем исемем Алсу"); err != nil {
		t.Fatal(err)
	}
	if err := answer("x", "Минем исемем Алсу"); err != nil {
		t.Fatal(err)
	}
	if err := answer("y", "Мин Казанда яшим"); err != nil {
		t.Fatal(err)
	}

	keys := map[string]bool{}
	for _, e := range turns {
		if e.EntityID != attempt.ID || e.AssignmentID != a.ID {
			t.Errorf("событие не о той попытке: %+v", e)
		}
		if e.Key == "x" || e.Key == "y" {
			t.Errorf("client_msg_id ушёл в аналитику как есть: %q", e.Key)
		}
		keys[e.Key] = true
	}
	if len(keys) != 2 {
		t.Fatalf("ожидали два разных ответа в аналитике, получили ключи %v", keys)
	}
}
