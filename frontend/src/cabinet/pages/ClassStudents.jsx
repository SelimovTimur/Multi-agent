// ClassStudents.jsx — вкладка «Ученики» (NR-21).
//
// Учеников заводят списком: сами в класс они не входят. Здесь учитель
// добавляет учеников, видит их личные коды, печатает карточки, выдаёт новый
// код вместо потерянного и исключает лишнего.
//
// Код виден учителю всегда, а не один раз: карточку раздаёт он, и когда
// ребёнок её теряет, восстанавливать доступ тоже ему — прямо на уроке.
//
// Шапка класса с вкладками живёт в ClassHeader — одном компоненте на все
// три экрана класса; второй копии разметки здесь быть не должно.
//
// Исключение мягкое: доступ закрывается, ответы сохраняются. Поэтому
// рядом есть переключатель «показать исключённых» и возврат в класс —
// исключить по ошибке можно, потерять историю ученика нельзя.
//
// Успеваемость здесь не показывается: вкладка — только состав класса.
// Результаты учеников живут в «Аналитике» и в карточке ученика.

import { useCallback, useEffect, useMemo, useState } from 'react'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { ApiError, classes as classesApi } from '../../api/v1'
import ClassHeader from '../ClassHeader'
import Icon from '../icons'
import { Shell } from '../Shell'
import { Button, Field, Kebab, Modal, Table, Toggle, classesChanged, lastSeen, shortDate, useToast } from '../ui'
import { parseNames } from './student-names'

export default function ClassStudents() {
	const { classId } = useParams()
	const navigate = useNavigate()
	const toast = useToast()
	const [params, setParams] = useSearchParams()

	const [klass, setKlass] = useState(null)
	const [students, setStudents] = useState([])
	const [showRemoved, setShowRemoved] = useState(false)
	const [query, setQuery] = useState('')
	// Окно добавления открыто сразу после создания класса (?add=1): новый
	// класс пуст, и первое, что с ним делают, — заводят учеников.
	const [adding, setAdding] = useState(() => params.get('add') === '1')
	// Ученик, которому учитель собирается выдать новый код.
	const [reissuing, setReissuing] = useState(null)

	// Параметр одноразовый: после обновления страницы окно не должно
	// всплывать снова.
	useEffect(() => {
		if (params.get('add') === '1') setParams({}, { replace: true })
	}, [params, setParams])

	const load = useCallback(async () => {
		try {
			const [c, s] = await Promise.all([
				classesApi.get(classId),
				classesApi.students(classId, { removed: showRemoved }),
			])
			setKlass(c.class)
			setStudents(s.students || [])
		} catch (err) {
			toast(err.message)
			navigate('/app', { replace: true })
		}
	}, [classId, showRemoved, navigate, toast])

	useEffect(() => {
		load()
	}, [load])

	// Поиск по уже загруженному списку: класс — это десятки строк, а не тысячи,
	// и гонять за каждым нажатием клавиши запрос на сервер незачем.
	const visible = useMemo(() => {
		const q = query.trim().toLowerCase()
		if (!q) return students
		return students.filter((s) => `${s.name} ${s.tag} ${s.code}`.toLowerCase().includes(q))
	}, [students, query])

	if (!klass) return <Shell activeClassId={classId}><div className='tiny'>Загружаем…</div></Shell>

	async function remove(student) {
		try {
			await classesApi.removeStudent(classId, student.student_id)
			toast(`${student.name} больше не в классе`)
			load()
			// Счётчик класса в левом меню обязан измениться вместе с таблицей:
			// неизменившееся число читается как «исключение не сработало».
			classesChanged()
		} catch (err) {
			toast(err.message)
		}
	}

	async function restore(student) {
		try {
			await classesApi.restoreStudent(classId, student.student_id)
			toast(`${student.name} снова в классе`)
			load()
			classesChanged()
		} catch (err) {
			toast(err.message)
		}
	}

	const rows = visible.map((s) => {
		const removed = s.status === 'removed'
		const actions = removed
			? [{ label: 'Вернуть в класс', run: () => restore(s) }]
			: [
					{ label: 'Выдать новый код', run: () => setReissuing(s) },
					{ label: 'Исключить из класса', danger: true, run: () => remove(s) },
				]

		// Карточка ученика — единственное место, где учитель видит его
		// результаты: состав класса их больше не показывает.
		const card = `/app/class/${classId}/student/${s.student_id}`

		return {
			key: s.student_id,
			cells: [
				<>
					<Link className='t-name' to={card}>
						{s.name}
					</Link>{' '}
					<span className='tiny'>{s.tag}</span>
					{removed && <span className='badge badge-gray' style={{ marginLeft: 8 }}>не в классе</span>}
				</>,
				<span className='student-code'>{s.code}</span>,
				<span className='tiny'>{lastSeen(s.last_seen_at)}</span>,
				<span className='t-actions'>
					<Button to={card}>Карточка</Button>
					<Kebab items={actions} />
				</span>,
			],
			mobile: {
				title: (
					<>
						{s.name} <span className='tiny'>{s.tag}</span>
					</>
				),
				kv: [
					['Личный код', <span className='student-code'>{s.code}</span>],
					['Активность', lastSeen(s.last_seen_at)],
					['В классе с', shortDate(s.joined_at)],
				],
				// На телефоне строка не «кликается» глазами, а имя там обычный
				// текст — без этой кнопки карточку ученика открыть нечем.
				actions: (
					<>
						<Button to={card}>Карточка</Button>
						<Kebab items={actions} />
					</>
				),
			},
		}
	})

	return (
		<Shell activeClassId={classId}>
			<ClassHeader klass={klass} active='students' />

			<div className='row mt22'>
				<Button variant='primary' icon='plus' onClick={() => setAdding(true)}>
					Добавить учеников
				</Button>
				{/* Карточки печатаются со своей страницы без меню кабинета:
				    на листе должны быть только сами карточки. */}
				<Button icon='printer' to={`/app/class/${classId}/cards`} target='_blank' rel='noopener'>
					Карточки с кодами
				</Button>
			</div>

			<div className='filters mt22'>
				<div className='field grow'>
					<label htmlFor='f-search'>Поиск по имени</label>
					<input
						className='input'
						id='f-search'
						value={query}
						onChange={(e) => setQuery(e.target.value)}
						placeholder='Например, Айгуль'
					/>
				</div>
				<Toggle on={showRemoved} onClick={() => setShowRemoved((v) => !v)} label='Показать исключённых' />
			</div>

			<div className='mt16'>
				<Table
					head={['Ученик', 'Личный код', 'Последняя активность', '']}
					rows={rows}
					empty={
						query
							? 'Никого не нашли. Попробуйте другое имя.'
							: 'Учеников пока нет. Добавьте их списком — каждому выдастся личный код для входа.'
					}
				/>
			</div>

			{adding && (
				<AddStudents
					classId={classId}
					onClose={() => setAdding(false)}
					onDone={() => {
						setAdding(false)
						load()
						classesChanged()
					}}
				/>
			)}
			{reissuing && (
				<ReissueCode
					classId={classId}
					student={reissuing}
					onClose={() => setReissuing(null)}
					onDone={() => {
						setReissuing(null)
						load()
					}}
				/>
			)}
		</Shell>
	)
}

/**
 * AddStudents — окно «Добавить учеников»: список имён, по одному в строке.
 *
 * Списки школа присылает в Word и Excel, поэтому вставка столбца, номера
 * строк и лишние пробелы должны просто работать (parseNames). Имена, которые
 * в классе уже есть, сервер пропускает, и мы об этом говорим: молча
 * проглоченная строка выглядит как потерянный ученик.
 */
function AddStudents({ classId, onClose, onDone }) {
	const toast = useToast()
	const [text, setText] = useState('')
	const [error, setError] = useState(null)
	const [busy, setBusy] = useState(false)
	const names = useMemo(() => parseNames(text), [text])

	async function submit() {
		setError(null)
		if (names.length === 0) {
			setError('Впишите хотя бы одно имя')
			return
		}
		setBusy(true)
		try {
			const res = await classesApi.addStudents(classId, names)
			const added = res.added?.length || 0
			const skipped = res.skipped || []
			toast(
				skipped.length
					? `Добавлено: ${added}. Уже были в классе: ${skipped.join(', ')}`
					: `Добавлено учеников: ${added}`,
			)
			onDone()
		} catch (err) {
			setError(err instanceof ApiError && err.isValidation ? err.fields?.names || err.message : err.message)
			setBusy(false)
		}
	}

	return (
		<Modal
			title='Добавить учеников'
			sub='Каждому ученику выдастся личный код — по нему он войдёт без логина и пароля.'
			onClose={onClose}
			footer={
				<>
					<Button onClick={onClose}>Отмена</Button>
					<Button variant='primary' onClick={submit} disabled={busy || names.length === 0}>
						{busy ? 'Добавляем…' : names.length ? `Добавить: ${names.length}` : 'Добавить'}
					</Button>
				</>
			}
		>
			<Field
				label='Ученики'
				htmlFor='f-names'
				hint='По одному ученику в строке. Можно вставить столбец из Excel или Word.'
				error={error}
			>
				<textarea
					className='textarea'
					id='f-names'
					rows={10}
					value={text}
					onChange={(e) => setText(e.target.value)}
					placeholder={'Абдуллина Азалия\nГарипова Инзиля'}
					autoFocus
				/>
			</Field>
		</Modal>
	)
}

/**
 * ReissueCode — подтверждение нового кода.
 *
 * Отдельное окно, а не пункт меню без вопросов: старый код перестаёт
 * работать, а ученик выходит со всех устройств — на середине урока это
 * заметно, и случайный клик не должен этого делать.
 */
function ReissueCode({ classId, student, onClose, onDone }) {
	const toast = useToast()
	const [busy, setBusy] = useState(false)

	async function confirm() {
		setBusy(true)
		try {
			const res = await classesApi.reissueCode(classId, student.student_id)
			toast(`Новый код ${student.name}: ${res.student.code}`)
			onDone()
		} catch (err) {
			toast(err.message)
			setBusy(false)
		}
	}

	return (
		<Modal
			title={`Выдать новый код: ${student.name}?`}
			onClose={onClose}
			footer={
				<>
					<Button onClick={onClose}>Отмена</Button>
					<Button variant='primary' onClick={confirm} disabled={busy}>
						Выдать новый код
					</Button>
				</>
			}
		>
			<div className='row'>
				<Icon name='warn' size={16} />
				<p className='small'>
					Старый код {student.code} перестанет работать, а ученик выйдет на всех устройствах. Ответы и
					прогресс сохранятся. Новый код появится в таблице — перепишите его на карточку.
				</p>
			</div>
		</Modal>
	)
}
