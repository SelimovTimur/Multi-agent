// poll.js — опрос сервера, пока экран ученика на виду.
//
// Учитель выдаёт задания и завершает работу на уроке у себя, а ученик узнаёт
// об этом только из опроса: дети на уроке страницу не обновляют, они ждут,
// пока задание появится само (docs/product/class-lessons/story.md, JOB-8 и
// JOB-9). Поэтому оба экрана ученика спрашивают сервер раз в 5 секунд.
//
// Три правила, ради которых опрос вынесен сюда, а не написан в экранах:
//
// - Следующий запрос уходит только после ответа на предыдущий. Интервал
//   на медленной связи накладывал бы запросы друг на друга, и ответы
//   приходили бы задом наперёд.
// - Спрятанная вкладка не опрашивает: класс из тридцати телефонов с
//   погасшими экранами — это тридцать пустых запросов каждые 5 секунд.
//   Вернулся на вкладку — спрашиваем сразу, а не через 5 секунд.
// - Уходя с экрана, запрос обрываем: его ответ уже некому показать.

import { useEffect } from 'react'

/**
 * usePoll вызывает load(signal) сразу и затем через ms после каждого ответа.
 *
 * load сам разбирает ответ и ошибки: опрос не знает, что показывать. Новая
 * функция load (другая попытка, другой раздел) начинает опрос заново.
 */
export function usePoll(load, ms = 5000) {
	useEffect(() => {
		let alive = true
		let timer = 0
		let ctrl = null

		async function tick() {
			clearTimeout(timer)
			if (!alive || ctrl || document.hidden) return
			ctrl = new AbortController()
			try {
				await load(ctrl.signal)
			} catch {
				// Ошибку разбирает load; здесь важно только не оборвать цикл.
			} finally {
				ctrl = null
			}
			if (alive && !document.hidden) timer = setTimeout(tick, ms)
		}

		const wake = () => {
			if (!document.hidden) tick()
		}
		document.addEventListener('visibilitychange', wake)
		tick()
		return () => {
			alive = false
			clearTimeout(timer)
			ctrl?.abort()
			document.removeEventListener('visibilitychange', wake)
		}
	}, [load, ms])
}
