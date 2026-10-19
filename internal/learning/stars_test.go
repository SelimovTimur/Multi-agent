package learning

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

// Звёзды — награда ребёнку за работу. Звезда, погасшая без причины, или
// «1 из 3» за выдачу, где условий всего одно, читается как наказание, поэтому
// правила проверяются здесь, а не на уроке.

// gaileWork — ответы по «Гаилә»: пары — одна верная из двух, пропуски —
// две верные из трёх.
func gaileWork(ex1, ex2 uuid.UUID, progress []TaskProgress) studentWork {
	answers := answersFor(ex1, map[string]bool{"әни": true, "әти": false})
	for k, v := range answersFor(ex2, map[string]bool{"q1": true, "q2": true, "q3": false}) {
		answers[k] = v
	}
	return newWork(&Attempt{Answers: answers}, progress)
}

func TestClassIssuesOfOneDayAreOneIssue(t *testing.T) {
	tasks, ex1, ex2 := testLesson()
	now := at("2026-09-29T10:30:00+03:00")
	today := classDueAt(now, msk)
	tomorrow := now.Add(24 * time.Hour)
	homeDue := at("2026-10-03T23:59:59+03:00")

	// Учитель открыл задания по ходу урока двумя выдачами, на следующий
	// день — ещё одной, и задал домашнюю работу.
	first := issue(IssueClass, now.Add(-20*time.Minute), &today, TaskObjectives, TaskText)
	second := issue(IssueClass, now.Add(-5*time.Minute), &today, ExerciseTaskKey(ex1))
	next := issue(IssueClass, tomorrow, ptr(classDueAt(tomorrow, msk)), ExerciseTaskKey(ex2))
	home := issue(IssueHome, now.Add(-time.Minute), &homeDue, TaskDialog)

	b := newBoard(tasks, []Issue{next, home, second, first}, tomorrow.Add(time.Minute), false)
	got := studentIssues(b, newWork(nil, nil), ConversationWords{}, msk)
	if len(got) != 3 {
		t.Fatalf("выдач у ученика %d, ожидали три: урок, домашка, урок следующего дня", len(got))
	}
	if got[0].Kind != IssueClass || len(got[0].Tasks) != 3 || !got[0].OpenedAt.Equal(first.OpenedAt) {
		t.Errorf("две выдачи одного урока — одна, с первой даты: %+v", got[0])
	}
	if !got[0].Closed || got[0].Open {
		t.Errorf("урок вчерашнего дня закрыт по концу дня: %+v", got[0])
	}
	if got[1].Kind != IssueHome || got[2].Kind != IssueClass || !got[2].Open {
		t.Errorf("домашка, затем открытый урок следующего дня: %+v / %+v", got[1], got[2])
	}
	if got[1].DueAt == nil || !got[1].DueAt.Equal(homeDue) {
		t.Errorf("срок домашки: %v", got[1].DueAt)
	}
}

func TestStarsOfIssue(t *testing.T) {
	tasks, ex1, ex2 := testLesson()
	now := at("2026-09-29T10:30:00+03:00")
	today := classDueAt(now, msk)
	homeDue := at("2026-10-03T23:59:59+03:00")
	done := now.Add(-time.Minute)

	class := issue(IssueClass, now.Add(-time.Hour), &today, TaskText, ExerciseTaskKey(ex1), ExerciseTaskKey(ex2))
	home := issue(IssueHome, now.Add(-time.Minute), &homeDue, TaskObjectives, TaskDialog)
	progress := []TaskProgress{
		{TaskKey: TaskText, OpenedAt: now.Add(-time.Hour), DoneAt: &done},
		{TaskKey: TaskObjectives, OpenedAt: now.Add(-time.Hour), DoneAt: &done},
		{TaskKey: TaskDialog, OpenedAt: now.Add(-time.Hour), DoneAt: &done},
	}
	b := newBoard(tasks, []Issue{class, home}, now, false)
	words := ConversationWords{State: WordsReady, Used: 3, Total: 6}
	got := studentIssues(b, gaileWork(ex1, ex2, progress), words, msk)

	// На уроке: всё сделано, верных 3 из 5 — 60 %, разговора нет.
	lesson := got[0]
	if lesson.Done != 3 || lesson.Correct != 3 || lesson.Items != 5 || lesson.Pct() != 60 || lesson.Words != nil {
		t.Errorf("счёт выдачи на уроке: %+v", lesson)
	}
	want := []Star{{Kind: StarDone, On: true}, {Kind: StarCorrect, On: false}}
	if !equalStars(lesson.Stars, want) {
		t.Errorf("звёзды на уроке %+v, ожидали %+v", lesson.Stars, want)
	}
	// Серия: «әни» верно, «әти» нет, затем две верные подряд — лучшая 2.
	if lesson.BestStreak != 2 {
		t.Errorf("лучшая серия %d, ожидали 2", lesson.BestStreak)
	}

	// Домашка: без упражнений — звезды «80 %» нет совсем; 3 выражения из 6 —
	// ровно половина, звезда горит.
	homework := got[1]
	// Цели урока — вводный экран: в счёте домашки одно задание, разговор.
	if homework.Total != 1 || homework.Done != 1 || len(homework.Tasks) != 2 {
		t.Errorf("счёт домашки без целей урока: всего %d, сделано %d, заданий в списке %d",
			homework.Total, homework.Done, len(homework.Tasks))
	}
	want = []Star{{Kind: StarDone, On: true}, {Kind: StarWords, On: true}}
	if !equalStars(homework.Stars, want) {
		t.Errorf("звёзды домашки %+v, ожидали %+v", homework.Stars, want)
	}
	if earned, total := lessonStars(got); earned != 3 || total != 4 {
		t.Errorf("звёзды урока %d из %d, ожидали 3 из 4", earned, total)
	}
	if pct := lessonPct(got); pct == nil || *pct != 60 {
		t.Errorf("доля верных урока: %v", pct)
	}
}

func TestStarsWhenIssueClosedEarly(t *testing.T) {
	tasks, ex1, ex2 := testLesson()
	now := at("2026-09-29T10:30:00+03:00")
	finished := now.Add(-time.Minute)

	// Учитель завершил работу на уроке, а ученик успел только пары.
	class := issue(IssueClass, now.Add(-time.Hour), ptr(classDueAt(now, msk)), ExerciseTaskKey(ex1), ExerciseTaskKey(ex2))
	class.ClosedAt = &finished
	w := newWork(&Attempt{Answers: answersFor(ex1, map[string]bool{"әни": true, "әти": true})}, nil)
	got := studentIssues(newBoard(tasks, []Issue{class}, now, false), w, ConversationWords{}, msk)[0]

	if !got.Settled() || got.Done != 1 {
		t.Fatalf("закрытая выдача засчитана со сделанным одним заданием: %+v", got)
	}
	// Неотвеченные пропуски — неверные: 2 из 5.
	if got.Pct() != 40 {
		t.Errorf("доля верных %d, ожидали 40", got.Pct())
	}
	want := []Star{{Kind: StarDone, On: false}, {Kind: StarCorrect, On: false}}
	if !equalStars(got.Stars, want) {
		t.Errorf("звёзды %+v, ожидали %+v", got.Stars, want)
	}
}

func TestOpenIssueStarsDoNotCountYet(t *testing.T) {
	tasks, _, _ := testLesson()
	now := at("2026-09-29T10:30:00+03:00")
	class := issue(IssueClass, now.Add(-time.Hour), ptr(classDueAt(now, msk)), TaskText, TaskObjectives)
	got := studentIssues(newBoard(tasks, []Issue{class}, now, false), newWork(nil, nil), ConversationWords{}, msk)

	// Выдача из текста и целей: одна звезда, а не «1 из 3».
	if len(got[0].Stars) != 1 || got[0].Stars[0].Kind != StarDone {
		t.Errorf("у выдачи без упражнений и разговора одна звезда: %+v", got[0].Stars)
	}
	if earned, total := lessonStars(got); earned != 0 || total != 0 {
		t.Errorf("звёзды открытой недоделанной выдачи не считаются: %d из %d", earned, total)
	}
	if pct := lessonPct(got); pct != nil {
		t.Errorf("упражнений не выдавали — доли верных нет: %v", *pct)
	}
}

func TestWordsStarNeedsHalfRoundedUp(t *testing.T) {
	cases := []struct {
		words ConversationWords
		need  int
		on    bool
	}{
		{ConversationWords{State: WordsReady, Used: 5, Total: 10}, 5, true},
		{ConversationWords{State: WordsReady, Used: 4, Total: 10}, 5, false},
		{ConversationWords{State: WordsReady, Used: 3, Total: 5}, 3, true},
		{ConversationWords{State: WordsReady, Used: 2, Total: 5}, 3, false},
		// Разбор ещё готовится — звезда пока не горит, сколько бы ни было.
		{ConversationWords{State: WordsPending, Used: 9, Total: 10}, 5, false},
		{ConversationWords{State: WordsNone}, 0, false},
	}
	for _, c := range cases {
		if c.words.Need() != c.need || c.words.Earned() != c.on {
			t.Errorf("%+v: нужно %d, горит %v; ожидали %d и %v", c.words, c.words.Need(), c.words.Earned(), c.need, c.on)
		}
	}
}

func TestBestStreakFollowsAnswerTime(t *testing.T) {
	tasks, ex1, ex2 := testLesson()
	now := at("2026-09-29T10:30:00+03:00")
	at := func(min int) time.Time { return now.Add(time.Duration(min) * time.Minute) }
	answers := map[string]GivenAnswer{
		ex1.String() + ":әни": {Correct: true, At: at(1)},
		ex1.String() + ":әти": {Correct: false, At: at(5)},
		ex2.String() + ":q1":  {Correct: true, At: at(2)},
		ex2.String() + ":q2":  {Correct: true, At: at(3)},
		ex2.String() + ":q3":  {Correct: true, At: at(4)},
	}
	class := issue(IssueClass, now.Add(-time.Hour), ptr(classDueAt(now, msk)), ExerciseTaskKey(ex1), ExerciseTaskKey(ex2))
	b := newBoard(tasks, []Issue{class}, now, false)

	// По времени: четыре верных подряд, ошибка последней.
	got := studentIssues(b, newWork(&Attempt{Answers: answers}, nil), ConversationWords{}, msk)[0]
	if got.BestStreak != 4 {
		t.Errorf("серия по времени ответа %d, ожидали 4", got.BestStreak)
	}

	// Без времени — по порядку урока: верно, ошибка, три верных.
	for k, v := range answers {
		v.At = time.Time{}
		answers[k] = v
	}
	got = studentIssues(b, newWork(&Attempt{Answers: answers}, nil), ConversationWords{}, msk)[0]
	if got.BestStreak != 3 {
		t.Errorf("серия по порядку урока %d, ожидали 3", got.BestStreak)
	}
}

func TestSectionStarsInMyLessons(t *testing.T) {
	tasks, ex1, _ := testLesson()
	now := at("2026-09-29T10:30:00+03:00")
	done := now.Add(-time.Minute)
	a := Assignment{ID: uuid.New(), State: StateActive}
	class := issue(IssueClass, now.Add(-time.Hour), ptr(classDueAt(now, msk)), TaskText, ExerciseTaskKey(ex1))
	attempt := &Attempt{ID: uuid.New(), Answers: answersFor(ex1, map[string]bool{"әни": true, "әти": true})}
	progress := []TaskProgress{{TaskKey: TaskText, OpenedAt: now.Add(-time.Hour), DoneAt: &done}}

	got := studentLesson(a, tasks, []Issue{class}, attempt, progress, ConversationWords{}, msk, now)
	s := got.ClassSection
	if s == nil || s.Left != 0 {
		t.Fatalf("раздел на уроке сделан: %+v", s)
	}
	want := []Star{{Kind: StarDone, On: true}, {Kind: StarCorrect, On: true}}
	if !equalStars(s.Stars, want) {
		t.Errorf("звёзды раздела %+v, ожидали %+v", s.Stars, want)
	}
	if earned, total := lessonStars(got.Issues); earned != 2 || total != 2 {
		t.Errorf("сделанная выдача уже даёт звёзды: %d из %d", earned, total)
	}
}

func TestConversationWordsFromColumns(t *testing.T) {
	cases := []struct {
		name     string
		closed   bool
		status   string
		credited *int
		total    *int
		want     ConversationWords
	}{
		{"разбор готов", true, ReportReady, ptr(4), ptr(6), ConversationWords{State: WordsReady, Used: 4, Total: 6}},
		{"разговор закрыт, разбора нет", true, "", nil, nil, ConversationWords{State: WordsPending}},
		{"разбор идёт", true, ReportPending, nil, nil, ConversationWords{State: WordsPending}},
		{"разбор упал — ещё перезапустится", true, ReportFailed, nil, nil, ConversationWords{State: WordsPending}},
		{"разговор не закончен", false, "", nil, nil, ConversationWords{State: WordsNone}},
	}
	for _, c := range cases {
		if got := conversationWords(c.closed, c.status, c.credited, c.total); got != c.want {
			t.Errorf("%s: %+v, ожидали %+v", c.name, got, c.want)
		}
	}
}

func equalStars(got, want []Star) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
