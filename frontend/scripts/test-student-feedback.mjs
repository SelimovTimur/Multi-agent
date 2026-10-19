import assert from 'node:assert/strict'
import {
	markAsked,
	questionsToAsk,
	ratingsPayload,
	whyPayload,
	whyQuestions,
} from '../src/cabinet/student/student-feedback.js'

const memory = () => {
	const data = {}
	return { getItem: (k) => data[k] ?? null, setItem: (k, v) => (data[k] = String(v)) }
}
const storage = memory()
assert.deepEqual(questionsToAsk('a1', false, storage), ['lesson'], 'без разговора — только про урок')
assert.deepEqual(questionsToAsk('a1', true, storage), ['lesson', 'avatar'], 'с разговором — оба вопроса')
markAsked('a1', ['lesson'], storage)
assert.deepEqual(questionsToAsk('a1', false, storage), [], 'про урок второй раз не спрашиваем')
assert.deepEqual(questionsToAsk('a1', true, storage), ['avatar'], 'разговор в домашней работе — спрашиваем только про аватара')
markAsked('a1', ['avatar'], storage)
assert.deepEqual(questionsToAsk('a1', true, storage), [], 'всё спросили — окно не открываем')
assert.deepEqual(questionsToAsk('a2', false, storage), ['lesson'], 'другая попытка — спрашиваем заново')
assert.deepEqual(questionsToAsk('', true, storage), [], 'без попытки не спрашиваем')

const broken = { getItem: () => { throw new Error('закрыто') }, setItem: () => { throw new Error('закрыто') } }
markAsked('a1', ['lesson'], broken)
assert.deepEqual(questionsToAsk('a1', false, broken), ['lesson'], 'закрытое хранилище не ломает экран')
assert.deepEqual(questionsToAsk('a1', false, null), ['lesson'], 'хранилища нет вовсе')
for (let i = 0; i < 60; i += 1) markAsked(`x${i}`, ['lesson'], storage)
assert.equal(Object.keys(JSON.parse(storage.getItem('student-feedback-asked'))).length, 50, 'запись не растёт без конца')

assert.equal(ratingsPayload({ unclear: '   ' }), null, 'пустой опрос не отправляется')
assert.deepEqual(ratingsPayload({ lesson: 'good', avatar: '', unclear: ' слова ' }), { lesson: 'good', unclear: 'слова' })
assert.deepEqual(whyQuestions({ lesson: 'bad', avatar: 'ok' }), ['lesson'], '🙁 за урок — спросить почему')
assert.deepEqual(whyQuestions({ lesson: 'bad', avatar: 'bad' }), ['lesson', 'avatar'])
assert.deepEqual(whyQuestions({ lesson: 'ok', avatar: '' }), [], 'без 🙁 второго шага нет')
assert.equal(whyPayload({ lessonWhy: ' ', avatarWhy: '' }), null)
assert.deepEqual(whyPayload({ lessonWhy: ' скучно ', avatarWhy: '' }), { lesson_why: 'скучно' })

console.log('student-feedback: ok')
