// StudentCard.jsx — карточка ученика (NR-21, #62).
//
// Учителю нужно видеть, как ученик решил задания, а не общую сумму баллов.
// Поэтому карточка — это список всех уроков, назначенных классу: сначала
// свёрнутый, с итогом в строке, а при раскрытии урока — ответы ученика на
// каждый вопрос с верным ответом рядом.
//
// Ответы дочитываются только при раскрытии и той же ручкой, что панель
// ученика на экране «Урок в классе»: за год у класса десятки уроков, и тянуть
// все ответы ради свёрнутого списка незачем. А вторая ручка с теми же данными
// рано или поздно разошлась бы с первой.
//
// У раскрытого урока две вкладки: «Письменные задания» и «Разговор с
// аватаром» (#63). Разговор — тот же отчёт, что на экране «Урок в классе»:
// расшифровка с ошибками в репликах и итог по целям. Разговор урока
// смотрится там же, где его письменные ответы, без перехода на другой экран.
//
// Доля верных считается по правилам вкладки «Аналитика»: от вопросов урока.
// Раньше карточка делила на данные ответы, и у одного ученика на соседних
// экранах было два разных процента.
//
// Внизу — «Что повторить» (#68): трудные слова класса, где ошибался этот
// ученик, с его числом ошибок и числом класса. Список берётся у той же ручки,
// что блок на главной аналитики, — числа совпадают. Ошибок нет — блока нет.

import { useCallback, useEffect, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { assignments as assignmentsApi, classes as classesApi, learning } from '../../api/v1'
import { Shell } from '../Shell'
import Icon from '../icons'
import { Button, lastSeen, pluralWord, shortDate, useToast } from '../ui'
import { issueWhen, studentStatus } from './ClassLessonLive'
import StudentConversation from './conversation/StudentConversation'
import { HardWordList } from './HardWords'
import { scoreTone, studentWords } from './results'
import '../class-lesson.css'

const percent = (value) => (value == null ? '—' : `${value}%`)

export default function StudentCard() {
	const { classId, studentId } = useParams()
	const navigate = useNavigate()
	const toast = useToast()

	const [student, setStudent] = useState(null)
	const [card, setCard] = useState(null)
	// Раскрытый урок — один: два раскрытых урока с десятком заданий каждый
	// превращают карточку в простыню, где не найти, чей это ответ.
	const [open, setOpen] = useState(null)
	// Ответы по урокам: assignment_id → { data } или { error }.
	const [answers, setAnswers] = useState({})
	const [onlyErrors, setOnlyErrors] = useState(false)
	// Вкладка раскрытого урока — общая для всех уроков: учитель, который
	// просматривает разговоры, переходит к следующему уроку сразу к разговору.
	const [tab, setTab] = useState('tasks')
	// Слова ученика: undefined — грузятся, { error } — не удалось.
	const [words, setWords] = useState(undefined)

	const load = useCallback(async () => {
		try {
			const [roster, data] = await Promise.all([
				classesApi.students(classId, { removed: true }),
				learning.studentCard(classId, studentId),
			])
			setStudent((roster.students || []).find((s) => s.student_id === studentId) || null)
			setCard(data)
		} catch (err) {
			toast(err.message)
			navigate(`/app/class/${classId}/students`, { replace: true })
		}
	}, [classId, studentId, navigate, toast])

	useEffect(() => {
		load()
	}, [load])

	// Отдельно от карточки: трудные слова считать дольше, а уроки ученика —
	// главное на экране.
	useEffect(() => {
		let gone = false
		setWords(undefined)
		classesApi
			.hardWords(classId)
			.then((r) => !gone && setWords(studentWords(r.words || [], studentId)))
			.catch((err) => !gone && setWords({ error: err.message }))
		return () => {
			gone = true
		}
	}, [classId, studentId])

	async function toggle(lesson) {
		if (open === lesson.assignment_id) {
			setOpen(null)
			return
		}
		setOpen(lesson.assignment_id)
		if (answers[lesson.assignment_id]?.data) return
		try {
			const data = await assignmentsApi.student(lesson.assignment_id, studentId)
			setAnswers((prev) => ({ ...prev, [lesson.assignment_id]: { data } }))
		} catch (err) {
			setAnswers((prev) => ({ ...prev, [lesson.assignment_id]: { error: err.message } }))
		}
	}

	if (!card) {
		return (
			<Shell activeClassId={classId}>
				<div className='tiny'>Загружаем…</div>
			</Shell>
		)
	}

	const s = card.summary
	const lessons = card.lessons || []

	return (
		<Shell activeClassId={classId}>
			<div className='row' style={{ alignItems: 'flex-start', gap: 14 }}>
				<button
					className='btn-icon'
					type='button'
					title='К составу класса'
					style={{ marginTop: 2 }}
					onClick={() => navigate(`/app/class/${classId}/students`)}
				>
					<Icon name='back' size={18} />
				</button>
				<div>
					<h1 className='h1'>
						{student?.name || 'Ученик'} <span className='tiny'>{student?.tag}</span>
					</h1>
					<div className='lead mt8'>
						{student?.level ? `Уровень ${student.level}` : 'Уровень не задан'}
						{student?.status === 'removed' && ', исключён из класса'}
					</div>
				</div>
			</div>

			<p className='sc-summary mt16'>
				Пройдено{' '}
				<b>
					{s.passed} из {s.assigned}
				</b>{' '}
				{pluralWord(s.assigned, 'назначенного урока', 'назначенных уроков', 'назначенных уроков')}
				{s.correct_percent != null && (
					<>
						, верных ответов <b>{percent(s.correct_percent)}</b>
					</>
				)}
				. Последняя активность: {s.last_seen_at ? lastSeen(s.last_seen_at) : 'уроки не открывал'}.
			</p>

			<h2 className='h2 mt28'>Назначенные уроки</h2>
			{lessons.length === 0 ? (
				<div className='tiny mt8'>Классу ещё не назначили ни одного урока.</div>
			) : (
				<>
					<div className='tiny mt8'>Нажмите на урок, чтобы увидеть ответы ученика.</div>
					<div className='sc-lessons mt12'>
						{lessons.map((l) => (
							<LessonRow
								key={l.assignment_id}
								lesson={l}
								classId={classId}
								studentId={studentId}
								open={open === l.assignment_id}
								answers={answers[l.assignment_id]}
								onlyErrors={onlyErrors}
								setOnlyErrors={setOnlyErrors}
								tab={tab}
								setTab={setTab}
								onToggle={() => toggle(l)}
							/>
						))}
					</div>
				</>
			)}

			<StudentWords classId={classId} studentId={studentId} words={words} />
		</Shell>
	)
}

/** «Что повторить» ученика. Пока грузится и когда ошибок нет — ничего. */
export function StudentWords({ classId, studentId, words }) {
	if (words?.error) return <p className='tiny mt28'>Не удалось посчитать, что повторить: {words.error}</p>
	if (!words?.length) return null
	return (
		<section className='mt28'>
			<h2 className='h2'>Что повторить</h2>
			<p className='muted mt8'>
				Трудные слова класса, в которых ошибался этот ученик, в заданиях и в разговоре с аватаром. Справа — его
				ошибки и для сравнения ошибки всего класса.
			</p>
			<HardWordList classId={classId} words={words} studentId={studentId} />
		</section>
	)
}

/**
 * LessonRow — урок в карточке: строка с итогом, а при раскрытии — ответы.
 *
 * Не открытый учеником урок не раскрывается: ответов нет, а «Не открывал»
 * в строке уже всё сказало.
 */
function LessonRow({
	lesson,
	classId,
	studentId,
	open,
	answers,
	onlyErrors,
	setOnlyErrors,
	tab,
	setTab,
	onToggle,
}) {
	const title = lesson.title || 'Урок'
	const conv = lesson.conversation

	const head = (
		<>
			<div className='grow'>
				<div className='t-name'>{title}</div>
				<div className='tiny'>Назначен {shortDate(lesson.assigned_at)}</div>
			</div>
			<div className='sc-lesson-meta'>
				{!lesson.started ? (
					<span className='badge badge-gray'>Не открывал</span>
				) : (
					<>
						{lesson.total_questions > 0 && (
							<span className={`badge ${scoreTone(lesson.correct_percent)}`}>
								{lesson.correct_answers} из {lesson.total_questions} верно
							</span>
						)}
						{!lesson.completed && <span className='badge badge-gray'>Не завершён</span>}
						{conv && (
							<span className='tiny'>
								{conv.ready ? `Разговор: ${conv.credited} из ${conv.goals} целей` : 'Разговор: отчёт готовится'}
							</span>
						)}
						<Icon name={open ? 'up' : 'down'} size={18} />
					</>
				)}
			</div>
		</>
	)

	return (
		<div className='sc-lesson' data-open={open}>
			{lesson.started ? (
				<button className='sc-lesson-hd' type='button' aria-expanded={open} onClick={onToggle}>
					{head}
				</button>
			) : (
				<div className='sc-lesson-hd'>{head}</div>
			)}
			{open && (
				<div className='sc-lesson-body'>
					<LessonWork
						assignmentId={lesson.assignment_id}
						studentId={studentId}
						conversation={lesson.conversation}
						issues={lesson.issues}
						answers={answers}
						onlyErrors={onlyErrors}
						setOnlyErrors={setOnlyErrors}
						tab={tab}
						setTab={setTab}
					/>
					<div className='mt16'>
						<Link to={`/app/class/${classId}/lesson/${lesson.assignment_id}`}>Открыть урок в классе →</Link>
					</div>
				</div>
			)}
		</div>
	)
}

/**
 * LessonWork — работа ученика на одном уроке: вкладки «Письменные задания» и
 * «Разговор с аватаром».
 *
 * Общая для карточки ученика и панели клетки в «Аналитике»: учитель видит
 * одни и те же ответы, откуда бы ни пришёл. answers — { data } или { error }
 * ручки ответов ученика, пока не пришли — undefined. issues — выдачи урока,
 * когда они известны: в карточке они подписывают ответы, в панели их нет.
 */
export function LessonWork({
	assignmentId,
	studentId,
	conversation,
	issues,
	answers,
	onlyErrors,
	setOnlyErrors,
	tab,
	setTab,
}) {
	return (
		<>
			<div className='tabs sc-tabs' role='tablist' aria-label='Работа ученика на уроке'>
				<button
					className='tab'
					type='button'
					role='tab'
					aria-selected={tab === 'tasks'}
					onClick={() => setTab('tasks')}
				>
					Письменные задания
				</button>
				<button
					className='tab'
					type='button'
					role='tab'
					aria-selected={tab === 'conversation'}
					onClick={() => setTab('conversation')}
				>
					Разговор с аватаром
				</button>
			</div>
			{tab === 'conversation' ? (
				<LessonConversation assignmentId={assignmentId} conversation={conversation} studentId={studentId} />
			) : (
				<>
					<div className='row-between mt16'>
						<div className='tiny'>
							{issues && (issues.map(issueWhen).join(' · ') || 'Задания ещё не выдавались')}
						</div>
						<div className='chips' role='group' aria-label='Какие ответы показывать'>
							<button
								className='chip'
								type='button'
								aria-pressed={!onlyErrors}
								onClick={() => setOnlyErrors(false)}
							>
								Все ответы
							</button>
							<button
								className='chip'
								type='button'
								aria-pressed={onlyErrors}
								onClick={() => setOnlyErrors(true)}
							>
								Только ошибки
							</button>
						</div>
					</div>
					<Answers answers={answers} onlyErrors={onlyErrors} onConversation={() => setTab('conversation')} />
				</>
			)}
		</>
	)
}

/**
 * LessonConversation — вкладка «Разговор с аватаром»: отчёт ученика, как на
 * экране «Урок в классе», только без кнопки «Все ученики» и без имени —
 * карточка и так про этого ученика.
 *
 * Разговора не было — отчёт не запрашиваем: ручка ответила бы «ещё не
 * начинал» и опрашивалась бы, пока урок раскрыт, а урок мог давно пройти.
 */
function LessonConversation({ assignmentId, conversation, studentId }) {
	if (!conversation) {
		return (
			<div className='card card-white cr-wait mt16'>
				<div className='h3'>Разговора с аватаром не было</div>
				<p className='small muted'>Ученик не отправил аватару ни одного ответа на этом уроке.</p>
			</div>
		)
	}
	return <StudentConversation assignmentId={assignmentId} studentId={studentId} embedded />
}

/** Answers — ответы ученика по заданиям урока: вопрос, его ответ, верный ответ. */
function Answers({ answers, onlyErrors, onConversation }) {
	if (!answers) return <div className='tiny mt12'>Загружаем ответы…</div>
	if (answers.error) return <div className='tiny mt12 cl-lost'>{answers.error}</div>
	const tasks = answers.data?.tasks || []
	if (tasks.length === 0) return <div className='tiny mt12'>Задания этого урока ещё не выдавались.</div>

	return tasks.map((t) => {
		const [label, cls] = studentStatus(t)
		const questions = (t.questions || []).filter((q) => !onlyErrors || !q.correct)
		// В режиме «Только ошибки» задание без ошибок не показываем вовсе:
		// иначе экран заполняется заголовками с пустотой под ними.
		if (onlyErrors && questions.length === 0 && t.kind !== 'dialog') return null
		return (
			<div className='cl-dtask' key={t.key}>
				<div className='cl-dtask-hd'>
					<b>
						{t.n}. {t.title}
					</b>
					<span className={`badge ${cls}`}>{label}</span>
				</div>
				{/* Разговор с аватаром разбирается на соседней вкладке: там
				    расшифровка с отметками. */}
				{t.kind === 'dialog' && (
					<div className='mt8'>
						<Button variant='ghost' className='btn-compact' onClick={onConversation}>
							Расшифровка разговора
						</Button>
					</div>
				)}
				{questions.map((q, i) => (
					<div className='cl-ans' key={i}>
						<span>{q.prompt}</span>
						{q.correct ? (
							<span className='cl-ans-ok'>✓ {q.answer}</span>
						) : (
							<span className='cl-ans-bad'>
								✗ {q.answer} {q.expected && <span className='tiny'>→ {q.expected}</span>}
							</span>
						)}
					</div>
				))}
				{!onlyErrors && t.kind === 'exercise' && (t.questions || []).length === 0 && (
					<div className='tiny mt8'>Ответов пока нет.</div>
				)}
			</div>
		)
	})
}
