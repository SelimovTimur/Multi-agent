// HardWords.jsx — блок «Что повторить на следующем уроке» (#67).
//
// Слова и выражения из лексики уроков, в которых класс сделал больше всего
// ошибок, — в заданиях и в разговоре с аватаром. Какое слово трудное и в
// каком порядке слова идут, решает сервер (internal/learning/hard_words.go):
// экран только показывает первые три и фильтрует по уроку.
//
// Строка короткая: слово, перевод, урок и «10 ошибок у 6 учеников». Откуда
// ошибки и какие — в раскрытии, чтобы список читался за секунды.
//
// Тот же список (HardWordList) стоит ещё в двух местах (#68): в карточке
// ученика — его слова с его числом ошибок и числом класса для сравнения, и
// на странице урока — вкладка «Трудные слова» с теми же числами, но только
// по этому уроку.

import { useState } from 'react'
import { Link } from 'react-router-dom'
import { plural, pluralWord } from '../ui'
import { mistakeLabel, scoreTone, studentErrors } from './results'

// Сколько слов видно сразу. Остальные — по кнопке: учителю к следующему
// уроку нужны главные, а не весь список.
const WORDS_SHOWN = 3

export default function HardWords({ classId, lessons, words }) {
	const [lesson, setLesson] = useState('')

	// Фильтр — только по урокам, где трудные слова есть: кнопка урока,
	// которая всегда показывает «слов нет», учителю ничего не говорит.
	const withWords = lessons.filter((l) => words.some((w) => w.assignment_id === l.assignment_id))
	const list = lesson ? words.filter((w) => w.assignment_id === lesson) : words

	return (
		<section className='mt28 hw-section'>
			<h2 className='h2'>Что повторить на следующем уроке</h2>
			<p className='muted mt8'>
				Слова из уроков, в которых класс сделал больше всего ошибок, в заданиях и в разговоре с аватаром. Слово
				попадает сюда, когда ошиблись хотя бы двое и не меньше четверти учеников, которым оно встречалось, а
				неверных ответов с ним — от 30%. Нажмите на слово, чтобы увидеть ошибки.
			</p>

			{withWords.length > 1 && (
				<div className='chips mt12' role='group' aria-label='Урок'>
					<button className='chip' type='button' aria-pressed={!lesson} onClick={() => setLesson('')}>
						Все уроки
					</button>
					{withWords.map((l) => (
						<button
							key={l.assignment_id}
							className='chip'
							type='button'
							aria-pressed={lesson === l.assignment_id}
							onClick={() => setLesson(l.assignment_id)}
						>
							{l.title || 'Урок'}
						</button>
					))}
				</div>
			)}

			<HardWordList classId={classId} words={list} limit={WORDS_SHOWN} />
		</section>
	)
}

/**
 * Список трудных слов с раскрытием. limit — сколько видно до кнопки
 * «Показать все». studentId — карточка ученика: в строке его ошибки и для
 * сравнения ошибки класса. compact — страница урока: урок в строке не
 * подписан, он и так в заголовке.
 */
export function HardWordList({ classId, words, limit, studentId, compact }) {
	const [all, setAll] = useState(false)
	const [open, setOpen] = useState(null)
	const shown = all || !limit ? words : words.slice(0, limit)
	return (
		<div className='hw-list mt12'>
			{words.length === 0 ? (
				<div className='hw-empty'>Слов, в которых класс часто ошибается, нет.</div>
			) : (
				shown.map((w) => {
					const key = `${w.assignment_id}:${w.phrase}`
					return (
						<HardWord
							key={key}
							classId={classId}
							word={w}
							studentId={studentId}
							compact={compact}
							open={open === key}
							onToggle={() => setOpen((cur) => (cur === key ? null : key))}
						/>
					)
				})
			)}
			{limit && words.length > limit && (
				<div className='hw-more'>
					<button className='btn btn-ghost btn-compact' type='button' onClick={() => setAll((v) => !v)}>
						{all ? 'Свернуть' : `Показать все ${plural(words.length, 'слово', 'слова', 'слов')}`}
					</button>
				</div>
			)}
		</div>
	)
}

/** Одно слово: строка и раскрытие. Отдельно — для smoke-проверки раскрытия. */
export function HardWord({ classId, word: w, studentId, compact, open, onToggle }) {
	const erred = w.struggled.length
	const lessonUrl = `/app/class/${classId}/analytics/lesson/${w.assignment_id}`
	const mine = studentId ? studentErrors(w, studentId) : null
	return (
		<div className='hw' data-open={open || undefined}>
			<button className='hw-row' type='button' aria-expanded={open} onClick={onToggle}>
				<span>
					<span className='hw-word'>{w.phrase}</span>
					<span className='hw-sub'>
						{[w.translation, !compact && `урок «${w.lesson_title || 'Урок'}»`].filter(Boolean).join(' · ')}
					</span>
				</span>
				<span className='hw-count'>
					<b>{plural(mine ?? w.errors, 'ошибка', 'ошибки', 'ошибок')}</b>
					<span className='tiny'>
						{mine == null
							? `у ${plural(erred, 'ученика', 'учеников', 'учеников')}`
							: `у класса всего ${w.errors}`}
					</span>
				</span>
			</button>

			{open && (
				<div className='hw-more-grid'>
					<div className='hw-col'>
						<Written tasks={w.tasks} written={w.written} />
						<Spoken conversation={w.conversation} spoken={w.spoken} lessonUrl={lessonUrl} />
					</div>
					<div className='hw-col'>
						{w.confusions.length > 0 && (
							<>
								<div className='eyebrow'>С чем путают</div>
								<div className='hw-chips'>
									{w.confusions.map((c) => (
										<span key={c} className='badge badge-gray'>
											{c}
										</span>
									))}
								</div>
							</>
						)}
						<div className={`eyebrow ${w.confusions.length ? 'mt16' : ''}`}>Кому повторить</div>
						<div className='hw-chips'>
							{w.struggled.map((s) => (
								<Link key={s.student_id} className='chip' to={`/app/class/${classId}/student/${s.student_id}`}>
									{s.display_name}
								</Link>
							))}
						</div>
					</div>
				</div>
			)}
		</div>
	)
}

/** Ошибки в письменных заданиях: одинаковые ответы склеены с числом. */
function Written({ tasks, written }) {
	return (
		<>
			<div className='eyebrow'>
				В письменных заданиях
				{tasks.correct_percent != null && (
					<>
						{' · '}
						<span className={`badge ${scoreTone(tasks.correct_percent)}`}>{tasks.correct_percent}% верных</span>
					</>
				)}
			</div>
			{written.length === 0 ? (
				<p className='tiny'>
					{tasks.answers ? 'Ошибок в заданиях нет.' : 'В заданиях этого слова не было.'}
				</p>
			) : (
				written.map((m, i) => (
					<div key={i} className='hw-quote'>
						<div className='row-between'>
							<span className='hw-said'>{mistakeLabel(m)}</span>
							<span className='badge badge-err'>{plural(m.count, 'раз', 'раза', 'раз')}</span>
						</div>
						<span className='small'>
							Верно: <span className='hw-fix'>{m.expected || '—'}</span>
						</span>
						<span className='tiny'>{`«${m.task_title}» · ${names(m.students)}`}</span>
					</div>
				))
			)}
		</>
	)
}

/** Ошибки в разговоре: цитата, верная форма, ссылка на расшифровку. */
function Spoken({ conversation, spoken, lessonUrl }) {
	const c = conversation
	return (
		<>
			<div className='eyebrow mt16'>
				В разговоре с аватаром
				{c.students > 0 && (
					<>
						{' · '}
						{c.erred
							? `${c.erred} из ${c.students} ${pluralWord(c.erred, 'ошибся', 'ошиблись', 'ошиблись')}`
							: 'ошибок нет'}
					</>
				)}
			</div>
			{spoken.length === 0 ? (
				<p className='tiny'>
					{c.students ? 'В разговоре это слово говорят верно.' : 'В разговоре это слово не встречалось.'}
				</p>
			) : (
				spoken.map((q, i) => (
					<div key={i} className='hw-quote'>
						<span className='hw-said'>{q.quote || '—'}</span>
						{q.correct_form && (
							<span className='small'>
								Верно: <span className='hw-fix'>{q.correct_form}</span>
							</span>
						)}
						<span className='tiny'>
							{`${q.student.display_name} · реплика ${q.turn_no} · `}
							<Link to={`${lessonUrl}?tab=conversation&student=${q.student.student_id}`}>открыть разговор</Link>
						</span>
					</div>
				))
			)}
		</>
	)
}

// Имена в подписи ошибки: трое и «ещё N» — полный список в «Кому повторить».
function names(students) {
	const first = students.slice(0, 3).map((s) => s.display_name)
	const rest = students.length - first.length
	return rest > 0 ? `${first.join(', ')} и ещё ${rest}` : first.join(', ')
}
