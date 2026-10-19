// StudentHome.jsx — «Мои занятия»: что учитель выдал сейчас на уроке, что
// задано на дом и что уже закрыто, со звёздами ученика
// (docs/product/student-screens/story.md, JOB-1).
//
// Урок у ученика один, а задания учитель выдаёт частями: одно-два на уроке,
// остальное на дом (docs/product/class-lessons/story.md, JOB-8). Поэтому
// разделы делят не уроки, а их задания по выдачам: один и тот же урок может
// стоять и в «Сейчас на уроке», и в «Домашней работе» — ответы всё равно
// копятся в одну попытку. Урок, в котором ничего не выдано, сервер не
// присылает вовсе: ученик не должен видеть то, что учитель только назначил.
//
// Урок завершён, когда выданы и закрыты все его задания (`finished`): это
// решает сервер, а экран только раскладывает то, что пришло. Сделанная
// выдача остаётся в своём разделе с меткой «готово ✓» и своими звёздами;
// нажатие на неё, как и на завершённый урок, открывает «Результаты урока».
//
// Список обновляется сам раз в 5 секунд, пока экран на виду: учитель жмёт
// «Выдать на уроке», и у ученика урок появляется без перезагрузки.
//
// Число заданий пишем всегда. «Начать» без числа обещает неизвестно сколько
// работы, а «Продолжить» без остатка не говорит, много ли ещё осталось.

import { useCallback, useEffect, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { my, student as studentApi } from '../../api/v1'
import { useAuth } from '../AuthContext'
import { AuthShell } from '../Shell'
import { Button, useToast } from '../ui'
import { dayOf, dueText, tasksWord } from './lesson/kit'
import { LessonFrame } from './lesson/parts'
import { usePoll } from './poll'
import './lesson/lesson.css'

/** «все 3 задания сделаны». Про одно задание «все» не говорят. */
const allDone = (n) => (n === 1 ? '1 задание сделано' : `все ${tasksWord(n)} сделаны`)

/**
 * Счёт заданий раздела словами.
 *
 * Не начинал — сколько их всего; начал — сколько осталось из скольких;
 * сделал всё — что сделано всё. Начатость решает сервер (`begun`): открытое
 * и не доделанное задание — уже начало, хотя сделанных ещё нет.
 */
function sectionCount(s) {
	if (s.left <= 0) return allDone(s.tasks)
	if (!s.begun) return tasksWord(s.tasks)
	return `осталось ${tasksWord(s.left)} из ${s.tasks}`
}

/** Звёзды выдачи маленькими значками: горят заработанные. */
function MiniStars({ stars }) {
	if (!stars?.length) return null
	const lit = stars.filter((s) => s.on).length
	return (
		<span role='img' aria-label={`звёзд ${lit} из ${stars.length}`}>
			{stars.map((s) => (
				<span key={s.kind} className={`lk-ms${s.on ? ' is-on' : ''}`} aria-hidden='true'>
					⭐
				</span>
			))}
		</span>
	)
}

/** Строка завершённого урока: «⭐ 4 из 5 · 29.09 · 86% верных». */
function finishedLine(l) {
	const parts = [`⭐ ${l.stars?.earned ?? 0} из ${l.stars?.max ?? 0}`]
	const day = dayOf(l.issued_at)
	if (day) parts.push(day)
	if (l.correct_pct !== null && l.correct_pct !== undefined) parts.push(`${l.correct_pct}% верных`)
	return parts.join(' · ')
}

export default function StudentHome() {
	const { actor, logout } = useAuth()
	const navigate = useNavigate()
	const toast = useToast()

	const [classes, setClasses] = useState(null)
	const [lessons, setLessons] = useState(null)
	const [failed, setFailed] = useState(false)
	const [opening, setOpening] = useState('')
	// Пришёл ли хоть один список. После этого сбой опроса экран не трогает:
	// на нём остаётся то, что уже было, а следующий опрос через 5 секунд.
	const loaded = useRef(false)
	// Об ошибке первой загрузки говорим один раз, а не каждые 5 секунд.
	const warned = useRef(false)

	// Классы нужны приветствию («7А · #4821») и пустому экрану: «ты не в
	// классе» и «ничего не выдано» — разные советы. Их не опрашиваем: за урок
	// состав классов ученика не меняется.
	useEffect(() => {
		studentApi
			.myClasses()
			.then((res) => setClasses(res.classes || []))
			.catch(() => setClasses(null))
	}, [])

	const load = useCallback(
		async (signal) => {
			try {
				const res = await my.lessons('', { signal })
				loaded.current = true
				setLessons(res.lessons || [])
				setFailed(false)
			} catch (err) {
				if (signal.aborted || loaded.current) return
				// Отличаем «ничего не выдано» от «не смогли прочитать»: во
				// втором случае писать «заданий нет» — прямая неправда.
				setLessons([])
				setFailed(true)
				if (!warned.current) {
					warned.current = true
					toast(err.message)
				}
			}
		},
		[toast],
	)

	usePoll(load)

	async function open(lesson, section) {
		setOpening(`${lesson.assignment_id}:${section}`)
		try {
			const res = await my.start(lesson.assignment_id)
			const attempt = res.attempt || res
			// Раздел едет в адресе: урок показывает только его задания, и
			// перезагрузка страницы не должна выкидывать в «все задания».
			// Название урока — следом: в содержимом урока лежит название
			// версии, а это служебная пометка учителя, не для ученика.
			navigate(`/student/lesson/${attempt.id}?section=${section}`, {
				state: { title: lesson.title },
			})
		} catch (err) {
			toast(err.message)
			setOpening('')
		}
	}

	const results = (lesson) =>
		navigate(`/student/results/${lesson.assignment_id}`, { state: { title: lesson.title } })

	const all = lessons || []
	const live = all.filter((l) => l.class_section?.tasks > 0)
	const home = all.filter((l) => l.home_section?.tasks > 0)
	const closed = all.filter((l) => l.finished)
	const nothing = !live.length && !home.length && !closed.length
	// Не в классе — только если список классов пришёл и он пуст. Не
	// прочитали — советовать вступить в класс нельзя: может, он уже есть.
	const outside = Array.isArray(classes) && classes.length === 0
	const stars = all.reduce((n, l) => n + (l.stars?.earned || 0), 0)
	const who = [(classes || []).map((c) => c.name).filter(Boolean).join(', '), actor?.public_tag]
		.filter(Boolean)
		.join(' · ')

	// Строка урока в открытом разделе. Сделано всё — метка «готово ✓» и
	// звёзды выдачи вместо кнопки: открывать раздел незачем, а «Продолжить»
	// обещало бы ещё работу. Нажатие открывает результаты урока.
	const row = (l, section, s, meta) => {
		const id = `${l.assignment_id}:${section}`
		const now = section === 'class' ? ' is-now' : ''
		if (s.left <= 0) {
			return (
				<button className={`lk-row${now}`} key={id} type='button' onClick={() => results(l)}>
					<div className='lk-row-main'>
						<div className='lk-row-t'>{l.title}</div>
						<div className='lk-row-m'>
							{meta.filter(Boolean).map((m, i) => (
								<span key={i}>
									{i > 0 && ' · '}
									{m}
								</span>
							))}{' '}
							<MiniStars stars={s.stars} />
						</div>
					</div>
					<span className='badge badge-green'>готово ✓</span>
				</button>
			)
		}
		return (
			<div className={`lk-row${now}`} key={id}>
				<div className='lk-row-main'>
					<div className='lk-row-t'>{l.title}</div>
					<div className='lk-row-m'>
						{meta.filter(Boolean).map((m, i) => (
							<span key={i}>
								{i > 0 && ' · '}
								{m}
							</span>
						))}
					</div>
				</div>
				<Button variant='primary' disabled={opening === id} onClick={() => open(l, section)}>
					{opening === id ? 'Открываем…' : s.begun ? 'Продолжить' : 'Начать'}
				</Button>
			</div>
		)
	}

	return (
		<AuthShell className='lk-wrap-flush'>
			<div className='lk-wrap'>
				<LessonFrame stage='text'>
					<div className='lk-hi'>
						<img className='lk-wave' src='/avatar/wave.png' width={56} height={56} alt='Аватар машет' />
						<div>
							<h1 className='h2'>Hello, {actor?.display_name}!</h1>
							{who && <div className='tiny'>{who}</div>}
						</div>
						<div className='lk-total' title='Все твои звёзды' aria-label={`Все твои звёзды: ${stars}`}>
							⭐ {stars}
						</div>
					</div>

					{lessons === null ? (
						<div className='lk-sub mt16'>Загружаем…</div>
					) : failed ? (
						<div className='empty mt16'>Не удалось загрузить занятия. Обнови страницу или войди заново.</div>
					) : nothing ? (
						<div className='empty mt16'>
							{outside
								? 'Ты пока не в классе. Попроси учителя добавить тебя.'
								: 'Сейчас заданий нет. Когда учитель их выдаст, они появятся здесь сами.'}
						</div>
					) : (
						<>
							{/* «Сейчас на уроке» есть, только пока учитель держит открытой
							    выдачу на уроке: пустой раздел на перемене сбивал бы с
							    толку — «урок идёт, а заданий нет». */}
							{live.length > 0 && (
								<section className='lk-sec'>
									<h2 className='lk-sec-h'>
										<i className='lk-dot' aria-hidden='true' />
										Сейчас на уроке
									</h2>
									{live.map((l) =>
										row(l, 'class', l.class_section, [l.class?.name, sectionCount(l.class_section)]),
									)}
								</section>
							)}

							<section className='lk-sec'>
								<h2 className='lk-sec-h'>
									Домашняя работа {home.length > 0 && <small>{home.length}</small>}
								</h2>
								{home.length > 0 ? (
									home.map((l) =>
										row(l, 'home', l.home_section, [
											<span className='lk-due' key='due'>
												{dueText(l.home_section.due_at)}
											</span>,
											sectionCount(l.home_section),
										]),
									)
								) : (
									<div className='lk-empty'>Домашней работы сейчас нет.</div>
								)}
							</section>

							{closed.length > 0 && (
								<section className='lk-sec'>
									<h2 className='lk-sec-h'>
										Завершённые <small>{closed.length}</small>
									</h2>
									{/* Все выдачи закрыты, отвечать больше негде — строка
									    открывает результаты урока: звёзды, ошибки, повтор. */}
									{closed.map((l) => (
										<button className='lk-row' key={l.assignment_id} type='button' onClick={() => results(l)}>
											<div className='lk-row-main'>
												<div className='lk-row-t'>{l.title}</div>
												<div className='lk-row-m'>{finishedLine(l)}</div>
											</div>
											<span className='lk-chev' aria-hidden='true'>
												›
											</span>
										</button>
									))}
								</section>
							)}
						</>
					)}

					<div className='lk-home-foot'>
						<button
							type='button'
							onClick={async () => {
								await logout()
								navigate('/login', { replace: true })
							}}
						>
							Выйти с этого устройства
						</button>
					</div>
				</LessonFrame>
			</div>
		</AuthShell>
	)
}
