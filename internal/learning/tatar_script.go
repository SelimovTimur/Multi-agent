package learning

import (
	"strings"
	"unicode"
)

// Реплики аватара из живой сессии — только татарской кириллицей.
//
// Голосовая модель расшифровывает свою речь сама и на татарском иногда
// пишет казахскими буквами (қ, ғ, і) или турецкой латиницей (05.10.2026,
// первые тесты с учителями). Ученик видит такую реплику в подсказке, учитель
// — в отчёте. Правила те же, что у распознавания ответов
// (asr_service/asr_review.py) и у экрана разговора
// (frontend/src/cabinet/live/tatar-script.js):
//   - казахские и башкирские буквы — татарскими (қ→к, і→е, ҙ→з);
//   - латиница внутри кириллического слова — кириллицей: двойник той же
//     буквой (a→а), прочее по татарской латинице (t→т);
//   - слово с буквами тюркской латиницы (ä, ş, ı…) и реплика, почти целиком
//     записанная латиницей, — транслитерацией: «Anlamadım» → «Анламадым».
//
// Латинское имя внутри кириллической реплики («YouTube») не трогается.

const tatarLanguage = "tatar"

var foreignCyrillic = map[rune]rune{
	'қ': 'к', 'ғ': 'г', 'ұ': 'у', 'і': 'е', // казахские
	'ҡ': 'к', 'ҙ': 'з', 'ҫ': 'с', // башкирские
}

var turkicLatin = map[rune]rune{
	'ä': 'ә', 'ə': 'ә', 'ö': 'ө', 'ü': 'ү', 'ñ': 'ң', 'ŋ': 'ң',
	'ş': 'ш', 'ç': 'ч', 'ğ': 'г', 'ı': 'ы',
}

// latinTwins — латинские буквы, на глаз неотличимые от кириллических.
var latinTwins = map[rune]rune{
	'a': 'а', 'c': 'с', 'e': 'е', 'o': 'о', 'p': 'р', 'x': 'х', 'y': 'у', 'h': 'һ', 'ə': 'ә',
	'A': 'А', 'B': 'В', 'C': 'С', 'E': 'Е', 'H': 'Н', 'K': 'К', 'M': 'М',
	'O': 'О', 'P': 'Р', 'T': 'Т', 'X': 'Х', 'Y': 'Ү', 'Ə': 'Ә',
}

// latinToTatar — татарская латиница по буквам (строчные).
var latinToTatar = map[rune]rune{
	'a': 'а', 'b': 'б', 'c': 'җ', 'd': 'д', 'e': 'е', 'f': 'ф', 'g': 'г',
	'h': 'һ', 'i': 'и', 'j': 'ж', 'k': 'к', 'l': 'л', 'm': 'м', 'n': 'н',
	'o': 'о', 'p': 'п', 'q': 'к', 'r': 'р', 's': 'с', 't': 'т', 'u': 'у',
	'v': 'в', 'w': 'в', 'x': 'х', 'y': 'й', 'z': 'з',
	'ä': 'ә', 'ə': 'ә', 'ö': 'ө', 'ü': 'ү', 'ñ': 'ң', 'ŋ': 'ң',
	'ş': 'ш', 'ç': 'ч', 'ğ': 'г', 'ı': 'ы',
}

var latinDigraphs = map[string]rune{"ya": 'я', "yä": 'я', "yu": 'ю', "yü": 'ю', "yo": 'ё'}

func isLatin(r rune) bool    { return unicode.Is(unicode.Latin, r) }
func isCyrillic(r rune) bool { return unicode.Is(unicode.Cyrillic, r) }

// withCase переносит регистр исходной буквы на замену. unicode.ToLower
// переводит турецкую «İ» в обычную «i».
func withCase(src, dst rune) rune {
	if unicode.IsUpper(src) {
		return unicode.ToUpper(dst)
	}
	return dst
}

func isTurkic(r rune) bool {
	_, ok := turkicLatin[unicode.ToLower(r)]
	return ok || r == 'İ'
}

func mostlyLatin(text string) bool {
	letters, latin := 0, 0
	for _, r := range text {
		if unicode.IsLetter(r) {
			letters++
			if isLatin(r) {
				latin++
			}
		}
	}
	return letters > 0 && latin*2 > letters
}

func inCyrillicWord(r rune) rune {
	if !isLatin(r) {
		return r
	}
	if c, ok := latinTwins[r]; ok {
		return c
	}
	if c, ok := latinToTatar[unicode.ToLower(r)]; ok {
		return withCase(r, c)
	}
	return r
}

func transliterate(word []rune) []rune {
	out := make([]rune, 0, len(word))
	for i := 0; i < len(word); i++ {
		if i+1 < len(word) {
			pair := strings.ToLower(string(word[i : i+2]))
			if c, ok := latinDigraphs[pair]; ok {
				out = append(out, withCase(word[i], c))
				i++
				continue
			}
		}
		r := word[i]
		low := unicode.ToLower(r)
		switch c, ok := latinToTatar[low]; {
		case low == 'e' && i == 0:
			out = append(out, withCase(r, 'э')) // eş → эш
		case ok:
			out = append(out, withCase(r, c))
		default:
			out = append(out, r)
		}
	}
	return out
}

func fixWord(word []rune, whole bool) []rune {
	cyrillic, latin, turkic := false, false, false
	for _, r := range word {
		cyrillic = cyrillic || isCyrillic(r)
		latin = latin || isLatin(r)
		turkic = turkic || isTurkic(r)
	}
	switch {
	case cyrillic && latin:
		out := make([]rune, len(word))
		for i, r := range word {
			out[i] = inCyrillicWord(r)
		}
		return out
	case latin && (whole || turkic):
		return transliterate(word)
	}
	return word
}

// toTatarCyrillic приводит татарский текст к татарской кириллице.
func toTatarCyrillic(text string) string {
	runes := []rune(text)
	for i, r := range runes {
		if c, ok := foreignCyrillic[unicode.ToLower(r)]; ok {
			runes[i] = withCase(r, c)
		}
	}
	whole := mostlyLatin(string(runes))
	var b strings.Builder
	b.Grow(len(text))
	for i := 0; i < len(runes); {
		if !unicode.IsLetter(runes[i]) {
			b.WriteRune(runes[i])
			i++
			continue
		}
		j := i
		for j < len(runes) && unicode.IsLetter(runes[j]) {
			j++
		}
		b.WriteString(string(fixWord(runes[i:j], whole)))
		i = j
	}
	return b.String()
}
