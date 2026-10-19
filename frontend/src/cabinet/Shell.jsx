// Shell.jsx — каркасы экранов кабинета.
//
// Два каркаса: с навигацией (кабинет) и без неё (вход, регистрация).
// Разделение из макета: на экранах входа сайдбар не нужен, а показывать
// пустой — значит обещать разделы, которых у неаутентифицированного нет.

import { useCallback, useEffect, useState } from 'react'
import { Link, NavLink, useLocation } from 'react-router-dom'
import { classes as classesApi } from '../api/v1'
import { useAuth } from './AuthContext'
import Icon from './icons'
import { Button, useClassesChanged, useToast } from './ui'
import { APP_LANGUAGE_BRAND } from '../branding'
import logo from '../assets/logo.js'

// Отдельного пункта для разговора с аватаром нет: разговор — часть урока,
// а не отдельная сущность.
const NAV = [
	{ to: '/app', label: 'Главная', icon: 'home', end: true },
	{ to: '/app/library', label: 'Библиотека уроков', icon: 'book' },
	{ to: '/app/profile', label: 'Профиль', icon: 'user' },
]

export function Wordmark({ language = APP_LANGUAGE_BRAND + '.' }) {
	return (
		<div className='hdr-l'>
			<img className='logo' src={logo} alt='На родном' />
			<div className='wordmark'>
				На родном. <span>{language}</span>
			</div>
		</div>
	)
}

/** AuthShell — экран без кабинета: вход, регистрация, письмо. */
export function AuthShell({ children, className = '' }) {
	return (
		<div className='nr'>
			<div className='shell'>
				<header className='hdr'>
					<Wordmark />
				</header>
				<div className={['auth-wrap', className].filter(Boolean).join(' ')}>{children}</div>
			</div>
		</div>
	)
}

/**
 * PlainShell — страница во всю ширину, без сайдбара кабинета.
 *
 * Нужен предпросмотру: учитель смотрит урок глазами ученика, и список классов
 * сбоку в этот момент мешает — ученик его не увидит. От AuthShell отличается
 * тем, что содержимое не сжимается в карточку входа: урок длинный.
 *
 * `action` — правая часть шапки: на предпросмотре там возврат к редактированию.
 */
export function PlainShell({ children, action }) {
	return (
		<div className='nr'>
			<div className='shell'>
				<header className='hdr'>
					<Wordmark />
					{action && <div className='hdr-r'>{action}</div>}
				</header>
				<main className='main' style={{ maxWidth: 820, margin: '0 auto', width: '100%' }}>
					{children}
				</main>
			</div>
		</div>
	)
}

/**
 * Shell — экран кабинета: шапка, сайдбар со списком классов, контент.
 *
 * Список классов в сайдбаре грузится здесь, а не в каждой странице: он виден
 * на всех экранах кабинета, и перезапрашивать его при каждом переходе значит
 * моргать сайдбаром на ровном месте.
 */
export function Shell({ children, activeClassId }) {
	const { actor, logout } = useAuth()
	const toast = useToast()
	const location = useLocation()
	// null — «ещё не спрашивали». Пустой массив значит «сервер ответил, и
	// классов правда нет»: разница видна учителю, у которого классы есть, —
	// иначе на каждой перезагрузке он читает, что потерял работу.
	const [classes, setClasses] = useState(null)
	const [drawer, setDrawer] = useState(false)

	const loadClasses = useCallback(() => {
		classesApi
			.list()
			.then((res) => setClasses(res.classes || []))
			.catch(() => {
				// Сайдбар без списка классов работать не мешает: навигация
				// и контент экрана на месте. Молчим, чтобы не пугать тостом
				// на каждом переходе. Пустой список здесь честнее заглушек:
				// повторять попытку нам нечем.
				setClasses([])
			})
	}, [])

	useEffect(() => {
		loadClasses()
	}, [loadClasses, location.key])

	// Состав класса меняют экраны внутри Shell — счётчик в меню обязан
	// меняться вместе с их таблицей, а не после перезагрузки страницы.
	useClassesChanged(loadClasses)

	// Меню на телефоне открывается атрибутом на body — так же, как в макете.
	// Держать это в CSS, а не в JS, значит не дублировать точку перелома
	// вёрстки в двух местах.
	useEffect(() => {
		document.body.dataset.drawer = drawer ? 'open' : 'closed'
		return () => {
			document.body.dataset.drawer = 'closed'
		}
	}, [drawer])

	// Переход по ссылке закрывает меню — иначе оно остаётся поверх нового экрана.
	useEffect(() => setDrawer(false), [location.pathname])

	return (
		<div className='nr'>
			<div className='shell'>
				{actor?.impersonated && (
					<div className='impersonation-banner'>
						Сессия просмотра: вы работаете от имени другого пользователя
						{actor.readOnly ? ' в режиме только чтения' : ''}. Всё, что вы открываете, записывается в
						журнал.
					</div>
				)}
				<header className='hdr'>
					<div className='hdr-l'>
						<button className='burger' aria-label='Меню' onClick={() => setDrawer(true)} type='button'>
							<Icon name='menu' size={20} />
						</button>
						<img className='logo' src={logo} alt='На родном' />
						<div className='wordmark'>
							На родном. <span>{APP_LANGUAGE_BRAND}.</span>
						</div>
					</div>
					<div className='hdr-r'>
						<span className='chip-user'>{actor?.display_name}</span>
						<Button
							icon='exit'
							onClick={async () => {
								await logout()
								toast('Вы вышли из аккаунта')
							}}
						>
							Выйти
						</Button>
					</div>
				</header>

				<div className='shell-body'>
					<aside className='side'>
						<Button variant='primary' icon='plus' block to='/app/class/new'>
							Создать класс
						</Button>

						<div className='side-group'>
							<div className='side-label'>Мои классы</div>
							<div className='side-items'>
								{/* До ответа сервера — заглушки строк. «Классов пока нет»
								    рядом с «Загружаем…» в основной области — два
								    противоречащих утверждения на одном экране. */}
								{classes === null &&
									[0, 1, 2].map((i) => <span className='skel' key={i} aria-hidden='true' />)}
								{classes?.length === 0 && <div className='tiny'>Классов пока нет</div>}
								{(classes || []).map((c) => (
									<Link
										key={c.id}
										className='classrow'
										to={`/app/class/${c.id}/lessons`}
										aria-current={c.id === activeClassId}
									>
										<span className='classrow-t'>
											<span className='classrow-n'>{c.name}</span>
											<span className='classrow-s'>{c.subject}</span>
										</span>
										<span className='classrow-c'>{c.students}</span>
									</Link>
								))}
							</div>
						</div>

						<div className='hair' />
						<div className='side-items'>
							{/* aria-current='true' вместо стандартного 'page': именно это
							    значение подсвечивает пункт в cabinet.css. */}
							{NAV.map((n) => (
								<NavLink key={n.to} to={n.to} end={n.end} className='navitem' aria-current='true'>
									<Icon name={n.icon} />
									<span>{n.label}</span>
								</NavLink>
							))}
						</div>
					</aside>

					<button className='scrim' aria-label='Закрыть меню' onClick={() => setDrawer(false)} />

					<main className='main'>
						{children}
					</main>
				</div>
			</div>
		</div>
	)
}
