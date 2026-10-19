package content

// Разбор грамматической конструкции (NR-27).
//
// Экран «Как устроен этот текст» правит объяснение, примеры парами и задания
// на конструкцию. Здесь — приведение того, что пришло из кабинета, к
// сохраняемому виду и пределы, за которыми разбор перестаёт быть разбором.

import (
	"fmt"
	"strings"
)

func normalizeGrammar(g GrammarTopic, fields map[string]string) GrammarTopic {
	g.Title = strings.TrimSpace(g.Title)
	g.Example = strings.TrimSpace(g.Example)
	g.Explanation = strings.TrimSpace(g.Explanation)

	examples := make([]GrammarExample, 0, len(g.Examples))
	for _, e := range g.Examples {
		e.Phrase = strings.TrimSpace(e.Phrase)
		e.Translation = strings.TrimSpace(e.Translation)
		// Пример без самой конструкции — пустая строка на экране; перевод
		// бывает не нужен, и его отсутствие примера не отменяет.
		if e.Phrase == "" {
			continue
		}
		examples = append(examples, e)
	}
	if len(examples) > GrammarMaxExamples {
		fields["grammar.examples"] = fmt.Sprintf("Не больше %d примеров на конструкцию", GrammarMaxExamples)
	}
	g.Examples = examples

	practice := make([]GrammarTask, 0, len(g.Practice))
	for _, t := range g.Practice {
		t.Prompt = strings.TrimSpace(t.Prompt)
		t.Answer = strings.TrimSpace(t.Answer)
		if t.Prompt == "" {
			continue
		}
		practice = append(practice, t)
	}
	if len(practice) > GrammarMaxPractice {
		fields["grammar.practice"] = fmt.Sprintf("Не больше %d заданий на конструкцию", GrammarMaxPractice)
	}
	g.Practice = practice

	// Строка примеров через точку с запятой — короткая подпись конструкции.
	// Собираем её из примеров, чтобы подпись не расходилась с разбором.
	if len(g.Examples) > 0 {
		phrases := make([]string, 0, len(g.Examples))
		for _, e := range g.Examples {
			phrases = append(phrases, e.Phrase)
		}
		g.Example = strings.Join(phrases, "; ")
	}
	return g
}

// normalizeObjectives приводит цели урока к сохраняемому виду.
//
// Пустые строки выбрасываем молча: строка появляется по нажатию «Добавить
// цель», и незаполненная — это передумал, а не ошибка.
func normalizeObjectives(items []Objective, fields map[string]string) []Objective {
	out := make([]Objective, 0, len(items))
	for _, o := range items {
		o.Text = strings.TrimSpace(o.Text)
		if o.Text == "" {
			continue
		}
		if len([]rune(o.Text)) > ObjectiveMaxRunes {
			fields["objectives"] = fmt.Sprintf("Цель — не длиннее %d символов", ObjectiveMaxRunes)
			break
		}
		out = append(out, o)
	}
	if len(out) > ObjectivesMax {
		fields["objectives"] = fmt.Sprintf("Не больше %d целей на урок", ObjectivesMax)
	}
	return out
}
