package content

import (
	"strings"
	"testing"
)

func TestNormalizeGrammarPreservesLongQuotesAndPractice(t *testing.T) {
	quote := strings.Repeat("Длинная исходная цитата. ", 20)
	explanation := strings.Repeat("Объяснение конструкции. ", 100)
	fields := map[string]string{}
	got := normalizeGrammar(GrammarTopic{
		Title:       "Конструкция",
		Explanation: explanation,
		Examples:    []GrammarExample{{Phrase: quote, Translation: quote}},
		Practice:    []GrammarTask{{Prompt: quote, Answer: quote}},
	}, fields)
	if len(fields) != 0 {
		t.Fatalf("длина грамматического материала отклонена: %v", fields)
	}
	if got.Explanation != strings.TrimSpace(explanation) || len(got.Examples) != 1 || len(got.Practice) != 1 {
		t.Fatal("потеряна часть грамматического материала")
	}
	if got.Examples[0].Phrase != strings.TrimSpace(quote) || got.Examples[0].Translation != strings.TrimSpace(quote) ||
		got.Practice[0].Prompt != strings.TrimSpace(quote) || got.Practice[0].Answer != strings.TrimSpace(quote) {
		t.Fatal("цитата, перевод или практика были обрезаны")
	}
}
