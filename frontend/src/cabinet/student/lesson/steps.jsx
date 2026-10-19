// steps.jsx — шаги урока без проверки: цели, текст, выражения, правило,
// подготовка к разговору с Ишкой (docs/product/student-screens/story.md,
// JOB-3 и JOB-9).

import { Fragment, useEffect, useMemo, useRef, useState } from 'react'
import { my } from '../../../api/v1'
import Icon from '../../icons'
import { plural } from '../../plural.js'
import { Button } from '../../ui'
import Avatar from '../Avatar'
import { frankPairs, paragraphs } from './kit'

/** ObjectivesStep — «Что ты научишься делать»: цели урока карточками. */
export function ObjectivesStep({ content, title }) {
	return (
		<div>
			<div className='lk-row-hi'>
				<Avatar mood='read' size={72} className='lk-avatar' />
				<div>
					<h2 className='h2'>Что ты научишься делать</h2>
					{title && <div className='lk-sub'>Урок «{title}»</div>}
				</div>
			</div>
			<div className='lk-goals'>
				{content.objectives.map((o, i) => (
					<div className='lk-goal' key={o.id || i} style={{ animationDelay: `${i * 0.08}s` }}>
						<span className='lk-goal-mark' aria-hidden='true'>
							<Icon name='check' size={15} />
						</span>
						<span>{o.text}</span>
					</div>
				))}
			</div>
		</div>
	)
}

/**
 * TextStep — чтение. Перевод по кнопке и под каждой фразой: сначала
 * попытка понять самому, потом сверка по фразам, а не два текста рядом.
 */
export function TextStep({ content }) {
	const [frank, setFrank] = useState(false)
	const pairs = useMemo(() => frankPairs(content.frank_text), [content.frank_text])
	return (
		<div>
			<div className='lk-row-hi'>
				<Avatar mood='read' size={64} className='lk-avatar' />
				<div>
					<h2 className='h2'>Прочитай текст</h2>
					{/* Формулировка без рода: в классе половина девочек, и «попробуй
					    понять сам» они читают про себя. */}
					<div className='lk-sub'>Сначала попробуй понять без перевода.</div>
				</div>
			</div>

			<div className='lk-reading' lang='tt'>
				{!frank &&
					paragraphs(content.refined_text).map((p, i) => <p key={i}>{p}</p>)}
				{frank && pairs && (
					<p>
						{pairs.map((x, i) => (
							<Fragment key={i}>
								{x.text}
								<span className='lk-fr' lang='ru'>
									{x.translation}
								</span>
							</Fragment>
						))}
					</p>
				)}
				{frank && !pairs && <p className='lk-frank'>{content.frank_text}</p>}
			</div>

			{content.frank_text && (
				<div className='mt12'>
					<Button icon='eye' onClick={() => setFrank((f) => !f)} aria-pressed={frank}>
						{frank ? 'Только английский' : 'Показать перевод'}
					</Button>
				</div>
			)}
		</div>
	)
}

/** LexiconStep — выражения урока одной большой карточкой. */
export function LexiconStep({ content }) {
	return (
		<div>
			<h2 className='h2'>Выражения урока</h2>
			<div className='lk-sub'>Нажми на карточку, чтобы перевернуть. Листай свайпом или стрелками.</div>
			<Flashcards items={content.lexicon} />
		</div>
	)
}

/**
 * Flashcards — карточка с переворотом: выражение крупно, на обороте
 * перевод. Листается свайпом, стрелками на экране и клавишами ← →.
 * Без оценки: то же и в повторе выражений из результатов урока.
 */
export function Flashcards({ items }) {
	const [at, setAt] = useState(0)
	const [flip, setFlip] = useState(false)
	const [shown, setShown] = useState(0)
	const x0 = useRef(null)
	const swiped = useRef(false)
	// Новая карточка — новая кнопка (без обратного переворота старой), и
	// фокус, если листали клавишами, переезжает на неё.
	const card = useRef(null)
	const refocus = useRef(false)
	useEffect(() => {
		if (!refocus.current) return
		refocus.current = false
		card.current?.focus()
	}, [shown])
	if (!items?.length) return null
	const i = Math.min(at, items.length - 1)
	const it = items[i]

	const move = (d) => {
		const n = i + d
		if (n < 0 || n >= items.length) return
		setAt(n)
		setFlip(false)
		setShown((k) => k + 1)
	}

	return (
		<>
			<div
				className='lk-flip-wrap'
				onPointerDown={(e) => {
					x0.current = e.clientX
				}}
				onPointerUp={(e) => {
					if (x0.current === null) return
					const dx = e.clientX - x0.current
					x0.current = null
					if (Math.abs(dx) > 50) {
						// Свайп — листание, а не переворот: клик после него не считается.
						swiped.current = true
						move(dx < 0 ? 1 : -1)
					}
				}}
			>
				<button
					key={shown}
					ref={card}
					className={`lk-flip${flip ? ' is-on' : ''}${shown ? ' is-in' : ''}`}
					type='button'
					aria-label={
						flip ? `${it.translation}. Нажми, чтобы увидеть выражение` : `${it.phrase}. Нажми, чтобы увидеть перевод`
					}
					onClick={() => {
						if (swiped.current) {
							swiped.current = false
							return
						}
						setFlip((f) => !f)
					}}
					onKeyDown={(e) => {
						if (e.key !== 'ArrowRight' && e.key !== 'ArrowLeft') return
						refocus.current = true
						move(e.key === 'ArrowRight' ? 1 : -1)
					}}
				>
					<span className='lk-face is-front' aria-hidden='true'>
						<span className='lk-face-ph' lang='tt'>
							{it.phrase}
						</span>
						<span className='lk-face-hint'>нажми, чтобы увидеть перевод</span>
					</span>
					<span className='lk-face is-back' aria-hidden='true'>
						<span className='lk-face-tr'>{it.translation}</span>
						<span className='lk-face-hint' lang='tt'>
							{it.phrase}
						</span>
					</span>
				</button>
			</div>
			<div className='lk-slider'>
				<button className='btn-icon' type='button' disabled={i === 0} onClick={() => move(-1)} aria-label='Предыдущее выражение'>
					<Icon name='back' />
				</button>
				<div className='lk-center'>
					{/* Точек не больше полутора десятков: дальше это уже не счёт, а
					    полоса, и «12 из 30» под ней понятнее. */}
					{items.length <= 15 && (
						<div className='lk-dots' aria-hidden='true'>
							{items.map((_, k) => (
								<i key={k} className={k === i ? 'is-on' : ''} />
							))}
						</div>
					)}
					<div className='lk-sub'>
						{i + 1} из {items.length}
					</div>
				</div>
				<button
					className='btn-icon'
					type='button'
					disabled={i === items.length - 1}
					onClick={() => move(1)}
					aria-label='Следующее выражение'
				>
					<Icon name='fwd' />
				</button>
			</div>
		</>
	)
}

/** Пример к грамматической теме — коротко: первый из разбора. */
function shortExample(g) {
	if (g.examples?.length) return g.examples[0].phrase
	return String(g.example || '').split(';')[0].trim()
}

/** GrammarStep — «Как устроен этот текст»: темы раскрываются по нажатию. */
export function GrammarStep({ content }) {
	const n = content.grammar.length
	return (
		<div>
			<h2 className='h2'>Как устроен этот текст</h2>
			<div className='lk-sub'>
				{plural(n, 'главный кирпичик', 'главных кирпичика', 'главных кирпичиков')}. Нажми, чтобы раскрыть.
			</div>
			{content.grammar.map((g, i) => (
				<details className='lk-rule' key={g.id || i} open={i === 0}>
					<summary>
						<span className='lk-rule-mark' aria-hidden='true'>
							<Icon name='puzzle' size={16} />
						</span>
						{g.title}
					</summary>
					<div className='lk-rule-in'>
						{g.explanation && <div className='lk-rule-note'>{g.explanation}</div>}
						{g.examples?.length > 0 ? (
							<div className='mt12'>
								{g.examples.map((e, k) => (
									<div className='lk-ex' key={k}>
										<div lang='tt'>{e.phrase}</div>
										{e.translation && <span>{e.translation}</span>}
									</div>
								))}
							</div>
						) : (
							g.example && <div className='lk-try'>{g.example}</div>
						)}
						{/* Задания на конструкцию — для разбора, а не для оценки:
						    проверяемые упражнения идут отдельными шагами урока. */}
						{g.practice?.map((t, k) => (
							<div className='lk-try' key={k}>
								{k === 0 && <strong>Попробуй: </strong>}
								{t.prompt}
							</div>
						))}
					</div>
				</details>
			))}
		</div>
	)
}

/**
 * useTalk — что с разговором: начат ли, закрыт ли и сколько выражений урока
 * в нём засчитано. Разбор готовится в фоне, поэтому итог переспрашиваем,
 * пока он не готов, — недолго: потом это скажут результаты урока.
 */
export function useTalk(attemptId, active) {
	const [state, setState] = useState({ conv: null, summary: null, off: false, failed: false })
	useEffect(() => {
		if (!active) return undefined
		let gone = false
		let timer = 0
		let tries = 0
		const ac = new AbortController()
		const poll = async () => {
			try {
				const s = await my.conversationSummary(attemptId, { signal: ac.signal })
				if (gone) return
				setState((st) => ({ ...st, summary: s }))
				if (s.status === 'pending' && ++tries < 30) timer = setTimeout(poll, 4000)
			} catch {
				if (!gone && ++tries < 30) timer = setTimeout(poll, 4000)
			}
		}
		let loads = 0
		const load = () =>
			my.conversation(attemptId, { signal: ac.signal })
				.then((conv) => {
					if (gone) return
					setState((st) => ({ ...st, conv, off: conv.enabled === false, failed: false }))
					if (conv.status === 'closed') poll()
				})
				.catch((err) => {
					if (gone) return
					// Разговор выключен на сервере — ученик не должен застрять на
					// шаге, который нельзя пройти.
					if (err?.status === 503) return setState((st) => ({ ...st, off: true }))
					// Любая другая ошибка: пробуем ещё пару раз, потом даём выйти к
					// итогам — иначе на шаге без «Дальше» ученик в тупике.
					if (++loads < 3) timer = setTimeout(load, 3000)
					else setState((st) => ({ ...st, failed: true }))
				})
		load()
		return () => {
			gone = true
			clearTimeout(timer)
			ac.abort()
		}
	}, [attemptId, active])
	return state
}

/**
 * TalkStep — подготовка к разговору с Ишкой и экран после него.
 *
 * До разговора: тема урока, что употребить и «Начать разговор». Нижней
 * «Дальше» на этом шаге нет: разговор обязателен. После — «Разговор
 * завершён», сколько выражений урока в нём засчитано, и «К итогам →».
 */
export function TalkStep({ attemptId, section, title, content, talk, onFinish, busy }) {
	const chat = `/student/attempt/${attemptId}/chat${section ? `?section=${section}` : ''}`
	const { conv, summary, off, failed } = talk
	const closed = conv?.status === 'closed'
	const talked = closed || (conv?.turns_taken || 0) > 0
	// Что употребить — цели разговора, если их назначил учитель, иначе
	// выражения урока.
	const phrases = conv?.phrases?.length ? conv.phrases : (content.lexicon || []).map((u) => u.phrase)
	const grammar = content.grammar || []
	const sum = summary?.status === 'ready' ? summary.summary : null

	return (
		<div>
			<div className='lk-talk-head'>
				<Avatar mood='talk' size={120} className='lk-avatar' />
				<div className='lk-bubble'>
					Hello! Сегодня поговорим на тему{' '}
					<span className='lk-topic'>{title || 'урока'}</span> 👋
				</div>
			</div>

			{talked ? (
				<div className='lk-talked'>
					<div className='h2' aria-hidden='true'>
						✅
					</div>
					<div className='h3 mt8'>{closed ? 'Разговор завершён' : 'Разговор начат'}</div>
					{sum && (
						<div className='lk-sub'>
							Выражений урока в разговоре:{' '}
							<b>
								{sum.credited_phrases?.length || 0} из {sum.phrases_total || 0}
							</b>
						</div>
					)}
					{closed && !sum && <div className='lk-sub'>Считаем выражения из разговора…</div>}
					<div className='lk-actions'>
						<Button variant='primary' disabled={busy} onClick={onFinish}>
							{busy ? 'Сохраняем…' : 'К итогам →'}
						</Button>
						{!closed && (
							<Button to={chat} state={title ? { title } : undefined}>
								Продолжить разговор
							</Button>
						)}
					</div>
				</div>
			) : (
				<>
					<div className='lk-use'>
						<div className='lk-label'>Употреби в разговоре</div>
						<div className='lk-sub'>Чем больше выражений используешь, тем ближе третья звезда.</div>
						{phrases.length > 0 && (
							<>
								<div className='h3 mt12'>Выражения</div>
								<div className='lk-tags' lang='tt'>
									{phrases.map((p, i) => (
										<span className='lk-tag' key={i}>
											{p}
										</span>
									))}
								</div>
							</>
						)}
						{grammar.length > 0 && (
							<>
								<div className='h3 mt16'>Грамматика</div>
								<div className='lk-gram'>
									{grammar.map((g, i) => (
										<div key={g.id || i}>
											<Icon name='puzzle' size={14} />
											{g.title}
											{shortExample(g) && <span lang='tt'>{shortExample(g)}</span>}
										</div>
									))}
								</div>
							</>
						)}
					</div>
					<div className='lk-actions'>
						{off ? (
							<>
								<div className='lk-sub'>Разговор с аватаром сейчас недоступен — можно перейти к итогам.</div>
								<Button variant='primary' disabled={busy} onClick={onFinish}>
									К итогам →
								</Button>
							</>
						) : (
							<>
								<Button variant='gold' icon='bubble' to={chat} state={title ? { title } : undefined}>
									Начать разговор
								</Button>
								{failed && (
									<>
										<div className='lk-sub'>
											Не удалось проверить разговор. Можно начать его или перейти к итогам.
										</div>
										<Button disabled={busy} onClick={onFinish}>
											К итогам →
										</Button>
									</>
								)}
							</>
						)}
					</div>
				</>
			)}
		</div>
	)
}

/** CustomStep — «Свой текст» учителя: памятка или задание без проверки. */
export function CustomStep({ exercise }) {
	return (
		<div>
			<h2 className='h2'>{exercise.title || 'Свой текст'}</h2>
			{exercise.instruction && <div className='lk-sub'>{exercise.instruction}</div>}
			<div className='lk-reading lk-frank'>{exercise.payload?.text || ''}</div>
		</div>
	)
}

/**
 * MissingStep — задание, которого нет в содержимом урока. Название от сервера
 * всё равно есть: по нему ученик назовёт задание учителю.
 */
export function MissingStep({ task }) {
	return (
		<div>
			<h2 className='h2'>{task.title || 'Задание'}</h2>
			<div className='lk-sub'>Это задание не открылось. Скажи учителю или нажми «Дальше».</div>
		</div>
	)
}
