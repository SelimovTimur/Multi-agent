// pending.js — ответы ученика, которые не дошли до сервера.
//
// В школе связь рвётся посреди урока. Раньше ответ, отправленный в этот
// момент, пропадал: ученик видел ошибку, и если не нажимал вариант ещё раз,
// вопрос оставался неотвеченным, а половина упражнения — несохранённой.
// Теперь такой ответ ложится в очередь на устройстве и уходит сам, когда
// связь вернётся, — даже если вкладку закрыли и урок открыли заново.
//
// Повторная отправка ничего не удваивает: сервер принимает ответ на вопрос
// один раз, а на повтор отвечает тем, что уже записал. Поэтому ответ, который
// дошёл, но ответ сервера потерялся по дороге, повтором тоже не испортить.
//
// Без React и браузера: правила проверяет scripts/test-pending-answers.mjs.

const PREFIX = 'nr-pending-answers:'
// Больше на одну попытку не бывает: в уроке десятки вопросов, не сотни.
const LIMIT = 300

function localStore() {
	try {
		return globalThis.localStorage || null
	} catch {
		return null
	}
}

/** Ключ вопроса — как у сервера: «упражнение:вопрос». */
export function pendingKey(p) {
	return `${p.exerciseId}:${p.itemId}`
}

function valid(p) {
	return (
		p && typeof p === 'object' && typeof p.exerciseId === 'string' && p.exerciseId !== '' &&
		typeof p.itemId === 'string' && p.itemId !== '' && typeof p.answer === 'string'
	)
}

/** Очередь попытки из хранилища; испорченная запись — пустая очередь. */
export function loadPending(attemptId, store = localStore()) {
	try {
		const list = JSON.parse((store && store.getItem(PREFIX + attemptId)) || '[]')
		return Array.isArray(list) ? list.filter(valid) : []
	} catch {
		return []
	}
}

/** Сохранить очередь; пустая — удаляется. Хранилище закрыто — молча. */
export function savePending(attemptId, list, store = localStore()) {
	try {
		if (!store) return
		if (list.length) store.setItem(PREFIX + attemptId, JSON.stringify(list.slice(-LIMIT)))
		else store.removeItem(PREFIX + attemptId)
	} catch {
		// Нет места или приватный режим: очередь живёт до закрытия вкладки.
	}
}

/** Добавить ответ; второй ответ на тот же вопрос заменяет первый. */
export function addPending(list, item) {
	const key = pendingKey(item)
	return [...list.filter((p) => pendingKey(p) !== key), item]
}

/**
 * Стоит ли отправить ещё раз. Сбой связи, таймаут, перегрузка и ошибка
 * сервера — да. Отказ по существу (задание закрыли, нет доступа, урок не
 * найден) повтором не исправить.
 */
export function retryable(err) {
	if (!err) return false
	if (err.status === 0 || err.code === 'network' || err.code === 'timeout') return true
	return err.status === 429 || err.status >= 500
}

/**
 * Отправить очередь по порядку. Дошёл — в sent с ответом сервера; отказ по
 * существу — в dropped, повторять незачем; сбой связи — отправка
 * прекращается до следующей попытки, остаток — в kept.
 */
export async function flushPending(list, send) {
	const sent = []
	const dropped = []
	const kept = [...list]
	while (kept.length) {
		const item = kept[0]
		try {
			const res = await send(item)
			sent.push({ item, res })
			kept.shift()
		} catch (err) {
			if (retryable(err)) break
			dropped.push({ item, err })
			kept.shift()
		}
	}
	return { sent, dropped, kept }
}

/** Пауза перед следующей попыткой: 3, 6, 12… секунд, не дольше минуты. */
export function nextDelay(previous) {
	return Math.min(60000, previous ? previous * 2 : 3000)
}
