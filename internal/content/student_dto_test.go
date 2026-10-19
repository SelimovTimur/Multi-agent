package content

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// Ключ ответов не уходит ученику ни при каких обстоятельствах (NR-28).
//
// Проверка стоит на собранном JSON, а не на структуре: поле, добавленное в
// DTO мимо ToStudentDTO, эта проверка увидит, а обход по известным полям —
// нет. Утечка ключа обессмысливает все задания урока сразу, и заметить её
// по экрану нельзя: он выглядит как обычно, ключ лежит в исходниках страницы.
func TestStudentContentCarriesNoAnswerKey(t *testing.T) {
	versionID, lessonID := uuid.New(), uuid.New()
	content := VersionContent{
		Version: LessonVersion{
			ID: versionID, LessonID: lessonID, Title: "Сабантуй", Level: "A2", Version: 1,
			RefinedText: "Сабантуй — татар халкының бәйрәме.",
		},
		Exercises: []Exercise{{
			ID:        uuid.New(),
			Type:      "gap_fill",
			Title:     "Вставь выражение",
			Payload:   json.RawMessage(`{"questions":[{"id":"q_1","prompt":"…","choices":[{"id":"c_1","text":"бәйрәм"}]}]}`),
			AnswerKey: json.RawMessage(`{"q_1":"c_1"}`),
		}},
		Grammar: []GrammarTopic{{
			Title:    "Направительный падеж",
			Practice: []GrammarTask{{Prompt: "Поставь слово в нужную форму", Answer: "мичкә"}},
		}},
		Avatar: AvatarConfig{Enabled: true, RolePrompt: "Ты продавец на рынке", MaxTurns: 5,
			PracticePhrases: []string{"Минем исемем"}, PracticePhraseNotes: []string{"Меня зовут — ждём имя"},
			PracticeGrammar: []string{"Местный падеж"}, PracticeGrammarNotes: []string{"окончание -да/-дә"}},
	}

	raw, err := json.Marshal(ToStudentDTO(content))
	if err != nil {
		t.Fatal(err)
	}
	out := string(raw)

	// Идентификатор варианта ученику как раз нужен — он отправляет его
	// ответом. Утечка — это связь «вопрос → верный вариант», а она живёт
	// в answer_key, и её тут быть не должно.
	// Пояснения к целям разговора — как роль: текст учителя для модели.
	for _, leak := range []string{"answer_key", `{"q_1":"c_1"}`, "мичкә", "продавец на рынке",
		"practice_phrase_notes", "practice_grammar_notes", "ждём имя", "окончание -да/-дә"} {
		if strings.Contains(out, leak) {
			t.Errorf("ученику ушло лишнее: %q\n%s", leak, out)
		}
	}
	// Урок при этом остаётся уроком: задание и его варианты на месте.
	for _, want := range []string{"Вставь выражение", "бәйрәм", "Направительный падеж"} {
		if !strings.Contains(out, want) {
			t.Errorf("из урока пропало нужное: %q", want)
		}
	}
}

// Учитель пояснения видит всегда — массивом той же длины, что список целей,
// даже у настройки, сохранённой до пояснений.
func TestTeacherSeesGoalNotesAligned(t *testing.T) {
	raw, err := json.Marshal(toContentDTO(VersionContent{Avatar: AvatarConfig{
		PracticePhrases:     []string{"Минем исемем", "Мин Казанда яшим"},
		PracticePhraseNotes: []string{"Меня зовут"},
	}}))
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Avatar struct {
			PhraseNotes  []string `json:"practice_phrase_notes"`
			GrammarNotes []string `json:"practice_grammar_notes"`
		} `json:"avatar"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Avatar.PhraseNotes) != 2 || out.Avatar.PhraseNotes[0] != "Меня зовут" || out.Avatar.PhraseNotes[1] != "" {
		t.Errorf("пояснения фраз: %q", out.Avatar.PhraseNotes)
	}
	if out.Avatar.GrammarNotes == nil || len(out.Avatar.GrammarNotes) != 0 {
		t.Errorf("пояснения тем — пустой массив, а не null: %#v (%s)", out.Avatar.GrammarNotes, raw)
	}
}
