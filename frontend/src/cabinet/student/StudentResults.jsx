// StudentResults.jsx — «Результаты урока»: звёзды по выдачам, как прошли
// упражнения и разговор, что выучено, «Мои ошибки» и повтор выражений
// (docs/product/student-screens/story.md, JOB-11 и JOB-12).
//
// Только просмотр: ответить заново отсюда нельзя. Открывается из «Моих
// занятий» — нажатием на сделанную выдачу или завершённый урок.
//
// Звёзды и счёт выдач даёт сервер (/my/lessons/{id}/results): там же
// закрытые выдачи, которых в разделах «Моих занятий» уже нет. Ошибки и
// выражения — из попытки и содержимого урока, теми же ручками, что и
// прохождение. Ученик, так и не открывший урок, попытки не имеет: у него
// только звёзды и счёт.
//
// Подэкраны — в адресе (/mistakes, /cards): «Назад» в браузере и на телефоне
// возвращает к результатам, а не в «Мои занятия».

import { useEffect, useMemo, useState } from 'react'
import { useLocation, useNavigate, useParams } from 'react-router-dom'
import { my } from '../../api/v1'
import { AuthShell } from '../Shell'
import Icon from '../icons'
import { Button } from '../ui'
import { dayOf, exerciseName, issueLine, litStars, mistakesOf, restoreAnswers } from './lesson/kit'
import { FrameTop, LessonFrame, MistakesButton, MistakesList, StarsGrid } from './lesson/parts'
import { Flashcards } from './lesson/steps'
import './lesson/lesson.css'

/** «из 10 слов», «из 1 слова». */
const wordsOf = (n) => (n % 10 === 1 && n % 100 !== 11 ? 'слова' : 'слов')

export default function StudentResults() {
	const { assignmentId, view = '' } = useParams()
	const navigate = useNavigate()
	const location = useLocation()

	const [data, setData] = useState(null)
	const [failed, setFailed] = useState('')
	const [content, setContent] = useState(null)
	const [answers, setAnswers] = useState({})

	useEffect(() => {
		let gone = false
		my.results(assignmentId)
			.then((res) => {
				if (gone) return
				setData(res)
				const attemptId = res.lesson?.attempt_id
				if (!attemptId) return
				my.lesson(attemptId)
					.then((c) => !gone && setContent(c))
					.catch(() => {})
				my.attempt(attemptId)
					.then((a) => !gone && setAnswers(restoreAnswers(a.attempt)))
					.catch(() => {})
			})
			.catch((err) => !gone && setFailed(err.message))
		return () => {
			gone = true
		}
	}, [assignmentId])

	// Все ошибки урока: каждое неверно отвеченное, по упражнениям в порядке
	// урока.
	const misses = useMemo(() => (content ? mistakesOf(content, answers) : []), [content, answers])

	const title = data?.lesson?.title || location.state?.title || ''
	const base = `/student/results/${assignmentId}`
	const go = (to) => navigate(to, { state: { title } })
	const home = () => navigate('/student')
	const back = (onClick) => (
		<button className='btn-icon' type='button' aria-label='Назад' title='Назад' onClick={onClick}>
			<Icon name='back' />
		</button>
	)

	if (failed || !data) {
		return (
			<Page>
				<LessonFrame stage='finish' top={<FrameTop eyebrow='Результаты урока' sub={title} onExit={home} />}>
					{failed ? (
						<>
							<h2 className='h2'>Результаты не открылись</h2>
							<div className='lk-sub'>{failed}</div>
							<div className='lk-actions'>
								<Button variant='primary' to='/student'>
									К моим занятиям
								</Button>
							</div>
						</>
					) : (
						<div className='lk-sub'>Загружаем…</div>
					)}
				</LessonFrame>
			</Page>
		)
	}

	if (view === 'mistakes') {
		return (
			<Page>
				<LessonFrame
					stage='exercise'
					enter='mistakes'
					top={<FrameTop eyebrow='Мои ошибки' sub={title} onExit={home} />}
					foot={back(() => go(base))}
				>
					<h2 className='h2'>Мои ошибки</h2>
					<div className='lk-sub'>{title} · верный ответ — зелёным, твой — красным</div>
					{content || !data.lesson.attempt_id ? <MistakesList list={misses} /> : <div className='lk-sub'>Загружаем…</div>}
				</LessonFrame>
			</Page>
		)
	}

	if (view === 'cards') {
		return (
			<Page>
				<LessonFrame
					stage='lexicon'
					enter='cards'
					top={<FrameTop eyebrow='Повторение' sub={title} onExit={home} />}
					foot={back(() => go(base))}
				>
					<h2 className='h2'>Выражения урока</h2>
					<div className='lk-sub'>Нажми на карточку, чтобы перевернуть. Листай свайпом или стрелками.</div>
					{content ? <Flashcards items={content.lexicon || []} /> : <div className='lk-sub'>Загружаем…</div>}
				</LessonFrame>
			</Page>
		)
	}

	const { lesson, stars } = data
	// Блок — на каждую сделанную или закрытую выдачу: у той, что ещё
	// делается, звёзд пока нет.
	const settled = (data.issues || []).filter((i) => i.settled)
	const rows = settled.flatMap((i) =>
		i.tasks
			.filter((t) => t.kind === 'exercise' && t.questions > 0)
			.map((t) => ({ key: t.key, name: exerciseName(t), correct: t.correct, total: t.questions })),
	)
	const talk = settled.find((i) => i.words)?.words || null
	const lexicon = content?.lexicon || []
	const grammar = content?.grammar || []

	return (
		<Page>
			<LessonFrame
				stage='finish'
				enter='results'
				top={<FrameTop eyebrow='Результаты урока' sub={title} onExit={home} />}
				foot={back(home)}
			>
				<h2 className='h2'>{title}</h2>
				<div className='lk-sub'>
					{[lesson.finished ? `Пройден ${dayOf(lesson.issued_at)}` : 'Урок ещё идёт', lesson.class?.name]
						.filter(Boolean)
						.join(' · ')}{' '}
					· <span className='badge badge-gray'>только просмотр</span>
				</div>

				<div className='lk-box'>
					<div className='lk-box-h'>
						<b>Звёзды за урок</b>
						<span>
							⭐ {stars?.earned ?? 0} из {stars?.max ?? 0}
						</span>
					</div>
				</div>
				{lesson.attempt_id && <MistakesButton count={misses.length} onOpen={() => go(`${base}/mistakes`)} />}

				{settled.map((i, k) => (
					<div className='lk-box' key={k}>
						<div className='lk-box-h'>
							<b>{issueLine(i)}</b>
							<span>
								⭐ {litStars(i.stars)} из {i.stars.length}
							</span>
						</div>
						<StarsGrid issue={i} />
						{/* Без рода: «ты всё сделал» половина класса прочитала бы
						    не про себя. */}
						{i.closed && i.tasks_done < i.tasks_total && (
							<div className='lk-closed-note'>Учитель завершил выдачу раньше, чем всё было сделано.</div>
						)}
					</div>
				))}

				{(rows.length > 0 || talk) && (
					<div className='lk-box'>
						<div className='lk-label'>Как прошло</div>
						{rows.map((r) => (
							<div className='lk-rrow' key={r.key}>
								<div className='lk-rrow-nm'>
									{r.name}
									<div className='bar'>
										<i style={{ width: `${r.total ? Math.round((r.correct / r.total) * 100) : 0}%` }} />
									</div>
								</div>
								<div className='lk-rrow-v'>
									{r.correct}/{r.total}
								</div>
							</div>
						))}
						{talk && (
							<div className='lk-rrow'>
								<div className='lk-rrow-nm'>
									Разговор с аватаром
									<div className='bar'>
										<i
											style={{
												width: `${talk.state === 'ready' && talk.total ? Math.round((talk.used / talk.total) * 100) : 0}%`,
											}}
										/>
									</div>
								</div>
								<div className='lk-rrow-v'>
									{talk.state === 'ready'
										? `${talk.used} из ${talk.total} ${wordsOf(talk.total)}`
										: talk.state === 'pending'
											? 'считаем…'
											: 'не было'}
								</div>
							</div>
						)}
					</div>
				)}

				{(lexicon.length > 0 || grammar.length > 0) && (
					<div className='lk-box'>
						{/* «Ты выучил» из макета — без рода: «Теперь ты знаешь». */}
						<div className='lk-label'>Теперь ты знаешь</div>
						{lexicon.length > 0 && (
							<div className='lk-tags' lang='tt'>
								{lexicon.map((u, k) => (
									<span className='lk-tag' key={u.id || k}>
										{u.phrase}
									</span>
								))}
							</div>
						)}
						{grammar.length > 0 && (
							<div className='lk-tags'>
								{grammar.map((g, k) => (
									<span className='lk-tag is-green' key={g.id || k}>
										<Icon name='puzzle' size={14} />
										{g.title}
									</span>
								))}
							</div>
						)}
					</div>
				)}

				{lexicon.length > 0 && (
					<div className='lk-actions'>
						<Button variant='primary' icon='refresh' onClick={() => go(`${base}/cards`)}>
							Повторить выражения
						</Button>
					</div>
				)}
			</LessonFrame>
		</Page>
	)
}

/** Page — страница экрана ученика: шапка с логотипом и карточка. */
function Page({ children }) {
	return (
		<AuthShell className='lk-wrap-flush'>
			<div className='lk-wrap'>{children}</div>
		</AuthShell>
	)
}
