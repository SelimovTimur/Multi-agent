// Правила экранов ученика (docs/product/student-screens/story.md): этапы в
// шапке, «Задание 2 из 4», пропуск в предложении, перевод по фразам, звёзды
// словами и разбор ошибок.
import assert from 'node:assert/strict'
import { test } from 'node:test'
import {
	dayOf, dueText, frankPairs, gapParts, issueLine, leadOf, mistakesOf, restoreAnswers, sectionIssue,
	stageBar, stepLine, starView,
} from '../src/cabinet/student/lesson/kit.js'

const tasks = [
	{ key: 'objectives', kind: 'objectives' },
	{ key: 'text', kind: 'text' },
	{ key: 'lexicon', kind: 'lexicon' },
	{ key: 'ex:m', kind: 'exercise' },
	{ key: 'ex:g', kind: 'exercise' },
]

test('цели урока — вводный экран: в счёт заданий не идут', () => {
	assert.equal(stepLine('class', tasks, 'objectives'), 'На уроке · цели урока')
	assert.equal(stepLine('class', tasks, 'text'), 'На уроке · Задание 1 из 4')
	assert.equal(stepLine('home', tasks, 'ex:g'), 'Домашняя работа · Задание 4 из 4')
	assert.equal(stepLine('home', tasks, null), 'Домашняя работа · итог')
	assert.equal(stepLine('', tasks, null), 'Итог')
})

test('иконки этапов: только этапы раздела и итог, пройденные с галочкой', () => {
	const done = new Set(['objectives', 'text', 'lexicon'])
	const bar = stageBar(tasks, 'ex:g', (t) => done.has(t.key))
	assert.deepEqual(bar.map((s) => s.key), ['text', 'lexicon', 'exercise', 'finish'])
	assert.deepEqual(bar.map((s) => s.state), ['done', 'done', 'cur', ''])
	// Отрезок после текущего этапа — по шагам внутри него: второй из двух.
	assert.equal(bar[2].fill, 67)
	assert.equal(bar[0].fill, 100)
	assert.equal(bar[0].first, 'objectives')
	// К итогу — только когда сделано всё.
	assert.equal(bar[3].reachable, false)
	const all = stageBar(tasks, null, () => true)
	assert.equal(all[3].state, 'cur')
	assert.equal(all[3].reachable, true)
})

test('пропуск: слот на месте маркера, русская подсказка после « — » не показывается', () => {
	assert.deepEqual(gapParts('Без һәрберебез дә ___.'), { before: 'Без һәрберебез дә ', after: '.', slot: true })
	assert.deepEqual(gapParts('___, хөрмәтле дусларым! — так начинается приветствие'), {
		before: '', after: ', хөрмәтле дусларым!', slot: true,
	})
	// Тире внутри татарского предложения — не подсказка.
	assert.equal(gapParts('Камыр өчен — ___ кирәк.').before, 'Камыр өчен — ')
	assert.equal(gapParts('Тыз да салып ___ — бер ярты чәй калагы.').after, ' — бер ярты чәй калагы.')
	// Урок, собранный до маркеров: слот — после предложения.
	assert.equal(gapParts('Мин мәктәпкә барам').slot, false)
})

test('начало фразы для «Продолжи фразу» — без маркера и многоточия', () => {
	assert.equal(leadOf('Каны пешерә беләбез, ә бүген ___'), 'Каны пешерә беләбез, ә бүген')
	assert.equal(leadOf('Хәерле көн…'), 'Хәерле көн')
})

test('перевод по фразам из «фраза\\n— перевод»', () => {
	const frank = 'Хәерле көн!\n— Добрый день!\n\nБез каны пешерәбез.\n— Мы варим кашу.'
	assert.deepEqual(frankPairs(frank), [
		{ text: 'Хәерле көн!', translation: 'Добрый день!' },
		{ text: 'Без каны пешерәбез.', translation: 'Мы варим кашу.' },
	])
	// Текст правили руками, разметки нет — перевод одним текстом.
	assert.equal(frankPairs('Просто перевод без разметки.'), null)
})

test('звёзды словами', () => {
	const issue = { tasks_done: 1, tasks_total: 2, pct: 60, words: { state: 'ready', used: 4, total: 10, need: 5 } }
	assert.deepEqual(starView({ kind: 'done', on: false }, issue), { title: 'Всё сделано', note: 'сделано 1 из 2' })
	assert.deepEqual(starView({ kind: 'done', on: true }, { ...issue, tasks_done: 2 }), { title: 'Всё сделано', note: '2 из 2' })
	assert.equal(starView({ kind: 'correct', on: false }, issue).note, 'у тебя 60%')
	assert.equal(starView({ kind: 'words', on: false }, issue).note, '4 из 10, нужно 5')
	assert.equal(starView({ kind: 'words', on: true }, { ...issue, words: { state: 'ready', used: 6, total: 10, need: 5 } }).note, '6 из 10')
	assert.equal(starView({ kind: 'words', on: false }, { ...issue, words: { state: 'pending' } }).pending, true)
})

test('выдача раздела: на уроке — открытая, на дом — домашняя', () => {
	const issues = [
		{ kind: 'class', open: false, opened_at: '2026-09-29T10:00:00+03:00' },
		{ kind: 'home', open: true, due_at: '2026-10-03T23:59:59+03:00' },
		{ kind: 'class', open: true, opened_at: '2026-10-06T10:00:00+03:00' },
	]
	assert.equal(sectionIssue(issues, 'class'), issues[2])
	assert.equal(sectionIssue(issues, 'home'), issues[1])
	assert.equal(sectionIssue(issues.slice(0, 2), 'class'), issues[0])
	assert.equal(sectionIssue(issues, ''), null)
	assert.equal(issueLine(issues[0]), 'На уроке · 29.09')
	assert.equal(issueLine(issues[1]), 'Домашняя работа · до 03.10')
})

test('даты берутся из строки сервера, а не из пояса браузера', () => {
	assert.equal(dayOf('2026-10-03T23:59:59+03:00'), '03.10')
	assert.equal(dueText(null), 'без срока')
})

test('ошибки: только неверные, словами урока, по упражнениям в порядке урока', () => {
	const content = {
		exercises: [
			{ id: 'm', type: 'matching', payload: { items: [{ phrase: 'әни', translation: 'мама' }, { phrase: 'әти', translation: 'папа' }] } },
			{ id: 'g', type: 'gap_fill', payload: { questions: [
				{ id: 'q1', prompt: 'Минем ___ укытучы.', choices: [{ id: 'A', text: 'әнием' }, { id: 'B', text: 'әниең' }] },
				{ id: 'q2', prompt: 'Ул ___.', choices: [{ id: 'A', text: 'бара' }, { id: 'B', text: 'бар' }] },
			] } },
		],
	}
	const answers = restoreAnswers({
		answers: {
			'm:әни': { answer: 'мама', correct: true, expected: 'мама' },
			'm:әти': { answer: 'мама', correct: false, expected: 'папа' },
			'g:q1': { answer: 'B', correct: false, expected: 'A' },
		},
	})
	const list = mistakesOf(content, answers)
	assert.deepEqual(list.map((m) => [m.name, m.prompt, m.expected, m.chosen]), [
		['Найди пары', 'әти', 'папа', 'мама'],
		['Вставь пропущенное', 'Минем ___ укытучы.', 'әнием', 'әниең'],
	])
	// Ошибки выдачи — только её упражнений.
	assert.equal(mistakesOf(content, answers, new Set(['g'])).length, 1)
})
