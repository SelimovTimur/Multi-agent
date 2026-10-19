// state.js — логика разговора урока по ходам: без React, сети и браузера.
//
// Экран AvatarChat рисует состояние и выполняет действия, а всё, что можно
// проверить без браузера, лежит здесь: редьюсер, решение «показать ученику
// распознанный текст», хранение неотправленного ответа, разбор отказов
// сервера и шаг опроса итога. Тест — frontend/scripts/test-conversation.mjs.
//
// Договор — docs/product/avatar-goals/contract.md (разделы 5–7), поведение
// экрана — docs/product/avatar-goals/student-ui.md.

import { randomId } from '../../uuid.js'

/** Предел ответа по договору: text — 1…1000 символов. */
export const MAX_ANSWER_CHARS = 1000

export const UNAVAILABLE_MESSAGE = 'Аватар не смог ответить. Твой ответ сохранён — нажми «Повторить»'
export const NETWORK_MESSAGE = 'Ответ не дошёл до сервера. Он не потерян — нажми «Повторить»'
export const RESUME_MESSAGE = 'Твой ответ сохранён, но аватар ещё не ответил. Нажми «Повторить»'
export const UNSENT_MESSAGE = 'Ответ не успел отправиться. Нажми «Повторить» — он уйдёт тем же сообщением'
export const FAREWELL_MESSAGE = 'Аватар не успел попрощаться — на итог это не влияет'

// Почему голосового разговора нет: строка под «Голосовой разговор
// недоступен». Без обращения — она одинакова для ученика и учителя.
export const LIVE_OFF_MESSAGE = 'Сейчас его не удаётся подключить.'
export const LIVE_BROWSER_MESSAGE = 'Этот браузер не поддерживает голосовой разговор.'
// Сайт открыт по http: микрофона не даст ни один браузер. Дело в адресе, и
// ученик не должен думать, что ему нужен другой браузер.
export const LIVE_INSECURE_MESSAGE = 'Сайт открыт по незащищённому адресу (http), а голосовой разговор работает только по https.'
// Причины, которые повтором не исправить: ни адрес, ни браузер сами не
// поменяются — «Повторить» у них нет.
export const LIVE_FIXED_REASONS = new Set([LIVE_BROWSER_MESSAGE, LIVE_INSECURE_MESSAGE])
const MIC_REASONS = {
	denied: 'Браузер не дал доступ к микрофону.',
	missing: 'Микрофон не найден.',
	failed: 'Не удалось включить микрофон.',
}

/* ---------------------------------------------------------------------------
 * Мелочи: числа, слова, время
 * ------------------------------------------------------------------------- */

function toInt(value) {
	const n = Number(value)
	return Number.isFinite(n) && n > 0 ? Math.floor(n) : 0
}

/** Форма слова при числе: 1 ответ, 3 ответа, 5 ответов. */
export function pluralWord(n, one, few, many) {
	const abs = Math.abs(n) % 100
	const last = abs % 10
	if (abs > 10 && abs < 15) return many
	if (last === 1) return one
	if (last >= 2 && last <= 4) return few
	return many
}

/** Сколько ответов осталось. Считаются только отправленные — их число ведёт сервер. */
export function answersLeft(maxTurns, turnsTaken) {
	if (!maxTurns) return 0
	return Math.max(maxTurns - turnsTaken, 0)
}

/** «Остался 1 ответ», «Осталось 3 ответа», «Осталось 5 ответов». */
export function leftLabel(n) {
	const verb = n % 10 === 1 && n % 100 !== 11 ? 'Остался' : 'Осталось'
	return `${verb} ${n} ${pluralWord(n, 'ответ', 'ответа', 'ответов')}`
}

/* -------------------------------------------------------------------------
 * Подсказка к счётчику ответов
 * ------------------------------------------------------------------------- */

// Первые TURNS_HINT_VISITS разговоров под «Осталось N ответов» стоит
// подсказка со стрелочкой: что это за число и что будет, когда оно дойдёт до
// нуля. Заходы считаются в localStorage — на этом устройстве, по всем
// разговорам сразу.
export const TURNS_HINT_VISITS = 3
const TURNS_HINT_KEY = 'nr-turns-hint-visits'

function readVisits(storage) {
	try {
		const n = Number.parseInt(storage?.getItem(TURNS_HINT_KEY) || '0', 10)
		return Number.isInteger(n) && n > 0 ? n : 0
	} catch {
		return 0
	}
}

/**
 * Засчитать заход и сказать, показывать ли подсказку на нём. Вызывается один
 * раз за разговор. Без хранилища подсказка показывается каждый раз — это
 * лучше, чем не показать её ни разу.
 */
export function takeTurnsHintVisit(storage) {
	const seen = readVisits(storage)
	if (seen >= TURNS_HINT_VISITS) return false
	try {
		storage?.setItem(TURNS_HINT_KEY, String(seen + 1))
	} catch {
		// См. выше.
	}
	return true
}

/** «Понятно» — больше не показывать. */
export function dismissTurnsHint(storage) {
	try {
		storage?.setItem(TURNS_HINT_KEY, String(TURNS_HINT_VISITS))
	} catch {
		// Не записалось — подсказка покажется в следующий раз.
	}
}

/** 75 → «1:15». Для проигрывателя реплики. */
export function formatClock(seconds) {
	const total = Math.max(0, Math.floor(Number(seconds) || 0))
	return `${Math.floor(total / 60)}:${String(total % 60).padStart(2, '0')}`
}

// Язык урока в предложном падеже: «разговаривать с аватаром на английском».
const PREPOSITIONAL = {
	english: 'английском',
	tatar: 'татарском',
}

export function onLanguage(language) {
	const word = PREPOSITIONAL[String(language || '').toLowerCase()]
	return word ? `на ${word}` : 'на языке урока'
}

// Код языка для атрибута lang у текста реплики: экранный чтец читает
// английскую фразу английским голосом, а не русским.
const LANG_TAGS = { english: 'en', tatar: 'tt' }

export function langTag(language) {
	return LANG_TAGS[String(language || '').toLowerCase()] || undefined
}

/**
 * Имя файла записи по типу звука: распознавание не гадает по содержимому.
 * Фразы голосового разговора уходят WAV (utterances.js).
 */
export function fileNameFor(type = '') {
	const t = String(type).toLowerCase()
	if (t.includes('mp4') || t.includes('aac') || t.includes('m4a')) return 'voice.m4a'
	if (t.includes('ogg')) return 'voice.ogg'
	if (t.includes('wav')) return 'voice.wav'
	return 'voice.webm'
}

/** Идентификатор ответа для повторов; работает и по http (см. uuid.js). */
export function newClientMsgId(c = globalThis.crypto) {
	return randomId(c)
}

/* ---------------------------------------------------------------------------
 * Реплики
 * ------------------------------------------------------------------------- */

/**
 * Ключ реплики: роль и номер хода.
 *
 * Номер у ответа ученика и у ответа аватара на него один и тот же (договор,
 * раздел 1), поэтому роль входит в ключ. У реплик старого режима номера нет —
 * тогда ключ по месту в списке.
 */
export function turnKey(role, turnNo, index = 0) {
	return `${role}:${Number.isInteger(turnNo) ? turnNo : `i${index}`}`
}

/**
 * Реплики сервера в вид экрана. Чужие роли отбрасываются.
 *
 * В живом режиме на один номер бывает несколько реплик аватара (договор,
 * 10.2) — вторая и следующие получают ключ с порядковым номером: ключ
 * держит открытую подсказку и звук реплики и не должен повторяться.
 */
export function readTurns(list) {
	const seen = new Map()
	return (Array.isArray(list) ? list : [])
		.filter((t) => t && (t.role === 'avatar' || t.role === 'student'))
		.map((t, i) => {
			const turnNo = Number.isInteger(t.turn_no) ? t.turn_no : null
			const base = turnKey(t.role, turnNo, i)
			const n = (seen.get(base) || 0) + 1
			seen.set(base, n)
			return {
				key: n > 1 ? `${base}#${n}` : base,
				turnNo,
				role: t.role,
				content: String(t.content || ''),
				translation: String(t.translation || ''),
				input: t.input === 'voice' ? 'voice' : 'text',
				edited: Boolean(t.edited),
				// local — ответ, который сервер ещё не подтвердил:
				// 'sending', 'saved' (сохранён, аватар не ответил), 'unsent'.
				local: '',
			}
		})
}

function avatarTurn(turnNo, res) {
	return {
		key: turnKey('avatar', turnNo),
		turnNo,
		role: 'avatar',
		content: String(res.reply || ''),
		translation: String(res.translation || ''),
		input: 'text',
		edited: false,
		local: '',
	}
}

function studentTurn(pending, local) {
	return {
		key: turnKey('student', pending.turnNo),
		turnNo: pending.turnNo,
		role: 'student',
		content: pending.text,
		translation: '',
		input: pending.input,
		edited: Boolean(pending.edited),
		local,
	}
}

/** Пузырь ответа ученика: обновить метку у уже показанного или добавить в конец. */
function withStudentTurn(turns, pending, local) {
	const key = turnKey('student', pending.turnNo)
	if (turns.some((t) => t.key === key)) return turns.map((t) => (t.key === key ? { ...t, local } : t))
	return [...turns, studentTurn(pending, local)]
}

/* ---------------------------------------------------------------------------
 * Распознавание и решение «показать проверку»
 * ------------------------------------------------------------------------- */

/**
 * shouldReview — показать ли ученику распознанный текст до отправки.
 *
 * Решает сервер: он знает уверенность модели, громкость записи и чужие буквы
 * в тексте (договор, раздел 5). Поля нет — проверку показываем: лишний раз
 * спросить дешевле, чем засчитать ученику ошибку, которую сделало
 * распознавание (Уч-2).
 */
export function shouldReview(res) {
	return res?.needs_review !== false
}

/** Ответ распознавания в вид экрана. confidence: число или null — «не знаем». */
export function readAsr(res) {
	const confidence = Number(res?.confidence)
	return {
		text: String(res?.text || '').trim(),
		confidence: res?.confidence != null && Number.isFinite(confidence) ? confidence : null,
		review: shouldReview(res),
		reasons: Array.isArray(res?.review_reasons) ? res.review_reasons.map(String) : [],
	}
}

/**
 * Что делать с распознанным: 'empty' — ничего не расслышали, 'review' —
 * показать панель «Распознано», 'send' — уверенно, ответ уходит сразу.
 */
export function asrNextStep(asr) {
	if (!asr?.text) return 'empty'
	return asr.review ? 'review' : 'send'
}

/** Одна строка к панели «Распознано»: почему мы просим проверить. */
export function reviewHint(reasons = []) {
	if (reasons.includes('quiet_audio')) return 'Запись получилась тихой — проверь, всё ли мы расслышали верно.'
	if (reasons.includes('foreign_letters')) return 'В тексте были буквы другого алфавита — проверь написание.'
	if (reasons.includes('low_confidence')) return 'Мы не уверены, что расслышали всё верно — проверь текст.'
	return 'Проверь, правильно ли мы тебя услышали. Ответ засчитается, только когда ты его отправишь.'
}

/* ---------------------------------------------------------------------------
 * Ответ и его доставка
 * ------------------------------------------------------------------------- */

/**
 * makeAnswer — ответ ученика в том виде, в каком он хранится до успеха.
 *
 * Поля «по факту»: input — как ответ был дан; asrText и asrConfidence — что
 * услышало распознавание (только у голоса); edited — ученик поменял
 * распознанный текст перед отправкой. Открытая и не изменённая правка
 * исправлением не считается.
 */
export function makeAnswer({ text, input, asr = null, clientMsgId, turnNo }) {
	const value = String(text || '').trim()
	const voice = input === 'voice'
	const asrText = voice ? String(asr?.text || '').trim() : null
	return {
		clientMsgId,
		turnNo,
		text: value,
		input: voice ? 'voice' : 'text',
		asrText,
		asrConfidence: voice && typeof asr?.confidence === 'number' ? asr.confidence : null,
		edited: voice && value !== asrText,
	}
}

/**
 * classifySendError — что делать с отказом на отправку ответа.
 *
 * drop — ответ больше не хранить: сервер его не примет и повтором это не
 * исправить. Всё остальное (обрыв, таймаут, 5xx, «аватар недоступен»)
 * хранится и уходит повтором с тем же идентификатором — сервер засчитает его
 * один раз.
 */
export function classifySendError(err) {
	const status = Number(err?.status) || 0
	if (status === 503 && err?.code === 'avatar_unavailable') {
		return { action: 'send_failed', kind: 'unavailable', message: err.message || UNAVAILABLE_MESSAGE, drop: false }
	}
	// 409: «разговор ещё не начат» или «закончен» — экран перечитывает разговор.
	if (status === 409) return { action: 'send_conflict', message: err.message || '', drop: true }
	if ([400, 403, 404, 413, 422].includes(status)) {
		return { action: 'send_rejected', message: err.message || 'Ответ не принят', drop: true }
	}
	return {
		action: 'send_failed',
		kind: 'network',
		message: status ? err?.message || NETWORK_MESSAGE : NETWORK_MESSAGE,
		drop: false,
	}
}

/**
 * deliverAnswer отправляет ответ и раскладывает исход по действиям редьюсера.
 *
 * Ответ кладётся в хранилище до запроса и снимается только после успеха или
 * окончательного отказа: обрыв связи, закрытая вкладка или перезагрузка не
 * теряют сказанное. Зависимости — параметрами: так поведение проверяется без
 * браузера и без сервера.
 */
export async function deliverAnswer({ send, storage, attemptId, pending, dispatch }) {
	savePending(storage, attemptId, pending)
	dispatch({ type: 'send', pending })
	try {
		const res = await send(pending)
		clearPending(storage, attemptId)
		dispatch({ type: 'answered', res: res || {}, pending })
		return { ok: true, res: res || {} }
	} catch (err) {
		const outcome = classifySendError(err)
		if (outcome.drop) clearPending(storage, attemptId)
		dispatch({ type: outcome.action, kind: outcome.kind, message: outcome.message, pending })
		return { ok: false, outcome }
	}
}

/**
 * createGate — замок от двойного нажатия.
 *
 * Пока первое действие не закончилось, второе не выполняется вовсе. Кнопка
 * гаснет только после перерисовки, а второй клик успевает раньше — и без
 * замка ушёл бы второй ответ с новым идентификатором, который сервер честно
 * засчитал бы ещё раз.
 */
export function createGate() {
	let busy = false
	return {
		get busy() {
			return busy
		},
		async run(fn) {
			if (busy) return undefined
			busy = true
			try {
				return await fn()
			} finally {
				busy = false
			}
		},
	}
}

/* ---------------------------------------------------------------------------
 * Хранение неотправленного ответа
 * ------------------------------------------------------------------------- */

// Ключ — по попытке: у ученика может быть открыто несколько уроков, и ответ
// одного разговора не должен всплыть в другом. sessionStorage, а не
// localStorage: ответ нужен этой вкладке после перезагрузки, а сохранённый на
// сервере ответ с другого устройства вернёт pending_answer.
const PENDING_PREFIX = 'nr-avatar-answer:'

export function pendingKey(attemptId) {
	return `${PENDING_PREFIX}${attemptId}`
}

function validPending(p) {
	return Boolean(
		p &&
			typeof p === 'object' &&
			typeof p.clientMsgId === 'string' &&
			p.clientMsgId &&
			Number.isInteger(p.turnNo) &&
			p.turnNo > 0 &&
			typeof p.text === 'string' &&
			p.text.trim() &&
			(p.input === 'voice' || p.input === 'text'),
	)
}

// Хранилище может отсутствовать или бросать (приватный режим, запрет
// cookie): разговор от этого не ломается, теряется только восстановление.
export function savePending(storage, attemptId, pending) {
	try {
		storage?.setItem(pendingKey(attemptId), JSON.stringify(pending))
	} catch {
		// См. выше: без хранилища ответ живёт до перезагрузки.
	}
}

export function loadPending(storage, attemptId) {
	try {
		const raw = storage?.getItem(pendingKey(attemptId))
		if (!raw) return null
		const p = JSON.parse(raw)
		return validPending(p) ? p : null
	} catch {
		return null
	}
}

export function clearPending(storage, attemptId) {
	try {
		storage?.removeItem(pendingKey(attemptId))
	} catch {
		// Нечего чистить — нечего и терять.
	}
}

/**
 * resolvePending — с какого места продолжить после перезагрузки.
 *
 * - Сервер прислал pending_answer: ответ сохранён, аватар не ответил — его и
 *   повторяем с тем же client_msg_id (saved). Если та же запись лежит в
 *   хранилище, берём её целиком: там input и распознанный текст.
 * - Сервер о нём не знает, а в хранилище ответ на следующий ход — запрос не
 *   дошёл, ответ уходит повтором с тем же идентификатором (не saved).
 * - Остальное устарело: ход уже засчитан или разговор не идёт.
 */
export function resolvePending(stored, res = {}) {
	const none = { pending: null, saved: false }
	if (res.status !== 'active') return none
	const taken = toInt(res.turns_taken)
	const sp = res.pending_answer
	if (sp && typeof sp.client_msg_id === 'string' && sp.client_msg_id) {
		const turnNo = Number.isInteger(sp.turn_no) && sp.turn_no > 0 ? sp.turn_no : Math.max(taken, 1)
		if (stored && stored.clientMsgId === sp.client_msg_id) return { pending: { ...stored, turnNo }, saved: true }
		const said = readTurns(res.turns).find((t) => t.role === 'student' && t.turnNo === turnNo)
		return {
			pending: {
				clientMsgId: sp.client_msg_id,
				turnNo,
				text: String(sp.text || said?.content || '').trim(),
				input: said?.input || 'text',
				asrText: null,
				asrConfidence: null,
				edited: Boolean(said?.edited),
			},
			saved: true,
		}
	}
	if (validPending(stored) && stored.turnNo === taken + 1) return { pending: stored, saved: false }
	return none
}

/* ---------------------------------------------------------------------------
 * Итог
 * ------------------------------------------------------------------------- */

/** Сколько раз спросить итог, прежде чем сказать «готовится дольше обычного». */
export const SUMMARY_MAX_ATTEMPTS = 40
const SUMMARY_DELAYS = [1500, 2000, 3000, 4000]

/**
 * Пауза перед следующим вопросом об итоге.
 *
 * Сначала часто — разбор обычно готов за десяток секунд, — потом раз в пять
 * секунд. После failed — реже: сервер перезапускает разбор не чаще раза в
 * минуту, и частый опрос ничего не ускорит. Сорок попыток — около трёх минут:
 * дольше этого зависший разбор сервер и сам перезапустит.
 */
export function summaryDelay(attempt, status = 'pending') {
	if (status === 'failed') return 15000
	return SUMMARY_DELAYS[attempt - 1] || 5000
}

function titles(list) {
	return (Array.isArray(list) ? list : []).map((x) => String(x?.title || '').trim()).filter(Boolean)
}

/** Итог сервера в вид экрана. Ни ошибок, ни их числа здесь нет и быть не должно. */
export function readSummary(res) {
	const s = res?.summary || {}
	const credited = titles(s.credited_phrases)
	const applied = titles(s.applied_themes)
	const next = s.next_time || null
	const nextText = String(next?.text_ru || '').trim()
	const nextExample = String(next?.example || '').trim()
	return {
		credited,
		phrasesTotal: Math.max(toInt(s.phrases_total), credited.length),
		applied,
		themesTotal: Math.max(toInt(s.themes_total), applied.length),
		nextTime: nextText || nextExample ? { text: nextText, example: nextExample } : null,
		incidents: (Array.isArray(s.incidents) ? s.incidents : [])
			.map((i) => ({ kind: String(i?.kind || ''), count: toInt(i?.count) }))
			.filter((i) => i.count > 0),
	}
}

const INCIDENT_LABELS = { asr: 'распознавание речи', tts: 'озвучка', avatar: 'ответ аватара', llm: 'ответ аватара' }

/** «распознавание речи — 1, озвучка — 2»: сбои одной строкой, отдельно от качества речи. */
export function incidentLine(incidents = []) {
	const counts = new Map()
	for (const it of incidents) {
		const label = INCIDENT_LABELS[it.kind] || 'другое'
		counts.set(label, (counts.get(label) || 0) + toInt(it.count))
	}
	return [...counts]
		.filter(([, n]) => n > 0)
		.map(([label, n]) => `${label} — ${n}`)
		.join(', ')
}

/* ---------------------------------------------------------------------------
 * Редьюсер экрана
 * ------------------------------------------------------------------------- */

/**
 * Состояние экрана.
 *
 * phase — где разговор: loading, failed (не открылся), disabled (учитель
 * убрал разговор), intro (не начат), active, closed.
 *
 * mode — как ученик говорит с аватаром: live (голосовой разговор, одна
 * сессия GPT Live) или text (ввод текстом). Голосовых сообщений с записью
 * ответа больше нет: голос — только через GPT Live, а когда он недоступен,
 * остаётся ввод текстом и «Повторить».
 *
 * composer — панель ответа в режиме text: text (поле ввода), sending,
 * send_error (ответ сохранён или не дошёл — «Повторить»).
 */
export function initialState() {
	return {
		phase: 'loading',
		error: '',
		language: '',
		maxTurns: 0,
		turnsTaken: 0,
		turns: [],
		// Открытые подсказки и состояние озвучки — по ключу реплики.
		hints: {},
		voices: {},
		starting: false,
		composer: 'text',
		// null | 'ask' | 'denied' | 'missing' | 'failed' — окно про микрофон.
		micPrompt: null,
		typed: '',
		notice: '',
		pending: null,
		sendError: null,
		summary: { status: 'idle', data: null, failed: false },
		// Живой режим (договор, раздел 10): одна сессия GPT Live. mode —
		// 'live' или 'text'; по умолчанию live, если сервер и браузер его
		// дают. modeChosen — ученик выбрал сам или голосовой разговор
		// сорвался: перечитывание разговора выбор не сбрасывает.
		mode: 'text',
		modeChosen: false,
		live: initialLive(),
	}
}

/**
 * Состояние живой сессии для экрана.
 *
 * status: idle (нет сессии), connecting, active, reconnecting, closing
 * (аватар прощается), ended. talking — звучит аватар, hearing — ученик
 * говорит, mic — ученик включил микрофон кнопкой. draining — после перехода к вводу текстом дописываются фразы
 * голосового разговора. replaying — какая реплика звучит записью.
 * unavailable — почему голосового разговора нет (сервер не дал, браузер не
 * умеет, не соединилось, нет микрофона); пусто — ученик выбрал текст сам.
 */
function initialLive(available = false, unavailable = '') {
	return { available, status: 'idle', talking: false, hearing: false, mic: false, draining: false, replaying: null, unavailable }
}

function waitingSummary(state) {
	return state.summary.status === 'ready' ? state.summary : { status: 'waiting', data: null, failed: false }
}

// Режим можно переключать, только пока ответ не уходит: посреди отправки
// переключение потеряло бы его.
const SWITCHABLE = new Set(['text', 'send_error'])

// Ввод текстом: по выбору ученика (причины нет) или потому что голосового
// разговора нет (причина — строка под «Голосовой разговор недоступен»).
function toText(state, unavailable = '') {
	return {
		...state,
		mode: 'text',
		modeChosen: true,
		starting: false,
		notice: '',
		error: '',
		live: { ...state.live, unavailable },
	}
}

export function reducer(state, action) {
	switch (action.type) {
		case 'loaded': {
			const res = action.res || {}
			const turns = readTurns(res.turns)
			const maxTurns = toInt(res.max_turns)
			const turnsTaken = toInt(res.turns_taken)
			// Голосовой разговор — если его даёт сервер (live_available) и умеет
			// браузер. Нет — ввод текстом с причиной; выбор ученика и причину
			// после сбоя перечитывание не сбрасывает.
			const available = Boolean(res.live_available) && action.liveSupported !== false
			let mode = available ? 'live' : 'text'
			let unavailable = ''
			if (!available && action.liveSupported === false) {
				unavailable = action.liveBlocked === 'insecure' ? LIVE_INSECURE_MESSAGE : LIVE_BROWSER_MESSAGE
			} else if (!available) unavailable = LIVE_OFF_MESSAGE
			else if (state.modeChosen && state.mode === 'text') {
				mode = 'text'
				unavailable = state.live.unavailable
			}
			const base = {
				...state,
				error: '',
				language: res.language || state.language,
				maxTurns,
				turnsTaken,
				turns,
				starting: false,
				pending: null,
				sendError: null,
				micPrompt: null,
				composer: 'text',
				mode,
				live: initialLive(available, unavailable),
			}
			if (res.enabled === false) return { ...base, phase: 'disabled' }
			if (res.status === 'closed' || (maxTurns > 0 && turnsTaken >= maxTurns && res.status !== 'not_started')) {
				return { ...base, phase: 'closed', summary: waitingSummary(state) }
			}
			if (res.status !== 'active' || !turns.some((t) => t.role === 'avatar')) return { ...base, phase: 'intro' }
			const { pending, saved } = action.restored || resolvePending(null, res)
			if (!pending) return { ...base, phase: 'active' }
			return {
				...base,
				phase: 'active',
				turns: withStudentTurn(turns, pending, saved ? 'saved' : 'unsent'),
				turnsTaken: saved ? Math.max(turnsTaken, pending.turnNo) : turnsTaken,
				pending,
				composer: 'send_error',
				sendError: saved ? { kind: 'resume', message: RESUME_MESSAGE } : { kind: 'unsent', message: UNSENT_MESSAGE },
			}
		}
		case 'load_failed':
			return { ...state, phase: 'failed', error: action.message || 'Не удалось открыть разговор' }

		case 'start':
			return { ...state, starting: true, error: '' }
		case 'started': {
			const res = action.res || {}
			const turnNo = Number.isInteger(res.turn_no) ? res.turn_no : 0
			const key = turnKey('avatar', turnNo)
			const turns = state.turns.some((t) => t.key === key) || !res.reply ? state.turns : [...state.turns, avatarTurn(turnNo, res)]
			const maxTurns = toInt(res.max_turns) || state.maxTurns
			const turnsTaken = Number.isInteger(res.turns_taken) ? res.turns_taken : state.turnsTaken
			const closed = res.status === 'closed'
			return {
				...state,
				starting: false,
				turns,
				maxTurns,
				turnsTaken,
				phase: closed ? 'closed' : 'active',
				summary: closed ? waitingSummary(state) : state.summary,
			}
		}
		case 'start_failed':
			return { ...state, starting: false, error: action.message || 'Не удалось начать разговор' }

		// Подсказка — только показ текста: ни звук, ни счёт ответов, ни панель
		// ответа она не трогает (Уч-1).
		case 'hint':
			return { ...state, hints: { ...state.hints, [action.key]: !state.hints[action.key] } }

		case 'audio_loading':
			return { ...state, voices: { ...state.voices, [action.key]: 'loading' } }
		case 'audio_ready': {
			const voices = { ...state.voices }
			delete voices[action.key]
			return { ...state, voices }
		}
		// Озвучка не вышла — текст реплики открывается сам: иначе ученику
		// нечего было бы слушать и нечего читать.
		case 'audio_failed':
			return {
				...state,
				voices: { ...state.voices, [action.key]: 'failed' },
				hints: { ...state.hints, [action.key]: true },
			}

		case 'mic_ask':
			return { ...state, micPrompt: 'ask' }
		case 'mic_denied':
		case 'mic_missing':
		case 'mic_failed': {
			const kind = action.type.slice(4)
			return { ...state, micPrompt: kind }
		}
		case 'mic_close':
			return { ...state, micPrompt: null }
		// «Не сейчас» и «Ответить текстом» в окне микрофона — ввод текстом.
		// После отказа браузера под ним причина и «Повторить».
		case 'text_mode': {
			const closed = { ...state, micPrompt: null }
			if (state.mode !== 'text' && !canSwitchMode(state, 'text')) return closed
			return toText(closed, MIC_REASONS[state.micPrompt] || '')
		}

		case 'typed':
			return { ...state, typed: String(action.text ?? '') }
		case 'notice':
			return { ...state, notice: action.text || '' }

		case 'send':
			return {
				...state,
				composer: 'sending',
				pending: action.pending,
				sendError: null,
				notice: '',
				turns: withStudentTurn(state.turns, action.pending, 'sending'),
			}
		case 'answered': {
			const res = action.res || {}
			const p = action.pending
			let turns = withStudentTurn(state.turns, p, '')
			const replyNo = Number.isInteger(res.turn_no) ? res.turn_no : p.turnNo
			if (res.reply && !turns.some((t) => t.key === turnKey('avatar', replyNo))) {
				turns = [...turns, avatarTurn(replyNo, res)]
			}
			const maxTurns = toInt(res.max_turns) || state.maxTurns
			const turnsTaken = Number.isInteger(res.turns_taken) ? res.turns_taken : Math.max(state.turnsTaken, p.turnNo)
			const closed = res.status === 'closed' || (maxTurns > 0 && turnsTaken >= maxTurns)
			return {
				...state,
				turns,
				maxTurns,
				turnsTaken,
				pending: null,
				sendError: null,
				typed: p.input === 'text' ? '' : state.typed,
				notice: '',
				composer: 'text',
				phase: closed ? 'closed' : 'active',
				summary: closed ? waitingSummary(state) : state.summary,
			}
		}
		case 'send_failed': {
			const p = state.pending || action.pending
			if (!p) return state
			const unavailable = action.kind === 'unavailable'
			const turns = withStudentTurn(state.turns, p, unavailable ? 'saved' : 'unsent')
			// «Аватар недоступен» — ответ уже записан и засчитан сервером.
			const turnsTaken = unavailable ? Math.max(state.turnsTaken, p.turnNo) : state.turnsTaken
			// Последний ответ сохранён — разговор сервер уже закрыл (договор,
			// раздел 6). Прощание не обязательно, итог от него не зависит, и
			// держать ученика на «Повторить» незачем.
			if (unavailable && state.maxTurns > 0 && turnsTaken >= state.maxTurns) {
				return {
					...state,
					turns,
					turnsTaken,
					pending: null,
					sendError: null,
					composer: 'text',
					phase: 'closed',
					summary: waitingSummary(state),
					notice: FAREWELL_MESSAGE,
				}
			}
			return {
				...state,
				turns,
				turnsTaken,
				pending: p,
				composer: 'send_error',
				sendError: {
					kind: unavailable ? 'unavailable' : 'network',
					message: action.message || (unavailable ? UNAVAILABLE_MESSAGE : NETWORK_MESSAGE),
				},
			}
		}
		// Сервер ответ не принял: возвращаем текст в поле, чтобы не набирать заново.
		case 'send_rejected': {
			const p = state.pending || action.pending
			return {
				...state,
				turns: p ? state.turns.filter((t) => !(t.key === turnKey('student', p.turnNo) && t.local)) : state.turns,
				pending: null,
				sendError: null,
				composer: 'text',
				typed: p?.text || state.typed,
				notice: action.message || 'Ответ не принят',
			}
		}
		// Разговор на сервере уже в другом состоянии — экран его перечитает.
		case 'send_conflict': {
			const p = state.pending || action.pending
			return {
				...state,
				turns: p ? state.turns.filter((t) => !(t.key === turnKey('student', p.turnNo) && t.local)) : state.turns,
				pending: null,
				sendError: null,
				composer: 'text',
				notice: action.message || '',
			}
		}

		case 'summary': {
			const res = action.res || {}
			if (res.status === 'ready') return { ...state, summary: { status: 'ready', data: readSummary(res), failed: false } }
			if (res.status === 'failed') return { ...state, summary: { ...state.summary, failed: true } }
			if (res.status === 'pending') return { ...state, summary: { ...state.summary, failed: false } }
			return state
		}
		case 'summary_slow':
			return { ...state, summary: { ...state.summary, status: 'slow' } }
		case 'summary_retry':
			return { ...state, summary: { status: 'waiting', data: null, failed: false } }

		/* --- живой режим (договор, раздел 10) --- */

		case 'mode': {
			const mode = action.mode === 'live' ? 'live' : 'text'
			if (mode === state.mode) return { ...state, modeChosen: true }
			if (!canSwitchMode(state, mode)) return state
			if (mode === 'text') return toText(state)
			return { ...state, mode, modeChosen: true, notice: '', error: '', live: { ...state.live, unavailable: '' } }
		}
		// Голосовой разговор не поднялся ещё до сессии (браузер не дал звук
		// микрофона) — ввод текстом с причиной.
		case 'live_unavailable':
			return toText(state, action.message || LIVE_OFF_MESSAGE)
		case 'live_status': {
			const status = action.status || 'idle'
			const running = status === 'connecting' || status === 'active' || status === 'reconnecting'
			return {
				...state,
				starting: state.phase === 'intro' && status === 'connecting',
				live: { ...state.live, status, ...(running ? {} : { talking: false, hearing: false, mic: false }) },
			}
		}
		// …/conversation/live ответил: разговор начат (или уже закрыт), счёт — с сервера.
		case 'live_open': {
			const res = action.res || {}
			const closed = res.status === 'closed'
			return {
				...state,
				starting: false,
				error: '',
				maxTurns: toInt(res.max_turns) || state.maxTurns,
				turnsTaken: Number.isInteger(res.turns_taken) ? res.turns_taken : state.turnsTaken,
				phase: closed ? 'closed' : 'active',
				summary: closed ? waitingSummary(state) : state.summary,
			}
		}
		case 'live_flags':
			return { ...state, live: { ...state.live, talking: Boolean(action.talking), hearing: Boolean(action.hearing) } }
		case 'live_mic':
			return { ...state, live: { ...state.live, mic: Boolean(action.open) } }
		case 'live_replay':
			return { ...state, live: { ...state.live, replaying: action.id || null } }
		case 'live_student':
			return { ...state, turns: liveStudent(state.turns, action) }
		case 'live_draft':
			return {
				...state,
				turns: state.turns.map((t) =>
					t.key === liveKey('student', action.id) ? { ...t, live: { ...t.live, draft: String(action.text ?? '') } } : t,
				),
			}
		// Ответ засчитан: «Осталось» считает только засчитанные (Уч-4).
		case 'live_answered': {
			const res = action.res || {}
			const p = action.pending || {}
			const key = liveKey('student', action.id)
			const turnNo = Number.isInteger(res.turn_no) ? res.turn_no : Number.isInteger(p.turnNo) ? p.turnNo : null
			return {
				...state,
				turns: state.turns.map((t) =>
					t.key === key
						? { ...t, turnNo, content: p.text ?? t.content, edited: Boolean(p.edited), live: { ...t.live, stage: 'sent', note: '' } }
						: t,
				),
				maxTurns: toInt(res.max_turns) || state.maxTurns,
				turnsTaken: Number.isInteger(res.turns_taken) ? res.turns_taken : Math.max(state.turnsTaken, toInt(p.turnNo)),
			}
		}
		case 'live_avatar':
			return { ...state, turns: liveAvatar(state.turns, action) }
		// Сессия сорвалась (message — почему) или ученик выбрал ввод текстом:
		// дальше — он, а фразы голосового разговора дописываются (draining).
		case 'live_handover': {
			const next = toText(state, action.message || '')
			return {
				...next,
				live: { ...next.live, status: 'idle', talking: false, hearing: false, mic: false, draining: true, replaying: null },
			}
		}
		case 'live_drained':
			return { ...state, live: { ...state.live, draining: false } }
		// Аватар попрощался после последнего ответа — ждём итог.
		case 'live_finished':
			return {
				...state,
				phase: 'closed',
				summary: waitingSummary(state),
				live: { ...state.live, status: 'ended', talking: false, hearing: false, mic: false },
			}

		default:
			return state
	}
}

/* ---------------------------------------------------------------------------
 * Живой режим: реплики в ленте
 * ------------------------------------------------------------------------- */

/** Ключ реплики живого разговора: номера у неё до записи ещё нет. */
export function liveKey(role, id) {
	return `live:${role === 'student' ? 's' : 'a'}:${id}`
}

// Фраза ученика появляется в ленте с началом речи (скрыто) или с
// распознаванием. Остальные стадии только обновляют уже показанную: поздняя
// весть о снятой фразе не должна вернуть её на экран.
const LIVE_STUDENT_NEW = new Set(['hearing', 'recognizing'])

/**
 * Фраза ученика в живом разговоре. live.stage: hearing (говорит, не видна),
 * recognizing, review («Верно» / «Исправить»), editing, queued, sending,
 * sent, unsent, rejected, late (разговор уже закрыт), asr_failed; dropped —
 * убрать из ленты (не ответ).
 */
function liveStudent(turns, action) {
	const key = liveKey('student', action.id)
	const at = turns.findIndex((t) => t.key === key)
	if (action.stage === 'dropped') return at < 0 ? turns : turns.filter((t) => t.key !== key)
	if (at < 0 && !LIVE_STUDENT_NEW.has(action.stage)) return turns
	const old =
		at < 0
			? {
					key,
					turnNo: null,
					role: 'student',
					content: '',
					translation: '',
					input: 'voice',
					edited: false,
					local: '',
					live: { id: action.id, stage: action.stage, asr: null, draft: '', note: '', deadline: 0 },
				}
			: turns[at]
	const live = { ...old.live, stage: action.stage || old.live.stage, note: action.note || '' }
	const next = { ...old, live }
	if (action.asr) {
		live.asr = action.asr
		live.draft = action.asr.text
		next.content = action.asr.text
	}
	if (action.deadline) live.deadline = action.deadline
	if (action.text != null) next.content = String(action.text)
	if (action.edited != null) next.edited = Boolean(action.edited)
	if (live.stage === 'editing' && !live.draft) live.draft = next.content
	return at < 0 ? [...turns, next] : turns.map((t, i) => (i === at ? next : t))
}

/**
 * Реплика аватара в живом разговоре: пузырь без текста. live.stage:
 * speaking, done (договорена, записывается), saved (записана, перевод
 * есть), unsaved. live.sound — есть запись звука для «Повторить».
 */
function liveAvatar(turns, action) {
	const key = liveKey('avatar', action.id)
	const at = turns.findIndex((t) => t.key === key)
	if (at < 0 && action.stage !== 'speaking' && action.stage !== 'done') return turns
	const old =
		at < 0
			? {
					key,
					turnNo: null,
					role: 'avatar',
					content: '',
					translation: '',
					input: 'text',
					edited: false,
					local: '',
					live: { id: action.id, stage: action.stage, sound: false },
				}
			: turns[at]
	const next = { ...old, live: { ...old.live } }
	if (action.stage) next.live.stage = action.stage
	if (action.content != null) next.content = String(action.content)
	if (action.translation != null) next.translation = String(action.translation)
	if (Number.isInteger(action.turnNo)) next.turnNo = action.turnNo
	if (action.sound != null) next.live.sound = Boolean(action.sound)
	return at < 0 ? [...turns, next] : turns.map((t, i) => (i === at ? next : t))
}

/**
 * Можно ли переключить режим разговора сейчас.
 *
 * Только между делами: посреди отправки или живой сессии переключение
 * потеряло бы ответ. В голосовой разговор — только если он доступен.
 */
export function canSwitchMode(state, mode) {
	if (mode === 'live' && !state.live.available) return false
	return betweenAnswers(state)
}

/**
 * Можно ли нажать «Повторить» у «Голосовой разговор недоступен»: то же
 * «между делами», но без live.available — повтор сам спросит сервер заново.
 */
export function canRetryLive(state) {
	return betweenAnswers(state)
}

function betweenAnswers(state) {
	if (state.live.draining) return false
	if (!['idle', 'ended'].includes(state.live.status)) return false
	if (state.phase === 'intro') return !state.starting
	if (state.phase !== 'active') return false
	return SWITCHABLE.has(state.composer)
}

/** Подпись паузы живого разговора, если последний ответ ещё ждёт отправки. */
export function livePendingNote(state) {
	if (!state.pending) return ''
	if (state.sendError?.kind === 'resume') return 'Твой последний ответ сохранён — аватар продолжит с него.'
	return 'Последний ответ не успел отправиться — он уйдёт первым, когда разговор продолжится.'
}
