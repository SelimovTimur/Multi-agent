package learning

// Выдачи и звёзды глазами ученика: «Мои занятия», итог выдачи и «Результаты
// урока» (docs/product/student-screens/story.md, раздел «Алгоритм»).
//
// Здесь только арифметика, без базы и сети: правила звёзд проверяются
// тестами, а не глазами на уроке. Звёзды не хранятся — они пересчитываются
// из ответов, отметок заданий и разбора разговора при каждом чтении. Так
// они не расходятся с тем, что видит учитель, и не требуют переписывать
// историю, если правило поменяется.

import (
	"slices"
	"time"
)

// Звёзды выдачи. Три независимые: можно получить «Всё сделано» и «Слова в
// разговоре» без «От 80 % верных».
const (
	// StarDone — сделаны все задания выдачи. Есть у любой выдачи.
	StarDone = "done"
	// StarCorrect — от 80 % верных. Есть, если в выдаче есть упражнения.
	StarCorrect = "correct"
	// StarWords — половина целевых выражений урока в разговоре. Есть, если
	// в выдаче есть разговор.
	StarWords = "words"
)

// correctStarPct — доля верных, с которой загорается «От 80 % верных».
const correctStarPct = 80

// Сколько выражений урока ученик употребил в разговоре. Считает не learning,
// а разбор разговора (логика аватара): здесь только его итог.
const (
	WordsNone    = "none"    // разговора не было или он не закончен
	WordsPending = "pending" // разговор закрыт, разбор ещё готовится
	WordsReady   = "ready"   // разбор готов
)

// ConversationWords — засчитанные разбором целевые выражения разговора.
type ConversationWords struct {
	State string
	// Used — сколько целевых выражений засчитал разбор, Total — сколько их
	// было у разговора. Заполнены только у готового разбора.
	Used  int
	Total int
}

// Need — сколько выражений нужно для звезды: половина, с округлением вверх
// (из 10 — 5, из 5 — 3).
func (w ConversationWords) Need() int { return (w.Total + 1) / 2 }

// Earned — звезда «Слова в разговоре» заработана.
func (w ConversationWords) Earned() bool { return w.State == WordsReady && w.Used >= w.Need() }

// Star — звезда выдачи: какая и горит ли.
type Star struct {
	Kind string
	On   bool
}

// IssueTask — задание выдачи и что ученик с ним сделал.
type IssueTask struct {
	Task     LessonTask
	Answered int
	Correct  int
	Done     bool
}

// StudentIssue — выдача глазами ученика.
//
// Выдачи «на уроке» одного дня ученик видит одной. Учитель открывает задания
// по ходу урока — по одному или парами, и каждое «Выдать» — отдельная запись,
// но у ученика это один раздел «Сейчас на уроке» и один итог: четыре итога
// со звёздами за один урок читались бы как четыре урока. Домашняя работа у
// урока одна.
type StudentIssue struct {
	Kind     string
	OpenedAt time.Time
	// DueAt — до какого момента выдача открыта: у работы на уроке — конец
	// дня, у домашней — срок. nil — перенесённое назначение без срока.
	DueAt *time.Time
	// Open — хоть одна запись выдачи принимает ответы; Closed — закрыты все.
	// Ещё не открытая выдача (перенесённое назначение с открытием в
	// будущем) не открыта и не закрыта.
	Open   bool
	Closed bool
	// Tasks — все задания выдачи в порядке урока. Total и Done — сколько из
	// них заданий и сколько сделано: цели урока — вводный экран, не задание,
	// и в счёт не идут (см. countsAsTask).
	Tasks []IssueTask
	Total int
	Done  int
	// Correct и Items — верные и все элементы выданных упражнений.
	// Неотвеченное считается неверным: выдачу могли закрыть раньше, чем
	// ученик всё сделал.
	Correct int
	Items   int
	// BestStreak — самая длинная серия верных ответов подряд в выдаче.
	BestStreak int
	// Words — разговор с аватаром; nil — разговора в выдаче нет.
	Words *ConversationWords
	Stars []Star
}

// Settled — выдача сделана или закрыта: звёзды за неё засчитаны. Пока
// ученик её делает, звёзд у неё нет — они появляются на итоге выдачи.
func (si StudentIssue) Settled() bool { return si.Done >= si.Total || si.Closed }

// countsAsTask — идёт ли задание в счёт «4 задания» у ученика. Цели урока —
// вводный экран перед заданиями, а не задание: «Задание 1 из 4» начинается
// с текста. Выдача из одних целей — исключение: иначе в ней было бы «0
// заданий».
func countsAsTask(t LessonTask, onlyIntro bool) bool { return onlyIntro || t.Kind != TaskObjectives }

// onlyIntro — в списке только цели урока.
func onlyIntro(tasks []LessonTask) bool {
	for _, t := range tasks {
		if t.Kind != TaskObjectives {
			return false
		}
	}
	return true
}

// Pct — доля верных выдачи, в процентах с округлением.
func (si StudentIssue) Pct() int { return percent(si.Correct, si.Items) }

// studentIssues раскладывает выдачи урока по выдачам ученика — в порядке
// выдачи, старые первыми. zone — время школы: «день» работы на уроке
// считается по нему, а не по часам сервера.
func studentIssues(b lessonBoard, w studentWork, words ConversationWords, zone *time.Location) []StudentIssue {
	out := []StudentIssue{}
	group := make([]int, len(b.issues)) // индекс выдачи доски → индекс выдачи ученика
	noDue := []bool{}                   // у выдачи ученика есть запись без срока
	byKey := map[string]int{}
	for i, is := range b.issues {
		key := is.Kind
		if is.Kind == IssueClass {
			key += ":" + is.OpenedAt.In(zone).Format(dueDateLayout)
		}
		g, ok := byKey[key]
		if !ok {
			g = len(out)
			byKey[key] = g
			out = append(out, StudentIssue{Kind: is.Kind, OpenedAt: is.OpenedAt, Closed: true})
			noDue = append(noDue, false)
		}
		group[i] = g
		si := &out[g]
		if is.OpenedAt.Before(si.OpenedAt) {
			si.OpenedAt = is.OpenedAt
		}
		if b.open[i] {
			si.Open = true
		}
		if !b.closed[i] {
			si.Closed = false
		}
		switch {
		case is.DueAt == nil:
			noDue[g] = true
		case si.DueAt == nil || is.DueAt.After(*si.DueAt):
			due := *is.DueAt
			si.DueAt = &due
		}
	}
	for g := range out {
		if noDue[g] {
			out[g].DueAt = nil
		}
	}

	for _, t := range b.tasks {
		i, ok := b.issueOf(t.Key)
		if !ok {
			continue
		}
		si := &out[group[i]]
		st := w.status(t)
		si.Tasks = append(si.Tasks, IssueTask{Task: t, Answered: st.Answered, Correct: st.Correct, Done: st.Done})
		if t.byAnswers() {
			si.Items += len(t.ItemIDs)
			si.Correct += st.Correct
		}
		if t.Kind == TaskDialog && si.Words == nil {
			talk := words
			if talk.State == "" {
				talk.State = WordsNone
			}
			si.Words = &talk
		}
	}
	for g := range out {
		si := &out[g]
		intro := true
		for _, it := range si.Tasks {
			intro = intro && it.Task.Kind == TaskObjectives
		}
		for _, it := range si.Tasks {
			if !countsAsTask(it.Task, intro) {
				continue
			}
			si.Total++
			if it.Done {
				si.Done++
			}
		}
		si.BestStreak = bestStreak(si.Tasks, w)
		si.Stars = starsOf(*si)
	}
	// Выдача, задания которой из урока пропали, ученику ни о чём не говорит,
	// а звезда «Всё сделано» за ноль заданий была бы выдумкой.
	return slices.DeleteFunc(out, func(si StudentIssue) bool { return len(si.Tasks) == 0 })
}

// starsOf — звёзды выдачи. Условия, которого в выдаче нет, нет и среди
// звёзд: выдача из текста и выражений даёт «1 из 1», а не «1 из 3».
func starsOf(si StudentIssue) []Star {
	stars := []Star{{Kind: StarDone, On: si.Total > 0 && si.Done >= si.Total}}
	if si.Items > 0 {
		stars = append(stars, Star{Kind: StarCorrect, On: si.Pct() >= correctStarPct})
	}
	if si.Words != nil {
		stars = append(stars, Star{Kind: StarWords, On: si.Words.Earned()})
	}
	return stars
}

// bestStreak — самая длинная серия верных ответов подряд в упражнениях
// выдачи.
//
// Порядок — по времени ответа. У ответов, данных до того, как время стали
// записывать, его нет; тогда серия идёт по порядку урока: упражнения по
// очереди, вопросы по порядку — так их и проходят.
func bestStreak(tasks []IssueTask, w studentWork) int {
	type mark struct {
		at time.Time
		ok bool
	}
	var marks []mark
	timed := true
	for _, it := range tasks {
		if !it.Task.byAnswers() {
			continue
		}
		prefix := it.Task.ExerciseID.String() + ":"
		for _, id := range it.Task.ItemIDs {
			g, ok := w.answers[prefix+id]
			if !ok {
				continue
			}
			if g.At.IsZero() {
				timed = false
			}
			marks = append(marks, mark{at: g.At, ok: g.Correct})
		}
	}
	if timed {
		slices.SortStableFunc(marks, func(x, y mark) int { return x.at.Compare(y.at) })
	}
	best, run := 0, 0
	for _, m := range marks {
		if !m.ok {
			run = 0
			continue
		}
		run++
		best = max(best, run)
	}
	return best
}

// lessonStars — звёзды урока: сумма звёзд и их максимумов по сделанным и
// закрытым выдачам («⭐ 4 из 5»).
func lessonStars(issues []StudentIssue) (earned, total int) {
	for _, si := range issues {
		if !si.Settled() {
			continue
		}
		for _, s := range si.Stars {
			total++
			if s.On {
				earned++
			}
		}
	}
	return earned, total
}

// lessonPct — доля верных по всем выданным упражнениям урока. nil —
// упражнений не выдавали, и «0 % верных» было бы неправдой.
func lessonPct(issues []StudentIssue) *int {
	ok, n := 0, 0
	for _, si := range issues {
		ok += si.Correct
		n += si.Items
	}
	return percentOrNil(ok, n)
}

// issuedAt — когда по уроку выдали первое задание: дата урока в «Моих
// занятиях» и в «Результатах урока».
func issuedAt(issues []StudentIssue) time.Time {
	var first time.Time
	for _, si := range issues {
		if first.IsZero() || si.OpenedAt.Before(first) {
			first = si.OpenedAt
		}
	}
	return first
}

// sectionIssue — выдача ученика, которую показывает раздел «Моих занятий»:
// для «на уроке» — открытая сегодня, для «на дом» — домашняя. false — такой
// выдачи нет.
func sectionIssue(issues []StudentIssue, kind string) (StudentIssue, bool) {
	for i := len(issues) - 1; i >= 0; i-- {
		si := issues[i]
		if si.Kind != kind {
			continue
		}
		if kind == IssueHome || si.Open {
			return si, true
		}
	}
	return StudentIssue{}, false
}
