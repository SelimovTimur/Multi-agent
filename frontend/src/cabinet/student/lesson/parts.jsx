// parts.jsx — общие части экранов ученика: карточка урока с шапкой и
// этапами, серия верных подряд, плашка Ишки, конфетти, звёзды выдачи и
// «Мои ошибки» (docs/product/student-screens/story.md).

import { Fragment, useCallback, useEffect, useRef, useState } from 'react'
import Icon from '../../icons'
import Avatar from '../Avatar'
import { gapParts, leadOf, starView } from './kit'

/**
 * Перезапустить анимацию на элементе: снять класс, дать браузеру заметить
 * это и поставить снова. Иначе второй верный ответ подряд не подпрыгнул бы.
 */
export function pulse(el, cls) {
	if (!el) return
	el.classList.remove(cls)
	void el.offsetWidth
	el.classList.add(cls)
}

/**
 * LessonFrame — карточка экрана: шапка, тело с фоном этапа и низ с кнопками.
 *
 * stage — фон тела (text, lexicon, exercise, grammar, talk, finish). enter —
 * ключ шага: новый шаг въезжает справа, тот же — не дёргается.
 */
export function LessonFrame({ stage = 'text', top, foot, children, enter }) {
	return (
		<div className='lk' data-stage={stage}>
			{top}
			<div className={`lk-body${enter ? ' is-enter' : ''}`} key={enter || undefined}>
				{children}
			</div>
			{foot && <div className='lk-foot'>{foot}</div>}
		</div>
	)
}

/**
 * FrameTop — шапка: название урока, где ученик, серия и выход.
 *
 * stages — иконки этапов (kit.stageBar); без них шапка экрана вне урока:
 * результатов, ошибок, повтора. onStage(stage) — переход по иконке.
 */
export function FrameTop({ eyebrow, sub, streak = 0, onExit, stages, onStage, busy }) {
	const cur = stages?.find((s) => s.state === 'cur')
	return (
		<div className='lk-top'>
			<div className='lk-head'>
				<div className='lk-head-main'>
					{eyebrow && <div className='auth-eyebrow'>{eyebrow}</div>}
					{sub && <div className='tiny'>{sub}</div>}
				</div>
				<div className='lk-tools'>
					{stages && <Streak n={streak} />}
					{onExit && (
						<button className='btn-icon' type='button' title='К моим занятиям' aria-label='К моим занятиям' onClick={onExit}>
							<Icon name='x' size={18} />
						</button>
					)}
				</div>
			</div>
			{stages && (
				<>
					<div className='lk-stages'>
						{stages.map((s, i) => (
							<Fragment key={s.key}>
								<button
									type='button'
									className={`lk-stage${s.state ? ` is-${s.state}` : ''}`}
									title={s.label}
									aria-label={`${s.label}${s.state === 'done' ? ', пройдено' : ''}`}
									aria-current={s.state === 'cur' ? 'step' : undefined}
									disabled={busy || !onStage || !s.reachable}
									onClick={() => onStage(s)}
								>
									<span aria-hidden='true'>{s.icon}</span>
								</button>
								{i < stages.length - 1 && (
									<div className='lk-seg' aria-hidden='true'>
										<i style={{ width: `${s.fill}%` }} />
									</div>
								)}
							</Fragment>
						))}
					</div>
					<div className='lk-stage-label'>{cur?.label}</div>
				</>
			)}
		</div>
	)
}

/** Серия верных подряд: видна с двух, растёт с «пульсом». */
function Streak({ n }) {
	const ref = useRef(null)
	const prev = useRef(n)
	useEffect(() => {
		if (n > prev.current && n >= 2) pulse(ref.current, 'is-pop')
		prev.current = n
	}, [n])
	return (
		<div ref={ref} className={`lk-streak${n >= 2 ? ' is-on' : ''}`} title='Верных ответов подряд' aria-hidden={n < 2}>
			<span aria-hidden='true'>🔥</span> {n}
		</div>
	)
}

/**
 * useCheer — плашка Ишки после верного ответа. Сама скрывается через
 * полторы секунды: она радует, а не просит что-то сделать.
 */
export function useCheer() {
	const [cheer, setCheer] = useState(null)
	const timer = useRef(0)
	const show = useCallback((mood, title, sub) => {
		clearTimeout(timer.current)
		setCheer({ mood, title, sub, on: true })
		timer.current = setTimeout(() => setCheer((c) => c && { ...c, on: false }), 1500)
	}, [])
	useEffect(() => () => clearTimeout(timer.current), [])
	return [cheer, show]
}

/** Cheer — сама плашка. Содержимое остаётся, пока она уезжает вниз. */
export function Cheer({ cheer }) {
	return (
		<div className={`lk-cheer${cheer?.on ? ' is-on' : ''}`} role='status' aria-live='polite'>
			{cheer && <Avatar mood={cheer.mood} size={56} className='lk-avatar' />}
			{cheer && (
				<div>
					<b>{cheer.title}</b>
					<span>{cheer.sub}</span>
				</div>
			)}
		</div>
	)
}

/**
 * Confetti — конфетти на итоге выдачи. fire — номер залпа: новый номер —
 * новый залп. При «уменьшать движение» в системе залпа нет.
 */
export function Confetti({ fire }) {
	const ref = useRef(null)
	useEffect(() => {
		if (!fire) return undefined
		if (window.matchMedia?.('(prefers-reduced-motion: reduce)').matches) return undefined
		const canvas = ref.current
		const ctx = canvas?.getContext?.('2d')
		if (!ctx) return undefined
		const w = (canvas.width = window.innerWidth)
		const h = (canvas.height = window.innerHeight)
		const colors = ['#1F7A4D', '#D4A755', '#E59A76', '#7AA7E0', '#B98BD0']
		const parts = Array.from({ length: 120 }, () => ({
			x: w / 2, y: h * 0.35,
			vx: (Math.random() - 0.5) * 12, vy: -Math.random() * 11 - 4,
			r: Math.random() * 6 + 4, c: colors[Math.floor(Math.random() * colors.length)],
			a: Math.random() * 6, va: (Math.random() - 0.5) * 0.3,
		}))
		let t = 0
		let raf = 0
		const frame = () => {
			ctx.clearRect(0, 0, w, h)
			for (const p of parts) {
				p.vy += 0.32
				p.vx *= 0.99
				p.x += p.vx
				p.y += p.vy
				p.a += p.va
				ctx.save()
				ctx.translate(p.x, p.y)
				ctx.rotate(p.a)
				ctx.fillStyle = p.c
				ctx.fillRect(-p.r / 2, -p.r / 4, p.r, p.r / 2)
				ctx.restore()
			}
			t += 1
			if (t < 150) raf = requestAnimationFrame(frame)
			else ctx.clearRect(0, 0, w, h)
		}
		raf = requestAnimationFrame(frame)
		return () => {
			cancelAnimationFrame(raf)
			ctx.clearRect(0, 0, w, h)
		}
	}, [fire])
	return fire ? <canvas ref={ref} className='lk-confetti' aria-hidden='true' /> : null
}

/**
 * StarsGrid — звёзды выдачи карточками: условие и подпись. Заработанные
 * загораются (по очереди, если animate), незаработанные серые с коричневой
 * подписью — «у тебя 60%», «4 из 10, нужно 5».
 */
export function StarsGrid({ issue, animate = false }) {
	const stars = issue?.stars || []
	const [lit, setLit] = useState(animate ? 0 : stars.length)
	useEffect(() => {
		if (!animate) {
			setLit(stars.length)
			return undefined
		}
		setLit(0)
		const timers = stars.map((_, i) => setTimeout(() => setLit(i + 1), 200 + i * 220))
		return () => timers.forEach(clearTimeout)
	}, [animate, stars.length])
	if (!stars.length) return null
	return (
		<div className='lk-stars' style={{ '--n': stars.length }}>
			{stars.map((s, i) => {
				const v = starView(s, issue)
				return (
					<div
						key={s.kind}
						className={`lk-star${s.on && i < lit ? ' is-on' : ''}${s.on ? '' : ' is-miss'}`}
						role='img'
						aria-label={`${v.title}: ${s.on ? 'звезда есть' : 'звезды нет'}, ${v.note}`}
					>
						<span className='lk-star-s' aria-hidden='true'>⭐</span>
						<b aria-hidden='true'>{v.title}</b>
						<span className='lk-star-note' aria-hidden='true'>
							{v.note}
						</span>
					</div>
				)
			})}
		</div>
	)
}

/** Кнопка «Мои ошибки · N ›» или плашка «Ошибок нет 🎉». */
export function MistakesButton({ count, onOpen }) {
	if (!count) return <div className='lk-mist is-none'>Ошибок нет 🎉</div>
	return (
		<button type='button' className='lk-mist' onClick={onOpen}>
			<span>Мои ошибки · {count}</span>
			<span aria-hidden='true'>›</span>
		</button>
	)
}

/**
 * MistakesList — ошибки по упражнениям: вопрос с верным ответом зелёным и
 * «Твой ответ» красным. list — из kit.mistakesOf.
 */
export function MistakesList({ list }) {
	if (!list.length) return <div className='lk-arow is-empty'>Ошибок нет 🎉</div>
	const out = []
	let group = null
	for (const m of list) {
		if (m.exercise !== group) {
			group = m.exercise
			out.push(
				<div className='lk-mist-h' key={`h:${m.exercise}`}>
					{m.name}
				</div>,
			)
		}
		out.push(
			<div className='lk-arow' key={m.key}>
				<MistakeQuestion m={m} />
				<div className='lk-arow-bad'>Твой ответ: {m.chosen || '—'}</div>
			</div>,
		)
	}
	return <>{out}</>
}

/**
 * Вопрос с верным ответом: пара — «выражение — перевод», пропуск — верное
 * в слоте, продолжение — дописанная фраза.
 */
function MistakeQuestion({ m }) {
	if (m.type === 'matching') {
		return (
			<>
				<b>{m.prompt}</b> — <span className='lk-arow-ok'>{m.expected}</span>
			</>
		)
	}
	if (m.type === 'logical') {
		return (
			<>
				{leadOf(m.prompt)} <span className='lk-arow-ok'>{m.expected}</span>
			</>
		)
	}
	const g = gapParts(m.prompt)
	return (
		<>
			{g.before}
			{!g.slot && ' '}
			<span className='lk-gap is-filled is-small'>{m.expected}</span>
			{g.after}
		</>
	)
}
