package app

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/NATIVE/NATIVE/internal/classroom"
	"github.com/NATIVE/NATIVE/internal/content"
	"github.com/NATIVE/NATIVE/internal/iam"
	"github.com/NATIVE/NATIVE/internal/learning"
	"github.com/NATIVE/NATIVE/internal/lessonpipe"
	"github.com/NATIVE/NATIVE/internal/live"
	"github.com/NATIVE/NATIVE/internal/speech"
	"github.com/google/uuid"
)

// Здесь и только здесь модули узнают друг о друге.
//
// learning объявляет, что ему нужно от классов и уроков, а эти адаптеры
// переводят его запросы в вызовы classroom и content. Сами модули друг о
// друге не знают: заменить classroom на вызов по сети можно правкой одного
// файла — этого.

// Соответствие портам проверяется здесь же, а не при сборке графа.
//
// Порт живёт в своём модуле и расширяется вместе с ним. Без этих строк
// добавленный метод роняет сборку в app.go — в файле, где собираются
// зависимости и где чинить нечего: сообщение указывает на строку вызова,
// а не на адаптер, которому не хватает метода. Ошибка должна падать там,
// где её исправляют.
var (
	_ learning.Classes        = classAdapter{}
	_ learning.Lessons        = lessonAdapter{}
	_ learning.LessonMaterial = materialAdapter{}
	_ learning.LiveVoice      = liveAdapter{}
	_ learning.Conversation   = conversationAdapter{}
	_ learning.LevelGuide     = (*levelGuide)(nil)
	_ content.Assignments     = assignmentsAdapter{}
	_ speech.VoiceStyle       = voiceStyleAdapter{}
)

// classAdapter — доступ к классам для модуля learning.
type classAdapter struct{ svc *classroom.Service }

func (a classAdapter) OwnedClass(ctx context.Context, actor iam.Actor, classID uuid.UUID) (learning.ClassInfo, error) {
	c, err := a.svc.Class(ctx, actor, classID)
	if err != nil {
		return learning.ClassInfo{}, err
	}
	return learning.ClassInfo{
		ID: c.ID, Title: c.Title, Subject: c.Subject,
		Level: c.DefaultLevel, Lang: c.LangCode,
	}, nil
}

func (a classAdapter) StudentClasses(ctx context.Context, actor iam.Actor) ([]learning.ClassInfo, error) {
	classes, err := a.svc.MyClasses(ctx, actor)
	if err != nil {
		return nil, err
	}
	out := make([]learning.ClassInfo, 0, len(classes))
	for _, c := range classes {
		out = append(out, learning.ClassInfo{
			ID: c.ID, Title: c.Title, Subject: c.Subject,
			Level: c.DefaultLevel, Lang: c.LangCode,
		})
	}
	return out, nil
}

// Roster — состав класса для аналитики.
//
// includeRemoved=false намеренно: доля прохождения считается от нынешнего
// состава. Работа ушедшего ученика остаётся в суммах класса — она была, — но
// отдельной строкой он не показывается, иначе аналитика разошлась бы с
// вкладкой «Ученики», где составом распоряжается classroom.
func (a classAdapter) Roster(ctx context.Context, actor iam.Actor, classID uuid.UUID) ([]learning.ClassMember, error) {
	members, err := a.svc.Roster(ctx, actor, classID, false)
	if err != nil {
		return nil, err
	}
	out := make([]learning.ClassMember, 0, len(members))
	for _, m := range members {
		out = append(out, learning.ClassMember{
			ID:          m.Enrollment.StudentID,
			DisplayName: m.DisplayName,
			PublicTag:   m.PublicTag,
		})
	}
	return out, nil
}

func (a classAdapter) ClassesInfo(ctx context.Context, orgID uuid.UUID,
	ids []uuid.UUID) (map[uuid.UUID]learning.ClassInfo, error) {
	classes, err := a.svc.ClassesByIDs(ctx, orgID, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]learning.ClassInfo, len(classes))
	for id, c := range classes {
		out[id] = learning.ClassInfo{
			ID: c.ID, Title: c.Title, Subject: c.Subject,
			Level: c.DefaultLevel, Lang: c.LangCode,
		}
	}
	return out, nil
}

// lessonAdapter — доступ к версиям уроков для модуля learning.
type lessonAdapter struct{ svc *content.Service }

func (a lessonAdapter) OwnedVersion(ctx context.Context, actor iam.Actor, versionID uuid.UUID) (learning.VersionInfo, error) {
	v, err := a.svc.VersionOwner(ctx, actor, versionID)
	if err != nil {
		return learning.VersionInfo{}, err
	}
	out := toVersionInfo(v)
	// Название урока — то, что увидят класс и ученик. Дочитываем его здесь:
	// learning в схему content не ходит.
	if titles, err := a.svc.LessonTitles(ctx, actor.OrgID, []uuid.UUID{v.LessonID}); err == nil {
		out.LessonTitle = titles[v.LessonID]
	}
	return out, nil
}

func (a lessonAdapter) VersionsInfo(ctx context.Context, orgID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]learning.VersionInfo, error) {
	versions, err := a.svc.VersionsByIDs(ctx, orgID, ids)
	if err != nil {
		return nil, err
	}
	lessonIDs := make([]uuid.UUID, 0, len(versions))
	for _, v := range versions {
		lessonIDs = append(lessonIDs, v.LessonID)
	}
	// Одним запросом на весь список: название урока нужно каждой строке, а
	// запрос на строку превратил бы список заданий в N+1.
	titles, err := a.svc.LessonTitles(ctx, orgID, lessonIDs)
	if err != nil {
		return nil, err
	}

	out := make(map[uuid.UUID]learning.VersionInfo, len(versions))
	for id, v := range versions {
		info := toVersionInfo(v)
		info.LessonTitle = titles[v.LessonID]
		out[id] = info
	}
	return out, nil
}

// StudentContent отдаёт содержимое урока ученику.
//
// Тип any в интерфейсе learning намеренно: модуль назначений не должен
// знать структуру урока — он только проверяет право и пробрасывает ответ.
// Знание о форме содержимого остаётся в content, где ему и место.
func (a lessonAdapter) StudentContent(ctx context.Context, orgID, versionID uuid.UUID) (any, error) {
	c, err := a.svc.StudentContent(ctx, orgID, versionID)
	if err != nil {
		return nil, err
	}
	return content.ToStudentDTO(c), nil
}

func toVersionInfo(v content.LessonVersion) learning.VersionInfo {
	return learning.VersionInfo{
		ID: v.ID, LessonID: v.LessonID, Version: v.Version,
		Title: v.Title, Level: v.Level, Status: v.Status,
	}
}

// CheckAnswer — проверка ответа ученика ключом упражнения.
//
// Ключ остаётся в content: сюда возвращается только вердикт, верный ответ
// и цена ответа.
func (a lessonAdapter) CheckAnswer(ctx context.Context, orgID, versionID, exerciseID uuid.UUID,
	itemID, value string) (learning.AnswerCheck, error) {

	res, err := a.svc.CheckAnswer(ctx, orgID, versionID, exerciseID, itemID, value)
	if err != nil {
		return learning.AnswerCheck{}, err
	}
	return learning.AnswerCheck{
		Correct: res.Correct, Expected: res.Expected, Score: res.Score, Kind: res.Kind,
	}, nil
}

// QuestionCount — сколько в уроке вопросов, на которые ученик отвечает.
func (a lessonAdapter) QuestionCount(ctx context.Context, orgID, versionID uuid.UUID) (int, error) {
	return a.svc.QuestionCount(ctx, orgID, versionID)
}

// LessonTasks — задания уроков для выдачи по заданиям («Урок в классе»).
//
// Список копируется: content отдаёт его из своего кеша, и правка на стороне
// learning не должна задеть соседний запрос.
func (a lessonAdapter) LessonTasks(ctx context.Context, orgID uuid.UUID,
	versionIDs []uuid.UUID) (map[uuid.UUID][]learning.LessonTask, error) {
	in, err := a.svc.LessonTasks(ctx, orgID, versionIDs)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID][]learning.LessonTask, len(in))
	for id, tasks := range in {
		out[id] = toLearningTasks(tasks)
	}
	return out, nil
}

func toLearningTasks(tasks []content.LessonTask) []learning.LessonTask {
	out := make([]learning.LessonTask, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, learning.LessonTask{
			Key: t.Key, N: t.N, Kind: t.Kind, Type: t.Type, TypeLabel: t.TypeLabel,
			Title: t.Title, Questions: t.Questions, ExerciseID: t.ExerciseID,
			ItemIDs: slices.Clone(t.ItemIDs),
		})
	}
	return out
}

// ExerciseReviews — вопросы упражнений с текстами и верными ответами.
//
// Только для экранов учителя: ключ ответов уходит из content уже словами и
// только сюда — ручки ученика этот метод не вызывают.
func (a lessonAdapter) ExerciseReviews(ctx context.Context, orgID,
	versionID uuid.UUID) (map[uuid.UUID]learning.ExerciseReview, error) {
	in, err := a.svc.ExerciseReviews(ctx, orgID, versionID)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]learning.ExerciseReview, len(in))
	for id, r := range in {
		qs := make([]learning.QuestionReview, 0, len(r.Questions))
		for _, q := range r.Questions {
			qs = append(qs, learning.QuestionReview{
				ItemID: q.ItemID, Prompt: q.Prompt, Expected: q.Expected, Choices: maps.Clone(q.Choices),
			})
		}
		out[id] = learning.ExerciseReview{Questions: qs}
	}
	return out, nil
}

// LessonWords — лексика урока для трудных слов класса.
func (a lessonAdapter) LessonWords(ctx context.Context, orgID, versionID uuid.UUID) ([]learning.LessonWord, error) {
	lexicon, err := a.svc.Lexicon(ctx, orgID, versionID)
	if err != nil {
		return nil, err
	}
	out := make([]learning.LessonWord, 0, len(lexicon))
	for _, u := range lexicon {
		out = append(out, learning.LessonWord{Phrase: u.Phrase, Translation: u.Translation})
	}
	return out, nil
}

// assignmentsAdapter — в какие классы выдан урок, для строки библиотеки.
//
// Обратное направление к lessonAdapter: там learning спрашивает про уроки,
// здесь content спрашивает про назначения. Ходить в learning.assignments и
// classroom.classes напрямую библиотека не имеет права — это чужие схемы,
// и запрет закреплён тестами в internal/arch.
type assignmentsAdapter struct{ svc *learning.Service }

func (a assignmentsAdapter) HasVersionAssignments(ctx context.Context, orgID, versionID uuid.UUID) (bool, error) {
	return a.svc.HasVersionAssignments(ctx, orgID, versionID)
}

func (a assignmentsAdapter) ClassesForLessons(ctx context.Context, orgID uuid.UUID,
	lessonIDs []uuid.UUID) (map[uuid.UUID][]content.ClassRef, error) {

	byLesson, err := a.svc.LessonClasses(ctx, orgID, lessonIDs)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID][]content.ClassRef, len(byLesson))
	for lessonID, classes := range byLesson {
		refs := make([]content.ClassRef, 0, len(classes))
		for _, c := range classes {
			refs = append(refs, content.ClassRef{ID: c.ID, Name: c.Name})
		}
		out[lessonID] = refs
	}
	return out, nil
}

// materialAdapter — материал урока и настройка аватара для разговора.
//
// Аватар говорит по тексту урока и его ключевым выражениям: без них он
// уводит разговор в сторону от того, что ученик только что прочитал.
// Роль, тон и длину разговора задаёт учитель (NR-29).
type materialAdapter struct{ svc *content.Service }

// TeacherAvatarBrief — настройка разговора для пробного разговора учителя.
func (m materialAdapter) TeacherAvatarBrief(ctx context.Context, actor iam.Actor,
	versionID uuid.UUID) (learning.AvatarBrief, error) {
	// Через TeacherVersion: он проверяет, что урок принадлежит учителю.
	if _, err := m.svc.TeacherVersion(ctx, actor, versionID); err != nil {
		return learning.AvatarBrief{}, err
	}
	return m.AvatarBriefFor(ctx, actor.OrgID, versionID)
}

func (m materialAdapter) AvatarBriefFor(ctx context.Context, orgID, versionID uuid.UUID) (learning.AvatarBrief, error) {
	c, err := m.svc.StudentContent(ctx, orgID, versionID)
	if err != nil {
		return learning.AvatarBrief{}, err
	}
	phrases := make([]string, 0, len(c.Lexicon))
	for _, u := range c.Lexicon {
		if u.Translation != "" {
			phrases = append(phrases, u.Phrase+" — "+u.Translation)
		} else {
			phrases = append(phrases, u.Phrase)
		}
	}
	language, err := m.svc.VersionLanguage(ctx, orgID, versionID)
	if err != nil {
		return learning.AvatarBrief{}, err
	}

	brief := learning.AvatarBrief{
		Text:              c.Version.RefinedText,
		Collocations:      strings.Join(phrases, "\n"),
		Language:          language,
		Level:             c.Version.Level,
		Enabled:           c.Avatar.Enabled,
		RolePrompt:        c.Avatar.RolePrompt,
		MaxTurns:          c.Avatar.MaxTurns,
		PracticePhrases:   c.Avatar.PracticePhrases,
		PracticeGrammar:   c.Avatar.PracticeGrammar,
		HintEnabled:       c.Avatar.HintEnabled,
		DictionaryEnabled: c.Avatar.DictionaryEnabled,
		Goals:             avatarGoals(c),
	}
	// Учитель не открывал экран настройки: отрабатываем всё, что есть в уроке.
	// Ровно это и показывает прототип, когда списки ещё не правили.
	//
	// С теми же пределами, что и у сохранённой настройки: длинный урок иначе
	// собрал бы список, который llm_service отклонит как слишком большой, и
	// разговор не начался бы вовсе.
	if !c.Avatar.Saved {
		for _, u := range c.Lexicon {
			if len(brief.PracticePhrases) >= content.AvatarMaxPhrases {
				break
			}
			brief.PracticePhrases = append(brief.PracticePhrases, u.Phrase)
		}
		for _, g := range c.Grammar {
			if len(brief.PracticeGrammar) >= content.AvatarMaxGrammar {
				break
			}
			brief.PracticeGrammar = append(brief.PracticeGrammar, g.Title)
		}
	}
	return brief, nil
}

// Цели урока, которого учитель не настраивал (договор avatar-goals, раздел
// 6): разговор из 8 ответов не успеет дать повод для каждой из десяти фраз,
// и цель, к которой аватар так и не подвёл, читалась бы в отчёте как провал
// ученика. Поэтому — первые шесть выражений и первые три темы.
const (
	defaultPhraseGoals  = 6
	defaultGrammarGoals = 3
)

// avatarGoals — цели разговора версии урока: фразы p1…, затем темы g1….
//
// Настройка сохранена — списки учителя с его пояснениями. Не сохранена —
// выражения словаря урока (пояснение — перевод) и грамматические темы
// (пояснение — объяснение темы, коротко).
func avatarGoals(c content.VersionContent) []learning.AvatarGoal {
	goals := []learning.AvatarGoal{}
	add := func(kind, prefix, title, note string, n int) int {
		title = strings.TrimSpace(title)
		if title == "" {
			return n
		}
		n++
		goals = append(goals, learning.AvatarGoal{
			Key: prefix + strconv.Itoa(n), Kind: kind, Title: title, Note: clipNote(note),
		})
		return n
	}
	phrases, themes := 0, 0
	if c.Avatar.Saved {
		for i, p := range c.Avatar.PracticePhrases {
			phrases = add(learning.GoalPhrase, "p", p, noteAt(c.Avatar.PracticePhraseNotes, i), phrases)
		}
		for i, g := range c.Avatar.PracticeGrammar {
			themes = add(learning.GoalGrammar, "g", g, noteAt(c.Avatar.PracticeGrammarNotes, i), themes)
		}
		return goals
	}
	for _, u := range c.Lexicon {
		if phrases >= defaultPhraseGoals {
			break
		}
		phrases = add(learning.GoalPhrase, "p", u.Phrase, u.Translation, phrases)
	}
	for _, g := range c.Grammar {
		if themes >= defaultGrammarGoals {
			break
		}
		themes = add(learning.GoalGrammar, "g", g.Title, g.Explanation, themes)
	}
	return goals
}

func noteAt(notes []string, i int) string {
	if i < len(notes) {
		return notes[i]
	}
	return ""
}

// clipNote — пояснение цели не длиннее предела настройки: объяснение темы в
// уроке бывает в абзац, а в промпт каждого хода уходит каждое пояснение.
// Режется по границе слова — оборванное слово модель читает как опечатку.
func clipNote(note string) string {
	note = strings.TrimSpace(note)
	r := []rune(note)
	if len(r) <= content.AvatarNoteMaxRunes {
		return note
	}
	cut := string(r[:content.AvatarNoteMaxRunes-1])
	if i := strings.LastIndexAny(cut, " \n"); i > len(cut)/2 {
		cut = cut[:i]
	}
	return strings.TrimSpace(cut) + "…"
}

// conversationAdapter — движок разговора по целям для модуля learning.
//
// learning владеет доступом, счётом ответов и отчётами, lessonpipe — тем,
// как устроен llm_service. Адаптер переводит одно в другое и ничего не
// решает сам.
type conversationAdapter struct{ runner *lessonpipe.Runner }

func (a conversationAdapter) Start(ctx context.Context, orgID, studentID uuid.UUID,
	in learning.EngineStart) (learning.EngineReply, error) {
	out, err := a.runner.ConversationStart(ctx, orgID, studentID, lessonpipe.ConversationStartRequest{
		Language: in.Language, Level: in.Level, RefinedText: in.Text, Collocations: in.Collocations,
		RolePrompt: in.RolePrompt, Goals: pipeGoals(in.Goals), MaxTurns: in.MaxTurns,
		History: pipeHistory(in.History), ReplyMode: pipeReplyMode(in.ReplyMode),
	})
	if err != nil {
		return learning.EngineReply{}, engineError(err)
	}
	return learning.EngineReply{State: out.State, Reply: out.Reply, ReplyRU: out.ReplyRU, TargetGoal: out.TargetGoal,
		Steer: out.Steer}, nil
}

func (a conversationAdapter) Turn(ctx context.Context, orgID, studentID uuid.UUID,
	in learning.EngineTurn) (learning.EngineReply, error) {
	out, err := a.runner.ConversationTurn(ctx, orgID, studentID, lessonpipe.ConversationTurnRequest{
		State: in.State,
		Answer: lessonpipe.ConversationAnswer{
			TurnNo: in.Answer.TurnNo, Text: in.Answer.Content, Input: in.Answer.Input, Edited: in.Answer.Edited,
		},
		History:   pipeHistory(in.History),
		Final:     in.Final,
		ReplyMode: pipeReplyMode(in.ReplyMode),
	})
	if err != nil {
		return learning.EngineReply{}, engineError(err)
	}
	return learning.EngineReply{State: out.State, Reply: out.Reply, ReplyRU: out.ReplyRU, TargetGoal: out.TargetGoal,
		Steer: out.Steer}, nil
}

func (a conversationAdapter) Observe(ctx context.Context, orgID, studentID uuid.UUID,
	in learning.EngineObserve) (learning.EngineObservation, error) {
	out, err := a.runner.ConversationObserve(ctx, orgID, studentID, lessonpipe.ConversationObserveRequest{
		State: in.State, TurnNo: in.TurnNo, Reply: in.Reply, Translate: in.Translate, SteerTurn: in.SteerTurn,
	})
	if err != nil {
		return learning.EngineObservation{}, engineError(err)
	}
	return learning.EngineObservation{State: out.State, ReplyRU: out.ReplyRU, Leaked: out.Leaked}, nil
}

// Progress и Check — без модели: идентификаторы для учёта им не нужны,
// lessonpipe ведёт их мимо пулов.
func (a conversationAdapter) Progress(ctx context.Context, _, _ uuid.UUID,
	state json.RawMessage) (learning.EngineProgress, error) {
	out, err := a.runner.ConversationProgress(ctx, state)
	if err != nil {
		return learning.EngineProgress{}, engineError(err)
	}
	p := learning.EngineProgress{
		Goals:        make([]learning.ProgressGoal, 0, len(out.Goals)),
		AvoidPhrases: out.AvoidPhrases,
		Corrections:  make([]learning.Correction, 0, len(out.Corrections)),
		AnswersLeft:  out.AnswersLeft,
		Finished:     out.Finished,
	}
	for _, g := range out.Goals {
		p.Goals = append(p.Goals, learning.ProgressGoal{Key: g.Key, Kind: g.Kind, Title: g.Title, Note: g.Note,
			Status: g.Status})
	}
	for _, c := range out.Corrections {
		p.Corrections = append(p.Corrections, learning.Correction{Quote: c.Quote, CorrectForm: c.CorrectForm})
	}
	if out.TargetGoal != nil {
		p.TargetGoal = *out.TargetGoal
	}
	return p, nil
}

func (a conversationAdapter) Check(ctx context.Context, _, _ uuid.UUID,
	in learning.EngineCheck) (learning.EngineCheckResult, error) {
	out, err := a.runner.ConversationCheck(ctx, lessonpipe.ConversationCheckRequest{
		State: in.State, Text: in.Text, Final: in.Final,
	})
	if err != nil {
		return learning.EngineCheckResult{}, engineError(err)
	}
	return learning.EngineCheckResult{OK: out.OK, Problems: out.Problems}, nil
}

// pipeReplyMode — режим ответа движка на языке lessonpipe. Строки те же;
// функция держит соответствие в одном месте, а не на доверии к совпадению.
func pipeReplyMode(mode string) string {
	if mode == learning.ReplySteer {
		return lessonpipe.ReplyModeSteer
	}
	return lessonpipe.ReplyModeText
}

func (a conversationAdapter) Analyze(ctx context.Context, orgID, studentID uuid.UUID,
	in learning.EngineAnalyze) (json.RawMessage, error) {
	transcript := make([]lessonpipe.ConversationTranscriptLine, 0, len(in.Transcript))
	for _, l := range in.Transcript {
		transcript = append(transcript, lessonpipe.ConversationTranscriptLine{
			TurnNo: l.TurnNo, Role: l.Role, Content: l.Content, Input: l.Input, Edited: l.Edited,
		})
	}
	return a.runner.ConversationAnalyze(ctx, orgID, studentID, lessonpipe.ConversationAnalyzeRequest{
		Language: in.Language, Level: in.Level, Goals: pipeGoals(in.Goals),
		State: in.State, Transcript: transcript,
	})
}

func pipeGoals(goals []learning.AvatarGoal) []lessonpipe.ConversationGoal {
	out := make([]lessonpipe.ConversationGoal, 0, len(goals))
	for _, g := range goals {
		out = append(out, lessonpipe.ConversationGoal{Key: g.Key, Kind: g.Kind, Title: g.Title, Note: g.Note})
	}
	return out
}

func pipeHistory(lines []learning.EngineLine) []lessonpipe.ConversationLine {
	out := make([]lessonpipe.ConversationLine, 0, len(lines))
	for _, l := range lines {
		out = append(out, lessonpipe.ConversationLine{Role: l.Role, Content: l.Content, TurnNo: l.TurnNo})
	}
	return out
}

// engineError — отказ движка «ход уже применён» на языке learning.
func engineError(err error) error {
	if errors.Is(err, lessonpipe.ErrConversationConflict) {
		return learning.ErrEngineConflict
	}
	return err
}

// avatarHistory переносит только роли и текст, без персональных данных.
func avatarHistory(turns []learning.DialogTurn) json.RawMessage {
	entries := make([]map[string]string, 0, len(turns))
	start := 0
	if len(turns) > 60 {
		start = len(turns) - 60
	}
	for _, turn := range turns[start:] {
		text := []rune(turn.Content)
		if len(text) > 1000 {
			text = text[:1000]
		}
		entries = append(entries, map[string]string{"role": turn.Role, "content": string(text)})
	}
	raw, _ := json.Marshal(entries)
	return raw
}

// liveAdapter — голосовой разговор урока через GPT Live.
//
// learning знает только, что голосовой разговор бывает; ключ, политика
// сессии и обмен SDP остаются в internal/live.
type liveAdapter struct{ api *live.API }

func (l liveAdapter) Supports(language string) bool { return l.api.Supports(language) }

func (l liveAdapter) Release(ctx context.Context, sessionID string) { l.api.Release(ctx, sessionID) }

// OpenConversation — живая сессия разговора по целям (раздел 10.3
// договора): цели с пояснениями, правила и инструменты делегата.
func (l liveAdapter) OpenConversation(ctx context.Context, orgID, studentID uuid.UUID, offerSDP string,
	brief learning.AvatarBrief) (learning.LiveAnswer, error) {
	session, err := l.api.OpenConversation(ctx, orgID, studentID, offerSDP, liveConversationBrief(brief))
	if err != nil {
		return learning.LiveAnswer{}, err
	}
	return learning.LiveAnswer{SessionID: session.ID, SDP: session.SDP}, nil
}

// liveConversationBrief — урок и разговор на языке internal/live.
func liveConversationBrief(brief learning.AvatarBrief) live.ConversationBrief {
	goals := make([]live.Goal, 0, len(brief.Goals))
	for _, g := range brief.Goals {
		goals = append(goals, live.Goal{Key: g.Key, Kind: g.Kind, Title: g.Title, Note: g.Note})
	}
	return live.ConversationBrief{
		Language:   brief.Language,
		Level:      brief.Level,
		Text:       brief.Text,
		RolePrompt: brief.RolePrompt,
		Goals:      goals,
		MaxTurns:   brief.MaxTurns,
		// Уже сказанное — только роли и текст, как у голосового диалога.
		History:       string(avatarHistory(brief.History)),
		LevelGuidance: brief.LevelGuidance,
	}
}

// voiceStyleAdapter — тон разговора для синтеза речи.
//
// speech спрашивает тон по попытке и не знает ни про уроки, ни про
// настройку аватара; learning отвечает, проверив, что попытка ученика.
type voiceStyleAdapter struct{ svc *learning.Service }

func (a voiceStyleAdapter) StyleForAttempt(ctx context.Context, actor iam.Actor,
	attemptID uuid.UUID) (string, error) {
	return a.svc.AvatarVoiceStyle(ctx, actor, attemptID)
}
