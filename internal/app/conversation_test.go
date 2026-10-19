package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/NATIVE/NATIVE/internal/content"
	"github.com/NATIVE/NATIVE/internal/learning"
	"github.com/NATIVE/NATIVE/internal/lessonpipe"
)

// Цели разговора — правило договора (раздел 6): сохранённая настройка —
// списки учителя с пояснениями; не сохранённая — первые 6 выражений
// словаря с переводом и первые 3 темы с объяснением. Ключи p1…, g1… по
// порядку: по ним отчёт учителя сводит отметки всего класса.
func TestGoalsFollowTheContractRule(t *testing.T) {
	saved := content.VersionContent{Avatar: content.AvatarConfig{
		Saved:                true,
		PracticePhrases:      []string{"Минем исемем …", "Мин Казанда яшим"},
		PracticePhraseNotes:  []string{"Меня зовут …", ""},
		PracticeGrammar:      []string{"Местный падеж -да/-дә"},
		PracticeGrammarNotes: []string{"где? — в Казани"},
	}, Lexicon: []content.LexicalUnit{{Phrase: "не должна попасть"}}}
	got := avatarGoals(saved)
	want := []learning.AvatarGoal{
		{Key: "p1", Kind: learning.GoalPhrase, Title: "Минем исемем …", Note: "Меня зовут …"},
		{Key: "p2", Kind: learning.GoalPhrase, Title: "Мин Казанда яшим", Note: ""},
		{Key: "g1", Kind: learning.GoalGrammar, Title: "Местный падеж -да/-дә", Note: "где? — в Казани"},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("сохранённая настройка:\n%v\nожидали\n%v", got, want)
	}

	// Учитель сохранил пустые списки — целей нет, а не «вся лексика».
	if g := avatarGoals(content.VersionContent{Avatar: content.AvatarConfig{Saved: true},
		Lexicon: []content.LexicalUnit{{Phrase: "әни"}}}); len(g) != 0 {
		t.Errorf("пустая сохранённая настройка дала цели: %v", g)
	}

	var lexicon []content.LexicalUnit
	for i := 1; i <= 9; i++ {
		lexicon = append(lexicon, content.LexicalUnit{Phrase: fmt.Sprintf("сүз %d", i), Translation: fmt.Sprintf("слово %d", i)})
	}
	lexicon[2].Phrase = "  " // пустое выражение не занимает номер
	long := strings.Repeat("Объяснение темы ", 40)
	unsaved := content.VersionContent{Lexicon: lexicon, Grammar: []content.GrammarTopic{
		{Title: "Тема 1", Explanation: long}, {Title: "Тема 2"}, {Title: "Тема 3"}, {Title: "Тема 4"},
	}}
	got = avatarGoals(unsaved)
	if len(got) != 9 {
		t.Fatalf("6 фраз и 3 темы, получили %d: %v", len(got), got)
	}
	if got[0].Key != "p1" || got[0].Note != "слово 1" || got[2].Key != "p3" || got[2].Title != "сүз 4" ||
		got[5].Key != "p6" || got[5].Title != "сүз 7" {
		t.Errorf("фразы по порядку с переводом: %v", got[:6])
	}
	if got[6].Key != "g1" || got[8].Key != "g3" || got[8].Title != "Тема 3" || got[6].Kind != learning.GoalGrammar {
		t.Errorf("темы: %v", got[6:])
	}
	if n := len([]rune(got[6].Note)); n > content.AvatarNoteMaxRunes || !strings.HasSuffix(got[6].Note, "…") {
		t.Errorf("объяснение темы не обрезано до %d символов: %d", content.AvatarNoteMaxRunes, n)
	}
}

// Отказ движка «ход уже применён» доходит до learning своим значением, а
// не общей ошибкой: по нему ученик получает уже записанную реплику, а не
// «аватар не смог ответить».
func TestEngineConflictCrossesThePort(t *testing.T) {
	if !errors.Is(engineError(lessonpipe.ErrConversationConflict), learning.ErrEngineConflict) {
		t.Fatal("409 движка потерялся по пути в learning")
	}
	other := errors.New("сбой")
	if engineError(other) != other {
		t.Error("прочие ошибки должны проходить как есть")
	}
}

// Кеш указаний уровня: один запрос на язык и уровень, сбой не мешает
// разговору и кешируется ненадолго, размер ограничен.
func TestLevelGuideCachesAndDegradesSilently(t *testing.T) {
	now := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	calls := map[string]int{}
	fail := false
	g := &levelGuide{
		fetch: func(_ context.Context, language, level string) (string, error) {
			calls[language+"|"+level]++
			if fail {
				return "", errors.New("llm_service лежит")
			}
			return "указания " + level, nil
		},
		ttl: time.Hour, failTTL: time.Minute, limit: 3, now: func() time.Time { return now },
		items: map[string]guideEntry{},
	}
	ctx := context.Background()

	if got := g.LevelGuidance(ctx, "Tatar", "A2"); got != "указания a2" {
		t.Fatalf("указания: %q", got)
	}
	if got := g.LevelGuidance(ctx, "tatar", "a2"); got != "указания a2" || calls["tatar|a2"] != 1 {
		t.Errorf("повтор не из кеша: %q, запросов %d", got, calls["tatar|a2"])
	}
	if g.LevelGuidance(ctx, "tatar", "") != "" || len(calls) != 1 {
		t.Error("без уровня справочник не спрашивается")
	}

	// Сбой — пустые указания, и минуту справочник не дёргают.
	fail = true
	if got := g.LevelGuidance(ctx, "tatar", "b1"); got != "" {
		t.Errorf("при сбое указаний нет: %q", got)
	}
	g.LevelGuidance(ctx, "tatar", "b1")
	if calls["tatar|b1"] != 1 {
		t.Errorf("сбой не закеширован: %d запросов", calls["tatar|b1"])
	}
	now = now.Add(2 * time.Minute)
	fail = false
	if got := g.LevelGuidance(ctx, "tatar", "b1"); got != "указания b1" || calls["tatar|b1"] != 2 {
		t.Errorf("после сбоя справочник спрашивается снова: %q, %d", got, calls["tatar|b1"])
	}

	// Кеш не растёт сверх предела.
	for _, level := range []string{"a1", "b2", "c1", "zz"} {
		g.LevelGuidance(ctx, "tatar", level)
	}
	if len(g.items) > g.limit {
		t.Errorf("кеш вырос до %d при пределе %d", len(g.items), g.limit)
	}

	// По истечении TTL указания перечитываются.
	before := calls["tatar|c1"]
	now = now.Add(2 * time.Hour)
	g.LevelGuidance(ctx, "tatar", "c1")
	if calls["tatar|c1"] != before+1 {
		t.Error("указания не перечитаны после TTL")
	}
}
