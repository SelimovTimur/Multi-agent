// StudentConversation.jsx — карточка ученика в отчёте по разговору (У-2…У-4).
//
// Полная расшифровка разговора с отметками: ошибки по целям трёх видов,
// общие ошибки с уровнем или «уровень не определён», промахи, места, где
// ученик исправил распознанный текст, и зачтённые употребления. Каждая
// отметка в списке ведёт к цитате в расшифровке — прокрутка и подсветка.
//
// Отчёт появляется только после завершения разговора и дальше не меняется,
// поэтому опрос останавливается, как только он готов. Ручка и форма ответа —
// docs/product/avatar-goals/contract.md, раздел 6; экран — teacher-ui.md.

import { useEffect, useMemo, useRef, useState } from 'react'
import { assignments as api } from '../../../api/v1'
import Icon from '../../icons'
import { Button, plural } from '../../ui'
import { buildCard, noteFor, noteLine, noteTitle, reportState, statusView } from './report'
import { fatal, usePolling } from './usePolling'
import './conversation.css'

// Разбор вот-вот будет готов — спрашиваем часто. Разговор идёт — реже: отчёт
// появится только после последнего ответа ученика. Неудачный разбор сервер
// перезапускает не чаще раза в минуту — чаще спрашивать о нём незачем.
const PENDING_MS = 5000
const WAITING_MS = 20000
const FAILED_MS = 30000
const NEXT_POLL = { pending: PENDING_MS, failed: FAILED_MS, ready: null }
const REQUEST_TIMEOUT_MS = 15000

export default function StudentConversation({ assignmentId, studentId, known, focus, onBack, embedded = false }) {
	// stopped — отказ, после которого опрос встал: «попробуем ещё раз» не обещаем.
	const [state, setState] = useState({ data: null, error: '', stopped: false })

	usePolling(
		async (signal) => {
			try {
				const data = await api.studentConversation(assignmentId, studentId, {
					signal,
					timeoutMs: REQUEST_TIMEOUT_MS,
				})
				if (signal.aborted) return null
				setState({ data, error: '', stopped: false })
				const now = reportState(data)
				// Готовый отчёт больше не меняется — опрос на нём кончается.
				return now in NEXT_POLL ? NEXT_POLL[now] : WAITING_MS
			} catch (err) {
				if (signal.aborted) return null
				setState((s) => ({ ...s, error: err.message, stopped: fatal(err) }))
				return fatal(err) ? null : WAITING_MS
			}
		},
		[assignmentId, studentId],
	)

	return (
		<StudentConversationView
			data={state.data}
			error={state.error}
			retrying={!state.stopped}
			known={known}
			focus={focus}
			onBack={onBack}
			embedded={embedded}
		/>
	)
}

/**
 * StudentConversationView — карточка по готовому ответу ручки.
 *
 * data — ответ `…/students/{id}/conversation` или null, пока он не пришёл;
 * known — строка ученика из отчёта класса: имя и статус видны сразу, до
 * ответа. focus — { turnNo, quote }: карточку открыли кликом по цитате в
 * общих ошибках класса, и эту цитату надо сразу показать.
 *
 * embedded — отчёт встроен в карточку ученика (#63): кнопки «Все ученики» и
 * имени нет, карточка и так про этого ученика. Остаются ответы, статус и
 * всё ниже.
 */
export function StudentConversationView({
	data,
	error = '',
	retrying = true,
	known,
	focus,
	onBack,
	embedded = false,
}) {
	const card = useMemo(() => (data?.report ? buildCard(data.report) : null), [data])
	const [active, setActive] = useState('')
	const box = useRef(null)
	const scroller = useRef(null)

	// Карточка открылась — её шапка должна быть на экране: учитель мог
	// нажать на ученика внизу длинной таблицы.
	useEffect(() => {
		const el = box.current
		if (el && el.getBoundingClientRect().top < 0) el.scrollIntoView({ block: 'start' })
	}, [])

	/**
	 * Показать отметку: подсветить цитату и прокрутить расшифровку к ответу.
	 *
	 * Ответы в расшифровке уже на экране, поэтому прокрутка идёт сразу, без
	 * ожидания перерисовки: подсветка догонит её тем же кадром. Фокус
	 * переходит на ответ — с клавиатуры дальше читают именно его.
	 */
	function show(note) {
		setActive(note.id)
		const el = note.anchor && document.getElementById(note.anchor)
		if (!el) return
		const smooth = !window.matchMedia?.('(prefers-reduced-motion: reduce)').matches
		const behavior = smooth ? 'smooth' : 'auto'
		const panel = scroller.current
		// На широком экране расшифровка прокручивается сама по себе — двигаем
		// её, а не страницу: список отметок остаётся на месте.
		if (panel && panel.scrollHeight > panel.clientHeight + 1) {
			const top = el.getBoundingClientRect().top - panel.getBoundingClientRect().top + panel.scrollTop
			panel.scrollTo({ top: Math.max(0, top - panel.clientHeight / 3), behavior })
			panel.scrollIntoView({ block: 'nearest', behavior })
		} else {
			el.scrollIntoView({ block: 'center', behavior })
		}
		el.focus({ preventScroll: true })
	}

	// Пришли из общих ошибок класса — показываем ту самую цитату, как только
	// отчёт загрузился.
	const shown = useRef(null)
	useEffect(() => {
		if (!card || !focus || shown.current === focus) return
		shown.current = focus
		const note = noteFor(card, focus)
		if (note) show(note)
	}, [card, focus])

	const info = data?.student || known || {}
	const now = reportState(data)
	const status = statusView(data?.status || known?.status)
	const taken = data?.turns_taken ?? known?.turns_taken
	const max = data?.max_turns
	const answers = taken === undefined ? '' : `ответов ${taken}${max ? ` из ${max}` : ''}`

	return (
		<section
			className={`cr cr-card ${embedded ? 'mt16' : 'mt22'}`}
			ref={box}
			aria-label={`Разговор ученика ${info.name || ''}`}
		>
			{!embedded && (
				<Button variant='ghost' icon='back' className='btn-compact' onClick={onBack}>
					Все ученики
				</Button>
			)}

			<div className={`cr-card-hd${embedded ? '' : ' mt12'}`}>
				<div>
					{!embedded && (
						<h2 className='h2'>
							{info.name || 'Ученик'} {info.tag && <span className='tiny'>#{info.tag}</span>}
						</h2>
					)}
					<div className={embedded ? 'tiny' : 'tiny mt8'}>
						{[answers, card?.closed, card && `уровень урока ${card.level}`].filter(Boolean).join(' · ')}
					</div>
				</div>
				{(data || known) && <span className={`badge ${status.cls}`}>{status.label}</span>}
			</div>

			{now === 'loading' &&
				(error ? (
					<div className='notice notice-err mt16'>
						Не удалось загрузить разговор: {error}.{retrying ? ' Экран попробует ещё раз сам.' : ''}
					</div>
				) : (
					<div className='tiny mt16'>Загружаем разговор…</div>
				))}

			{(now === 'not_started' || now === 'in_progress') && (
				<div className='card card-white cr-wait mt16'>
					<div className='h3'>
						{now === 'in_progress' ? 'Разговор ещё идёт' : 'Ученик ещё не начинал разговор'}
					</div>
					<p className='small muted'>
						Отчёт появится, когда ученик завершит разговор. Сам разговор в реальном времени не
						виден: отметки ставятся по ходу, а показываются целиком после последнего ответа.
					</p>
				</div>
			)}

			{now === 'pending' && (
				<div className='card card-white cr-wait mt16'>
					<div className='cr-wait-row'>
						<span className='spinner' aria-hidden='true' />
						<b>Разбор готовится…</b>
					</div>
					<p className='small muted'>
						Разговор завершён. Система проверяет ответы и ищет общие ошибки — отчёт появится здесь
						сам, обычно меньше чем через минуту.
					</p>
				</div>
			)}

			{now === 'failed' && (
				<div className='card card-white cr-wait mt16'>
					<div className='h3'>Разбор не получился</div>
					<p className='small muted'>
						Ответы ученика сохранены. Разбор запустится заново сам, и отчёт появится здесь — обычно
						в течение пары минут.
					</p>
				</div>
			)}

			{now === 'ready' && card && <ReadyCard card={card} active={active} onShow={show} scroller={scroller} />}

			{/* Данные на экране есть, но последний запрос не прошёл: они могли
			    устареть, и учитель должен это знать. */}
			{data && error && <div className='tiny cr-lost mt12'>{error}</div>}
		</section>
	)
}

/** Готовый отчёт: цели, отметки и расшифровка. */
function ReadyCard({ card, active, onShow, scroller }) {
	const { lists } = card
	return (
		<>
			<div className='card card-white cr-goals mt16'>
				<div className='row-between'>
					<div className='h3'>Цели разговора</div>
					<span className={`badge ${card.creditedCount ? 'badge-green' : 'badge-gray'}`}>
						зачтено {card.creditedCount} из {card.goalsTotal}
					</span>
				</div>
				<ul className='mt8'>
					{card.goals.map((g) => (
						<li className={`cr-goal${g.credited ? ' is-ok' : ''}`} key={g.key}>
							<span className='cr-goal-mark' title={g.credited ? 'зачтено' : 'не зачтено'}>
								<Icon name={g.credited ? 'check' : 'x'} size={14} />
							</span>
							<span className='grow'>
								<span className='t-name'>{g.title}</span> <span className='tiny'>{g.kindLabel}</span>
								{g.note && <span className='cr-item-sub'>{g.note}</span>}
								<span className='cr-stats'>
									<span>
										{plural(g.correctUses, 'верное употребление', 'верных употребления', 'верных употреблений')}
									</span>
									<span className={g.errors ? 'cr-bad' : ''}>{plural(g.errors, 'ошибка', 'ошибки', 'ошибок')}</span>
									<span>{plural(g.misses, 'промах', 'промаха', 'промахов')}</span>
								</span>
							</span>
						</li>
					))}
				</ul>
				{card.speech && <div className='tiny mt8'>{card.speech}.</div>}
			</div>

			<div className='cr-body mt22'>
				<aside className='cr-marks cr-scroll' aria-label='Отметки'>
					<MarkSection
						title='Ошибки по целям'
						notes={lists.goalErrors}
						empty='Ошибок по целям нет'
						active={active}
						onShow={onShow}
					/>
					<MarkSection
						title='Общие ошибки'
						notes={lists.general}
						empty='Общих ошибок нет'
						footer={card.aboveLevelLabel}
						active={active}
						onShow={onShow}
					/>
					<MarkSection title='Промахи' notes={lists.misses} active={active} onShow={onShow} />
					<MarkSection
						title='Исправления распознанного текста'
						notes={lists.repairs}
						active={active}
						onShow={onShow}
					/>
					<MarkSection title='Зачтено' notes={lists.credited} folded active={active} onShow={onShow} />
					{card.incidents.length > 0 && (
						<div className='cr-incidents'>
							<b>Технические сбои:</b>{' '}
							{card.incidents.map((i) => `${i.label} — ${i.count}`).join(', ')}. Ошибками ученика они
							не считаются.
						</div>
					)}
				</aside>

				<div className='cr-transcript'>
					<div className='h3'>Расшифровка</div>
					<div className='tiny mt8'>
						Реплики аватара ученик слышал голосом; ответы — то, что ученик отправил.
					</div>
					<div className='cr-scroll mt12' ref={scroller}>
						<ol className='cr-turns'>
							{card.turns.map((t) => (
								<Turn key={t.key} turn={t} active={active} onShow={onShow} />
							))}
						</ol>
					</div>
				</div>
			</div>
		</>
	)
}

/**
 * Раздел отметок одного вида. Пустой раздел ошибок остаётся со словами
 * «нет» — учитель должен видеть, что ошибок не было, а не гадать, не
 * пропал ли раздел. Пустые промахи, исправления и зачтённое не показываются.
 *
 * folded — раздел свёрнут, пока его не раскроют. Так стоит «Зачтено»: что
 * зачтено, видно по целям вверху и по подсветке в расшифровке, а длинный
 * список отодвигал бы расшифровку на телефоне на несколько экранов вниз.
 */
function MarkSection({ title, notes, empty, footer, folded = false, active, onShow }) {
	const [open, setOpen] = useState(!folded)
	if (!notes.length && !empty && !footer) return null
	return (
		<section>
			<div className='cr-sec-t'>
				<span>{title}</span>
				{notes.length > 0 &&
					(folded ? (
						<button className='cr-sec-toggle' type='button' aria-expanded={open} onClick={() => setOpen(!open)}>
							{notes.length} · {open ? 'скрыть' : 'показать'}
						</button>
					) : (
						<span className='tiny'>{notes.length}</span>
					))}
			</div>
			{notes.length === 0 && empty && <div className='tiny mt8'>{empty}</div>}
			{notes.length > 0 && open && (
				<ul className='cr-marklist'>
					{notes.map((n) => (
						<li key={n.id}>
							<MarkItem note={n} active={active === n.id} onShow={onShow} />
						</li>
					))}
				</ul>
			)}
			{footer && <div className='tiny mt8'>{footer}.</div>}
		</section>
	)
}

/** Одна отметка в списке: по нажатию — к цитате в расшифровке. */
function MarkItem({ note, active, onShow }) {
	return (
		<button
			type='button'
			className={`cr-mark cr-tone-${note.category}${active ? ' is-active' : ''}`}
			aria-pressed={active}
			onClick={() => onShow(note)}
		>
			<span className='cr-mark-hd'>
				<b>{noteTitle(note)}</b>
				<span className='tiny'>Ответ {note.turnNo}</span>
			</span>
			{note.goalTitle && (
				<span className='tiny'>
					{note.goalKind ? `${note.goalKind}: ` : ''}
					{note.goalTitle}
				</span>
			)}
			{note.category === 'repair' ? (
				<span>
					Было: «{note.before}» → стало: «{note.after}»
				</span>
			) : (
				note.quote && <span className='cr-mark-q'>«{note.quote}»</span>
			)}
			{note.correctForm && (
				<span>
					Верно: <b>{note.correctForm}</b>
				</span>
			)}
			{note.rule && <span>{note.rule}</span>}
			{note.category === 'general' && <span className='tiny'>{note.levelText}</span>}
			{note.comment && <span className='tiny'>{note.comment}</span>}
		</button>
	)
}

/** Ход разговора. У ответа ученика — подсвеченные цитаты и отметки под ним. */
function Turn({ turn, active, onShow }) {
	if (turn.role !== 'student') {
		return (
			<li className='cr-turn is-avatar'>
				<div className='cr-turn-who'>{turn.label}</div>
				<div className='cr-turn-body'>
					<div>{turn.content}</div>
					{turn.translation && <div className='tiny mt8'>{turn.translation}</div>}
				</div>
			</li>
		)
	}

	const lit = turn.notes.some((n) => n.id === active)
	// Исправление распознанного текста показано строкой «Распознано было» —
	// отдельная отметка под ответом повторяла бы её слово в слово.
	const repair = turn.notes.find((n) => n.category === 'repair')
	const chips = turn.notes.filter((n) => n.category !== 'repair')
	return (
		<li className={`cr-turn is-student${lit ? ' is-lit' : ''}`} id={turn.anchor} tabIndex={-1}>
			<div className='cr-turn-who'>
				{turn.label}
				<span className='tiny'>{turn.voice ? 'голосом' : 'текстом'}</span>
			</div>
			<div className='cr-turn-body'>
				<div>
					{turn.segments.map((s, i) =>
						s.ids.length ? (
							<mark className={`cr-q cr-tone-${s.tone}${s.ids.includes(active) ? ' is-active' : ''}`} key={i}>
								{s.text}
							</mark>
						) : (
							<span key={i}>{s.text}</span>
						),
					)}
				</div>
				{turn.edited && turn.asrText && (
					<div className={`cr-repair${repair && repair.id === active ? ' is-active' : ''}`}>
						Распознано было: <s>{turn.asrText}</s> — ученик исправил перед отправкой
					</div>
				)}
				{chips.length > 0 && (
					<div className='cr-notes'>
						{chips.map((n) => (
							<button
								type='button'
								key={n.id}
								className={`cr-note cr-tone-${n.category}${n.id === active ? ' is-active' : ''}`}
								onClick={() => onShow(n)}
							>
								{noteLine(n)}
							</button>
						))}
					</div>
				)}
			</div>
		</li>
	)
}
