// Home.jsx — главная кабинета: мои классы (NR-20).
//
// Список классов — точка входа во всё остальное, поэтому он первый экран
// после входа. Пустое состояние ведёт к созданию класса, а не показывает
// пустоту: у нового учителя классов нет по определению.

import { useCallback, useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { classes as classesApi, lessons as lessonsApi } from '../../api/v1'
import { Shell } from '../Shell'
import Icon from '../icons'
import { Button, langLabel, plural, useToast } from '../ui'

export default function Home() {
	const toast = useToast()
	const [items, setItems] = useState(null)
	const [recent, setRecent] = useState([])

	const load = useCallback(() => {
		classesApi
			.list()
			.then((res) => setItems(res.classes || []))
			.catch((err) => {
				setItems([])
				toast(err.message)
			})
		// Последние уроки рядом с классами: чаще всего учитель заходит не
		// «в класс», а «к тому уроку, что делал вчера».
		lessonsApi
			.list({ limit: 3 })
			.then((res) => setRecent(res.lessons || []))
			.catch(() => setRecent([]))
	}, [toast])

	useEffect(load, [load])

	return (
		<Shell>
			<h1 className='h1'>Мои классы</h1>

			{items === null ? (
				<div className='mt22 tiny'>Загружаем…</div>
			) : (
				<ClassCards items={items} />
			)}

			<div className='mt28'>
				<div className='row-between'>
					<h2 className='h2'>Последние уроки</h2>
					<Button icon='book' to='/app/library'>
						Библиотека уроков
					</Button>
				</div>

				{recent.length === 0 ? (
					<div className='empty mt16'>
						Уроков пока нет.
					</div>
				) : (
					<div className='grid grid-3 mt16'>
						{recent.map((l) => (
							<Link className='card card-pick' key={l.id} to={`/app/library/${l.id}`}>
								<div className='h3'>{l.title}</div>
								<div className='tiny'>
									{l.level}, {langLabel(l.lang)},{' '}
									{new Date(l.created_at).toLocaleDateString('ru-RU')}
								</div>
								{/* Номер редакции и классы — как в макете: «опубликован» без
								    номера не говорит, какую версию видит класс, а без второй
								    строки непонятно, выдан ли урок вообще. Оба поля уже
								    приходят в ответе списка уроков. */}
								<div className='mt8 row'>
									<span className={`badge badge-${l.status === 'published' ? 'green' : 'gray'}`}>
										{l.status === 'published'
											? l.version
												? `версия v${l.version}`
												: 'опубликован'
											: 'черновик'}
									</span>
									<span className='tiny'>
										{(l.classes || []).length
											? `назначен: ${l.classes.map((c) => c.name).join(', ')}`
											: 'не назначен'}
									</span>
								</div>
							</Link>
						))}
					</div>
				)}
			</div>
		</Shell>
	)
}

/** Карточки классов и пунктир «Создать класс». */
export function ClassCards({ items }) {
	return (
		<div className='grid grid-3 mt22'>
			{items.map((c) => (
				<Link className='card classcard card-pick' key={c.id} to={`/app/class/${c.id}/lessons`}>
					<div className='classcard-n'>{c.name}</div>
					<div className='classcard-s'>{c.subject}</div>
					<div className='mt8'>
						{/* Язык названием: в одной карточке «Татарский» и «TATAR»
						    выглядели как две разные вещи, и учитель искал между ними
						    разницу, которой нет. Код остаётся в адресах. */}
						<div className='eyebrow'>
							Уровень {c.level}, {langLabel(c.lang)}
						</div>
					</div>
					<div className='classcard-f'>
						<Icon name='users' />
						<span>{plural(c.students, 'ученик', 'ученика', 'учеников')}</span>
					</div>
				</Link>
			))}

			<Link className='card-dashed' to='/app/class/new'>
				<span className='dot'>
					<Icon name='plus' size={20} />
				</span>
				<span>Создать класс</span>
			</Link>
		</div>
	)
}
