import assert from 'node:assert/strict'
import { test } from 'node:test'
import { averageOf, cellView, mistakeLabel, scoreTone, studentErrors, studentWords } from '../src/cabinet/pages/results.js'

test('цвет клетки: зелёный от 70 %, жёлтый 60–69 %, красный ниже 60 %', () => {
	assert.equal(scoreTone(100), 'badge-green')
	assert.equal(scoreTone(70), 'badge-green')
	assert.equal(scoreTone(69), 'badge-warn')
	assert.equal(scoreTone(60), 'badge-warn')
	assert.equal(scoreTone(59), 'badge-err')
	assert.equal(scoreTone(0), 'badge-err')
	assert.equal(scoreTone(null), 'badge-gray')
})

const done = { started: true, completed: true, correct_answers: 6, total_questions: 10, correct_percent: 60, conversation: null }

test('задания: процент, «не пройден» и «не завершён»', () => {
	assert.deepEqual(cellView(done, 'tasks'), { text: '60%', tone: 'badge-warn', note: '' })
	assert.deepEqual(cellView({ started: false }, 'tasks'), { text: 'не пройден', tone: null })
	assert.equal(cellView({ ...done, completed: false }, 'tasks').note, 'не завершён')
	// Ноль процентов — результат, а не пустая клетка.
	assert.deepEqual(cellView({ ...done, correct_answers: 0, correct_percent: 0 }, 'tasks').text, '0%')
})

test('разговор: «3 из 4», «не было» и разбор, который ещё готовится', () => {
	const conv = (c) => ({ ...done, conversation: c })
	assert.deepEqual(cellView(conv({ ready: true, credited: 3, goals: 4 }), 'conversation'), {
		text: '3 из 4',
		tone: 'badge-green',
	})
	assert.equal(cellView(conv({ ready: true, credited: 1, goals: 4 }), 'conversation').tone, 'badge-err')
	assert.equal(cellView(conv(null), 'conversation').text, 'не было')
	assert.equal(cellView({ started: false }, 'conversation').text, 'не было')
	assert.equal(cellView(conv({ ready: false, credited: 0, goals: 0 }), 'conversation').text, 'готовится')
})

test('среднее строки и столбца по режиму', () => {
	const row = { correct_percent: 73, conversation_percent: null }
	assert.equal(averageOf(row, 'tasks'), '73%')
	assert.equal(averageOf(row, 'conversation'), '—')
})

test('ошибка трудного слова: вид задания, вопрос и неверный ответ', () => {
	assert.equal(
		mistakeLabel({ task_type: 'matching', prompt: 'энекәш', answer: 'старший брат' }),
		'Сопоставление: энекәш → старший брат',
	)
	assert.equal(mistakeLabel({ task_type: 'gap_fill', prompt: 'Минем ___ бар.', answer: 'сеңел' }), 'Пропуски: Минем ___ бар. → сеңел')
	assert.equal(mistakeLabel({ task_type: 'custom', prompt: 'әти', answer: 'мама' }), 'әти → мама')
	assert.equal(mistakeLabel({ task_type: 'matching', prompt: '', answer: 'мама' }), 'Сопоставление: мама')
})

test('что повторить ученику: только его слова, больше его ошибок — выше', () => {
	const word = (phrase, errors, struggled) => ({ phrase, errors, struggled })
	const words = [
		word('энекәш', 10, [{ student_id: 'a', errors: 1 }, { student_id: 'b', errors: 4 }]),
		word('күрше', 7, [{ student_id: 'b', errors: 1 }]),
		word('урам', 5, [{ student_id: 'a', errors: 3 }]),
	]
	assert.deepEqual(studentWords(words, 'a').map((w) => w.phrase), ['урам', 'энекәш'])
	assert.deepEqual(studentWords(words, 'c'), [])
	assert.equal(studentErrors(words[0], 'b'), 4)
	assert.equal(studentErrors(words[1], 'a'), 0)
})
