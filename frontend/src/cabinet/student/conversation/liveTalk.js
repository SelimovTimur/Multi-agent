// liveTalk.js — живой разговор урока: одна сессия GPT Live и всё, что
// работает рядом с ней.
//
// LiveTalk связывает части, каждая из которых проверяется отдельно:
// LiveConnection (WebRTC и канал событий сессии), захват и нарезку фраз
// (utterances.js), очередь реплик и правила (live.js), инструменты делегата
// (ToolBridge). Экран (AvatarChat.jsx) рисует то, что LiveTalk сообщает
// действиями редьюсера (state.js), и передаёт нажатия ученика.
//
// Как идёт разговор:
// 1. start — микрофон и рукопожатие через …/conversation/live. По
//    session.started в сессию уходит указание из ответа сервера: аватар
//    начинает разговор сам; промолчит 4 секунды — response.create.
// 2. Ученик говорит кнопкой (setMic): нажал — нарезка фраз слушает
//    микрофон; нажал ещё раз — ход ученика окончен. Сессия звука ученика не
//    слышит вообще: ей идёт тишина. Поэтому она не вступает на паузе, не
//    отвечает на обрывок фразы и не принимает шум класса за ответ.
//    Фраза ученика распознаётся (/my/transcribe), уверенная сразу становится
//    ответом (mode: live), неуверенная ждёт «Верно» или правки — разговор
//    при этом идёт. Слова уходят сессии текстом вместе с просьбой ответить
//    (sendTurn) сразу, как распознаны, — когда кнопка закрыта и все фразы
//    нажатия распознаны; проверка текста учеником на них не влияет.
//    steer из ответа сервера уходит в сессию, когда ученик снова включит
//    микрофон или заговорит: пришедший сразу, он вызывал вторую реплику
//    подряд.
// 3. Реплика аватара — из расшифровки сессии; записывается по порядку после
//    ответа с её номером и с номером указания, по которому она звучит
//    (steer_turn); перевод для «Подсказки» приходит в ответ на запись.
// 4. Последний ответ — прощание; после него (или через 20 секунд) сессия
//    закрывается, дальше экран ждёт итог.
// 5. Сбой сессии или «Ответить текстом» — разговор переходит к вводу
//    текстом: очередь дописывается, экран перечитывает разговор с сервера —
//    состояние там общее. Голосовых сообщений с записью ответа нет: голос
//    только здесь.
//
// Всё браузерное приходит зависимостями (deps): весь путь проверяется в node
// на поддельном канале — frontend/scripts/test-conversation-live.mjs.

import { LiveConnection } from '../../live/connection.js'
import { scriptFor } from '../../live/tatar-script.js'
import { ToolBridge } from '../../live/tool-bridge.js'
import {
	closingAnswer,
	FAREWELL_GUIDANCE,
	FAREWELL_MS,
	INTRO_GUIDANCE,
	instructionEvent,
	learnerEvent,
	LiveLedger,
	liveAsrStep,
	REVIEW_MS,
	responseEvent,
	steerContent,
} from './live.js'
import { classifySendError, clearPending, makeAnswer, newClientMsgId, readAsr, savePending } from './state.js'
import { levelOf, phraseFile, Segmenter, startCapture } from './utterances.js'

/** Микрофон — с теми же обработками, что у голосовой практики. */
export const MIC_CONSTRAINTS = Object.freeze({
	audio: { echoCancellation: true, noiseSuppression: true, autoGainControl: true },
})

// Громкость входящего звука, выше которой аватар «звучит».
const REMOTE_AUDIBLE = 0.01
// Между словами аватара бывают паузы: столько после звука он ещё «говорит».
const TALK_HOLD_MS = 400
// Запись реплики для «Повторить» заканчивается, когда расшифровка готова
// и звук стих на столько; дольше RECORD_MAX_MS не пишем.
const RECORD_QUIET_MS = 700
const RECORD_MAX_MS = 30000
// Прощание сказано, когда звук стих на столько.
const FAREWELL_QUIET_MS = 800
// Громкость входящего звука браузер не дал измерить — ждём столько после
// расшифровки: звук обычно отстаёт от текста.
const BLIND_TAIL_MS = 3000
// Распознавание упало — один повтор той же записи.
const ASR_RETRY_MS = 1500
// Вступление: столько ждём, что сессия заговорит сама по указанию, прежде
// чем попросить её (response.create).
export const INTRO_WAIT_MS = 4000
// Сессия закрыта, а недосказанную реплику LiveConnection не отдал: столько
// ждём (он сам ждёт подтверждения закрытия до пяти секунд).
const REPLY_SETTLE_MS = 6000
// Записанных реплик для «Повторить» держим не больше.
const KEEP_RECORDINGS = 8
const MIN_RECORDING_BYTES = 2000
// Ученик забыл выключить микрофон — закрываем сами: иначе нарезка фраз
// принимала бы за ответ шум класса весь разговор.
const MIC_MAX_MS = 60000
// Фраза ещё не распознана: пока такие есть, сессии рано отвечать.
const WAITING_STAGES = new Set(['hearing', 'recognizing'])

/**
 * Что мешает говорить вживую: '' — ничего; 'insecure' — страница открыта по
 * http, а микрофон любой браузер даёт только по https или на localhost
 * (дело в адресе, не в браузере); 'browser' — нет микрофона, WebRTC или
 * AudioContext для нарезки фраз.
 */
export function liveBlocker(g = globalThis) {
	if (!g.window) return 'browser'
	if (!g.isSecureContext) return 'insecure'
	const ok =
		g.navigator?.mediaDevices?.getUserMedia &&
		typeof g.RTCPeerConnection === 'function' &&
		typeof (g.AudioContext || g.webkitAudioContext) === 'function'
	return ok ? '' : 'browser'
}

/** Можно ли говорить вживую в этом браузере (см. liveBlocker). */
export function liveSupported(g = globalThis) {
	return liveBlocker(g) === ''
}

/**
 * AudioContext для захвата. Создавать — в обработчике нажатия: Safari
 * включает звук только по жесту ученика, а после await жеста уже нет.
 */
export function createAudioContext(g = globalThis) {
	const Context = g.AudioContext || g.webkitAudioContext
	if (typeof Context !== 'function') return null
	try {
		const context = new Context()
		context.resume?.()?.catch?.(() => {})
		return context
	} catch {
		return null
	}
}

/** Отказ микрофона по имени ошибки: какое окно показать. */
export function micErrorKind(err) {
	const name = err?.name
	if (name === 'NotAllowedError' || name === 'SecurityError') return 'denied'
	if (name === 'NotFoundError' || name === 'OverconstrainedError') return 'missing'
	return 'failed'
}

function stopTracks(stream) {
	try {
		stream?.getTracks?.().forEach((track) => track.stop())
	} catch {
		// Поток уже закрыт.
	}
}

// Первая фраза сообщения LiveConnection — причина без совета «попробуйте
// снова»: «Повторить» и ввод текстом экран показывает сам.
function reasonOf(message) {
	const first = String(message || '').trim().split(/[.!?]\s/)[0].replace(/[.!?]+$/, '')
	return first ? first.charAt(0).toLowerCase() + first.slice(1) : ''
}

/**
 * Дорожка тишины для сессии: настоящий микрофон слышит только нарезка
 * фраз. Сессии нужен входящий поток, даже если слов в нём нет.
 */
function silentStreamOf(context) {
	const destination = context.createMediaStreamDestination()
	const source = context.createConstantSource()
	source.offset.value = 0
	source.connect(destination)
	source.start()
	return destination.stream
}

function browserDeps() {
	const g = globalThis
	return {
		getUserMedia: (constraints) => g.navigator.mediaDevices.getUserMedia(constraints),
		createConnection: (options) => new LiveConnection(options),
		silentStream: silentStreamOf,
		startCapture,
		MediaRecorder: g.MediaRecorder,
		createAudio: () => new g.Audio(),
		createObjectURL: (blob) => g.URL.createObjectURL(blob),
		revokeObjectURL: (url) => g.URL.revokeObjectURL(url),
		now: () => Date.now(),
		setTimeout: (fn, ms) => g.setTimeout(fn, ms),
		clearTimeout: (id) => g.clearTimeout(id),
		setInterval: (fn, ms) => g.setInterval(fn, ms),
		clearInterval: (id) => g.clearInterval(id),
		newId: () => newClientMsgId(),
	}
}

/** Реплика задаёт вопрос — значит, это не прощание. */
function asksQuestion(text) {
	return /[?？]/.test(String(text || ''))
}

/**
 * LiveTalk — один живой разговор от «Начать» до прощания или сбоя.
 *
 * api — ручки ученика для этой попытки: open(sdp, {signal}), answer(answer),
 * avatar(reply), progress(), check(text), end(sessionId), endBeacon(sessionId),
 * transcribe(file, {language}). dispatch — действия редьюсера экрана.
 * onDrained — после перехода к вводу текстом очередь дописана;
 * onConflict — сервер сообщил, что разговор уже не тот (409); onMicError —
 * микрофон не дали ('denied' | 'missing' | 'failed').
 */
export class LiveTalk {
	constructor({
		attemptId,
		language = '',
		turnsTaken = 0,
		maxTurns = 0,
		audio = null,
		api,
		dispatch,
		report,
		reportLive,
		storage = null,
		onConflict,
		onDrained,
		onMicError,
		deps = {},
	}) {
		this.attemptId = attemptId
		this.language = language
		this.maxTurns = maxTurns
		this.audio = audio
		this.api = api
		this.ui = dispatch || (() => {})
		this.report = report || (() => {})
		this.reportLive = reportLive || (() => {})
		this.storage = storage
		this.onConflict = onConflict || (() => {})
		this.onDrained = onDrained || (() => {})
		this.onMicError = onMicError || (() => {})
		this.d = { ...browserDeps(), ...deps }

		// idle → connecting → active → closing → ended; сбой или переключение —
		// handover. finishing — новых фраз больше не берём.
		this.status = 'idle'
		this.finishing = false
		// Фразы ученика, пока они не записаны: id → { stage, asr, timer }.
		this.students = new Map()
		this.segIds = new Map()
		this.replies = new Map()
		this.openReply = null
		this.steers = 0
		// Номер хода последнего указания, ушедшего в сессию (вступление — 0).
		// Реплика аватара запоминает его в момент своего первого слова.
		this.steerTurn = null
		this.talking = false
		this.hearing = false
		// Микрофон ученика включён кнопкой (setMic).
		this.micOpen = false
		// Когда ученик последний раз включил микрофон и начал фразу, и когда
		// аватар последний раз заговорил: сессия слышит ученика вживую и
		// вступает сама, бывает и на паузе при включённом микрофоне.
		this.micOpenedAt = 0
		this.phraseStartedAt = 0
		this.replyStartedAt = 0
		// Слова ученика, ещё не отправленные сессии, и счётчик её ответов.
		this.spoken = []
		this.turns = 0
		this.lastSound = 0
		this.recorders = []
		this.sounds = new Map()
		this.replaying = null

		this.ledger = new LiveLedger({
			lastTurnNo: turnsTaken,
			sendAnswer: (pending) => this.sendAnswer(pending),
			saveAvatar: (reply) => this.api.avatar(reply),
			sleep: (ms) => this.sleep(ms),
			on: {
				sending: ({ id }) => this.studentStage(id, 'sending'),
				answered: (event) => this.answered(event),
				rejected: ({ id, message }) => this.studentDone(id, 'rejected', message),
				unsent: ({ id, message }) => this.studentStage(id, 'unsent', { note: message }),
				conflict: ({ id, message }) => this.conflict(id, message),
				saved: ({ id, res, reply }) =>
					this.ui({ type: 'live_avatar', id, stage: 'saved', turnNo: reply.turnNo, translation: String(res?.translation || '') }),
				unsaved: ({ id, reply }) => this.ui({ type: 'live_avatar', id, stage: 'unsaved', turnNo: reply.turnNo }),
				idle: () => this.checkDrained(),
			},
		})
	}

	sleep(ms) {
		return new Promise((resolve) => this.d.setTimeout(resolve, ms))
	}

	/** Громкость микрофона для индикатора (RMS последнего шага). */
	level() {
		return this.segmenter?.level || 0
	}

	/* --- соединение --- */

	/**
	 * Начать разговор. context — AudioContext из обработчика нажатия; LiveTalk
	 * закрывает его сам. false — микрофона не дали: экран показывает окно.
	 */
	async start({ context = null } = {}) {
		if (this.status !== 'idle') return false
		this.status = 'connecting'
		this.context = context
		this.ui({ type: 'live_status', status: 'connecting' })
		let stream
		try {
			stream = await this.d.getUserMedia(MIC_CONSTRAINTS)
		} catch (err) {
			if (this.status === 'connecting') {
				this.status = 'idle'
				this.finishing = true
				this.ui({ type: 'live_status', status: 'idle' })
				this.release()
				this.onMicError(micErrorKind(err))
			}
			return false
		}
		if (this.status !== 'connecting' || this.disposed || this.finishing) {
			// Пока браузер спрашивал разрешение, ученик ушёл или передумал.
			// dispose статус не меняет, поэтому смотрим и на него: иначе
			// разрешение, данное уже после ухода с экрана, открывало платную
			// сессию с включённым микрофоном, которую нечем было остановить.
			stopTracks(stream)
			return false
		}
		this.stream = stream
		// Голосовая модель расшифровывает себя и на татарском бывает с
		// казахскими буквами и латиницей: в подсказку — кириллицей.
		const script = scriptFor(this.language)
		const connection = this.d.createConnection({
			audio: this.audio,
			greeting: false,
			// Сессия звук ученика не слышит: ей идёт тишина, а слова ученика —
			// текстом (sendTurn). Настоящий микрофон слушает только нарезка фраз.
			captureInput: false,
			mediaDevices: null,
			createSilentStream: () => this.d.silentStream(this.context),
			createSession: (sdp, signal) => this.open(sdp, signal),
			onState: (state) => this.connectionState(state),
			onError: (message) => this.failed(message),
			onTelemetry: (event) => this.reportLive(event),
			// Конец сессии освобождает место на ключе. Ошибка ученику не важна:
			// шлюз снимет место сам по таймауту.
			onEnd: (sessionId) => {
				void Promise.resolve()
					.then(() => this.api.end(sessionId))
					.catch(() => {})
			},
			onEvent: (event) => this.event(event),
			onPartial: (role, text) => {
				if (role === 'avatar') this.avatarPartial(script(text))
			},
			onTranscript: (role, text) => {
				if (role === 'avatar') this.avatarDone(script(text))
			},
		})
		this.connection = connection
		this.bridge = new ToolBridge({
			send: (event) => this.send(event),
			execute: (name, args) => this.tool(name, args),
			isCurrent: () => this.connection === connection && !connection.stopped,
		})
		this.meter = this.d.setInterval(() => this.measure(), 100)
		void connection.start()
		return true
	}

	async open(sdp, signal) {
		const res = (await this.api.open(sdp, { signal })) || {}
		if (this.finishing) return res
		this.opened = res
		if (Number.isInteger(res.turns_taken)) this.ledger.lastTurnNo = res.turns_taken
		if (Number(res.max_turns) > 0) this.maxTurns = Number(res.max_turns)
		this.ui({ type: 'live_open', res })
		// Разговор уже закрыт (последний ответ дошёл с другого устройства) —
		// сессия не нужна. После возврата: LiveConnection успеет запомнить её
		// идентификатор и сообщит о конце.
		if (res.status === 'closed') queueMicrotask(() => this.finish())
		return res
	}

	connectionState(state) {
		if (this.finishing) return
		if (state === 'active') {
			this.status = 'active'
			this.ui({ type: 'live_status', status: 'active' })
		} else if (state === 'reconnecting') {
			this.ui({ type: 'live_status', status: 'reconnecting' })
		} else if (state === 'ended') {
			this.failed('сессия закрылась')
		}
	}

	event(event) {
		if (event?.type === 'session.started' && !this.greeted && !this.finishing) {
			this.greeted = true
			// Вступление: указание движка из ответа …/live, и аватар говорит первым.
			// Номер указания — ход, после которого оно написано: у нового
			// разговора 0, у продолжения — число уже засчитанных ответов.
			const taken = this.opened?.turns_taken
			this.instruct(this.opened?.steer, { fallback: INTRO_GUIDANCE, turn: Number.isInteger(taken) ? taken : 0 })
			// В сессию и так идёт тишина; говорим ей об этом прямо.
			this.send({ type: 'session.input_audio.mute', event_id: 'mic_closed' })
			// Сессия часто здоровается сама, получив указание, и response.create
			// сразу следом давал второе приветствие (на проде — у 64 вступлений
			// из 724 за две недели). Просим, только если она промолчала.
			this.introTimer = this.d.setTimeout(() => {
				if (this.finishing || this.talking || this.replies.size) return
				this.send(responseEvent('intro'))
			}, INTRO_WAIT_MS)
			void this.listen()
		}
		this.bridge?.handle(event)
	}

	/**
	 * Указание сессии на ход turn. Пустой steer — fallback (или ничего).
	 * Ушедшее указание запоминается: реплика, которая начнётся после него,
	 * будет записана с его номером (steer_turn).
	 */
	instruct(steer, { fallback = '', turn = null } = {}) {
		const content = steerContent(steer) || fallback
		if (!content || !this.send(instructionEvent(content, `steer_${++this.steers}`))) return
		if (Number.isInteger(turn)) this.steerTurn = turn
	}

	send(event) {
		const c = this.connection
		if (!c || c.stopped) return false
		c.send(event)
		return true
	}

	endConnection() {
		const c = this.connection
		if (c && !c.stopped) void c.stop()
		// Микрофон принадлежит нам, а не соединению: отпускаем сами.
		stopTracks(this.stream)
	}

	/** Инструменты делегата: проверка себя по состоянию разговора. */
	async tool(name, args) {
		if (name === 'lesson_progress') return { output: await this.api.progress() }
		if (name === 'check_reply') return { output: await this.api.check(String(args?.text ?? '')) }
		throw new Error(`Unknown tool: ${name}`)
	}

	/* --- кнопка микрофона --- */

	/**
	 * Кнопка «говорю»: open — ученик начал говорить, иначе закончил. Пока
	 * микрофон закрыт, сессия получает тишину, а нарезка фраз — ничего.
	 * Закрытие заканчивает фразу сразу, не дожидаясь паузы, и это конец хода
	 * ученика: сессия отвечает (политика, conversation.go). false — нажатие
	 * ничего не поменяло.
	 */
	setMic(open) {
		const next = Boolean(open)
		if (next === this.micOpen) return false
		if (next && (this.status !== 'active' || this.finishing)) return false
		this.micOpen = next
		this.d.clearTimeout(this.micTimer)
		if (next) {
			this.heardSinceOpen = false
			this.micOpenedAt = this.d.now()
			// Запись реплики в динамиках микрофон услышал бы как ученика.
			this.stopReplay()
			// Ход ученика начался: отложенное указание — в сессию, ответит она
			// уже по нему.
			this.releaseSteer()
			this.micTimer = this.d.setTimeout(() => this.setMic(false), MIC_MAX_MS)
		} else {
			this.segmenter?.end()
			this.closedAt = this.d.now()
		}
		this.ui({ type: 'live_mic', open: next })
		// Кнопка закрыта и фраз в разборе нет — слова ученика уходят сессии.
		this.sendTurn()
		return true
	}

	/** Разговор кончается или уходит в текст: кнопка больше не нужна. */
	closeMic() {
		this.d.clearTimeout(this.micTimer)
		if (!this.micOpen) return
		this.micOpen = false
		this.ui({ type: 'live_mic', open: false })
	}

	/**
	 * Ход ученика — сессии текстом и просьба ответить. Только когда кнопка
	 * закрыта и все фразы нажатия распознаны: иначе сессия отвечала бы на
	 * первую фразу, не дождавшись остальных. Слова из нескольких фраз уходят
	 * одним сообщением.
	 */
	sendTurn() {
		if (this.micOpen || this.finishing || !this.spoken.length) return
		for (const item of this.students.values()) {
			if (WAITING_STAGES.has(item.stage)) return
		}
		const text = this.spoken.join(' ')
		if (!this.send(learnerEvent(text))) return
		this.spoken = []
		// Ответ заказан, но ещё не начался: прощание его не дублирует (farewell).
		this.turnAsked = true
		this.send(responseEvent(`turn_${++this.turns}`))
	}

	/* --- фразы ученика --- */

	async listen() {
		if (this.segmenter || this.finishing) return
		if (!this.stream || !this.context) {
			this.failed('браузер не дал разбирать звук микрофона')
			return
		}
		this.segmenter = new Segmenter({
			sampleRate: this.context.sampleRate,
			onStart: (p) => this.phraseStart(p),
			onPhrase: (p) => this.phrase(p),
			onDrop: (p) => this.phraseDrop(p),
		})
		try {
			const capture = await this.d.startCapture(this.stream, {
				context: this.context,
				// Без кнопки звук не режется: дорожка выключена, а тишину
				// фразой считать незачем.
				onChunk: (chunk) => {
					if (this.micOpen) this.segmenter?.push(chunk)
				},
			})
			if (this.finishing) capture.stop()
			else this.capture = capture
		} catch {
			this.failed('не получилось подключить распознавание фраз')
		}
	}

	/** keep — недосказанная фраза ещё станет ответом (сбой, переключение). */
	stopListening({ keep = false } = {}) {
		this.capture?.stop()
		this.capture = null
		const segmenter = this.segmenter
		this.segmenter = null
		if (keep) segmenter?.end()
		else segmenter?.cancel()
		this.setHearing(false)
	}

	phraseStart({ id: seg }) {
		if (this.finishing) return
		const id = this.d.newId()
		this.segIds.set(seg, id)
		this.students.set(id, { id, stage: 'hearing', asr: null, timer: 0 })
		this.phraseStartedAt = this.d.now()
		this.ledger.addStudent(id)
		this.setHearing(true)
		this.releaseSteer()
		this.ui({ type: 'live_student', id, stage: 'hearing' })
	}

	phraseDrop({ id: seg }) {
		this.setHearing(false)
		const id = this.segIds.get(seg)
		this.segIds.delete(seg)
		if (id) this.drop(id)
	}

	phrase(p) {
		this.setHearing(false)
		this.heardSinceOpen = true
		// Конец последней фразы ученика: по нему прощание узнаёт, ответила ли
		// уже сессия на последний ответ (farewell). Порядковый номер, а не
		// время: события одного мгновения по времени не различить.
		this.lastPhraseOrder = this.nextOrder()
		const id = this.segIds.get(p.id)
		this.segIds.delete(p.id)
		if (!id || !this.students.has(id)) return
		this.studentStage(id, 'recognizing')
		void this.recognize(id, phraseFile(p))
	}

	async recognize(id, file) {
		let res = null
		for (let attempt = 0; ; attempt++) {
			try {
				res = await this.api.transcribe(file, { language: this.language })
				break
			} catch (err) {
				if (!this.students.has(id)) return
				// Речь не разобрана (шум, вдох, тишина) — то же, что пустой
				// текст: сессия фразу уже услышала, повтор её не разберёт.
				if (err?.isValidation) {
					this.drop(id)
					return
				}
				if (attempt === 0) {
					await this.sleep(ASR_RETRY_MS)
					if (!this.students.has(id)) return
					continue
				}
				// Сбой сервиса, а не ученика: в журнал, ответом фраза не становится.
				this.report('asr', this.ledger.lastTurnNo + 1, err?.message || 'Распознавание недоступно')
				this.students.delete(id)
				this.ledger.resolveStudent(id, null)
				this.ui({ type: 'live_student', id, stage: 'asr_failed' })
				this.sendTurn()
				return
			}
		}
		const item = this.students.get(id)
		if (!item || item.stage !== 'recognizing') return
		const asr = readAsr(res)
		const step = liveAsrStep(asr)
		if (step === 'empty') {
			this.drop(id)
			return
		}
		item.asr = asr
		// Сессии слова уходят сразу, как распознаны: разговор не ждёт, пока
		// ученик проверит текст (он влияет на запись ответа, не на разговор).
		this.spoken.push(asr.text.trim())
		// Уверенно — ответ сразу. Ученик ушёл с экрана — проверять некому: как есть.
		if (step === 'send' || this.unattended) this.accept(id, asr.text)
		else this.review(item)
		this.sendTurn()
	}

	review(item) {
		item.stage = 'review'
		this.d.clearTimeout(item.timer)
		this.ui({ type: 'live_student', id: item.id, stage: 'review', asr: item.asr, deadline: this.d.now() + REVIEW_MS })
		// Ученик не ответил — текст уходит как есть: разговор идёт, ждать его
		// дольше незачем.
		item.timer = this.d.setTimeout(() => {
			if (this.students.get(item.id)?.stage === 'review') this.accept(item.id, item.asr.text)
		}, REVIEW_MS)
	}

	/** Фраза стала ответом: в очередь. edited — по факту, его считает makeAnswer. */
	accept(id, text) {
		const item = this.students.get(id)
		const value = String(text || '').trim()
		if (!item?.asr || !value) return false
		this.d.clearTimeout(item.timer)
		const answer = makeAnswer({ text: value, input: 'voice', asr: item.asr, clientMsgId: this.d.newId(), turnNo: null })
		this.studentStage(id, 'queued', { text: answer.text, edited: answer.edited })
		this.ledger.resolveStudent(id, answer)
		return true
	}

	/** Фраза ответом не стала: номера не занимает, пузырь исчезает. */
	drop(id) {
		const item = this.students.get(id)
		if (item) this.d.clearTimeout(item.timer)
		this.students.delete(id)
		this.ledger.resolveStudent(id, null)
		this.ui({ type: 'live_student', id, stage: 'dropped' })
		this.sendTurn()
	}

	studentStage(id, stage, extra = {}) {
		const item = this.students.get(id)
		if (item) item.stage = stage
		this.ui({ type: 'live_student', id, stage, ...extra })
	}

	studentDone(id, stage, note = '') {
		this.students.delete(id)
		this.ui({ type: 'live_student', id, stage, note })
	}

	confirm(id) {
		const item = this.students.get(id)
		if (item?.stage === 'review') this.accept(id, item.asr.text)
	}

	edit(id) {
		const item = this.students.get(id)
		if (item?.stage !== 'review') return
		// Пока ученик правит, текст сам не уходит: он занят им.
		this.d.clearTimeout(item.timer)
		this.studentStage(id, 'editing')
	}

	cancelEdit(id) {
		const item = this.students.get(id)
		if (item?.stage === 'editing') this.review(item)
	}

	sendEdited(id, text) {
		if (this.students.get(id)?.stage === 'editing') this.accept(id, text)
	}

	/** «Не отправлять»: фраза не моя или не ответ. */
	discard(id) {
		const stage = this.students.get(id)?.stage
		if (stage === 'review' || stage === 'editing') this.drop(id)
	}

	/** «Повторить» у ответа, который не дошёл до сервера. */
	retry() {
		this.ledger.retry()
	}

	async sendAnswer(pending) {
		// Ответ хранится до подтверждения: после перезагрузки экран повторит
		// его тем же сообщением (resolvePending, как ответ текстом).
		savePending(this.storage, this.attemptId, pending)
		try {
			const res = await this.api.answer({ ...pending, mode: 'live' })
			clearPending(this.storage, this.attemptId)
			return res
		} catch (err) {
			const outcome = classifySendError(err)
			if (outcome.drop || outcome.kind === 'unavailable') clearPending(this.storage, this.attemptId)
			throw err
		}
	}

	answered({ id, res, pending }) {
		this.students.delete(id)
		this.ui({ type: 'live_answered', id, res, pending })
		if (Number(res.max_turns) > 0) this.maxTurns = Number(res.max_turns)
		if (this.finishing) return
		const turn = Number.isInteger(res.turn_no) ? res.turn_no : pending.turnNo
		if (closingAnswer(res, this.maxTurns)) {
			this.farewell(res.steer, turn)
			return
		}
		if (!res.steer || this.status !== 'active') return
		// Указание на следующий ход уходит в сессию, когда ученик заговорит
		// снова. Сессия на эту фразу уже ответила, а указание, пришедшее через
		// несколько секунд после ответа, она принимала за повод сказать ещё
		// одну реплику (на проде так звучали две реплики подряд, хотя
		// указание идёт в контекст, а не в инструкции). Пока ученик говорит
		// (или включил микрофон), сессия не вступает, а ответит ему уже по
		// новому указанию.
		// Но если сессия уже вступила при включённом микрофоне (она слышит
		// ученика вживую и вступает на паузе сама), указание тоже ждёт: иначе
		// она отвечала по нему второй репликой подряд.
		const answeredThisTurn = this.replyStartedAt >= this.micOpenedAt
		if ((this.hearing || this.micOpen) && !answeredThisTurn) this.instruct(res.steer, { turn })
		else this.heldSteer = { steer: res.steer, turn }
	}

	/** Отложенное указание — в сессию: ученик заговорил. */
	releaseSteer() {
		const held = this.heldSteer
		this.heldSteer = null
		if (held && this.status === 'active' && !this.finishing) this.instruct(held.steer, { turn: held.turn })
	}

	/* --- реплики аватара --- */

	avatarPartial(text) {
		const content = String(text || '').trim()
		if (!content || this.repliesClosed) return
		if (!this.openReply) this.replyOpen()
		const reply = this.replies.get(this.openReply)
		reply.content = content
		const now = this.d.now()
		// Текст подсказки по ходу речи — не чаще трёх раз в секунду.
		if (now - reply.shownAt >= 300) {
			reply.shownAt = now
			this.ui({ type: 'live_avatar', id: reply.id, content })
		}
	}

	/** Первое слово реплики аватара: она встаёт в очередь с номером указания, по которому звучит. */
	replyOpen() {
		const id = this.d.newId()
		this.replyStartedAt = this.d.now()
		this.turnAsked = false
		this.replies.set(id, { id, openedAt: this.d.now(), order: this.nextOrder(), steerTurn: this.steerTurn,
			shownAt: 0, content: '' })
		this.openReply = id
		this.ledger.addAvatar(id, { steerTurn: this.steerTurn })
		this.recordStart(id)
		this.ui({ type: 'live_avatar', id, stage: 'speaking' })
		this.updateTalking()
	}

	avatarDone(text) {
		const content = String(text || '').trim()
		if (!content || this.repliesClosed) return
		if (!this.openReply) this.replyOpen()
		const id = this.openReply
		this.openReply = null
		const reply = this.replies.get(id)
		this.replies.delete(id)
		const now = this.d.now()
		this.ledger.completeAvatar(id, content)
		const rec = this.recorders.find((r) => r.id === id)
		if (rec) rec.doneAt = now
		this.ui({ type: 'live_avatar', id, stage: 'done', content })
		this.updateTalking()
		this.lastReplyDone = { at: now, order: reply?.order ?? 0, steerTurn: reply?.steerTurn ?? null, content }
		if (this.farewellIsOpenReply) {
			// Эту реплику сессия начала в ответ на последний ответ ученика. Без
			// вопроса — это и есть прощание; с вопросом — указание о конце не
			// успело, и попрощаться просим после неё, как раньше.
			this.farewellIsOpenReply = false
			if (asksQuestion(content)) this.askFarewell()
			else this.farewellSaidAt = now
		} else if (this.farewellAfterReply) {
			this.farewellAfterReply = false
			this.askFarewell()
		} else if (this.farewellAsked && reply.openedAt >= this.farewellAskedAt) {
			this.farewellSaidAt = now
		}
	}

	/**
	 * Сессия закрыта, а реплика аватара не договорена. Её досказывает
	 * LiveConnection: закрываясь, он отдаёт недосказанное (flushAll). Не отдал
	 * за REPLY_SETTLE_MS — реплика записывается тем, что успели расслышать:
	 * иначе очередь ждала бы её вечно, а запоздавший остаток стал бы второй
	 * репликой.
	 */
	settleOpenReply() {
		const id = this.openReply
		if (!id) return
		this.d.clearTimeout(this.replyTimer)
		this.replyTimer = this.d.setTimeout(() => {
			if (this.openReply !== id) return
			this.avatarDone(this.replies.get(id)?.content || '…')
			this.repliesClosed = true
		}, REPLY_SETTLE_MS)
	}

	/* --- звук аватара: громкость, запись для «Повторить» --- */

	measure() {
		const now = this.d.now()
		if (this.remoteLevel() > REMOTE_AUDIBLE) this.lastSound = now
		this.updateTalking()
		const quietSince = (t) => now - Math.max(this.lastSound, t)
		const settled = (t) => (this.analyser ? quietSince(t) >= RECORD_QUIET_MS : now - t >= BLIND_TAIL_MS)
		for (const rec of [...this.recorders]) {
			if ((rec.doneAt && settled(rec.doneAt)) || now - rec.startedAt > RECORD_MAX_MS) this.recordStop(rec)
		}
		if (this.farewellSaidAt) {
			const t = this.farewellSaidAt
			if (this.analyser ? quietSince(t) >= FAREWELL_QUIET_MS : now - t >= BLIND_TAIL_MS) this.finish()
		}
	}

	remoteLevel() {
		const stream = this.audio?.srcObject
		const context = this.context
		if (!stream || typeof context?.createMediaStreamSource !== 'function') return 0
		if (stream !== this.remoteStream) {
			this.remoteStream = stream
			try {
				this.remoteSource?.disconnect()
				this.analyser ??= context.createAnalyser()
				this.analyser.fftSize = 1024
				this.remoteBuffer = new Float32Array(this.analyser.fftSize)
				this.remoteSource = context.createMediaStreamSource(stream)
				// Только анализ: в динамики звук идёт через <audio>.
				this.remoteSource.connect(this.analyser)
			} catch {
				this.analyser = null
			}
		}
		if (!this.analyser) return 0
		this.analyser.getFloatTimeDomainData(this.remoteBuffer)
		return levelOf(this.remoteBuffer)
	}

	updateTalking() {
		const talking = Boolean(this.openReply) || (this.lastSound > 0 && this.d.now() - this.lastSound < TALK_HOLD_MS)
		this.segmenter?.setDucking(talking)
		if (talking === this.talking) return
		this.talking = talking
		if (talking) this.stopReplay()
		this.ui({ type: 'live_flags', talking, hearing: this.hearing })
	}

	setHearing(hearing) {
		if (hearing === this.hearing) return
		this.hearing = hearing
		this.ui({ type: 'live_flags', talking: this.talking, hearing })
	}

	recordStart(id) {
		// Новая реплика заканчивает запись прошлой, даже если та ещё ждёт тишины.
		for (const rec of [...this.recorders]) this.recordStop(rec)
		const Recorder = this.d.MediaRecorder
		const stream = this.audio?.srcObject
		if (typeof Recorder !== 'function' || !stream) return
		try {
			const recorder = new Recorder(stream)
			const rec = { id, recorder, chunks: [], startedAt: this.d.now(), doneAt: 0 }
			recorder.ondataavailable = (e) => {
				if (e.data?.size) rec.chunks.push(e.data)
			}
			recorder.onstop = () => this.recorded(rec)
			recorder.start()
			this.recorders.push(rec)
		} catch {
			// Браузер не пишет входящий звук — у реплики не будет «Повторить».
		}
	}

	recordStop(rec) {
		this.recorders = this.recorders.filter((r) => r !== rec)
		try {
			if (rec.recorder.state !== 'inactive') rec.recorder.stop()
		} catch {
			// Уже остановлена.
		}
	}

	recorded(rec) {
		if (this.disposed || !rec.chunks.length) return
		const blob = new Blob(rec.chunks, { type: rec.recorder.mimeType || rec.chunks[0]?.type || 'audio/webm' })
		if (blob.size < MIN_RECORDING_BYTES) return
		this.sounds.set(rec.id, this.d.createObjectURL(blob))
		// Старые записи выпускаем: за длинный разговор их было бы много.
		while (this.sounds.size > KEEP_RECORDINGS) {
			const [oldId, url] = this.sounds.entries().next().value
			if (this.replaying === oldId) this.stopReplay()
			this.sounds.delete(oldId)
			this.d.revokeObjectURL(url)
			this.ui({ type: 'live_avatar', id: oldId, sound: false })
		}
		this.ui({ type: 'live_avatar', id: rec.id, sound: true })
	}

	/**
	 * «Повторить» в живом режиме — запись входящего звука реплики. Сессия
	 * звука ученика не слышит, а нарезка фраз при открытой кнопке не даёт
	 * включить повтор.
	 */
	toggleReplay(id) {
		if (this.replaying === id) {
			this.stopReplay()
			return false
		}
		const url = this.sounds.get(id)
		if (!url || this.talking || this.micOpen) return false
		this.stopReplay()
		const el = (this.replayEl ??= this.d.createAudio())
		this.replaying = id
		const done = () => {
			if (this.replaying !== id) return
			this.replaying = null
			el.onended = el.onpause = el.onerror = null
			this.ui({ type: 'live_replay', id: null })
		}
		el.onended = done
		el.onpause = done
		el.onerror = done
		el.src = url
		this.ui({ type: 'live_replay', id })
		Promise.resolve()
			.then(() => el.play())
			.catch(done)
		return true
	}

	stopReplay() {
		if (this.replaying && this.replayEl) this.replayEl.pause()
	}

	/* --- конец разговора --- */

	/**
	 * Последний ответ записан: аватар прощается. Сессия больше не слушает
	 * ученика, указание движка (или наше) просит попрощаться; если аватар
	 * ещё договаривает — просим после его реплики, иначе два ответа сразу
	 * сессия посчитала бы ошибкой.
	 */
	farewell(steer = '', turn = null) {
		if (this.finishing) return
		this.finishing = true
		this.status = 'closing'
		// У прощания своё указание, отложенное больше не нужно.
		this.heldSteer = null
		this.ui({ type: 'live_status', status: 'closing' })
		this.closeMic()
		this.stopListening()
		for (const id of this.ledger.dropStudents()) {
			if (!this.students.has(id)) continue
			const item = this.students.get(id)
			this.d.clearTimeout(item.timer)
			this.studentDone(id, 'late')
		}
		const c = this.connection
		if (!c || c.stopped || !this.greeted) {
			this.finish()
			return
		}
		this.instruct(steer, { fallback: FAREWELL_GUIDANCE, turn })
		this.farewellTimer = this.d.setTimeout(() => this.finish(), FAREWELL_MS)
		// Указание после предпоследнего ответа просит сессию попрощаться сразу
		// после последнего, и сессия отвечает раньше, чем сюда приходит ответ
		// сервера. Реплика, начатая после последней фразы ученика, и есть
		// прощание — второе не просим: иначе аватар прощался бы дважды.
		// Прощанием считается только реплика, начатая по указанию с
		// предупреждением о конце (указание хода turn − 1 или новее).
		const answersLast = (reply) => Boolean(reply) && reply.order > (this.lastPhraseOrder || 0)
			&& Number.isInteger(turn) && Number.isInteger(reply.steerTurn) && reply.steerTurn >= turn - 1
		const open = this.openReply ? this.replies.get(this.openReply) : null
		// Сессии уже заказан ответ на последнюю фразу, а он ещё не начался:
		// второй response.create поверх него она считает ошибкой, поэтому
		// это ответ и есть прощание — как реплика, начатая сессией сама.
		if (!open && this.turnAsked) this.farewellIsOpenReply = true
		else if (open && answersLast(open)) this.farewellIsOpenReply = true
		else if (!open && answersLast(this.lastReplyDone) && !asksQuestion(this.lastReplyDone.content)) {
			this.farewellSaidAt = this.lastReplyDone.at
		} else if (open) this.farewellAfterReply = true
		else this.askFarewell()
	}

	nextOrder() {
		this.order = (this.order || 0) + 1
		return this.order
	}

	askFarewell() {
		this.farewellAsked = true
		this.farewellAskedAt = this.d.now()
		this.send(responseEvent('farewell'))
	}

	/** Сессия закончена после последнего ответа: экран ждёт итог. */
	finish() {
		if (this.finished || this.disposed) return
		this.finished = true
		this.finishing = true
		this.status = 'ended'
		this.farewellSaidAt = 0
		this.closeMic()
		this.d.clearTimeout(this.farewellTimer)
		this.d.clearTimeout(this.introTimer)
		this.d.clearInterval(this.meter)
		this.stopListening()
		for (const rec of [...this.recorders]) this.recordStop(rec)
		this.endConnection()
		this.settleOpenReply()
		this.updateTalking()
		this.ui({ type: 'live_finished' })
	}

	/**
	 * handOver — разговор продолжается вводом текстом: сбой сессии (message —
	 * почему) или выбор ученика. Сказанное не теряется: недосказанная фраза дорабатывается,
	 * неуверенные ждут проверки, очередь дописывается — потом onDrained.
	 */
	handOver(message = '') {
		if (this.status === 'handover' || this.finished || this.disposed) return
		this.status = 'handover'
		this.finishing = true
		this.handingOver = true
		this.closeMic()
		this.d.clearTimeout(this.farewellTimer)
		this.d.clearTimeout(this.introTimer)
		this.stopListening({ keep: true })
		this.endConnection()
		this.settleOpenReply()
		this.updateTalking()
		this.ui({ type: 'live_handover', message })
		this.checkDrained()
	}

	failed(message) {
		if (this.status === 'handover' || this.finished || this.disposed) return
		// Связь оборвалась на прощании: разговор уже закрыт, итог от прощания
		// не зависит — просто заканчиваем.
		if (this.status === 'closing') {
			this.finish()
			return
		}
		const reason = reasonOf(message)
		// Строка под «Голосовой разговор недоступен» (parts.jsx, LiveUnavailable).
		const what = this.greeted ? 'Разговор прервался' : 'Не получилось соединиться'
		this.handOver(`${what}${reason ? ` (${reason})` : ''}.`)
	}

	checkDrained() {
		if (!this.handingOver || this.drained || !this.ledger.idle) return
		this.drained = true
		this.onDrained()
	}

	/** 409 на ответ: разговор на сервере уже закрыт или не начат — экран перечитает его. */
	conflict(id, message) {
		this.studentDone(id, 'dropped')
		this.finishing = true
		this.status = 'ended'
		this.closeMic()
		this.stopListening()
		this.endConnection()
		this.onConflict(message)
	}

	/** «Отменить», пока идёт подключение. */
	cancel() {
		if (this.status !== 'connecting') return false
		this.status = 'idle'
		this.finishing = true
		this.endConnection()
		stopTracks(this.stream)
		this.ui({ type: 'live_status', status: 'idle' })
		return true
	}

	/**
	 * Уход со страницы. Обычный запрос браузер оборвёт вместе с ней, поэтому о
	 * конце сессии сообщает маяк — иначе место на ключе держалось бы до
	 * таймаута шлюза.
	 */
	pageHide() {
		const c = this.connection
		if (c?.sessionId && !c.endReported) {
			c.endReported = true
			this.api.endBeacon?.(c.sessionId)
		}
		this.handOver('')
	}

	/**
	 * Экран закрыт. Сессия — стоп, микрофон — отпустить. Фразы, которые ждали
	 * проверки, уходят как есть — так же, как через 20 секунд; очередь
	 * дописывается без экрана.
	 */
	dispose() {
		if (this.disposed) return
		this.unattended = true
		for (const item of [...this.students.values()]) {
			if (item.stage === 'review' || item.stage === 'editing') this.accept(item.id, item.asr?.text)
		}
		this.disposed = true
		this.finishing = true
		this.d.clearTimeout(this.micTimer)
		this.d.clearInterval(this.meter)
		this.d.clearTimeout(this.farewellTimer)
		this.d.clearTimeout(this.introTimer)
		this.stopListening({ keep: true })
		this.endConnection()
		this.settleOpenReply()
		stopTracks(this.stream)
		this.stopReplay()
		for (const rec of [...this.recorders]) this.recordStop(rec)
		for (const url of this.sounds.values()) this.d.revokeObjectURL(url)
		this.sounds.clear()
		try {
			this.remoteSource?.disconnect()
		} catch {
			// Уже отсоединён.
		}
		this.release()
	}

	release() {
		const context = this.context
		this.context = null
		try {
			context?.close?.()?.catch?.(() => {})
		} catch {
			// Контекст уже закрыт.
		}
	}
}
