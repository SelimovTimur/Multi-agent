package classroom

import "testing"

// Списки учеников приходят из Word и Excel. Повторный импорт того же списка
// не должен заводить дубли из-за двойного пробела, регистра или «ё»: иначе
// в классе появятся две Ясмины, а ответы разойдутся по двум профилям.
func TestNameKeyIgnoresFormatting(t *testing.T) {
	same := [][2]string{
		{"Гарипова  Ясмина", "Гарипова Ясмина"},
		{"Гимаева Альбина\t", "гимаева альбина"},
		{"Семёнова Камилла", "Семенова Камилла"},
		{"АЗАЛИЯ", "Азалия"},
	}
	for _, p := range same {
		if NameKey(p[0]) != NameKey(p[1]) {
			t.Errorf("%q и %q должны считаться одним именем", p[0], p[1])
		}
	}
	if NameKey("Вдовина Амелия") == NameKey("Вдовина София") {
		t.Error("разные ученики с одной фамилией склеились")
	}
}

func TestNormalizeNameCollapsesSpaces(t *testing.T) {
	if got := normalizeName("  Гарипова \t Ясмина  "); got != "Гарипова Ясмина" {
		t.Errorf("normalizeName = %q", got)
	}
}
