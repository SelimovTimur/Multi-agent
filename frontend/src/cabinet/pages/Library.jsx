// Library.jsx — библиотека готовых уроков.
//
// Уроки приходят готовыми при первом запуске (cmd/seed): здесь учитель
// находит урок и назначает его классу. Дальше урок живёт на вкладке «Уроки»
// класса — там задания выдаются ученикам.

import { useCallback, useEffect, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { lessons as lessonsApi } from '../../api/v1'
import { Shell } from '../Shell'
import { Button, Table, shortDate, useToast } from '../ui'
import { CEFR_LEVELS } from '../levels'
import { AssignDialog } from './LibraryDialogs'

export default function Library() {
	const toast = useToast()
	const [items, setItems] = useState(null)
	const [filters, setFilters] = useState({ q: '', level: '' })
	// Урок, для которого открыта форма назначения; null — форма закрыта.
	const [assigning, setAssigning] = useState(null)
	// ?assignTo=<класс> — приход со вкладки «Уроки» класса: там нажали
	// «Выдать урок», и класс в форме уже выбран.
	const [params, setParams] = useSearchParams()
	const assignTo = params.get('assignTo') || ''

	const load = useCallback(() => {
		lessonsApi
			.list({ ...filters, status: 'published' })
			.then((res) => setItems(res.lessons || []))
			.catch((err) => {
				setItems([])
				toast(err.message)
			})
	}, [filters, toast])

	useEffect(load, [load])

	const set = (key) => (e) => setFilters((f) => ({ ...f, [key]: e.target.value }))

	const rowActions = (l) => (
		<span className='row-icons'>
			<Button className='btn-compact' icon='users' onClick={() => setAssigning(l)}>
				Назначить классу
			</Button>
		</span>
	)

	// Пустая выдача называет то, что её сузило, и даёт снять именно это.
	const narrowed = [filters.q && `по запросу «${filters.q}»`, filters.level && `уровень ${filters.level}`].filter(
		Boolean,
	)

	const emptyNote = narrowed.length ? (
		<>
			<div>Ничего не нашли: {narrowed.join(', ')}.</div>
			<div className='row mt16' style={{ justifyContent: 'center' }}>
				<Button onClick={() => setFilters({ q: '', level: '' })}>Сбросить фильтры</Button>
			</div>
		</>
	) : (
		'Уроков пока нет. Готовые уроки заводит команда seed при первом запуске.'
	)

	const rows = (items || []).map((l) => {
		const klasses = l.classes || []
		const when = shortDate(l.created_at)

		return {
			key: l.id,
			cells: [
				<Link className='t-name' to={`/app/library/${l.id}`}>
					{l.title}
				</Link>,
				<span className='badge badge-lvl'>{l.level}</span>,
				<span className='tiny'>{when}</span>,
				klasses.length ? (
					<span className='row'>
						{klasses.map((c) => (
							<span className='badge badge-green' key={c.id}>
								{c.name}
							</span>
						))}
					</span>
				) : (
					<span className='tiny'>не назначен</span>
				),
				<span className='t-actions'>{rowActions(l)}</span>,
			],
			mobile: {
				title: l.title,
				badge: <span className='badge badge-lvl'>{l.level}</span>,
				kv: [
					['Добавлен', when],
					['Классы', klasses.length ? klasses.map((c) => c.name).join(', ') : '—'],
				],
				actions: rowActions(l),
			},
		}
	})

	return (
		<Shell>
			<h1 className='h1'>Библиотека уроков</h1>

			<div className='filters mt22'>
				<div className='field'>
					<label htmlFor='f-level'>Уровень</label>
					<select className='input select' id='f-level' value={filters.level} onChange={set('level')}>
						<option value=''>Любой</option>
						{CEFR_LEVELS.map((l) => (
							<option key={l} value={l}>
								{l}
							</option>
						))}
					</select>
				</div>
				<div className='field grow'>
					<label htmlFor='f-q'>Поиск по названию</label>
					<input
						className='input'
						id='f-q'
						value={filters.q}
						onChange={set('q')}
						placeholder='Например, weekend'
					/>
				</div>
			</div>

			<div className='mt16'>
				{items === null ? (
					<div className='tiny'>Загружаем…</div>
				) : (
					<Table head={['Название', 'Уровень', 'Добавлен', 'Назначен в классы', '']} rows={rows} empty={emptyNote} />
				)}
			</div>

			{assigning && (
				<AssignDialog
					lessonId={assigning.id}
					presetClassId={assignTo}
					onClose={() => setAssigning(null)}
					onDone={(msg) => {
						setAssigning(null)
						toast(msg)
						// Класс из адреса больше не нужен: следующая выдача
						// не должна молча уйти в тот же класс.
						if (assignTo) setParams({}, { replace: true })
						load()
					}}
				/>
			)}
		</Shell>
	)
}
