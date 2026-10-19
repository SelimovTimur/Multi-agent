package learning

// Трудные слова класса (#67): слова и выражения из лексики урока, в которых
// класс ошибается чаще всего, — блок «Что повторить на следующем уроке» под
// таблицей результатов.
//
// Ошибка по слову — неверный ответ на вопрос задания, где это слово есть
// (в вопросе или в верном ответе), и ошибка по нему в разговоре с аватаром.
// Не использовал слово в разговоре — не ошибка: аватар мог и не дать повода.
//
// Ответ привязывается к слову по тексту, а не по ссылке из упражнения:
// упражнения не помнят, из какого слова сделаны. На старых уроках учеников
// не было, так что задним числом ничего привязывать не нужно.

import (
	"context"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/NATIVE/NATIVE/internal/iam"
	"github.com/NATIVE/NATIVE/internal/platform/httpx"
	"github.com/google/uuid"
)

// LessonWord — слово или выражение из лексики урока. Зеркало
// content.LexicalUnit без служебных полей.
type LessonWord struct {
	Phrase      string
	Translation string
}

// HardWord — трудное слово класса.
type HardWord struct {
	AssignmentID uuid.UUID
	LessonTitle  string
	Word         LessonWord
	// Errors и Answers — неверные и все ответы со словом: задания и
	// разговор вместе.
	Errors  int
	Answers int
	// Met — скольким ученикам слово встречалось; Struggled — кто ошибся,
	// по имени. Это «кому повторить».
	Met       int
	Struggled []ClassMember
	// StudentErrors — ошибки каждого из Struggled: карточка ученика
	// показывает его число рядом с числом всего класса.
	StudentErrors map[uuid.UUID]int
	// Tasks и Conversation — те же числа по отдельности: в раскрытии
	// задания и разговор показаны разными блоками.
	Tasks        WordTally
	Conversation WordTally
	// Written — одинаковые неверные ответы в заданиях, склеенные; частые
	// сверху.
	Written []WordMistake
	// Spoken — ошибки в разговоре с цитатой.
	Spoken []WordQuote
	// Confusions — с чем путают: неверные ответы в заданиях, без повторов.
	Confusions []string
}

// WordTally — ответы со словом в одном виде работы.
type WordTally struct {
	Answers  int
	Errors   int
	Students int
	Erred    int
}

// WordMistake — один и тот же неверный ответ на один вопрос задания.
type WordMistake struct {
	TaskTitle string
	// TaskType — вид упражнения (matching, gap_fill…): подпись «Сопоставление»
	// экран берёт из тех же названий, что в редакторе урока.
	TaskType string
	Prompt   string
	Answer   string
	Expected string
	Count    int
	Students []ClassMember
}

// WordQuote — ошибка по слову в разговоре.
type WordQuote struct {
	Member      ClassMember
	TurnNo      int
	Quote       string
	CorrectForm string
}

// Пороги трудного слова. Оба условия сразу: одна доля учеников даёт в
// маленьком классе «трудное» из-за одного рассеянного, одна доля ответов —
// слово, на котором споткнулся один ученик, зато много раз.
const (
	// hardMinStudents — ошиблись хотя бы двое: ошибка одного — повод
	// поговорить с ним, а не повторять слово со всем классом.
	hardMinStudents = 2
	// hardStudentShare — ошиблись не меньше четверти тех, кому слово
	// встречалось.
	hardStudentShare = 25
	// hardAnswerShare — неверны не меньше 30 % ответов со словом.
	hardAnswerShare = 30
)

// isHard — правило трудного слова. Доли — от тех, кому слово встречалось,
// а не от всего класса: слово из урока, который половина класса не
// открывала, не становится от этого лёгким.
func isHard(met, erred, answers, errors int) bool {
	return erred >= hardMinStudents &&
		erred*100 >= met*hardStudentShare &&
		errors*100 >= answers*hardAnswerShare
}

// ClassHardWords — трудные слова класса по всем его урокам. Если задан
// lesson — только по этому уроку класса: страница урока в аналитике (#68).
// Слово считается внутри своего урока, так что числа те же, что в общем
// списке, — урок просто не дочитывает чужие уроки.
//
// Отдельная ручка, а не часть таблицы результатов: для неё нужны ответы по
// вопросам, ключи заданий и отчёты разговоров каждого урока, а таблица
// должна открываться сразу.
func (s *Service) ClassHardWords(ctx context.Context, actor iam.Actor, classID, lesson uuid.UUID) ([]HardWord, error) {
	if _, err := s.classes.OwnedClass(ctx, actor, classID); err != nil {
		return nil, err
	}
	members, err := s.classes.Roster(ctx, actor, classID)
	if err != nil {
		return nil, err
	}
	assignments, err := s.store.ListByClass(ctx, s.pool, actor.OrgID, classID, "")
	if err != nil {
		return nil, httpx.Internal("Не удалось прочитать назначения").Wrap(err)
	}
	if lesson != uuid.Nil {
		assignments = slices.DeleteFunc(assignments, func(a Assignment) bool { return a.ID != lesson })
	}
	if len(members) == 0 || len(assignments) == 0 {
		return []HardWord{}, nil
	}
	versionIDs := make([]uuid.UUID, 0, len(assignments))
	for _, a := range assignments {
		versionIDs = append(versionIDs, a.LessonVersionID)
	}
	versions, err := s.lessons.VersionsInfo(ctx, actor.OrgID, versionIDs)
	if err != nil {
		return nil, err
	}
	tasks, err := s.lessons.LessonTasks(ctx, actor.OrgID, versionIDs)
	if err != nil {
		return nil, err
	}

	lessons := make([]wordLesson, 0, len(assignments))
	for _, a := range assignments {
		l, err := s.wordLessonOf(ctx, actor.OrgID, a)
		if err != nil {
			return nil, err
		}
		if len(l.words) == 0 {
			continue
		}
		l.title = versions[a.LessonVersionID].Name()
		l.tasks = tasks[a.LessonVersionID]
		lessons = append(lessons, l)
	}
	return classHardWords(members, lessons), nil
}

// wordLessonOf дочитывает урок класса: лексику, ключи заданий, ответы и
// разговоры. Урок без лексики дальше не читается — считать в нём нечего.
func (s *Service) wordLessonOf(ctx context.Context, orgID uuid.UUID, a Assignment) (wordLesson, error) {
	l := wordLesson{assignment: a}
	var err error
	if l.words, err = s.lessons.LessonWords(ctx, orgID, a.LessonVersionID); err != nil || len(l.words) == 0 {
		return l, err
	}
	if l.reviews, err = s.lessons.ExerciseReviews(ctx, orgID, a.LessonVersionID); err != nil {
		return l, err
	}
	if l.attempts, err = s.store.AttemptsOfAssignment(ctx, s.pool, orgID, a.ID); err != nil {
		return l, httpx.Internal("Не удалось прочитать ответы").Wrap(err)
	}
	// Без разговорного контура разговоров нет — считаются только задания.
	if s.material == nil {
		return l, nil
	}
	brief, err := s.briefOfVersion(ctx, orgID, a.LessonVersionID)
	if err != nil {
		return l, err
	}
	l.goals = brief.Goals
	if l.convs, err = s.store.ConversationsOfAssignment(ctx, s.pool, orgID, a.ID); err != nil {
		return l, httpx.Internal("Не удалось прочитать разговоры").Wrap(err)
	}
	return l, nil
}

// wordLesson — всё, из чего считаются трудные слова одного урока класса.
type wordLesson struct {
	assignment Assignment
	title      string
	words      []LessonWord
	tasks      []LessonTask
	reviews    map[uuid.UUID]ExerciseReview
	attempts   []Attempt
	goals      []AvatarGoal
	convs      []ConversationRow
}

// classHardWords — арифметика блока без базы.
//
// Считаются только ученики нынешнего состава — как и во всей аналитике.
// Порядок: больше ошибок — выше, при равенстве — больше учеников; дальше —
// как в уроках, чтобы строки не прыгали между открытиями.
func classHardWords(members []ClassMember, lessons []wordLesson) []HardWord {
	out := []HardWord{}
	roster := sortedMembers(members)
	for _, l := range lessons {
		for _, w := range l.words {
			hw := countWord(l, w, roster)
			if isHard(hw.Met, len(hw.Struggled), hw.Answers, hw.Errors) {
				out = append(out, hw)
			}
		}
	}
	slices.SortStableFunc(out, func(x, y HardWord) int {
		if x.Errors != y.Errors {
			return y.Errors - x.Errors
		}
		return len(y.Struggled) - len(x.Struggled)
	})
	return out
}

// countWord считает одно слово урока по всему составу.
func countWord(l wordLesson, w LessonWord, roster []ClassMember) HardWord {
	out := HardWord{
		AssignmentID: l.assignment.ID, LessonTitle: l.title, Word: w,
		Struggled: []ClassMember{}, StudentErrors: map[uuid.UUID]int{}, Written: []WordMistake{}, Spoken: []WordQuote{}, Confusions: []string{},
	}
	word := wordTokens(w.Phrase)
	if len(word) == 0 {
		return out
	}
	questions := wordQuestions(l, word)
	goals := wordGoals(l.goals, word)

	attemptOf := make(map[uuid.UUID]Attempt, len(l.attempts))
	for _, at := range l.attempts {
		attemptOf[at.StudentID] = at
	}
	convOf := make(map[uuid.UUID]ConversationRow, len(l.convs))
	for _, c := range l.convs {
		convOf[c.StudentID] = c
	}

	index := map[string]int{}
	for _, m := range roster {
		met, wrong := false, 0

		// Задания.
		tally := WordTally{}
		for _, q := range questions {
			given, ok := attemptOf[m.ID].Answers[q.answerKey]
			if !ok {
				continue
			}
			tally.Answers++
			if given.Correct {
				continue
			}
			tally.Errors++
			answer := strings.TrimSpace(q.review.text(given.Answer))
			key := q.task.Key + "\x00" + q.review.ItemID + "\x00" + sameAnswer(answer)
			i, seen := index[key]
			if !seen {
				i = len(out.Written)
				index[key] = i
				expected := q.review.Expected
				if expected == "" {
					expected = q.review.text(given.Expected)
				}
				out.Written = append(out.Written, WordMistake{
					TaskTitle: q.task.Title, TaskType: q.task.Type, Prompt: q.review.Prompt,
					Answer: answer, Expected: expected, Students: []ClassMember{},
				})
			}
			out.Written[i].Count++
			if !slices.ContainsFunc(out.Written[i].Students, func(x ClassMember) bool { return x.ID == m.ID }) {
				out.Written[i].Students = append(out.Written[i].Students, m)
			}
		}
		if tally.Answers > 0 {
			met = true
			out.Tasks.Students++
		}
		if tally.Errors > 0 {
			out.Tasks.Erred++
		}
		wrong += tally.Errors
		out.Tasks.Answers += tally.Answers
		out.Tasks.Errors += tally.Errors

		// Разговор.
		if row, ok := convOf[m.ID]; ok {
			uses, quotes := spokenWord(row, goals, word)
			if uses+len(quotes) > 0 {
				met = true
				out.Conversation.Students++
			}
			if len(quotes) > 0 {
				out.Conversation.Erred++
			}
			wrong += len(quotes)
			out.Conversation.Answers += uses + len(quotes)
			out.Conversation.Errors += len(quotes)
			for _, q := range quotes {
				q.Member = m
				out.Spoken = append(out.Spoken, q)
			}
		}

		if met {
			out.Met++
		}
		if wrong > 0 {
			out.Struggled = append(out.Struggled, m)
			out.StudentErrors[m.ID] = wrong
		}
	}
	out.Answers = out.Tasks.Answers + out.Conversation.Answers
	out.Errors = out.Tasks.Errors + out.Conversation.Errors

	// Частые сверху; при равенстве — в порядке появления.
	slices.SortStableFunc(out.Written, func(x, y WordMistake) int { return y.Count - x.Count })
	seen := map[string]bool{}
	for _, m := range out.Written {
		k := sameAnswer(m.Answer)
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		out.Confusions = append(out.Confusions, m.Answer)
	}
	return out
}

// wordQuestion — вопрос задания, привязанный к слову.
type wordQuestion struct {
	task      LessonTask
	review    QuestionReview
	answerKey string
}

// wordQuestions — вопросы письменных заданий урока, где есть слово: в
// тексте вопроса или в верном ответе. В сопоставлении слово стоит в
// вопросе, в пропусках — в ответе.
func wordQuestions(l wordLesson, word []string) []wordQuestion {
	var out []wordQuestion
	for _, t := range l.tasks {
		if t.Kind != TaskExercise {
			continue
		}
		prefix := t.ExerciseID.String() + ":"
		for _, q := range l.reviews[t.ExerciseID].Questions {
			if mentions(wordTokens(q.Prompt), word) || mentions(wordTokens(q.Expected), word) {
				out = append(out, wordQuestion{task: t, review: q, answerKey: prefix + q.ItemID})
			}
		}
	}
	return out
}

// wordGoals — ключи целей-фраз разговора, которые и есть это слово. Цель
// урока, который учитель не настраивал, — выражение словаря как есть; цель,
// которую учитель поправил, может быть длиннее или короче слова.
func wordGoals(goals []AvatarGoal, word []string) map[string]bool {
	out := map[string]bool{}
	for _, g := range goals {
		if g.Kind != GoalPhrase {
			continue
		}
		title := wordTokens(g.Title)
		if mentions(title, word) || mentions(word, title) {
			out[g.Key] = true
		}
	}
	return out
}

// spokenWord — верные употребления слова и ошибки по нему в разговоре
// ученика. Только готовый разбор: незаконченный разговор ещё не оценён.
//
// Ошибка по слову — отметка «ошибка» по его цели и общая ошибка, которую
// движок сгруппировал под это слово. Ошибки выше уровня урока не считаются
// — учитель их не видит. Одна реплика — одна ошибка, даже если движок
// отметил её дважды.
func spokenWord(row ConversationRow, goals map[string]bool, word []string) (int, []WordQuote) {
	snap, a, ok := readyReport(row)
	if !ok {
		return 0, nil
	}
	uses := 0
	for _, g := range a.Goals {
		if goals[g.Key] {
			uses += g.CorrectUses
		}
	}
	var quotes []WordQuote
	turns := map[int]bool{}
	for _, m := range visibleMarks(a.Marks, snap.Turns) {
		if m.Verdict != VerdictError || !goals[m.GoalKey] || turns[m.TurnNo] {
			continue
		}
		turns[m.TurnNo] = true
		quotes = append(quotes, WordQuote{TurnNo: m.TurnNo, Quote: m.Quote, CorrectForm: m.CorrectForm})
	}
	for _, e := range visibleErrors(a.GeneralErrors, snap.Turns) {
		if e.Visibility == VisibilityAboveLevel || turns[e.TurnNo] {
			continue
		}
		if !mentions(wordTokens(e.GroupKey), word) && !mentions(wordTokens(e.GroupLabel), word) {
			continue
		}
		turns[e.TurnNo] = true
		quotes = append(quotes, WordQuote{TurnNo: e.TurnNo, Quote: e.Quote, CorrectForm: e.CorrectForm})
	}
	slices.SortStableFunc(quotes, func(x, y WordQuote) int { return x.TurnNo - y.TurnNo })
	return uses, quotes
}

// wordTokens — слова текста строчными, без знаков.
func wordTokens(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// mentions — в тексте есть слово: все его части по порядку, между ними
// могут стоять другие слова («Мин … яшим» есть в «Мин Казанда яшим»).
func mentions(text, word []string) bool {
	if len(word) == 0 {
		return false
	}
	i := 0
	for _, t := range text {
		if tokenFits(t, word[i]) {
			i++
			if i == len(word) {
				return true
			}
		}
	}
	return false
}

// tokenFits — слово текста — форма слова из словаря.
//
// Татарский приклеивает окончания: «күрше» в тексте бывает «күршем»,
// «күршесе». Поэтому сравнивается начало слова. У длинных слов последняя
// буква основы перед окончанием меняется («китап» — «китабым»), её не
// сравниваем. Короткие слова — только целиком: иначе «ул» нашлось бы в
// «улым».
func tokenFits(t, w string) bool {
	n := utf8.RuneCountInString(w)
	switch {
	case n < 3:
		return t == w
	case n >= 5:
		r := []rune(w)
		return utf8.RuneCountInString(t) >= n && strings.HasPrefix(t, string(r[:n-1]))
	}
	return strings.HasPrefix(t, w)
}

// sameAnswer — по чему одинаковые ответы склеиваются: без регистра, пробелов
// и знаков. Буквы не сводятся: «а» и «ә» в татарском — разные слова, и
// ответ «бала» вместо «бәлә» — настоящая ошибка, а не опечатка.
func sameAnswer(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, s)
}
