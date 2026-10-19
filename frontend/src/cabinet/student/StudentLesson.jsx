// StudentLesson.jsx — прохождение урока учеником.
//
// Урок идёт заданиями: цели, текст, выражения, упражнения, правило,
// разговор с Ишкой. Одно задание на экране: подросток с телефона не должен
// листать простыню из четырёх упражнений сразу. Вид — по макету «Экраны
// ученика» (docs/product/student-screens/story.md): иконки этапов в шапке,
// свой фон у каждого этапа, отклик на ответ и серия верных подряд, итог
// выдачи со звёздами.
//
// Задания учитель выдаёт частями — на уроке или на дом
// (docs/product/class-lessons/story.md, JOB-9), и экран показывает только
// выданное. Раздел приходит в адресе: ?section=class — «на уроке»,
// ?section=home — «домашняя работа», без него — все открытые задания урока.
// Список заданий раздела даёт сервер, и экран спрашивает его раз в 5 секунд:
// новое задание учителя появляется без перезагрузки, а работа, которую
// учитель завершил, закрывается у ученика сама.
//
// Правильность ответа решает сервер. Ключа ответов здесь нет и быть не
// может: он не приходит на клиент вовсе — иначе ответы лежали бы в
// исходниках страницы.
//
// Ответы хранятся на уровне урока, а не внутри шага. Пока они лежали в
// упражнении, возврат на шаг назад стирал их вместе с размонтированием
// компонента: ученик видел вопрос снова неотвеченным, а счёт при этом уже
// был засчитан. Общее хранилище нужно и для итога — он называет ошибки
// выдачи, — и для прежних ответов попытки: их отдаёт сервер, и урок,
// открытый со второго устройства, продолжается, а не начинается.

import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useLocation, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { my } from '../../api/v1'
import { AuthShell } from '../Shell'
import Icon from '../icons'
import { Button, plural, useToast } from '../ui'
import Avatar from './Avatar'
import StudentFeedback from './StudentFeedback'
import { markAsked, questionsToAsk } from './student-feedback'
import { MatchingExercise, QuizExercise } from './lesson/exercises'
import { IssueFinish } from './lesson/finish'
import {
	SECTION_LABEL, mistakesOf, restoreAnswers, sectionIssue, stageBar, stageOf, stepLine, wordsPending,
} from './lesson/kit'
import { Cheer, Confetti, FrameTop, LessonFrame, MistakesList, useCheer } from './lesson/parts'
import { addPending, flushPending, loadPending, nextDelay, pendingKey, retryable, savePending } from './lesson/pending'
import {
	CustomStep, GrammarStep, LexiconStep, MissingStep, ObjectivesStep, TalkStep, TextStep, useTalk,
} from './lesson/steps'
import { usePoll } from './poll'
import './lesson/lesson.css'

/**
 * Ключ ответа: упражнение плюс вопрос (или левая часть пары).
 *
 * Такой же, как на сервере (learning.answerKey) — по нему он и хранит
 * прежние ответы попытки, и отличает повторный ответ от нового.
 */
const answerKey = (exerciseId, itemId) => `${exerciseId}:${itemId}`

/** Итог раздела — место в списке после последнего задания. */
const DONE = '#done'

// Ответ ждём недолго: при обрыве связи общий таймаут в 30 секунд держал бы
// ученика на вопросе, а ответ всё равно уйдёт из очереди (lesson/pending.js).
const ANSWER_OPTS = { timeoutMs: 15000 }

/** Ответ из очереди — на экране «сохранится, когда будет связь». */
const waitingAnswer = (p) => ({ chosen: p.answer, correct: false, expected: '', pending: true })

/** Сколько раз перечитать итог, пока считается разбор разговора: две минуты. */
const RESULTS_POLLS = 24

/**
 * Отказы сервера по выдаче: задание закрылось или его ещё нет.
 *
 * Все три приходят одним кодом 409, и различает их только текст. Обычно мы
 * ветвимся по коду, но эти тексты закреплены контрактом
 * (docs/backend/class-lessons-api.md) ровно затем, чтобы по ним различать.
 */
const REFUSALS = {
	'Учитель завершил работу на уроке': 'class_finished',
	'Срок домашней работы прошёл': 'home_expired',
	'Это задание ещё не выдано': 'not_issued',
}

/** Причина отказа по выдаче или пустая строка, если отказ другой. */
function refusal(err) {
	if (err?.status !== 409) return ''
	// «ё» и точку в конце не различаем: из-за них «срок прошёл» не должен
	// превращаться посреди урока в непонятную ошибку.
	const plain = (s) => String(s || '').trim().replace(/\.$/, '').replace(/ё/g, 'е')
	const text = plain(err.message)
	const hit = Object.keys(REFUSALS).find((k) => plain(k) === text)
	return hit ? REFUSALS[hit] : ''
}

/**
 * Что сказать, когда раздел закрыт или пуст.
 *
 * Главное в обоих закрытиях — «ответы сохранены»: у ребёнка задание
 * исчезло посреди упражнения, и без этих слов он решит, что всё пропало.
 */
function closedText(reason, section) {
	if (reason === 'class_finished') return ['Учитель завершил работу на уроке', 'Твои ответы сохранены.']
	if (reason === 'home_expired') return ['Срок домашней работы прошёл', 'Твои ответы сохранены.']
	return [
		'Сейчас заданий нет',
		section === 'class'
			? 'Когда учитель выдаст задания, они появятся в разделе «Сейчас на уроке».'
			: 'Когда учитель выдаст задания, они появятся в «Моих занятиях».',
	]
}

/**
 * Задание сервера → шаг урока: чем его показать.
 *
 * Ключи заданий — из контракта: objectives, text, lexicon, ex:<упражнение>,
 * grammar, dialog. Если под заданием нет содержимого (урок и список заданий
 * разошлись), шаг становится «missing»: лучше название и «Дальше», чем
 * белый экран у ребёнка посреди урока.
 */
function toStep(task, content) {
	const missing = { kind: 'missing', task }
	switch (task.kind) {
		case 'objectives':
			return content.objectives?.length ? { kind: 'objectives', task } : missing
		case 'text':
			return { kind: 'text', task }
		case 'lexicon':
			return content.lexicon?.length ? { kind: 'lexicon', task } : missing
		case 'exercise': {
			const ex = (content.exercises || []).find((e) => `ex:${e.id}` === task.key)
			if (!ex) return missing
			// «Свой текст» — блок, который учитель написал руками: отвечать в
			// нём нечего, и проверки у него нет. Упражнение без вопросов —
			// так же: иначе в нём не было бы ни вопросов, ни «Дальше».
			const items = ex.type === 'matching' ? ex.payload?.items : ex.payload?.questions
			if (ex.type === 'custom' || !items?.length) return { kind: 'custom', ex, task }
			return { kind: ex.type === 'matching' ? 'matching' : 'quiz', ex, task }
		}
		case 'grammar':
			return content.grammar?.length ? { kind: 'grammar', task } : missing
		case 'dialog':
			return { kind: 'talk', task }
		default:
			return missing
	}
}

/** Шаги, которые ведут себя сами: внизу только «←», «Дальше» — внутри. */
const SELF_LED = new Set(['matching', 'quiz', 'talk'])

export default function StudentLesson() {
	const { attemptId } = useParams()
	const [params] = useSearchParams()
	const asked = params.get('section')
	const section = asked === 'class' || asked === 'home' ? asked : ''
	// Свой экран на каждый раздел: без перемонтирования переход из «на
	// уроке» в «домашнюю работу» унёс бы позицию и отметки одного раздела в
	// другой.
	return <LessonRun key={`${attemptId}:${section}`} attemptId={attemptId} section={section} />
}

function LessonRun({ attemptId, section }) {
	const navigate = useNavigate()
	const location = useLocation()
	const toast = useToast()

	const [content, setContent] = useState(null)
	const [failed, setFailed] = useState('')
	// Ответы этого урока: ключ → что выбрали, верно ли и как было верно.
	const [answers, setAnswers] = useState({})
	// Ответы, не дошедшие до сервера из-за связи: очередь на устройстве
	// (lesson/pending.js). waiting — сколько их, для строки над заданием.
	const queue = useRef([])
	const [waiting, setWaiting] = useState(0)
	const flushing = useRef(false)
	const retry = useRef({ timer: 0, delay: 0 })
	const flushRef = useRef(() => {})
	// Разбор отказа сервера (settle) объявлен ниже; досылке он нужен раньше.
	const settleRef = useRef(async () => false)
	// Назначение попытки: по нему читаются «Результаты урока» — звёзды
	// итога и название урока. null — попытка ещё не прочитана, '' — не вышло.
	const [assignmentId, setAssignmentId] = useState(null)
	// Задания раздела глазами сервера: открыт ли раздел, почему закрыт и что
	// в нём — в порядке урока. null — список ещё не пришёл.
	const [feed, setFeed] = useState(null)
	const [feedError, setFeedError] = useState('')
	// Где ученик: ключ задания, DONE — итог раздела, null — ещё не решили.
	// Ключ, а не номер: учитель выдаёт задания посреди урока, и номер
	// текущего задания в разделе от этого сдвигается.
	const [pos, setPos] = useState(null)
	// Задания без вопросов, отмеченные сделанными в этот заход: опрос
	// подтвердит их позже, а засчитать «Дальше» должна сразу.
	const [marked, setMarked] = useState(() => new Set())
	const [moving, setMoving] = useState(false)
	// Выдачи ученика со звёздами — для итога выдачи.
	const [results, setResults] = useState({ data: null, failed: false })
	// Итог: arrived — пришли сюда последним «Дальше» в этот заход (сначала
	// «Почти всё!»), confirmed — сохранили, party — залп конфетти.
	const [finish, setFinish] = useState({ arrived: false, confirmed: false, saving: false, party: 0 })
	// '' — шаг или итог, 'mistakes' — «Мои ошибки» этой выдачи.
	const [view, setView] = useState('')
	// Вопросы опроса после урока, пока окно открыто; null — закрыто.
	const [asking, setAsking] = useState(null)
	// Серия верных подряд за этот заход.
	const [streak, setStreak] = useState({ now: 0, best: 0 })
	const streakRef = useRef(streak)
	const [cheer, showCheer] = useCheer()

	useEffect(() => {
		my.lesson(attemptId)
			.then(setContent)
			.catch((err) => setFailed(err.message))
		// Состояние попытки — отдельной ручкой: содержимое урока одно на всех,
		// а ответы и счёт — у каждого ученика свои.
		my.attempt(attemptId)
			.then((res) => {
				const server = restoreAnswers(res.attempt)
				// Ответы, отложенные без связи в прошлый заход: что сервер уже
				// принял, из очереди убираем, остальное показываем и досылаем.
				for (const p of loadPending(attemptId)) {
					if (!server[pendingKey(p)] && !queue.current.some((q) => pendingKey(q) === pendingKey(p))) {
						queue.current = addPending(queue.current, p)
					}
				}
				queue.current = queue.current.filter((p) => !server[pendingKey(p)])
				savePending(attemptId, queue.current)
				setWaiting(queue.current.length)
				const held = Object.fromEntries(queue.current.map((p) => [pendingKey(p), waitingAnswer(p)]))
				// Ответы, данные только что, важнее прежних: попытка могла
				// прийти позже первого ответа.
				setAnswers((now) => ({ ...server, ...held, ...now }))
				setAssignmentId(res.attempt?.assignment_id || '')
				if (queue.current.length) flushRef.current()
			})
			.catch(() => {
				// Не прочиталось — урок откроется как новый. Ответить второй
				// раз сервер всё равно не даст, так что соврать это не даст.
				setAssignmentId('')
			})
	}, [attemptId])

	const loadResults = useCallback(async () => {
		if (!assignmentId) return null
		try {
			const data = await my.results(assignmentId)
			setResults({ data, failed: false })
			return data
		} catch {
			setResults((r) => ({ ...r, failed: !r.data }))
			return null
		}
	}, [assignmentId])

	useEffect(() => {
		loadResults()
	}, [loadResults])

	// Номера запросов: ответ на ранний опрос не должен перетирать более
	// поздний — иначе завершённая учителем работа на секунду открывалась бы
	// снова.
	const sent = useRef(0)
	const shown = useRef(0)

	const apply = useCallback((res, n) => {
		if (n < shown.current) return
		shown.current = n
		setFeedError('')
		setFeed((prev) => ({
			open: Boolean(res?.open),
			reason: res?.closed_reason || '',
			// Закрытый раздел приходит без заданий. Прежний список оставляем:
			// у того, кто успел сделать всё, итог раздела считается по нему.
			tasks: res?.open ? [...(res.tasks || [])].sort((a, b) => a.n - b.n) : prev?.tasks || [],
		}))
	}, [])

	const poll = useCallback(
		async (signal) => {
			const n = ++sent.current
			try {
				apply(await my.tasks(attemptId, section, { signal }), n)
			} catch (err) {
				// Причина нужна, только пока показать нечего. Потом сбой опроса
				// экран не трогает: следующий через 5 секунд.
				if (!signal.aborted) setFeedError(err.message)
			}
		},
		[attemptId, section, apply],
	)

	usePoll(poll)

	/** Перечитать раздел сразу, не дожидаясь опроса. null — не вышло. */
	const reread = useCallback(async () => {
		const n = ++sent.current
		try {
			const res = await my.tasks(attemptId, section)
			apply(res, n)
			return res
		} catch {
			return null
		}
	}, [attemptId, section, apply])

	// Сколько ответов дано по каждому упражнению. Считаем по ответам урока,
	// а не ждём опроса: последний ответ должен засчитать упражнение сразу.
	const answeredBy = useMemo(() => {
		const out = {}
		for (const key of Object.keys(answers)) {
			const ex = key.slice(0, key.indexOf(':'))
			out[ex] = (out[ex] || 0) + 1
		}
		return out
	}, [answers])

	const isDone = useCallback(
		(t) =>
			Boolean(t.done) ||
			marked.has(t.key) ||
			(t.kind === 'exercise' && t.questions > 0 && (answeredBy[t.key.slice(3)] || 0) >= t.questions),
		[marked, answeredBy],
	)

	const steps = useMemo(
		() => (content && feed ? feed.tasks.map((t) => toStep(t, content)) : []),
		[content, feed],
	)
	const index = steps.findIndex((s) => s.task.key === pos)
	const current = index >= 0 ? steps[index] : null
	const live = Boolean(feed?.open)

	// Опрос меняет список, а позицию ученика трогает, только когда без этого
	// нельзя. Поэтому позицию и «сделано» эффект читает через ссылки: иначе
	// он срабатывал бы на каждый шаг ученика, а не на новый список.
	const posRef = useRef(pos)
	posRef.current = pos
	const doneRef = useRef(isDone)
	doneRef.current = isDone
	// Вернулись из разговора с аватаром — к заданию «Разговор», где осталось
	// нажать «К итогам», а не к первому несделанному.
	const wanted = useRef(location.state?.task || '')
	const seen = useRef(null)
	const wasOpen = useRef(false)

	useEffect(() => {
		if (!feed) return
		const reopened = !wasOpen.current
		wasOpen.current = feed.open
		if (!feed.open || !feed.tasks.length) return

		const tasks = feed.tasks
		// Новые задания от учителя называем словами: иначе «Задание 2 из 3»
		// тихо становится «2 из 4», и прибавки никто не замечает.
		if (seen.current) {
			const fresh = tasks.filter((t) => !seen.current.has(t.key)).length
			if (fresh) {
				toast(
					fresh === 1
						? 'Учитель выдал ещё задание'
						: `Учитель выдал ещё ${plural(fresh, 'задание', 'задания', 'заданий')}`,
				)
			}
		}
		seen.current = new Set([...(seen.current || []), ...tasks.map((t) => t.key)])

		const at = posRef.current
		if (tasks.some((t) => t.key === at)) return
		const left = tasks.find((t) => !doneRef.current(t))
		if (at === DONE) {
			// Всё было сделано, а учитель выдал ещё — сразу к новому.
			if (left) setPos(left.key)
			return
		}
		// Задание пропало из открытого раздела: учитель завершил одну из
		// выдач или отменил её. Без объяснения экран просто сменился бы.
		if (at !== null && !reopened) toast('Это задание учитель закрыл. Твои ответы сохранены.')
		const back = at === null ? tasks.find((t) => t.key === wanted.current) : null
		setPos(back ? back.key : left ? left.key : DONE)
	}, [feed, toast])

	// Сервер отказал по выдаче: задание закрылось, пока ученик был внутри.
	// Что показать дальше, решает свежий список: закрыт ли весь раздел или
	// только эта выдача. true — отказ разобран, общая ошибка не нужна.
	const settle = useCallback(
		async (err) => {
			const reason = refusal(err)
			if (!reason) return false
			const key = posRef.current
			const res = await reread()
			if (!res) {
				// Перечитать не вышло — верим отказу: задание закрыто.
				if (reason === 'not_issued') toast(err.message)
				else setFeed((prev) => ({ open: false, reason, tasks: prev?.tasks || [] }))
				return true
			}
			// Раздел открыт, и задание в нём осталось — называем причину словами
			// сервера: молча ничего бы не изменилось, и ученик жал бы снова.
			if (res.open && (res.tasks || []).some((t) => t.key === key)) toast(err.message)
			return true
		},
		[reread, toast],
	)
	settleRef.current = settle

	// Учитель видит «открыл» в таблице класса. Отмечаем раз за заход: ручка
	// идемпотентна, но дёргать её на каждое «Назад» незачем.
	//
	// Цели урока — вводный экран, а не задание: показали — значит, сделано.
	// Иначе раздел, где осталось только «прочитать цели», висел бы
	// несделанным, хотя в счёт заданий цели не входят.
	const opened = useRef(new Set())
	const currentKey = current?.task.key || ''
	useEffect(() => {
		if (!currentKey || !live || opened.current.has(currentKey)) return
		const task = feed.tasks.find((t) => t.key === currentKey)
		if (!task || doneRef.current(task)) return
		opened.current.add(currentKey)
		my.openTask(attemptId, currentKey)
			.then(() => {
				if (task.kind !== 'objectives') return
				return my.taskDone(attemptId, currentKey).then(() => setMarked((m) => new Set(m).add(currentKey)))
			})
			.catch(async (err) => {
				if (await settle(err)) return
				// Не отметилось — отметим, когда задание покажется ещё раз.
				opened.current.delete(currentKey)
			})
		// Список читаем в момент показа: новый опрос того же задания второй
		// раз его не открывает.
		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [attemptId, currentKey, live, settle])

	// Новое задание — с начала экрана: «Дальше» стоит внизу, и на телефоне
	// следующее задание иначе открывалось бы с середины.
	useEffect(() => {
		if (pos !== null) window.scrollTo(0, 0)
	}, [pos, view])

	// Номер шага — место задания в уроке, как и до выдач: по нему сервер
	// хранит ответ, и ответы прежних попыток с ним совпадают.
	const stepNo = current ? current.task.n - 1 : 0

	// Досылка очереди. Дошедшие ответы получают настоящую отметку «верно» или
	// «неверно», отказ по существу (учитель закрыл задание, пока не было
	// связи) убирает ответ и называет причину, сбой связи — новая попытка
	// позже: 3, 6, 12… секунд, не дольше минуты.
	const flush = useCallback(async () => {
		if (flushing.current || !queue.current.length) return
		flushing.current = true
		clearTimeout(retry.current.timer)
		let refused = null
		try {
			const { sent, dropped } = await flushPending([...queue.current], (p) => my.answer(attemptId, p, ANSWER_OPTS))
			const done = new Set([...sent, ...dropped].map((x) => pendingKey(x.item)))
			queue.current = queue.current.filter((p) => !done.has(pendingKey(p)))
			if (sent.length || dropped.length) {
				retry.current.delay = 0
				setAnswers((prev) => {
					const next = { ...prev }
					for (const { item, res } of sent) {
						next[pendingKey(item)] = { chosen: item.answer, correct: Boolean(res.correct), expected: res.expected ?? '' }
					}
					for (const { item } of dropped) delete next[pendingKey(item)]
					return next
				})
			}
			if (dropped.length) refused = dropped[0].err
		} finally {
			flushing.current = false
			savePending(attemptId, queue.current)
			setWaiting(queue.current.length)
		}
		if (refused && !(await settleRef.current(refused))) toast(refused.message)
		if (queue.current.length) {
			retry.current.delay = nextDelay(retry.current.delay)
			retry.current.timer = setTimeout(() => flushRef.current(), retry.current.delay)
		}
	}, [attemptId, toast])
	flushRef.current = flush

	// Связь вернулась или вкладку снова открыли — досылаем сразу, не дожидаясь
	// таймера.
	useEffect(() => {
		const again = () => flushRef.current()
		const visible = () => {
			if (document.visibilityState === 'visible') again()
		}
		window.addEventListener('online', again)
		document.addEventListener('visibilitychange', visible)
		const timers = retry.current
		return () => {
			window.removeEventListener('online', again)
			document.removeEventListener('visibilitychange', visible)
			clearTimeout(timers.timer)
		}
	}, [])

	const hold = useCallback(
		(item) => {
			queue.current = addPending(queue.current, item)
			savePending(attemptId, queue.current)
			setWaiting(queue.current.length)
			setAnswers((prev) => ({ ...prev, [pendingKey(item)]: waitingAnswer(item) }))
			clearTimeout(retry.current.timer)
			retry.current.timer = setTimeout(() => flushRef.current(), retry.current.delay || 3000)
		},
		[attemptId],
	)

	const record = useCallback(
		async (exerciseId, itemId, answer) => {
			try {
				const res = await my.answer(attemptId, { exerciseId, itemId, answer, step: stepNo }, ANSWER_OPTS)
				setAnswers((prev) => ({
					...prev,
					[answerKey(exerciseId, itemId)]: {
						chosen: answer,
						correct: Boolean(res.correct),
						expected: res.expected ?? '',
					},
				}))
				return res
			} catch (err) {
				// Связь пропала — ответ не теряем: он ложится в очередь, на
				// экране «сохранится, когда будет связь», отправка повторится сама.
				if (retryable(err)) {
					hold({ exerciseId, itemId, answer, step: stepNo })
					return { pending: true }
				}
				// Задание закрылось посреди упражнения — вместо общей ошибки
				// экран раздела: «учитель завершил работу», ответы сохранены.
				if (!(await settle(err))) toast(err.message)
				return null
			}
		},
		[attemptId, stepNo, toast, settle, hold],
	)

	// Ответ с откликом: серия верных подряд и плашка Ишки. Повтор того же
	// ответа (вторая вкладка) серию не двигает — счёт он тоже не двигает.
	const answer = useCallback(
		async (exerciseId, itemId, value) => {
			const res = await record(exerciseId, itemId, value)
			if (!res || res.repeated || res.pending) return res
			const s = streakRef.current
			const now = res.correct ? s.now + 1 : 0
			const next = { now, best: Math.max(s.best, now) }
			streakRef.current = next
			setStreak(next)
			if (res.correct) {
				if (now >= 3) showCheer('next', `Great! ${now} подряд`, 'Так держать, серия растёт')
				else showCheer('correct', 'Right!', 'Верно, идём дальше')
			}
			return res
		},
		[record, showCheer],
	)

	// Ответы упражнения — плоским объектом «вопрос → ответ».
	const answersOf = useCallback(
		(exerciseId) => {
			const prefix = `${exerciseId}:`
			const out = {}
			for (const [key, value] of Object.entries(answers)) {
				if (key.startsWith(prefix)) out[key.slice(prefix.length)] = value
			}
			return out
		},
		[answers],
	)

	const talk = useTalk(attemptId, current?.kind === 'talk')

	// Итог: свежие звёзды при входе и, пока считается разбор разговора,
	// раз в 5 секунд — недолго: потом это покажут «Результаты урока».
	const issue = sectionIssue(results.data?.issues, section)
	const pending = pos === DONE && wordsPending(issue)
	const polls = useRef(0)
	useEffect(() => {
		if (pos === DONE) {
			polls.current = 0
			loadResults()
		}
	}, [pos, loadResults])
	useEffect(() => {
		if (!pending || polls.current >= RESULTS_POLLS) return undefined
		const t = setTimeout(() => {
			polls.current += 1
			loadResults()
		}, 5000)
		return () => clearTimeout(t)
		// Зависимость — весь results, а не results.data: неудачный опрос данные
		// не меняет, и с results.data опрос замирал после первой же ошибки.
	}, [pending, results, loadResults])

	const tasks = feed?.tasks || []
	// Цели урока — вводный экран, не задание: в счёт не входят.
	const countable = tasks.some((t) => t.kind !== 'objectives') ? tasks.filter((t) => t.kind !== 'objectives') : tasks

	// Разбор ошибок — по упражнениям этого раздела и в порядке урока: чужие
	// ошибки из прошлой выдачи к этому итогу не относятся.
	const misses = useMemo(() => {
		if (!content) return []
		const own = new Set(tasks.filter((t) => t.kind === 'exercise').map((t) => t.key.slice(3)))
		return mistakesOf(content, answers, own)
	}, [content, answers, tasks])

	async function next() {
		const task = current.task
		// Задание без вопросов засчитывается кнопкой: прочитал — дальше.
		// Упражнение засчитывает сервер по ответам, и кнопка его не трогает:
		// пролистать упражнение можно, сделанным оно от этого не станет.
		let mark = live && task.questions === 0 && !isDone(task)
		if (mark) {
			setMoving(true)
			try {
				await my.taskDone(attemptId, task.key)
				setMarked((m) => new Set(m).add(task.key))
			} catch (err) {
				setMoving(false)
				if (await settle(err)) return
				toast(err.message)
				// Другой отказ сервера повтором не исправить — идём дальше, не
				// засчитав. Сбой связи — остаёмся: нажать ещё раз имеет смысл.
				if (err.status !== 409) return
				mark = false
			}
			setMoving(false)
		}
		if (index < steps.length - 1) {
			setPos(steps[index + 1].task.key)
			return
		}
		// С последнего задания — к первому несделанному: учитель мог выдать
		// задание раньше этого, или ученик ушёл с упражнения, не ответив.
		const left = steps.find((s) => !isDone(s.task) && !(mark && s.task.key === task.key))
		if (!left) {
			setFinish((f) => ({ ...f, arrived: true }))
			setPos(DONE)
			return
		}
		setPos(left.task.key)
		// Остались на том же задании — подсказать, почему. Задание без
		// вопросов здесь остаётся, только если сервер отказал, и причину он
		// уже назвал.
		if (left.task.key !== task.key) {
			const n = countable.findIndex((t) => t.key === left.task.key) + 1
			toast(n > 0 ? `Задание ${n} ещё не сделано` : 'Сначала посмотри цели урока')
		} else if (task.questions > 0) toast('Сначала ответь на все вопросы задания')
	}

	// «Почти всё!» → «Завершить»: ответы и отметки уже записаны, кнопка
	// перечитывает раздел и выдачу и показывает итог с конфетти.
	async function confirm() {
		setFinish((f) => ({ ...f, saving: true }))
		// Сначала досылаем отложенные ответы: итог должен их видеть. Связи
		// всё ещё нет — итог покажем, а ответы уйдут позже сами.
		await flushRef.current()
		if (queue.current.length) {
			toast(`Нет связи: не отправлено ответов — ${queue.current.length}. Они уйдут сами, когда появится интернет`)
		}
		await Promise.all([reread(), loadResults()])
		setFinish((f) => ({ ...f, saving: false, confirmed: true, party: f.party + 1 }))
	}

	const exit = () => navigate('/student')

	// Выход с итога: результаты посмотрены — тут и спрашиваем, как прошёл
	// урок. Пока итог ещё «Почти всё!», выход обычный: результатов ученик
	// не видел. Отметку ставим при показе: закрыл окно — второй раз про то
	// же не спросим.
	const leave = () => {
		const storage = localStore()
		const talked = (feed?.tasks || []).some((t) => t.kind === 'dialog' && isDone(t))
		const questions = questionsToAsk(attemptId, talked, storage)
		if (!questions.length) {
			exit()
			return
		}
		markAsked(attemptId, questions, storage)
		setAsking(questions)
	}

	// Название урока приходит из «Моих занятий» или из результатов урока. В
	// содержимом урока лежит название версии — служебная пометка учителя при
	// публикации («исправила опечатки»), ученику её не показывают; она
	// остаётся запасной, только если результатов прочитать не вышло.
	const lessonTitle =
		location.state?.title ||
		results.data?.lesson?.title ||
		(assignmentId === '' || results.failed ? content?.version?.title : '') ||
		''

	// Раздел закрыт или пуст — сообщение вместо заданий. Исключение — итог:
	// ученик сделал всё, и закрытие раздела ничего у него не отнимает.
	if (feed && pos !== DONE && (!feed.open || !feed.tasks.length)) {
		return <SectionClosed reason={feed.open ? '' : feed.reason} section={section} title={lessonTitle} />
	}

	if (failed || (feedError && !feed)) {
		return (
			<StudentFrame>
				<LessonFrame stage='text'>
					<h2 className='h2'>Урок не открылся</h2>
					<div className='lk-sub'>{failed || feedError}</div>
					<div className='lk-actions'>
						<Button variant='primary' to='/student'>
							К моим занятиям
						</Button>
					</div>
				</LessonFrame>
			</StudentFrame>
		)
	}

	if (!content || !feed || (pos !== DONE && !current)) {
		return (
			<StudentFrame>
				<LessonFrame stage='text'>
					<div className='lk-sub'>Загружаем урок…</div>
				</LessonFrame>
			</StudentFrame>
		)
	}

	const back = (onClick, disabled = false) => (
		<button className='btn-icon' type='button' aria-label='Назад' title='Назад' disabled={disabled} onClick={onClick}>
			<Icon name='back' />
		</button>
	)

	if (pos === DONE && view === 'mistakes') {
		return (
			<StudentFrame>
				<LessonFrame
					stage='finish'
					enter='mistakes'
					top={<FrameTop eyebrow='Мои ошибки' sub={lessonTitle} onExit={exit} />}
					foot={back(() => setView(''))}
				>
					<h2 className='h2'>Мои ошибки</h2>
					<div className='lk-sub'>
						{[lessonTitle, SECTION_LABEL[section]?.toLowerCase(), 'верный ответ — зелёным, твой — красным']
							.filter(Boolean)
							.join(' · ')}
					</div>
					<MistakesList list={misses} />
				</LessonFrame>
			</StudentFrame>
		)
	}

	const at = pos === DONE ? null : current.task.key
	const bar = stageBar(tasks, at, isDone)
	const top = (
		<FrameTop
			eyebrow={lessonTitle}
			sub={stepLine(section, tasks, at)}
			streak={streak.now}
			onExit={pos === DONE && !(finish.arrived && !finish.confirmed) ? leave : exit}
			stages={bar}
			busy={moving}
			onStage={(s) => {
				if (s.key === 'finish') setPos(DONE)
				else if (s.first) setPos(s.first)
			}}
		/>
	)

	if (pos === DONE) {
		return (
			<StudentFrame cheer={cheer} party={finish.party}>
				<LessonFrame stage='finish' enter='done' top={top}>
					<IssueFinish
						section={section}
						title={lessonTitle}
						total={countable.length}
						done={countable.filter(isDone).length}
						issue={issue}
						loading={Boolean(section) && !results.failed && Boolean(assignmentId)}
						almost={finish.arrived && !finish.confirmed}
						saving={finish.saving}
						onConfirm={confirm}
						party={finish.party}
						mistakes={misses.length}
						onMistakes={() => setView('mistakes')}
						best={streak.best}
						onLeave={leave}
					/>
				</LessonFrame>
				{asking && <StudentFeedback attemptId={attemptId} questions={asking} onDone={exit} />}
			</StudentFrame>
		)
	}

	// Внизу «←» и «Дальше». Упражнение ведёт себя само — «Следующий
	// пропуск», в конце «Дальше →» внутри, — а разговор обязателен: на этих
	// шагах внизу только «←».
	const foot = (
		<>
			{/* Пока «Дальше» сохраняет отметку, назад не пускаем: ответ
			    сервера переставил бы ученика с уже другого задания. */}
			{back(() => setPos(steps[index - 1].task.key), index === 0 || moving)}
			{!SELF_LED.has(current.kind) && (
				<Button variant='primary' disabled={moving} onClick={next}>
					{moving ? 'Сохраняем…' : 'Дальше'}
				</Button>
			)}
		</>
	)

	return (
		<StudentFrame cheer={cheer}>
			<LessonFrame stage={stageOf(current.task.kind)} enter={current.task.key} top={top} foot={foot}>
				{waiting > 0 && (
					<div className='lk-offline' role='status'>
						Нет связи с сервером. Не отправлено ответов: {waiting}. Они сохранены на этом устройстве и уйдут
						сами, как только появится интернет.
					</div>
				)}
				{current.kind === 'objectives' && <ObjectivesStep content={content} title={lessonTitle} />}
				{current.kind === 'text' && <TextStep content={content} />}
				{current.kind === 'lexicon' && <LexiconStep content={content} />}
				{current.kind === 'matching' && (
					<MatchingExercise
						key={current.ex.id}
						exercise={current.ex}
						given={answersOf(current.ex.id)}
						onAnswer={answer}
						onForward={next}
					/>
				)}
				{current.kind === 'quiz' && (
					<QuizExercise
						key={current.ex.id}
						exercise={current.ex}
						given={answersOf(current.ex.id)}
						onAnswer={answer}
						onForward={next}
					/>
				)}
				{current.kind === 'custom' && <CustomStep exercise={current.ex} />}
				{current.kind === 'grammar' && <GrammarStep content={content} />}
				{current.kind === 'talk' && (
					<TalkStep
						attemptId={attemptId}
						section={section}
						title={lessonTitle}
						content={content}
						talk={talk}
						busy={moving}
						onFinish={next}
					/>
				)}
				{current.kind === 'missing' && <MissingStep task={current.task} />}
			</LessonFrame>
		</StudentFrame>
	)
}

/**
 * StudentFrame — страница экрана ученика: шапка с логотипом, карточка урока
 * и то, что висит поверх неё, — плашка Ишки и конфетти.
 */
function StudentFrame({ children, cheer = null, party = 0 }) {
	return (
		<AuthShell className='lk-wrap-flush'>
			<div className='lk-wrap'>{children}</div>
			<Cheer cheer={cheer} />
			<Confetti fire={party} />
		</AuthShell>
	)
}

/** localStorage или null: в закрытом режиме браузера само обращение бросает. */
function localStore() {
	try {
		return window.localStorage
	} catch {
		return null
	}
}

/**
 * SectionClosed — раздел закрыт или в нём пусто.
 *
 * Показывается и посреди задания: учитель нажал «Завершить работу на
 * уроке», а ученик ещё отвечал. Ответы, данные до этого, сервер сохранил, и
 * экран говорит об этом прямо — вместо ошибки, которую ребёнок принял бы за
 * поломку.
 */
function SectionClosed({ reason, section, title }) {
	const [head, sub] = closedText(reason, section)
	return (
		<StudentFrame>
			<LessonFrame stage='finish'>
				<div className='lk-finish'>
					{title && <div className='auth-eyebrow'>{title}</div>}
					<Avatar mood='finish' size={120} className='lk-avatar' />
					<h2 className='h2 mt16'>{head}</h2>
					<div className='lk-sub'>{sub}</div>
					<div className='lk-actions'>
						<Button variant='primary' to='/student'>
							К моим занятиям
						</Button>
					</div>
				</div>
			</LessonFrame>
		</StudentFrame>
	)
}
