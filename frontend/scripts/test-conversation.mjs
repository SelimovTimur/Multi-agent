// test-conversation.mjs — разговор с аватаром по ходам без браузера и сервера.
//
// Запуск: node scripts/test-conversation.mjs
//
// Проверяется то, на чём держится договор ученика (docs/product/avatar-goals/
// contract.md, разделы 5–7, и истории Уч-1…Уч-5): когда показывать
// распознанное, что считается ответом, как ответ переживает сбой и
// перезагрузку, и что попадает в итог.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
	answersLeft,
	asrNextStep,
	classifySendError,
	clearPending,
	createGate,
	deliverAnswer,
	dismissTurnsHint,
	FAREWELL_MESSAGE,
	LIVE_BROWSER_MESSAGE,
	LIVE_OFF_MESSAGE,
	fileNameFor,
	formatClock,
	incidentLine,
	initialState,
	leftLabel,
	loadPending,
	makeAnswer,
	newClientMsgId,
	pendingKey,
	readAsr,
	readSummary,
	reducer,
	resolvePending,
	savePending,
	shouldReview,
	SUMMARY_MAX_ATTEMPTS,
	summaryDelay,
	takeTurnsHintVisit,
	TURNS_HINT_VISITS,
	turnKey,
} from '../src/cabinet/student/conversation/state.js'

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/

function memoryStorage() {
	const data = new Map()
	return {
		data,
		getItem: (k) => (data.has(k) ? data.get(k) : null),
		setItem: (k, v) => data.set(k, String(v)),
		removeItem: (k) => data.delete(k),
	}
}

const BASE_TURNS = [
	{ turn_no: 0, role: 'avatar', content: 'Исәнме! Син кайда яшисең?', translation: 'Привет! Где ты живёшь?', input: 'text', edited: false },
	{ turn_no: 1, role: 'student', content: 'Мин Казанда яшим', translation: '', input: 'voice', edited: false },
	{ turn_no: 1, role: 'avatar', content: 'Яхшы! Ә син кем булып эшлисең?', translation: 'Хорошо! А кем ты работаешь?', input: 'text', edited: false },
]

/** GET …/conversation по договору: три ответа, один уже отправлен. */
function conversation(over = {}) {
	return {
		enabled: true,
		language: 'tatar',
		level: 'a2',
		status: 'active',
		max_turns: 3,
		turns_taken: 1,
		turns: BASE_TURNS,
		pending_answer: null,
		report_status: '',
		dictionary_enabled: false,
		phrases: [],
		...over,
	}
}

function loaded(res, stored = null) {
	return reducer(initialState(), { type: 'loaded', res, restored: resolvePending(stored, res) })
}

function apiError(status, code, message) {
	return Object.assign(new Error(message), { status, code })
}

const reply = (n, over = {}) => ({
	reply: 'Бик яхшы!',
	translation: 'Очень хорошо!',
	turn_no: n,
	turns_taken: n,
	max_turns: 3,
	status: 'active',
	report_status: '',
	...over,
})

test('уверенное распознавание уходит сразу, неуверенное — на проверку', () => {
	const sure = readAsr({ text: 'Мин Казанда яшим', confidence: 0.93, needs_review: false, review_reasons: [] })
	assert.equal(asrNextStep(sure), 'send')

	const unsure = readAsr({ text: 'Мин Казанда яшим', confidence: 0.41, needs_review: true, review_reasons: ['low_confidence'] })
	assert.equal(asrNextStep(unsure), 'review')
	assert.deepEqual(unsure.reasons, ['low_confidence'])

	// Сервер не сообщил needs_review — проверку показываем: лишний раз
	// спросить дешевле, чем засчитать ошибку распознавания.
	assert.equal(shouldReview({ text: 'Мин' }), true)
	assert.equal(asrNextStep(readAsr({ text: 'Мин' })), 'review')

	// Ничего не расслышали — это не проверка, а «Ошибка распознавания».
	assert.equal(asrNextStep(readAsr({ text: '   ', needs_review: false })), 'empty')

	// Локальная модель уверенности не даёт: null, а не ноль.
	const local = readAsr({ text: 'Сәлам', confidence: null, needs_review: true, review_reasons: ['no_confidence'] })
	assert.equal(local.confidence, null)
	assert.equal(asrNextStep(local), 'review')
})

test('ответ голосом и текстом: input, asr_text, asr_confidence и edited — по факту', () => {
	const asr = readAsr({ text: 'Мин Казанда яшим', confidence: 0.71, needs_review: true })

	assert.deepEqual(makeAnswer({ text: asr.text, input: 'voice', asr, clientMsgId: 'a', turnNo: 2 }), {
		clientMsgId: 'a',
		turnNo: 2,
		text: 'Мин Казанда яшим',
		input: 'voice',
		asrText: 'Мин Казанда яшим',
		asrConfidence: 0.71,
		edited: false,
	})

	const fixed = makeAnswer({ text: 'Мин Казанда яшим.', input: 'voice', asr, clientMsgId: 'b', turnNo: 2 })
	assert.equal(fixed.edited, true)
	assert.equal(fixed.asrText, 'Мин Казанда яшим')

	// Открыл правку и ничего не поменял — это не исправление.
	assert.equal(makeAnswer({ text: ' Мин Казанда яшим ', input: 'voice', asr, clientMsgId: 'c', turnNo: 2 }).edited, false)

	assert.deepEqual(makeAnswer({ text: ' Сәлам ', input: 'text', clientMsgId: 'd', turnNo: 2 }), {
		clientMsgId: 'd',
		turnNo: 2,
		text: 'Сәлам',
		input: 'text',
		asrText: null,
		asrConfidence: null,
		edited: false,
	})
})

test('неотправленный ответ хранится по попытке и переживает битое хранилище', () => {
	const storage = memoryStorage()
	const p = makeAnswer({ text: 'Мин укытучы', input: 'text', clientMsgId: 'id-1', turnNo: 2 })
	savePending(storage, 'att-1', p)
	assert.ok(storage.data.has(pendingKey('att-1')))
	assert.deepEqual(loadPending(storage, 'att-1'), p)
	// Ответ одного разговора не всплывает в другом.
	assert.equal(loadPending(storage, 'att-2'), null)
	clearPending(storage, 'att-1')
	assert.equal(loadPending(storage, 'att-1'), null)

	storage.setItem(pendingKey('att-1'), '{битый')
	assert.equal(loadPending(storage, 'att-1'), null)
	storage.setItem(pendingKey('att-1'), JSON.stringify({ clientMsgId: '', turnNo: 2, text: 'x', input: 'text' }))
	assert.equal(loadPending(storage, 'att-1'), null)

	// Приватный режим: хранилище бросает — разговор не падает.
	const throwing = {
		getItem() {
			throw new Error('denied')
		},
		setItem() {
			throw new Error('denied')
		},
		removeItem() {
			throw new Error('denied')
		},
	}
	assert.doesNotThrow(() => savePending(throwing, 'x', p))
	assert.equal(loadPending(throwing, 'x'), null)
	assert.doesNotThrow(() => clearPending(throwing, 'x'))
	assert.equal(loadPending(null, 'x'), null)
})

test('после перезагрузки разговор продолжается с того же места', () => {
	// Сервер сохранил второй ответ, а аватар на него не ответил.
	const said = { turn_no: 2, role: 'student', content: 'Мин укытучы', translation: '', input: 'voice', edited: true }
	const res = conversation({
		turns_taken: 2,
		turns: [...BASE_TURNS, said],
		pending_answer: { client_msg_id: 'id-2', turn_no: 2, text: 'Мин укытучы' },
	})
	const asr = readAsr({ text: 'Мин укытуча', confidence: 0.5, needs_review: true })
	const stored = makeAnswer({ text: 'Мин укытучы', input: 'voice', asr, clientMsgId: 'id-2', turnNo: 2 })

	const same = resolvePending(stored, res)
	assert.equal(same.saved, true)
	// Та же запись во вкладке — берём её целиком, с распознанным текстом.
	assert.deepEqual(same.pending, stored)

	const s = reducer(initialState(), { type: 'loaded', res, restored: same })
	assert.equal(s.phase, 'active')
	assert.equal(s.composer, 'send_error')
	assert.equal(s.sendError.kind, 'resume')
	assert.equal(s.pending.clientMsgId, 'id-2')
	assert.equal(s.turnsTaken, 2)
	// Пузырь ответа один — сервер его уже прислал.
	assert.equal(s.turns.filter((t) => t.key === turnKey('student', 2)).length, 1)

	// Другое устройство: во вкладке ничего нет — ответ собирается из pending_answer.
	const fromServer = resolvePending(null, res)
	assert.equal(fromServer.saved, true)
	assert.equal(fromServer.pending.clientMsgId, 'id-2')
	assert.equal(fromServer.pending.text, 'Мин укытучы')
	assert.equal(fromServer.pending.input, 'voice')
	assert.equal(fromServer.pending.edited, true)

	// Запрос не дошёл: сервер о нём не знает, вкладка помнит.
	const unsent = makeAnswer({ text: 'Мин укытучы', input: 'text', clientMsgId: 'id-x', turnNo: 2 })
	const plain = conversation()
	const lost = resolvePending(unsent, plain)
	assert.equal(lost.saved, false)
	assert.deepEqual(lost.pending, unsent)
	const u = reducer(initialState(), { type: 'loaded', res: plain, restored: lost })
	assert.equal(u.composer, 'send_error')
	assert.equal(u.sendError.kind, 'unsent')
	assert.equal(u.turns.at(-1).local, 'unsent')
	// Не дошедший ответ не засчитан.
	assert.equal(u.turnsTaken, 1)

	// Устаревшее не восстанавливается: ход засчитан, разговор закрыт или не начат.
	assert.equal(resolvePending({ ...unsent, turnNo: 1 }, plain).pending, null)
	assert.equal(resolvePending(unsent, conversation({ status: 'closed' })).pending, null)
	assert.equal(resolvePending(unsent, conversation({ status: 'not_started', turns_taken: 0, turns: [] })).pending, null)
})

test('подсказка показывает текст сразу и ответом не считается', () => {
	const s = loaded(conversation())
	const first = turnKey('avatar', 1)
	assert.equal(s.hints[first], undefined)

	const h = reducer(s, { type: 'hint', key: first })
	assert.equal(h.hints[first], true)
	// Ни счёт, ни панель ответа, ни отправка подсказка не трогает.
	assert.equal(h.turnsTaken, s.turnsTaken)
	assert.equal(h.composer, s.composer)
	assert.equal(h.pending, null)
	assert.equal(reducer(h, { type: 'hint', key: first }).hints[first], false)

	// Следующая реплика приходит снова без текста.
	const p = makeAnswer({ text: 'Мин укытучы', input: 'text', clientMsgId: 'n', turnNo: 2 })
	let n = reducer(h, { type: 'send', pending: p })
	n = reducer(n, { type: 'answered', pending: p, res: reply(2) })
	assert.equal(n.hints[turnKey('avatar', 2)], undefined)
	assert.equal(n.turns.at(-1).content, 'Бик яхшы!')
	assert.equal(n.turns.at(-1).translation, 'Очень хорошо!')
})

test('«Осталось N ответов» — только отправленные ответы', () => {
	let s = loaded(conversation())
	assert.equal(answersLeft(s.maxTurns, s.turnsTaken), 2)

	// Набранный, но не отправленный текст ответа не тратит.
	s = reducer(s, { type: 'typed', text: 'Мин укытучы' })
	assert.equal(answersLeft(s.maxTurns, s.turnsTaken), 2)

	const p = makeAnswer({ text: s.typed, input: 'text', clientMsgId: 'e', turnNo: 2 })
	s = reducer(s, { type: 'send', pending: p })
	assert.equal(answersLeft(s.maxTurns, s.turnsTaken), 2)
	s = reducer(s, { type: 'answered', pending: p, res: reply(2) })
	assert.equal(answersLeft(s.maxTurns, s.turnsTaken), 1)
	assert.equal(s.composer, 'text')

	assert.equal(leftLabel(1), 'Остался 1 ответ')
	assert.equal(leftLabel(2), 'Осталось 2 ответа')
	assert.equal(leftLabel(5), 'Осталось 5 ответов')
	assert.equal(leftLabel(11), 'Осталось 11 ответов')
	assert.equal(leftLabel(21), 'Остался 21 ответ')
	assert.equal(answersLeft(0, 3), 0)
	assert.equal(answersLeft(3, 5), 0)
})

test('«Аватар недоступен»: ответ сохранён, «Повторить» шлёт тот же идентификатор', async () => {
	const storage = memoryStorage()
	let state = loaded(conversation())
	const dispatch = (action) => {
		state = reducer(state, action)
	}
	const sent = []
	let fail = true
	const send = async (p) => {
		sent.push(p.clientMsgId)
		if (fail) throw apiError(503, 'avatar_unavailable', 'Аватар не смог ответить. Твой ответ сохранён — нажми «Повторить»')
		return reply(2, { reply: 'Яхшы', translation: 'Хорошо' })
	}
	const p = makeAnswer({ text: 'Мин укытучы', input: 'text', clientMsgId: 'id-1', turnNo: 2 })

	const first = await deliverAnswer({ send, storage, attemptId: 'att', pending: p, dispatch })
	assert.equal(first.ok, false)
	assert.equal(state.composer, 'send_error')
	assert.equal(state.sendError.kind, 'unavailable')
	assert.match(state.sendError.message, /Повторить/)
	// Сервер записал и засчитал ответ до вызова модели.
	assert.equal(state.turnsTaken, 2)
	assert.equal(state.turns.at(-1).local, 'saved')
	// До успеха ответ лежит во вкладке.
	assert.deepEqual(loadPending(storage, 'att'), p)

	fail = false
	const second = await deliverAnswer({ send, storage, attemptId: 'att', pending: state.pending, dispatch })
	assert.equal(second.ok, true)
	assert.deepEqual(sent, ['id-1', 'id-1'])
	assert.equal(loadPending(storage, 'att'), null)
	assert.equal(state.composer, 'text')
	assert.equal(state.pending, null)
	assert.equal(state.turns.filter((t) => t.key === turnKey('student', 2)).length, 1)
	assert.equal(state.turns.find((t) => t.key === turnKey('student', 2)).local, '')
	assert.equal(state.turns.at(-1).content, 'Яхшы')
})

test('обрыв связи не теряет ответ, отказ сервера возвращает текст в поле', async () => {
	const p = makeAnswer({ text: 'Мин укытучы', input: 'text', clientMsgId: 'id-n', turnNo: 2 })

	// Нет связи: ответ хранится, не засчитан, ждёт «Повторить».
	{
		const storage = memoryStorage()
		let state = loaded(conversation())
		const dispatch = (a) => (state = reducer(state, a))
		const send = async () => {
			throw apiError(0, 'network', 'Нет связи с сервером. Проверьте интернет')
		}
		await deliverAnswer({ send, storage, attemptId: 'att', pending: p, dispatch })
		assert.equal(state.composer, 'send_error')
		assert.equal(state.sendError.kind, 'network')
		assert.equal(state.turnsTaken, 1)
		assert.equal(state.turns.at(-1).local, 'unsent')
		assert.deepEqual(loadPending(storage, 'att'), p)
	}

	// Сервер не принял текст: хранить нечего, текст — обратно в поле.
	{
		const storage = memoryStorage()
		let state = loaded(conversation())
		const dispatch = (a) => (state = reducer(state, a))
		const send = async () => {
			throw apiError(400, 'validation_failed', 'Ответ не длиннее 1000 символов')
		}
		await deliverAnswer({ send, storage, attemptId: 'att', pending: p, dispatch })
		assert.equal(state.composer, 'text')
		assert.equal(state.typed, 'Мин укытучы')
		assert.equal(state.notice, 'Ответ не длиннее 1000 символов')
		assert.equal(state.turns.some((t) => t.key === turnKey('student', 2)), false)
		assert.equal(loadPending(storage, 'att'), null)
	}

	// 409 — разговор уже в другом состоянии: ответ снимается, экран перечитает.
	assert.equal(classifySendError(apiError(409, 'conflict', 'Разговор закончен')).action, 'send_conflict')
	assert.equal(classifySendError(apiError(409, 'conflict', 'Разговор закончен')).drop, true)
	// Перегрузка и таймаут — повторяемы.
	assert.equal(classifySendError(apiError(429, 'rate_limited', 'Подождите')).drop, false)
	assert.equal(classifySendError(apiError(0, 'timeout', 'Сервер долго не отвечает')).kind, 'network')
	assert.equal(classifySendError(apiError(502, 'internal_error', '')).kind, 'network')
})

test('двойное нажатие не отправляет ответ дважды', async () => {
	const gate = createGate()
	let calls = 0
	let release
	const slow = () =>
		new Promise((resolve) => {
			calls += 1
			release = resolve
		})
	const first = gate.run(slow)
	const second = gate.run(slow)
	assert.equal(gate.busy, true)
	assert.equal(await second, undefined)
	release('ok')
	assert.equal(await first, 'ok')
	assert.equal(calls, 1)

	// Замок отпускается и после успеха, и после ошибки.
	await gate.run(async () => {
		calls += 1
	})
	assert.equal(calls, 2)
	await assert.rejects(
		gate.run(async () => {
			throw new Error('сбой')
		}),
	)
	assert.equal(gate.busy, false)
})

test('последний ответ закрывает разговор, итог готовится', () => {
	let s = loaded(conversation({ turns_taken: 2 }))
	const p = makeAnswer({ text: 'Сау бул!', input: 'text', clientMsgId: 'last', turnNo: 3 })
	s = reducer(s, { type: 'send', pending: p })
	s = reducer(s, { type: 'answered', pending: p, res: reply(3, { reply: 'Рәхмәт! Сау бул!', status: 'closed', report_status: 'pending' }) })
	assert.equal(s.phase, 'closed')
	assert.equal(s.summary.status, 'waiting')
	assert.equal(s.turns.at(-1).content, 'Рәхмәт! Сау бул!')

	// Аватар не смог попрощаться — разговор всё равно закрыт: ответ записан.
	let f = loaded(conversation({ turns_taken: 2 }))
	f = reducer(f, { type: 'send', pending: p })
	f = reducer(f, { type: 'send_failed', kind: 'unavailable', message: 'Аватар не смог ответить', pending: p })
	assert.equal(f.phase, 'closed')
	assert.equal(f.summary.status, 'waiting')
	assert.equal(f.notice, FAREWELL_MESSAGE)
	assert.equal(f.pending, null)

	// Закрытый разговор после перезагрузки сразу ждёт итог.
	const c = loaded(conversation({ status: 'closed', turns_taken: 3 }))
	assert.equal(c.phase, 'closed')
	assert.equal(c.summary.status, 'waiting')
})

test('итог: зачтённые фразы, применённые темы, одна вещь и сбои отдельно', () => {
	const res = {
		status: 'ready',
		summary: {
			credited_phrases: [{ key: 'p1', title: 'Минем исемем …' }],
			applied_themes: [{ key: 'g1', title: 'Местный падеж -да/-дә' }],
			phrases_total: 3,
			themes_total: 1,
			next_time: { text_ru: 'Скажи, где ты живёшь, с окончанием -да/-дә', example: 'Мин Казанда яшим' },
			incidents: [
				{ kind: 'asr', count: 1 },
				{ kind: 'tts', count: 2 },
			],
		},
	}
	const d = readSummary(res)
	assert.deepEqual(d.credited, ['Минем исемем …'])
	assert.equal(d.phrasesTotal, 3)
	assert.deepEqual(d.applied, ['Местный падеж -да/-дә'])
	assert.equal(d.themesTotal, 1)
	assert.deepEqual(d.nextTime, { text: 'Скажи, где ты живёшь, с окончанием -да/-дә', example: 'Мин Казанда яшим' })
	assert.equal(incidentLine(d.incidents), 'распознавание речи — 1, озвучка — 2')
	// Списка ошибок в итоге нет и взяться ему неоткуда.
	assert.deepEqual(Object.keys(d).sort(), ['applied', 'credited', 'incidents', 'nextTime', 'phrasesTotal', 'themesTotal'])

	assert.equal(readSummary({ summary: { next_time: null } }).nextTime, null)
	assert.equal(incidentLine([]), '')

	let s = loaded(conversation({ status: 'closed', turns_taken: 3 }))
	s = reducer(s, { type: 'summary', res: { status: 'pending' } })
	assert.equal(s.summary.status, 'waiting')
	s = reducer(s, { type: 'summary', res: { status: 'failed' } })
	assert.equal(s.summary.failed, true)
	s = reducer(s, { type: 'summary_slow' })
	assert.equal(s.summary.status, 'slow')
	s = reducer(s, { type: 'summary_retry' })
	assert.equal(s.summary.status, 'waiting')
	s = reducer(s, { type: 'summary', res })
	assert.equal(s.summary.status, 'ready')
	assert.equal(s.summary.data.credited.length, 1)
})

test('опрос итога: сначала часто, потом реже, с пределом около трёх минут', () => {
	assert.equal(summaryDelay(1), 1500)
	assert.equal(summaryDelay(2), 2000)
	assert.equal(summaryDelay(5), 5000)
	assert.equal(summaryDelay(39), 5000)
	assert.equal(summaryDelay(3, 'failed'), 15000)
	let total = 0
	for (let attempt = 1; attempt < SUMMARY_MAX_ATTEMPTS; attempt++) total += summaryDelay(attempt)
	assert.ok(total >= 120000 && total <= 240000, `опрос длится ${total} мс`)
})

test('сбой озвучки открывает текст реплики', () => {
	const key = turnKey('avatar', 1)
	let s = loaded(conversation())
	s = reducer(s, { type: 'audio_loading', key })
	assert.equal(s.voices[key], 'loading')
	s = reducer(s, { type: 'audio_failed', key })
	assert.equal(s.voices[key], 'failed')
	assert.equal(s.hints[key], true)

	const other = turnKey('avatar', 0)
	s = reducer(s, { type: 'audio_loading', key: other })
	s = reducer(s, { type: 'audio_ready', key: other })
	assert.equal(s.voices[other], undefined)
	assert.equal(s.hints[other], undefined)
})

test('голосовых сообщений нет: без голосового разговора — ввод текстом и причина', () => {
	// Сервер голосовой разговор не дал — панель ответа сразу текстовая, а
	// над ней причина и «Повторить».
	let s = loaded(conversation())
	assert.equal(s.mode, 'text')
	assert.equal(s.composer, 'text')
	assert.equal(s.live.unavailable, LIVE_OFF_MESSAGE)

	// После ответа панель снова текстовая.
	const p = makeAnswer({ text: 'Мин укытучы', input: 'text', clientMsgId: 't', turnNo: 2 })
	s = reducer(s, { type: 'typed', text: 'Мин укытучы' })
	s = reducer(s, { type: 'send', pending: p })
	s = reducer(s, { type: 'answered', pending: p, res: reply(2) })
	assert.equal(s.composer, 'text')
	assert.equal(s.typed, '')

	// Записи и распознавания у экрана больше нет: их действия ничего не меняют.
	for (const type of ['arming', 'record_start', 'record_done', 'asr_review', 'asr_failed', 'voice_mode', 'voice_unsupported']) {
		assert.equal(reducer(s, { type, asr: { text: 'x' } }), s, type)
	}

	// Браузер не умеет голосовой разговор — своя причина.
	const old = reducer(initialState(), {
		type: 'loaded',
		res: conversation({ live_available: true }),
		restored: { pending: null, saved: false },
		liveSupported: false,
	})
	assert.equal(old.mode, 'text')
	assert.equal(old.live.unavailable, LIVE_BROWSER_MESSAGE)
})

test('микрофон: «Не сейчас» — ввод текстом по выбору, отказ — ввод текстом с причиной', () => {
	const live = () =>
		reducer(initialState(), {
			type: 'loaded',
			res: conversation({ live_available: true }),
			restored: { pending: null, saved: false },
			liveSupported: true,
		})
	let s = reducer(live(), { type: 'mic_ask' })
	assert.equal(s.micPrompt, 'ask')
	s = reducer(s, { type: 'text_mode' })
	assert.deepEqual([s.micPrompt, s.mode, s.composer, s.live.unavailable], [null, 'text', 'text', ''])

	s = reducer(live(), { type: 'mic_denied' })
	assert.equal(s.micPrompt, 'denied')
	s = reducer(s, { type: 'text_mode' })
	assert.equal(s.mode, 'text')
	assert.match(s.live.unavailable, /микрофон/)
	assert.match(reducer(reducer(live(), { type: 'mic_missing' }), { type: 'text_mode' }).live.unavailable, /не найден/)
})

test('разговор, которого нет или который не начат', () => {
	assert.equal(loaded(conversation({ enabled: false })).phase, 'disabled')
	const intro = loaded(conversation({ status: 'not_started', turns_taken: 0, turns: [] }))
	assert.equal(intro.phase, 'intro')

	let s = reducer(intro, { type: 'start' })
	assert.equal(s.starting, true)
	s = reducer(s, {
		type: 'started',
		res: { reply: 'Исәнме!', translation: 'Привет!', turn_no: 0, turns_taken: 0, max_turns: 3, status: 'active' },
	})
	assert.equal(s.phase, 'active')
	assert.equal(s.turns.length, 1)
	assert.equal(s.turns[0].key, turnKey('avatar', 0))
	// Повторный старт отдаёт ту же реплику — второй не появляется.
	s = reducer(s, {
		type: 'started',
		res: { reply: 'Исәнме!', translation: 'Привет!', turn_no: 0, turns_taken: 0, max_turns: 3, status: 'active' },
	})
	assert.equal(s.turns.length, 1)
})

test('мелочи: идентификатор без randomUUID, имя файла, время', () => {
	assert.equal(newClientMsgId({ randomUUID: () => 'uuid-1' }), 'uuid-1')
	// Пилот работает и по http: там randomUUID нет, а getRandomValues есть.
	assert.match(
		newClientMsgId({
			getRandomValues: (bytes) => {
				for (let i = 0; i < bytes.length; i++) bytes[i] = (i * 37) & 255
				return bytes
			},
		}),
		UUID,
	)
	assert.match(newClientMsgId({}), UUID)
	assert.notEqual(newClientMsgId(), newClientMsgId())

	assert.equal(fileNameFor('audio/webm;codecs=opus'), 'voice.webm')
	assert.equal(fileNameFor('audio/mp4'), 'voice.m4a')
	assert.equal(fileNameFor('audio/ogg;codecs=opus'), 'voice.ogg')
	assert.equal(fileNameFor(''), 'voice.webm')

	assert.equal(formatClock(0), '0:00')
	assert.equal(formatClock(12.7), '0:12')
	assert.equal(formatClock(75), '1:15')
})

test('клиент API: ответ, сбой, итог и язык распознавания — по договору', async () => {
	const calls = []
	let next = { ok: true, status: 200, body: {} }
	globalThis.fetch = async (url, init = {}) => {
		calls.push({ url, init })
		const { ok, status, body } = next
		return { ok, status, headers: { get: () => null }, json: async () => body }
	}
	const { my, speech } = await import('../src/api/v1.js')

	const asr = readAsr({ text: 'Мин Казанда яшим', confidence: 0.71, needs_review: true })
	await my.answerConversation('att', makeAnswer({ text: 'Мин Казанда яшим.', input: 'voice', asr, clientMsgId: 'id-7', turnNo: 3 }))
	assert.equal(calls[0].url, '/api/v1/my/attempts/att/conversation/answer')
	assert.equal(calls[0].init.method, 'POST')
	assert.deepEqual(JSON.parse(calls[0].init.body), {
		client_msg_id: 'id-7',
		text: 'Мин Казанда яшим.',
		input: 'voice',
		asr_text: 'Мин Казанда яшим',
		asr_confidence: 0.71,
		edited: true,
	})

	// У набранного ответа распознавания не было — и полей о нём нет.
	await my.answerConversation('att', makeAnswer({ text: 'Сәлам', input: 'text', clientMsgId: 'id-8', turnNo: 4 }))
	assert.deepEqual(JSON.parse(calls[1].init.body), { client_msg_id: 'id-8', text: 'Сәлам', input: 'text', edited: false })

	await my.conversationIncident('att', { kind: 'tts', turnNo: 2, message: 'Синтез недоступен' })
	assert.equal(calls[2].url, '/api/v1/my/attempts/att/conversation/incident')
	assert.deepEqual(JSON.parse(calls[2].init.body), { kind: 'tts', turn_no: 2, message: 'Синтез недоступен' })
	await my.conversationIncident('att', { kind: 'asr' })
	assert.deepEqual(JSON.parse(calls[3].init.body), { kind: 'asr' })

	await speech.transcribeMine(new File([new Uint8Array(4000)], 'voice.webm', { type: 'audio/webm' }), { language: 'tatar' })
	assert.equal(calls[4].url, '/api/v1/my/transcribe')
	assert.equal(calls[4].init.body.get('language'), 'tatar')
	assert.ok(calls[4].init.body.get('file'))

	await my.conversation('att')
	assert.equal(calls[5].url, '/api/v1/my/attempts/att/conversation')
	await my.startConversation('att')
	assert.equal(calls[6].url, '/api/v1/my/attempts/att/conversation/start')
	assert.equal(calls[6].init.method, 'POST')
	await my.conversationSummary('att')
	assert.equal(calls[7].url, '/api/v1/my/attempts/att/conversation/summary')

	// 503 avatar_unavailable доходит до экрана с кодом: по нему — «Повторить».
	next = {
		ok: false,
		status: 503,
		body: { error: { code: 'avatar_unavailable', message: 'Аватар не смог ответить. Твой ответ сохранён — нажми «Повторить»' } },
	}
	const err = await my
		.answerConversation('att', makeAnswer({ text: 'Сәлам', input: 'text', clientMsgId: 'id-9', turnNo: 4 }))
		.catch((e) => e)
	const outcome = classifySendError(err)
	assert.equal(outcome.action, 'send_failed')
	assert.equal(outcome.kind, 'unavailable')
	assert.equal(outcome.drop, false)
	assert.match(outcome.message, /Повторить/)
})

// ---------------------------------------------------------------------------
// Обращение: пробный разговор учителя — тот же экран на «вы»
// ---------------------------------------------------------------------------

import { readFileSync } from 'node:fs'
import { polite, POLITE_KEYS } from '../src/cabinet/student/conversation/address.js'
import { liveReviewHint } from '../src/cabinet/student/conversation/live.js'

// Слова обращения на «ты». Строка экрана с ними без вежливой пары — это
// «ты», которое увидит учитель.
const INFORMAL = /(^|[^а-яё])(ты|тебя|тебе|тобой|твой|твоя|твоё|Твой|[Нн]ажми|[Пп]опробуй|[Пп]роверь|[Сс]кажи|[Гг]овори|[Нн]апиши|[Ии]справь|[Рр]азреши|[Пп]одключи|отвечай|[Дд]оговоришь)([^а-яё]|$)/

test('у каждой строки экрана на «ты» есть пара на «вы»', () => {
	const files = ['parts.jsx', 'state.js', 'live.js'].map((f) =>
		readFileSync(new URL(`../src/cabinet/student/conversation/${f}`, import.meta.url), 'utf8'),
	)
	const keys = new Set(POLITE_KEYS)
	// Части подсказки живого разговора собираются отдельно (live.js) — их
	// переводит polite целиком, проверка ниже.
	const assembled = new Set(['Проверь, правильно ли мы тебя услышали.'])
	const missing = []
	for (const src of files) {
		for (const raw of src.split('\n')) {
			const code = raw.replace(/^\s*(\/\/|\*).*$/, '')
			for (const m of code.matchAll(/'([^'\n]+)'/g)) {
				const text = m[1]
				if (INFORMAL.test(text) && !keys.has(text) && !assembled.has(text)) missing.push(text)
			}
		}
	}
	assert.deepEqual(missing, [])
})

test('polite: учителю — «вы», строки без обращения — как есть', () => {
	assert.equal(polite('Напиши ответ'), 'Напишите ответ')
	assert.equal(polite('Напиши ответ', false), 'Напиши ответ')
	assert.equal(polite('Аватар отвечает…'), 'Аватар отвечает…')
	assert.equal(polite(''), '')
	assert.equal(polite(undefined), undefined)
	assert.match(polite(liveReviewHint([])), /^Проверьте, правильно ли мы вас услышали\. Если ничего не нажимать/)
	assert.match(polite(liveReviewHint(['quiet_audio'])), /^Запись получилась тихой — проверьте/)
})

test('подсказка к счётчику ответов: первые три захода, «Понятно» гасит', () => {
	const store = () => {
		const m = new Map()
		return { getItem: (k) => m.get(k) ?? null, setItem: (k, v) => m.set(k, String(v)) }
	}
	const s = store()
	const shown = Array.from({ length: TURNS_HINT_VISITS + 2 }, () => takeTurnsHintVisit(s))
	assert.deepEqual(shown, [true, true, true, false, false])

	const early = store()
	assert.equal(takeTurnsHintVisit(early), true)
	dismissTurnsHint(early)
	assert.equal(takeTurnsHintVisit(early), false)

	// Хранилища нет или оно бросает — подсказка показывается, разговор не падает.
	assert.equal(takeTurnsHintVisit(null), true)
	const broken = { getItem: () => { throw new Error('denied') }, setItem: () => { throw new Error('denied') } }
	assert.equal(takeTurnsHintVisit(broken), true)
	assert.doesNotThrow(() => dismissTurnsHint(broken))
})
