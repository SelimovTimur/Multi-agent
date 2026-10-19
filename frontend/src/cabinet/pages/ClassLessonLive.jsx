// ClassLessonLive.jsx — экран «Урок в классе» (JOB-2…JOB-6).
//
// Учитель ведёт занятие и открывает классу задания урока по одному-два: на
// уроке — сразу и до «Завершить», на дом — до даты. Тут же видно, кто
// начал, кто закончил и у кого ошибки. Раньше это всплывало только в
// аналитике, когда подходить к застрявшему ученику было уже поздно, — и
// платформа годилась лишь для домашки.
//
// Экран живой: раз в 5 секунд перечитывает урок, пока открыт и виден.
// Правила выдачи — задание выдаётся один раз, домашняя работа одна,
// отменить можно, пока никто не начал, — решает сервер. Экран их не
// дублирует: он только не даёт нажать заведомо лишнее и пересказывает
// отказ словами сервера. Форма данных — в docs/backend/class-lessons-api.md.
//
// Отсюда же берут даты и подписи вкладка «Уроки» и карточка ученика: одна и
// та же выдача не должна называться на трёх экранах по-разному.

import { useCallback, useEffect, useRef, useState } from 'react'
import { Link, Navigate, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { assignments as api, issues as issuesApi } from '../../api/v1'
import { useAuth } from '../AuthContext'
import { Shell } from '../Shell'
import Icon from '../icons'
import { Button, plural, useToast } from '../ui'
import '../class-lesson.css'

// Раз в сколько перечитывать урок. Столько же обещает стори: ответ ученика
// виден учителю не позже чем через 5 секунд.
const POLL_MS = 5000

// Опрос не ждёт общие 30 секунд таймаута: следующий запрос не уходит, пока
// не кончился предыдущий, и зависший держал бы таблицу полминуты.
const POLL_TIMEOUT_MS = 10000

// Страница урока в аналитике: там отчёт по разговору с аватаром.
const analyticsUrl = (classId, assignmentId) => `/app/class/${classId}/analytics/lesson/${assignmentId}`

// Порог «справился»: от 70 % верных клетка зелёная, ниже — жёлтая заливка.
// Один на таблицу, панель ученика и его карточку.
const PASS_SHARE = 0.7

/* ---------------------------------------------------------------------------
 * Даты и подписи — общие с вкладкой «Уроки» и карточкой ученика
 * ------------------------------------------------------------------------- */

const pad = (n) => String(n).padStart(2, '0')

/** Время из ответа сервера; пусто или мусор — null, а не Invalid Date. */
function parse(iso) {
	if (!iso) return null
	const d = new Date(iso)
	return Number.isNaN(d.getTime()) ? null : d
}

/** ISO → «29.09» в местном времени. */
export function dayMonth(iso) {
	const d = parse(iso)
	return d ? `${pad(d.getDate())}.${pad(d.getMonth() + 1)}` : ''
}

/** ISO → «10:15» в местном времени. */
export function clockTime(iso) {
	const d = parse(iso)
	return d ? `${pad(d.getHours())}:${pad(d.getMinutes())}` : ''
}

/** 3 → «3 задания». */
export const taskCount = (n) => plural(n, 'задание', 'задания', 'заданий')

/** Доля верных дотягивает до «справился». */
export const passed = (correct, total) => total > 0 && correct / total >= PASS_SHARE

/**
 * Домашняя работа одной строкой: «на дом до 03.10».
 *
 * Без срока бывают только назначения, перенесённые из старой схемы: там
 * урок был открыт без ограничения, и «до —» читалось бы как поломка.
 */
export function homeLabel(home) {
	if (!home.open) return 'дом, срок прошёл'
	return home.due_at ? `на дом до ${dayMonth(home.due_at)}` : 'на дом, без срока'
}

/**
 * Когда была выдача — заголовок группы: «На уроке 29.09, 10:15–10:32»,
 * «На дом до 03.10».
 *
 * Работа на уроке кончается кнопкой — тогда время в closed_at — или сама в
 * конце дня, и тогда это due_at. Пока она идёт, конца у неё нет.
 */
export function issueWhen(issue) {
	if (issue.kind === 'home') {
		return issue.due_at ? `На дом до ${dayMonth(issue.due_at)}` : 'На дом, без срока'
	}
	const end = issue.open ? '' : clockTime(issue.closed_at || issue.due_at)
	return `На уроке ${dayMonth(issue.opened_at)}, ${clockTime(issue.opened_at)}${end ? `–${end}` : ''}`
}

/**
 * Номера заданий: «2, 4», «5–7», «1, 3–5».
 *
 * Три и больше подряд сворачиваются в диапазон, как в макете. Пара подряд
 * остаётся парой: «2–3» читается хуже, чем «2, 3».
 */
export function taskNumbers(numbers) {
	const ns = [...numbers].sort((a, b) => a - b)
	const out = []
	for (let i = 0; i < ns.length; ) {
		let j = i
		while (j + 1 < ns.length && ns[j + 1] === ns[j] + 1) j += 1
		if (j - i >= 2) out.push(`${ns[i]}–${ns[j]}`)
		else for (let k = i; k <= j; k += 1) out.push(String(ns[k]))
		i = j + 1
	}
	return out.join(', ')
}

/** Date → «2026-10-03» для input type=date: местный день, а не UTC. */
function ymd(d) {
	return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
}

/** «2026-10-03» + n дней. Через Date: конец месяца и года он считает сам. */
function addDays(day, n) {
	const [y, m, d] = day.split('-').map(Number)
	return ymd(new Date(y, m - 1, d + n))
}

/** «2026-10-03» → «03.10». */
function shortDay(day) {
	const [, m, d] = day.split('-')
	return `${d}.${m}`
}

/**
 * Отказ, после которого опрашивать дальше бессмысленно: урок сняли с
 * класса, он чужой или адрес битый. Сеть, таймаут и занятый сервер — не
 * такие: их переживаем и пробуем снова.
 */
const fatal = (err) => err.status >= 400 && err.status < 500 && err.status !== 408 && err.status !== 429

/* ---------------------------------------------------------------------------
 * Состояния: клетка таблицы, метка задания, статус в панели ученика
 * ------------------------------------------------------------------------- */

/**
 * Клетка таблицы по состоянию из контракта: [текст, класс].
 *
 * Клетки нет — задание выдали только что, а сервер о нём ещё не рассказал:
 * это «не начинал», а не поломка.
 */
function cellView(cell) {
	const c = cell || {}
	switch (c.state) {
		case 'doing':
			// У своего упражнения вопросов нет, и «0/0» считать нечего.
			return c.total > 0 ? [`${c.answered}/${c.total}`, 'cl-doing'] : ['открыл', 'cl-doing']
		case 'opened':
			return ['открыл', 'cl-doing']
		case 'done':
			if (!c.total) return ['✓', 'cl-ok']
			return [`${c.correct}/${c.total}`, passed(c.correct, c.total) ? 'cl-ok' : 'cl-mid']
		case 'miss':
			return ['не сделал', 'cl-miss']
		default:
			return ['—', 'cl-none']
	}
}

/** Метка задания в списке: выдано ли оно и как. */
function taskBadge(issue) {
	if (!issue) return { label: 'не выдано', cls: 'badge-gray' }
	if (issue.kind === 'class') {
		return issue.open
			? { label: 'на уроке', cls: 'badge-green', live: true }
			: { label: `было на уроке ${dayMonth(issue.opened_at)}`, cls: 'badge-gray' }
	}
	return { label: homeLabel(issue), cls: issue.open ? 'badge-warn' : 'badge-gray' }
}

/**
 * Статус задания в панели ученика: [текст, класс].
 *
 * Время — только у сделанного: сервер считает его от открытия до «сделано»,
 * и у начатого задания это было бы время, которое ещё идёт.
 */
/** Подпись задания у ученика: «сделал 4/5, 6 мин», «не сделал». Её же показывает карточка ученика. */
export function studentStatus(t) {
	switch (t.state) {
		case 'doing':
			return [t.total > 0 ? 'делает' : 'открыл', 'badge-warn']
		case 'opened':
			return ['открыл', 'badge-warn']
		case 'done': {
			const parts = [t.total > 0 ? `сделал ${t.correct}/${t.total}` : 'сделал']
			if (t.minutes != null) parts.push(`${t.minutes} мин`)
			return [parts.join(', '), !t.total || passed(t.correct, t.total) ? 'badge-green' : 'badge-warn']
		}
		case 'miss':
			return ['не сделал', 'badge-err']
		default:
			return ['не начал', 'badge-gray']
	}
}

/* ---------------------------------------------------------------------------
 * Урок после действия учителя
 * ------------------------------------------------------------------------- */

/** Урок с новой или изменённой выдачей — тем, что сервер ответил на действие. */
function withIssue(lesson, issue) {
	if (!issue) return lesson
	const keys = new Set(issue.task_keys || [])
	const issues = lesson.issues || []
	const known = issues.some((i) => i.id === issue.id)
	return {
		...lesson,
		issues: known ? issues.map((i) => (i.id === issue.id ? issue : i)) : [...issues, issue],
		tasks: (lesson.tasks || []).map((t) => (keys.has(t.key) ? { ...t, issue_id: issue.id } : t)),
		has_home: lesson.has_home || issue.kind === 'home',
	}
}

/** Урок без отменённой выдачи: её задания снова «не выданы». */
function withoutIssue(lesson, id) {
	const rest = (lesson.issues || []).filter((i) => i.id !== id)
	return {
		...lesson,
		issues: rest,
		tasks: (lesson.tasks || []).map((t) =>
			t.issue_id === id ? { ...t, issue_id: null, done_students: 0 } : t,
		),
		has_home: rest.some((i) => i.kind === 'home'),
	}
}

/* ---------------------------------------------------------------------------
 * Опрос
 * ------------------------------------------------------------------------- */

/**
 * usePoll — опрос раз в `ms`, пока экран открыт и вкладка видна.
 *
 * Запросы не накладываются: следующий заводится только после ответа на
 * предыдущий. setInterval на медленной сети копил бы очередь, ответы
 * приходили бы не в том порядке, в каком ушли, и таблица прыгала бы назад.
 *
 * Пока вкладка скрыта, опрос стоит: дёргать сервер ради экрана, которого
 * никто не видит, незачем. Учитель вернулся — урок перечитывается сразу, не
 * дожидаясь срока.
 *
 * Возвращает «обновить сейчас»: после действия учителя очередного опроса не
 * ждут. Если запрос уже в пути, новый встанет сразу за ним, а не рядом.
 *
 * `run(signal)` сам разбирает свои ошибки: опрос не должен умирать от
 * одного неудачного ответа.
 */
function usePoll(run, ms) {
	const runRef = useRef(run)
	useEffect(() => {
		runRef.current = run
	})
	const kick = useRef(() => {})

	useEffect(() => {
		let stopped = false
		let timer = null
		let ctrl = null
		let again = false

		async function tick() {
			clearTimeout(timer)
			timer = null
			if (stopped || document.hidden) return
			if (ctrl) {
				again = true
				return
			}
			ctrl = new AbortController()
			try {
				await runRef.current(ctrl.signal)
			} catch {
				// Сюда долетает только неожиданное — опрос из-за него не встаёт.
			} finally {
				ctrl = null
			}
			if (stopped) return
			if (again) {
				again = false
				tick()
			} else if (!document.hidden) {
				timer = setTimeout(tick, ms)
			}
		}

		function onVisibility() {
			if (document.hidden) {
				clearTimeout(timer)
				timer = null
			} else {
				tick()
			}
		}

		kick.current = tick
		tick()
		document.addEventListener('visibilitychange', onVisibility)
		return () => {
			stopped = true
			clearTimeout(timer)
			ctrl?.abort()
			kick.current = () => {}
			document.removeEventListener('visibilitychange', onVisibility)
		}
	}, [ms])

	return useCallback(() => kick.current(), [])
}

/* ---------------------------------------------------------------------------
 * Экран
 * ------------------------------------------------------------------------- */

export default function ClassLessonLive() {
	const { assignmentId } = useParams()
	// Другой урок — другой экран. Ключ сбрасывает выбор, панель и опрос:
	// иначе при переходе между уроками секунду висели бы задания прежнего, а
	// отмеченные галочки уехали бы в чужую выдачу.
	return <LiveScreen key={assignmentId} />
}

function LiveScreen() {
	const { classId, assignmentId } = useParams()
	const navigate = useNavigate()
	const toast = useToast()
	const { refresh: recheckAuth } = useAuth()

	const [lesson, setLesson] = useState(null)
	// Первая загрузка не удалась: пишем почему, а опрос продолжает пробовать.
	const [failed, setFailed] = useState('')
	// Последний опрос не прошёл, но прежние данные есть. Таблицу не стираем:
	// пропавшая сеть — не повод учителю потерять класс из виду.
	const [lost, setLost] = useState(false)
	const [seenAt, setSeenAt] = useState(0)
	// Насколько часы сервера впереди браузера. «Сегодня» для срока считаем по
	// серверу: у школьного компьютера часы бывают сбиты на сутки.
	const [skew, setSkew] = useState(0)
	const [picked, setPicked] = useState(() => new Set())
	const [homePick, setHomePick] = useState(false)
	const [homeDue, setHomeDue] = useState('')
	// Продление открыто у одной выдачи: { id, date }.
	const [extend, setExtend] = useState(null)
	// Какое действие сейчас идёт. Пока оно в пути, остальные кнопки ждут:
	// второе нажатие ушло бы с устаревшим списком заданий.
	const [busy, setBusy] = useState('')
	const [drawer, setDrawer] = useState(null)
	// Ответы ученика из панели: { id, data, error }.
	const [panel, setPanel] = useState(null)
	// Разговор с аватаром переехал на страницу урока в аналитике (#66). Старые
	// ссылки ?tab=conversation остались в истории браузера — их ведём туда.
	const [params] = useSearchParams()

	// Опрос читает их в момент запроса, а не в момент последней отрисовки:
	// ученика открывают и сразу просят свежие данные — до перерисовки.
	const drawerRef = useRef(null)
	const hasLesson = useRef(false)
	// Счётчик действий учителя. Ответ опроса, ушедшего до действия, описывает
	// урок до него: выданные задания на секунду снова стали бы «не выдано».
	const epoch = useRef(0)

	const poll = usePoll(async (signal) => {
		const sid = drawerRef.current
		const at = epoch.current
		const opts = { signal, timeoutMs: POLL_TIMEOUT_MS }
		const [live, card] = await Promise.allSettled([
			api.live(assignmentId, opts),
			sid ? api.student(assignmentId, sid, opts) : Promise.resolve(null),
		])
		if (signal.aborted || at !== epoch.current) return

		if (live.status === 'rejected') {
			const err = live.reason
			if (err.isUnauthorized) {
				// Сессия кончилась, пока экран висел открытым. Gate сам отправит
				// на вход и вернёт сюда же.
				recheckAuth()
				return
			}
			if (fatal(err)) {
				toast(err.message)
				navigate(`/app/class/${classId}/lessons`, { replace: true })
				return
			}
			if (hasLesson.current) setLost(true)
			else setFailed(err.message)
			return
		}

		hasLesson.current = true
		setLesson(live.value)
		setSeenAt(Date.now())
		const server = parse(live.value.server_time)
		setSkew(server ? server.getTime() - Date.now() : 0)
		setLost(false)
		setFailed('')

		// Панель — только того ученика, что открыт сейчас: пока шёл запрос,
		// учитель мог открыть соседа или закрыть панель.
		if (sid && sid === drawerRef.current) {
			if (card.status === 'fulfilled') {
				setPanel({ id: sid, data: card.value, error: '' })
			} else {
				setPanel((p) => ({
					id: sid,
					data: p?.id === sid ? p.data : null,
					error: card.reason.message,
				}))
			}
		}
	}, POLL_MS)

	// Отмеченное, но уже выданное — например, из соседней вкладки — снимаем.
	// Иначе после отмены той выдачи задание всплыло бы отмеченным само.
	useEffect(() => {
		if (!lesson) return
		const free = new Set((lesson.tasks || []).filter((t) => !t.issue_id).map((t) => t.key))
		setPicked((prev) => {
			const next = new Set([...prev].filter((k) => free.has(k)))
			return next.size === prev.size ? prev : next
		})
	}, [lesson])

	function openStudent(id) {
		drawerRef.current = id
		setDrawer(id)
		poll()
	}

	function closeStudent() {
		drawerRef.current = null
		setDrawer(null)
	}

	useEffect(() => {
		if (!drawer) return
		const onKey = (e) => {
			if (e.key === 'Escape') closeStudent()
		}
		document.addEventListener('keydown', onKey)
		return () => document.removeEventListener('keydown', onKey)
	}, [drawer])

	if (params.get('tab') === 'conversation') {
		return <Navigate to={`${analyticsUrl(classId, assignmentId)}?${params}`} replace />
	}

	if (!lesson) {
		return (
			<Shell activeClassId={classId}>
				<div className='tiny'>Загружаем…</div>
				{failed && (
					<div className='notice notice-err mt12'>
						Не удалось загрузить урок: {failed}. Экран попробует ещё раз сам.
					</div>
				)}
			</Shell>
		)
	}

	const { assignment = {}, tasks = [], issues = [], students = [] } = lesson
	const byId = new Map(issues.map((i) => [i.id, i]))
	const numOf = new Map(tasks.map((t) => [t.key, t.n]))
	const issued = tasks.filter((t) => t.issue_id)
	// Урок сняли с класса — выдавать по нему нечего. Говорим об этом сразу,
	// а не отказом на нажатие: иначе учитель отмечает задания, жмёт «Выдать»
	// и только тогда узнаёт, что урока у класса уже нет.
	const removed = assignment.state === 'closed'
	const free = removed ? [] : tasks.filter((t) => !t.issue_id)
	const chosen = free.filter((t) => picked.has(t.key))
	const allPicked = free.length > 0 && chosen.length === free.length
	const home = issues.find((i) => i.kind === 'home')
	const hasHome = Boolean(home) || Boolean(lesson.has_home)
	const today = ymd(new Date(Date.now() + skew))
	const working = Boolean(busy)
	const lessonsUrl = `/app/class/${classId}/lessons`
	// Отчёт по разговору живёт на странице урока в аналитике; здесь — только
	// ссылка, и только если разговор в уроке есть: это задание «Диалог».
	const dialog = tasks.find((t) => t.kind === 'dialog')

	function toggle(key) {
		setPicked((prev) => {
			const next = new Set(prev)
			if (next.has(key)) next.delete(key)
			else next.add(key)
			return next
		})
	}

	/**
	 * Действие учителя: выдать, завершить, продлить, отменить.
	 *
	 * Ответ сервера сразу кладём в экран, не дожидаясь опроса: иначе после
	 * «Выдать» задания ещё секунду висят невыданными, и учитель жмёт второй
	 * раз. Свежий опрос всё равно идёт следом — он и рассудит.
	 */
	async function act(key, request, apply, message) {
		setBusy(key)
		try {
			const res = await request()
			epoch.current += 1
			setLesson((l) => (l ? apply(l, res) : l))
			toast(message)
			return true
		} catch (err) {
			toast(err.message)
			return false
		} finally {
			setBusy('')
			poll()
		}
	}

	async function give(kind) {
		const keys = chosen.map((t) => t.key)
		if (!keys.length) return
		if (kind === 'home' && (!homeDue || homeDue < today)) {
			toast(homeDue ? 'Срок — не раньше сегодняшнего дня' : 'Укажите срок')
			return
		}
		const ok = await act(
			kind,
			() => api.issue(assignmentId, { kind, taskKeys: keys, dueDate: homeDue }),
			(l, res) => withIssue(l, res?.issue),
			kind === 'class'
				? `Выдано на уроке: ${taskCount(keys.length)}. Ученики видят их сразу.`
				: `Задано на дом до ${shortDay(homeDue)}: ${taskCount(keys.length)}.`,
		)
		if (ok) {
			setPicked(new Set())
			setHomePick(false)
		}
	}

	function openHome() {
		// Срок по умолчанию — через три дня: обычно это следующее занятие.
		// Чипы рядом меняют его одним нажатием.
		setHomeDue(addDays(today, 3))
		setHomePick(true)
	}

	// Продлить можно только позже текущего срока — и не в прошлое: срок,
	// который уже прошёл, ничего ученикам не откроет.
	function extendFloor(issue) {
		const due = parse(issue.due_at)
		const next = due ? addDays(ymd(due), 1) : today
		return next > today ? next : today
	}

	function extendChips(issue) {
		const due = parse(issue.due_at)
		const from = due && ymd(due) > today ? ymd(due) : today
		return [1, 3, 7].map((n) => {
			const date = addDays(from, n)
			return { date, label: `до ${shortDay(date)}` }
		})
	}

	async function prolong(issue) {
		const date = extend?.date || ''
		if (!date || date < extendFloor(issue)) {
			toast(date ? 'Новый срок должен быть позже текущего' : 'Укажите срок')
			return
		}
		const ok = await act(
			`extend:${issue.id}`,
			() => issuesApi.extend(issue.id, date),
			(l, res) => withIssue(l, res?.issue),
			issue.open
				? `Срок продлён до ${shortDay(date)}.`
				: `Срок продлён до ${shortDay(date)}. Задания снова открыты.`,
		)
		if (ok) setExtend(null)
	}

	const finish = (issue) =>
		act(
			`finish:${issue.id}`,
			() => issuesApi.finish(issue.id),
			(l, res) => withIssue(l, res?.issue),
			'Работа на уроке завершена. Ответы сохранены.',
		)

	const cancel = (issue) =>
		act(
			`cancel:${issue.id}`,
			() => issuesApi.cancel(issue.id),
			(l) => withoutIssue(l, issue.id),
			'Выдача отменена. Задания снова можно выдать.',
		)

	function issueHead(issue) {
		if (issue.kind === 'class' && issue.open) {
			return (
				<>
					<i className='cl-dot cl-pulse' aria-hidden='true' />
					<b>Сейчас на уроке</b>
					<span>
						с {clockTime(issue.opened_at)}
					</span>
				</>
			)
		}
		if (issue.kind === 'class') {
			return (
				<>
					<b>На уроке {dayMonth(issue.opened_at)}</b>
					<span>
						{clockTime(issue.opened_at)}–{clockTime(issue.closed_at || issue.due_at)}, завершено
					</span>
				</>
			)
		}
		return (
			<>
				<b>{issueWhen(issue)}</b>
				{!issue.open && (
					<span>
						срок прошёл
					</span>
				)}
			</>
		)
	}

	function issueCard(issue) {
		const isHome = issue.kind === 'home'
		const keys = issue.task_keys || []
		const numbers = keys.map((k) => numOf.get(k)).filter(Boolean)
		// Продлевать нечего только у старого назначения без срока: оно и так
		// открыто без ограничения.
		const canExtend = isHome && (issue.due_at || !issue.open)
		const extending = extend?.id === issue.id

		return (
			<div
				className={`cl-issue${isHome ? ' is-home' : ''}${issue.open ? '' : ' is-done'}`}
				key={issue.id}
			>
				<div>
					<div className='cl-issue-hd'>{issueHead(issue)}</div>
					<div className='cl-issue-sub'>
						{taskCount(keys.length)}: {taskNumbers(numbers)}. Всё сделали {issue.done_students ?? 0}{' '}
						из {students.length}
					</div>
				</div>
				<div className='cl-issue-acts'>
					{issue.kind === 'class' && issue.open && (
						<Button
							variant='danger'
							className='btn-compact'
							onClick={() => finish(issue)}
							disabled={working}
						>
							■ Завершить работу на уроке
						</Button>
					)}
					{canExtend && !extending && (
						<Button
							className='btn-compact'
							disabled={working}
							onClick={() => {
								const chips = extendChips(issue)
								setExtend({ id: issue.id, date: chips[1].date })
							}}
						>
							Продлить срок
						</Button>
					)}
					{/* Отменить можно, пока никто из учеников не начал: дальше за
					    выдачей стоят ответы, и молча их не выбрасываем. */}
					{!issue.started && (
						<Button
							variant='ghost'
							className='btn-compact'
							onClick={() => cancel(issue)}
							disabled={working}
						>
							Отменить выдачу
						</Button>
					)}
				</div>
				{extending && (
					<DuePicker
						label='Продлить до:'
						value={extend.date}
						min={extendFloor(issue)}
						chips={extendChips(issue)}
						onChange={(date) => setExtend({ id: issue.id, date })}
					>
						<Button
							variant='primary'
							className='btn-compact'
							onClick={() => prolong(issue)}
							disabled={working || !extend.date}
						>
							Продлить
						</Button>
						<Button variant='ghost' className='btn-compact' onClick={() => setExtend(null)}>
							Отмена
						</Button>
					</DuePicker>
				)}
			</div>
		)
	}

	return (
		<Shell activeClassId={classId}>
			<nav className='crumbs'>
				<Link to={lessonsUrl}>{lesson.class?.name || 'Класс'}</Link>
				<span className='sep'>/</span>
				<Link to={lessonsUrl}>Уроки</Link>
				<span className='sep'>/</span>
				<span>{assignment.title}</span>
			</nav>

			<h1 className='h1'>{assignment.title}</h1>
			<div className='lead mt8'>
				{[
					lesson.class?.name,
					assignment.level,
					`выдано ${issued.length} из ${plural(tasks.length, 'задания', 'заданий', 'заданий')}`,
				]
					.filter(Boolean)
					.join(', ')}
			</div>

			{removed && (
				<div className='notice notice-warn mt16'>
					<span>
						<b>Урок снят с класса</b>
						<div className='tiny mt8'>
							Выдавать задания по нему больше нельзя. Всё, что ученики успели сделать, сохранено и
							видно в таблице.
						</div>
					</span>
				</div>
			)}

			{dialog && (
				<div className='mt12'>
					<Link to={`${analyticsUrl(classId, assignmentId)}?tab=conversation`}>
						Разговор с аватаром: отчёт по классу в аналитике →
					</Link>
				</div>
			)}

			{issues.length > 0 && (
				<section className='cl-issues mt22' aria-label='Выдачи'>
					<div className='eyebrow'>Выдачи</div>
					{issues.map(issueCard)}
				</section>
			)}

			<div className={`cl-live${issues.length ? '' : ' mt22'}`}>
				<TaskPicker
					tasks={tasks}
					byId={byId}
					picked={picked}
					studentsCount={students.length}
					removed={removed}
					free={free}
					chosen={chosen}
					working={working}
					hasHome={hasHome}
					home={home}
					homePick={homePick}
					homeDue={homeDue}
					today={today}
					onToggle={toggle}
					onPickAll={() => setPicked(allPicked ? new Set() : new Set(free.map((t) => t.key)))}
					allPicked={allPicked}
					onGive={give}
					onOpenHome={openHome}
					onCancelHome={() => setHomePick(false)}
					onHomeDue={setHomeDue}
				/>

				<section aria-label='Как проходит класс'>
					<div className='cl-board-hd'>
						<h2 className='h3'>Как проходит класс</h2>
						<Ago at={seenAt} lost={lost} />
					</div>

					{issued.length === 0 ? (
						<div className='card cl-empty'>
							<div className='h3'>Пока ничего не выдано</div>
							<div className='muted'>
								Отметьте слева задания и выдайте их на уроке или на дом. Здесь появится таблица:
								кто начал, кто сделал, у кого ошибки.
							</div>
						</div>
					) : (
						<>
							<div className='cl-grid'>
								<table>
									<thead>
										<tr>
											<th className='cl-name'>Ученик</th>
											{issued.map((t) => (
												<th key={t.key} title={t.title}>
													Задание {t.n}
													<small>{t.title}</small>
												</th>
											))}
										</tr>
									</thead>
									<tbody>
										{students.length === 0 && (
											<tr>
												<td className='cl-quiet' colSpan={issued.length + 1}>
													В классе пока нет учеников — добавьте их на вкладке «Ученики».
												</td>
											</tr>
										)}
										{students.map((s) => (
											<tr
												key={s.id}
												className={drawer === s.id ? 'is-on' : ''}
												tabIndex={0}
												onClick={() => openStudent(s.id)}
												onKeyDown={(e) => {
													if (e.key === 'Enter' || e.key === ' ') {
														e.preventDefault()
														openStudent(s.id)
													}
												}}
											>
												<td className='cl-name'>
													<span className='cl-who'>
														<i
															className={`cl-dot${s.online ? '' : ' is-off'}`}
															title={s.online ? 'в сети' : 'не в сети'}
														/>
														<b>{s.name}</b>
														{s.tag && <span className='tiny'>#{s.tag}</span>}
													</span>
													{!s.online && <div className='tiny'>не в сети</div>}
												</td>
												{issued.map((t) => {
													const [text, cls] = cellView(s.cells?.[t.key])
													return (
														<td key={t.key}>
															<span className={`cl-cell ${cls}`}>{text}</span>
														</td>
													)
												})}
											</tr>
										))}
									</tbody>
								</table>
							</div>
							<div className='cl-legend'>
								<span>
									<span className='cl-cell cl-none'>—</span> не начал
								</span>
								<span>
									<span className='cl-cell cl-doing'>2/5</span> делает
								</span>
								<span>
									<span className='cl-cell cl-ok'>5/5</span> сделал, от 70%
								</span>
								<span>
									<span className='cl-cell cl-mid'>2/5</span> сделал, меньше 70%
								</span>
								<span>
									<span className='cl-cell cl-miss'>!</span> не сделал, выдача закрыта
								</span>
								<span>
									<i className='cl-dot' /> в сети
								</span>
							</div>
						</>
					)}
				</section>
			</div>

			{drawer && (
				<StudentPanel
					classId={classId}
					assignmentId={assignmentId}
					studentId={drawer}
					student={students.find((s) => s.id === drawer)}
					panel={panel?.id === drawer ? panel : null}
					onClose={closeStudent}
				/>
			)}
		</Shell>
	)
}

/**
 * Ago — «обновлено 12 с назад»: сколько лет данным в таблице.
 *
 * Свои часы раз в секунду: перерисовывать всю таблицу ради одной подписи
 * незачем. Пропала связь — подпись говорит об этом рядом, а таблица
 * остаётся какой была.
 */
function Ago({ at, lost }) {
	const [, tick] = useState(0)
	useEffect(() => {
		const timer = setInterval(() => tick((n) => n + 1), 1000)
		return () => clearInterval(timer)
	}, [])
	if (!at) return null

	const sec = Math.max(0, Math.round((Date.now() - at) / 1000))
	let ago = `${sec} с назад`
	if (sec < 2) ago = 'только что'
	else if (sec >= 60) ago = `${Math.floor(sec / 60)} мин назад`

	return (
		<span className='cl-ago'>
			{lost && <span className='cl-lost'>нет связи, </span>}
			обновлено {ago}
		</span>
	)
}

/**
 * DuePicker — срок кнопками и календарём.
 *
 * Типовой срок — «завтра», «через неделю» — ставится одним нажатием,
 * календарь остаётся для точной даты. min не даёт выбрать день, который
 * сервер всё равно отвергнет; набранную руками дату проверяем перед
 * отправкой — min браузер не навязывает.
 *
 * children — кнопки подтверждения и отмены.
 */
function DuePicker({ label, value, min, chips, onChange, children }) {
	return (
		<div className='cl-due'>
			<div className='cl-due-l'>{label}</div>
			<div className='chips'>
				{chips.map((c) => (
					<button
						className='chip'
						type='button'
						key={c.date}
						aria-pressed={value === c.date}
						onClick={() => onChange(c.date)}
					>
						{c.label}
					</button>
				))}
			</div>
			<input
				className='input'
				type='date'
				aria-label={label}
				value={value}
				min={min}
				onChange={(e) => onChange(e.target.value)}
			/>
			<div className='row'>{children}</div>
		</div>
	)
}

/**
 * StudentPanel — ответы одного ученика справа от таблицы (JOB-6).
 *
 * Без затемнения: таблица под панелью живая, и соседнего ученика открывают
 * прямо из неё. Обновляется вместе с таблицей — тем же опросом.
 */
function StudentPanel({ classId, assignmentId, studentId, student, panel, onClose }) {
	const box = useRef(null)

	// Фокус — в панель: с клавиатуры её иначе не найти, она в конце страницы.
	useEffect(() => {
		box.current?.focus()
	}, [studentId])

	const info = panel?.data?.student || student || {}
	const tasks = panel?.data?.tasks

	return (
		<aside
			className='cl-drawer'
			ref={box}
			tabIndex={-1}
			role='dialog'
			aria-label={info.name || 'Ученик'}
		>
			<div className='cl-drawer-hd'>
				<div>
					<div className='tiny'>Ученик, {info.online ? 'в сети' : 'не в сети'}</div>
					<h2 className='h2 mt8'>
						{info.name || 'Ученик'} {info.tag && <span className='tiny'>#{info.tag}</span>}
					</h2>
				</div>
				<button className='modal-x' type='button' aria-label='Закрыть' onClick={onClose}>
					<Icon name='x' size={20} />
				</button>
			</div>

			{!tasks && <div className='tiny mt16'>{panel?.error || 'Загружаем ответы…'}</div>}
			{tasks?.length === 0 && <div className='tiny mt16'>Задания ещё не выданы.</div>}
			{tasks?.map((t) => {
				const [label, cls] = studentStatus(t)
				return (
					<div className='cl-dtask' key={t.key}>
						<div className='cl-dtask-hd'>
							<b>
								{t.n}. {t.title}
							</b>
							<span className={`badge ${cls}`}>{label}</span>
						</div>
						{/* Разговор с аватаром разбирается не здесь, а в отчёте на
						    странице урока в аналитике: там расшифровка с отметками.
						    Ссылка открывает его на этом ученике. */}
						{t.kind === 'dialog' && (
							<div className='mt8'>
								<Link to={`${analyticsUrl(classId, assignmentId)}?tab=conversation&student=${studentId}`}>
									Отчёт по разговору →
								</Link>
							</div>
						)}
						{(t.questions || []).map((q, i) => (
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
					</div>
				)
			})}
			{/* Ответы на экране есть, но последний запрос за ними не прошёл: они
			    могли устареть, и учитель должен это знать. */}
			{tasks && panel?.error && <div className='tiny mt12 cl-lost'>{panel.error}</div>}

			<div className='mt22'>
				<Button to={`/app/class/${classId}/student/${studentId}`}>Открыть карточку ученика →</Button>
			</div>
		</aside>
	)
}

/**
 * Задания урока с выбором и кнопками выдачи: на уроке или на дом. Отдельно
 * от экрана, чтобы приветственное окно учителя показывало настоящий выбор
 * на демонстрационном уроке.
 */
export function TaskPicker({
	tasks,
	byId,
	picked,
	studentsCount,
	removed,
	free,
	chosen,
	working,
	hasHome,
	home,
	homePick,
	homeDue,
	today,
	allPicked,
	onToggle,
	onPickAll,
	onGive,
	onOpenHome,
	onCancelHome,
	onHomeDue,
}) {
	return (
		<div className='cl-tasks'>
			<div className='cl-tasks-hd'>
				<b>Задания урока</b>
				{free.length > 0 && (
					<Button
						variant='ghost'
						className='btn-compact'
						onClick={onPickAll}
					>
						{allPicked ? 'Снять все' : 'Выбрать все'}
					</Button>
				)}
			</div>

			<div className='cl-task-list'>
				{tasks.map((t) => {
					const issue = t.issue_id ? byId.get(t.issue_id) : null
					const on = !t.issue_id && picked.has(t.key)
					const badge =
						t.issue_id && !issue ? { label: 'выдано', cls: 'badge-gray' } : taskBadge(issue)
					const meta = [
						t.type_label,
						t.questions > 0 && `${t.questions} вопр.`,
						t.issue_id && `сделали ${t.done_students ?? 0} из ${studentsCount}`,
					]
					return (
						<label
							className={`cl-task${on ? ' is-sel' : ''}${t.issue_id ? ' is-off' : ''}`}
							key={t.key}
						>
							<input
								type='checkbox'
								checked={on}
								disabled={Boolean(t.issue_id) || removed}
								onChange={() => onToggle(t.key)}
							/>
							<div className='grow'>
								<div className='cl-task-t'>
									{t.n}. {t.title}
								</div>
								<div className='cl-task-meta'>{meta.filter(Boolean).join(', ')}</div>
								<div className='cl-task-badge'>
									<span className={`badge ${badge.cls}`}>
										{badge.live && <i className='cl-dot cl-pulse' aria-hidden='true' />}
										{badge.label}
									</span>
								</div>
							</div>
						</label>
					)
				})}
			</div>

			<div className='cl-actbar'>
				{chosen.length > 0 ? (
					<div className='small'>
						<b>Выбрано: {taskCount(chosen.length)}</b>
					</div>
				) : (
					<div className='tiny'>
						{free.length
							? 'Отметьте задания и выберите, как их выдать.'
							: 'Все задания урока выданы.'}
					</div>
				)}
				<Button
					variant='primary'
					onClick={() => onGive('class')}
					disabled={!chosen.length || working}
				>
					▶ Выдать сейчас на уроке
				</Button>
				{hasHome ? (
					<>
						<Button disabled>Задать на дом…</Button>
						<div className='tiny'>
							Домашняя работа по уроку уже задана (
							{home?.due_at ? `до ${dayMonth(home.due_at)}` : 'без срока'}). Её можно
							продлить или отменить в блоке «Выдачи».
						</div>
					</>
				) : homePick ? (
					<DuePicker
						label='Задать на дом до:'
						value={homeDue}
						min={today}
						chips={[
							{ label: 'завтра', date: addDays(today, 1) },
							{ label: 'через 3 дня', date: addDays(today, 3) },
							{ label: 'через неделю', date: addDays(today, 7) },
						]}
						onChange={onHomeDue}
					>
						<Button
							variant='primary'
							className='btn-compact'
							onClick={() => onGive('home')}
							disabled={!chosen.length || !homeDue || working}
						>
							Задать на дом
						</Button>
						<Button variant='ghost' className='btn-compact' onClick={onCancelHome}>
							Отмена
						</Button>
					</DuePicker>
				) : (
					<Button onClick={onOpenHome} disabled={!chosen.length || working}>
						Задать на дом…
					</Button>
				)}
			</div>
		</div>
	)
}
