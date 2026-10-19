// ClassCreate.jsx — создание класса (NR-20, экран для NR-17).
//
// Модалка поверх главной, как в макете: создание класса — короткое действие,
// уводить ради него на отдельную страницу и терять контекст незачем.

import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { ApiError, classes as classesApi } from '../../api/v1'
import Home from './Home'
import { Button, Field, Input, LANGS, Modal, Select, useToast } from '../ui'
import { CEFR_LEVELS } from '../levels'

// Языки — общим списком из ui: в MVP один английский.

export default function ClassCreate() {
	const navigate = useNavigate()
	const toast = useToast()
	const [form, setForm] = useState({ name: '', subject: '', lang: 'tatar', level: 'A2' })
	const [errors, setErrors] = useState({})
	const [busy, setBusy] = useState(false)

	const set = (k) => (e) => setForm((f) => ({ ...f, [k]: e.target.value }))

	async function submit(e) {
		e?.preventDefault()
		setErrors({})
		setBusy(true)
		try {
			const res = await classesApi.create({
				name: form.name.trim(),
				subject: form.subject.trim(),
				level: form.level,
				lang: form.lang,
			})
			// Новый класс пуст, а сами ученики в него не войдут: их заводят
			// списком. Поэтому сразу ведём на «Учеников» с открытым окном
			// добавления, а не на экран с кодом класса, как раньше.
			navigate(`/app/class/${res.class.id}/students?add=1`, { replace: true })
		} catch (err) {
			if (err instanceof ApiError && err.isValidation) setErrors(err.fields || {})
			else toast(err.message)
		} finally {
			setBusy(false)
		}
	}

	return (
		<>
			{/* Под модалкой видна главная — человек не теряет из виду, куда
			    вернётся, если передумает. */}
			<Home />
			<Modal
				title='Новый класс'
				onClose={() => navigate('/app')}
				footer={
					<>
						<Button onClick={() => navigate('/app')}>Отмена</Button>
						<Button variant='primary' onClick={submit} disabled={busy}>
							{busy ? 'Создаём…' : 'Создать класс'}
						</Button>
					</>
				}
			>
				<form className='stack' onSubmit={submit}>
					<Field label='Название класса' error={errors.title}>
						<Input value={form.name} onChange={set('name')} placeholder='Например, 8В' autoFocus />
					</Field>
					<Field label='Направление' error={errors.subject}>
						<Input value={form.subject} onChange={set('subject')} placeholder='Татарский язык' />
					</Field>
					<Field label='Язык обучения' error={errors.lang_code}>
						<Select options={LANGS} value={form.lang} onChange={set('lang')} />
					</Field>
					<Field
						label='Уровень по умолчанию'
						hint='Подставится при создании уроков для этого класса'
						error={errors.level}
					>
						<Select options={CEFR_LEVELS} value={form.level} onChange={set('level')} />
					</Field>
					{/* Кнопка нужна, чтобы Enter в поле отправлял форму. */}
					<button type='submit' hidden />
				</form>
			</Modal>
		</>
	)
}
