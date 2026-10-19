package learning

// Страница урока в аналитике (#66): сводка урока по классу и разбор
// письменных заданий — доля верных и частые неверные ответы.
//
// Разговор с аватаром здесь не считается: у него свой отчёт по классу
// (GET /assignments/{id}/conversation), и страница его переиспользует.

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/NATIVE/NATIVE/internal/iam"
	"github.com/NATIVE/NATIVE/internal/platform/httpx"
	"github.com/google/uuid"
)

// LessonAnalytics — урок класса глазами аналитики.
type LessonAnalytics struct {
	Assignment Assignment
	Version    VersionInfo
	Class      ClassInfo
	// Students — ученики нынешнего состава; Passed — сколько прошли урок
	// до конца.
	Students int
	Passed   int
	// Tasks и Conversation — те же доли, что в итоговой строке таблицы
	// результатов: числа на странице урока и в столбце обязаны совпадать.
	Tasks        Share
	Conversation Share
	// Talked — у скольких учеников готов разбор разговора.
	Talked int
	// HasDialog — в уроке есть задание «Диалог»; DialogIssued — оно выдано.
	HasDialog    bool
	DialogIssued bool
	// Exercises — письменные задания в порядке урока.
	Exercises []ExerciseStats
}

// ExerciseStats — одно письменное задание по классу.
type ExerciseStats struct {
	Task LessonTask
	// Students — сколько учеников ответили хоть на один вопрос.
	Students int
	// Answered и Correct — данные ответы и верные из них. Доля считается от
	// данных ответов, а не от вопросов × учеников: задание, которое половина
	// класса не открывала, не «наполовину провалено».
	Answered int
	Correct  int
	// Mistakes — одинаковые неверные ответы, склеенные; частые сверху.
	Mistakes []Mistake
}

// Mistake — один и тот же неверный ответ на один вопрос у нескольких
// учеников.
type Mistake struct {
	Prompt   string
	Answer   string
	Expected string
	Count    int
	// Students — кто так ответил, по имени.
	Students []ClassMember
}

// LessonAnalyticsFor собирает страницу урока в аналитике.
func (s *Service) LessonAnalyticsFor(ctx context.Context, actor iam.Actor,
	assignmentID uuid.UUID) (LessonAnalytics, error) {

	a, class, err := s.ownedAssignment(ctx, actor, assignmentID)
	if err != nil {
		return LessonAnalytics{}, err
	}
	c, err := s.loadClasswork(ctx, actor, a, class)
	if err != nil {
		return LessonAnalytics{}, err
	}
	reviews, err := s.lessons.ExerciseReviews(ctx, actor.OrgID, a.LessonVersionID)
	if err != nil {
		return LessonAnalytics{}, err
	}
	convs, err := s.store.ConversationsOfAssignment(ctx, s.pool, actor.OrgID, a.ID)
	if err != nil {
		return LessonAnalytics{}, httpx.Internal("Не удалось прочитать разговоры").Wrap(err)
	}
	return buildLessonAnalytics(c, reviews, convs, time.Now()), nil
}

// buildLessonAnalytics — арифметика страницы без базы.
//
// Сводка считается тем же assembleAnalytics, что и таблица результатов, а
// не своей копией: иначе первое же изменение правил клетки разведёт числа
// на двух экранах.
func buildLessonAnalytics(c classwork, reviews map[uuid.UUID]ExerciseReview, convs []ConversationRow,
	now time.Time) LessonAnalytics {

	out := LessonAnalytics{
		Assignment: c.assignment, Version: c.version, Class: c.class,
		Exercises: []ExerciseStats{},
	}

	attempts := make([]ClassAttempt, 0, len(c.attempts))
	for _, at := range c.attempts {
		attempts = append(attempts, ClassAttempt{
			ID: at.ID, AssignmentID: at.AssignmentID, StudentID: at.StudentID, Completed: at.Completed,
			CorrectAnswers: at.CorrectAnswers, TotalQuestions: at.TotalQuestions, LastSeenAt: at.LastSeenAt,
		})
	}
	table := assembleAnalytics(c.members, []Assignment{c.assignment},
		map[uuid.UUID]VersionInfo{c.assignment.LessonVersionID: c.version}, attempts, convs, now)
	out.Students, out.Passed = len(c.members), table.Passed
	if len(table.Lessons) > 0 {
		out.Tasks, out.Conversation = table.Lessons[0].Tasks, table.Lessons[0].Conversation
	}
	for _, st := range table.Students {
		if conv := st.Cells[0].Conversation; conv != nil && conv.Ready {
			out.Talked++
		}
	}

	b := c.board(now)
	works := c.works()
	members := sortedMembers(c.members)
	for _, t := range c.tasks {
		if t.Kind == TaskDialog {
			out.HasDialog = true
			_, out.DialogIssued = b.issueOf(t.Key)
		}
		if t.Kind != TaskExercise || t.Questions == 0 {
			continue
		}
		out.Exercises = append(out.Exercises, exerciseStats(t, reviews[t.ExerciseID], works, members))
	}
	return out
}

// exerciseStats — доля верных и склеенные ошибки одного задания.
//
// Считаются только ученики нынешнего состава — как и во всей аналитике.
func exerciseStats(t LessonTask, review ExerciseReview, works map[uuid.UUID]studentWork,
	members []ClassMember) ExerciseStats {

	out := ExerciseStats{Task: t, Mistakes: []Mistake{}}
	index := map[string]int{}
	for _, m := range members {
		qs := answeredQuestions(t, review, works[m.ID].answers)
		if len(qs) == 0 {
			continue
		}
		out.Students++
		for _, q := range qs {
			out.Answered++
			if q.Correct {
				out.Correct++
				continue
			}
			// Одинаковые — с точностью до регистра, пробелов и знаков:
			// «Казанга» и «казанга.» учителю — одна и та же ошибка. Правило
			// то же, что у трудных слов (sameAnswer).
			key := q.Prompt + "\x00" + sameAnswer(q.Answer)
			i, ok := index[key]
			if !ok {
				i = len(out.Mistakes)
				index[key] = i
				out.Mistakes = append(out.Mistakes, Mistake{
					Prompt: q.Prompt, Answer: strings.TrimSpace(q.Answer), Expected: q.Expected,
				})
			}
			out.Mistakes[i].Count++
			out.Mistakes[i].Students = append(out.Mistakes[i].Students, m)
		}
	}
	// Частые сверху; при равенстве — в порядке появления, чтобы строки не
	// прыгали при каждом открытии.
	slices.SortStableFunc(out.Mistakes, func(x, y Mistake) int { return y.Count - x.Count })
	return out
}
