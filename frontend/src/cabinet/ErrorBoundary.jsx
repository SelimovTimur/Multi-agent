// ErrorBoundary.jsx — экран вместо белого листа, когда интерфейс упал.
//
// Без него любое исключение при отрисовке в React 18 убирает всё дерево:
// ученик посреди урока видит пустую страницу без единой кнопки. Здесь сбой
// называется словами и есть «Обновить».
//
// Часть приложения после выката может не загрузиться: вкладка, открытая до
// выката, просит старый файл, которого на сервере уже нет. Такая страница
// один раз перезагружается сама.

import { Component } from 'react'

const RELOADED = 'nr-chunk-reloaded'

function isChunkError(error) {
	const text = String(error?.message || error || '')
	return /dynamically imported module|Importing a module script failed|Loading chunk|ChunkLoadError/i.test(text)
}

function storage() {
	try {
		return window.sessionStorage
	} catch {
		return null
	}
}

export default class ErrorBoundary extends Component {
	constructor(props) {
		super(props)
		this.state = { failed: false }
	}

	static getDerivedStateFromError() {
		return { failed: true }
	}

	componentDidCatch(error) {
		const chunk = isChunkError(error)
		// Отметка со временем, а не флаг: после следующего выката та же вкладка
		// снова перезагрузится, а зациклиться, если файл и правда пропал, не даст.
		const last = Number(storage()?.getItem(RELOADED)) || 0
		if (chunk && Date.now() - last > 60000) {
			storage()?.setItem(RELOADED, String(Date.now()))
			window.location.reload()
		}
	}

	render() {
		if (!this.state.failed) return this.props.children
		return (
			<div className='nr nr-loading'>
				<div style={{ maxWidth: 420, textAlign: 'center', padding: 24 }}>
					<div className='h3' style={{ color: 'var(--ink)' }}>
						Что-то пошло не так
					</div>
					<p className='lead mt8'>
						Экран не открылся. Обновите страницу — ответы и прогресс сохранены на сервере.
					</p>
					<div className='row mt16' style={{ justifyContent: 'center' }}>
						<button className='btn btn-primary' type='button' onClick={() => window.location.reload()}>
							Обновить страницу
						</button>
						<a className='btn btn-outline' href='/'>
							На главную
						</a>
					</div>
				</div>
			</div>
		)
	}
}
