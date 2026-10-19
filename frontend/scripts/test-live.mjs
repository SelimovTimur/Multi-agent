import { mock, test } from 'node:test'
import assert from 'node:assert/strict'
import { LiveConnection } from '../src/cabinet/live/connection.js'

test('keeps final transcripts while closing and ignores events after close', async () => {
	const turns = []
	const h = harness({ onTranscript: (role, text) => turns.push({ role, text }) })
	await h.call.start()
	const dc = h.peer().dc
	const emit = (type, delta) => dc.onmessage({ data: JSON.stringify({ type, delta }) })
	emit('session.input_transcript.delta', 'Мин ')
	const stopped = h.call.stop()
	emit('session.input_transcript.delta', 'укыйм.')
	emit('session.input_transcript.done')
	emit('session.output_transcript.delta', 'Яхшы.')
	emit('session.closed')
	await stopped
	emit('session.input_transcript.delta', 'lost')
	h.call.finish()
	assert.deepEqual(turns, [{ role: 'student', text: 'Мин укыйм.' }, { role: 'avatar', text: 'Яхшы.' }])
})

function harness(overrides = {}) {
	const states = [], errors = [], sent = []
	const track = { stop() { this.stopped = true } }
	const stream = { getTracks: () => [track] }
	const silentTrack = { stop() { this.stopped = true } }
	const silentStream = { getTracks: () => [silentTrack] }
	const audio = { pause() { this.paused = true }, play: async () => {}, srcObject: null }
	let pc, requests = 0
	class Peer {
		constructor() {
			pc = this
			this.iceGatheringState = 'complete'
			this.localDescription = { sdp: 'offer' }
			this.dc = { readyState: 'open', send: (data) => sent.push(JSON.parse(data)), close() { this.closed = true } }
		}
		addTrack(track) { this.sender = { track, replaceTrack: async next => { this.sender.track = next } }; return this.sender }
		createDataChannel() { return this.dc }
		async createOffer() { return { sdp: 'offer' } }
		async setLocalDescription() {}
		async setRemoteDescription() {}
		close() { this.closed = true }
	}
	const call = new LiveConnection({ audio, onState: (s) => states.push(s), onError: (e) => errors.push(e),
		createSilentStream: () => silentStream,
		mediaDevices: { getUserMedia: async () => stream }, PeerConnection: Peer,
		createSession: async () => { requests++; return { transport: { sdp: 'answer' } } }, ...overrides })
	return { call, audio, track, stream, silentTrack, states, errors, sent, peer: () => pc, requests: () => requests }
}

test('reports start, connection and failed first audio without transcript text', async () => {
	const events = []
	const h = harness({ onTelemetry: (event) => events.push(event) })
	await h.call.start()
	h.peer().dc.onmessage({ data: '{"type":"session.started"}' })
	h.peer().connectionState = 'failed'
	h.peer().onconnectionstatechange()
	assert.deepEqual(events.map(({ stage, result, protocol }) => ({ stage, result, protocol })), [
		{ stage: 'start', result: 'ok', protocol: '' },
		{ stage: 'connect', result: 'ok', protocol: '' },
		{ stage: 'first_audio', result: 'failed', protocol: '' },
	])
	assert.equal(events.some((event) => 'content' in event || 'message' in event), false)
})

test('reports an unexpected provider close as a broken session', async () => {
	const events = []
	const h = harness({ onTelemetry: (event) => events.push(event) })
	await h.call.start()
	h.peer().dc.onmessage({ data: '{"type":"session.started"}' })
	h.peer().dc.onmessage({ data: '{"type":"session.closed"}' })
	assert.deepEqual(events.at(-1), {
		stage: 'session', result: 'failed', protocol: '', network: 'online',
	})
})

test('reports silence at the start without ending the conversation, and counts one failure per call', async () => {
	mock.timers.enable({ apis: ['setTimeout'] })
	try {
		const events = []
		const h = harness({ onTelemetry: (event) => events.push(event) })
		await h.call.start()
		h.peer().dc.onmessage({ data: '{"type":"session.started"}' })
		mock.timers.tick(10000)
		assert.deepEqual(events.at(-1), { stage: 'first_audio', result: 'failed', protocol: '', network: 'online' })
		assert.deepEqual(h.errors, [])
		assert.equal(h.states.at(-1), 'active')

		h.peer().connectionState = 'failed'
		h.peer().onconnectionstatechange()
		assert.equal(events.filter((event) => event.result === 'failed').length, 1)
		assert.equal(h.errors.length, 1)
	} finally {
		mock.timers.reset()
	}
})

test('first audio is the avatar speaking, not the incoming track starting', async () => {
	mock.timers.enable({ apis: ['setTimeout'] })
	try {
		const events = []
		const h = harness({ onTelemetry: (event) => events.push(event) })
		await h.call.start()
		// Дорожка провайдера приходит и играет тишину ещё до session.started.
		h.peer().ontrack({ streams: [{}], track: {} })
		await Promise.resolve()
		h.peer().dc.onmessage({ data: '{"type":"session.started"}' })
		assert.deepEqual(events.map((event) => event.stage), ['start', 'connect'])

		h.peer().dc.onmessage({ data: '{"type":"session.output_transcript.delta","delta":"Исәнме"}' })
		h.peer().dc.onmessage({ data: '{"type":"session.output_transcript.delta","delta":"!"}' })
		mock.timers.tick(10000)
		assert.deepEqual(events.map(({ stage, result }) => `${stage}:${result}`), ['start:ok', 'connect:ok', 'first_audio:ok'])
	} finally {
		mock.timers.reset()
	}
})

test('silence is counted after the intro wait, not before', async () => {
	mock.timers.enable({ apis: ['setTimeout'] })
	try {
		const events = []
		const h = harness({ onTelemetry: (event) => events.push(event) })
		await h.call.start()
		h.peer().ontrack({ streams: [{}], track: {} })
		await Promise.resolve()
		h.peer().dc.onmessage({ data: '{"type":"session.started"}' })
		// Сессия может промолчать 4 с, пока разговор не попросит вступление.
		mock.timers.tick(9000)
		assert.equal(events.some((event) => event.result === 'failed'), false)
		mock.timers.tick(1000)
		assert.deepEqual(events.at(-1), { stage: 'first_audio', result: 'failed', protocol: '', network: 'online' })
	} finally {
		mock.timers.reset()
	}
})

test('takes the page protocol from navigation timing', async () => {
	const original = Object.getOwnPropertyDescriptor(globalThis, 'performance')
	Object.defineProperty(globalThis, 'performance', { configurable: true,
		value: { getEntriesByType: (type) => (type === 'navigation' ? [{ nextHopProtocol: 'http/1.1' }] : []) } })
	try {
		const events = []
		const h = harness({ onTelemetry: (event) => events.push(event) })
		await h.call.start()
		assert.equal(events[0].protocol, 'http/1.1')
	} finally {
		Object.defineProperty(globalThis, 'performance', original)
	}
})

test('waits for session.started, greets once, stops capture immediately and closes gracefully', async () => {
	const h = harness()
	await h.call.start()
	assert.deepEqual(h.states, ['connecting'])
	const dc = h.peer().dc
	dc.onmessage({ data: '{"type":"session.started"}' })
	dc.onmessage({ data: '{"type":"session.started"}' })
	assert.deepEqual(h.states, ['connecting', 'active'])
	assert.equal(h.sent.filter(e => e.type === 'session.instructions.append').length, 1)
	const stop = h.call.stop()
	assert.equal(h.track.stopped, true)
	assert.equal(h.audio.srcObject, null)
	assert.equal(h.peer().closed, undefined)
	assert.equal(h.sent.at(-1).type, 'session.close')
	dc.onmessage({ data: '{"type":"session.closed"}' })
	await stop
	assert.equal(h.peer().closed, true)
})

test('late microphone permission after cancel never starts a paid session', async () => {
	let grant
	const h = harness({ mediaDevices: { getUserMedia: () => new Promise(r => { grant = r }) } })
	const start = h.call.start()
	await h.call.stop()
	grant(h.stream)
	await start
	assert.equal(h.track.stopped, true)
	assert.equal(h.requests(), 0)
})

test('microphone denial is recoverable and never reaches OpenAI', async () => {
	const h = harness({ mediaDevices: { getUserMedia: async () => { throw Object.assign(new Error(), { name: 'NotAllowedError' }) } } })
	await h.call.start()
	assert.match(h.errors[0], /Разрешите доступ/)
	assert.equal(h.requests(), 0)
	assert.equal(h.call.stopped, true)
})

test('cancel during server handshake aborts request and discards late answer', async () => {
	let respond, signal
	const h = harness({ createSession: (_, s) => { signal = s; return new Promise(r => { respond = r }) } })
	const start = h.call.start()
	while (!respond) await new Promise(r => setImmediate(r))
	const stopped = h.call.stop()
	assert.equal(signal.aborted, true)
	h.peer().dc.onmessage({ data: '{"type":"session.closed"}' })
	respond({ transport: { sdp: 'answer' } })
	await Promise.all([start, stopped])
	assert.equal(h.track.stopped, true)
	assert.equal(h.states.includes('active'), false)
})

// Ключ OpenAI держит ограниченное число голосовых сессий, и место на нём
// освобождает только сигнал о конце разговора. Сигнал — один на сессию, с
// её идентификатором, при любом конце: остановке, обрыве, закрытии.
test('reports the end of the session once, with its id', async () => {
	const ended = []
	const h = harness({
		createSession: async () => ({ session: { id: 'live_42' }, transport: { sdp: 'answer' } }),
		onEnd: (id) => ended.push(id),
	})
	await h.call.start()
	const dc = h.peer().dc
	const stopped = h.call.stop()
	dc.onmessage({ data: '{"type":"session.closed"}' })
	await stopped
	h.call.finish()
	h.call.ended()
	assert.deepEqual(ended, ['live_42'])
})

// Сессия не открылась — сообщать не о чем: места на ключе она не заняла.
test('does not report an end for a session that never opened', async () => {
	const ended = []
	const h = harness({
		createSession: async () => { throw new Error('Все голосовые линии заняты') },
		onEnd: (id) => ended.push(id),
	})
	await h.call.start()
	await h.call.stop()
	assert.deepEqual(ended, [])
})

test('video sessions start silently and microphone gating also notifies GPT-Live', async () => {
	const h = harness({ greeting: false, inputEnabled: false })
	await h.call.start()
	assert.equal(h.track.enabled, false)
	h.peer().dc.onmessage({ data: '{"type":"session.started"}' })
	assert.equal(h.sent.some(e => e.type === 'session.instructions.append'), false)
	h.call.setInputEnabled(true)
	assert.equal(h.track.enabled, true)
	assert.equal(h.sent.at(-1).type, 'session.input_audio.unmute')
	h.call.setInputEnabled(false)
	assert.equal(h.track.enabled, false)
	assert.equal(h.sent.at(-1).type, 'session.input_audio.mute')
	const stopped = h.call.stop()
	h.peer().dc.onmessage({ data: '{"type":"session.closed"}' })
	await stopped
})

test('text-only video call connects without a microphone and survives a later denial', async () => {
	let captures = 0
	const h = harness({ greeting: false, inputEnabled: false, captureInput: false,
		mediaDevices: { getUserMedia: async () => { captures++; throw Object.assign(new Error(), { name: 'NotFoundError' }) } } })
	await h.call.start()
	h.peer().dc.onmessage({ data: '{"type":"session.started"}' })
	assert.equal(captures, 0)
	assert.equal(h.requests(), 1)
	assert.equal(h.peer().sender.track, h.silentTrack)
	await assert.rejects(h.call.ensureMicrophone(), { name: 'NotFoundError' })
	assert.equal(h.call.stopped, false)
	h.call.send({ type: 'response.create' })
	assert.equal(h.sent.at(-1).type, 'response.create')
	const stopped = h.call.stop()
	h.peer().dc.onmessage({ data: '{"type":"session.closed"}' })
	await stopped
})

test('lazy microphone shares one request and attaches without creating another paid session', async () => {
	let grant, captures = 0
	const h = harness({ inputEnabled: false, captureInput: false,
		mediaDevices: { getUserMedia: () => { captures++; return new Promise(resolve => { grant = resolve }) } } })
	await h.call.start()
	const first = h.call.ensureMicrophone(), second = h.call.ensureMicrophone()
	grant(h.stream)
	await Promise.all([first, second])
	assert.equal(captures, 1)
	assert.equal(h.peer().sender.track, h.track)
	assert.equal(h.track.enabled, false)
	h.call.setInputEnabled(true)
	assert.equal(h.track.enabled, true)
	assert.equal(h.requests(), 1)
	const stopped = h.call.stop()
	h.peer().dc.onmessage({ data: '{"type":"session.closed"}' })
	await stopped
})

test('closing a text-only call stops a late microphone grant without attaching it', async () => {
	let grant
	const h = harness({ captureInput: false,
		mediaDevices: { getUserMedia: () => new Promise(resolve => { grant = resolve }) } })
	await h.call.start()
	const pending = h.call.ensureMicrophone()
	const stopped = h.call.stop()
	h.peer().dc.onmessage({ data: '{"type":"session.closed"}' })
	grant(h.stream)
	assert.equal(await pending, null)
	await stopped
	assert.equal(h.track.stopped, true)
	assert.equal(h.peer().sender.track, h.silentTrack)
	assert.equal(h.silentTrack.stopped, true)
	assert.equal(h.requests(), 1)
})
