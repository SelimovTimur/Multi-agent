package classroom

import (
	"strings"
	"testing"

	"github.com/NATIVE/NATIVE/internal/platform/httpx"
)

// Язык класса — это язык его уроков, и собрать урок можно только на том,
// который знает llm_service. Проверка нашла класс, заведённый на языке из
// семи строк формы; API же принимал любую строку, и ломалось это позже —
// на сборке урока, распознавании речи и разговоре с аватаром.
func TestClassLanguageMustBeSupported(t *testing.T) {
	_, err := ClassInput{Title: "9А", Level: "A2", LangCode: "klingon"}.validate()
	if err == nil {
		t.Fatal("класс на неподдерживаемом языке создался")
	}
	if reason := httpx.AsError(err).Fields["lang_code"]; reason == "" {
		t.Error("отказ не назвал поле языка")
	}

	if _, err := (ClassInput{Title: "9А", Level: "A2", LangCode: "english"}).validate(); err != nil {
		t.Errorf("язык платформы english отклонён: %v", err)
	}
}

// Правка не проверяет язык: он и не меняется. Класс, заведённый до появления
// проверки, иначе стало бы нельзя ни переименовать, ни закрыть.
func TestEditingOldClassDoesNotStumbleOnItsLanguage(t *testing.T) {
	if _, err := (ClassInput{Title: "9А", Level: "A2", LangCode: "klingon"}).validateEdit(); err != nil {
		t.Errorf("правка класса на старом языке отклонена: %v", err)
	}
	// Остальные поля проверяются как обычно.
	_, err := ClassInput{Title: strings.Repeat("а", 65), Level: "A2", LangCode: "english"}.validateEdit()
	if err == nil {
		t.Error("правка приняла слишком длинное название")
	}
}
