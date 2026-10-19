// ConversationTab.jsx — отчёт по разговору с аватаром по уроку класса
// (У-2, У-3, У-4).
//
// Вкладка «Разговор с аватаром» страницы урока в аналитике (#66; раньше
// жила на экране «Урок в классе»): статусы учеников,
// представления «По ученикам» и «По темам», общие ошибки класса и карточка
// ученика с расшифровкой. Что открыто — в адресе (?tab=conversation,
// &view=themes, &student=<id>): из боковой панели ученика сюда ведёт обычная
// ссылка, а «Назад» в браузере возвращает к списку.
//
// Отчёт ученика появляется после завершения разговора и потом не меняется.
// Разговор в реальном времени учитель не видит — это решение договора
// (docs/product/avatar-goals/contract.md, раздел 0), а не ограничение экрана.

import { useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { assignments as api } from '../../../api/v1'
import Icon from '../../icons'
import { Button, Table, plural } from '../../ui'
import {
	commonErrorGroups,
	countsView,
	goalsLine,
	levelLabel,
	readyCount,
	studentRows,
	themeGroups,
} from './report'
import StudentConversation from './StudentConversation'
import { fatal, usePolling } from './usePolling'
import './conversation.css'

// Отчёт класса меняется, когда ученики заканчивают разговоры, — раз в
// полминуты достаточно: разбор ученика готов не сразу, а смотрят отчёт
// после урока, а не во время.
const REPORT_POLL_MS = 30000
const REQUEST_TIMEOUT_MS = 15000

export default function ConversationTab({ assignmentId, dialogIssued = true }) {
	const [params, setParams] = useSearchParams()
	const studentId = params.get('student') || ''
	const view = params.get('view') === 'themes' ? 'themes' : 'students'
	// stopped — отказ, после которого опрос встал (нет доступа, нет выдачи):
	// обещать «попробуем ещё раз» тогда нельзя.
	const [state, setState] = useState({ data: null, error: '', stopped: false })
	// Цитата из общих ошибок класса, к которой надо сразу перейти в карточке.
	const [focus, setFocus] = useState(null)

	// Пока открыта карточка ученика, отчёт класса не опрашиваем: учитель его
	// не видит. Вернулся к списку — перечитываем сразу.
	usePolling(
		async (signal) => {
			if (studentId) return null
			try {
				const data = await api.conversationReport(assignmentId, { signal, timeoutMs: REQUEST_TIMEOUT_MS })
				if (signal.aborted) return null
				setState({ data, error: '', stopped: false })
				return REPORT_POLL_MS
			} catch (err) {
				if (signal.aborted) return null
				setState((s) => ({ ...s, error: err.message, stopped: fatal(err) }))
				return fatal(err) ? null : REPORT_POLL_MS
			}
		},
		[assignmentId, Boolean(studentId)],
	)

	function change(patch) {
		const next = new URLSearchParams(params)
		for (const [key, value] of Object.entries(patch)) {
			if (value) next.set(key, value)
			else next.delete(key)
		}
		setParams(next)
	}

	if (studentId) {
		const row = state.data?.students?.find((s) => s.student?.id === studentId)
		return (
			<StudentConversation
				key={studentId}
				assignmentId={assignmentId}
				studentId={studentId}
				known={row ? { ...row.student, status: row.status, turns_taken: row.turns_taken } : null}
				focus={focus}
				onBack={() => {
					setFocus(null)
					change({ student: '' })
				}}
			/>
		)
	}

	return (
		<ConversationReportView
			data={state.data}
			error={state.error}
			retrying={!state.stopped}
			view={view}
			dialogIssued={dialogIssued}
			onView={(next) => change({ view: next === 'themes' ? 'themes' : '' })}
			onOpenStudent={(id, quote = null) => {
				setFocus(quote)
				change({ student: id })
			}}
		/>
	)
}

/**
 * ConversationReportView — отчёт класса по готовому ответу ручки.
 *
 * data — ответ `GET /assignments/{id}/conversation` или null, пока он не
 * пришёл. onOpenStudent(id, quote?) открывает карточку ученика; quote —
 * { turnNo, quote }, если открыли кликом по цитате общей ошибки.
 */
export function ConversationReportView({
	data,
	error = '',
	retrying = true,
	view = 'students',
	dialogIssued = true,
	onView,
	onOpenStudent,
}) {
	if (!data) {
		return (
			<section className='cr mt22' aria-label='Разговор с аватаром'>
				{error ? (
					<div className='notice notice-err'>
						Не удалось загрузить отчёт по разговору: {error}.
						{retrying ? ' Экран попробует ещё раз сам.' : ''}
					</div>
				) : (
					<div className='tiny'>Загружаем отчёт по разговору…</div>
				)}
			</section>
		)
	}

	if (data.enabled === false) {
		return (
			<section className='cr mt22' aria-label='Разговор с аватаром'>
				<div className='card cl-empty'>
					<div className='h3'>В этой версии урока нет разговора с аватаром</div>
				</div>
			</section>
		)
	}

	const ready = readyCount(data)
	const goals = goalsLine(data.goals)

	return (
		<section className='cr mt22' aria-label='Разговор с аватаром'>
			<div className='cr-hd'>
				<div>
					<h2 className='h3'>Разговор с аватаром</h2>
					<div className='tiny mt8'>
						{[
							`уровень урока ${levelLabel(data.level)}`,
							data.max_turns && `${plural(data.max_turns, 'ответ', 'ответа', 'ответов')} ученика`,
							goals && `цели: ${goals}`,
						]
							.filter(Boolean)
							.join(' · ')}
					</div>
				</div>
				{error && (
					<span className='tiny cr-lost'>{retrying ? 'нет связи · данные могли устареть' : error}</span>
				)}
			</div>

			{!dialogIssued && (
				<div className='notice notice-warn mt12'>
					Задание «Диалог» ещё не выдано — ученики не могут начать разговор. Выдайте его на экране
					«Урок в классе».
				</div>
			)}

			<div className='cr-counts mt16'>
				{countsView(data).map((c) => (
					<div className='cr-count' key={c.key}>
						<b>{c.n}</b>
						<span>{c.label}</span>
					</div>
				))}
			</div>
			<div className='small muted mt12'>
				Отчёт по ученику появляется, когда он завершит разговор, и после этого не меняется. Ошибки
				выше уровня урока в отчёт не попадают.
			</div>

			<div className='chips mt16' role='group' aria-label='Представление отчёта'>
				<button
					className='chip'
					type='button'
					aria-pressed={view === 'students'}
					onClick={() => onView?.('students')}
				>
					По ученикам
				</button>
				<button
					className='chip'
					type='button'
					aria-pressed={view === 'themes'}
					onClick={() => onView?.('themes')}
				>
					По темам
				</button>
			</div>

			{view === 'themes' ? (
				<ThemesView groups={themeGroups(data)} onOpenStudent={onOpenStudent} />
			) : (
				<StudentsView rows={studentRows(data)} onOpenStudent={onOpenStudent} />
			)}

			<CommonErrors groups={commonErrorGroups(data)} ready={ready} onOpenStudent={onOpenStudent} />
		</section>
	)
}

/** Число в таблице: ноль серым не прячем, ошибки — красным; нет данных — «—». */
function count(n, bad = false) {
	if (n === null || n === undefined) return '—'
	return bad && n > 0 ? <span className='cr-bad'>{n}</span> : n
}

/** «По ученикам»: статус, ответы, зачтённые цели, ошибки по целям и промахи. */
function StudentsView({ rows, onOpenStudent }) {
	const open = (id) => onOpenStudent?.(id)
	return (
		<div className='cr-table mt16'>
			<Table
				head={['Ученик', 'Статус', 'Ответов', 'Зачтено целей', 'Ошибки по целям', 'Промахи']}
				empty='В классе пока нет учеников'
				rows={rows.map((r) => ({
					key: r.id,
					onClick: () => open(r.id),
					cells: [
						<span key='n'>
							<button
								className='cr-name'
								type='button'
								onClick={(e) => {
									// Строка тоже открывает карточку — второй раз не нужно.
									e.stopPropagation()
									open(r.id)
								}}
							>
								{r.name}
							</button>{' '}
							{r.tag && <span className='tiny'>#{r.tag}</span>}
						</span>,
						<span className={`badge ${r.statusCls}`} key='s'>
							{r.statusLabel}
						</span>,
						r.answers,
						r.credited || <span className='tiny'>{r.note || '—'}</span>,
						count(r.errors, true),
						count(r.misses),
					],
					mobile: {
						title: r.name,
						badge: <span className={`badge ${r.statusCls}`}>{r.statusLabel}</span>,
						kv: [
							['Ответов', r.answers],
							['Зачтено целей', r.credited || r.note || '—'],
							['Ошибки по целям', count(r.errors, true)],
							['Промахи', count(r.misses)],
						],
						actions: (
							<Button className='btn-compact' onClick={() => open(r.id)}>
								Открыть разговор
							</Button>
						),
					},
				}))}
			/>
		</div>
	)
}

/**
 * «По темам»: по каждой фразе и теме — верные употребления, ошибки и
 * сколько учеников зачли; раскрытие — ученики.
 */
function ThemesView({ groups, onOpenStudent }) {
	const [open, setOpen] = useState('')
	if (!groups.length) return <div className='empty'>У разговора нет целей</div>

	return groups.map((g) => (
		<div className='mt22' key={g.kind}>
			<div className='t-name'>{g.title}</div>
			<div className='cr-list mt8'>
				{g.items.map((t) => {
					const expanded = open === t.key
					return (
						<div className='cr-item' key={t.key}>
							<button
								className='cr-item-hd'
								type='button'
								aria-expanded={expanded}
								onClick={() => setOpen(expanded ? '' : t.key)}
							>
								<span className='grow'>
									<span className='cr-item-t'>{t.title}</span>
									{t.note && <span className='cr-item-sub'>{t.note}</span>}
								</span>
								<span className='cr-stats'>
									<span>{t.usesText}</span>
									<span className={t.errors ? 'cr-bad' : ''}>{t.errorsText}</span>
									<span>{t.creditedText}</span>
								</span>
								<Icon name={expanded ? 'up' : 'down'} size={18} />
							</button>
							{expanded && (
								<div className='cr-item-bd'>
									{t.students.length === 0 ? (
										<div className='tiny mt8'>Готовых отчётов пока нет.</div>
									) : (
										<table className='cr-mini'>
											<thead>
												<tr>
													<th>Ученик</th>
													<th>Верно</th>
													<th>Ошибки</th>
													<th>Промахи</th>
												</tr>
											</thead>
											<tbody>
												{t.students.map((s) => (
													<tr key={s.id}>
														<td>
															<button className='cr-name' type='button' onClick={() => onOpenStudent?.(s.id)}>
																{s.name}
															</button>{' '}
															{s.credited && <span className='badge badge-green'>зачтено</span>}
														</td>
														<td>{s.correctUses}</td>
														<td>{count(s.errors, true)}</td>
														<td>{s.misses}</td>
													</tr>
												))}
											</tbody>
										</table>
									)}
								</div>
							)}
						</div>
					)
				})}
			</div>
		</div>
	))
}

/**
 * Общие ошибки класса: группы по слову или конструкции с числом; по клику —
 * ученики и их цитаты. Цитата ведёт в расшифровку ученика.
 */
function CommonErrors({ groups, ready, onOpenStudent }) {
	const [open, setOpen] = useState('')

	return (
		<section className='mt28' aria-label='Общие ошибки класса'>
			<h3 className='h3'>Общие ошибки класса</h3>
			<div className='small muted mt8'>
				Ошибки вне целей урока в том, что ученик уже должен знать на уровне урока. Сгруппированы по
				слову или конструкции.
			</div>

			{groups.length === 0 ? (
				<div className='tiny mt12'>
					{ready ? 'Общих ошибок нет.' : 'Появятся, когда будут готовы отчёты учеников.'}
				</div>
			) : (
				<div className='cr-list mt12'>
					{groups.map((g) => {
						const expanded = open === g.key
						return (
							<div className='cr-item' key={g.key}>
								<button
									className='cr-item-hd'
									type='button'
									aria-expanded={expanded}
									onClick={() => setOpen(expanded ? '' : g.key)}
								>
									<span className='grow'>
										<span className='cr-item-t'>{g.label}</span>
									</span>
									<span className='cr-tags'>
										<span className='badge badge-gray'>{g.typeLabel}</span>
										<span className={`badge ${g.unknownLevel ? 'badge-warn' : 'badge-lvl'}`}>
											{g.levelLabel}
										</span>
									</span>
									<span className='cr-stats'>
										<span>{g.countText}</span>
									</span>
									<Icon name={expanded ? 'up' : 'down'} size={18} />
								</button>
								{expanded && (
									<div className='cr-item-bd'>
										{g.students.map((s) => (
											<div className='cr-who' key={s.id}>
												<button className='cr-name' type='button' onClick={() => onOpenStudent?.(s.id)}>
													{s.name}
												</button>
												<div className='cr-quotes'>
													{s.quotes.map((q, i) => (
														<button
															className='cr-quote'
															type='button'
															key={i}
															onClick={() => onOpenStudent?.(s.id, { turnNo: q.turnNo, quote: q.quote })}
														>
															<span className='tiny'>Ответ {q.turnNo}</span>
															<span>«{q.quote}»</span>
															{q.correctForm && (
																<span>
																	→ <b>{q.correctForm}</b>
																</span>
															)}
														</button>
													))}
												</div>
											</div>
										))}
									</div>
								)}
							</div>
						)
					})}
				</div>
			)}
		</section>
	)
}
