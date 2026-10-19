// ClassAnalytics.jsx — вкладка «Аналитика» класса (#65).
//
// Главное в аналитике — результаты учеников. Поэтому экран — одна строка
// сводки и таблица «ученики × уроки»: строки — ученики, столбцы — уроки от
// нового к старому, в клетке — доля верных в заданиях урока или, в режиме
// «Разговор с аватаром», сколько целей разговора закрыто. Справа — среднее
// ученика, внизу — среднее урока.
//
// Клик по клетке открывает панель с ответами ученика и его разговором —
// теми же компонентами, что в карточке ученика. Клик по имени ведёт в
// карточку, по названию урока — на страницу урока (LessonAnalytics.jsx).
//
// Таблица приходит одним ответом сервера: средние считаются из тех же клеток,
// и по запросу на клетку они бы разошлись. Ответы дочитываются только при
// открытии клетки той же ручкой, что в карточке.
//
// Под таблицей — «Что повторить на следующем уроке» (HardWords.jsx, #67).
// Трудные слова грузятся своим запросом и не держат таблицу: считать их
// дольше, а результаты учеников — главное на экране.

import { useCallback, useEffect, useRef, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { assignments as assignmentsApi, classes as classesApi } from '../../api/v1'
import ClassHeader from '../ClassHeader'
import { Shell } from '../Shell'
import Icon from '../icons'
import { Button, pluralWord, shortDate, useToast } from '../ui'
import HardWords from './HardWords'
import { LessonWork } from './StudentCard'
import { averageOf, cellView, scoreTone, share } from './results'
import '../class-lesson.css'

export default function ClassAnalytics() {
	const { classId } = useParams()
	const navigate = useNavigate()
	const toast = useToast()

	const [klass, setKlass] = useState(null)
	const [data, setData] = useState(null)
	// В MVP уроки без упражнений: главное в аналитике — разговор с аватаром.
	const [mode, setMode] = useState('conversation')
	// Открытая клетка: { student, lesson } — индексы строки и столбца.
	const [active, setActive] = useState(null)
	// Трудные слова: undefined — грузятся, { error } — не удалось.
	const [words, setWords] = useState(undefined)

	const load = useCallback(async () => {
		try {
			// Класс приходит конвертом {class: {...}} — как и на соседних вкладках.
			const [c, a] = await Promise.all([classesApi.get(classId), classesApi.analytics(classId)])
			setKlass(c.class)
			setData(a)
		} catch (err) {
			// Без класса рисовать нечего: и шапка, и вкладки, и таблица — про
			// него. Полупустой экран хуже честного возврата к списку классов.
			toast(err.message)
			navigate('/app', { replace: true })
		}
	}, [classId, navigate, toast])

	useEffect(() => {
		load()
	}, [load])

	useEffect(() => {
		let gone = false
		setWords(undefined)
		classesApi
			.hardWords(classId)
			.then((r) => !gone && setWords(r.words || []))
			.catch((err) => !gone && setWords({ error: err.message }))
		return () => {
			gone = true
		}
	}, [classId])

	if (!klass || !data) {
		return (
			<Shell activeClassId={classId}>
				<div className='tiny'>Загружаем…</div>
			</Shell>
		)
	}

	const { assigned_count = 0, total_students = 0 } = data

	// Пустой класс: считать нечего, и таблица из «не пройден» соврёт — учитель
	// прочитает её как «всё провалено», хотя никто ещё ничего не открывал.
	// Одно объяснение и действие, которое эту пустоту закрывает, полезнее.
	if (!total_students || !assigned_count) {
		const noStudents = !total_students
		return (
			<Shell activeClassId={classId}>
				<ClassHeader klass={klass} active='analytics' />
				<div className='card card-white mt22' style={{ maxWidth: 560 }}>
					<div className='h3'>Считать пока нечего</div>
					<p className='lead mt8'>
						{noStudents
							? 'В классе пока нет учеников. Добавьте их на вкладке «Ученики» и раздайте карточки с кодами — после первого урока здесь будут результаты каждого.'
							: 'Классу ещё не выдали ни одного урока. Выдайте — и здесь появится таблица: кто прошёл урок и сколько ответов верных.'}
					</p>
					{!noStudents && (
						<div className='mt16'>
							<Button variant='primary' icon='plus' to={`/app/library?assignTo=${classId}`}>
								Выдать урок
							</Button>
						</div>
					)}
				</div>
			</Shell>
		)
	}

	const student = active && data.students[active.student]
	const lesson = active && data.lessons[active.lesson]

	return (
		<Shell activeClassId={classId}>
			<ClassHeader klass={klass} active='analytics' />

			<ResultsSummary classId={classId} data={data} />

			<div className='row-between mt22'>
				<h2 className='h2'>Результаты</h2>
				<ModeSwitch mode={mode} onChange={setMode} />
			</div>
			<p className='tiny mt8'>
				{mode === 'conversation'
					? 'Сколько целей разговора ученик закрыл.'
					: 'Доля верных ответов в заданиях урока.'}{' '}
				Нажмите на клетку, чтобы увидеть ответы ученика, на название урока — чтобы открыть его разбор.
			</p>
			<ResultsTable
				classId={classId}
				data={data}
				mode={mode}
				active={active}
				onCell={(s, l) =>
					setActive((cur) => (cur?.student === s && cur?.lesson === l ? null : { student: s, lesson: l }))
				}
			/>

			{words === undefined ? (
				<p className='tiny mt28'>Ищем слова, которые стоит повторить…</p>
			) : words.error ? (
				<p className='tiny mt28'>Не удалось посчитать, что повторить: {words.error}</p>
			) : (
				<HardWords classId={classId} lessons={data.lessons || []} words={words} />
			)}

			{student && lesson && (
				<ResultPanel
					classId={classId}
					student={student}
					lesson={lesson}
					cell={student.cells[active.lesson]}
					mode={mode}
					onClose={() => setActive(null)}
				/>
			)}
		</Shell>
	)
}

/**
 * Строка сводки над таблицей: сколько прохождений из ожидаемых и кто неделю
 * не работал с уроками. Отдельно от загрузки: её же на демонстрационных
 * данных показывает приветственное окно учителя.
 */
export function ResultsSummary({ classId, data }) {
	const { passed_count = 0, expected_count = 0 } = data
	const inactive = data.inactive || []
	return (
		<p className='sc-summary mt22'>
			Прошли{' '}
			<b>
				{passed_count} из {expected_count}
			</b>{' '}
			{pluralWord(expected_count, 'заданного урока', 'заданных уроков', 'заданных уроков')}.
			{inactive.length > 0 && (
				<>
					{' '}
					{inactive.length > 1 ? 'Неделю не заходили' : 'Неделю не заходил'}:{' '}
					{inactive.map((s, i) => (
						<span key={s.student_id}>
							{i > 0 && ', '}
							<Link to={`/app/class/${classId}/student/${s.student_id}`}>{s.display_name}</Link>
						</span>
					))}
					.
				</>
			)}
		</p>
	)
}

/** Переключатель «Задания / Разговор с аватаром». */
export function ModeSwitch({ mode, onChange }) {
	return (
		<div className='chips' role='group' aria-label='Что показывать в таблице'>
			<button className='chip' type='button' aria-pressed={mode === 'tasks'} onClick={() => onChange('tasks')}>
				Задания
			</button>
			<button
				className='chip'
				type='button'
				aria-pressed={mode === 'conversation'}
				onClick={() => onChange('conversation')}
			>
				Разговор с аватаром
			</button>
		</div>
	)
}

/**
 * Таблица «ученики × уроки».
 *
 * Своя разметка, а не общий Table: у неё итоговая строка и клетки-кнопки,
 * а на телефоне она прокручивается вбок с закреплённым именем — карточки
 * по ученику потеряли бы главное, сравнение по столбцу.
 */
export function ResultsTable({ classId, data, mode, active, onCell }) {
	const lessons = data.lessons || []
	const students = data.students || []
	return (
		<div className='ra-wrap mt12'>
			<table className='ra-table'>
				<thead>
					<tr>
						<th>Ученик</th>
						{lessons.map((l) => (
							<th key={l.assignment_id}>
								<Link className='ra-lesson' to={`/app/class/${classId}/analytics/lesson/${l.assignment_id}`}>
									{l.title || 'Урок'}
								</Link>
								<div className='tiny'>{shortDate(l.assigned_at)}</div>
							</th>
						))}
						<th>В среднем</th>
					</tr>
				</thead>
				<tbody>
					{students.map((s, si) => (
						<tr key={s.student_id}>
							<th scope='row'>
								<Link className='t-name' to={`/app/class/${classId}/student/${s.student_id}`}>
									{s.display_name}
								</Link>{' '}
								<span className='tiny'>{s.public_tag}</span>
							</th>
							{lessons.map((l, li) => {
								const v = cellView(s.cells?.[li], mode)
								const on = active?.student === si && active?.lesson === li
								return (
									<td key={l.assignment_id} data-active={on || undefined}>
										<button
											className='ra-cell'
											type='button'
											aria-pressed={on}
											aria-label={`${s.display_name}, ${l.title || 'урок'}: ${v.text}`}
											onClick={() => onCell(si, li)}
										>
											{v.tone ? <span className={`badge ${v.tone}`}>{v.text}</span> : <span className='tiny'>{v.text}</span>}
											{v.note && <span className='tiny'>{v.note}</span>}
										</button>
									</td>
								)
							})}
							<td className='ra-avg'>
								<b>{averageOf(s, mode)}</b>
							</td>
						</tr>
					))}
				</tbody>
				<tfoot>
					<tr>
						<th scope='row'>Класс</th>
						{lessons.map((l) => (
							<td key={l.assignment_id} className='ra-avg'>
								{averageOf(l, mode)}
							</td>
						))}
						<td />
					</tr>
				</tfoot>
			</table>
		</div>
	)
}

/**
 * ResultPanel — что ученик сделал в уроке: письменные ответы и разговор.
 *
 * Без затемнения, как панель ученика на экране «Урок в классе»: таблица под
 * ней живая, и соседнюю клетку открывают прямо из неё.
 */
function ResultPanel({ classId, student, lesson, cell, mode, onClose }) {
	const box = useRef(null)
	const [answers, setAnswers] = useState(undefined)
	const [onlyErrors, setOnlyErrors] = useState(false)
	// Открыли клетку в режиме разговора — панель сразу на разговоре.
	const [tab, setTab] = useState(mode === 'conversation' ? 'conversation' : 'tasks')

	const assignmentId = lesson.assignment_id
	const studentId = student.student_id
	const started = Boolean(cell?.started)

	useEffect(() => {
		box.current?.focus()
	}, [assignmentId, studentId])

	useEffect(() => {
		setTab(mode === 'conversation' ? 'conversation' : 'tasks')
	}, [mode, assignmentId, studentId])

	useEffect(() => {
		const onKey = (e) => e.key === 'Escape' && onClose()
		document.addEventListener('keydown', onKey)
		return () => document.removeEventListener('keydown', onKey)
	}, [onClose])

	useEffect(() => {
		setAnswers(undefined)
		if (!started) return undefined
		let gone = false
		assignmentsApi
			.student(assignmentId, studentId)
			.then((data) => !gone && setAnswers({ data }))
			.catch((err) => !gone && setAnswers({ error: err.message }))
		return () => {
			gone = true
		}
	}, [assignmentId, studentId, started])

	const conv = cell?.conversation
	const pct = cell?.correct_percent

	return (
		<aside className='cl-drawer ra-drawer' ref={box} tabIndex={-1} role='dialog' aria-label={student.display_name}>
			<div className='cl-drawer-hd'>
				<div>
					<div className='tiny'>{lesson.title || 'Урок'}</div>
					<h2 className='h2 mt8'>
						{student.display_name} <span className='tiny'>{student.public_tag}</span>
					</h2>
				</div>
				<button className='modal-x' type='button' aria-label='Закрыть' onClick={onClose}>
					<Icon name='x' size={20} />
				</button>
			</div>

			{!started ? (
				<p className='lead mt16'>Урок ещё не пройден: ученик его не открывал.</p>
			) : (
				<>
					<div className='row mt12'>
						{cell.total_questions > 0 && (
							<span className={`badge ${scoreTone(pct)}`}>
								Задания: {cell.correct_answers} из {cell.total_questions} верно
							</span>
						)}
						{!cell.completed && <span className='badge badge-gray'>Не завершён</span>}
						{!conv ? (
							<span className='badge badge-gray'>Разговора не было</span>
						) : conv.ready ? (
							<span className={`badge ${scoreTone(share(conv.credited, conv.goals))}`}>
								Разговор: {conv.credited} из {conv.goals}{' '}
								{pluralWord(conv.goals, 'цели', 'целей', 'целей')}
							</span>
						) : (
							<span className='badge badge-gray'>Разговор: отчёт готовится</span>
						)}
					</div>
					<LessonWork
						assignmentId={assignmentId}
						studentId={studentId}
						conversation={conv}
						answers={answers}
						onlyErrors={onlyErrors}
						setOnlyErrors={setOnlyErrors}
						tab={tab}
						setTab={setTab}
					/>
				</>
			)}

			<div className='mt22'>
				<Link to={`/app/class/${classId}/student/${studentId}`}>Карточка ученика →</Link>
			</div>
		</aside>
	)
}
