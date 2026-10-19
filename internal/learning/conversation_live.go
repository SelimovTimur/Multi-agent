package learning

// Живой режим разговора по целям урока: одна сессия GPT Live вместо ходов
// по одному ответу (их экран ведёт вводом текстом). Договор — docs/product/avatar-goals/contract.md,
// раздел 10; что и почему сделано здесь — docs/product/avatar-goals/backend.md,
// раздел «Живой режим».
//
// Аватар отвечает сам, голосом сессии: звук идёт между браузером и
// провайдером мимо сервера. Сервер делает то же, что в режиме голосовых
// сообщений, — доступ, счёт ответов, идемпотентность, закрытие, разбор, —
// но ответом на ответ ученика служит не реплика аватара, а указание сессии
// на следующий ход (steer): браузер добавляет его в сессию. Реплики
// аватара браузер присылает из расшифровки сессии (…/avatar), сервер
// хранит их с переводом и сообщает движку (observe). Инструменты делегата
// сессии браузер исполняет через …/progress и …/check.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/NATIVE/NATIVE/internal/iam"
	"github.com/NATIVE/NATIVE/internal/platform/httpx"
	"github.com/NATIVE/NATIVE/internal/platform/idgen"
	"github.com/NATIVE/NATIVE/internal/platform/logx"
	"github.com/NATIVE/NATIVE/internal/platform/pg"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// EngineObserve — настоящая реплика аватара для движка.
type EngineObserve struct {
	State  json.RawMessage
	TurnNo int
	Reply  string
	// Translate — нужен перевод для «Подсказки»: вызов модели. Без перевода
	// движок только запоминает реплику — без модели.
	Translate bool
	// SteerTurn — номер хода указания, которое уже дошло до сессии к началу
	// реплики (вступление — 0). nil — поле в движок не уходит, и он считает
	// реплику идущей по последнему указанию.
	SteerTurn *int
}

// EngineObservation — состояние с запомненной репликой и её перевод.
type EngineObservation struct {
	State   json.RawMessage
	ReplyRU string
	// Leaked — незачтённые фокусные фразы, которые аватар произнёс сам.
	// Отмечает их движок в своём состоянии; Go пишет их только в журнал.
	Leaked []string
}

// ProgressGoal — цель и открыта ли она ещё (open, closed).
type ProgressGoal struct {
	Key    string
	Kind   string
	Title  string
	Note   string
	Status string
}

// Correction — верная форма, которую аватару стоит естественно употребить
// после ошибки ученика.
type Correction struct {
	Quote       string
	CorrectForm string
}

// EngineProgress — ход разговора по состоянию движка.
type EngineProgress struct {
	Goals []ProgressGoal
	// TargetGoal — ключ цели, к которой вести сейчас; пусто — все цели
	// закрыты.
	TargetGoal   string
	AvoidPhrases []string
	Corrections  []Correction
	AnswersLeft  int
	Finished     bool
}

// EngineCheck — задуманная реплика аватара для проверки по правилам.
type EngineCheck struct {
	State json.RawMessage
	Text  string
	// Final — разговор закрыт: следующая реплика — прощание без вопроса.
	Final bool
}

// EngineCheckResult — годится ли реплика и что в ней не так.
type EngineCheckResult struct {
	OK       bool
	Problems []string
}

// turnGuidance — метка указания сессии на ход. Та же, что
// live.TurnGuidancePrefix: правила сессии велят следовать последнему
// указанию с этой меткой.
const turnGuidance = "Turn guidance"

// Пределы живого режима.
const (
	// steerMaxRunes — указание сессии. Движок обещает до 1200 символов;
	// потолок с запасом держит запись в базе предсказуемой.
	steerMaxRunes = 2000
	// avatarLinesPerTurn — реплик аватара на номер хода. Сессия делит ответ
	// на части (до и после инструмента делегата), переподключение добавляет
	// приветствие; больше — это браузер шлёт лишнее, а каждая реплика —
	// вызов модели на перевод.
	avatarLinesPerTurn = 4
	// checkTextMaxRunes — задуманная реплика для проверки.
	checkTextMaxRunes = 2000
	// observeTries — сколько раз observe переигрывается поверх состояния,
	// которое за это время продвинул ход.
	observeTries = 3
	// observeBudget — запись реплики аватара с переводом. Как и ход,
	// отвязана от запроса: реплика уже записана, и перевод не должен
	// пропасть из-за оборванной связи.
	observeBudget = time.Minute
	// reobserveBudget — переигрывание реплик аватара после хода: observe без
	// модели отвечает за миллисекунды, а указание тем временем ждёт ученик.
	reobserveBudget = 3 * time.Second
)

// Отказы живого режима.
const (
	msgLiveOff          = "Голосовой разговор сейчас недоступен"
	msgLiveLanguage     = "Голосовой разговор на этом языке пока недоступен"
	msgLiveExhausted    = "Голосовой разговор в этом уроке исчерпан"
	msgAvatarAhead      = "Ответ ученика с этим номером ещё не записан"
	msgAvatarLinesLimit = "Реплик аватара в этом разговоре слишком много"
	msgNoTranslation    = "Не удалось перевести реплику аватара — открой подсказку ещё раз"
	msgProgressOff      = "Не удалось узнать ход разговора"
	msgCheckOff         = "Не удалось проверить реплику"
)

var (
	errAvatarAhead      = errors.New("learning: реплика аватара на незаписанный ответ")
	errAvatarLinesLimit = errors.New("learning: потолок реплик аватара")
	errSteerAhead       = errors.New("learning: указание на незаписанный ответ")
)

// avatarLinesCap — потолок реплик аватара на разговор: вступление и ответ
// на каждый ответ ученика, по avatarLinesPerTurn на каждое.
func avatarLinesCap(maxTurns int) int {
	if maxTurns < 1 {
		maxTurns = defaultAvatarTurns
	}
	return (maxTurns + 1) * avatarLinesPerTurn
}

// liveAvailable — разговор можно вести живой сессией: голос настроен для
// языка урока, разговор не закрыт и сессии не исчерпаны. Ворота задания
// здесь не проверяются, как и у остального GET: их проверит …/live.
func (s *Service) liveAvailable(brief AvatarBrief, conv conversation) bool {
	return s.live != nil && s.conversation != nil && brief.Enabled && s.live.Supports(brief.Language) &&
		!conv.closed() && conv.row.Sessions < maxLiveSessions(brief.MaxTurns)
}

// answersLeft — сколько ответов ученику осталось, по счёту Go.
func (c conversation) answersLeft() int { return max(0, c.max-c.row.Turns) }

// liveStepOf — указание сессии, записанное при ответе t.
func (c conversation) liveStepOf(t convTurn) ConversationStep {
	return ConversationStep{
		Steer: t.Steer, TurnNo: t.No, TurnsTaken: c.row.Turns, MaxTurns: c.max,
		Status: c.status(), ReportStatus: c.reportStatus(),
	}
}

// latestLiveStep — указание последнего ответа, у которого оно есть.
func (c conversation) latestLiveStep() ConversationStep {
	for i := len(c.turns) - 1; i >= 0; i-- {
		if t := c.turns[i]; t.Role == RoleStudent && t.Steer != "" {
			return c.liveStepOf(t)
		}
	}
	return ConversationStep{TurnNo: c.lastStudentNo(), TurnsTaken: c.row.Turns, MaxTurns: c.max,
		Status: c.status(), ReportStatus: c.reportStatus()}
}

// labelSteer — указание с меткой «Turn guidance». Правила сессии велят
// следовать последнему указанию с этой меткой, а договор говорит только о
// содержании указания — метку гарантирует Go.
func labelSteer(steer string) string {
	steer = strings.TrimSpace(steer)
	if steer == "" {
		return ""
	}
	if !strings.HasPrefix(strings.ToLower(steer), strings.ToLower(turnGuidance)) {
		steer = turnGuidance + ": " + steer
	}
	return clip(steer, steerMaxRunes)
}

// ---------------------------------------------------------------------------
// Ученик: открыть живую сессию
// ---------------------------------------------------------------------------

// LiveConversation — открытая живая сессия и указание на её первый ход.
type LiveConversation struct {
	Answer     LiveAnswer
	Steer      string
	TurnsTaken int
	MaxTurns   int
	Status     string
}

// OpenLiveConversation открывает живую сессию разговора по целям.
//
// Ворота — как у разговора: задание «Диалог» выдано и открыто, разговор есть
// в уроке. Сессий на попытку — не больше, чем у голосового диалога прежнего
// режима (счётчик общий). Указание на первый ход: состояния движка нет —
// начало в режиме steer, вступление произносит сессия; состояние есть —
// указание из хода разговора (progressSteer).
func (s *Service) OpenLiveConversation(ctx context.Context, actor iam.Actor, attemptID uuid.UUID,
	offerSDP string) (LiveConversation, error) {
	if s.live == nil || s.conversation == nil || s.material == nil {
		return LiveConversation{}, httpx.Unavailable(msgLiveOff)
	}
	// Полная проверка SDP — в internal/live. Пустое предложение отсекается
	// здесь: до него сессия засчитывается и движок начинает разговор.
	if strings.TrimSpace(offerSDP) == "" {
		return LiveConversation{}, httpx.FieldError("sdp", "Не удалось подготовить аудиосоединение")
	}
	attempt, err := s.ownAttempt(ctx, actor, attemptID)
	if err != nil {
		return LiveConversation{}, err
	}
	issueID, _, err := s.conversationGate(ctx, actor, attempt)
	if err != nil {
		return LiveConversation{}, err
	}
	brief, err := s.enabledBrief(ctx, actor, attempt)
	if err != nil {
		return LiveConversation{}, err
	}
	if !s.live.Supports(brief.Language) {
		return LiveConversation{}, httpx.Unavailable(msgLiveLanguage)
	}
	if err := s.markOpened(ctx, actor, attempt.ID, TaskDialog, issueID); err != nil {
		return LiveConversation{}, err
	}
	conv, err := s.readConversation(ctx, actor.OrgID, attempt.ID, brief.MaxTurns)
	if err != nil {
		return LiveConversation{}, err
	}
	if conv.needsClosing() {
		if conv, err = s.settle(ctx, actor, attempt, brief); err != nil {
			return LiveConversation{}, err
		}
	}
	if conv.closed() {
		return LiveConversation{}, httpx.Conflict(msgConversationFinished)
	}
	// Сессия платная, а сколько в ней сказано, сервер знает только со слов
	// браузера. Предел и счётчик — те же, что у голосового диалога прежнего
	// режима (OpenLiveDialog): цикл «открой ещё одну» упрётся в него.
	limit := maxLiveSessions(brief.MaxTurns)
	if conv.row.Sessions >= limit {
		return LiveConversation{}, httpx.Conflict(msgLiveExhausted)
	}

	// Указание — до сессии: сессия платная, и открывать её, когда движок
	// лежит, незачем. Отвязано от запроса, как начало разговора: состояние,
	// положенное движком, не должно пропасть из-за оборванной связи.
	ectx, cancel := context.WithTimeout(context.WithoutCancel(ctx), turnBudget)
	defer cancel()
	steer, err := s.liveSteer(ectx, actor, attempt, brief, conv)
	if err != nil {
		s.avatarIncident(ectx, actor.OrgID, attempt.ID, conv.answeredNo(), err)
		return LiveConversation{}, avatarUnavailable(msgAvatarNoStart, err)
	}

	sessions, err := s.store.AddLiveSession(ctx, s.pool, actor.OrgID, attempt.ID)
	if err != nil {
		return LiveConversation{}, httpx.Internal("Не удалось открыть разговор").Wrap(err)
	}
	if sessions > limit {
		return LiveConversation{}, httpx.Conflict(msgLiveExhausted)
	}
	if conv, err = s.readConversation(ctx, actor.OrgID, attempt.ID, brief.MaxTurns); err != nil {
		return LiveConversation{}, err
	}
	// Сессия открывается и посреди разговора: уже сказанное уходит в её
	// инструкцию, чтобы она продолжила, а не поздоровалась заново.
	brief.History = conv.dialogTurns()
	if s.guide != nil {
		brief.LevelGuidance = s.guide.LevelGuidance(ctx, brief.Language, brief.Level)
	}
	answer, err := s.live.OpenConversation(ctx, actor.OrgID, actor.StudentID, offerSDP, brief)
	if err != nil {
		s.returnLiveSession(ctx, actor.OrgID, attempt.ID)
		return LiveConversation{}, err
	}
	return LiveConversation{Answer: answer, Steer: steer, TurnsTaken: conv.row.Turns, MaxTurns: conv.max,
		Status: conv.status()}, nil
}

// liveSteer — указание на первый ход новой сессии.
func (s *Service) liveSteer(ctx context.Context, actor iam.Actor, attempt Attempt, brief AvatarBrief,
	conv conversation) (string, error) {
	if !conv.row.HasEngine {
		// Разговор прежних режимов продолжается и здесь: движок получает его
		// реплики и не здоровается заново.
		reply, err := s.conversation.Start(ctx, actor.OrgID, actor.StudentID, EngineStart{
			Language: brief.Language, Level: levelCode(brief.Level), Text: brief.Text,
			Collocations: brief.Collocations, RolePrompt: brief.RolePrompt, Goals: brief.Goals,
			MaxTurns: brief.MaxTurns, History: lines(conv.turns), ReplyMode: ReplySteer,
		})
		if err == nil {
			err = checkReply(reply, ReplySteer)
		}
		if err != nil {
			return "", err
		}
		started, err := s.store.StartEngine(ctx, s.pool, actor.OrgID, attempt.ID, reply.State)
		if err != nil {
			return "", err
		}
		if started {
			return labelSteer(reply.Steer), nil
		}
		// Соседняя вкладка начала разговор раньше: указание — по её
		// состоянию, второго начала не будет.
		if conv, err = s.loadConversation(ctx, s.pool, actor.OrgID, attempt.ID, brief.MaxTurns); err != nil {
			return "", err
		}
	}
	p, err := s.conversation.Progress(ctx, actor.OrgID, actor.StudentID, conv.row.Engine)
	if err != nil {
		return "", err
	}
	return progressSteer(p, len(conv.turns) == 0, conv.answersLeft(), p.Finished || conv.closed()), nil
}

// progressSteer — указание сессии на текущий ход по ходу разговора из
// движка (/progress).
//
// Нужно, когда сессия открывается посреди разговора: после обрыва связи,
// перезагрузки страницы, из второй вкладки. Указание последнего ответа
// прежняя сессия могла уже исполнить, а у начала, которое сессия так и не
// произнесла, указания нет вовсе. Ход разговора движок отдаёт без модели,
// по текущему состоянию; здесь он складывается в текст того же вида, что
// указания движка: по-английски, с меткой «Turn guidance», не длиннее 1200
// символов (договор, 10.1). Цель и остаток ответов — обязательная часть;
// запретные фразы и верные формы входят, сколько поместится.
func progressSteer(p EngineProgress, opening bool, answersLeft int, finished bool) string {
	if finished {
		return turnGuidance + ": the conversation is over. Briefly thank the learner in Tatar and say " +
			"goodbye, without a question."
	}
	head := turnGuidance + ": continue the conversation from where it stopped; do not greet again. "
	if opening {
		head = turnGuidance + ": open the conversation: greet the learner briefly in Tatar, without " +
			"introducing yourself by name, and ask one question. "
	}
	if g, ok := progressTarget(p); ok {
		kind := "a phrase the learner should say"
		if g.Kind == GoalGrammar {
			kind = "grammar the learner should use"
		}
		head += fmt.Sprintf("Lead the learner to the goal %s (%s", steerQuoted(g.Title, steerTitleRunes), kind)
		if note := steerQuoted(g.Note, steerNoteRunes); note != "" {
			head += "; note: " + note
		}
		head += "). "
	} else {
		head += "All lesson goals are reached: talk with the learner about the lesson topic. "
	}
	tail := fmt.Sprintf("Learner answers left: %d.", answersLeft)

	room := progressSteerMaxRunes - len([]rune(head)) - len([]rune(tail))
	phrases := make([]string, 0, len(p.AvoidPhrases))
	for _, ph := range p.AvoidPhrases {
		phrases = append(phrases, steerQuoted(ph, steerPhraseRunes))
	}
	forms := make([]string, 0, len(p.Corrections))
	for _, c := range p.Corrections {
		forms = append(forms, steerQuoted(c.CorrectForm, steerFormRunes))
	}
	avoid := steerSentence("Do not say these phrases yourself, the learner has to say them: ", ".", phrases, &room)
	correct := steerSentence("Naturally use the correct forms ", " without mentioning any mistake.", forms, &room)
	return head + avoid + correct + tail
}

// Длины в указании из хода разговора — те же, что у указаний движка.
const (
	progressSteerMaxRunes = 1200
	steerTitleRunes       = 100
	steerNoteRunes        = 140
	steerPhraseRunes      = 80
	steerFormRunes        = 60
)

// steerSentence — предложение со списком: элементы входят по порядку, пока
// помещаются в room; не вошёл ни один — предложения нет.
func steerSentence(prefix, suffix string, items []string, room *int) string {
	used := len([]rune(prefix)) + len([]rune(suffix)) + 1
	var picked []string
	for _, it := range items {
		if it == "" {
			continue
		}
		n := len([]rune(it))
		if len(picked) > 0 {
			n += 2
		}
		if used+n > *room {
			break
		}
		picked = append(picked, it)
		used += n
	}
	if len(picked) == 0 {
		return ""
	}
	*room -= used
	return prefix + strings.Join(picked, ", ") + suffix + " "
}

// progressTarget — цель, к которой вести сейчас.
func progressTarget(p EngineProgress) (ProgressGoal, bool) {
	if p.TargetGoal == "" {
		return ProgressGoal{}, false
	}
	for _, g := range p.Goals {
		if g.Key == p.TargetGoal && strings.TrimSpace(g.Title) != "" {
			return g, true
		}
	}
	return ProgressGoal{}, false
}

var (
	steerControl  = regexp.MustCompile(`[\x00-\x1f\x7f]`)
	steerGuidance = regexp.MustCompile(`(?i)turn\s+guidance`)
	// steerQuotes — кавычки и ограждения внутри чужого текста.
	steerQuotes = strings.NewReplacer("«", `"`, "»", `"`, "<<<", " ", ">>>", " ")
)

// steerQuoted — чужой текст в указании: данные в «», которые нельзя
// закрыть. Внутри не остаётся «», переводов строк, ограждений <<< >>>
// политики сессии и слов «Turn guidance»: учитель в пояснении не допишет
// своё указание — сессия выполняет последнее «Turn guidance», и такая
// строка внутри данных выглядела бы новым указанием. Так же чистит данные
// в своих указаниях движок. Пустой текст — пустая строка.
func steerQuoted(text string, limit int) string {
	text = steerControl.ReplaceAllString(text, " ")
	text = steerQuotes.Replace(text)
	text = steerGuidance.ReplaceAllString(text, "turn note")
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return ""
	}
	return "«" + cutWords(text, limit) + "»"
}

// cutWords — не длиннее limit символов, по границе слова: обрывок слова
// сбивает модель сильнее, чем отсутствие хвоста.
func cutWords(text string, limit int) string {
	r := []rune(text)
	if len(r) <= limit {
		return text
	}
	cut := string(r[:limit-1])
	if i := strings.LastIndex(cut, " "); i > len(cut)/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,;:—-") + "…"
}

// ---------------------------------------------------------------------------
// Ученик: указание сессии на ответ
// ---------------------------------------------------------------------------

// saveSteer — шаг 4 для живого режима: указание сессии записывается при
// ответе, и вместе с ним — новое состояние движка.
//
// Правило «состояние только вперёд» здесь уточнено. В живом режиме
// состояние двигают не только ходы, но и реплики аватара (observe): сессия
// говорит, пока движок оценивает ответ, и observe её реплики нередко
// успевает раньше хода. Отказ писать состояние после такого observe
// останавливал бы разговор: указание ушло бы ученику, а движок о ходе не
// узнал бы. Поэтому ход пишет состояние и тогда, когда ответ последний и
// указания на него ещё нет: двигать состояние за время хода могли только
// observe и запоздавшие ходы прежних ответов. Наблюдения, которые ход
// затёр, переигрываются поверх нового состояния (reobserve). Ход ответа,
// который разговор обогнал, состояние, как и раньше, не трогает: его
// отметки движок оценит при разборе.
func (s *Service) saveSteer(ctx context.Context, actor iam.Actor, attempt Attempt, brief AvatarBrief,
	answer convTurn, from, next json.RawMessage, steer string) (ConversationStep, error) {
	steer = labelSteer(steer)
	overwrote := false
	err := pg.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := s.store.LockAttempt(ctx, tx, actor.OrgID, attempt.ID); err != nil {
			return err
		}
		c, err := s.loadConversation(ctx, tx, actor.OrgID, attempt.ID, brief.MaxTurns)
		if err != nil {
			return err
		}
		a, ok := c.turnByID(answer.ID)
		if !ok || a.Steer != "" {
			// Повтор из соседней вкладки успел первым: его указание уже у
			// ученика, второе было бы другим — остаётся первое.
			return nil
		}
		if _, err := s.store.SetAnswerSteer(ctx, tx, actor.OrgID, attempt.ID, a.ID, a.OccurredAt, steer); err != nil {
			return err
		}
		switch {
		case bytes.Equal(c.row.Engine, from):
		case c.isLastAnswer(a):
			overwrote = true
		default:
			return nil
		}
		return s.store.SetEngine(ctx, tx, actor.OrgID, attempt.ID, next)
	})
	if err != nil {
		return ConversationStep{}, httpx.Internal("Не удалось сохранить ответ аватара").Wrap(err)
	}
	if overwrote {
		s.reobserve(ctx, actor, attempt, brief, answer.No)
	}
	conv, err := s.readConversation(ctx, actor.OrgID, attempt.ID, brief.MaxTurns)
	if err != nil {
		return ConversationStep{}, err
	}
	if a, ok := conv.turnByID(answer.ID); ok && a.Steer != "" {
		return conv.liveStepOf(a), nil
	}
	return conv.latestLiveStep(), nil
}

// reobserve переигрывает observe реплик аватара, которые ход мог затереть:
// ответы аватара на этот ответ и на предыдущий, по порядку. Без перевода —
// без модели. Ошибки — только в журнал: это уточнение состояния, ответ
// ученику от него не зависит. Поэтому и время на него ограничено, а первый
// же сбой его прекращает: указание ждёт ученик, и лежащий движок не должен
// задерживать его на таймаут каждой реплики.
func (s *Service) reobserve(ctx context.Context, actor iam.Actor, attempt Attempt, brief AvatarBrief, answerNo int) {
	ctx, cancel := context.WithTimeout(ctx, reobserveBudget)
	defer cancel()
	conv, err := s.loadConversation(ctx, s.pool, actor.OrgID, attempt.ID, brief.MaxTurns)
	if err != nil {
		logx.From(ctx).Warn("не удалось перечитать разговор для observe", "attempt_id", attempt.ID, "err", err)
		return
	}
	for _, t := range conv.turns {
		if t.Role != RoleAvatar || t.Legacy || t.No < answerNo-1 || t.No > answerNo {
			continue
		}
		if _, err := s.observe(ctx, actor, attempt.ID, t, false); err != nil {
			logx.From(ctx).Warn("реплика аватара не переиграна в состоянии движка",
				"attempt_id", attempt.ID, "turn_no", t.No, "err", err)
			return
		}
	}
}

// ---------------------------------------------------------------------------
// Ученик: реплика аватара из расшифровки сессии
// ---------------------------------------------------------------------------

// AvatarLineInput — реплика аватара из расшифровки живой сессии.
type AvatarLineInput struct {
	ClientMsgID string
	// TurnNo — номер ответа ученика, на который реплика отвечает; 0 —
	// вступление. nil — номера не прислали.
	TurnNo  *int
	Content string
	// SteerTurn — необязательно: номер хода последнего указания, которое
	// браузер отправил в сессию до начала этой реплики (вступление — 0).
	// Сессия отвечает сразу, а указание приходит через несколько секунд, и
	// движку нужно знать, к какой цели реплика вела на самом деле.
	SteerTurn *int
}

func (in AvatarLineInput) normalize() (AvatarLineInput, error) {
	in.ClientMsgID = strings.TrimSpace(in.ClientMsgID)
	in.Content = strings.TrimSpace(in.Content)
	fields := map[string]string{}
	switch {
	case in.ClientMsgID == "":
		fields["client_msg_id"] = "Нужен идентификатор реплики"
	case len([]rune(in.ClientMsgID)) > clientMsgIDMaxRunes:
		fields["client_msg_id"] = "Идентификатор реплики — не длиннее 64 символов"
	}
	switch {
	case in.TurnNo == nil:
		fields["turn_no"] = "Нужен номер ответа, на который отвечает реплика"
	case *in.TurnNo < 0:
		fields["turn_no"] = "Номер ответа — не меньше 0"
	}
	if in.SteerTurn != nil && *in.SteerTurn < 0 {
		fields["steer_turn"] = "Номер указания — не меньше 0"
	}
	switch n := len([]rune(in.Content)); {
	case n == 0:
		fields["content"] = "Пустая реплика"
	case n > replyMaxRunes:
		fields["content"] = "Слишком длинная реплика — не больше 2000 символов"
	}
	if len(fields) > 0 {
		return in, httpx.Validation("Проверьте реплику", fields)
	}
	return in, nil
}

// RecordAvatarLine записывает реплику аватара из расшифровки живой сессии и
// отдаёт её перевод для «Подсказки».
//
//  1. Реплика с известным client_msg_id второй раз не пишется: есть перевод
//     — отдаётся он, без модели; нет (перевод не удался) — запрашивается
//     снова.
//  2. Номер — ответ ученика, на который реплика отвечает (вступление — 0),
//     не больше засчитанных: реплику на ещё не записанный ответ браузер
//     придерживает. Несколько реплик на номер — можно: сессия делит ответ
//     на части. Номер указания (steer_turn) — тоже не больше засчитанных:
//     указание выдаётся на записанный ответ, и другого у сессии быть не
//     могло. Он хранится с репликой и уходит в каждый её observe.
//  3. Реплика пишется до вызова модели: расшифровка разговора полна, даже
//     если перевод не удался (503 avatar_unavailable, повтор с тем же
//     client_msg_id).
//  4. Ворот задания нет, как у сбоев: реплики отвечают на уже принятые
//     ответы и разговор не удлиняют, а прощание после N-го ответа пишется и
//     в закрытый разговор. Расход держит потолок реплик на разговор.
//  5. Реплика закрытого разговора — прощание: после неё запускается разбор,
//     и прощание попадает в снимок отчёта.
func (s *Service) RecordAvatarLine(ctx context.Context, actor iam.Actor, attemptID uuid.UUID,
	in AvatarLineInput) (string, error) {
	if s.conversation == nil || s.material == nil {
		return "", httpx.Unavailable(msgConversationOff)
	}
	in, err := in.normalize()
	if err != nil {
		return "", err
	}
	attempt, err := s.ownAttempt(ctx, actor, attemptID)
	if err != nil {
		return "", err
	}
	brief, err := s.enabledBrief(ctx, actor, attempt)
	if err != nil {
		return "", err
	}
	if strings.EqualFold(brief.Language, tatarLanguage) {
		// Расшифровка голосовой модели бывает с казахскими буквами и
		// латиницей; в подсказку и отчёт учителя она идёт кириллицей.
		in.Content = toTatarCyrillic(in.Content)
	}
	conv, err := s.readConversation(ctx, actor.OrgID, attempt.ID, brief.MaxTurns)
	if err != nil {
		return "", err
	}
	if line, ok := conv.avatarLineOf(in.ClientMsgID); ok && line.Translation != "" {
		return line.Translation, nil
	}

	line, err := s.saveAvatarLine(ctx, actor, attempt.ID, brief, in)
	switch {
	case errors.Is(err, errConversationNotStarted):
		return "", httpx.Conflict(msgConversationNotStarted)
	case errors.Is(err, errAvatarAhead):
		return "", httpx.Conflict(msgAvatarAhead)
	case errors.Is(err, errAvatarLinesLimit):
		return "", httpx.Conflict(msgAvatarLinesLimit)
	case errors.Is(err, errSteerAhead):
		return "", httpx.FieldError("steer_turn", "Указания на ответ с таким номером не было")
	case err != nil:
		return "", httpx.Internal("Не удалось сохранить реплику аватара").Wrap(err)
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), observeBudget)
	defer cancel()
	translation, observeErr := s.observe(ctx, actor, attempt.ID, line, true)
	if observeErr == nil {
		if err := s.store.SetTurnTranslation(ctx, s.pool, actor.OrgID, attempt.ID, line.ID, line.OccurredAt,
			translation); err != nil {
			// Перевод уже получен — ученик его получит; запись повторит
			// следующий повтор с тем же client_msg_id.
			logx.From(ctx).Error("не удалось записать перевод реплики аватара", "attempt_id", attempt.ID, "err", err)
		}
	}
	// Прощание закрытого разговора записано (с переводом или без) — разбор
	// можно начинать: в снимок отчёта оно уже попадёт.
	if c, err := s.readConversation(ctx, actor.OrgID, attempt.ID, brief.MaxTurns); err == nil && c.closed() {
		s.kickReport(ctx, actor.OrgID, attempt.ID, attempt.StudentID, brief)
	}
	if observeErr != nil {
		logx.From(ctx).Warn("реплика аватара не переведена", "attempt_id", attempt.ID, "turn_no", line.No,
			"err", observeErr)
		return "", avatarUnavailable(msgNoTranslation, observeErr)
	}
	return translation, nil
}

// saveAvatarLine записывает реплику под блокировкой попытки: повтор с тем
// же client_msg_id из двух вкладок пишет её один раз, а номер сверяется с
// засчитанными ответами на той же заблокированной строке.
func (s *Service) saveAvatarLine(ctx context.Context, actor iam.Actor, attemptID uuid.UUID, brief AvatarBrief,
	in AvatarLineInput) (convTurn, error) {
	var id uuid.UUID
	err := pg.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := s.store.LockAttempt(ctx, tx, actor.OrgID, attemptID); err != nil {
			return err
		}
		c, err := s.loadConversation(ctx, tx, actor.OrgID, attemptID, brief.MaxTurns)
		if err != nil {
			return err
		}
		if t, ok := c.avatarLineOf(in.ClientMsgID); ok {
			id = t.ID
			return nil
		}
		if !c.row.HasEngine {
			return errConversationNotStarted
		}
		if *in.TurnNo > c.answeredNo() {
			return errAvatarAhead
		}
		if in.SteerTurn != nil && *in.SteerTurn > c.answeredNo() {
			return errSteerAhead
		}
		if c.avatarLines() >= avatarLinesCap(brief.MaxTurns) {
			return errAvatarLinesLimit
		}
		no := *in.TurnNo
		t := DialogTurn{
			ID: idgen.New(), OrgID: actor.OrgID, AttemptID: attemptID, StudentID: actor.StudentID,
			Role: RoleAvatar, Content: in.Content, ClientMsgID: in.ClientMsgID, TurnNo: &no, Input: InputText,
			SteerTurn: in.SteerTurn,
		}
		id = t.ID
		return s.store.InsertConversationTurn(ctx, tx, t)
	})
	if err != nil {
		return convTurn{}, err
	}
	conv, err := s.loadConversation(ctx, s.pool, actor.OrgID, attemptID, brief.MaxTurns)
	if err != nil {
		return convTurn{}, err
	}
	line, ok := conv.turnByID(id)
	if !ok {
		return convTurn{}, errors.New("learning: записанная реплика аватара не нашлась")
	}
	return line, nil
}

// observe — реплика аватара в состоянии движка: observe от текущего
// состояния и запись нового, если состояние за это время не сдвинулось.
// Сдвинулось (ход или соседняя реплика) — observe ещё раз поверх нового,
// без перевода, не больше observeTries раз.
//
// Ошибка — только у первого вызова: без него нет ни перевода, ни записи.
// Дальше сбой лишь оставляет состояние без этой реплики, и ученику его не
// показывают: перевод уже получен.
func (s *Service) observe(ctx context.Context, actor iam.Actor, attemptID uuid.UUID, line convTurn,
	translate bool) (string, error) {
	var translation string
	for try := 0; try < observeTries; try++ {
		row, err := s.store.ConversationOf(ctx, s.pool, actor.OrgID, attemptID)
		var obs EngineObservation
		if err == nil {
			obs, err = s.conversation.Observe(ctx, actor.OrgID, actor.StudentID, EngineObserve{
				State: row.Engine, TurnNo: line.No, Reply: line.Content, Translate: translate && try == 0,
				SteerTurn: line.SteerTurn,
			})
		}
		if err != nil {
			if try == 0 {
				return "", err
			}
			logx.From(ctx).Warn("реплика аватара не записана в состояние движка",
				"attempt_id", attemptID, "turn_no", line.No, "err", err)
			return translation, nil
		}
		if translate && try == 0 {
			translation = clip(strings.TrimSpace(obs.ReplyRU), replyMaxRunes)
		}
		if len(obs.Leaked) > 0 {
			logx.From(ctx).Info("аватар произнёс незачтённую фокусную фразу",
				"attempt_id", attemptID, "turn_no", line.No, "goals", obs.Leaked)
		}
		if !engineState(obs.State) {
			return translation, nil
		}
		wrote, err := s.replaceEngine(ctx, actor.OrgID, attemptID, row.Engine, obs.State)
		if err != nil {
			logx.From(ctx).Error("не удалось записать состояние движка после реплики аватара",
				"attempt_id", attemptID, "turn_no", line.No, "err", err)
			return translation, nil
		}
		if wrote {
			return translation, nil
		}
	}
	logx.From(ctx).Warn("состояние движка всё время сдвигалось: реплика аватара в него не записана",
		"attempt_id", attemptID, "turn_no", line.No)
	return translation, nil
}

// replaceEngine кладёт новое состояние движка, только если в базе всё ещё
// то, от которого оно посчитано: сравнение под блокировкой попытки, как у
// хода. false — состояние за это время сдвинулось.
func (s *Service) replaceEngine(ctx context.Context, orgID, attemptID uuid.UUID, from, next json.RawMessage) (bool, error) {
	wrote := false
	err := pg.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := s.store.LockAttempt(ctx, tx, orgID, attemptID); err != nil {
			return err
		}
		row, err := s.store.ConversationOf(ctx, tx, orgID, attemptID)
		if err != nil {
			return err
		}
		if !bytes.Equal(row.Engine, from) {
			return nil
		}
		if err := s.store.SetEngine(ctx, tx, orgID, attemptID, next); err != nil {
			return err
		}
		wrote = true
		return nil
	})
	return wrote, err
}

// ---------------------------------------------------------------------------
// Ученик: инструменты делегата и конец сессии
// ---------------------------------------------------------------------------

// startedConversation — разговор попытки ученика, в котором движок уже
// запущен: без состояния инструментам делегата отвечать нечем. Ворот
// задания нет: инструменты отвечают про уже идущий разговор, без модели, и
// новых реплик не добавляют.
func (s *Service) startedConversation(ctx context.Context, actor iam.Actor, attemptID uuid.UUID) (conversation, error) {
	if s.conversation == nil || s.material == nil {
		return conversation{}, httpx.Unavailable(msgConversationOff)
	}
	attempt, err := s.ownAttempt(ctx, actor, attemptID)
	if err != nil {
		return conversation{}, err
	}
	brief, err := s.enabledBrief(ctx, actor, attempt)
	if err != nil {
		return conversation{}, err
	}
	conv, err := s.readConversation(ctx, actor.OrgID, attempt.ID, brief.MaxTurns)
	if err != nil {
		return conversation{}, err
	}
	if !conv.row.HasEngine {
		return conversation{}, httpx.Conflict(msgConversationNotStarted)
	}
	return conv, nil
}

// ConversationProgress — ход разговора для инструмента делегата
// lesson_progress: проход к /progress движка.
//
// Сколько ответов осталось и закончен ли разговор, решает Go — по своему
// счёту: состояние движка может отстать на ход (ход прежнего ответа его не
// пишет), а счёт ответов — нет.
func (s *Service) ConversationProgress(ctx context.Context, actor iam.Actor, attemptID uuid.UUID) (EngineProgress, error) {
	conv, err := s.startedConversation(ctx, actor, attemptID)
	if err != nil {
		return EngineProgress{}, err
	}
	p, err := s.conversation.Progress(ctx, actor.OrgID, actor.StudentID, conv.row.Engine)
	if err != nil {
		return EngineProgress{}, httpx.Unavailable(msgProgressOff).Wrap(err)
	}
	p.AnswersLeft = conv.answersLeft()
	p.Finished = p.Finished || conv.closed()
	return p, nil
}

// CheckConversationReply — проверка задуманной реплики аватара для
// инструмента делегата check_reply: проход к /check движка. Разговор
// закрыт — проверяется прощание (final).
func (s *Service) CheckConversationReply(ctx context.Context, actor iam.Actor, attemptID uuid.UUID,
	text string) (EngineCheckResult, error) {
	text = strings.TrimSpace(text)
	switch n := len([]rune(text)); {
	case n == 0:
		return EngineCheckResult{}, httpx.FieldError("text", "Пустая реплика")
	case n > checkTextMaxRunes:
		return EngineCheckResult{}, httpx.FieldError("text", "Слишком длинная реплика — не больше 2000 символов")
	}
	conv, err := s.startedConversation(ctx, actor, attemptID)
	if err != nil {
		return EngineCheckResult{}, err
	}
	r, err := s.conversation.Check(ctx, actor.OrgID, actor.StudentID, EngineCheck{
		State: conv.row.Engine, Text: text, Final: conv.closed(),
	})
	if err != nil {
		return EngineCheckResult{}, httpx.Unavailable(msgCheckOff).Wrap(err)
	}
	if r.Problems == nil {
		r.Problems = []string{}
	}
	return r, nil
}
