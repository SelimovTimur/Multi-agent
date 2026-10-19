// student-names.js — разбор списка учеников, вставленного учителем.
//
// Списки приходят из Word и Excel в самом разном виде: таблица «№ / ФИ»
// копируется то строкой «1<Tab>Абдуллина Азалия», то номером и фамилией на
// разных строках, с шапкой, двойными пробелами и пустыми строками. Учитель
// не должен чистить это руками: каждая лишняя строка превратилась бы в
// ученика по имени «12» или «ФИО».

// Заголовки столбцов, которые копируются вместе с таблицей.
const HEADERS = new Set(['№', 'n', 'no', 'фи', 'фио', 'ф.и.', 'ф.и.о.', 'имя', 'ученик', 'ученики'])

// Ячейка, в которой нет ни одной буквы: номер строки, «1.», «2)», «—».
const NO_LETTERS = /^[^\p{L}]*$/u

/**
 * parseNames превращает вставленный текст в список имён.
 *
 * Номера и заголовки отбрасываются, лишние пробелы схлопываются, повторы
 * внутри списка убираются без учёта регистра и «ё». Порядок сохраняется —
 * он совпадает с журналом.
 */
export function parseNames(text) {
	const out = []
	const seen = new Set()
	for (const line of String(text || '').split(/\r?\n/)) {
		const cells = line
			.split('\t')
			.map((c) => c.replace(/^\s*\d+\s*[.)]\s*/, '').replace(/\s+/g, ' ').trim())
			.filter((c) => c && !NO_LETTERS.test(c) && !HEADERS.has(c.toLowerCase()))
		const name = cells.join(' ')
		if (!name) continue
		const key = name.toLowerCase().replaceAll('ё', 'е')
		if (seen.has(key)) continue
		seen.add(key)
		out.push(name)
	}
	return out
}
