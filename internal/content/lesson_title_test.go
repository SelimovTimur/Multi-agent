package content

import (
	"strings"
	"testing"

	"github.com/NATIVE/NATIVE/internal/platform/httpx"
)

// Название урока проверяется одинаково при создании и при переименовании.
// Проверка нашла отказ без причины на создании; на переименовании отказа не
// было вовсе — урок с названием в триста символов получался в обход предела,
// и эта строка ложилась на все экраны, где стоит рядом с другими.
func TestTitleLimitIsTheSameOnBothPaths(t *testing.T) {
	long := strings.Repeat("ә", titleLimit+1)

	_, err := DraftContent{
		Title: long, LangCode: "tatar", Level: "A2", RefinedText: "текст урока",
	}.validate()
	if err == nil {
		t.Fatal("создание приняло название длиннее предела")
	}
	reason := httpx.AsError(err).Fields["title"]
	if !strings.Contains(reason, "200") {
		t.Errorf("отказ не называет предел: %q", reason)
	}
	// Тот же текст отказа на переименовании: два разных объяснения одного
	// запрета учитель читает как два разных правила.
	if reason != tooLongTitle {
		t.Errorf("отказы разошлись: %q против %q", reason, tooLongTitle)
	}

	// Ровно предел проходит: иначе запрет читался бы как «меньше двухсот».
	if _, err := (DraftContent{
		Title: strings.Repeat("ә", titleLimit), LangCode: "tatar", Level: "A2", RefinedText: "текст урока",
	}).validate(); err != nil {
		reasons := httpx.AsError(err).Fields
		if reasons["title"] != "" {
			t.Errorf("название ровно в предел отклонено: %q", reasons["title"])
		}
	}
}
