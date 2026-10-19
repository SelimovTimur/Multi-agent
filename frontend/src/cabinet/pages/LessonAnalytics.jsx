// LessonAnalytics.jsx — страница урока в аналитике класса (#66).
//
// Открывается из шапки таблицы результатов. Сверху сводка урока по классу:
// сколько прошли, доля верных в заданиях, сколько целей разговора зачтено —
// те же числа, что в итоговой строке таблицы. Ниже две вкладки:
//
// «Задания» — по каждому письменному заданию доля верных и частые неверные
// ответы; одинаковые склеены с числом («Казанга → Казанда, 6 раз»).
//
// «Трудные слова» (#68) — тот же список, что «Что повторить на следующем
// уроке» на главной аналитики, но только по этому уроку. Сервер считает слово
// внутри урока, так что числа совпадают с главной.
//
// «Разговор с аватаром» — отчёт по классу, тот же компонент, что раньше жил
// на экране «Урок в классе». Что открыто — в адресе (?tab=words,
// ?tab=conversation, &view=themes, &student=<id>): из панели ученика на уроке сюда ведёт
// обычная ссылка, а «Назад» в браузере возвращает к списку.

import { useCallback, useEffect, useState } from 'react'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { assignments as api, classes as classesApi } from '../../api/v1'
import { Shell } from '../Shell'
import { Button, Table, plural, pluralWord, shortDate, useToast } from '../ui'
import ConversationTab from './conversation/ConversationTab'
import { HardWordList } from './HardWords'
import { scoreTone } from './results'
import '../class-lesson.css'

// Сколько ошибок задания видно сразу. Остальные — по кнопке: длинный хвост
// единичных ошибок заслоняет главное, частые.
const MISTAKES_SHOWN = 3

export default function LessonAnalytics() {
	const { classId, assignmentId } = useParams()
	const navigate = useNavigate()
	const toast = useToast()
	const [params, setParams] = useSearchParams()
	const [data, setData] = useState(null)
	// Трудные слова урока: undefined — грузятся, { error } — не удалось.
	const [words, setWords] = useState(undefined)

	const load = useCallback(async () => {
		try {
			setData(await api.analytics(assignmentId))
		} catch (err) {
			// Урок сняли или ссылка чужая — возвращаем к аналитике класса, а не
			// оставляем пустой экран.
			toast(err.message)
			navigate(`/app/class/${classId}/analytics`, { replace: true })
		}
	}, [assignmentId, classId, navigate, toast])

	useEffect(() => {
		load()
	}, [load])

	useEffect(() => {
		let gone = false
		setWords(undefined)
		classesApi
			.hardWords(classId, assignmentId)
			.then((r) => !gone && setWords(r.words || []))
			.catch((err) => !gone && setWords({ error: err.message }))
		return () => {
			gone = true
		}
	}, [classId, assignmentId])

	const analyticsUrl = `/app/class/${classId}/analytics`

	if (!data) {
		return (
			<Shell activeClassId={classId}>
				<div className='tiny'>Загружаем…</div>
			</Shell>
		)
	}

	const { assignment = {}, tasks = [] } = data
	const asked = params.get('tab')
	// Без вкладки в адресе открывается разговор: в MVP уроки без упражнений.
	const tab = asked === 'words' ? 'words' : asked === 'tasks' || !data.has_dialog ? 'tasks' : 'conversation'
	const title = assignment.title || 'Урок'

	return (
		<Shell activeClassId={classId}>
			<nav className='crumbs'>
				<Link to={analyticsUrl}>{data.class?.name || 'Класс'}</Link>
				<span className='sep'>/</span>
				<Link to={analyticsUrl}>Аналитика</Link>
				<span className='sep'>/</span>
				<span>{title}</span>
			</nav>

			<div className='row-between'>
				<h1 className='h1'>{title}</h1>
				<Button className='btn-compact' to={`/app/class/${classId}/lesson/${assignmentId}`}>
					Открыть урок в классе
				</Button>
			</div>
			<div className='lead mt8'>
				{[assignment.level, assignment.assigned_at && `назначен ${shortDate(assignment.assigned_at)}`]
					.filter(Boolean)
					.join(', ')}
			</div>

			<LessonSummary data={data} />

			<div className='tabs' role='tablist' aria-label='Разделы урока'>
				<button
					className='tab'
					type='button'
					role='tab'
					aria-selected={tab === 'tasks'}
					onClick={() => setParams({ tab: 'tasks' })}
				>
					Задания
				</button>
				<button
					className='tab'
					type='button'
					role='tab'
					aria-selected={tab === 'words'}
					onClick={() => setParams({ tab: 'words' })}
				>
					Трудные слова
				</button>
				{data.has_dialog && (
					<button
						className='tab'
						type='button'
						role='tab'
						aria-selected={tab === 'conversation'}
						onClick={() => setParams({ tab: 'conversation' })}
					>
						Разговор с аватаром
					</button>
				)}
			</div>

			{tab === 'conversation' ? (
				<ConversationTab assignmentId={assignmentId} dialogIssued={data.dialog_issued} />
			) : tab === 'words' ? (
				<WordsView classId={classId} words={words} />
			) : (
				<TasksView tasks={tasks} />
			)}
		</Shell>
	)
}

/** Сводка урока: три плитки с теми же числами, что в столбце таблицы. */
function LessonSummary({ data }) {
	const total = data.total_students || 0
	const pct = data.correct_percent
	const conv = data.conversation_percent
	return (
		<div className='grid grid-3 mt22'>
			<div className='card la-tile'>
				<div className='eyebrow'>Пройден</div>
				<div className='la-num'>
					{data.passed_count || 0} <small>из {total}</small>
				</div>
			</div>
			<div className='card la-tile'>
				<div className='eyebrow'>Верных в заданиях</div>
				<div className='la-num'>{pct == null ? '—' : `${pct}%`}</div>
			</div>
			{data.has_dialog && (
				<div className='card la-tile'>
					<div className='eyebrow'>Разговор: целей зачтено</div>
					<div className='la-num'>{conv == null ? '—' : `${conv}%`}</div>
					<div className='tiny'>
						{data.talked_count || 0} из {total} {pluralWord(total, 'поговорил', 'поговорили', 'поговорили')}
					</div>
				</div>
			)}
		</div>
	)
}

/** Вкладка «Трудные слова»: список с главной аналитики, только по уроку. */
export function WordsView({ classId, words }) {
	return (
		<section className='mt22' aria-label='Трудные слова'>
			<p className='small muted'>
				Слова урока, в которых класс сделал больше всего ошибок, в заданиях и в разговоре с аватаром. Те же
				слова и числа, что в блоке «Что повторить на следующем уроке» в аналитике класса. Нажмите на слово,
				чтобы увидеть ошибки.
			</p>
			{words === undefined ? (
				<p className='tiny mt12'>Ищем трудные слова…</p>
			) : words.error ? (
				<p className='tiny mt12'>Не удалось посчитать трудные слова: {words.error}</p>
			) : (
				<HardWordList classId={classId} words={words} compact />
			)}
		</section>
	)
}

/** Вкладка «Задания»: доля верных и частые ошибки по каждому заданию. */
export function TasksView({ tasks }) {
	return (
		<section className='mt22' aria-label='Задания'>
			<p className='small muted'>
				Доля верных — от ответов, которые ученики дали. Одинаковые неверные ответы склеены в одну
				строку с числом.
			</p>
			<div className='mt12'>
				<Table
					head={['Задание', 'Верных', 'Частые ошибки']}
					empty='В уроке нет письменных заданий'
					rows={tasks.map((t) => {
						const badge = <PercentBadge task={t} />
						return {
							key: t.key,
							cells: [
								<TaskName key='t' task={t} />,
								badge,
								<Mistakes key='m' task={t} />,
							],
							mobile: {
								title: <TaskName task={t} />,
								badge,
								actions: <Mistakes task={t} />,
							},
						}
					})}
				/>
			</div>
		</section>
	)
}

function TaskName({ task }) {
	return (
		<span>
			<span className='t-name'>
				{task.n}. {task.title}
			</span>
			{task.type_label && <span className='tiny'> · {task.type_label}</span>}
		</span>
	)
}

function PercentBadge({ task }) {
	const pct = task.correct_percent
	if (pct == null) return <span className='tiny'>ещё не отвечали</span>
	return (
		<span className='la-pct'>
			<span className={`badge ${scoreTone(pct)}`}>{pct}%</span>
			<span className='tiny'>
				{plural(task.students, 'ученик', 'ученика', 'учеников')}
			</span>
		</span>
	)
}

/** Склеенные ошибки задания: частые сверху, хвост — по кнопке. */
function Mistakes({ task }) {
	const [all, setAll] = useState(false)
	const list = task.mistakes || []
	if (!list.length) {
		return <span className='tiny'>{task.answered ? 'Ошибок нет' : '—'}</span>
	}
	const shown = all ? list : list.slice(0, MISTAKES_SHOWN)
	return (
		<div className='la-mistakes'>
			{shown.map((m, i) => (
				<div className='la-mistake' key={i}>
					{m.prompt && <span className='tiny'>{m.prompt}</span>}
					<span>
						<span className='cl-ans-bad'>{m.answer || '(пусто)'}</span>
						{m.expected && (
							<>
								{' '}
								→ <span className='cl-ans-ok'>{m.expected}</span>
							</>
						)}
						<span className='tiny'>, {plural(m.count, 'раз', 'раза', 'раз')}</span>
					</span>
					<span className='tiny'>{names(m.students)}</span>
				</div>
			))}
			{list.length > MISTAKES_SHOWN && (
				<button className='la-more' type='button' onClick={() => setAll(!all)}>
					{all ? 'Свернуть' : `Показать все ${plural(list.length, 'ошибку', 'ошибки', 'ошибок')}`}
				</button>
			)}
		</div>
	)
}

/** «Айгуль, Тимур и ещё 3» — кто так ответил. */
function names(list = []) {
	if (list.length <= 3) return list.join(', ')
	return `${list.slice(0, 2).join(', ')} и ещё ${list.length - 2}`
}
