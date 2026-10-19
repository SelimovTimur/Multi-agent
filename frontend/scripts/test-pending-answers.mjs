import assert from 'node:assert/strict'
import { test } from 'node:test'
import {
	addPending, flushPending, loadPending, nextDelay, pendingKey, retryable, savePending,
} from '../src/cabinet/student/lesson/pending.js'

function memory() {
	const data = new Map()
	return {
		getItem: (k) => (data.has(k) ? data.get(k) : null),
		setItem: (k, v) => data.set(k, String(v)),
		removeItem: (k) => data.delete(k),
		data,
	}
}

const answer = (itemId, value = 'a') => ({ exerciseId: 'ex1', itemId, answer: value, step: 2 })

test('очередь переживает перезагрузку страницы и не путает попытки', () => {
	const store = memory()
	savePending('att1', [answer('q1'), answer('q2')], store)
	savePending('att2', [answer('q9')], store)
	assert.deepEqual(loadPending('att1', store).map(pendingKey), ['ex1:q1', 'ex1:q2'])
	assert.deepEqual(loadPending('att2', store).map(pendingKey), ['ex1:q9'])
	savePending('att1', [], store)
	assert.equal(store.data.has('nr-pending-answers:att1'), false, 'пустая очередь удаляется')
})

test('испорченная запись в хранилище не роняет урок', () => {
	const store = memory()
	store.setItem('nr-pending-answers:att1', '{не json')
	assert.deepEqual(loadPending('att1', store), [])
	store.setItem('nr-pending-answers:att1', JSON.stringify([{ exerciseId: 'ex1' }, answer('q1')]))
	assert.deepEqual(loadPending('att1', store).map(pendingKey), ['ex1:q1'])
	assert.deepEqual(loadPending('att1', null), [], 'хранилище закрыто — пустая очередь')
})

test('второй ответ на тот же вопрос заменяет первый', () => {
	const list = addPending(addPending([], answer('q1', 'a')), answer('q1', 'b'))
	assert.equal(list.length, 1)
	assert.equal(list[0].answer, 'b')
})

test('повторять стоит только сбой связи и сервера', () => {
	assert.equal(retryable({ status: 0, code: 'network' }), true)
	assert.equal(retryable({ status: 0, code: 'timeout' }), true)
	assert.equal(retryable({ status: 503 }), true)
	assert.equal(retryable({ status: 429 }), true)
	assert.equal(retryable({ status: 409, code: 'conflict' }), false, 'задание закрыто — повтор не поможет')
	assert.equal(retryable({ status: 404 }), false)
	assert.equal(retryable(null), false)
})

test('отправка по порядку: дошедшие уходят, отказ выбрасывается, сбой связи ждёт', async () => {
	const seen = []
	const send = async (p) => {
		seen.push(p.itemId)
		if (p.itemId === 'q2') throw Object.assign(new Error('Учитель завершил работу на уроке'), { status: 409 })
		if (p.itemId === 'q3') throw Object.assign(new Error('Нет связи'), { status: 0, code: 'network' })
		return { correct: p.itemId === 'q1', expected: 'a' }
	}
	const { sent, dropped, kept } = await flushPending([answer('q1'), answer('q2'), answer('q3'), answer('q4')], send)
	assert.deepEqual(seen, ['q1', 'q2', 'q3'], 'после сбоя связи дальше не шлём')
	assert.deepEqual(sent.map((x) => x.item.itemId), ['q1'])
	assert.equal(sent[0].res.correct, true)
	assert.deepEqual(dropped.map((x) => x.item.itemId), ['q2'])
	assert.deepEqual(kept.map((x) => x.itemId), ['q3', 'q4'])
})

test('пауза между попытками растёт до минуты', () => {
	let d = 0
	const seen = []
	for (let i = 0; i < 8; i++) seen.push((d = nextDelay(d)))
	assert.deepEqual(seen, [3000, 6000, 12000, 24000, 48000, 60000, 60000, 60000])
})
