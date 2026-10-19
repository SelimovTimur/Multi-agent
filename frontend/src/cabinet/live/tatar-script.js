// Реплики живой сессии — только татарской кириллицей.
//
// Голосовая модель расшифровывает свою речь сама и на татарском иногда пишет
// казахскими буквами (қ, ғ, і) или турецкой латиницей (05.10.2026, первые
// тесты с учителями). Правила те же, что у распознавания ответов
// (asr_service/asr_review.py) и у сервера (internal/learning/tatar_script.go):
// казахские и башкирские буквы — татарскими, латиница внутри кириллического
// слова — кириллицей, слово с тюркскими латинскими буквами (ä, ş, ı…) и
// реплика, почти целиком записанная латиницей, — транслитерацией. Латинское
// имя внутри кириллической реплики («YouTube») не трогается.

const FOREIGN_CYRILLIC = { қ: 'к', ғ: 'г', ұ: 'у', і: 'е', ҡ: 'к', ҙ: 'з', ҫ: 'с' }
const TURKIC_LATIN = { ä: 'ә', ə: 'ә', ö: 'ө', ü: 'ү', ñ: 'ң', ŋ: 'ң', ş: 'ш', ç: 'ч', ğ: 'г', ı: 'ы' }
// Латинские буквы, на глаз неотличимые от кириллических.
const LATIN_TWINS = {
	a: 'а', c: 'с', e: 'е', o: 'о', p: 'р', x: 'х', y: 'у', h: 'һ', ə: 'ә',
	A: 'А', B: 'В', C: 'С', E: 'Е', H: 'Н', K: 'К', M: 'М', O: 'О', P: 'Р', T: 'Т', X: 'Х', Y: 'Ү', Ə: 'Ә',
}
const LATIN_TO_TATAR = {
	a: 'а', b: 'б', c: 'җ', d: 'д', e: 'е', f: 'ф', g: 'г', h: 'һ', i: 'и', j: 'ж', k: 'к', l: 'л', m: 'м',
	n: 'н', o: 'о', p: 'п', q: 'к', r: 'р', s: 'с', t: 'т', u: 'у', v: 'в', w: 'в', x: 'х', y: 'й', z: 'з',
	...TURKIC_LATIN,
}
const DIGRAPHS = { ya: 'я', yä: 'я', yu: 'ю', yü: 'ю', yo: 'ё' }

const WORD = /\p{L}+/gu
const LETTER = /\p{L}/u
const CYRILLIC = /\p{Script=Cyrillic}/u
const LATIN = /\p{Script=Latin}/u
const FOREIGN = /[қғұіҡҙҫҚҒҰІҠҘҪ]/gu

const has = (map, key) => Object.prototype.hasOwnProperty.call(map, key)
// «İ».toLowerCase() — две кодовые точки (i и точка сверху).
const lower = (ch) => (ch === 'İ' ? 'i' : ch.toLowerCase())
const withCase = (src, dst) => (src !== src.toLowerCase() ? dst.toUpperCase() : dst)
const turkic = (ch) => ch === 'İ' || has(TURKIC_LATIN, lower(ch))

function mostlyLatin(text) {
	const letters = [...text].filter((ch) => LETTER.test(ch))
	const latin = letters.filter((ch) => LATIN.test(ch)).length
	return letters.length > 0 && latin * 2 > letters.length
}

function inCyrillicWord(ch) {
	if (!LATIN.test(ch)) return ch
	if (has(LATIN_TWINS, ch)) return LATIN_TWINS[ch]
	const low = lower(ch)
	return has(LATIN_TO_TATAR, low) ? withCase(ch, LATIN_TO_TATAR[low]) : ch
}

function transliterate(word) {
	const chars = [...word]
	let out = ''
	for (let i = 0; i < chars.length; i++) {
		const pair = (chars[i] + (chars[i + 1] || '')).toLowerCase()
		if (has(DIGRAPHS, pair)) {
			out += withCase(chars[i], DIGRAPHS[pair])
			i++
			continue
		}
		const ch = chars[i]
		const low = lower(ch)
		if (low === 'e' && i === 0) out += withCase(ch, 'э') // eş → эш
		else out += has(LATIN_TO_TATAR, low) ? withCase(ch, LATIN_TO_TATAR[low]) : ch
	}
	return out
}

/** Татарский текст без латиницы, казахских и башкирских букв. */
export function toTatarCyrillic(text) {
	if (!text) return text
	const replaced = String(text).replace(FOREIGN, (ch) => withCase(ch, FOREIGN_CYRILLIC[ch.toLowerCase()]))
	const whole = mostlyLatin(replaced)
	return replaced.replace(WORD, (word) => {
		if (CYRILLIC.test(word) && LATIN.test(word)) return [...word].map(inCyrillicWord).join('')
		if (LATIN.test(word) && (whole || [...word].some(turkic))) return transliterate(word)
		return word
	})
}

/** Нормализатор реплик для языка урока: для татарского — кириллица, иначе как есть. */
export function scriptFor(language) {
	return String(language || '').toLowerCase() === 'tatar' ? toTatarCyrillic : (text) => text
}
