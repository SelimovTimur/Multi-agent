// usePolling.js — опрос отчёта по разговору, пока экран открыт и вкладка видна.
//
// Отличие от опроса таблицы класса (ClassLessonLive.jsx): срок следующего
// запроса решает сам запрос. Отчёт ученика после готовности не меняется, и
// опрашивать его дальше незачем, а разбор, который вот-вот будет готов, стоит
// спрашивать чаще, чем разговор, который ученик ещё ведёт.

import { useEffect, useRef } from 'react'

/**
 * usePolling(load, deps) — load(signal) делает запрос и возвращает, через
 * сколько миллисекунд спросить снова, или null — больше не спрашивать.
 *
 * Запросы не накладываются: следующий заводится только после ответа на
 * предыдущий. Пока вкладка скрыта, опрос стоит; срок, подошедший за это
 * время, отрабатывается сразу, как учитель вернулся. Смена deps — новый
 * опрос с немедленным запросом.
 *
 * load сам разбирает свои ошибки: неожиданное исключение опрос
 * останавливает, а не крутит в цикле.
 */
export function usePolling(load, deps) {
	const ref = useRef(load)
	useEffect(() => {
		ref.current = load
	})

	useEffect(() => {
		let stopped = false
		let timer = null
		let ctrl = null
		let due = false

		async function tick() {
			clearTimeout(timer)
			timer = null
			if (stopped || ctrl) return
			if (document.hidden) {
				due = true
				return
			}
			ctrl = new AbortController()
			let next = null
			try {
				next = await ref.current(ctrl.signal)
			} catch {
				next = null
			} finally {
				ctrl = null
			}
			if (!stopped && next) timer = setTimeout(tick, next)
		}

		function onVisibility() {
			if (!document.hidden && due) {
				due = false
				tick()
			}
		}

		tick()
		document.addEventListener('visibilitychange', onVisibility)
		return () => {
			stopped = true
			clearTimeout(timer)
			ctrl?.abort()
			document.removeEventListener('visibilitychange', onVisibility)
		}
		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, deps)
}

/**
 * Отказ, после которого спрашивать снова бессмысленно: нет доступа, нет
 * такой выдачи или ученика. Сеть, таймаут и занятый сервер — не такие.
 */
export const fatal = (err) =>
	err?.status >= 400 && err.status < 500 && err.status !== 408 && err.status !== 429
