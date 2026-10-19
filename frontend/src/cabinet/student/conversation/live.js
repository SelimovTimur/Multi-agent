// live.js — живой режим разговора урока: правила без браузера и сети.
//
// По умолчанию разговор урока идёт одной живой сессией GPT Live (договор,
// раздел 10): ученик говорит кнопкой микрофона, аватар отвечает своим
// голосом. Рядом с сессией работают наше распознавание и движок: каждая
// фраза ученика распознаётся, уходит в разговор ответом и возвращает
// указание сессии на следующий ход (steer). Здесь — правила, на которых это держится:
//
// - какая фраза становится ответом: уверенно распознанная — сразу,
//   неуверенная — после «Верно» или правки ученика, а без них — как есть
//   через 20 секунд; пустая — не ответ;
// - LiveLedger — очередь реплик: ответы и реплики аватара записываются
//   строго в порядке разговора, номер реплики аватара — номер последнего
//   ответа ученика перед ней;
// - вид указаний сессии.
//
// Связка с браузером — liveTalk.js, экран — AvatarChat.jsx, тест —
// frontend/scripts/test-conversation-live.mjs. Договор —
// docs/product/avatar-goals/contract.md (раздел 10), поведение экрана —
// docs/product/avatar-goals/student-ui.md («Живой режим»).

import { asrNextStep, classifySendError, reviewHint } from './state.js'

/** Неуверенная фраза ждёт ученика столько, потом уходит как есть. */
export const REVIEW_MS = 20000
/** Прощание после последнего ответа: дольше сессию не держим. */
export const FAREWELL_MS = 20000
/** Паузы перед повтором ответа при обрыве связи (тот же client_msg_id). */
export const SEND_RETRY_MS = [1000, 2000, 4000, 8000]
/** Паузы перед повтором записи реплики аватара. */
export const SAVE_RETRY_MS = [1000, 3000]

/* ---------------------------------------------------------------------------
 * Указания сессии
 * ------------------------------------------------------------------------- */

// Политика сессии (договор, 10.3) велит следовать последнему указанию
// «Turn guidance» — оно заменяет прежние. Указание движка подписываем так,
// если подписи в нём ещё нет: иначе сессия могла бы принять его за обычную
// реплику контекста и держаться старого.
const GUIDANCE = /^turn guidance\b/i

/** Текст указания сессии: steer движка с подписью «Turn guidance». */
export function steerContent(steer) {
	const text = String(steer || '').trim()
	if (!text) return ''
	return GUIDANCE.test(text) ? text : `Turn guidance (replaces any earlier guidance):\n${text}`
}

/** Вступление, если сервер не прислал указания: аватар начинает сам. */
export const INTRO_GUIDANCE = steerContent(
	'Begin now, without waiting for the learner: greet them briefly in Tatar and ask one easy question about the lesson topic. Then listen.',
)

/** Конец разговора, если движок не прислал прощального указания. */
export const FAREWELL_GUIDANCE = steerContent(
	'The lesson conversation is over. Briefly thank the learner in Tatar and say goodbye. Do not ask a question.',
)

/**
 * Указание движка уходит в контекст сессии (session.thinking.append), а не в
 * её инструкции. На настоящих сессиях session.instructions.append сам
 * вызывал ответ: сессия уже ответила ученику, а указание, пришедшее через
 * пару секунд, заставляло её задать второй вопрос (и попрощаться дважды).
 * Контекст модель учитывает в следующем ответе. Но и на
 * контекст, пришедший сразу после её ответа, сессия иногда отвечала второй
 * репликой, поэтому liveTalk.js отдаёт указание, когда ученик заговорит.
 * Говорить по команде сессия начинает только на response.create:
 * вступление и прощание.
 */
export function instructionEvent(content, eventId) {
	return { type: 'session.thinking.append', event_id: eventId, delegation_id: null, content }
}

/**
 * Слова ученика сессии — текстом. Сессия звук ученика не слышит (в неё идёт
 * тишина): иначе на паузе при открытой кнопке она вступала сама и отвечала
 * на обрывок фразы.
 */
export function learnerEvent(text) {
	return { type: 'response.item.create', item: { type: 'message', role: 'user', content: [{ type: 'input_text', text }] } }
}

export function responseEvent(eventId) {
	return { type: 'response.create', event_id: eventId }
}

/* ---------------------------------------------------------------------------
 * Фраза ученика: ответ или нет
 * ------------------------------------------------------------------------- */

/** Есть ли в тексте хоть одна буква: «…» и «?!» — не ответ. */
export function meaningful(text) {
	return /\p{L}/u.test(String(text || ''))
}

/**
 * liveAsrStep — что делать с распознанной фразой.
 *
 * 'empty' — не ответ (ничего не расслышали или в тексте нет букв);
 * 'send' — распознавание уверено, ответ уходит сразу; 'review' — ученик
 * видит текст и подтверждает или правит его, разговор при этом идёт.
 * Решает сервер (needs_review) по ответу распознавания.
 */
export function liveAsrStep(asr) {
	if (!asr?.text || !meaningful(asr.text)) return 'empty'
	return asrNextStep(asr)
}

/** Подпись под неуверенной фразой: почему просим проверить и что будет без ответа. */
export function liveReviewHint(reasons = []) {
	const known = ['quiet_audio', 'foreign_letters', 'low_confidence'].some((r) => reasons.includes(r))
	const why = known ? reviewHint(reasons) : 'Проверь, правильно ли мы тебя услышали.'
	return `${why} Если ничего не нажимать, через 20 секунд текст уйдёт как есть.`
}

/** Ответ закрыл разговор: последний из N. */
export function closingAnswer(res, maxTurns = 0) {
	if (res?.status === 'closed') return true
	const max = Number(res?.max_turns) || maxTurns || 0
	const taken = Number(res?.turns_taken)
	return max > 0 && Number.isFinite(taken) && taken >= max
}

/* ---------------------------------------------------------------------------
 * Очередь реплик
 * ------------------------------------------------------------------------- */

function wait(ms) {
	return new Promise((resolve) => setTimeout(resolve, ms))
}

/**
 * LiveLedger — запись разговора строго по порядку.
 *
 * Реплики встают в очередь в момент, когда начались: фраза ученика — с
 * началом речи, реплика аватара — с первым словом расшифровки. Записываются
 * по одной, по порядку, и только готовые: фраза — когда решено, ответ ли она
 * (распознана и подтверждена), реплика — когда договорена. Неготовая голова
 * держит остальных, иначе ответы легли бы в историю задом наперёд.
 *
 * Номер ответа — следующий за последним записанным; номер реплики аватара —
 * номер последнего ответа перед ней (вступление — 0, несколько реплик на
 * один номер допустимы). Фраза, которая ответом не стала, номера не
 * занимает.
 *
 * Элемент уходит из очереди, только когда сервер его принял; повтор — с тем
 * же client_msg_id (как TurnQueue в live/turn-queue.js), сервер не засчитает
 * ответ дважды. События наружу — через on: sending, answered, rejected,
 * unsent, conflict, saved, unsaved, idle.
 */
export class LiveLedger {
	constructor({ lastTurnNo = 0, sendAnswer, saveAvatar, sleep = wait, on = {} } = {}) {
		this.lastTurnNo = lastTurnNo
		this.sendAnswer = sendAnswer
		this.saveAvatar = saveAvatar
		this.sleep = sleep
		this.on = on
		this.items = []
		this.running = false
		this.blocked = false
		this.current = null
	}

	/** Очередь пуста и ничего не отправляется. */
	get idle() {
		return !this.items.length && !this.running
	}

	addStudent(id) {
		this.items.push({ kind: 'student', id, ready: false, answer: null, pending: null })
	}

	/** answer — ответ без номера (makeAnswer) или null: фраза ответом не стала. */
	resolveStudent(id, answer) {
		const item = this.find('student', id)
		if (!item || item.ready) return false
		item.ready = true
		item.answer = answer || null
		void this.pump()
		return true
	}

	/**
	 * steerTurn — номер хода последнего указания, которое ушло в сессию до
	 * первого слова этой реплики (вступление — 0; null — указаний не было).
	 * Сессия отвечает сразу, а указание приходит через несколько секунд:
	 * реплика часто звучит по прежнему указанию, и движку нужно знать, к
	 * какой цели она вела на самом деле (договор, 10.1 и 10.4).
	 */
	addAvatar(id, { steerTurn = null } = {}) {
		this.items.push({ kind: 'avatar', id, ready: false, content: '', reply: null, steerTurn })
	}

	completeAvatar(id, content) {
		const item = this.find('avatar', id)
		if (!item || item.ready) return false
		item.ready = true
		item.content = String(content || '').trim()
		void this.pump()
		return true
	}

	/**
	 * Снять фразы ученика, которые ещё не ушли: разговор закрыт, ответов
	 * больше не принимают. Та, что отправляется сейчас, остаётся.
	 */
	dropStudents() {
		const dropped = []
		this.items = this.items.filter((item) => {
			if (item.kind !== 'student' || item === this.current) return true
			dropped.push(item.id)
			return false
		})
		void this.pump()
		return dropped
	}

	/** «Повторить» у не дошедшего ответа: тот же ответ, тот же идентификатор. */
	retry() {
		if (!this.blocked) return
		this.blocked = false
		void this.pump()
	}

	find(kind, id) {
		return this.items.find((item) => item.kind === kind && item.id === id)
	}

	async pump() {
		if (this.running) return
		this.running = true
		try {
			while (this.items.length && !this.blocked) {
				const item = this.items[0]
				if (!item.ready) break
				this.current = item
				const done = item.kind === 'student' ? await this.deliver(item) : await this.persist(item)
				this.current = null
				if (!done) break
				const at = this.items.indexOf(item)
				if (at >= 0) this.items.splice(at, 1)
			}
		} finally {
			this.running = false
			this.current = null
		}
		if (!this.items.length) this.on.idle?.()
	}

	async deliver(item) {
		if (!item.answer) return true
		if (!item.pending) item.pending = { ...item.answer, turnNo: this.lastTurnNo + 1 }
		const pending = item.pending
		for (let attempt = 0; ; attempt++) {
			this.on.sending?.({ id: item.id, pending, attempt })
			try {
				const res = (await this.sendAnswer(pending)) || {}
				if (Number.isInteger(res.turn_no)) this.lastTurnNo = res.turn_no
				else if (Number.isInteger(res.turns_taken)) this.lastTurnNo = res.turns_taken
				else this.lastTurnNo = pending.turnNo
				this.on.answered?.({ id: item.id, res, pending })
				return true
			} catch (err) {
				const outcome = classifySendError(err)
				if (outcome.kind === 'unavailable') {
					// Ответ записан и засчитан, движок не ответил (договор, раздел 6):
					// указания на этот ход не будет, разговор идёт дальше.
					this.lastTurnNo = pending.turnNo
					this.on.answered?.({ id: item.id, res: { turns_taken: pending.turnNo }, pending, unavailable: true })
					return true
				}
				if (outcome.action === 'send_conflict') {
					this.blocked = true
					this.on.conflict?.({ id: item.id, message: outcome.message })
					return false
				}
				if (outcome.action === 'send_rejected') {
					this.on.rejected?.({ id: item.id, message: outcome.message })
					return true
				}
				const pause = SEND_RETRY_MS[attempt]
				if (pause === undefined) {
					this.blocked = true
					this.on.unsent?.({ id: item.id, message: outcome.message })
					return false
				}
				await this.sleep(pause)
			}
		}
	}

	async persist(item) {
		if (!item.content) return true
		if (!item.reply) {
			item.reply = { clientMsgId: item.id, turnNo: this.lastTurnNo, content: item.content }
			if (Number.isInteger(item.steerTurn)) item.reply.steerTurn = item.steerTurn
		}
		const reply = item.reply
		for (let attempt = 0; ; attempt++) {
			try {
				const res = await this.saveAvatar(reply)
				this.on.saved?.({ id: item.id, res: res || {}, reply })
				return true
			} catch (err) {
				const status = Number(err?.status) || 0
				const pause = SAVE_RETRY_MS[attempt]
				// Отказ сервера повтором не исправить, связь — повторяем недолго:
				// без реплики аватара отчёт беднее, но ответы ученика ждать не должны.
				const refused = status >= 400 && status < 500 && status !== 408 && status !== 429
				if (refused || pause === undefined) {
					this.on.unsaved?.({ id: item.id, reply, message: err?.message || '' })
					return true
				}
				await this.sleep(pause)
			}
		}
	}
}
