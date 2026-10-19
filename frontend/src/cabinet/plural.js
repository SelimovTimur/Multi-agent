// plural.js — число со словом в нужной форме.
//
// Отдельным модулем без JSX: правилами с числами пользуются и экраны, и
// node-тесты (scripts/test-*.mjs), а файл с разметкой node не загрузит.

/** plural(3, 'ученик', 'ученика', 'учеников') → '3 ученика' */
export function plural(n, one, few, many) {
	return `${n} ${pluralWord(n, one, few, many)}`
}

/**
 * pluralWord — только слово, без числа: для фраз, где число стоит не прямо
 * перед словом. pluralWord(3, 'урока', 'уроков', 'уроков') → «из 3 уроков».
 */
export function pluralWord(n, one, few, many) {
	const abs = Math.abs(n) % 100
	const last = abs % 10
	if (abs < 11 || abs > 14) {
		if (last === 1) return one
		if (last >= 2 && last <= 4) return few
	}
	return many
}
