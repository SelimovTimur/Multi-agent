// exercises.jsx — упражнения с проверкой: «Найди пары», «Вставь
// пропущенное», «Продолжи фразу» (docs/product/student-screens/story.md,
// JOB-4…JOB-8).
//
// Правильность решает сервер: ключа ответов на клиенте нет. Ответ на
// вопрос даётся один раз — вернувшись к упражнению, ученик видит прежние
// ответы с теми же цветами, а ответить заново не может: кнопки отвеченного
// выключены, и повторный ответ на сервер не уходит.

import { useEffect, useMemo, useRef, useState } from 'react'
import { Button } from '../../ui'
import Avatar from '../Avatar'
import { gapParts, leadOf } from './kit'
import { pulse } from './parts'

/** Перемешивание: пары не должны стоять напротив своих переводов. */
function shuffled(list) {
	const out = [...list]
	for (let i = out.length - 1; i > 0; i -= 1) {
		const j = Math.floor(Math.random() * (i + 1))
		;[out[i], out[j]] = [out[j], out[i]]
	}
	return out
}

/**
 * MatchingExercise — «Найди пары».
 *
 * Слева выражение, справа переводы вперемешку. Выбранное выражение — тёмной
 * рамкой. Верная пара зеленеет и подпрыгивает. Ошибка: выбранный перевод
 * трясётся, выражение краснеет с «верно: …», а верный перевод гаснет — пара
 * разобрана; ошибочно выбранный перевод остаётся доступным.
 *
 * given — ответы упражнения: левая часть пары → { chosen, correct, expected }.
 */
export function MatchingExercise({ exercise, given, onAnswer, onForward }) {
	const items = exercise.payload?.items || []
	// eslint-disable-next-line react-hooks/exhaustive-deps
	const options = useMemo(() => shuffled(items.map((it) => it.translation)), [exercise.id])
	const [picked, setPicked] = useState(null)
	const [busy, setBusy] = useState(false)
	const tiles = useRef({})
	const [hit, setHit] = useState(null)

	// Отклик — после перерисовки: плитка уже в новом цвете.
	useEffect(() => {
		if (!hit) return
		for (const key of hit.keys) pulse(tiles.current[key], hit.cls)
	}, [hit])

	const answered = items.filter((it) => given[it.phrase])
	const paired = new Set(answered.filter((it) => given[it.phrase].correct).map((it) => given[it.phrase].chosen))
	const spent = new Set(
		answered.filter((it) => !given[it.phrase].correct && !given[it.phrase].pending).map((it) => given[it.phrase].expected),
	)
	// Пара без связи ждёт отправки: перевод занят, но верна ли пара, скажет сервер.
	const waiting = new Set(answered.filter((it) => given[it.phrase].pending).map((it) => given[it.phrase].chosen))

	async function choose(translation, index) {
		if (!picked || busy) return
		const phrase = picked
		setBusy(true)
		const res = await onAnswer(exercise.id, phrase, translation, {})
		setPicked(null)
		setBusy(false)
		if (!res || res.pending) return
		setHit(
			res.correct
				? { keys: [`l:${phrase}`, `r:${index}`], cls: 'lk-bounce', n: Date.now() }
				: { keys: [`r:${index}`, `l:${phrase}`], cls: 'lk-shake', n: Date.now() },
		)
	}

	const all = items.length > 0 && answered.length === items.length
	return (
		<div>
			<h2 className='h2'>Найди пары</h2>
			<div className='lk-sub'>
				{picked ? 'Теперь выбери перевод справа' : 'Выбери выражение слева, потом перевод справа'} ·{' '}
				{answered.length} из {items.length}
			</div>

			<div className='lk-match'>
				<div className='lk-col'>
					{items.map((it) => {
						const state = given[it.phrase]
						let cls = ''
						if (state) cls = state.pending ? ' is-wait' : state.correct ? ' is-ok' : ' is-bad'
						else if (picked === it.phrase) cls = ' is-pick'
						return (
							<button
								key={it.phrase}
								ref={(el) => {
									tiles.current[`l:${it.phrase}`] = el
								}}
								className={`lk-tile${cls}`}
								type='button'
								lang='tt'
								disabled={Boolean(state) || busy}
								aria-pressed={picked === it.phrase}
								onClick={() => setPicked(it.phrase)}
							>
								{it.phrase}
								{state?.pending && (
									<small className='lk-wait' lang='ru'>
										сохраним, когда будет связь
									</small>
								)}
								{state && !state.correct && !state.pending && <small lang='ru'>верно: {state.expected || '—'}</small>}
							</button>
						)
					})}
				</div>
				<div className='lk-col'>
					{options.map((t, index) => {
						const ok = paired.has(t)
						const wait = !ok && waiting.has(t)
						const used = !ok && !wait && spent.has(t)
						return (
							<button
								key={index}
								ref={(el) => {
									tiles.current[`r:${index}`] = el
								}}
								className={`lk-tile${ok ? ' is-ok' : wait ? ' is-wait' : used ? ' is-used' : ''}`}
								type='button'
								disabled={ok || wait || used || !picked || busy}
								onClick={() => choose(t, index)}
							>
								{t}
							</button>
						)
					})}
				</div>
			</div>

			{all && (
				<>
					<div className='lk-row-hi mt16'>
						<Avatar mood='next' size={56} className='lk-avatar' />
						<b>Все пары собраны!</b>
					</div>
					<div className='lk-actions'>
						<Button variant='primary' onClick={onForward}>
							Дальше →
						</Button>
					</div>
				</>
			)}
		</div>
	)
}

/** Подписи вопросов по виду упражнения. */
const QUIZ = {
	gap: {
		title: 'Вставь пропущенное',
		hint: 'Выбери выражение, которое подходит по смыслу',
		label: 'Пропуск',
		next: 'Следующий пропуск',
		cheer: 'Следующий пропуск…',
	},
	logic: {
		title: 'Продолжи фразу',
		hint: 'Прочитай начало и выбери подходящее продолжение',
		label: 'Фраза',
		next: 'Следующая фраза',
		cheer: 'Следующая фраза…',
	},
	choice: {
		title: '',
		hint: 'Выбери верный вариант',
		label: 'Вопрос',
		next: 'Следующий вопрос',
		cheer: 'Следующий вопрос…',
	},
}

/** Вид упражнения с вопросами: пропуски, продолжение или просто выбор. */
export function quizKind(exercise) {
	if (exercise.type === 'gap_fill') return 'gap'
	if (exercise.type === 'logical') return 'logic'
	return 'choice'
}

/**
 * QuizExercise — вопросы по одному: «Вставь пропущенное», «Продолжи фразу»
 * и прочие тесты с вариантами.
 *
 * Над карточкой — полоска точек: зелёная — верно, красная — ошибка, золотая
 * — текущий вопрос. После верного ответа следующий вопрос открывается сам
 * через 1,2 с, после ошибки — кнопкой. В слот встаёт только верный вариант.
 * В конце — карточка результата, из неё можно пролистать ответы без права
 * ответить заново.
 *
 * given — ответы упражнения: идентификатор вопроса → { chosen, correct,
 * expected } (варианты — идентификаторами).
 */
export function QuizExercise({ exercise, given, onAnswer, onForward }) {
	const kind = quizKind(exercise)
	const words = QUIZ[kind]
	const questions = exercise.payload?.questions || []
	const firstOpen = questions.findIndex((q) => !given[q.id])
	// Вернулись к упражнению — к первому неотвеченному, а сделанное — сразу
	// карточкой результата.
	const [at, setAt] = useState(firstOpen < 0 ? questions.length : firstOpen)
	const [review, setReview] = useState(false)
	const [busy, setBusy] = useState(false)
	const card = useRef(null)
	const chips = useRef({})
	const timer = useRef(0)
	const [hit, setHit] = useState(null)

	useEffect(() => () => clearTimeout(timer.current), [])
	useEffect(() => {
		if (hit) pulse(chips.current[hit.id], hit.cls)
	}, [hit])
	// Следующий вопрос въезжает справа — и после «Дальше», и сам после
	// верного ответа. Первый показ — без въезда: его уже показал шаг.
	const shown = useRef(false)
	useEffect(() => {
		if (shown.current) pulse(card.current, 'is-in')
		shown.current = true
	}, [at, review])

	const go = (i) => {
		clearTimeout(timer.current)
		setAt(i)
	}

	async function pick(q, choice) {
		if (given[q.id] || busy) return
		setBusy(true)
		const res = await onAnswer(exercise.id, q.id, choice.id, { choices: q.choices || [] })
		setBusy(false)
		// Без связи ответ ждёт отправки: верен ли он, скажет сервер, поэтому
		// ни радости, ни тряски — только «Дальше».
		if (!res || res.pending) return
		setHit({ id: choice.id, cls: res.correct ? 'lk-bounce' : 'lk-shake', n: Date.now() })
		if (res.correct) {
			const here = at
			timer.current = setTimeout(() => setAt((i) => (i === here ? i + 1 : i)), 1200)
		}
	}

	const ok = questions.filter((q) => given[q.id]?.correct).length
	const held = questions.filter((q) => given[q.id]?.pending).length
	const head = (
		<>
			<h2 className='h2'>{words.title || exercise.title || 'Выбери верный вариант'}</h2>
			<div className='lk-sub'>{words.hint}</div>
			<div className='lk-qdots' aria-hidden='true'>
				{questions.map((q, i) => {
					const a = given[q.id]
					let cls = ''
					if (a) cls = a.pending ? 'is-wait' : a.correct ? 'is-ok' : 'is-bad'
					else if (i === at) cls = 'is-cur'
					return <i key={q.id} className={cls} />
				})}
			</div>
		</>
	)

	if (!review && at >= questions.length) {
		const n = questions.length
		return (
			<div>
				{head}
				<div className='lk-qcard lk-center' ref={card}>
					<Avatar mood={ok >= n * 0.8 ? 'correct' : 'next'} size={110} className='lk-avatar' />
					<div className='lk-qtext'>
						{ok} из {n} верно
					</div>
					<div className='lk-sub'>Упражнение пройдено.</div>
					{held > 0 && (
						<div className='lk-sub'>Ещё {held} — проверим, когда появится связь.</div>
					)}
					<div className='lk-actions'>
						<Button variant='primary' onClick={onForward}>
							Дальше →
						</Button>
						{n > 0 && (
							<Button
								onClick={() => {
									setReview(true)
									go(0)
								}}
							>
								Посмотреть ответы
							</Button>
						)}
					</div>
				</div>
			</div>
		)
	}

	const q = questions[Math.min(at, questions.length - 1)]
	const a = given[q.id]
	const choices = q.choices || []
	const textOf = (id) => choices.find((c) => c.id === id)?.text ?? ''
	const tone = (c) => {
		if (!a) return ''
		if (a.pending) return c.id === a.chosen ? ' is-wait' : ' is-dim'
		if (c.id === a.expected) return ' is-ok'
		if (c.id === a.chosen) return ' is-bad'
		return ' is-dim'
	}
	const last = at >= questions.length - 1

	return (
		<div>
			{head}
			<div className='lk-qcard' ref={card}>
				<div className='lk-label'>
					{words.label} {at + 1} из {questions.length}
				</div>

				{kind === 'logic' ? (
					<div lang='tt'>
						<span className='lk-lead'>
							{leadOf(q.prompt)}
							{a ? (
								<>
									{' '}
									<em>{textOf(a.pending ? a.chosen : a.expected)}</em>
								</>
							) : (
								'…'
							)}
						</span>
					</div>
				) : (
					<GapSentence prompt={q.prompt} filled={a ? textOf(a.pending ? a.chosen : a.expected) : ''} />
				)}

				{kind === 'logic' ? (
					<div className='lk-conts' lang='tt'>
						{choices.map((c) => (
							<button
								key={c.id}
								ref={(el) => {
									chips.current[c.id] = el
								}}
								className={`lk-cont${tone(c)}`}
								type='button'
								disabled={Boolean(a) || busy}
								onClick={() => pick(q, c)}
							>
								{c.text}
							</button>
						))}
					</div>
				) : (
					<div className='lk-chips' lang='tt'>
						{choices.map((c) => (
							<button
								key={c.id}
								ref={(el) => {
									chips.current[c.id] = el
								}}
								className={`lk-chip${tone(c)}`}
								type='button'
								disabled={Boolean(a) || busy}
								onClick={() => pick(q, c)}
							>
								{c.text}
							</button>
						))}
					</div>
				)}

				{/* «Не так» не пишем: красный вариант и верный в слоте говорят
				    сами, а надпись про ошибку под ними только стыдит. */}
				<div className={`lk-feedback${a?.pending ? ' is-wait' : ''}`} role='status'>
					{a?.pending
						? 'Нет связи: ответ сохранён на устройстве и уйдёт сам'
						: a?.correct && !review
							? last
								? 'Right!'
								: `Right! ${words.cheer}`
							: ''}
				</div>

				{review ? (
					<div className='lk-review'>
						<button className='btn-icon' type='button' disabled={at === 0} onClick={() => go(at - 1)} aria-label='Предыдущий'>
							‹
						</button>
						<Button
							variant='primary'
							onClick={() => {
								if (last) {
									setReview(false)
									go(questions.length)
								} else go(at + 1)
							}}
						>
							{last ? 'Готово' : 'Дальше ›'}
						</Button>
					</div>
				) : (
					a &&
					!a.correct && (
						<Button variant='primary' onClick={() => go(at + 1)}>
							{last ? 'К результату →' : `${words.next} →`}
						</Button>
					)
				)}
			</div>
		</div>
	)
}

/**
 * GapSentence — предложение с пропуском: пустой слот пунктиром, после ответа
 * в нём верный вариант. Русской подсказки после « — » нет: она подсказывала
 * ответ.
 */
function GapSentence({ prompt, filled }) {
	const g = gapParts(prompt)
	const slot = <span className={`lk-gap${filled ? ' is-filled' : ''}`}>{filled || ' '}</span>
	return (
		<div className='lk-qtext' lang='tt'>
			{g.before}
			{!g.slot && ' '}
			{slot}
			{g.after}
		</div>
	)
}
