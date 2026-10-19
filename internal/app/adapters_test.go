package app

import (
	"testing"

	"github.com/NATIVE/NATIVE/internal/content"
	"github.com/NATIVE/NATIVE/internal/learning"
	"github.com/google/uuid"
)

// Задания урока content называет ключами и видами, а learning по ним хранит
// выданное и сделанное. Модули друг о друге не знают, поэтому совпадение
// проверяется здесь, где они встречаются: разойдись ключ упражнения хоть
// одним символом — ответы учеников перестанут находить своё задание, и
// узнали бы об этом только на уроке.
func TestTaskVocabularyMatchesAcrossPort(t *testing.T) {
	id := uuid.New()
	if content.ExerciseTaskKey(id) != learning.ExerciseTaskKey(id) {
		t.Fatalf("ключ упражнения: content %q, learning %q",
			content.ExerciseTaskKey(id), learning.ExerciseTaskKey(id))
	}
	kinds := map[string]string{
		content.TaskObjectives: learning.TaskObjectives,
		content.TaskText:       learning.TaskText,
		content.TaskLexicon:    learning.TaskLexicon,
		content.TaskExercise:   learning.TaskExercise,
		content.TaskGrammar:    learning.TaskGrammar,
		content.TaskDialog:     learning.TaskDialog,
	}
	for c, l := range kinds {
		if c != l {
			t.Errorf("вид задания: content %q, learning %q", c, l)
		}
	}

	in := []content.LessonTask{{Key: content.ExerciseTaskKey(id), N: 4, Kind: content.TaskExercise,
		Type: content.ExerciseMatching, TypeLabel: "Упражнение", Title: "Подбери пару", Questions: 2,
		ExerciseID: id, ItemIDs: []string{"әни", "әти"}}}
	out := toLearningTasks(in)
	if len(out) != 1 || out[0].Key != in[0].Key || out[0].ExerciseID != id || out[0].Questions != 2 ||
		out[0].TypeLabel != "Упражнение" || len(out[0].ItemIDs) != 2 {
		t.Fatalf("задание потеряло поля по пути через порт: %+v", out)
	}
	// Список копируется: content отдаёт его из кеша, общего для запросов.
	out[0].ItemIDs[0] = "чужое"
	if in[0].ItemIDs[0] != "әни" {
		t.Error("правка в learning задела кеш content")
	}
}
