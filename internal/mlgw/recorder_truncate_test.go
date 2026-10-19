package mlgw

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// Ошибка по-русски длиннее предела не должна резаться посреди буквы:
// PostgreSQL отвергает такой текст вместе со всей пачкой COPY учёта.
func TestTruncateKeepsValidUTF8(t *testing.T) {
	text := strings.Repeat("ошибка ", 100) // 2 байта на букву, нечётные границы
	for max := 500; max <= 512; max++ {
		got := truncate(text, max)
		if !utf8.ValidString(got) || len(got) > max || len(got) < max-3 {
			t.Fatalf("max=%d: %d байт, валидный UTF-8: %v", max, len(got), utf8.ValidString(got))
		}
	}
	if got := truncate("плохой\x00байт\xff", 512); strings.ContainsRune(got, 0) || !utf8.ValidString(got) {
		t.Errorf("нулевой байт и битый UTF-8 должны уйти: %q", got)
	}
	if truncate("коротко", 512) != "коротко" {
		t.Error("короткий текст не меняется")
	}
}
