// AvatarStage.jsx — тонкая обёртка над императивной сценой.
//
// Сцена создаётся один раз в эффекте и живёт своим циклом кадров; React
// только сообщает ей новое состояние. Модуль three.js подгружается
// динамически: smoke-screens рендерит экран через renderToString, где нет
// ни браузера, ни WebGL, и статический импорт уронил бы проверку.
import { useEffect, useRef } from 'react'
import './avatar-stage.css'

export default function AvatarStage({ state = 'idle', backdropUrl = null, audioRef = null }) {
	const mount = useRef(null)
	const fallback = useRef(null)
	const stage = useRef(null)

	useEffect(() => {
		let disposed = false
		import('./avatarScene.js')
			.then(({ createAvatarScene }) => {
				if (disposed || !mount.current) return
				stage.current = createAvatarScene(mount.current, {
					modelUrl: '/avatar/echpochmak-face-web.glb',
					backdropUrl,
					fallbackEl: fallback.current,
				})
				stage.current.setState(state)
				stage.current.setAudio(audioRef?.current)
			})
			.catch((error) => console.warn('avatar stage unavailable:', error))
		return () => {
			disposed = true
			stage.current?.dispose()
			stage.current = null
		}
		// Состояние приезжает отдельным эффектом: сцену пересоздавать нельзя.
		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [backdropUrl])

	useEffect(() => {
		stage.current?.setState(state)
	}, [state])

	return (
		// data-state не виден ученику, но делает поведение наблюдаемым:
		// без него «аватар замолчал, когда кончился звук» нечем проверить.
		<div className='avatar-stage' ref={mount} data-state={state} aria-label='Аватар'>
			<div className='avatar-stage-backdrop' data-avatar-backdrop aria-hidden='true' />
			{/* Видна, пока сцена не готова, и остаётся насовсем при отказе WebGL. */}
			<img className='avatar-stage-fallback' ref={fallback} src='/avatar/wave.png' alt='Аватар' />
			<div className='avatar-stage-shadow' aria-hidden='true' />
		</div>
	)
}
