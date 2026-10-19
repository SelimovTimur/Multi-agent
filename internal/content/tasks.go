package content

// Задания урока — шаги, которые учитель выдаёт классу по одному («Урок в
// классе», docs/backend/class-lessons-api.md).
//
// Из чего урок состоит, в каком порядке его проходят и как называются шаги,
// знает только content. learning хранит ключи выданного и ответы, но в схему
// content не ходит, поэтому список заданий он получает отсюда, через порт.

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/NATIVE/NATIVE/internal/platform/httpx"
	"github.com/NATIVE/NATIVE/internal/platform/pg"
	"github.com/google/uuid"
)

// Виды заданий. Порядок констант — порядок урока.
const (
	TaskObjectives = "objectives"
	TaskText       = "text"
	TaskLexicon    = "lexicon"
	TaskExercise   = "exercise"
	TaskGrammar    = "grammar"
	TaskDialog     = "dialog"
)

// ExerciseTaskPrefix — ключ задания-упражнения: «ex:» и идентификатор
// упражнения. Идентификатор, а не номер: номер у правленой версии другой, а
// ответы ученика уже записаны по идентификатору.
const ExerciseTaskPrefix = "ex:"

// ExerciseTaskKey — ключ задания упражнения.
func ExerciseTaskKey(exerciseID uuid.UUID) string { return ExerciseTaskPrefix + exerciseID.String() }

// LessonTask — задание урока.
type LessonTask struct {
	// Key стабилен в пределах версии урока: по нему learning помнит, что
	// выдано и что сделано.
	Key string
	// N — номер задания в порядке урока, с единицы.
	N    int
	Kind string
	// Type — вид упражнения (matching, gap_fill, logical, custom); у
	// остальных заданий пусто.
	Type      string
	TypeLabel string
	Title     string
	// Questions — сколько вопросов засчитывается ответами. У заданий без
	// проверки (текст, лексика, свой текст, грамматика) — ноль: они
	// засчитываются кнопкой «Дальше».
	Questions int
	// ExerciseID — только у упражнений.
	ExerciseID uuid.UUID
	// ItemIDs — чем ученик отвечает на вопросы упражнения: идентификатор
	// вопроса или левая часть пары. По ним learning считает, на сколько
	// вопросов ответ дан: ответ на несуществующий вопрос не должен
	// засчитывать упражнение.
	ItemIDs []string
}

// lessonOutline — то, из чего собираются задания версии. Без текстов и без
// ключа ответов: списку заданий они не нужны.
type lessonOutline struct {
	versionID     uuid.UUID
	status        string
	lessonTitle   string
	versionTitle  string
	objectives    int
	lexicon       int
	grammarTitles []string
	dialog        bool
	exercises     []Exercise
}

// buildTasks раскладывает версию на задания в порядке прохождения урока:
// цели, текст, лексика, упражнения по position, грамматика, диалог.
//
// Порядок тот же, в котором ученик проходит урок, — по нему же нумеруются
// задания («задание 4»), и учитель с учеником должны видеть одни номера.
func buildTasks(o lessonOutline) []LessonTask {
	out := make([]LessonTask, 0, len(o.exercises)+5)
	add := func(t LessonTask) {
		t.N = len(out) + 1
		out = append(out, t)
	}

	if o.objectives > 0 {
		add(LessonTask{Key: TaskObjectives, Kind: TaskObjectives, TypeLabel: "Цели", Title: "Цели урока"})
	}
	add(LessonTask{Key: TaskText, Kind: TaskText, TypeLabel: "Текст", Title: textTaskTitle(o)})
	if o.lexicon > 0 {
		add(LessonTask{Key: TaskLexicon, Kind: TaskLexicon, TypeLabel: "Лексика",
			Title: fmt.Sprintf("Лексика: %d %s", o.lexicon,
				plural(o.lexicon, "выражение", "выражения", "выражений"))})
	}

	exercises := slices.Clone(o.exercises)
	slices.SortStableFunc(exercises, func(a, b Exercise) int { return a.Position - b.Position })
	for _, e := range exercises {
		t := LessonTask{
			Key: ExerciseTaskKey(e.ID), Kind: TaskExercise, Type: e.Type,
			TypeLabel: "Упражнение", Title: exerciseTaskTitle(e), ExerciseID: e.ID,
		}
		// «Свой текст» — памятка учителя: отвечать в нём нечего, даже если в
		// payload случайно оказались вопросы.
		if e.Type != ExerciseCustom {
			p := parseTaskPayload(e.Payload)
			t.Questions = p.count
			t.ItemIDs = p.itemIDs()
		}
		add(t)
	}

	if n := len(o.grammarTitles); n > 0 {
		title := strings.TrimSpace(o.grammarTitles[0])
		if n > 1 || title == "" {
			title = fmt.Sprintf("Грамматика: %d %s", n, plural(n, "тема", "темы", "тем"))
		}
		// Тренировка в грамматике — для разбора, а не для оценки: вопросов у
		// задания нет, оно засчитывается кнопкой «Дальше».
		add(LessonTask{Key: TaskGrammar, Kind: TaskGrammar, TypeLabel: "Грамматика", Title: title})
	}
	if o.dialog {
		add(LessonTask{Key: TaskDialog, Kind: TaskDialog, TypeLabel: "Диалог", Title: "Разговор с аватаром"})
	}
	return out
}

// textTaskTitle — «Текст «Гаилә»». Название урока, а не версии: название
// версии — служебная пометка учителя («после замечаний методиста»).
func textTaskTitle(o lessonOutline) string {
	name := strings.TrimSpace(o.lessonTitle)
	if name == "" {
		name = strings.TrimSpace(o.versionTitle)
	}
	if name == "" {
		return "Текст урока"
	}
	return "Текст «" + name + "»"
}

// exerciseTaskTitle — название упражнения, а без него — название вида.
func exerciseTaskTitle(e Exercise) string {
	if title := strings.TrimSpace(e.Title); title != "" {
		return title
	}
	switch e.Type {
	case ExerciseMatching:
		return "Найди пары"
	case ExerciseGapFill:
		return "Вставь выражение"
	case ExerciseLogical:
		return "Логическое продолжение"
	case ExerciseCustom:
		return "Свой текст"
	}
	return "Упражнение"
}

// plural выбирает форму слова по числу: 1 выражение, 3 выражения, 7 выражений.
func plural(n int, one, few, many string) string {
	if n < 0 {
		n = -n
	}
	n10, n100 := n%10, n%100
	switch {
	case n10 == 1 && n100 != 11:
		return one
	case n10 >= 2 && n10 <= 4 && (n100 < 12 || n100 > 14):
		return few
	}
	return many
}

// taskPayload — содержимое упражнения глазами списка заданий.
//
// Не exercisePayload из печати урока: там разбор целиком, и один пункт не
// той формы обнуляет всё задание. Здесь — по элементу, потому что от числа
// пунктов зависит, засчитается ли упражнение ученику, а в старых уроках
// встречаются пункты не той формы.
type taskPayload struct {
	// count — пункты items плюс вопросы questions, как в QuestionCount.
	count     int
	items     []payloadItem
	questions []payloadQuestion
}

type payloadItem struct {
	Phrase      string `json:"phrase"`
	Translation string `json:"translation"`
}

type payloadQuestion struct {
	ID      string `json:"id"`
	Prompt  string `json:"prompt"`
	Choices []struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	} `json:"choices"`
}

func parseTaskPayload(raw []byte) taskPayload {
	var arrays struct {
		Items     []json.RawMessage `json:"items"`
		Questions []json.RawMessage `json:"questions"`
	}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &arrays)
	}
	out := taskPayload{count: len(arrays.Items) + len(arrays.Questions)}
	for _, it := range arrays.Items {
		var item payloadItem
		_ = json.Unmarshal(it, &item)
		out.items = append(out.items, item)
	}
	for _, q := range arrays.Questions {
		var question payloadQuestion
		_ = json.Unmarshal(q, &question)
		out.questions = append(out.questions, question)
	}
	return out
}

// itemIDs — чем ученик отвечает на вопросы, без пустых и повторов.
//
// Сопоставление отвечается левой частью пары, тесты — идентификатором
// вопроса (так их шлёт экран ученика и так их хранит learning.answerKey).
func (p taskPayload) itemIDs() []string {
	seen := make(map[string]bool, p.count)
	out := make([]string, 0, p.count)
	add := func(id string) {
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		out = append(out, id)
	}
	for _, it := range p.items {
		add(it.Phrase)
	}
	for _, q := range p.questions {
		add(q.ID)
	}
	return out
}

// LessonTasks — задания версий урока в порядке прохождения, пачкой.
//
// Пачкой, потому что вкладка «Уроки» класса и «Мои занятия» ученика
// показывают «выдано N из 8» в каждой строке. Версии, которой нет в
// организации, в ответе нет.
func (s *Service) LessonTasks(ctx context.Context, orgID uuid.UUID,
	versionIDs []uuid.UUID) (map[uuid.UUID][]LessonTask, error) {

	out := make(map[uuid.UUID][]LessonTask, len(versionIDs))
	var missing []uuid.UUID
	for _, id := range versionIDs {
		if _, done := out[id]; done || slices.Contains(missing, id) {
			continue
		}
		if tasks, ok := s.tasks.get(orgID, id); ok {
			out[id] = tasks
			continue
		}
		missing = append(missing, id)
	}
	if len(missing) == 0 {
		return out, nil
	}

	outlines, err := s.store.LessonOutlines(ctx, s.pool, orgID, missing)
	if err != nil {
		return nil, httpx.Internal("Не удалось прочитать задания урока").Wrap(err)
	}
	for _, o := range outlines {
		tasks := buildTasks(o)
		s.tasks.put(orgID, o.versionID, tasks, versionTTL(o.status))
		out[o.versionID] = tasks
	}
	return out, nil
}

// ExerciseReview — упражнение глазами учителя: вопросы с текстами вариантов
// и верными ответами.
//
// Только для экранов учителя: в ответ ученику это не уходит никогда, потому
// что здесь ключ ответов, переведённый в текст.
type ExerciseReview struct {
	ExerciseID uuid.UUID
	Questions  []QuestionReview
}

// QuestionReview — вопрос упражнения.
type QuestionReview struct {
	// ItemID — чем ученик на него отвечает (см. LessonTask.ItemIDs).
	ItemID string
	// Prompt — текст вопроса; у сопоставления — левая часть пары.
	Prompt string
	// Expected — верный ответ текстом, а не идентификатором варианта.
	Expected string
	// Choices — варианты: идентификатор → текст. Ответ ученика на тест —
	// идентификатор варианта, и учителю его надо показать словами.
	Choices map[string]string
}

// ExerciseReviews — разбор упражнений версии для учителя, по упражнению.
//
// Ключ ответов читается здесь же, в content, и наружу уходит только текстом
// верного ответа на каждый вопрос — тем же, что ученик видит после своего
// ответа.
func (s *Service) ExerciseReviews(ctx context.Context, orgID, versionID uuid.UUID) (map[uuid.UUID]ExerciseReview, error) {
	if cached, ok := s.reviews.get(orgID, versionID); ok {
		return cached, nil
	}
	status, items, err := s.store.ExercisesWithKeys(ctx, s.pool, orgID, versionID)
	if err != nil {
		return nil, httpx.Internal("Не удалось прочитать упражнения урока").Wrap(err)
	}
	out := make(map[uuid.UUID]ExerciseReview, len(items))
	for _, e := range items {
		out[e.ID] = reviewExercise(e)
	}
	if status != "" {
		s.reviews.put(orgID, versionID, out, versionTTL(status))
	}
	return out, nil
}

// reviewExercise переводит упражнение с ключом в вопросы с текстами.
func reviewExercise(e Exercise) ExerciseReview {
	out := ExerciseReview{ExerciseID: e.ID}
	if e.Type == ExerciseCustom {
		return out
	}
	// Тот же мягкий разбор, что и в CheckAnswer: ключ бывает числом, и
	// неразобранное считается отсутствующим.
	key := parseAnswerKey(e.AnswerKey)

	p := parseTaskPayload(e.Payload)
	for _, it := range p.items {
		if it.Phrase == "" {
			continue
		}
		expected, ok := key[it.Phrase]
		if !ok {
			expected = it.Translation
		}
		out.Questions = append(out.Questions, QuestionReview{
			ItemID: it.Phrase, Prompt: it.Phrase, Expected: expected,
		})
	}
	for _, q := range p.questions {
		if q.ID == "" {
			continue
		}
		choices := make(map[string]string, len(q.Choices))
		for _, c := range q.Choices {
			choices[c.ID] = c.Text
		}
		expected := key[q.ID]
		if text, ok := choices[expected]; ok {
			expected = text
		}
		out.Questions = append(out.Questions, QuestionReview{
			ItemID: q.ID, Prompt: q.Prompt, Expected: expected, Choices: choices,
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// Хранилище
// ---------------------------------------------------------------------------

// LessonOutlines читает всё, из чего собираются задания версий: два запроса
// на любой список версий.
//
// Счётчики и названия тем — подзапросами по индексам (lesson_version_id,
// position), а не выборкой строк: списку заданий тексты выражений и
// разборы не нужны.
func (s *Store) LessonOutlines(ctx context.Context, q pg.Querier, orgID uuid.UUID,
	versionIDs []uuid.UUID) ([]lessonOutline, error) {
	if len(versionIDs) == 0 {
		return nil, nil
	}
	rows, err := q.Query(ctx, `
		SELECT v.id, v.status, v.title, COALESCE(l.title, ''),
		       (SELECT count(*) FROM content.learning_objectives o WHERE o.lesson_version_id = v.id),
		       (SELECT count(*) FROM content.lexical_units u WHERE u.lesson_version_id = v.id),
		       COALESCE((SELECT array_agg(g.title ORDER BY g.position)
		                 FROM content.grammar_topics g WHERE g.lesson_version_id = v.id), '{}'),
		       -- Нет строки настройки — разговор в уроке есть (DefaultAvatarConfig).
		       COALESCE((SELECT a.enabled FROM content.avatar_configs a
		                 WHERE a.lesson_version_id = v.id), true)
		FROM content.lesson_versions v
		LEFT JOIN content.lessons l ON l.id = v.lesson_id AND l.org_id = v.org_id
		WHERE v.org_id = $1 AND v.id = ANY($2)`, orgID, versionIDs)
	if err != nil {
		return nil, fmt.Errorf("content: не удалось прочитать состав уроков: %w", err)
	}
	var out []lessonOutline
	index := map[uuid.UUID]int{}
	for rows.Next() {
		var o lessonOutline
		var objectives, lexicon int64
		if err := rows.Scan(&o.versionID, &o.status, &o.versionTitle, &o.lessonTitle,
			&objectives, &lexicon, &o.grammarTitles, &o.dialog); err != nil {
			rows.Close()
			return nil, fmt.Errorf("content: не удалось прочитать состав уроков: %w", err)
		}
		o.objectives, o.lexicon = int(objectives), int(lexicon)
		index[o.versionID] = len(out)
		out = append(out, o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("content: не удалось прочитать состав уроков: %w", err)
	}
	if len(out) == 0 {
		return nil, nil
	}

	// Упражнения — без ключа ответов: заданиям он не нужен, а невыбранное
	// из базы утечь не может.
	rows, err = q.Query(ctx, `
		SELECT lesson_version_id, id, type, position, title, payload
		FROM content.exercises
		WHERE org_id = $1 AND lesson_version_id = ANY($2)
		ORDER BY lesson_version_id, position`, orgID, versionIDs)
	if err != nil {
		return nil, fmt.Errorf("content: не удалось прочитать упражнения уроков: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var versionID uuid.UUID
		var e Exercise
		if err := rows.Scan(&versionID, &e.ID, &e.Type, &e.Position, &e.Title, &e.Payload); err != nil {
			return nil, fmt.Errorf("content: не удалось прочитать упражнения уроков: %w", err)
		}
		if i, ok := index[versionID]; ok {
			out[i].exercises = append(out[i].exercises, e)
		}
	}
	return out, rows.Err()
}

// ExercisesWithKeys — упражнения версии вместе с ключом ответов и статус
// версии. Только для разбора учителю; пустой статус — версии нет.
func (s *Store) ExercisesWithKeys(ctx context.Context, q pg.Querier, orgID,
	versionID uuid.UUID) (string, []Exercise, error) {
	var status string
	err := q.QueryRow(ctx,
		`SELECT status FROM content.lesson_versions WHERE id = $1 AND org_id = $2`,
		versionID, orgID).Scan(&status)
	if pg.IsNoRows(err) {
		return "", nil, nil
	}
	if err != nil {
		return "", nil, fmt.Errorf("content: не удалось прочитать версию: %w", err)
	}
	rows, err := q.Query(ctx, `
		SELECT id, type, position, title, instruction, payload, answer_key, raw_source
		FROM content.exercises
		WHERE org_id = $1 AND lesson_version_id = $2
		ORDER BY position`, orgID, versionID)
	if err != nil {
		return "", nil, fmt.Errorf("content: не удалось прочитать упражнения: %w", err)
	}
	defer rows.Close()
	var out []Exercise
	for rows.Next() {
		var e Exercise
		if err := rows.Scan(&e.ID, &e.Type, &e.Position, &e.Title, &e.Instruction,
			&e.Payload, &e.AnswerKey, &e.RawSource); err != nil {
			return "", nil, fmt.Errorf("content: не удалось прочитать упражнения: %w", err)
		}
		out = append(out, e)
	}
	return status, out, rows.Err()
}

// parseAnswerKey приводит ключ ответов к строкам.
//
// Значение бывает не только строкой: в старых уроках встречаются числа.
// Разбираем мягко — как и проверка ответов (см. CheckAnswer).
func parseAnswerKey(raw []byte) map[string]string {
	out := map[string]string{}
	if len(raw) == 0 {
		return out
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return out
	}
	for k, v := range parsed {
		out[k] = fmt.Sprint(v)
	}
	return out
}
