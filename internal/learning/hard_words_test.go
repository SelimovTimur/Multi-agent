package learning

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

// Трудное слово — это совет учителю, что повторять со всем классом. Ложное
// «трудное» тратит урок, пропущенное — оставляет класс с ошибкой.

// Оба порога сразу: ошиблись не меньше четверти тех, кому слово встречалось
// (и минимум двое), и неверны не меньше 30 % ответов со словом.
func TestIsHardThresholds(t *testing.T) {
	cases := []struct {
		name                        string
		met, erred, answers, errors int
		want                        bool
	}{
		// Маленький класс: доля легко набирается одним учеником, поэтому
		// нужен второй.
		{"маленький класс, ошибся один из трёх", 3, 1, 6, 3, false},
		{"маленький класс, ошиблись двое из четырёх", 4, 2, 8, 3, true},
		{"двое из восьми — ровно четверть", 8, 2, 16, 5, true},
		// Большой класс: двое — это уже не четверть.
		{"большой класс, двое из тридцати", 30, 2, 60, 30, false},
		{"большой класс, семеро из тридцати — меньше четверти", 30, 7, 60, 30, false},
		{"большой класс, восьмеро из тридцати", 30, 8, 60, 18, true},
		// Ошибались многие, но по разу на фоне множества верных ответов.
		{"неверных меньше 30 %", 30, 10, 60, 17, false},
		{"неверных ровно 30 %", 30, 10, 60, 18, true},
		{"никто не встречал", 0, 0, 0, 0, false},
	}
	for _, c := range cases {
		if got := isHard(c.met, c.erred, c.answers, c.errors); got != c.want {
			t.Errorf("%s: получили %v, ждали %v", c.name, got, c.want)
		}
	}
}

// Слово находится в тексте и в чужой форме: татарский приклеивает окончания.
func TestWordMentions(t *testing.T) {
	cases := []struct {
		text, word string
		want       bool
	}{
		{"Минем күршем бик яхшы", "күрше", true},
		{"Китабым өстәлдә", "китап", true},
		{"Энекәш", "энекәш", true},
		{"Мин Казанда яшим.", "Мин … яшим", true},
		{"Рәсем укыйм", "рәсем ясыйм", false},
		// Короткое слово — только целиком.
		{"Улым мәктәптә", "ул", false},
		{"Ул мәктәптә", "ул", true},
		// Другая буква — другое слово.
		{"Курше", "күрше", false},
	}
	for _, c := range cases {
		if got := mentions(wordTokens(c.text), wordTokens(c.word)); got != c.want {
			t.Errorf("«%s» в «%s»: получили %v, ждали %v", c.word, c.text, got, c.want)
		}
	}
}

// Одинаковые ответы — без регистра, пробелов и знаков, но а и ә — разные.
func TestSameAnswer(t *testing.T) {
	if sameAnswer("Старший брат!") != sameAnswer(" старший  брат") {
		t.Error("регистр, пробелы и знаки не должны различать ответы")
	}
	if sameAnswer("бала") == sameAnswer("бәлә") {
		t.Error("а и ә — разные буквы, ответы разные")
	}
	if sameAnswer("сенел") == sameAnswer("сеңел") {
		t.Error("н и ң — разные буквы, ответы разные")
	}
}

// Урок «Моя семья»: сопоставление и пропуски, у одного ученика — разговор.
// Слово «энекәш» трудное: в заданиях и в разговоре; «әти» — нет.
func TestClassHardWords(t *testing.T) {
	match, gaps := uuid.New(), uuid.New()
	tasks := []LessonTask{
		{Key: ExerciseTaskKey(match), Kind: TaskExercise, Title: "Сопоставь членов семьи", Type: "matching",
			ExerciseID: match, ItemIDs: []string{"энекәш", "әти"}},
		{Key: ExerciseTaskKey(gaps), Kind: TaskExercise, Title: "Вставь слово", Type: "gap_fill",
			ExerciseID: gaps, ItemIDs: []string{"q1"}},
		{Key: "dialog", Kind: TaskDialog, Title: "Диалог"},
	}
	reviews := map[uuid.UUID]ExerciseReview{
		match: {Questions: []QuestionReview{
			{ItemID: "энекәш", Prompt: "энекәш", Expected: "младший брат"},
			{ItemID: "әти", Prompt: "әти", Expected: "папа"},
		}},
		// Слово — в верном ответе, а не в вопросе.
		gaps: {Questions: []QuestionReview{
			{ItemID: "q1", Prompt: "Минем ___ мәктәпкә йөри.", Expected: "энекәшем"},
		}},
	}
	aidar, leysan, damir, alsu, left := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	members := []ClassMember{
		{ID: aidar, DisplayName: "Айдар"}, {ID: leysan, DisplayName: "Лейсан"},
		{ID: damir, DisplayName: "Дамир"}, {ID: alsu, DisplayName: "Алсу"},
	}
	ans := func(brother string, okBrother bool, dad string, okDad bool, gap string, okGap bool) map[string]GivenAnswer {
		m, g := match.String()+":", gaps.String()+":"
		return map[string]GivenAnswer{
			m + "энекәш": {Answer: brother, Correct: okBrother},
			m + "әти":    {Answer: dad, Correct: okDad},
			g + "q1":     {Answer: gap, Correct: okGap},
		}
	}
	attempts := []Attempt{
		{StudentID: aidar, Answers: ans("младший брат", true, "папа", true, "энекәшем", true)},
		{StudentID: leysan, Answers: ans("старший брат!", false, "папа", true, "энекәшем", true)},
		{StudentID: damir, Answers: ans("Старший брат", false, "мама", false, "сеңелем", false)},
		{StudentID: alsu, Answers: ans("младший брат", true, "папа", true, "энекәшем", true)},
		// Ушедший из класса ошибся везде, но не считается.
		{StudentID: left, Answers: ans("старший брат", false, "мама", false, "абыем", false)},
	}

	// Айдар в разговоре сказал «кече брат» вместо «энекәш»; Алсу слово не
	// использовала — это промах, а не ошибка.
	report := func(goals []AnalysisGoal, marks []GoalMark, turns []ReportTurn) []byte {
		analysis, _ := json.Marshal(Analysis{Goals: goals, Marks: marks})
		raw, _ := json.Marshal(reportSnapshot{Version: reportVersion, Turns: turns, Analysis: analysis})
		return raw
	}
	convs := []ConversationRow{
		{StudentID: aidar, ReportStatus: ReportReady, Report: report(
			[]AnalysisGoal{{Key: "p1", Kind: GoalPhrase, Title: "энекәш", CorrectUses: 1, Errors: 1}},
			[]GoalMark{{TurnNo: 2, GoalKey: "p1", Verdict: VerdictError, Quote: "кече брат", CorrectForm: "энекәшем"}},
			[]ReportTurn{{TurnNo: 2, Role: RoleStudent, Content: "Минем кече брат бар"}})},
		{StudentID: alsu, ReportStatus: ReportReady, Report: report(
			[]AnalysisGoal{{Key: "p1", Kind: GoalPhrase, Title: "энекәш", Misses: 1}},
			[]GoalMark{{TurnNo: 3, GoalKey: "p1", Verdict: VerdictMiss}},
			[]ReportTurn{{TurnNo: 3, Role: RoleStudent, Content: "Әйе"}})},
	}

	a := Assignment{ID: uuid.New()}
	got := classHardWords(members, []wordLesson{{
		assignment: a, title: "Минем гаиләм", tasks: tasks, reviews: reviews, attempts: attempts,
		words: []LessonWord{{Phrase: "энекәш", Translation: "младший брат"}, {Phrase: "әти", Translation: "папа"}},
		goals: []AvatarGoal{{Key: "p1", Kind: GoalPhrase, Title: "энекәш"}},
		convs: convs,
	}})

	if len(got) != 1 {
		t.Fatalf("трудное слово одно — энекәш, получили %+v", got)
	}
	w := got[0]
	if w.Word.Phrase != "энекәш" || w.AssignmentID != a.ID || w.LessonTitle != "Минем гаиләм" {
		t.Errorf("не то слово: %+v", w)
	}
	// Задания: 8 ответов со словом (по два у четверых), неверных 3.
	// Разговор: у Айдара одно верное употребление и одна ошибка.
	if w.Tasks.Answers != 8 || w.Tasks.Errors != 3 || w.Tasks.Erred != 2 {
		t.Errorf("задания: 8 ответов, 3 неверных у двоих; получили %+v", w.Tasks)
	}
	if w.Conversation.Answers != 2 || w.Conversation.Errors != 1 || w.Conversation.Students != 1 {
		t.Errorf("разговор: 2 употребления, 1 ошибка у одного; получили %+v", w.Conversation)
	}
	if w.Errors != 4 || w.Answers != 10 || w.Met != 4 || len(w.Struggled) != 3 {
		t.Errorf("4 ошибки из 10 ответов, встречалось четверым, ошиблись трое; получили %+v", w)
	}
	// Карточка ученика: у Дамира две ошибки в заданиях, у Айдара — одна в
	// разговоре, у Лейсан — одна в заданиях.
	if w.StudentErrors[damir] != 2 || w.StudentErrors[aidar] != 1 || w.StudentErrors[leysan] != 1 || len(w.StudentErrors) != 3 {
		t.Errorf("ошибки по ученикам: Дамир 2, Айдар 1, Лейсан 1; получили %v", w.StudentErrors)
	}
	// «Кому повторить» — по имени.
	if len(w.Struggled) == 3 {
		names := [3]string{w.Struggled[0].DisplayName, w.Struggled[1].DisplayName, w.Struggled[2].DisplayName}
		if names != [3]string{"Айдар", "Дамир", "Лейсан"} {
			t.Errorf("ошиблись Айдар, Дамир, Лейсан — по имени; получили %v", names)
		}
	}

	if len(w.Written) != 2 {
		t.Fatalf("две разные письменные ошибки, получили %+v", w.Written)
	}
	top := w.Written[0]
	if top.Answer != "Старший брат" || top.Count != 2 || len(top.Students) != 2 ||
		top.TaskType != "matching" || top.Prompt != "энекәш" || top.Expected != "младший брат" {
		t.Errorf("«Старший брат» и «старший брат!» — одна ошибка у двоих: %+v", top)
	}
	if second := w.Written[1]; second.Answer != "сеңелем" || second.TaskTitle != "Вставь слово" || second.Count != 1 {
		t.Errorf("вторая ошибка — «сеңелем» в пропусках: %+v", second)
	}
	if len(w.Spoken) != 1 || w.Spoken[0].Member.ID != aidar || w.Spoken[0].Quote != "кече брат" ||
		w.Spoken[0].TurnNo != 2 || w.Spoken[0].CorrectForm != "энекәшем" {
		t.Errorf("ошибка в разговоре — цитата Айдара: %+v", w.Spoken)
	}
	if len(w.Confusions) != 2 || w.Confusions[0] != "Старший брат" || w.Confusions[1] != "сеңелем" {
		t.Errorf("путают со «старший брат» и «сеңелем»: %v", w.Confusions)
	}
}

// Больше ошибок — выше; при равенстве — больше учеников.
func TestClassHardWordsOrder(t *testing.T) {
	ex := uuid.New()
	var members []ClassMember
	for range 4 {
		members = append(members, ClassMember{ID: uuid.New(), DisplayName: "Ученик"})
	}
	task := LessonTask{Key: ExerciseTaskKey(ex), Kind: TaskExercise, ExerciseID: ex}
	review := ExerciseReview{Questions: []QuestionReview{
		{ItemID: "a1", Prompt: "алма"}, {ItemID: "a2", Prompt: "алма"},
		{ItemID: "k1", Prompt: "кәбестә"}, {ItemID: "k2", Prompt: "кәбестә"},
		{ItemID: "y1", Prompt: "йорт"}, {ItemID: "y2", Prompt: "йорт"},
	}}
	p := ex.String() + ":"
	wrong := GivenAnswer{Answer: "x"}
	// алма: 4 ошибки у двоих; кәбестә: 4 ошибки у четверых; йорт: 2 у двоих.
	answers := []map[string]GivenAnswer{
		{p + "a1": wrong, p + "a2": wrong, p + "k1": wrong, p + "y1": wrong},
		{p + "a1": wrong, p + "a2": wrong, p + "k1": wrong, p + "y1": wrong},
		{p + "k1": wrong},
		{p + "k1": wrong},
	}
	var attempts []Attempt
	for i, m := range members {
		attempts = append(attempts, Attempt{StudentID: m.ID, Answers: answers[i]})
	}
	got := classHardWords(members, []wordLesson{{
		assignment: Assignment{ID: uuid.New()}, tasks: []LessonTask{task},
		reviews: map[uuid.UUID]ExerciseReview{ex: review}, attempts: attempts,
		words: []LessonWord{{Phrase: "йорт"}, {Phrase: "алма"}, {Phrase: "кәбестә"}},
	}})
	if len(got) != 3 {
		t.Fatalf("трудных три, получили %d", len(got))
	}
	order := []string{got[0].Word.Phrase, got[1].Word.Phrase, got[2].Word.Phrase}
	if order[0] != "кәбестә" || order[1] != "алма" || order[2] != "йорт" {
		t.Errorf("порядок кәбестә, алма, йорт; получили %v", order)
	}
}
