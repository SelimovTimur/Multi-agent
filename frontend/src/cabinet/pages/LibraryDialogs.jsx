// LibraryDialogs.jsx — назначить урок классу.
//
// В макете это модалка поверх библиотеки, и здесь так же: список
// остаётся виден, отмена возвращает ровно туда, откуда пришли.

import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { assignments, classes as classesApi, lessons as lessonsApi } from '../../api/v1'
import { Button, Field, Modal, Select, useToast } from '../ui'
import '../class-lesson.css'

/**
 * AssignDialog — назначить урок классу.
 *
 * Назначается **версия**, а не урок: класс, получивший урок в сентябре, в мае
 * видит сентябрьскую редакцию. Поэтому назначить можно только опубликованную
 * версию — черновик показывать ученикам нечего.
 *
 * Сроков здесь нет. Назначенный урок закрыт: ученики ничего не увидят, пока
 * учитель не выдаст задания на уроке или на дом, а срок бывает только у
 * домашней работы. Поэтому после «Назначить» ведём на вкладку «Уроки»
 * класса — задания выдают оттуда.
 */
export function AssignDialog({ lessonId, presetClassId, onClose, onDone }) {
	const toast = useToast()
	const navigate = useNavigate()
	// Ответ приходит конвертом {lesson, versions}: урок отдельно, его
	// редакции отдельно. Храним как есть, чтобы не разъезжаться с ним.
	const [data, setData] = useState(null)
	const [classList, setClassList] = useState([])
	const [classId, setClassId] = useState(presetClassId || '')
	// Редакция — самая свежая опубликованная: у готовых уроков она одна.
	const [versionId, setVersionId] = useState('')
	const [busy, setBusy] = useState(false)
	const [failed, setFailed] = useState('')
	// Отказ сервера по выбору в форме — «Этот урок уже назначен классу». Не
	// тост: тот гаснет через три секунды, а причина остаётся, пока учитель не
	// выберет другой класс.
	const [conflict, setConflict] = useState('')

	useEffect(() => {
		let alive = true
		Promise.all([lessonsApi.get(lessonId), classesApi.list()])
			.then(([res, cs]) => {
				if (!alive) return
				setData(res)
				const list = cs.classes || cs.items || []
				setClassList(list)
				// Класс за учителя не выбираем. Раньше сюда подставлялся первый
				// по списку, «Назначить» была сразу активна, и учитель с пятью
				// классами выдавал урок не тем детям — а снять выдачу, которую
				// кто-то успел начать, уже нельзя, только закрыть. Предвыбор
				// остаётся ровно в одном случае: урок открыт из класса.

				// По умолчанию — самая свежая опубликованная редакция.
				const published = (res.versions || []).filter((v) => v.status === 'published')
				if (published.length) setVersionId(published[published.length - 1].id)
			})
			.catch((err) => alive && setFailed(err.message))
		return () => {
			alive = false
		}
	}, [lessonId, presetClassId])

	const published = (data?.versions || []).filter((v) => v.status === 'published')

	async function assign() {
		if (!classId) {
			toast('Выберите класс')
			return
		}
		if (!versionId) {
			toast('У урока нет опубликованной редакции')
			return
		}
		setBusy(true)
		try {
			await assignments.create({ lessonVersionId: versionId, classId })
			onDone?.('Урок назначен. Задания пока закрыты.')
			navigate(`/app/class/${classId}/lessons`)
		} catch (err) {
			if (err.status === 409) setConflict(err.message)
			else toast(err.message)
			setBusy(false)
		}
	}

	return (
		<Modal
			title='Назначить урок'
			sub={data?.lesson?.title}
			onClose={onClose}
			footer={
				<>
					<Button onClick={onClose}>Отмена</Button>
					<Button
						variant='primary'
						onClick={assign}
						disabled={busy || !!failed || !!conflict || !classId}
						title={classId ? undefined : 'Сначала выберите класс'}
					>
						Назначить
					</Button>
				</>
			}
		>
			{failed && <p className='small'>{failed}</p>}

			{!failed && (
				<>
					<Field label='Класс' hint={classId ? undefined : 'Выберите, кому выдаёте урок'}>
						<Select
							placeholder='Не выбран'
							options={classList.map((c) => ({
								value: c.id,
								label: [c.name, c.subject].filter(Boolean).join(', '),
							}))}
							value={classId}
							onChange={(e) => {
								setClassId(e.target.value)
								setConflict('')
							}}
						/>
					</Field>

					{!published.length && (
						<div className='notice mt16'>У урока нет опубликованной редакции — ученикам его не выдать.</div>
					)}

					<ClosedNote />
					{conflict && <div className='notice notice-err mt16'>{conflict}</div>}
				</>
			)}
		</Modal>
	)
}

/**
 * ClosedNote — что будет после «Назначить».
 *
 * Раньше назначение сразу открывало урок ученикам. Теперь урок появляется в
 * классе закрытым, и без пояснения учитель ждал бы, что дети его уже видят.
 * Один текст на оба окна назначения: из библиотеки и со страницы урока.
 */
export function ClosedNote() {
	return (
		<div className='card cl-note mt16'>
			<b>Задания пока закрыты</b>
			<p>
				Урок появится в классе, но ученики ничего не увидят, пока вы не выдадите задания — на
				уроке или на дом.
			</p>
		</div>
	)
}

