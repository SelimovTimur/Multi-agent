// useAvatarState.js — во что превращается происходящее на экране для аватара.
//
// «Говорит» берётся из событий самого элемента <audio>, а не из признака
// занятости и не из таймера: и озвучка ответа, и живой голосовой разговор
// играют через один и тот же элемент, и только он знает, звучит ли речь.
import { useEffect, useState } from 'react'

export function useAvatarState({ audioRef, recording, busy, transcribing, voice }) {
	const [playing, setPlaying] = useState(false)

	useEffect(() => {
		const el = audioRef?.current
		if (!el) return undefined
		const start = () => setPlaying(true)
		const stop = () => setPlaying(false)
		// Только 'playing': событие 'play' срабатывает в момент запроса
		// воспроизведения, ещё до того, как звук пошёл. С ним аватар открывал
		// бы рот и на битой озвучке, которая так и не зазвучала.
		const events = [
			['playing', start],
			['pause', stop],
			['ended', stop],
			['emptied', stop],
			['error', stop],
			['stalled', stop],
		]
		for (const [name, handler] of events) el.addEventListener(name, handler)
		// Элемент мог начать играть до того, как мы подписались.
		setPlaying(!el.paused && !el.ended && el.currentTime > 0 && el.readyState >= 3)
		return () => {
			for (const [name, handler] of events) el.removeEventListener(name, handler)
		}
	}, [audioRef])

	// Порядок важен: «говорит» проверяется первым, иначе признак занятости
	// перебьёт реальное воспроизведение и аватар замолчит на полуслове.
	if (playing) return 'speaking'
	if (busy || transcribing || voice === 'connecting') return 'thinking'
	if (recording || voice === 'active') return 'listening'
	return 'idle'
}
