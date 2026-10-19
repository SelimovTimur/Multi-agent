// test-conversation-live.mjs — живой режим разговора урока без браузера и сервера.
//
// Запуск: node scripts/test-conversation-live.mjs
//
// Проверяется то, на чём держится живой режим (docs/product/avatar-goals/
// contract.md, раздел 10; поведение — student-ui.md, «Живой режим»):
// нарезка фраз на синтетическом сигнале и WAV для распознавания; какая
// фраза становится ответом; очередь реплик и их номера, в том числе номер
// указания (steer_turn); указания сессии и инструменты делегата на
// поддельном канале — с настоящим LiveConnection; прощание, сбой сессии,
// уход со страницы; редьюсер ленты и клиент API.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { ApiError } from '../src/api/v1.js'
import { LiveConnection } from '../src/cabinet/live/connection.js'
import {
	closingAnswer,
	FAREWELL_GUIDANCE,
	INTRO_GUIDANCE,
	LiveLedger,
	liveAsrStep,
	liveReviewHint,
	meaningful,
	REVIEW_MS,
	steerContent,
} from '../src/cabinet/student/conversation/live.js'
import { createAudioContext, INTRO_WAIT_MS, liveBlocker, LiveTalk, liveSupported, micErrorKind } from '../src/cabinet/student/conversation/liveTalk.js'
import {
	answersLeft,
	canRetryLive,
	canSwitchMode,
	initialState,
	leftLabel,
	LIVE_BROWSER_MESSAGE,
	LIVE_INSECURE_MESSAGE,
	LIVE_OFF_MESSAGE,
	liveKey,
	livePendingNote,
	makeAnswer,
	pendingKey,
	readAsr,
	readTurns,
	reducer,
} from '../src/cabinet/student/conversation/state.js'
import {
	encodeWav,
	levelOf,
	phraseFile,
	PHRASE,
	resample,
	Segmenter,
	startCapture,
} from '../src/cabinet/student/conversation/utterances.js'

/* ---------------------------------------------------------------------------
 * Синтетический сигнал
 * ------------------------------------------------------------------------- */

const RATE = 48000
const samplesOf = (ms) => Math.round((RATE * ms) / 1000)

/** Фон: шум с постоянной громкостью (RMS ≈ amp / √3). */
function quiet(ms, amp = 0.002, seed = 7) {
	const out = new Float32Array(samplesOf(ms))
	let x = seed
	for (let i = 0; i < out.length; i++) {
		x = (x * 1103515245 + 12345) & 0x7fffffff
		out[i] = ((x / 0x7fffffff) * 2 - 1) * amp
	}
	return out
}

/** «Речь»: голос 180 Гц с обертоном, громкость дышит по слогам (160 мс громко, 40 мс тише). */
function speech(ms, amp = 0.25) {
	const out = new Float32Array(samplesOf(ms))
	for (let i = 0; i < out.length; i++) {
		const t = i / RATE
		const syllable = (t * 1000) % 200 < 160 ? 1 : 0.35
		out[i] = amp * syllable * (0.7 * Math.sin(2 * Math.PI * 180 * t) + 0.3 * Math.sin(2 * Math.PI * 360 * t))
	}
	return out
}

/** Щелчок или стук: короткий громкий тон. */
function click(ms, amp = 0.6) {
	const out = new Float32Array(samplesOf(ms))
	for (let i = 0; i < out.length; i++) out[i] = amp * Math.sin((2 * Math.PI * 1000 * i) / RATE)
	return out
}

function join(...parts) {
	const out = new Float32Array(parts.reduce((n, p) => n + p.length, 0))
	let at = 0
	for (const p of parts) {
		out.set(p, at)
		at += p.length
	}
	return out
}

/** Кусками, как их отдаёт AudioWorklet. */
function feed(target, signal, chunk = 2048) {
	for (let i = 0; i < signal.length; i += chunk) target.push(signal.subarray(i, i + chunk))
}

function segmenter(options = {}) {
	const log = { starts: [], phrases: [], drops: [] }
	const seg = new Segmenter({
		sampleRate: RATE,
		onStart: (p) => log.starts.push(p),
		onPhrase: (p) => log.phrases.push(p),
		onDrop: (p) => log.drops.push(p),
		...options,
	})
	return { seg, log }
}

/* ---------------------------------------------------------------------------
 * Нарезка фраз
 * ------------------------------------------------------------------------- */

test('фраза: начало по голосу с предзахватом, конец — по паузе около секунды', () => {
	const { seg, log } = segmenter()
	feed(seg, join(quiet(1000), speech(1500)))
	assert.equal(log.starts.length, 1)
	assert.equal(log.phrases.length, 0)

	// Пауза короче 0,9 с — ученик раздумывает, фраза не закончилась.
	feed(seg, quiet(800))
	assert.equal(log.phrases.length, 0)
	feed(seg, quiet(200))
	assert.equal(log.phrases.length, 1)

	const p = log.phrases[0]
	assert.equal(p.sampleRate, RATE)
	assert.equal(p.cut, false)
	// Предзахват: фраза начинается не позже чем за 300 мс до голоса —
	// начало первого слова не теряется.
	assert.ok(p.startMs <= 1000 - PHRASE.preRollMs && p.startMs >= 1000 - 450, `startMs=${p.startMs}`)
	// Голос, предзахват и короткий хвост тишины, без всей паузы.
	assert.ok(p.durationMs >= 1500 + 300 && p.durationMs <= 1500 + 450 + 300, `durationMs=${p.durationMs}`)
	assert.ok(levelOf(p.samples, 0, samplesOf(200)) < 0.01, 'начало фразы — тишина предзахвата')
	assert.ok(levelOf(p.samples, samplesOf(600), samplesOf(1200)) > 0.05, 'середина — голос')
})

test('пауза внутри фразы короче секунды фразу не рвёт', () => {
	const { seg, log } = segmenter()
	feed(seg, join(quiet(500), speech(800), quiet(500), speech(800), quiet(1500)))
	assert.equal(log.starts.length, 1)
	assert.equal(log.phrases.length, 1)
	assert.ok(log.phrases[0].voicedMs >= 1600)
})

test('щелчок — не фраза, кашель — начало без фразы', () => {
	const tap = segmenter()
	feed(tap.seg, join(quiet(500), click(30), quiet(1500)))
	assert.equal(tap.log.starts.length, 0)

	const cough = segmenter()
	feed(cough.seg, join(quiet(500), speech(140), quiet(1500)))
	assert.equal(cough.log.starts.length, 1)
	assert.equal(cough.log.phrases.length, 0)
	assert.deepEqual(cough.log.drops.map((d) => d.reason), ['short'])
})

test('длинная речь режется по пределу, продолжение — следующая фраза', () => {
	const { seg, log } = segmenter()
	feed(seg, join(quiet(500), speech(30000), quiet(1500)))
	assert.equal(log.phrases.length, 2)
	assert.equal(log.phrases[0].cut, true)
	assert.equal(log.phrases[0].durationMs, PHRASE.maxPhraseMs)
	assert.equal(log.starts.length, 2)
	assert.equal(log.starts[1].continuation, true)
	assert.equal(log.phrases[1].cut, false)
	assert.ok(log.phrases[1].durationMs > 4500 && log.phrases[1].durationMs < 6500, `${log.phrases[1].durationMs}`)
})

test('порог подстраивается под фон: в шумном классе тихий голос фразой не становится', () => {
	// Тихая комната — тихий голос слышен.
	const calm = segmenter()
	feed(calm.seg, join(quiet(1000), speech(1000, 0.04), quiet(1500)))
	assert.equal(calm.log.phrases.length, 1)

	// Ровный шум с RMS около 0,012 — порог поднимается, тот же голос тонет.
	const noisy = segmenter()
	feed(noisy.seg, join(quiet(3000, 0.0208), speech(1000, 0.04), quiet(1500, 0.0208)))
	assert.equal(noisy.log.starts.length, 0)
	// Голос громче фона — фраза.
	feed(noisy.seg, join(speech(1000, 0.25), quiet(1500, 0.0208)))
	assert.equal(noisy.log.phrases.length, 1)
})

test('пока звучит аватар, порог выше; заговорил — фраза ученика закрывается сразу', () => {
	const duck = segmenter()
	duck.seg.setDucking(true)
	feed(duck.seg, join(quiet(500), speech(1000, 0.04), quiet(1500)))
	assert.equal(duck.log.starts.length, 0, 'эхо и тихий голос под аватаром — не фраза')
	// Перебить аватара можно — громче.
	feed(duck.seg, join(speech(800, 0.25), quiet(1500)))
	assert.equal(duck.log.phrases.length, 1)
	duck.seg.setDucking(false)
	feed(duck.seg, join(speech(1000, 0.04), quiet(1500)))
	assert.equal(duck.log.phrases.length, 2)

	// Ученик договорил и молчит 0,4 с, аватар уже отвечает — сессия решила,
	// что ученик закончил: фраза не ждёт полной паузы.
	const early = segmenter()
	feed(early.seg, join(quiet(500), speech(1000), quiet(400)))
	assert.equal(early.log.phrases.length, 0)
	early.seg.setDucking(true)
	assert.equal(early.log.phrases.length, 1)

	// Молчит меньше 0,3 с — это пауза внутри фразы, ждём дальше.
	const mid = segmenter()
	feed(mid.seg, join(quiet(500), speech(1000), quiet(200)))
	mid.seg.setDucking(true)
	assert.equal(mid.log.phrases.length, 0)
})

test('остановка захвата: недосказанное — фразой (end) или выбросить (cancel)', () => {
	const kept = segmenter()
	feed(kept.seg, join(quiet(500), speech(1000)))
	kept.seg.end()
	assert.equal(kept.log.phrases.length, 1)

	const dropped = segmenter()
	feed(dropped.seg, join(quiet(500), speech(1000)))
	dropped.seg.cancel()
	assert.equal(dropped.log.phrases.length, 0)
	assert.deepEqual(dropped.log.drops.map((d) => d.reason), ['cancelled'])
	assert.equal(dropped.seg.active, false)
})

/* ---------------------------------------------------------------------------
 * WAV
 * ------------------------------------------------------------------------- */

test('WAV: 16 кГц, моно, PCM16 — заголовок и отсчёты', () => {
	const bytes = encodeWav(Float32Array.from([0, 0.5, -0.5, 1, -1, 2, Number.NaN]), 16000)
	assert.equal(bytes.length, 44 + 7 * 2)
	const view = new DataView(bytes.buffer)
	const ascii = (at, n) => String.fromCharCode(...bytes.slice(at, at + n))
	assert.equal(ascii(0, 4), 'RIFF')
	assert.equal(view.getUint32(4, true), 36 + 14)
	assert.equal(ascii(8, 4), 'WAVE')
	assert.equal(ascii(12, 4), 'fmt ')
	assert.equal(view.getUint32(16, true), 16)
	assert.equal(view.getUint16(20, true), 1, 'PCM')
	assert.equal(view.getUint16(22, true), 1, 'моно')
	assert.equal(view.getUint32(24, true), 16000)
	assert.equal(view.getUint32(28, true), 32000)
	assert.equal(view.getUint16(32, true), 2)
	assert.equal(view.getUint16(34, true), 16)
	assert.equal(ascii(36, 4), 'data')
	assert.equal(view.getUint32(40, true), 14)
	const pcm = [...Array(7)].map((_, i) => view.getInt16(44 + i * 2, true))
	// Громче предела — срез, NaN — тишина.
	assert.deepEqual(pcm, [0, 16384, -16384, 32767, -32768, 32767, 0])
})

test('частота: 48 и 44,1 кГц → 16 кГц без потери голоса, 8 кГц → 16 кГц', () => {
	const dc = resample(new Float32Array(RATE).fill(0.3), RATE)
	assert.equal(dc.length, 16000)
	assert.ok(dc.every((v) => Math.abs(v - 0.3) < 1e-6))

	const sine = new Float32Array(RATE)
	for (let i = 0; i < sine.length; i++) sine[i] = 0.5 * Math.sin((2 * Math.PI * 1000 * i) / RATE)
	const down = resample(sine, RATE)
	assert.ok(Math.max(...down) > 0.45, 'тон 1 кГц сохраняет громкость')

	assert.equal(resample(new Float32Array(44100), 44100).length, 16000)

	const up = resample(Float32Array.from([0, 1, 0, -1]), 8000, 16000)
	assert.equal(up.length, 8)
	assert.ok(Math.abs(up[1] - 0.5) < 1e-6, 'между отсчётами — середина')
})

test('файл фразы для распознавания — voice.wav, audio/wav, 16 кГц', () => {
	const file = phraseFile({ samples: speech(1000), sampleRate: RATE })
	assert.equal(file.name, 'voice.wav')
	assert.equal(file.type, 'audio/wav')
	assert.equal(file.size, 44 + 16000 * 2)
})

/* ---------------------------------------------------------------------------
 * Захват с микрофонного потока (поддельный AudioContext)
 * ------------------------------------------------------------------------- */

function fakeAudioGraph({ worklet = false, workletFails = false } = {}) {
	const log = []
	const node = (name, extra = {}) => ({
		name,
		connect(to) {
			log.push(`${name}→${to.name}`)
		},
		disconnect() {
			log.push(`${name}×`)
		},
		...extra,
	})
	const context = {
		sampleRate: 44100,
		destination: { name: 'destination' },
		createMediaStreamSource: (stream) => node('source', { stream }),
		createGain: () => node('gain', { gain: { value: 1 } }),
		createScriptProcessor: (size) => {
			context.processor = node('processor', { size })
			return context.processor
		},
	}
	if (worklet) {
		context.audioWorklet = {
			addModule: async (url) => {
				log.push(`module:${String(url).startsWith('blob:')}`)
				if (workletFails) throw new Error('blob: запрещён')
			},
		}
	}
	return { context, log }
}

test('захват: ScriptProcessor отдаёт копии кусков, звук в динамики не идёт', async () => {
	const { context, log } = fakeAudioGraph()
	const chunks = []
	const stream = { id: 'mic' }
	const capture = await startCapture(stream, { context, onChunk: (c) => chunks.push(c) })
	assert.equal(capture.sampleRate, 44100)
	assert.equal(context.processor.size, 2048)
	assert.ok(log.includes('source→processor') && log.includes('processor→gain') && log.includes('gain→destination'))
	const data = Float32Array.from([0.1, -0.2, 0.3])
	context.processor.onaudioprocess({ inputBuffer: { getChannelData: () => data } })
	assert.deepEqual([...chunks[0]], [...data])
	assert.notEqual(chunks[0], data, 'буфер браузера переиспользуется — нужна копия')
	capture.stop()
	assert.ok(log.includes('source×') && log.includes('processor×'))
	assert.equal(context.processor.onaudioprocess, null)
})

test('захват: AudioWorklet из blob:, а если не загрузился — ScriptProcessor', async () => {
	const created = []
	const revoked = []
	const revoke = URL.revokeObjectURL
	URL.revokeObjectURL = (url) => {
		revoked.push(url)
		return revoke.call(URL, url)
	}
	globalThis.AudioWorkletNode = class {
		constructor(ctx, name, options) {
			this.name = 'worklet'
			this.port = { onmessage: null, close: () => created.push('closed') }
			created.push({ name, size: options.processorOptions.size })
			ctx.worklet = this
		}
		connect() {}
		disconnect() {}
	}
	try {
		const { context, log } = fakeAudioGraph({ worklet: true })
		const chunks = []
		const capture = await startCapture({}, { context, onChunk: (c) => chunks.push(c) })
		assert.deepEqual(created[0], { name: 'nr-pcm-tap', size: 2048 })
		assert.ok(log.includes('module:true'))
		context.worklet.port.onmessage({ data: Float32Array.from([0.5]) })
		assert.deepEqual([...chunks[0]], [0.5])
		capture.stop()
		assert.equal(revoked.length, 1)
		assert.ok(created.includes('closed'))

		const broken = fakeAudioGraph({ worklet: true, workletFails: true })
		await startCapture({}, { context: broken.context, onChunk: () => {} })
		assert.ok(broken.context.processor, 'запасной путь — ScriptProcessor')
	} finally {
		delete globalThis.AudioWorkletNode
		URL.revokeObjectURL = revoke
	}
})

/* ---------------------------------------------------------------------------
 * Фраза: ответ или нет
 * ------------------------------------------------------------------------- */

test('решение: уверенно — сразу, неуверенно — проверить, пусто — не ответ', () => {
	assert.equal(liveAsrStep(readAsr({ text: 'Мин Казанда яшим', confidence: 0.93, needs_review: false })), 'send')
	assert.equal(liveAsrStep(readAsr({ text: 'Мин Казанда яшим', confidence: 0.41, needs_review: true })), 'review')
	// Сервер не сказал needs_review — спросить дешевле, чем засчитать чужую ошибку.
	assert.equal(liveAsrStep(readAsr({ text: 'Мин' })), 'review')
	assert.equal(liveAsrStep(readAsr({ text: '   ', needs_review: false })), 'empty')
	// В тексте нет ни одной буквы — это не ответ, даже уверенный.
	assert.equal(liveAsrStep(readAsr({ text: '…?!', needs_review: false })), 'empty')
	assert.equal(meaningful('Әйе'), true)
	assert.equal(meaningful('...'), false)
	assert.match(liveReviewHint(['quiet_audio']), /тихой.*20 секунд/)
	assert.match(liveReviewHint([]), /Проверь.*как есть/)

	// edited — по факту: «Верно» и «как есть через 20 с» — не правка.
	const asr = readAsr({ text: 'Мин Казанда яшим', confidence: 0.4, needs_review: true })
	assert.equal(makeAnswer({ text: asr.text, input: 'voice', asr, clientMsgId: 'a', turnNo: null }).edited, false)
	assert.equal(makeAnswer({ text: 'Мин Казанда яшим.', input: 'voice', asr, clientMsgId: 'b', turnNo: null }).edited, true)

	assert.equal(closingAnswer({ status: 'closed' }), true)
	assert.equal(closingAnswer({ turns_taken: 3, max_turns: 3 }), true)
	assert.equal(closingAnswer({ turns_taken: 2 }, 3), false)
	assert.equal(closingAnswer({ turns_taken: 3 }, 3), true)
})

test('указание сессии подписано «Turn guidance» один раз', () => {
	assert.equal(steerContent(''), '')
	assert.match(steerContent('Lead to p1.'), /^Turn guidance \(replaces any earlier guidance\):\nLead to p1\.$/)
	assert.equal(steerContent('Turn guidance: lead to p2.'), 'Turn guidance: lead to p2.')
	assert.match(INTRO_GUIDANCE, /^Turn guidance/)
	assert.match(FAREWELL_GUIDANCE, /goodbye.*Do not ask a question/)
})

/* ---------------------------------------------------------------------------
 * Очередь реплик
 * ------------------------------------------------------------------------- */

async function settle() {
	for (let i = 0; i < 20; i++) await new Promise((resolve) => setImmediate(resolve))
}

function apiError(status, code, message) {
	return Object.assign(new Error(message), { status, code })
}

function answerOf(text, clientMsgId) {
	const asr = readAsr({ text, confidence: 0.9, needs_review: false })
	return makeAnswer({ text, input: 'voice', asr, clientMsgId, turnNo: null })
}

function ledgerHarness({ sendAnswer, saveAvatar, lastTurnNo = 0 } = {}) {
	const sent = []
	const saved = []
	const events = []
	const ledger = new LiveLedger({
		lastTurnNo,
		sendAnswer: async (p) => {
			sent.push(p)
			if (sendAnswer) return sendAnswer(p, sent.length)
			return { turn_no: p.turnNo, turns_taken: p.turnNo, status: 'active', steer: `steer ${p.turnNo}` }
		},
		saveAvatar: async (r) => {
			saved.push(r)
			if (saveAvatar) return saveAvatar(r, saved.length)
			return { translation: `перевод ${r.turnNo}` }
		},
		sleep: async (ms) => {
			events.push(['sleep', ms])
		},
		on: {
			sending: ({ id }) => events.push(['sending', id]),
			answered: ({ id, pending, unavailable }) => events.push(['answered', id, pending.turnNo, Boolean(unavailable)]),
			rejected: ({ id }) => events.push(['rejected', id]),
			unsent: ({ id }) => events.push(['unsent', id]),
			conflict: ({ id }) => events.push(['conflict', id]),
			saved: ({ id, reply }) => events.push(['saved', id, reply.turnNo]),
			unsaved: ({ id }) => events.push(['unsaved', id]),
			idle: () => events.push(['idle']),
		},
	})
	return { ledger, sent, saved, events }
}

test('очередь: реплики по порядку, номер реплики аватара — последний ответ перед ней', async () => {
	const { ledger, sent, saved, events } = ledgerHarness()
	// Вступление — номер 0, указание вступления — тоже 0.
	ledger.addAvatar('a0', { steerTurn: 0 })
	ledger.completeAvatar('a0', 'Исәнме! Син кайда яшисең?')
	await settle()
	assert.deepEqual(saved[0], { clientMsgId: 'a0', turnNo: 0, content: 'Исәнме! Син кайда яшисең?', steerTurn: 0 })

	// Ученик заговорил; сессия ответила раньше, чем распознали его фразу.
	ledger.addStudent('s1')
	ledger.addAvatar('a1', { steerTurn: 0 })
	ledger.completeAvatar('a1', 'Казанда! Ә кем белән?')
	await settle()
	assert.equal(saved.length, 1, 'реплика ждёт ответа, на который отвечает')
	ledger.resolveStudent('s1', answerOf('Мин Казанда яшим', 'c1'))
	await settle()
	assert.equal(sent.length, 1)
	assert.equal(sent[0].clientMsgId, 'c1')
	assert.equal(sent[0].turnNo, 1)
	assert.deepEqual(saved[1], { clientMsgId: 'a1', turnNo: 1, content: 'Казанда! Ә кем белән?', steerTurn: 0 })

	// Фраза, которая ответом не стала, номера не занимает; две реплики на
	// один номер — допустимо.
	ledger.addStudent('s2')
	ledger.addAvatar('a2', { steerTurn: 1 })
	ledger.resolveStudent('s2', null)
	ledger.completeAvatar('a2', 'Әйе, әйе.')
	await settle()
	assert.equal(sent.length, 1)
	assert.deepEqual(saved[2], { clientMsgId: 'a2', turnNo: 1, content: 'Әйе, әйе.', steerTurn: 1 })

	ledger.addStudent('s3')
	ledger.resolveStudent('s3', answerOf('Әнием белән', 'c3'))
	await settle()
	assert.equal(sent[1].turnNo, 2)
	assert.deepEqual(events.filter((e) => e[0] === 'answered'), [
		['answered', 's1', 1, false],
		['answered', 's3', 2, false],
	])
	assert.equal(events.at(-1)[0], 'idle')
	assert.equal(ledger.idle, true)

	// Реплика без номера указания — поля нет вовсе.
	ledger.addAvatar('a3')
	ledger.completeAvatar('a3', 'Яхшы.')
	await settle()
	assert.equal('steerTurn' in saved[3], false)
})

test('очередь: обрыв — повтор тем же client_msg_id, исчерпаны повторы — «Повторить»', async () => {
	let failures = 2
	const flaky = ledgerHarness({
		sendAnswer: (p) => {
			if (failures-- > 0) throw apiError(0, 'network', 'Нет связи с сервером')
			return { turn_no: p.turnNo, turns_taken: p.turnNo }
		},
	})
	flaky.ledger.addStudent('s1')
	flaky.ledger.resolveStudent('s1', answerOf('Сәлам', 'c1'))
	await settle()
	assert.equal(flaky.sent.length, 3)
	assert.ok(flaky.sent.every((p) => p.clientMsgId === 'c1' && p.turnNo === 1))
	assert.deepEqual(flaky.events.filter((e) => e[0] === 'sleep').map((e) => e[1]), [1000, 2000])

	let down = true
	const offline = ledgerHarness({
		sendAnswer: (p) => {
			if (down) throw apiError(502, 'internal_error', 'Плохой шлюз')
			return { turn_no: p.turnNo, turns_taken: p.turnNo }
		},
	})
	offline.ledger.addStudent('s1')
	offline.ledger.addAvatar('a1')
	offline.ledger.resolveStudent('s1', answerOf('Сәлам', 'c1'))
	offline.ledger.completeAvatar('a1', 'Исәнме!')
	await settle()
	assert.equal(offline.sent.length, 5, 'первая попытка и четыре повтора')
	assert.ok(offline.events.some((e) => e[0] === 'unsent'))
	assert.equal(offline.saved.length, 0, 'реплика ждёт ответа перед ней')
	assert.equal(offline.ledger.blocked, true)
	down = false
	offline.ledger.retry()
	await settle()
	assert.equal(offline.sent.at(-1).clientMsgId, 'c1')
	assert.equal(offline.saved[0].turnNo, 1)
})

test('очередь: 503 — ответ засчитан без указания, отказ — номер не занят, 409 — стоп', async () => {
	const unavailable = ledgerHarness({
		sendAnswer: () => {
			throw apiError(503, 'avatar_unavailable', 'Аватар не смог ответить')
		},
	})
	unavailable.ledger.addStudent('s1')
	unavailable.ledger.addAvatar('a1')
	unavailable.ledger.resolveStudent('s1', answerOf('Сәлам', 'c1'))
	unavailable.ledger.completeAvatar('a1', 'Исәнме!')
	await settle()
	assert.deepEqual(unavailable.events.find((e) => e[0] === 'answered'), ['answered', 's1', 1, true])
	assert.equal(unavailable.sent.length, 1, '503 не повторяется: ответ сервер уже записал')
	assert.equal(unavailable.saved[0].turnNo, 1)

	let calls = 0
	const rejected = ledgerHarness({
		sendAnswer: (p) => {
			calls += 1
			if (calls === 1) throw apiError(422, 'validation_failed', 'Слишком длинный ответ')
			return { turn_no: p.turnNo, turns_taken: p.turnNo }
		},
	})
	rejected.ledger.addStudent('s1')
	rejected.ledger.resolveStudent('s1', answerOf('Сәлам', 'c1'))
	rejected.ledger.addStudent('s2')
	rejected.ledger.resolveStudent('s2', answerOf('Сау бул', 'c2'))
	await settle()
	assert.ok(rejected.events.some((e) => e[0] === 'rejected' && e[1] === 's1'))
	assert.equal(rejected.sent[1].turnNo, 1, 'отказанный ответ номера не занял')

	const conflict = ledgerHarness({
		sendAnswer: () => {
			throw apiError(409, 'conflict', 'Разговор закончен')
		},
	})
	conflict.ledger.addStudent('s1')
	conflict.ledger.resolveStudent('s1', answerOf('Сәлам', 'c1'))
	conflict.ledger.addStudent('s2')
	conflict.ledger.resolveStudent('s2', answerOf('Сау бул', 'c2'))
	await settle()
	assert.ok(conflict.events.some((e) => e[0] === 'conflict'))
	assert.equal(conflict.sent.length, 1, 'после 409 очередь стоит')
})

test('очередь: реплика аватара — недолгий повтор, отказ сервера ответы не держит', async () => {
	let failures = 2
	const flaky = ledgerHarness({
		saveAvatar: (r) => {
			if (failures-- > 0) throw apiError(0, 'network', 'Нет связи')
			return { translation: 'Привет!' }
		},
	})
	flaky.ledger.addAvatar('a0', { steerTurn: 0 })
	flaky.ledger.completeAvatar('a0', 'Сәлам!')
	await settle()
	assert.equal(flaky.saved.length, 3)
	assert.deepEqual(flaky.events.filter((e) => e[0] === 'sleep').map((e) => e[1]), [1000, 3000])
	assert.ok(flaky.events.some((e) => e[0] === 'saved'))

	const refused = ledgerHarness({
		saveAvatar: () => {
			throw apiError(400, 'validation_failed', 'Пустая реплика')
		},
	})
	refused.ledger.addAvatar('a0')
	refused.ledger.completeAvatar('a0', 'Сәлам!')
	refused.ledger.addStudent('s1')
	refused.ledger.resolveStudent('s1', answerOf('Исәнме', 'c1'))
	await settle()
	assert.equal(refused.saved.length, 1)
	assert.ok(refused.events.some((e) => e[0] === 'unsaved'))
	assert.equal(refused.sent.length, 1)
})

test('очередь: разговор закрыт — неотправленные фразы снимаются, отправляемая остаётся', async () => {
	let release
	const { ledger, sent } = ledgerHarness({
		sendAnswer: (p) =>
			new Promise((resolve) => {
				release = () => resolve({ turn_no: p.turnNo, turns_taken: p.turnNo, status: 'closed' })
			}),
	})
	ledger.addStudent('s1')
	ledger.resolveStudent('s1', answerOf('Сау бул', 'c1'))
	ledger.addStudent('s2')
	ledger.addAvatar('a1')
	ledger.addStudent('s3')
	ledger.resolveStudent('s3', answerOf('Тагын', 'c3'))
	await settle()
	assert.deepEqual(ledger.dropStudents(), ['s2', 's3'])
	release()
	ledger.completeAvatar('a1', 'Рәхмәт!')
	await settle()
	assert.equal(sent.length, 1)
	assert.equal(ledger.idle, true)
})

/* ---------------------------------------------------------------------------
 * Живой разговор целиком: настоящий LiveConnection, поддельный канал
 * ------------------------------------------------------------------------- */

function memoryStorage() {
	const data = new Map()
	return {
		data,
		getItem: (k) => (data.has(k) ? data.get(k) : null),
		setItem: (k, v) => data.set(k, String(v)),
		removeItem: (k) => data.delete(k),
	}
}

/** Часы для LiveTalk: время идёт, только когда тест его двигает. */
function fakeClock() {
	let now = 1_000_000
	let seq = 0
	const timers = new Map()
	return {
		now: () => now,
		setTimeout: (fn, ms = 0) => {
			timers.set(++seq, { at: now + Math.max(0, ms), fn, every: 0 })
			return seq
		},
		clearTimeout: (id) => timers.delete(id),
		setInterval: (fn, ms) => {
			timers.set(++seq, { at: now + ms, fn, every: ms })
			return seq
		},
		clearInterval: (id) => timers.delete(id),
		async advance(ms) {
			const target = now + ms
			for (;;) {
				let next = null
				for (const [id, t] of timers) if (t.at <= target && (!next || t.at < next[1].at)) next = [id, t]
				if (!next) break
				const [id, t] = next
				now = t.at
				if (t.every) t.at += t.every
				else timers.delete(id)
				t.fn()
				await settle()
			}
			now = target
			await settle()
		},
	}
}

function deferred() {
	let resolve
	let reject
	const promise = new Promise((res, rej) => {
		resolve = res
		reject = rej
	})
	return { promise, resolve, reject }
}

/**
 * Живой разговор на поддельном канале. Настоящие LiveConnection, LiveTalk,
 * нарезка, очередь и ToolBridge; поддельные — WebRTC, микрофон, захват звука,
 * ручки сервера и часы.
 */
function liveHarness({ open = {}, transcripts = [], answers = [], recorder = null, mic = null } = {}) {
	const clock = fakeClock()
	const sent = []
	const actions = []
	const calls = []
	const storage = memoryStorage()
	let peer = null
	let capture = null
	let ids = 0
	class Peer {
		constructor() {
			peer = this
			this.iceGatheringState = 'complete'
			this.localDescription = { sdp: 'offer' }
			this.dc = {
				readyState: 'open',
				send: (data) => sent.push(JSON.parse(data)),
				close() {
					this.closed = true
				},
			}
		}
		addTrack(track) {
			this.sender = { track, replaceTrack: async (next) => (this.sender.track = next) }
			return this.sender
		}
		createDataChannel() {
			return this.dc
		}
		async createOffer() {
			return { sdp: 'offer' }
		}
		async setLocalDescription() {}
		async setRemoteDescription() {}
		close() {
			this.closed = true
		}
	}
	const track = {
		enabled: true,
		stop() {
			this.stopped = true
		},
	}
	const stream = { getTracks: () => [track] }
	// Дорожка тишины, которую слышит сессия: настоящий микрофон — только у нарезки.
	const silentTrack = {
		enabled: true,
		stop() {
			this.stopped = true
		},
	}
	const silent = { getTracks: () => [silentTrack] }
	const api = {
		open: async (sdp) => {
			calls.push(['open', sdp])
			return {
				session: { id: 'live_1' },
				transport: { type: 'webrtc', sdp: 'answer' },
				steer: 'Lead the learner to p1 "Минем исемем …".',
				turns_taken: 0,
				max_turns: 2,
				status: 'active',
				...open,
			}
		},
		answer: async (answer) => {
			calls.push(['answer', answer])
			const next = answers.shift()
			return typeof next === 'function' ? next(answer) : next
		},
		avatar: async (reply) => {
			calls.push(['avatar', reply])
			return { translation: `перевод ${reply.turnNo}` }
		},
		progress: async () => {
			calls.push(['progress'])
			return { goals: [{ key: 'p1', status: 'open' }], target_goal: 'p1', answers_left: 1, finished: false }
		},
		check: async (text) => {
			calls.push(['check', text])
			return { ok: false, problems: ['Фокусная фраза p1 не должна звучать'] }
		},
		end: async (id) => calls.push(['end', id]),
		endBeacon: (id) => {
			calls.push(['beacon', id])
			return true
		},
		transcribe: async (file, opts) => {
			calls.push(['transcribe', file.name, file.type, opts.language])
			const next = transcripts.shift()
			if (next instanceof Error) throw next
			return next
		},
	}
	const audio = {
		srcObject: null,
		pause() {},
		play: async () => {},
	}
	const drained = []
	const talk = new LiveTalk({
		attemptId: 'att',
		language: 'tatar',
		turnsTaken: 0,
		maxTurns: 2,
		audio,
		api,
		storage,
		dispatch: (action) => actions.push(action),
		report: (...args) => calls.push(['incident', ...args]),
		onDrained: () => drained.push(true),
		deps: {
			getUserMedia: mic ? () => mic.then(() => stream) : async () => stream,
			createConnection: (options) => new LiveConnection({ ...options, PeerConnection: Peer }),
			silentStream: () => silent,
			startCapture: async (s, { onChunk }) => {
				capture = {
					stream: s,
					onChunk,
					stop() {
						this.stopped = true
					},
				}
				return capture
			},
			MediaRecorder: recorder,
			createAudio: () => ({
				src: '',
				play: async () => {},
				pause() {
					this.onpause?.()
				},
			}),
			createObjectURL: () => `blob:reply-${++ids}`,
			revokeObjectURL: () => {},
			now: clock.now,
			setTimeout: clock.setTimeout,
			clearTimeout: clock.clearTimeout,
			setInterval: clock.setInterval,
			clearInterval: clock.clearInterval,
			newId: () => `id-${++ids}`,
		},
	})
	const emit = (event) => peer.dc.onmessage({ data: JSON.stringify(event) })
	return {
		talk,
		clock,
		sent,
		actions,
		calls,
		storage,
		track,
		silentTrack,
		audio,
		drained,
		emit,
		context: { sampleRate: RATE, closed: false, close: async function () { this.closed = true } },
		capture: () => capture,
		peer: () => peer,
		/**
		 * Фраза ученика: голос и пауза, кусками, как из AudioWorklet. Ученик
		 * включает микрофон кнопкой, говорит и выключает.
		 */
		say: (ms = 1200) => {
			talk.setMic(true)
			feed({ push: capture.onChunk }, join(speech(ms), quiet(1000)))
			talk.setMic(false)
		},
		/** Реплика аватара по расшифровке сессии. */
		avatar: (...deltas) => {
			for (const delta of deltas) emit({ type: 'session.output_transcript.delta', delta })
		},
		avatarDone: () => emit({ type: 'session.output_transcript.done' }),
		of: (type) => sent.filter((e) => e.type === type),
		called: (name) => calls.filter((c) => c[0] === name),
		acted: (type) => actions.filter((a) => a.type === type),
		/** Закрыть сессию до конца: провайдер подтверждает закрытие. */
		async close() {
			if (peer && !peer.closed) emit({ type: 'session.closed' })
			await settle()
		},
	}
}

async function startLive(h) {
	await h.talk.start({ context: h.context })
	await settle()
	h.emit({ type: 'session.started' })
	await settle()
}

test('живой разговор: вступление, ответы со steer, steer_turn, инструменты, прощание', async () => {
	const answer1 = deferred()
	const h = liveHarness({
		transcripts: [
			{ text: 'Мин Казанда яшим', confidence: 0.93, needs_review: false, review_reasons: [] },
			{ text: 'Минем исемем Алсу', confidence: 0.41, needs_review: true, review_reasons: ['low_confidence'] },
		],
		answers: [
			() => answer1.promise,
			{ reply: '', translation: '', turn_no: 2, turns_taken: 2, max_turns: 2, status: 'closed', steer: 'Thank the learner and say goodbye.' },
		],
	})
	await startLive(h)

	// Рукопожатие — через …/conversation/live; вступление — указание сервера:
	// аватар начинает сам, response.create сразу не нужен (он давал второе
	// приветствие).
	assert.deepEqual(h.called('open'), [['open', 'offer']])
	assert.ok(h.acted('live_open').length === 1)
	const [intro] = h.of('session.thinking.append')
	assert.equal(intro.delegation_id, null)
	assert.match(intro.content, /^Turn guidance/)
	assert.match(intro.content, /Lead the learner to p1/)
	assert.equal(h.of('response.create').length, 0)
	assert.ok(h.capture(), 'нарезка фраз слушает тот же микрофон')

	// Вступление аватара: номер 0, указание 0.
	h.avatar('Исәнме! ', 'Син кайда яшисең?')
	h.avatarDone()
	await settle()
	assert.deepEqual(h.called('avatar')[0][1], {
		clientMsgId: h.called('avatar')[0][1].clientMsgId,
		turnNo: 0,
		content: 'Исәнме! Син кайда яшисең?',
		steerTurn: 0,
	})
	assert.ok(h.acted('live_avatar').some((a) => a.stage === 'saved' && a.translation === 'перевод 0'))

	// Ученик отвечает уверенно: WAV на распознавание (язык урока), ответ — mode: live.
	h.say()
	await settle()
	assert.deepEqual(h.called('transcribe')[0], ['transcribe', 'voice.wav', 'audio/wav', 'tatar'])
	const [, first] = h.called('answer')[0]
	assert.equal(first.mode, 'live')
	assert.equal(first.input, 'voice')
	assert.equal(first.text, 'Мин Казанда яшим')
	assert.equal(first.asrText, 'Мин Казанда яшим')
	assert.equal(first.asrConfidence, 0.93)
	assert.equal(first.edited, false)
	assert.equal(first.turnNo, 1)
	// До подтверждения ответ лежит во вкладке: после перезагрузки он не потеряется.
	assert.ok(h.storage.data.has(pendingKey('att')))

	// Сессия отвечает сразу — раньше, чем пришло указание на этот ход.
	h.avatar('Казанда! ')
	await settle()
	answer1.resolve({ reply: '', translation: '', turn_no: 1, turns_taken: 1, max_turns: 2, status: 'active', steer: 'Use the form "Казанда" naturally; lead to p1.' })
	await settle()
	assert.equal(h.storage.data.has(pendingKey('att')), false)
	// Указание ждёт, пока ученик заговорит: пришедшее сразу, оно вызывало
	// вторую реплику аватара подряд.
	assert.equal(h.of('session.thinking.append').length, 1, 'ученик молчит — указание не отправлено')
	await h.clock.advance(INTRO_WAIT_MS)
	// Аватар поздоровался сам — вступление не просим; единственный
	// response.create — ответ на фразу ученика, и слова ушли ему текстом.
	assert.deepEqual(h.of('response.create').map((e) => e.event_id), ['turn_1'])
	const said = h.of('response.item.create')[0]
	assert.equal(said.item.role, 'user')
	assert.equal(said.item.content[0].text, 'Мин Казанда яшим')
	assert.ok(h.sent.indexOf(said) < h.sent.indexOf(h.of('response.create')[0]), 'сначала слова, потом просьба ответить')
	h.avatar('Ә кем белән яшисең?')
	h.avatarDone()
	await settle()
	// Реплика началась до указания хода 1: steer_turn — 0, хотя номер её — 1.
	const replies = h.called('avatar').map(([, r]) => r)
	assert.equal(replies[1].turnNo, 1)
	assert.equal(replies[1].steerTurn, 0)
	assert.equal(replies[1].content, 'Казанда! Ә кем белән яшисең?')
	assert.equal(h.of('session.thinking.append').length, 1, 'указание не ушло и после реплики')
	assert.ok(h.acted('live_answered').some((a) => a.res.turns_taken === 1))

	// Инструменты делегата исполняет браузер.
	h.emit({ type: 'response.event', delegation_id: 'd1', event: { type: 'response.created', response: { id: 'r1' } } })
	h.emit({
		type: 'response.event',
		delegation_id: 'd1',
		event: { type: 'response.output_item.done', item: { type: 'function_call', call_id: 'call_1', name: 'lesson_progress', arguments: '{}' } },
	})
	h.emit({
		type: 'response.event',
		delegation_id: 'd1',
		event: {
			type: 'response.output_item.done',
			item: { type: 'function_call', call_id: 'call_2', name: 'check_reply', arguments: '{"text":"Минем исемем Ишка."}' },
		},
	})
	h.emit({ type: 'response.event', delegation_id: 'd1', event: { type: 'response.completed', response: { id: 'r1' } } })
	await settle()
	assert.equal(h.called('progress').length, 1)
	assert.deepEqual(h.called('check'), [['check', 'Минем исемем Ишка.']])
	const outputs = h.of('response.item.create').map((e) => e.item).filter((i) => i.type === 'function_call_output')
	assert.deepEqual(
		outputs.map((i) => [i.type, i.call_id]),
		[
			['function_call_output', 'call_1'],
			['function_call_output', 'call_2'],
		],
	)
	assert.equal(JSON.parse(outputs[0].output).target_goal, 'p1')
	assert.equal(JSON.parse(outputs[1].output).ok, false)
	assert.equal(h.of('response.create').at(-1).event_id, 'continue_r1')

	// Неуверенная фраза: пузырь с «Верно» / «Исправить», разговор идёт; без
	// ответа ученика — как есть через 20 секунд.
	h.say()
	await settle()
	// Ученик заговорил — отложенное указание хода 1 ушло в сессию.
	const steers = h.of('session.thinking.append')
	assert.equal(steers.length, 2)
	assert.match(steers[1].content, /^Turn guidance[\s\S]*lead to p1/)
	const review = h.acted('live_student').find((a) => a.stage === 'review')
	assert.equal(review.asr.text, 'Минем исемем Алсу')
	assert.equal(h.called('answer').length, 1)
	await h.clock.advance(REVIEW_MS - 1000)
	assert.equal(h.called('answer').length, 1, 'до 20 секунд ответ ждёт ученика')
	await h.clock.advance(1500)
	const [, second] = h.called('answer')[1]
	assert.equal(second.text, 'Минем исемем Алсу')
	assert.equal(second.edited, false)
	assert.equal(second.turnNo, 2)

	// Последний ответ: слова ушли сессии, указание попрощаться добавлено.
	// Ответ на последнюю фразу уже заказан и ещё не начался — второй
	// response.create поверх него сессия считает ошибкой, прощание просить не
	// надо: реплика, которая сейчас начнётся, и есть прощание.
	await settle()
	assert.ok(h.acted('live_status').some((a) => a.status === 'closing'))
	assert.deepEqual(h.sent.slice(-3).map((e) => e.type), ['response.item.create', 'response.create', 'session.thinking.append'])
	assert.match(h.sent.at(-1).content, /say goodbye/)
	assert.deepEqual(h.of('response.create').map((e) => e.event_id).slice(-2), ['continue_r1', 'turn_2'])
	assert.equal(h.capture().stopped, true, 'фразы после последнего ответа не нарезаются')

	h.avatar('Рәхмәт! Сау бул!')
	h.avatarDone()
	await settle()
	assert.equal(h.acted('live_finished').length, 0, 'звук прощания ещё может играть')
	await h.clock.advance(3100)
	assert.equal(h.acted('live_finished').length, 1)
	assert.equal(h.of('session.close').length, 1)
	await h.close()
	assert.deepEqual(h.called('end'), [['end', 'live_1']])
	const farewell = h.called('avatar').at(-1)[1]
	assert.equal(farewell.turnNo, 2)
	assert.equal(farewell.steerTurn, 2)
	assert.equal(farewell.content, 'Рәхмәт! Сау бул!')
	h.talk.dispose()
})

test('живой разговор: сессия промолчала после указания — вступление просим через 4 секунды', async () => {
	const h = liveHarness({ transcripts: [], answers: [] })
	await startLive(h)
	assert.equal(h.of('response.create').length, 0)
	await h.clock.advance(INTRO_WAIT_MS - 100)
	assert.equal(h.of('response.create').length, 0)
	await h.clock.advance(200)
	assert.deepEqual(h.of('response.create'), [{ type: 'response.create', event_id: 'intro' }])
	h.talk.dispose()
})

test('живой разговор: указание, пришедшее посреди фразы ученика, уходит сразу', async () => {
	const answer1 = deferred()
	const h = liveHarness({
		transcripts: [
			{ text: 'Мин Казанда яшим', confidence: 0.93, needs_review: false, review_reasons: [] },
			{ text: 'Әнием белән', confidence: 0.93, needs_review: false, review_reasons: [] },
		],
		answers: [() => answer1.promise, { turn_no: 2, turns_taken: 2, max_turns: 3, status: 'active', steer: 'Lead to p2.' }],
		open: { max_turns: 3 },
	})
	await startLive(h)
	h.say()
	await settle()
	// Ученик уже говорит следующую фразу, когда сервер вернул указание.
	h.talk.setMic(true)
	feed({ push: h.capture().onChunk }, speech(1200))
	await settle()
	answer1.resolve({ turn_no: 1, turns_taken: 1, max_turns: 3, status: 'active', steer: 'Lead to p1.' })
	await settle()
	assert.equal(h.of('session.thinking.append').length, 2)
	assert.match(h.of('session.thinking.append').at(-1).content, /Lead to p1/)
	h.talk.dispose()
})

test('живой разговор: отложенное указание уходит, когда ученик включил микрофон', async () => {
	const answer1 = deferred()
	const h = liveHarness({
		transcripts: [{ text: 'Мин Казанда яшим', confidence: 0.93, needs_review: false, review_reasons: [] }],
		answers: [() => answer1.promise],
		open: { max_turns: 3 },
	})
	await startLive(h)
	h.say()
	await settle()
	answer1.resolve({ turn_no: 1, turns_taken: 1, max_turns: 3, status: 'active', steer: 'Lead to p1.' })
	await settle()
	assert.equal(h.of('session.thinking.append').length, 1, 'микрофон выключен — указание ждёт')
	// Нажал кнопку — ход ученика начался, сессия ответит уже по новому указанию.
	h.talk.setMic(true)
	assert.equal(h.of('session.thinking.append').length, 2)
	assert.match(h.of('session.thinking.append').at(-1).content, /Lead to p1/)
	h.talk.dispose()
})

test('живой разговор: сессия вступила при включённом микрофоне — указание ждёт следующего хода', async () => {
	const answer1 = deferred()
	const h = liveHarness({
		transcripts: [{ text: 'Белмим', confidence: 0.93, needs_review: false, review_reasons: [] }],
		answers: [() => answer1.promise],
		open: { max_turns: 3 },
	})
	await startLive(h)
	await h.clock.advance(1000)
	h.talk.setMic(true)
	feed({ push: h.capture().onChunk }, join(speech(1200), quiet(1000)))
	await settle()
	await h.clock.advance(500)
	// Ученик ещё держит микрофон, а сессия уже ответила на паузе.
	h.avatar('Аның әтисе үлгән.')
	h.avatarDone()
	await settle()
	const before = h.of('session.thinking.append').length
	answer1.resolve({ turn_no: 1, turns_taken: 1, max_turns: 3, status: 'active', steer: 'Lead to p1.' })
	await settle()
	assert.equal(h.of('session.thinking.append').length, before, 'указание дало бы вторую реплику подряд')
	h.talk.setMic(false)
	await settle()
	// Следующий ход — указание уходит.
	await h.clock.advance(1000)
	h.talk.setMic(true)
	assert.match(h.of('session.thinking.append').at(-1).content, /Lead to p1/)
	h.talk.dispose()
})

test('живой разговор: сессия ответила ещё при включённом микрофоне — после кнопки её не торопят', async () => {
	const h = liveHarness({
		transcripts: [{ text: 'Хәтерләмим', confidence: 0.93, needs_review: false, review_reasons: [] }],
		answers: [{ turn_no: 1, turns_taken: 1, max_turns: 3, status: 'active', steer: 'Lead to p1.' }],
		open: { max_turns: 3 },
	})
	await startLive(h)
	await h.clock.advance(1000)
	h.talk.setMic(true)
	feed({ push: h.capture().onChunk }, join(speech(1200), quiet(1000)))
	await settle()
	await h.clock.advance(500)
	h.avatar('Ярый.')
	h.avatarDone()
	await settle()
	await h.clock.advance(500)
	h.talk.setMic(false)
	await h.clock.advance(20000)
	assert.equal(h.of('response.create').filter((e) => e.event_id.startsWith('nudge_')).length, 0)
	h.talk.dispose()
})

test('живой разговор: прощание ждёт, пока аватар договорит, и не дольше 20 секунд', async () => {
	const h = liveHarness({
		transcripts: [{ text: 'Сау бул', confidence: 0.9, needs_review: false }],
		answers: [{ turn_no: 1, turns_taken: 1, max_turns: 1, status: 'closed' }],
		open: { max_turns: 1 },
	})
	await startLive(h)
	h.say()
	// Аватар уже отвечает на фразу, когда сервер закрыл разговор.
	h.avatar('Ә син ')
	await settle()
	const farewells = () => h.of('response.create').filter((e) => e.event_id === 'farewell')
	assert.equal(farewells().length, 0, 'пока реплика звучит, прощание не просим')
	// Без steer — наше указание попрощаться.
	assert.match(h.of('session.thinking.append').at(-1).content, /conversation is over/)
	h.avatar('кайда?')
	h.avatarDone()
	await settle()
	assert.equal(farewells().length, 1)
	// Прощания так и не было — через 20 секунд сессия закрывается сама.
	await h.clock.advance(20000)
	assert.equal(h.acted('live_finished').length, 1)
	await h.close()
	assert.deepEqual(h.called('end'), [['end', 'live_1']])
	h.talk.dispose()
})

test('правка неуверенной фразы: edited по факту, пока правит — текст сам не уходит', async () => {
	const h = liveHarness({
		transcripts: [{ text: 'Мин казанда яшим', confidence: 0.4, needs_review: true, review_reasons: ['low_confidence'] }],
		answers: [{ turn_no: 1, turns_taken: 1, max_turns: 2, status: 'active', steer: 'Next.' }],
	})
	await startLive(h)
	h.say()
	await settle()
	const id = h.acted('live_student').find((a) => a.stage === 'review').id
	h.talk.edit(id)
	await h.clock.advance(REVIEW_MS + 5000)
	assert.equal(h.called('answer').length, 0)
	h.talk.sendEdited(id, 'Мин Казанда яшим')
	await settle()
	const [, answer] = h.called('answer')[0]
	assert.equal(answer.text, 'Мин Казанда яшим')
	assert.equal(answer.asrText, 'Мин казанда яшим')
	assert.equal(answer.edited, true)
	h.talk.dispose()
	await h.close()
})

test('пустое распознавание — не ответ; сбой распознавания — повтор и запись в журнал', async () => {
	const h = liveHarness({
		transcripts: [
			{ text: '', confidence: null, needs_review: true },
			new Error('ASR недоступен'),
			new Error('ASR недоступен'),
		],
	})
	await startLive(h)
	h.say()
	await settle()
	assert.ok(h.acted('live_student').some((a) => a.stage === 'dropped'))
	h.say()
	await settle()
	await h.clock.advance(1500)
	assert.equal(h.called('transcribe').length, 3, 'одна запись — один повтор')
	assert.ok(h.acted('live_student').some((a) => a.stage === 'asr_failed'))
	assert.deepEqual(h.called('incident')[0].slice(0, 3), ['incident', 'asr', 1])
	assert.equal(h.called('answer').length, 0)
	h.talk.dispose()
	await h.close()
})

test('речь в фразе не разобрана — не сбой: фраза пропущена без повтора и журнала', async () => {
	const unclear = new ApiError({
		status: 422,
		code: 'validation_failed',
		message: 'Не удалось разобрать запись',
		fields: { file: 'Не удалось разобрать речь. Замените запись.' },
	})
	const h = liveHarness({ transcripts: [unclear] })
	await startLive(h)
	h.say()
	await settle()
	await h.clock.advance(1500)
	assert.equal(h.called('transcribe').length, 1, 'повтор ту же тишину не разберёт')
	assert.ok(h.acted('live_student').some((a) => a.stage === 'dropped'))
	assert.ok(!h.acted('live_student').some((a) => a.stage === 'asr_failed'))
	assert.equal(h.called('incident').length, 0)
	h.talk.dispose()
	await h.close()
})

test('сбой сессии — ввод текстом: сказанное дописывается, потом onDrained', async () => {
	const h = liveHarness({
		transcripts: [{ text: 'Мин мәктәптә укыйм', confidence: 0.4, needs_review: true }],
		answers: [{ turn_no: 1, turns_taken: 1, max_turns: 2, status: 'active', steer: 'Lead to p2.' }],
	})
	await startLive(h)
	h.say()
	await settle()
	const id = h.acted('live_student').find((a) => a.stage === 'review').id
	const steersBefore = h.of('session.thinking.append').length
	h.emit({ type: 'error', error: { message: 'boom' } })
	await settle()
	const handover = h.acted('live_handover')[0]
	assert.equal(handover.message, 'Разговор прервался (голосовой сервис не смог продолжить разговор).')
	assert.equal(h.drained.length, 0, 'неуверенная фраза ещё ждёт ученика')
	h.talk.confirm(id)
	await settle()
	assert.equal(h.called('answer').length, 1)
	assert.equal(h.drained.length, 1)
	assert.equal(h.of('session.thinking.append').length, steersBefore, 'сессии нет — указание не отправляется')
	h.talk.dispose()
	await h.close()
})

test('реплика аватара с казахскими буквами и латиницей — в подсказку и на сервер кириллицей', async () => {
	const h = liveHarness()
	await startLive(h)
	h.avatar('Тағы бір ', 'рет айт. Anlamadım?')
	await settle()
	const shown = h.acted('live_avatar').filter((a) => a.content).map((a) => a.content)
	assert.ok(shown.length > 0)
	assert.ok(shown.every((text) => !/[қғұіA-Za-zı]/.test(text)), shown.join(' | '))
	h.avatarDone()
	await settle()
	assert.deepEqual(h.called('avatar').map(([, r]) => r.content), ['Тагы бер рет айт. Анламадым?'])
	await h.close()
	h.talk.dispose()
})

test('сбой посреди реплики аватара: недосказанное записывается один раз, потом onDrained', async () => {
	const h = liveHarness()
	await startLive(h)
	h.avatar('Исәнме! Син ')
	await settle()
	h.emit({ type: 'error' })
	await settle()
	assert.equal(h.drained.length, 0, 'реплика ещё не досказана — очередь ждёт')
	// LiveConnection закрывается и отдаёт недосказанное.
	await h.close()
	const replies = h.called('avatar').map(([, r]) => r.content)
	assert.deepEqual(replies, ['Исәнме! Син'])
	assert.equal(h.drained.length, 1)
	h.talk.dispose()
})

test('сессия закрыта, а недосказанное не пришло — через 6 секунд реплика записывается тем, что есть', async () => {
	const h = liveHarness()
	await startLive(h)
	h.avatar('Сәлам, ')
	await settle()
	h.talk.handOver('')
	await h.clock.advance(6000)
	assert.deepEqual(
		h.called('avatar').map(([, r]) => r.content),
		['Сәлам,'],
	)
	assert.equal(h.drained.length, 1)
	// Запоздавший остаток второй репликой не становится.
	await h.close()
	assert.equal(h.called('avatar').length, 1)
	h.talk.dispose()
})

test('прощание оборвалось на 20-й секунде — сказанное всё равно записано с номером последнего ответа', async () => {
	const h = liveHarness({
		transcripts: [{ text: 'Сау бул', confidence: 0.9, needs_review: false }],
		answers: [{ turn_no: 1, turns_taken: 1, max_turns: 1, status: 'closed', steer: 'Say goodbye.' }],
		open: { max_turns: 1 },
	})
	await startLive(h)
	h.say()
	await settle()
	assert.equal(h.of('response.create').at(-1).event_id, 'turn_1', 'ответ заказан один раз, прощание поверх него не просим')
	h.avatar('Рәхмәт, ')
	await h.clock.advance(20000)
	assert.equal(h.acted('live_finished').length, 1)
	await h.close()
	const last = h.called('avatar').at(-1)[1]
	assert.deepEqual([last.turnNo, last.steerTurn, last.content], [1, 1, 'Рәхмәт,'])
	assert.deepEqual(h.called('end'), [['end', 'live_1']])
	h.talk.dispose()
})

test('сессия не открылась — «не получилось соединиться», дальше ввод текстом', async () => {
	const h = liveHarness()
	h.talk.api.open = async () => {
		throw new Error('Все голосовые линии заняты')
	}
	await h.talk.start({ context: h.context })
	await settle()
	assert.equal(h.acted('live_handover')[0].message, 'Не получилось соединиться (все голосовые линии заняты).')
	assert.equal(h.drained.length, 1)
	assert.equal(h.track.stopped, true, 'микрофон отпущен')
	h.talk.dispose()
	await h.close()
})

test('микрофон не дали — окно о микрофоне, сессия не открывается', async () => {
	const h = liveHarness()
	const kinds = []
	h.talk.onMicError = (kind) => kinds.push(kind)
	h.talk.d.getUserMedia = async () => {
		throw Object.assign(new Error('denied'), { name: 'NotAllowedError' })
	}
	assert.equal(await h.talk.start({ context: h.context }), false)
	assert.deepEqual(kinds, ['denied'])
	assert.equal(h.called('open').length, 0)
	assert.equal(h.context.closed, true)
	assert.equal(micErrorKind({ name: 'NotFoundError' }), 'missing')
	assert.equal(micErrorKind({ name: 'AbortError' }), 'failed')
})

test('уход со страницы — маяк конца сессии; закрытие экрана — фразы как есть', async () => {
	const h = liveHarness({
		transcripts: [{ text: 'Мин укыйм', confidence: 0.4, needs_review: true }],
		answers: [{ turn_no: 1, turns_taken: 1, max_turns: 2, status: 'active' }],
	})
	await startLive(h)
	h.say()
	await settle()
	h.talk.pageHide()
	await h.close()
	assert.deepEqual(h.called('beacon'), [['beacon', 'live_1']])
	assert.equal(h.called('end').length, 0, 'маяк вместо обычного запроса — второй раз не сообщаем')
	// Экран закрыт: проверять некому — неуверенная фраза уходит как есть.
	h.talk.dispose()
	await settle()
	const [, answer] = h.called('answer')[0]
	assert.equal(answer.text, 'Мин укыйм')
	assert.equal(answer.edited, false)
})

test('«Отменить» при подключении: рукопожатие обрывается, микрофон отпущен', async () => {
	const h = liveHarness()
	let signal
	h.talk.api.open = (sdp, opts) => {
		signal = opts.signal
		return new Promise(() => {})
	}
	await h.talk.start({ context: h.context })
	await settle()
	assert.equal(h.talk.cancel(), true)
	assert.equal(signal.aborted, true)
	assert.equal(h.track.stopped, true)
	assert.ok(h.acted('live_status').some((a) => a.status === 'idle'))
	assert.equal(h.acted('live_open').length, 0)
	h.talk.dispose()
	await h.close()
})

test('кнопка микрофона: без неё сессия и нарезка не слышат ученика', async () => {
	const h = liveHarness({
		transcripts: [{ text: 'Мин Казанда яшим', confidence: 0.93, needs_review: false, review_reasons: [] }],
		answers: [{ reply: '', translation: '', turn_no: 1, turns_taken: 1, max_turns: 2, status: 'active', steer: 'Lead to p1.' }],
	})
	// До соединения кнопка ничего не делает.
	assert.equal(h.talk.setMic(true), false)
	await startLive(h)
	// Сессия звука ученика не слышит вообще: ей идёт дорожка тишины, а
	// настоящий микрофон остаётся у нарезки фраз; сессии сказано прямо.
	assert.equal(h.peer().sender.track, h.silentTrack)
	assert.equal(h.track.enabled, true)
	assert.equal(h.of('session.input_audio.mute').length, 1)

	// Шум класса без кнопки фразой не становится.
	const capture = h.capture()
	feed({ push: capture.onChunk }, join(speech(1200), quiet(1000)))
	await settle()
	assert.equal(h.called('transcribe').length, 0)

	// Нажал — говорит: слушает нарезка, сессия по-прежнему глуха.
	assert.equal(h.talk.setMic(true), true)
	assert.equal(h.peer().sender.track, h.silentTrack)
	assert.equal(h.of('session.input_audio.unmute').length, 0)
	assert.ok(h.acted('live_mic').some((a) => a.open === true))
	// Фраза без паузы в конце: ученик нажал «готово» сразу после слов.
	feed({ push: capture.onChunk }, join(speech(1200), quiet(200)))
	await settle()
	assert.equal(h.called('transcribe').length, 0, 'пауза короткая — фраза ещё идёт')
	h.talk.setMic(false)
	await settle()
	assert.equal(h.called('transcribe').length, 1, 'кнопка закончила фразу сразу')
	// Микрофон для сессии не трогали: ни mute, ни unmute, дорожка та же.
	assert.equal(h.of('session.input_audio.mute').length, 1)
	assert.equal(h.of('session.input_audio.unmute').length, 0)
	assert.equal(h.track.enabled, true)
	assert.equal(h.acted('live_mic').at(-1).open, false)
	// Фраза разобрана — слова уходят сессии текстом, и она отвечает на них.
	assert.deepEqual(h.of('response.item.create').map((e) => e.item.content[0].text), ['Мин Казанда яшим'])
	assert.deepEqual(h.of('response.create').map((e) => e.event_id), ['turn_1'])
	// Самой по себе — без слов ученика — сессию не торопят.
	await h.clock.advance(20000)
	assert.deepEqual(h.of('response.create').map((e) => e.event_id).filter((id) => id !== 'intro'), ['turn_1'])

	// Забытый включённым микрофон закрывается сам через минуту.
	h.talk.setMic(true)
	await h.clock.advance(60000)
	assert.equal(h.acted('live_mic').at(-1).open, false)
	h.talk.dispose()
	await h.close()
})

test('одно нажатие, две фразы с паузой — сессии уходит один ход, и не раньше закрытия кнопки', async () => {
	const h = liveHarness({
		transcripts: [
			{ text: 'Мин Казанда яшим', confidence: 0.93, needs_review: false, review_reasons: [] },
			{ text: 'һәм студент', confidence: 0.93, needs_review: false, review_reasons: [] },
		],
		answers: [{ reply: '', translation: '', turn_no: 1, turns_taken: 1, max_turns: 3, status: 'active', steer: 'Lead to p1.' }],
	})
	await startLive(h)
	const capture = h.capture()
	h.talk.setMic(true)
	// Ученик задумался: фраза закончилась по паузе, но кнопка ещё открыта.
	feed({ push: capture.onChunk }, join(speech(1200), quiet(1100)))
	await settle()
	assert.equal(h.called('transcribe').length, 1)
	assert.equal(h.of('response.create').length, 0, 'кнопка открыта — сессия не вступает')
	assert.equal(h.of('response.item.create').length, 0)
	// Договорил, нажал «готово».
	feed({ push: capture.onChunk }, speech(1200))
	h.talk.setMic(false)
	await settle()
	assert.equal(h.called('transcribe').length, 2)
	assert.deepEqual(h.of('response.item.create').map((e) => e.item.content[0].text), ['Мин Казанда яшим һәм студент'])
	assert.deepEqual(h.of('response.create').map((e) => e.event_id), ['turn_1'])
	h.talk.dispose()
	await h.close()
})

test('неуверенная фраза: сессии слова уходят сразу, не дожидаясь «Верно»', async () => {
	const h = liveHarness({
		transcripts: [{ text: 'Мин казанда яшим', confidence: 0.4, needs_review: true, review_reasons: ['low_confidence'] }],
		answers: [{ turn_no: 1, turns_taken: 1, max_turns: 3, status: 'active', steer: 'Next.' }],
	})
	await startLive(h)
	h.say()
	await settle()
	assert.ok(h.acted('live_student').some((a) => a.stage === 'review'), 'ученик проверяет текст')
	assert.equal(h.called('answer').length, 0, 'ответ ждёт проверки')
	assert.deepEqual(h.of('response.item.create').map((e) => e.item.content[0].text), ['Мин казанда яшим'])
	assert.deepEqual(h.of('response.create').map((e) => e.event_id), ['turn_1'])
	h.talk.dispose()
	await h.close()
})

test('фраза не разобрана или пуста — сессии нечего сказать, ответа она не получает', async () => {
	const h = liveHarness({ transcripts: [{ text: '', confidence: null, needs_review: true }] })
	await startLive(h)
	h.say()
	await settle()
	assert.equal(h.of('response.item.create').length, 0)
	assert.equal(h.of('response.create').length, 0)
	h.talk.dispose()
	await h.close()
})

test('«Повторить»: запись входящего звука реплики; при открытой кнопке не звучит', async () => {
	const recorders = []
	class FakeRecorder {
		constructor(stream) {
			this.stream = stream
			this.state = 'inactive'
			this.mimeType = 'audio/webm'
			recorders.push(this)
		}
		start() {
			this.state = 'recording'
		}
		stop() {
			this.state = 'inactive'
			this.ondataavailable?.({ data: new Blob([new Uint8Array(4000)], { type: 'audio/webm' }) })
			this.onstop?.()
		}
	}
	const h = liveHarness({ recorder: FakeRecorder })
	await startLive(h)
	h.audio.srcObject = { id: 'remote' }
	h.avatar('Исәнме!')
	await settle()
	assert.equal(recorders.length, 1)
	assert.equal(recorders[0].stream, h.audio.srcObject, 'пишется входящий поток сессии')
	h.avatarDone()
	await h.clock.advance(3100)
	const sound = h.acted('live_avatar').find((a) => a.sound === true)
	assert.ok(sound, 'запись готова — у реплики есть «Повторить»')

	const before = h.sent.length
	assert.equal(h.talk.toggleReplay(sound.id), true)
	assert.equal(h.sent.length, before, 'сессия звука и так не слышит — ей ничего не шлём')
	assert.ok(h.acted('live_replay').some((a) => a.id === sound.id))
	h.talk.toggleReplay(sound.id)
	assert.equal(h.sent.length, before)
	assert.ok(h.acted('live_replay').some((a) => a.id === null))
	// Пока ученик говорит, запись не звучит: микрофон услышал бы её.
	h.talk.setMic(true)
	assert.equal(h.talk.toggleReplay(sound.id), false)
	h.talk.dispose()
	await h.close()

	// Браузер не пишет входящий звук — кнопки нет.
	const plain = liveHarness()
	await startLive(plain)
	plain.audio.srcObject = { id: 'remote' }
	plain.avatar('Исәнме!')
	plain.avatarDone()
	await plain.clock.advance(3100)
	assert.equal(plain.acted('live_avatar').some((a) => a.sound === true), false)
	plain.talk.dispose()
	await plain.close()
})

test('живой режим доступен только с микрофоном, WebRTC и AudioContext', () => {
	const ok = {
		window: {},
		isSecureContext: true,
		navigator: { mediaDevices: { getUserMedia() {} } },
		RTCPeerConnection: function () {},
		AudioContext: function () {},
	}
	assert.equal(liveSupported(ok), true)
	assert.equal(liveSupported({ ...ok, isSecureContext: false }), false)
	assert.equal(liveSupported({ ...ok, RTCPeerConnection: undefined }), false)
	assert.equal(liveSupported({ ...ok, AudioContext: undefined, webkitAudioContext: function () {} }), true)
	assert.equal(liveSupported({}), false)
	assert.equal(createAudioContext({}), null)

	// Почему нет: сайт по http — микрофона не даст ни один браузер, дело в
	// адресе; на https без WebRTC или AudioContext — дело в браузере.
	assert.equal(liveBlocker(ok), '')
	assert.equal(liveBlocker({ ...ok, isSecureContext: false, navigator: {} }), 'insecure')
	assert.equal(liveBlocker({ ...ok, RTCPeerConnection: undefined }), 'browser')
	assert.equal(liveBlocker({ ...ok, navigator: {} }), 'browser')
})

test('без голосового разговора: http и старый браузер — разные причины', () => {
	const load = (liveBlocked) =>
		reducer(initialState(), {
			type: 'loaded',
			res: conversation(),
			restored: { pending: null, saved: false },
			liveSupported: false,
			liveBlocked,
		})
	assert.equal(load('insecure').live.unavailable, LIVE_INSECURE_MESSAGE)
	assert.equal(load('browser').live.unavailable, LIVE_BROWSER_MESSAGE)
	assert.equal(load(undefined).live.unavailable, LIVE_BROWSER_MESSAGE)
	assert.notEqual(LIVE_INSECURE_MESSAGE, LIVE_BROWSER_MESSAGE)
})

/* ---------------------------------------------------------------------------
 * Лента и режим: редьюсер
 * ------------------------------------------------------------------------- */

function conversation(over = {}) {
	return {
		enabled: true,
		language: 'tatar',
		level: 'a2',
		status: 'active',
		max_turns: 3,
		turns_taken: 1,
		turns: [
			{ turn_no: 0, role: 'avatar', content: 'Исәнме!', translation: 'Привет!', input: 'text', edited: false },
			{ turn_no: 1, role: 'student', content: 'Сәлам', translation: '', input: 'voice', edited: false },
			{ turn_no: 1, role: 'avatar', content: 'Син кайда яшисең?', translation: 'Где ты живёшь?', input: 'text', edited: false },
		],
		pending_answer: null,
		report_status: '',
		live_available: true,
		...over,
	}
}

const loadedLive = (res = conversation(), liveOk = true) =>
	reducer(initialState(), { type: 'loaded', res, restored: { pending: null, saved: false }, liveSupported: liveOk })

test('режим: голосовой разговор по умолчанию; без него — ввод текстом с причиной', () => {
	assert.equal(loadedLive().mode, 'live')
	assert.equal(loadedLive().live.unavailable, '')
	assert.equal(loadedLive(conversation(), false).mode, 'text')
	assert.equal(loadedLive(conversation(), false).live.unavailable, LIVE_BROWSER_MESSAGE)
	assert.equal(loadedLive(conversation({ live_available: false })).mode, 'text')
	assert.equal(loadedLive(conversation({ live_available: false })).live.unavailable, LIVE_OFF_MESSAGE)
	assert.equal(loadedLive(conversation({ live_available: undefined })).live.available, false)

	// Ученик выбрал ввод текстом — причины нет, перечитывание выбор не сбрасывает.
	let s = reducer(loadedLive(), { type: 'mode', mode: 'text' })
	assert.deepEqual([s.mode, s.composer, s.live.unavailable], ['text', 'text', ''])
	s = reducer(s, { type: 'loaded', res: conversation(), restored: { pending: null, saved: false }, liveSupported: true })
	assert.equal(s.mode, 'text')
	assert.equal(reducer(s, { type: 'mode', mode: 'live' }).mode, 'live')

	// Посреди живой сессии и отправки режим не переключается.
	const busy = reducer(loadedLive(), { type: 'live_status', status: 'active' })
	assert.equal(reducer(busy, { type: 'mode', mode: 'text' }).mode, 'live')
	const sending = reducer(s, { type: 'send', pending: makeAnswer({ text: 'Сәлам', input: 'text', clientMsgId: 'x', turnNo: 2 }) })
	assert.equal(canSwitchMode(sending, 'live'), false)
	// Без live_available в голосовой разговор не переключить.
	assert.equal(reducer(loadedLive(conversation({ live_available: false })), { type: 'mode', mode: 'live' }).mode, 'text')

	// Голосовой разговор не поднялся до сессии (нет звука микрофона) — текст с причиной.
	const off = reducer(loadedLive(), { type: 'live_unavailable', message: 'Браузер не дал разбирать звук микрофона.' })
	assert.deepEqual([off.mode, off.live.unavailable], ['text', 'Браузер не дал разбирать звук микрофона.'])
	// «Повторить» — снова голосовой разговор, причина снимается.
	const again = reducer(off, { type: 'mode', mode: 'live' })
	assert.deepEqual([again.mode, again.live.unavailable], ['live', ''])
	// «Повторить» можно, пока ответ не уходит и сессии нет — даже когда
	// сервер голосовой разговор не давал: повтор спросит его заново.
	const offServer = loadedLive(conversation({ live_available: false }))
	assert.equal(canRetryLive(offServer), true)
	assert.equal(canSwitchMode(offServer, 'live'), false)
	assert.equal(canRetryLive(sending), false)
	assert.equal(canRetryLive(busy), false)
	assert.equal(canRetryLive(reducer(off, { type: 'live_handover', message: 'x' })), false, 'пока дописывается — нельзя')
	// Перечитывание после сбоя причину не теряет.
	const reread = reducer(off, { type: 'loaded', res: conversation(), restored: { pending: null, saved: false }, liveSupported: true })
	assert.deepEqual([reread.mode, reread.live.unavailable], ['text', 'Браузер не дал разбирать звук микрофона.'])
})

test('лента: фраза ученика — скрыта, распознаётся, проверка, ответ; «Осталось» — по засчитанным', () => {
	let s = reducer(loadedLive(), { type: 'live_status', status: 'active' })
	s = reducer(s, { type: 'live_student', id: 'p1', stage: 'hearing' })
	const key = liveKey('student', 'p1')
	assert.equal(s.turns.at(-1).key, key)
	assert.equal(s.turns.at(-1).live.stage, 'hearing')
	s = reducer(s, { type: 'live_student', id: 'p1', stage: 'recognizing' })
	const asr = readAsr({ text: 'Мин казанда яшим', confidence: 0.4, needs_review: true, review_reasons: ['low_confidence'] })
	s = reducer(s, { type: 'live_student', id: 'p1', stage: 'review', asr, deadline: 42 })
	let t = s.turns.at(-1)
	assert.equal(t.content, 'Мин казанда яшим')
	assert.equal(t.live.draft, 'Мин казанда яшим')
	assert.equal(t.live.deadline, 42)
	// Показ распознанного ответом не считается.
	assert.equal(s.turnsTaken, 1)
	assert.equal(leftLabel(answersLeft(s.maxTurns, s.turnsTaken)), 'Осталось 2 ответа')

	s = reducer(s, { type: 'live_student', id: 'p1', stage: 'editing' })
	s = reducer(s, { type: 'live_draft', id: 'p1', text: 'Мин Казанда яшим' })
	assert.equal(s.turns.at(-1).live.draft, 'Мин Казанда яшим')
	s = reducer(s, { type: 'live_student', id: 'p1', stage: 'queued', text: 'Мин Казанда яшим', edited: true })
	assert.equal(s.turnsTaken, 1)
	s = reducer(s, {
		type: 'live_answered',
		id: 'p1',
		res: { turn_no: 2, turns_taken: 2, max_turns: 3, steer: 'x' },
		pending: { text: 'Мин Казанда яшим', edited: true, turnNo: 2 },
	})
	t = s.turns.at(-1)
	assert.equal(t.live.stage, 'sent')
	assert.equal(t.turnNo, 2)
	assert.equal(t.edited, true)
	assert.equal(s.turnsTaken, 2)
	assert.equal(leftLabel(answersLeft(s.maxTurns, s.turnsTaken)), 'Остался 1 ответ')

	// Не ответ — пузырь убирается; поздняя весть о снятой фразе её не возвращает.
	s = reducer(s, { type: 'live_student', id: 'p2', stage: 'hearing' })
	s = reducer(s, { type: 'live_student', id: 'p2', stage: 'dropped' })
	s = reducer(s, { type: 'live_student', id: 'p2', stage: 'late' })
	assert.equal(s.turns.some((x) => x.key === liveKey('student', 'p2')), false)
})

test('лента: реплика аватара — без текста, подсказка с расшифровкой и переводом, номер', () => {
	let s = reducer(loadedLive(), { type: 'live_status', status: 'active' })
	s = reducer(s, { type: 'live_avatar', id: 'r1', stage: 'speaking' })
	const key = liveKey('avatar', 'r1')
	let t = s.turns.find((x) => x.key === key)
	assert.equal(t.role, 'avatar')
	assert.equal(t.content, '')
	s = reducer(s, { type: 'live_avatar', id: 'r1', content: 'Яхшы!' })
	s = reducer(s, { type: 'live_avatar', id: 'r1', stage: 'done', content: 'Яхшы! Ә син?' })
	s = reducer(s, { type: 'live_avatar', id: 'r1', stage: 'saved', turnNo: 1, translation: 'Хорошо! А ты?' })
	s = reducer(s, { type: 'live_avatar', id: 'r1', sound: true })
	t = s.turns.find((x) => x.key === key)
	assert.deepEqual([t.turnNo, t.content, t.translation, t.live.stage, t.live.sound], [1, 'Яхшы! Ә син?', 'Хорошо! А ты?', 'saved', true])
	// Подсказка — только показ: счёт ответов не трогает.
	const taken = s.turnsTaken
	s = reducer(s, { type: 'hint', key })
	assert.equal(s.hints[key], true)
	assert.equal(s.turnsTaken, taken)
	// Весть о неизвестной реплике не создаёт пузырь.
	assert.equal(reducer(s, { type: 'live_avatar', id: 'ghost', stage: 'saved' }).turns.length, s.turns.length)
})

test('лента: несколько реплик аватара на один номер — разные ключи', () => {
	const turns = readTurns([
		{ turn_no: 1, role: 'avatar', content: 'а' },
		{ turn_no: 1, role: 'avatar', content: 'б' },
		{ turn_no: 1, role: 'student', content: 'в' },
		{ turn_no: 1, role: 'avatar', content: 'г' },
	])
	assert.deepEqual(
		turns.map((t) => t.key),
		['avatar:1', 'avatar:1#2', 'student:1', 'avatar:1#3'],
	)
})

test('режим: сбой — ввод текстом с причиной, конец — ожидание итога', () => {
	let s = reducer(loadedLive(), { type: 'live_status', status: 'active' })
	s = reducer(s, { type: 'live_flags', talking: true, hearing: false })
	s = reducer(s, { type: 'live_handover', message: 'Разговор прервался (нет связи).' })
	assert.equal(s.mode, 'text')
	assert.equal(s.composer, 'text')
	assert.equal(s.live.status, 'idle')
	assert.equal(s.live.draining, true)
	assert.equal(s.live.talking, false)
	assert.equal(s.live.unavailable, 'Разговор прервался (нет связи).')
	assert.equal(canSwitchMode(s, 'live'), false, 'пока дописывается голосовой разговор, обратно нельзя')
	s = reducer(s, { type: 'live_drained' })
	assert.equal(s.live.draining, false)
	assert.equal(canSwitchMode(s, 'live'), true, '«Повторить» снова соединяет')

	// Ученик сам выбрал ввод текстом посреди разговора — без причины.
	const chosen = reducer(reducer(loadedLive(), { type: 'live_status', status: 'active' }), { type: 'live_handover', message: '' })
	assert.deepEqual([chosen.mode, chosen.live.unavailable], ['text', ''])

	let end = reducer(loadedLive(), { type: 'live_status', status: 'closing' })
	end = reducer(end, { type: 'live_finished' })
	assert.equal(end.phase, 'closed')
	assert.equal(end.summary.status, 'waiting')
	assert.equal(end.live.status, 'ended')

	// …/live: разговор начат — active, счёт с сервера; уже закрыт — итог.
	const intro = loadedLive(conversation({ status: 'not_started', turns_taken: 0, turns: [] }))
	assert.equal(intro.phase, 'intro')
	let opened = reducer(intro, { type: 'live_status', status: 'connecting' })
	assert.equal(opened.starting, true)
	opened = reducer(opened, { type: 'live_open', res: { turns_taken: 0, max_turns: 5, status: 'active' } })
	assert.deepEqual([opened.phase, opened.maxTurns, opened.starting], ['active', 5, false])
	const closed = reducer(intro, { type: 'live_open', res: { turns_taken: 5, max_turns: 5, status: 'closed' } })
	assert.equal(closed.phase, 'closed')

	// Неотправленный ответ после перезагрузки — подпись паузы.
	assert.match(livePendingNote({ pending: { text: 'x' }, sendError: { kind: 'resume' } }), /сохранён/)
	assert.match(livePendingNote({ pending: { text: 'x' }, sendError: { kind: 'unsent' } }), /уйдёт первым/)
	assert.equal(livePendingNote({ pending: null }), '')
})

/* ---------------------------------------------------------------------------
 * Клиент API
 * ------------------------------------------------------------------------- */

test('клиент API: ручки живого режима и mode у ответа — по договору', async () => {
	const calls = []
	globalThis.fetch = async (url, init = {}) => {
		calls.push({ url, init })
		return { ok: true, status: 200, headers: { get: () => null }, json: async () => ({}) }
	}
	const { my } = await import('../src/api/v1.js')
	const body = (i) => JSON.parse(calls[i].init.body)

	await my.openConversationLive('att', 'v=0 offer')
	assert.equal(calls[0].url, '/api/v1/my/attempts/att/conversation/live')
	assert.equal(calls[0].init.method, 'POST')
	assert.deepEqual(body(0), { sdp: 'v=0 offer' })

	const asr = readAsr({ text: 'Мин Казанда яшим', confidence: 0.93, needs_review: false })
	const answer = makeAnswer({ text: asr.text, input: 'voice', asr, clientMsgId: 'c1', turnNo: 2 })
	await my.answerConversation('att', { ...answer, mode: 'live' })
	assert.equal(calls[1].url, '/api/v1/my/attempts/att/conversation/answer')
	assert.deepEqual(body(1), {
		client_msg_id: 'c1',
		text: 'Мин Казанда яшим',
		input: 'voice',
		asr_text: 'Мин Казанда яшим',
		asr_confidence: 0.93,
		edited: false,
		mode: 'live',
	})
	await my.answerConversation('att', answer)
	assert.equal('mode' in body(2), false, 'вне голосового разговора mode не шлют')

	await my.conversationAvatar('att', { clientMsgId: 'r1', turnNo: 2, content: 'Яхшы!', steerTurn: 1 })
	assert.equal(calls[3].url, '/api/v1/my/attempts/att/conversation/avatar')
	assert.deepEqual(body(3), { client_msg_id: 'r1', turn_no: 2, content: 'Яхшы!', steer_turn: 1 })
	await my.conversationAvatar('att', { clientMsgId: 'r0', turnNo: 0, content: 'Исәнме!', steerTurn: 0 })
	assert.equal(body(4).steer_turn, 0, 'указание вступления — номер 0, не пропуск')
	await my.conversationAvatar('att', { clientMsgId: 'r2', turnNo: 2, content: 'Әйе.' })
	assert.equal('steer_turn' in body(5), false)

	await my.conversationProgress('att')
	assert.equal(calls[6].url, '/api/v1/my/attempts/att/conversation/progress')
	assert.equal(calls[6].init.method, 'GET')

	await my.conversationCheck('att', 'Минем исемем Ишка.')
	assert.equal(calls[7].url, '/api/v1/my/attempts/att/conversation/check')
	assert.deepEqual(body(7), { text: 'Минем исемем Ишка.' })

	await my.endConversationLive('att', 'live_1')
	assert.equal(calls[8].url, '/api/v1/my/attempts/att/conversation/live/end')
	assert.deepEqual(body(8), { session_id: 'live_1' })

	const beacons = []
	// В Node 20, на котором собирается фронт, глобального navigator нет —
	// он появился в Node 21.
	const ownNavigator = !('navigator' in globalThis)
	if (ownNavigator) globalThis.navigator = {}
	const original = navigator.sendBeacon
	navigator.sendBeacon = (url, blob) => {
		beacons.push({ url, blob })
		return true
	}
	try {
		assert.equal(my.endConversationLiveBeacon('att', 'live_1'), true)
		assert.equal(beacons[0].url, '/api/v1/my/attempts/att/conversation/live/end')
		assert.deepEqual(JSON.parse(await beacons[0].blob.text()), { session_id: 'live_1' })
	} finally {
		navigator.sendBeacon = original
		if (ownNavigator) delete globalThis.navigator
	}
})

test('живой разговор: сессия сразу попрощалась на последний ответ — второе прощание не просим', async () => {
	const h = liveHarness({
		transcripts: [{ text: 'Сау бул', confidence: 0.9, needs_review: false }],
		answers: [{ turn_no: 1, turns_taken: 1, max_turns: 1, status: 'closed' }],
		open: { max_turns: 1 },
	})
	await startLive(h)
	h.say()
	// Указание «следующий ответ последний» уже было: сессия отвечает
	// прощанием, пока сервер принимает ответ.
	h.avatar('Рәхмәт! Сау бул!')
	await settle()
	h.avatarDone()
	await settle()
	assert.equal(h.of('response.create').filter((e) => e.event_id === 'farewell').length, 0,
		'второе прощание не просим')
	await h.clock.advance(3100)
	assert.equal(h.acted('live_finished').length, 1)
	await h.close()
	assert.deepEqual(h.called('end'), [['end', 'live_1']])
	h.talk.dispose()
})

test('живой разговор: ушёл с экрана, пока браузер спрашивал микрофон, — сессия не открывается', async () => {
	const permission = deferred()
	const h = liveHarness({ mic: permission.promise })
	const started = h.talk.start({ context: h.context })
	await settle()
	h.talk.dispose()
	permission.resolve()
	assert.equal(await started, false)
	await settle()
	assert.deepEqual(h.called('open'), [], 'платная сессия без экрана')
	assert.equal(h.track.stopped, true, 'микрофон отпущен')
})
