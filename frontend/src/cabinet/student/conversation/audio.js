// audio.js — разрешение микрофона и звук реплик аватара.
//
// Всё, что трогает общий <audio>, — здесь, а не в экране: у проигрывателя
// свои правила уборки (отписаться от событий), и перепутать их с логикой
// разговора легко. Микрофон голосового разговора открывает liveTalk.js.
import { useEffect, useState } from 'react'

/**
 * Разрешение на микрофон: 'granted', 'denied', 'prompt' или 'unknown'.
 *
 * Спрашивать заранее нужно, чтобы показать своё окно до системного (как в
 * макете «Доступ к микрофону») — и не показывать его тому, кто уже разрешил.
 * Старый Firefox имени 'microphone' не знает и бросает — тогда «не знаем».
 */
export async function micPermission() {
	try {
		const status = await navigator.permissions?.query({ name: 'microphone' })
		return status?.state || 'unknown'
	} catch {
		return 'unknown'
	}
}

/**
 * useAudioClock — что сейчас играет общий <audio>: позиция, длина, идёт ли звук.
 *
 * Проигрыватель у каждой реплики свой на вид, но звучит один общий элемент:
 * к нему привязан аватар-Ишка, и отдельный Audio он бы не заметил. Поэтому
 * прогресс берётся из событий этого элемента, а не из таймера.
 */
export function useAudioClock(audioRef) {
	const [clock, setClock] = useState({ time: 0, duration: 0, playing: false })

	useEffect(() => {
		const el = audioRef.current
		if (!el) return undefined
		const read = () => {
			// Живой голос сессии (MediaStream в srcObject) — не реплика
			// проигрывателя: его время экрану не нужно, а timeupdate шёл бы весь
			// живой разговор и перерисовывал переписку четыре раза в секунду.
			if (el.srcObject) return
			setClock({
				time: el.currentTime || 0,
				// У потокового звука длина бывает Infinity — тогда её не показываем.
				duration: Number.isFinite(el.duration) ? el.duration : 0,
				playing: !el.paused && !el.ended,
			})
		}
		const events = ['timeupdate', 'durationchange', 'loadedmetadata', 'play', 'playing', 'pause', 'ended', 'emptied', 'seeked']
		for (const name of events) el.addEventListener(name, read)
		read()
		return () => {
			for (const name of events) el.removeEventListener(name, read)
		}
	}, [audioRef])

	return clock
}
