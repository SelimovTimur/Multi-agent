package content

import (
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Задания урока — общий язык учителя и ученика: «задание 4» у обоих должно
// значить одно и то же, а ключ задания — жить, пока жива версия урока.

func matching(pos int, title string, phrases ...string) Exercise {
	items := `[`
	for i, p := range phrases {
		if i > 0 {
			items += `,`
		}
		items += `{"phrase":"` + p + `","translation":"t-` + p + `"}`
	}
	items += `]`
	return Exercise{ID: uuid.New(), Type: ExerciseMatching, Position: pos, Title: title,
		Payload: []byte(`{"items":` + items + `}`)}
}

func choice(kind string, pos int, title string, ids ...string) Exercise {
	qs := `[`
	for i, id := range ids {
		if i > 0 {
			qs += `,`
		}
		qs += `{"id":"` + id + `","prompt":"p-` + id + `","choices":[{"id":"a","text":"А"},{"id":"b","text":"Б"}]}`
	}
	qs += `]`
	return Exercise{ID: uuid.New(), Type: kind, Position: pos, Title: title,
		Payload: []byte(`{"questions":` + qs + `}`)}
}

func TestBuildTasksFollowsLessonOrder(t *testing.T) {
	gap := choice(ExerciseGapFill, 2, "", "q1", "q2", "q3", "q4")
	pairs := matching(1, "Подбери пару", "әни", "әти", "апа", "абый", "әби")
	custom := Exercise{ID: uuid.New(), Type: ExerciseCustom, Position: 4,
		Payload: []byte(`{"text":"Памятка","questions":[{"id":"x"}]}`)}
	logic := choice(ExerciseLogical, 3, "Кто кому кем приходится", "l1", "l2")

	tasks := buildTasks(lessonOutline{
		lessonTitle: "Гаилә — Семья", versionTitle: "v1 после методиста",
		objectives: 3, lexicon: 10, grammarTitles: []string{"Притяжательные аффиксы"}, dialog: true,
		// Порядок в выборке не важен: упражнения идут по position.
		exercises: []Exercise{gap, custom, pairs, logic},
	})

	want := []struct {
		key, kind, typ, label, title string
		questions                    int
	}{
		{"objectives", TaskObjectives, "", "Цели", "Цели урока", 0},
		{"text", TaskText, "", "Текст", "Текст «Гаилә — Семья»", 0},
		{"lexicon", TaskLexicon, "", "Лексика", "Лексика: 10 выражений", 0},
		{ExerciseTaskKey(pairs.ID), TaskExercise, ExerciseMatching, "Упражнение", "Подбери пару", 5},
		{ExerciseTaskKey(gap.ID), TaskExercise, ExerciseGapFill, "Упражнение", "Вставь выражение", 4},
		{ExerciseTaskKey(logic.ID), TaskExercise, ExerciseLogical, "Упражнение", "Кто кому кем приходится", 2},
		{ExerciseTaskKey(custom.ID), TaskExercise, ExerciseCustom, "Упражнение", "Свой текст", 0},
		{"grammar", TaskGrammar, "", "Грамматика", "Притяжательные аффиксы", 0},
		{"dialog", TaskDialog, "", "Диалог", "Разговор с аватаром", 0},
	}
	if len(tasks) != len(want) {
		t.Fatalf("заданий %d, ожидали %d: %+v", len(tasks), len(want), tasks)
	}
	for i, w := range want {
		got := tasks[i]
		if got.N != i+1 || got.Key != w.key || got.Kind != w.kind || got.Type != w.typ ||
			got.TypeLabel != w.label || got.Title != w.title || got.Questions != w.questions {
			t.Errorf("задание %d: %+v\nожидали %+v", i+1, got, w)
		}
	}
	if !slices.Equal(tasks[3].ItemIDs, []string{"әни", "әти", "апа", "абый", "әби"}) || tasks[3].ExerciseID != pairs.ID {
		t.Errorf("сопоставление отвечается левыми частями пар: %+v", tasks[3])
	}
	if !slices.Equal(tasks[4].ItemIDs, []string{"q1", "q2", "q3", "q4"}) {
		t.Errorf("тест отвечается идентификаторами вопросов: %v", tasks[4].ItemIDs)
	}
	// «Свой текст» — памятка: даже случайные вопросы в нём не считаются.
	if tasks[6].ItemIDs != nil {
		t.Errorf("у «своего текста» вопросов нет: %v", tasks[6].ItemIDs)
	}
}

// Пустые блоки урока заданиями не становятся: текст есть всегда, остальное —
// только если есть что показать.
func TestBuildTasksSkipsEmptyBlocks(t *testing.T) {
	pairs := matching(1, "", "әни")
	tasks := buildTasks(lessonOutline{versionTitle: "Мой день", exercises: []Exercise{pairs}})
	if len(tasks) != 2 || tasks[0].Key != TaskText || tasks[1].Key != ExerciseTaskKey(pairs.ID) {
		t.Fatalf("урок без целей, лексики, грамматики и диалога: %+v", tasks)
	}
	// Названия урока нет — берём название версии; пустое упражнение — по виду.
	if tasks[0].Title != "Текст «Мой день»" || tasks[1].Title != "Найди пары" || tasks[1].N != 2 {
		t.Errorf("заголовки: %q, %q", tasks[0].Title, tasks[1].Title)
	}
	if got := buildTasks(lessonOutline{})[0].Title; got != "Текст урока" {
		t.Errorf("урок совсем без названия: %q", got)
	}
}

func TestTaskTitlesDecline(t *testing.T) {
	cases := []struct {
		lexicon int
		want    string
	}{
		{1, "Лексика: 1 выражение"},
		{3, "Лексика: 3 выражения"},
		{5, "Лексика: 5 выражений"},
		{11, "Лексика: 11 выражений"},
		{21, "Лексика: 21 выражение"},
		{22, "Лексика: 22 выражения"},
	}
	for _, c := range cases {
		tasks := buildTasks(lessonOutline{lexicon: c.lexicon})
		if tasks[1].Title != c.want {
			t.Errorf("%d выражений: %q, ожидали %q", c.lexicon, tasks[1].Title, c.want)
		}
	}
	two := buildTasks(lessonOutline{grammarTitles: []string{"Падежи", "Аффиксы"}})
	if got := two[len(two)-1].Title; got != "Грамматика: 2 темы" {
		t.Errorf("несколько тем: %q", got)
	}
	five := buildTasks(lessonOutline{grammarTitles: []string{"1", "2", "3", "4", "5"}})
	if got := five[len(five)-1].Title; got != "Грамматика: 5 тем" {
		t.Errorf("пять тем: %q", got)
	}
	for kind, want := range map[string]string{
		ExerciseMatching: "Найди пары", ExerciseGapFill: "Вставь выражение",
		ExerciseLogical: "Логическое продолжение", ExerciseCustom: "Свой текст", ExerciseGrammar: "Упражнение",
	} {
		if got := exerciseTaskTitle(Exercise{Type: kind, Title: "  "}); got != want {
			t.Errorf("%s без названия: %q, ожидали %q", kind, got, want)
		}
	}
}

// Пункты не той формы не роняют список заданий: число вопросов считается
// как в QuestionCount, а отвечать можно только на пункты с идентификатором.
func TestTaskPayloadIsLenient(t *testing.T) {
	p := parseTaskPayload([]byte(`{"items":[{"phrase":"әни"},"мусор",{"phrase":"әни"},{"phrase":""}],
		"questions":[{"id":"q1"},{"prompt":"без id"}]}`))
	if p.count != 6 {
		t.Errorf("вопросов %d, ожидали 6 — как в QuestionCount", p.count)
	}
	if got := p.itemIDs(); !slices.Equal(got, []string{"әни", "q1"}) {
		t.Errorf("ответить можно только на пункты с идентификатором: %v", got)
	}
	if p := parseTaskPayload([]byte(`не json`)); p.count != 0 || len(p.itemIDs()) != 0 {
		t.Errorf("неразборчивое содержимое — ноль вопросов: %+v", p)
	}
}

// Разбор для учителя — словами: вариант теста приходит идентификатором, а
// учитель должен видеть сам вариант.
func TestReviewExerciseInWords(t *testing.T) {
	gap := choice(ExerciseGapFill, 1, "", "q1", "q2")
	gap.AnswerKey = []byte(`{"q1":"b","q2":"z"}`)
	r := reviewExercise(gap)
	if len(r.Questions) != 2 || r.ExerciseID != gap.ID {
		t.Fatalf("вопросы теста: %+v", r)
	}
	if q := r.Questions[0]; q.ItemID != "q1" || q.Prompt != "p-q1" || q.Expected != "Б" || q.Choices["a"] != "А" {
		t.Errorf("верный ответ словами: %+v", q)
	}
	// В ключе не вариант, а текст — отдаём как есть.
	if r.Questions[1].Expected != "z" {
		t.Errorf("ключ не по вариантам: %+v", r.Questions[1])
	}

	pairs := matching(1, "", "әни", "әти")
	pairs.AnswerKey = []byte(`{"әни":"мама"}`)
	r = reviewExercise(pairs)
	if r.Questions[0].Prompt != "әни" || r.Questions[0].Expected != "мама" {
		t.Errorf("пара: левая часть и верный перевод: %+v", r.Questions[0])
	}
	// Ключа на пару нет — верный перевод есть в самой паре.
	if r.Questions[1].Expected != "t-әти" {
		t.Errorf("пара без ключа: %+v", r.Questions[1])
	}

	if r := reviewExercise(Exercise{Type: ExerciseCustom, Payload: []byte(`{"text":"x"}`)}); len(r.Questions) != 0 {
		t.Errorf("у «своего текста» вопросов нет: %+v", r)
	}
}

// Кеш ограничен сверху и не отдаёт устаревшее и чужое (И-1, И-5).
func TestVersionCacheIsBounded(t *testing.T) {
	now := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	c := newVersionCache[int](3)
	c.now = func() time.Time { return now }
	org, other := uuid.New(), uuid.New()
	ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New(), uuid.New()}

	for i, id := range ids[:3] {
		c.put(org, id, i, time.Minute)
	}
	// Обращение освежает запись: вытеснена будет самая давняя — вторая.
	if v, ok := c.get(org, ids[0]); !ok || v != 0 {
		t.Fatal("свежая запись должна находиться")
	}
	c.put(org, ids[3], 3, time.Minute)
	if c.len() != 3 {
		t.Errorf("в кеше %d записей при пределе 3", c.len())
	}
	if _, ok := c.get(org, ids[1]); ok {
		t.Error("самая давняя запись должна быть вытеснена")
	}
	if _, ok := c.get(other, ids[0]); ok {
		t.Error("запись другого тенанта не отдаётся")
	}
	now = now.Add(2 * time.Minute)
	if _, ok := c.get(org, ids[3]); ok {
		t.Error("устаревшая запись не отдаётся")
	}

	// Кешируется только неизменяемое: готовую версию ещё правят, и её задания
	// читаются из базы каждый раз.
	if versionTTL(VersionPublished) <= 0 {
		t.Error("опубликованная версия кешируется")
	}
	for _, status := range []string{VersionDraft, VersionReady, VersionArchived} {
		if versionTTL(status) != 0 {
			t.Errorf("версия «%s» правится — в кеш не кладётся", status)
		}
	}
	c.put(org, ids[0], 42, versionTTL(VersionReady))
	if v, _ := c.get(org, ids[0]); v == 42 {
		t.Error("запись с нулевым сроком не должна попадать в кеш")
	}
}
