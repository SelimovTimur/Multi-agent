// AvatarChat.jsx — разговор ученика с аватаром.
//
// Аватар говорит голосом, текста реплики на экране нет: его открывает
// «Подсказка» (Уч-1). Голосовой режим один.
//
// Голосовой разговор — по умолчанию, если его дают сервер (live_available) и
// браузер: одна сессия GPT Live, ученик говорит кнопкой микрофона, аватар
// отвечает своим голосом. Каждая фраза ученика параллельно распознаётся нашим
// распознаванием и уходит ответом в разговор; неуверенную ученик
// подтверждает или правит, не останавливая разговор (Уч-2). Связка — в
// conversation/liveTalk.js, правила — в conversation/live.js.
//
// Ввод текстом — запасной путь: у части учеников нет микрофона, часть
// стесняется говорить при классе, а школьная сеть или браузер бывают не
// готовы к GPT Live. Если голосовой разговор не соединился, ученик видит
// «Голосовой разговор недоступен», «Повторить» и ввод текстом. Голосовых
// сообщений с записью ответа нет: голос — только GPT Live (#28, #39).
//
// В обоих режимах ответ хранится до подтверждения и повторяется с тем же
// идентификатором — его не засчитают дважды и не потеряют при обрыве или
// перезагрузке (Уч-4). После последнего ответа — итог без списка ошибок
// (Уч-5). Логика без браузера — в conversation/state.js. Договор —
// docs/product/avatar-goals/contract.md, поведение экрана —
// docs/product/avatar-goals/student-ui.md.
//
// Сам экран — ConversationScreen — не знает, чей это разговор: вызовы сервера
// приходят параметром api. Ученику их даёт AvatarChat (разговор при попытке),
// учителю — пробный разговор из предпросмотра (lesson/PreviewConversation.jsx),
// чтобы учитель видел ровно тот экран, что ученик.

import { useCallback, useEffect, useMemo, useReducer, useRef, useState } from 'react'
import { useLocation, useNavigate, useParams, useSearchParams } from 'react-router-dom'
// Под своим именем: в ConversationScreen `live` — состояние живого режима, и
// под ним журнал соединения молча терял события (live.report был undefined).
import { live as liveApi, my, speech } from '../../api/v1'
import { AuthShell } from '../Shell'
import Icon from '../icons'
import { Button } from '../ui'
import AvatarStage from '../live/AvatarStage'
import { useAvatarState } from '../live/useAvatarState'
import {
	Closing,
	Draining,
	Intro,
	LivePanel,
	LiveStudentMessage,
	LiveUnavailable,
	LiveVoiceMessage,
	MicAccess,
	SendError,
	Sending,
	StudentMessage,
	Summary,
	TextAnswer,
	TurnsHint,
	VoiceMessage,
} from './conversation/parts'
import {
	answersLeft,
	canRetryLive,
	canSwitchMode,
	createGate,
	deliverAnswer,
	dismissTurnsHint,
	initialState,
	leftLabel,
	LIVE_FIXED_REASONS,
	LIVE_INSECURE_MESSAGE,
	livePendingNote,
	loadPending,
	makeAnswer,
	newClientMsgId,
	reducer,
	resolvePending,
	savePending,
	clearPending,
	SUMMARY_MAX_ATTEMPTS,
	summaryDelay,
	takeTurnsHintVisit,
	turnKey,
} from './conversation/state.js'
import { FormalContext, polite } from './conversation/address.js'
import { closingAnswer } from './conversation/live.js'
import { createAudioContext, liveBlocker, LiveTalk, liveSupported } from './conversation/liveTalk.js'
import { micPermission, useAudioClock } from './conversation/audio.js'
import './conversation/conversation.css'

// Звук реплик держим в памяти, пока ученик на экране: «Повторить» не платит
// за синтез второй раз. Старые выпадают — иначе за длинный разговор в памяти
// вкладки висело бы столько звуков, сколько было реплик.
const KEEP_SOUNDS = 8

// sessionStorage бывает недоступен (приватный режим, запрет cookie) и тогда
// бросает уже при обращении. Разговор от этого не ломается — теряется только
// восстановление неотправленного ответа после перезагрузки.
function sessionStore() {
	try {
		return window.sessionStorage
	} catch {
		return null
	}
}

// localStorage — для подсказки к счётчику ответов: заходы считаются между
// разговорами. Недоступен — подсказка просто не запомнится.
function localStore() {
	try {
		return window.localStorage
	} catch {
		return null
	}
}

/**
 * Ручки ученика (договор, разделы 6 и 10.2). Тот же набор отдаёт пробный
 * разговор учителя — с теми же ответами, но без попытки.
 */
function studentApi(attemptId) {
	return {
		conversation: () => my.conversation(attemptId),
		start: () => my.startConversation(attemptId),
		answer: (answer) => my.answerConversation(attemptId, answer),
		incident: (incident) => my.conversationIncident(attemptId, incident),
		summary: (opts) => my.conversationSummary(attemptId, opts),
		speak: (text) => speech.speak(text, attemptId),
		// Голосовой разговор: LiveTalk зовёт их сам. Распознавание — только
		// его: им он расшифровывает фразы ученика.
		live: {
			open: (sdp, opts) => my.openConversationLive(attemptId, sdp, opts),
			answer: (answer) => my.answerConversation(attemptId, answer),
			avatar: (reply) => my.conversationAvatar(attemptId, reply),
			progress: () => my.conversationProgress(attemptId),
			check: (text) => my.conversationCheck(attemptId, text),
			end: (sessionId) => my.endConversationLive(attemptId, sessionId),
			endBeacon: (sessionId) => my.endConversationLiveBeacon(attemptId, sessionId),
			transcribe: (file, opts) => speech.transcribeMine(file, opts),
		},
	}
}

// Сессия живого разговора занята: подключается, идёт или прощается.
const LIVE_RUNNING = new Set(['connecting', 'active', 'reconnecting', 'closing'])

/** Тот же адрес по https — для плашки «сайт открыт по http». */
function secureUrl() {
	try {
		const url = new URL(window.location.href)
		url.protocol = 'https:'
		return url.toString()
	} catch {
		return ''
	}
}

/** AudioContext больше не нужен: разговор не начался. Закрытый — не беда. */
function closeContext(context) {
	void context?.close?.()?.catch?.(() => {})
}

const LIVE_NO_AUDIO = 'Браузер не дал разбирать звук микрофона.'

export default function AvatarChat() {
	const { attemptId } = useParams()
	const navigate = useNavigate()
	// Разговор — одно из заданий урока. Назад — к нему же в том разделе,
	// откуда пришли («на уроке» или «домашняя работа»): там осталось нажать
	// «Дальше». Название урока возвращаем туда, откуда оно пришло: без него
	// шапка урока показала бы служебное название версии.
	const [params] = useSearchParams()
	const location = useLocation()
	const section = params.get('section')
	const title = location.state?.title
	const backToLesson = useCallback(
		() =>
			navigate(`/student/lesson/${attemptId}${section === 'class' || section === 'home' ? `?section=${section}` : ''}`, {
				state: { task: 'dialog', title },
			}),
		[navigate, attemptId, section, title],
	)
	const api = useMemo(() => studentApi(attemptId), [attemptId])
	return (
		<ConversationScreen
			key={attemptId}
			api={api}
			storageKey={attemptId}
			frame={(children) => <AuthShell>{children}</AuthShell>}
			onBack={backToLesson}
		/>
	)
}

/**
 * ConversationScreen — экран разговора по целям.
 *
 * api — вызовы сервера (см. studentApi), storageKey — под каким ключом
 * вкладка хранит неотправленный ответ. frame — оболочка раздела, onBack и
 * backLabel — возврат: только из итога и когда разговора нет, посреди
 * разговора кнопки выхода нет. formal — обращаться на «вы» (смотрит учитель),
 * notice — плашка над разговором, onRestart — «Начать заново» (пробный
 * разговор учителя; у ученика разговор один).
 */
export function ConversationScreen({
	api,
	storageKey,
	frame,
	onBack,
	backLabel = 'К заданиям',
	formal = false,
	notice: banner = null,
	onRestart = null,
}) {
	const backToLesson = onBack
	const [state, dispatch] = useReducer(reducer, undefined, initialState)
	// Последнее нарисованное состояние — для обработчиков, которые дочитывают
	// его после await: замыкание держало бы состояние на момент нажатия.
	const stateRef = useRef(state)
	stateRef.current = state
	const alive = useRef(true)
	const gate = useRef(null)
	if (!gate.current) gate.current = createGate()

	// Подсказка к «Осталось N ответов» — ученику на первых заходах (учитель в
	// пробном разговоре её не видит). Заход засчитывается один раз за экран,
	// когда счётчик впервые появился.
	const [turnsHint, setTurnsHint] = useState(false)
	const turnsHintTaken = useRef(false)
	const counterShown = state.phase === 'active' && state.maxTurns > 0
	useEffect(() => {
		if (formal || !counterShown || turnsHintTaken.current) return
		turnsHintTaken.current = true
		setTurnsHint(takeTurnsHintVisit(localStore()))
	}, [formal, counterShown])

	// Общий <audio>: через него звучат все реплики, и к нему привязан Ишка.
	const audio = useRef(null)
	const clock = useAudioClock(audio)
	const [loadedKey, setLoadedKey] = useState('')
	const loadedRef = useRef('')
	// Реплика, которую попросили последней: синтез прошлой мог закончиться
	// позже, и она не должна заиграть поверх.
	const wanted = useRef('')
	const sounds = useRef(new Map())
	const [durations, setDurations] = useState({})
	const reported = useRef(new Set())

	const textField = useRef(null)
	const chatBody = useRef(null)
	// «Повторить» голосового разговора спрашивает сервер.
	const [retrying, setRetrying] = useState(false)
	const end = useRef(null)

	// Живой разговор: один LiveTalk на сессию. Ссылкой — им управляют
	// обработчики, а экран рисует то, что он сообщает через dispatch.
	const liveRef = useRef(null)
	// Поле правки у каждой фразы своё: панели букв нужен её textarea.
	const liveFields = useRef(new Map())
	const getLevel = useCallback(() => liveRef.current?.level() || 0, [])

	useEffect(() => {
		alive.current = true
		// Уход со страницы: сессию — стоп, о конце — маяком (обычный запрос
		// браузер оборвёт вместе со страницей, а место на ключе осталось бы
		// занятым до таймаута шлюза).
		const leave = () => liveRef.current?.pageHide()
		window.addEventListener('pagehide', leave)
		return () => {
			alive.current = false
			window.removeEventListener('pagehide', leave)
			const talk = liveRef.current
			liveRef.current = null
			talk?.dispose()
		}
	}, [])

	/* --- загрузка и восстановление --- */

	// quiet — перечитывание ради «Повторить»: сбой сети не должен менять
	// идущий разговор на экран «Не удалось открыть разговор».
	const load = useCallback(async ({ quiet = false } = {}) => {
		try {
			const res = await api.conversation()
			if (!alive.current) return
			const storage = sessionStore()
			// Неотправленный ответ: сервер знает о сохранённом (pending_answer),
			// вкладка — о не дошедшем. Устаревший — снимаем, чтобы он не всплыл
			// в следующем ходе.
			const restored = resolvePending(loadPending(storage, storageKey), res)
			if (restored.pending) savePending(storage, storageKey, restored.pending)
			else clearPending(storage, storageKey)
			const blocked = liveBlocker()
			dispatch({ type: 'loaded', res, restored, liveSupported: !blocked, liveBlocked: blocked })
			return res
		} catch (err) {
			if (alive.current && !quiet) dispatch({ type: 'load_failed', message: err.message })
			return null
		}
	}, [api, storageKey])

	useEffect(() => {
		load()
	}, [load])

	// Уходя с экрана: звук — стоп, ссылки на звуки освобождаем. Микрофон
	// отпускает LiveTalk.
	useEffect(() => {
		const el = audio.current
		const cache = sounds.current
		return () => {
			el?.pause()
			for (const s of cache.values()) URL.revokeObjectURL(s.url)
			cache.clear()
		}
	}, [])

	/* --- звук реплик --- */

	function remember(key, sound) {
		const cache = sounds.current
		cache.set(key, sound)
		for (const [k, s] of cache) {
			if (cache.size <= KEEP_SOUNDS) break
			if (k === key || k === loadedRef.current) continue
			URL.revokeObjectURL(s.url)
			cache.delete(k)
		}
	}

	/** Сбой у ученика — в журнал разговора. Один раз на вид сбоя и ход. */
	function report(kind, turnNo, message) {
		const id = `${kind}:${Number.isInteger(turnNo) ? turnNo : '-'}`
		if (reported.current.has(id)) return
		reported.current.add(id)
		api.incident({
			kind,
			turnNo: Number.isInteger(turnNo) ? turnNo : undefined,
			message: String(message || '').slice(0, 300),
		}).catch(() => {
			// Не записался сбой — ученику это не важно: разговор от него не зависит.
		})
	}

	/**
	 * playTurn проигрывает реплику через общий <audio>.
	 *
	 * Звук создаётся при первом запросе и хранится, пока ученик на экране.
	 * Синтез не вышел — текст реплики открывается сам, сбой уходит в журнал
	 * (договор, раздел 7).
	 */
	async function playTurn(turn, { fromStart = false } = {}) {
		const el = audio.current
		if (!el || !turn?.content) return
		const key = turn.key
		wanted.current = key
		let sound = sounds.current.get(key)
		if (!sound) {
			dispatch({ type: 'audio_loading', key })
			try {
				const blob = await api.speak(turn.content)
				if (!alive.current) return
				sound = { url: URL.createObjectURL(blob) }
				remember(key, sound)
				dispatch({ type: 'audio_ready', key })
			} catch (err) {
				if (!alive.current) return
				dispatch({ type: 'audio_failed', key })
				report('tts', turn.turnNo, err?.message || 'Синтез недоступен')
				return
			}
			if (wanted.current !== key) return
		}
		// Общий <audio> занят голосом сессии — старая реплика не играет поверх.
		if (liveRef.current) return
		if (loadedRef.current !== key) {
			el.src = sound.url
			loadedRef.current = key
			setLoadedKey(key)
		} else if (fromStart || el.ended) {
			el.currentTime = 0
		}
		try {
			await el.play()
		} catch {
			// Браузер не дал включить звук без нажатия — ученик нажмёт ▶ сам.
		}
	}

	function togglePlay(turn) {
		const el = audio.current
		if (el && loadedRef.current === turn.key && !el.paused && !el.ended) {
			el.pause()
			return
		}
		void playTurn(turn)
	}

	function replyTurn(res, fallbackNo) {
		const turnNo = Number.isInteger(res.turn_no) ? res.turn_no : fallbackNo
		return { key: turnKey('avatar', turnNo), turnNo, content: res.reply }
	}

	/* --- начало разговора --- */

	function start() {
		return gate.current.run(async () => {
			dispatch({ type: 'start' })
			try {
				const res = await api.start()
				if (!alive.current) return
				dispatch({ type: 'started', res })
				if (res?.reply) void playTurn(replyTurn(res, 0))
			} catch (err) {
				if (alive.current) dispatch({ type: 'start_failed', message: err.message })
			}
		})
	}

	/* --- отправка ответа --- */

	/**
	 * deliver отправляет ответ: новый или тот же самый повтором.
	 *
	 * Замок пропускает одно действие за раз — второе нажатие, пришедшее до
	 * перерисовки, не отправит ответ второй раз с новым идентификатором.
	 */
	function deliver(pending) {
		return gate.current.run(async () => {
			const out = await deliverAnswer({
				send: (p) => api.answer(p),
				storage: sessionStore(),
				attemptId: storageKey,
				pending,
				dispatch: (action) => {
					if (alive.current) dispatch(action)
				},
			})
			if (!alive.current) return
			if (out.ok && out.res.reply) void playTurn(replyTurn(out.res, pending.turnNo))
			// Разговор на сервере уже не тот, что на экране: начат заново,
			// закрыт с другого устройства. Перечитываем.
			if (!out.ok && out.outcome.action === 'send_conflict') await load()
		})
	}

	function answer(text, input, asr = null) {
		const s = stateRef.current
		return deliver(makeAnswer({ text, input, asr, clientMsgId: newClientMsgId(), turnNo: s.turnsTaken + 1 }))
	}

	function sendTyped() {
		const s = stateRef.current
		if (s.composer !== 'text' || !s.typed.trim()) return
		void answer(s.typed, 'text')
	}

	function retrySend() {
		const p = stateRef.current.pending
		if (p) void deliver(p)
	}

	/* --- ввод текстом и окно микрофона --- */

	// «Не сейчас» и «Ответить текстом» в окне микрофона — ввод текстом.
	const toText = useCallback(() => dispatch({ type: 'text_mode' }), [])

	const closeMic = useCallback(() => dispatch({ type: 'mic_close' }), [])

	function allowMic() {
		dispatch({ type: 'mic_close' })
		beginLive(createAudioContext())
	}

	/* --- живой разговор --- */

	/**
	 * «Начать разговор» и «Продолжить разговор» в голосовом разговоре.
	 *
	 * AudioContext создаётся здесь же, синхронно: Safari включает звук только
	 * по жесту ученика. Сначала своё окно о микрофоне, потом системный запрос.
	 */
	function pressLive() {
		const s = stateRef.current
		if (!s.live.available || LIVE_RUNNING.has(s.live.status)) return
		connectLive(createAudioContext())
	}

	/**
	 * connectLive — подключить голосовой разговор с уже созданным
	 * AudioContext. Доступность проверяет вызывающий: после перечитывания
	 * разговора («Повторить») stateRef ещё не перерисован.
	 */
	function connectLive(context) {
		// Закончившийся разговор держится ради «Повторить» у его реплик. Если
		// сервер всё же считает разговор открытым, новый начинается с чистого.
		const old = liveRef.current
		if (old) {
			if (!old.finished) {
				closeContext(context)
				return
			}
			liveRef.current = null
			old.dispose()
		}
		if (!context) {
			dispatch({ type: 'live_unavailable', message: LIVE_NO_AUDIO })
			return
		}
		void (async () => {
			const permission = await micPermission()
			if (!alive.current || permission === 'denied' || permission === 'prompt') {
				closeContext(context)
				if (alive.current) dispatch({ type: permission === 'denied' ? 'mic_denied' : 'mic_ask' })
				return
			}
			beginLive(context)
		})()
	}

	function beginLive(context) {
		// Второе нажатие, пока первое ещё запускает разговор, ничего не делает.
		if (gate.current.busy) {
			closeContext(context)
			return
		}
		void gate.current.run(async () => {
			const s = stateRef.current
			if (liveRef.current || !context) {
				closeContext(context)
				return
			}
			// Ответ, который не дошёл или остался без ответа аватара, уходит
			// первым — тем же сообщением, как «Повторить» при вводе текстом.
			if (s.pending) {
				const out = await deliverAnswer({
					send: (p) => api.answer({ ...p, mode: 'live' }),
					storage: sessionStore(),
					attemptId: storageKey,
					pending: s.pending,
					dispatch: (action) => {
						if (alive.current) dispatch(action)
					},
				})
				// По ответу сервера, а не по stateRef: экран мог ещё не перерисоваться.
				if (!alive.current || !out.ok || closingAnswer(out.res, s.maxTurns)) {
					closeContext(context)
					if (alive.current && !out.ok && out.outcome.action === 'send_conflict') await load()
					return
				}
			}
			// Общий <audio> переходит живому голосу: озвучка прошлых реплик — стоп.
			audio.current?.pause()
			wanted.current = ''
			loadedRef.current = ''
			setLoadedKey('')
			const cur = stateRef.current
			const talk = new LiveTalk({
				attemptId: storageKey,
				language: cur.language,
				turnsTaken: cur.turnsTaken,
				maxTurns: cur.maxTurns,
				audio: audio.current,
				api: api.live,
				storage: sessionStore(),
				report,
				reportLive: liveApi.report,
				dispatch: (action) => {
					if (alive.current && liveRef.current === talk) dispatch(action)
				},
				// После перехода к вводу текстом очередь дописана — дальше
				// разговор идёт с того, что знает сервер.
				onDrained: () => {
					if (liveRef.current !== talk) return
					liveRef.current = null
					talk.dispose()
					if (!alive.current) return
					dispatch({ type: 'live_drained' })
					void load()
				},
				onConflict: () => {
					if (liveRef.current !== talk) return
					liveRef.current = null
					talk.dispose()
					if (alive.current) void load()
				},
				onMicError: (kind) => {
					if (liveRef.current === talk) liveRef.current = null
					talk.dispose()
					if (alive.current) dispatch({ type: `mic_${kind}` })
				},
			})
			liveRef.current = talk
			await talk.start({ context })
		})
	}

	function cancelLive() {
		const talk = liveRef.current
		if (!talk?.cancel()) return
		liveRef.current = null
		talk.dispose()
	}

	/** «Голосовой разговор» из ввода текстом: посреди разговора — сразу подключаемся. */
	function toLive() {
		const s = stateRef.current
		if (!canSwitchMode(s, 'live')) return
		dispatch({ type: 'mode', mode: 'live' })
		if (s.phase === 'active') pressLive()
	}

	/**
	 * «Повторить» у «Голосовой разговор недоступен» — соединиться снова.
	 *
	 * Сервер голосовой разговор не давал — сначала спрашиваем его заново: на
	 * школьном Wi‑Fi линия могла освободиться, а фильтр — пропустить. Браузер
	 * его не умеет — повторять нечего, кнопки нет. Посреди разговора и во
	 * вступлении повтор сразу подключает: ученик нажал, чтобы говорить.
	 * Пока сервер отвечает, кнопка — «Соединяем…»: иначе неудачный повтор
	 * выглядел бы как нажатие, которое ничего не сделало.
	 */
	async function retryLive() {
		const s = stateRef.current
		if (gate.current.busy || retrying || !canRetryLive(s) || !liveSupported()) return
		// Контекст — до await: после него жеста ученика для Safari уже нет.
		const context = createAudioContext()
		if (!s.live.available) {
			setRetrying(true)
			const res = await load({ quiet: true })
			if (!alive.current) return
			setRetrying(false)
			// Разговор закрыт или выключен — перечитывание уже показало это.
			const ready = res && res.enabled !== false && res.status !== 'closed' && Boolean(res.live_available)
			if (!ready) {
				closeContext(context)
				return
			}
		}
		dispatch({ type: 'mode', mode: 'live' })
		connectLive(context)
	}

	function liveField(key) {
		let ref = liveFields.current.get(key)
		if (!ref) {
			ref = { current: null }
			liveFields.current.set(key, ref)
		}
		return ref
	}

	/* --- итог --- */

	const summaryStatus = state.summary.status
	useEffect(() => {
		if (state.phase !== 'closed' || summaryStatus !== 'waiting') return undefined
		let stopped = false
		let timer = 0
		let attempt = 0
		const ctrl = new AbortController()
		async function tick() {
			let status = ''
			try {
				const res = await api.summary({ signal: ctrl.signal })
				if (stopped) return
				status = res?.status || ''
				dispatch({ type: 'summary', res })
			} catch {
				// Сбой опроса — не повод сдаваться: следующий вопрос через паузу.
				if (stopped) return
			}
			if (status === 'ready') return
			// Сервер считает разговор незакрытым — перечитываем, опрос идёт дальше.
			if (status === 'not_closed') void load()
			attempt += 1
			if (attempt >= SUMMARY_MAX_ATTEMPTS) {
				dispatch({ type: 'summary_slow' })
				return
			}
			timer = setTimeout(tick, summaryDelay(attempt, status))
		}
		void tick()
		return () => {
			stopped = true
			clearTimeout(timer)
			ctrl.abort()
		}
	}, [state.phase, summaryStatus, api, load])

	/* --- мелочи экрана --- */

	// Длина звука реплики остаётся у её проигрывателя и после того, как общий
	// элемент переключился на другую.
	useEffect(() => {
		if (!loadedKey || !(clock.duration > 0)) return
		setDurations((d) => (d[loadedKey] === clock.duration ? d : { ...d, [loadedKey]: clock.duration }))
	}, [loadedKey, clock.duration])

	// Новая реплика — лента к ней: иначе ответ аватара оказывается за краем,
	// и его не замечают. У ленты на компьютере своя прокрутка. Фраза живого
	// разговора видна не сразу (пока ученик говорит, её пузыря нет), а
	// просьба её проверить должна попасть на глаза.
	const shownTurns = state.turns.filter((t) => t.live?.stage !== 'hearing').length
	const liveReviews = state.turns.filter((t) => t.live?.stage === 'review').length
	useEffect(() => {
		const body = chatBody.current
		if (body && body.scrollHeight > body.clientHeight) body.scrollTo({ top: body.scrollHeight, behavior: 'smooth' })
	}, [shownTurns, liveReviews])

	// Страница — до конца колонки: видны последняя реплика и панель ответа.
	// На телефоне сцена аватара стоит над перепиской, и без этого до поля
	// ответа или открытого сбоем текста пришлось бы листать.
	const failedVoices = Object.values(state.voices).filter((v) => v === 'failed').length
	useEffect(() => {
		end.current?.scrollIntoView({ behavior: 'smooth', block: 'nearest' })
	}, [shownTurns, liveReviews, state.phase, state.composer, state.mode, state.live.status, summaryStatus, failedVoices])

	const live = state.live
	const liveOn = state.mode === 'live' && LIVE_RUNNING.has(live.status)
	const avatarState = useAvatarState({
		audioRef: audio,
		recording: liveOn && live.hearing,
		busy: state.composer === 'sending' || state.starting || Object.values(state.voices).includes('loading'),
		voice: liveOn ? (live.status === 'connecting' ? 'connecting' : 'active') : 'idle',
	})

	const showChat = state.phase === 'active' || state.phase === 'closed'
	const liveReview = state.turns.some((t) => t.live && (t.live.stage === 'review' || t.live.stage === 'editing'))
	const textMode = state.mode === 'text'
	const offReason = textMode ? live.unavailable : ''
	// Причина есть — «Повторить» на плашке; ученик выбрал текст сам — кнопка
	// «Голосовой разговор», если он доступен.
	const canToLive = textMode && !offReason && canSwitchMode(state, 'live')
	const unavailable = offReason ? (
		<LiveUnavailable
			reason={polite(offReason, formal)}
			busy={retrying || !canRetryLive(state)}
			retrying={retrying}
			// Адрес и браузер повтором не поменять — кнопки нет. По http —
			// ссылка на тот же урок по https.
			onRetry={LIVE_FIXED_REASONS.has(offReason) ? null : () => void retryLive()}
			secureUrl={offReason === LIVE_INSECURE_MESSAGE ? secureUrl() : ''}
		/>
	) : null

	function composer() {
		switch (state.composer) {
			case 'sending':
				return <Sending />
			case 'send_error':
				return <SendError error={state.sendError} onRetry={retrySend} />
			default:
				return (
					<TextAnswer
						value={state.typed}
						language={state.language}
						fieldRef={textField}
						onChange={(text) => dispatch({ type: 'typed', text })}
						onSend={sendTyped}
					/>
				)
		}
	}

	return frame(
		<FormalContext.Provider value={formal}>
			<div className='avatar-dialog conv-page'>
				<div className='avatar-dialog-stage'>
					{/* Пока фон один на все уроки. Подбор под тему — отдельный тикет:
					    он требует библиотеки сцен, просмотренной человеком. */}
					<AvatarStage state={avatarState} audioRef={audio} />
				</div>
				<div className='chat'>
					{/* Выйти посреди разговора нельзя: «К заданиям» есть только у
					    итога и там, где разговора нет (не открылся, убран из урока).
					    Учителю возврат даёт оболочка пробного разговора. */}
					<div className='row'>
						<Icon name='face' size={22} />
						<b>Разговор с аватаром</b>
					</div>

					{banner}

					{counterShown && (
						<div className='tiny mt8' aria-live='polite'>
							{leftLabel(answersLeft(state.maxTurns, state.turnsTaken))}
						</div>
					)}
					{counterShown && turnsHint && (
						<TurnsHint
							onClose={() => {
								dismissTurnsHint(localStore())
								setTurnsHint(false)
							}}
						/>
					)}

					{state.phase === 'loading' && <div className='empty'>Загружаем разговор…</div>}

					{state.phase === 'failed' && (
						<div className='mt16'>
							<div className='notice notice-err' role='alert'>
								{state.error}
							</div>
							<div className='row mt16'>
								<Button variant='primary' icon='refresh' onClick={() => void load()}>
									Повторить
								</Button>
								<Button onClick={backToLesson}>{backLabel}</Button>
							</div>
						</div>
					)}

					{state.phase === 'disabled' && (
						<div className='mt16'>
							<div className='empty'>В этом уроке разговора нет — учитель убрал его из урока.</div>
							<div className='row mt16'>
								<Button onClick={backToLesson}>{backLabel}</Button>
							</div>
						</div>
					)}

					{state.phase === 'intro' && (
						<Intro
							maxTurns={state.maxTurns}
							language={state.language}
							starting={state.starting}
							error={state.error}
							live={state.mode === 'live'}
							unavailable={unavailable}
							onStart={state.mode === 'live' ? pressLive : () => void start()}
							onSwitch={canToLive ? toLive : null}
						/>
					)}

					{showChat && (
						<div className='chat-body mt16' ref={chatBody}>
							{state.turns.map((t) =>
								t.live && t.role === 'student' ? (
									<LiveStudentMessage
										key={t.key}
										turn={t}
										language={state.language}
										fieldRef={liveField(t.key)}
										onConfirm={() => liveRef.current?.confirm(t.live.id)}
										onEdit={() => liveRef.current?.edit(t.live.id)}
										onDraft={(text) => dispatch({ type: 'live_draft', id: t.live.id, text })}
										onSendEdited={() => {
											const now = stateRef.current.turns.find((x) => x.key === t.key)
											liveRef.current?.sendEdited(t.live.id, now?.live?.draft)
										}}
										onCancelEdit={() => liveRef.current?.cancelEdit(t.live.id)}
										onDiscard={() => liveRef.current?.discard(t.live.id)}
										onRetry={() => liveRef.current?.retry()}
									/>
								) : t.live ? (
									<LiveVoiceMessage
										key={t.key}
										turn={t}
										hint={Boolean(state.hints[t.key])}
										language={state.language}
										replaying={live.replaying === t.live.id}
										disabled={live.talking || live.mic}
										onReplay={() => liveRef.current?.toggleReplay(t.live.id)}
										onHint={() => dispatch({ type: 'hint', key: t.key })}
									/>
								) : t.role === 'avatar' ? (
									<VoiceMessage
										key={t.key}
										turn={t}
										hint={Boolean(state.hints[t.key])}
										status={state.voices[t.key] || ''}
										playing={loadedKey === t.key && clock.playing}
										time={loadedKey === t.key ? clock.time : 0}
										duration={(loadedKey === t.key && clock.duration) || durations[t.key] || 0}
										language={state.language}
										// Общий <audio> занят голосом сессии.
										disabled={liveOn}
										onToggle={() => togglePlay(t)}
										onRepeat={() => void playTurn(t, { fromStart: true })}
										onHint={() => dispatch({ type: 'hint', key: t.key })}
									/>
								) : (
									<StudentMessage key={t.key} turn={t} />
								),
							)}
							{state.composer === 'sending' && state.mode !== 'live' && (
								<div className='bubble bubble-avatar bubble-typing' aria-hidden='true'>
									…
								</div>
							)}
						</div>
					)}

					{state.phase === 'active' && (
						<div className='conv-composer'>
							{state.mode === 'live' ? (
								<LivePanel
									status={live.status}
									talking={live.talking}
									hearing={live.hearing}
									mic={live.mic}
									resume={livePendingNote(state)}
									resuming={state.composer === 'sending'}
									error={state.sendError && !['resume', 'unsent'].includes(state.sendError.kind) ? state.sendError.message : ''}
									getLevel={getLevel}
									onStart={pressLive}
									onCancel={cancelLive}
									onMic={() => liveRef.current?.setMic(!live.mic)}
								/>
							) : (
								<>
									{unavailable}
									{live.draining ? <Draining review={liveReview} /> : composer()}
								</>
							)}
							{state.notice && (
								<p className='conv-notice' role='status'>
									{polite(state.notice, formal)}
								</p>
							)}
							{canToLive && (
								<Button variant='ghost' icon='mic' onClick={toLive}>
									Голосовой разговор
								</Button>
							)}
						</div>
					)}

					{state.phase === 'closed' && (
						<>
							{state.notice && (
								<p className='conv-notice mt16' role='status'>
									{polite(state.notice, formal)}
								</p>
							)}
							{state.summary.status === 'ready' ? (
								<Summary
									data={state.summary.data}
									language={state.language}
									onBack={backToLesson}
									backLabel={backLabel}
									extra={
										onRestart && (
											<Button icon='refresh' onClick={onRestart}>
												Начать заново
											</Button>
										)
									}
								/>
							) : (
								<Closing
									slow={state.summary.status === 'slow'}
									failed={state.summary.failed}
									onRetry={() => dispatch({ type: 'summary_retry' })}
									onBack={backToLesson}
									backLabel={backLabel}
								/>
							)}
						</>
					)}

					{/* Пробный разговор учителя можно начать заново в любой момент:
					    учитель пробует разные ответы. У ученика разговор один. */}
					{onRestart && state.phase === 'active' && (
						<div className='row mt16'>
							<Button variant='ghost' icon='refresh' onClick={onRestart}>
								Начать заново
							</Button>
						</div>
					)}

					{/* Общий звук разговора: и реплики, и реакция Ишки идут через него. */}
					<audio ref={audio} preload='auto' />
					<div ref={end} />
				</div>
			</div>
			{state.micPrompt && (
				<MicAccess
					kind={state.micPrompt}
					language={state.language}
					onAllow={allowMic}
					onLater={toText}
					onClose={closeMic}
				/>
			)}
		</FormalContext.Provider>,
	)
}
