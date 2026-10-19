// results.js — клетка таблицы результатов «ученики × уроки» без React.
//
// Пороги цвета и подписи клеток живут здесь, а не в разметке: их проверяет
// node-тест (scripts/test-class-results.mjs), и ими же красится строка урока
// в карточке ученика — одна и та же доля не должна быть зелёной на одном
// экране и жёлтой на другом.

/**
 * Цвет доли верных: зелёный от 70 %, жёлтый 60–69 %, красный ниже 60 %.
 * Пустая доля — серая: «считать не из чего» не хорошо и не плохо.
 */
export function scoreTone(pct) {
	if (pct == null) return 'badge-gray'
	if (pct >= 70) return 'badge-green'
	if (pct >= 60) return 'badge-warn'
	return 'badge-err'
}

/** Доля в процентах или null, когда целого нет. */
export function share(part, total) {
	return total > 0 ? Math.round((part / total) * 100) : null
}

/**
 * Что показать в клетке: { text, tone, note }. tone — класс бейджа или
 * null, когда клетка — подпись, а не результат («не пройден», «не было»).
 *
 * mode — 'tasks' (доля верных в заданиях урока) или 'conversation' (сколько
 * целей разговора закрыто). Числа — те же, что в строке урока карточки
 * ученика: сервер отдаёт клетку теми же полями.
 */
export function cellView(cell, mode) {
	if (!cell?.started) return { text: mode === 'conversation' ? 'не было' : 'не пройден', tone: null }
	if (mode === 'conversation') {
		const c = cell.conversation
		if (!c) return { text: 'не было', tone: null }
		if (!c.ready) return { text: 'готовится', tone: null }
		if (!c.goals) return { text: '—', tone: null }
		return { text: `${c.credited} из ${c.goals}`, tone: scoreTone(share(c.credited, c.goals)) }
	}
	const pct = cell.correct_percent
	// Урок без вопросов: открыт, но доли нет.
	if (pct == null) return { text: '—', tone: null, note: cell.completed ? '' : 'не завершён' }
	return { text: `${pct}%`, tone: scoreTone(pct), note: cell.completed ? '' : 'не завершён' }
}

/** Среднее строки или столбца в выбранном режиме. */
export function averageOf(row, mode) {
	const v = mode === 'conversation' ? row?.conversation_percent : row?.correct_percent
	return v == null ? '—' : `${v}%`
}

// Названия видов упражнений — те же, что в редакторе урока (AddBlock.jsx).
const EXERCISE_KINDS = {
	matching: 'Сопоставление',
	gap_fill: 'Пропуски',
	logical: 'Логическое продолжение',
	grammar: 'Грамматика',
}

/**
 * Подпись склеенной ошибки в задании трудного слова: вид задания, вопрос и
 * неверный ответ — «Сопоставление: энекәш → старший брат». Вид неизвестен —
 * без него: «Упражнение» ничего не добавляет к вопросу.
 */
export function mistakeLabel(m) {
	const kind = EXERCISE_KINDS[m.task_type]
	const said = m.prompt ? `${m.prompt} → ${m.answer}` : m.answer
	return kind ? `${kind}: ${said}` : said
}

/** Ошибки ученика в трудном слове; 0 — не ошибался. */
export function studentErrors(word, studentId) {
	return word.struggled.find((s) => s.student_id === studentId)?.errors || 0
}

/**
 * «Что повторить» в карточке ученика: трудные слова класса, где ученик
 * ошибался, — больше его ошибок выше. Список и числа — те же, что на главной
 * аналитики: слово, трудное только для одного ученика, классу не повторяют,
 * и карточка его не выдумывает.
 */
export function studentWords(words, studentId) {
	return words
		.filter((w) => studentErrors(w, studentId) > 0)
		.sort((x, y) => studentErrors(y, studentId) - studentErrors(x, studentId))
}
