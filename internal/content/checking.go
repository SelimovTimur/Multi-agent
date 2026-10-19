package content

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/NATIVE/NATIVE/internal/platform/httpx"
	"github.com/NATIVE/NATIVE/internal/platform/pg"
	"github.com/google/uuid"
)

// AnswerCheck — итог сверки ответа с ключом.
type AnswerCheck struct {
	Correct bool
	// Expected — верный ответ. Показывается ученику ПОСЛЕ его попытки:
	// не увидев правильного варианта, он не научится, а просто запомнит,
	// что ошибся.
	Expected string
	// Score — сколько добавить к счёту. Считает сервер, а не клиент.
	Score int
	// Kind — вид задания. Уходит в аналитику: «сопоставление» и «пропуски»
	// говорят учителю о разном, а «шаг 2» и «шаг 4» — ни о чём, потому что
	// у разных уроков под этими номерами разные задания.
	Kind string
}

// QuestionCount — сколько в версии вопросов, на которые ученик отвечает.
//
// Текст, выражения, грамматика и свой текст не считаются: отвечать там
// нечего. От этого числа считается доля верных ответов ученика.
func (s *Service) QuestionCount(ctx context.Context, orgID, versionID uuid.UUID) (int, error) {
	items, err := s.store.ListExercises(ctx, s.pool, versionID, true)
	if err != nil {
		return 0, httpx.Internal("Не удалось прочитать урок").Wrap(err)
	}
	total := 0
	for _, e := range items {
		var payload struct {
			Items     []json.RawMessage `json:"items"`
			Questions []json.RawMessage `json:"questions"`
		}
		if len(e.Payload) > 0 {
			_ = json.Unmarshal(e.Payload, &payload)
		}
		total += len(payload.Items) + len(payload.Questions)
	}
	return total, nil
}

// scorePerAnswer — цена верного ответа.
//
// Одинаковая для всех типов: разная цена требует объяснения ученику, а
// объяснять пока нечего — упражнения сопоставимы по трудности.
const scorePerAnswer = 10

// CheckAnswer сверяет ответ ученика с ключом упражнения.
//
// Живёт в content, потому что ключ хранится здесь и здесь же известен его
// формат. Наружу уходит только «верно/неверно» и правильный ответ на этот
// конкретный вопрос — сам ключ не покидает модуль.
//
// Раньше правильность присылал клиент, а ключа у него нет: проверить ответ
// он не мог физически, а мог прислать «верно» на что угодно.
func (s *Service) CheckAnswer(ctx context.Context, orgID, versionID, exerciseID uuid.UUID,
	itemID, value string) (AnswerCheck, error) {

	ex, err := s.store.ExerciseByID(ctx, s.pool, versionID, exerciseID)
	if errors.Is(err, ErrNotFound) {
		return AnswerCheck{}, httpx.NotFound("Упражнение не найдено")
	}
	if err != nil {
		return AnswerCheck{}, err
	}

	key := map[string]string{}
	if len(ex.AnswerKey) > 0 {
		// Ключ бывает не только строкой: в старых уроках встречаются числа
		// и списки. Разбираем мягко — неразобранное считаем отсутствующим,
		// а не роняем прохождение.
		var raw map[string]any
		if err := json.Unmarshal(ex.AnswerKey, &raw); err != nil {
			return AnswerCheck{}, httpx.Internal("Не удалось прочитать ключ ответов").Wrap(err)
		}
		for k, v := range raw {
			key[k] = fmt.Sprint(v)
		}
	}

	expected, ok := key[itemID]
	if !ok {
		// Ключа на этот вопрос нет — упражнение собрано без него. Ответ
		// принимаем, но верным не объявляем: врать в статистику хуже, чем
		// признать, что проверить нечем.
		return AnswerCheck{Correct: false, Expected: "", Kind: ex.Type}, nil
	}

	correct := sameAnswer(expected, value)
	out := AnswerCheck{Correct: correct, Expected: expected, Kind: ex.Type}
	if correct {
		out.Score = scorePerAnswer
	}
	return out, nil
}

// sameAnswer сравнивает ответы так, как их сравнил бы человек.
//
// Регистр и лишние пробелы не должны решать судьбу ответа: ученик,
// написавший «Эчпочмак » вместо «эчпочмак», ответил верно.
func sameAnswer(expected, got string) bool {
	norm := func(s string) string {
		s = strings.ToLower(strings.TrimSpace(s))
		return strings.Join(strings.Fields(s), " ")
	}
	return norm(expected) == norm(got)
}

// ExerciseByID читает одно упражнение вместе с ключом.
//
// Только для проверки ответа: наружу этот метод не выставлен, а ручки
// ученика ходят через ListExercises(withAnswers=false).
func (s *Store) ExerciseByID(ctx context.Context, q pg.Querier, versionID, id uuid.UUID) (Exercise, error) {
	var e Exercise
	err := q.QueryRow(ctx, `
		SELECT id, type, position, title, instruction, payload, answer_key, raw_source
		FROM content.exercises WHERE id = $1 AND lesson_version_id = $2`, id, versionID).
		Scan(&e.ID, &e.Type, &e.Position, &e.Title, &e.Instruction,
			&e.Payload, &e.AnswerKey, &e.RawSource)
	if pg.IsNoRows(err) {
		return Exercise{}, ErrNotFound
	}
	if err != nil {
		return Exercise{}, fmt.Errorf("content: не удалось прочитать упражнение: %w", err)
	}
	return e, nil
}
