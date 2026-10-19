// StudentFeedback.jsx — короткий опрос ученика после урока.
//
// Открывается, когда ученик посмотрел итог и уходит к своим занятиям:
// результаты он уже видел, и окно их не загораживает.
//
// Два шага. Сначала смайлики и необязательное поле «что было сложно». Если
// ученик поставил 🙁, второй шаг спрашивает, почему. Оценки уходят на сервер
// сразу после первого шага: закрыл второй — они всё равно сохранены.
//
// Всё необязательно, «Пропустить» закрывает окно. Опрос не должен стоять
// между ребёнком и его занятиями.

import { useState } from 'react'
import { my } from '../../api/v1'
import { APP_AVATAR } from '../../branding'
import { Button, Modal, useToast } from '../ui'
import { RATINGS, TEXT_LIMIT, ratingsPayload, whyPayload, whyQuestions } from './student-feedback'

function Smiles({ id, label, value, onChange }) {
	return (
		<div className='field' role='group' aria-labelledby={id}>
			<label id={id}>{label}</label>
			<div className='chips fb-smiles'>
				{RATINGS.map((r) => (
					<button
						key={r.value}
						type='button'
						className='chip'
						title={r.title}
						aria-label={r.title}
						aria-pressed={value === r.value}
						// Повторное нажатие снимает выбор: ответ необязателен.
						onClick={() => onChange(value === r.value ? '' : r.value)}
					>
						{r.label}
					</button>
				))}
			</div>
		</div>
	)
}

function TextField({ id, label, value, onChange }) {
	return (
		<div className='field'>
			<label htmlFor={id}>{label}</label>
			<textarea
				id={id}
				className='textarea'
				rows={3}
				maxLength={TEXT_LIMIT}
				value={value}
				onChange={(e) => onChange(e.target.value)}
				placeholder='Напиши своими словами'
			/>
		</div>
	)
}

/**
 * questions — что спросить: 'lesson' и/или 'avatar' (см. questionsToAsk).
 * onDone вызывается, когда окно закрыто — отправкой или пропуском.
 */
export default function StudentFeedback({ attemptId, questions, onDone }) {
	const toast = useToast()
	const [lesson, setLesson] = useState('')
	const [avatar, setAvatar] = useState('')
	const [unclear, setUnclear] = useState('')
	const [lessonWhy, setLessonWhy] = useState('')
	const [avatarWhy, setAvatarWhy] = useState('')
	// null — первый шаг; список — второй, про что спросить «почему».
	const [why, setWhy] = useState(null)
	const [busy, setBusy] = useState(false)

	const first = ratingsPayload({ lesson, avatar, unclear })
	const second = whyPayload({ lessonWhy, avatarWhy })

	async function submit(payload) {
		setBusy(true)
		try {
			await my.feedback(attemptId, payload)
			return true
		} catch {
			// Урок пройден, это главное. Неотправленный отзыв — не повод
			// держать ученика на этом экране.
			toast('Не удалось отправить ответы')
			return false
		} finally {
			setBusy(false)
		}
	}

	async function sendFirst() {
		const sent = await submit(first)
		const next = whyQuestions({ lesson, avatar })
		if (sent && next.length) {
			setWhy(next)
			return
		}
		if (sent) toast('Спасибо!')
		onDone()
	}

	async function sendSecond() {
		if (await submit(second)) toast('Спасибо!')
		onDone()
	}

	if (why) {
		return (
			<Modal
				title='Расскажи, почему?'
				sub='Так мы поймём, что исправить.'
				onClose={onDone}
				footer={
					<>
						<Button onClick={onDone} disabled={busy}>
							Пропустить
						</Button>
						<Button variant='primary' icon='check' onClick={sendSecond} disabled={busy || !second}>
							{busy ? 'Отправляем…' : 'Отправить'}
						</Button>
					</>
				}
			>
				<div className='stack'>
					{why.includes('lesson') && (
						<TextField
							id='sfb-lesson-why'
							label='Что не понравилось в уроке?'
							value={lessonWhy}
							onChange={setLessonWhy}
						/>
					)}
					{why.includes('avatar') && (
						<TextField
							id='sfb-avatar-why'
							label='Что не понравилось в разговоре с аватаром?'
							value={avatarWhy}
							onChange={setAvatarWhy}
						/>
					)}
				</div>
			</Modal>
		)
	}

	return (
		<Modal
			title='Как прошёл урок?'
			sub='Ответь в пару нажатий. Можно ответить не на все вопросы.'
			onClose={onDone}
			footer={
				<>
					<Button onClick={onDone} disabled={busy}>
						Пропустить
					</Button>
					<Button variant='primary' icon='check' onClick={sendFirst} disabled={busy || !first}>
						{busy ? 'Отправляем…' : 'Отправить'}
					</Button>
				</>
			}
		>
			<div className='stack'>
				{questions.includes('lesson') && (
					<Smiles id='sfb-lesson' label='Как тебе урок?' value={lesson} onChange={setLesson} />
				)}
				{questions.includes('avatar') && (
					<Smiles id='sfb-avatar' label={APP_AVATAR.feedbackQuestion} value={avatar} onChange={setAvatar} />
				)}
				{questions.includes('lesson') && (
					<TextField
						id='sfb-unclear'
						label='Что было непонятно или сложно в уроке?'
						value={unclear}
						onChange={setUnclear}
					/>
				)}
			</div>
		</Modal>
	)
}
