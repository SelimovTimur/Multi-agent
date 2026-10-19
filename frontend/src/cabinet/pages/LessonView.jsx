// LessonView.jsx — готовый урок из библиотеки.
//
// Учитель видит, о чём будет разговор с аватаром: цели урока, роль аватара,
// фразы и грамматику, которые ученик должен употребить, и текст, по
// которому идёт разговор. Отсюда урок назначается классу.

import { useCallback, useEffect, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { lessons as lessonsApi } from '../../api/v1'
import { Shell } from '../Shell'
import { Button, langLabel, useToast } from '../ui'
import { AssignDialog } from './LibraryDialogs'

export default function LessonView() {
	const { lessonId } = useParams()
	const navigate = useNavigate()
	const toast = useToast()

	const [data, setData] = useState(null)
	const [content, setContent] = useState(null)
	const [assigning, setAssigning] = useState(false)

	const load = useCallback(() => {
		lessonsApi
			.get(lessonId)
			.then((res) => {
				setData(res)
				const published = res.versions?.find((v) => v.status === 'published') || res.versions?.[0]
				if (published) lessonsApi.version(published.id).then(setContent).catch(() => setContent(null))
			})
			.catch((err) => {
				toast(err.message)
				navigate('/app/library', { replace: true })
			})
	}, [lessonId, navigate, toast])

	useEffect(load, [load])

	if (!data) {
		return (
			<Shell>
				<div className='tiny'>Загружаем…</div>
			</Shell>
		)
	}

	const lesson = data.lesson
	const avatar = content?.avatar
	const goals = [...(avatar?.practice_phrases || []), ...(avatar?.practice_grammar || [])]

	return (
		<Shell>
			<div className='row-between'>
				<div>
					<h1 className='h1'>{lesson.title}</h1>
					<div className='lead mt8'>
						{langLabel(lesson.lang)}, уровень {lesson.level}
					</div>
				</div>
				<div className='row'>
					<Button icon='back' to='/app/library'>
						К библиотеке
					</Button>
					<Button variant='primary' icon='share' onClick={() => setAssigning(true)}>
						Назначить классу
					</Button>
				</div>
			</div>

			{!content ? (
				<div className='tiny mt22'>Загружаем…</div>
			) : (
				<>
					<div className='grid grid-2 mt22' style={{ alignItems: 'start' }}>
						<div className='card card-white'>
							<div className='h3'>Разговор с аватаром</div>
							{content.objectives?.length > 0 && (
								<ul className='stack-s mt8' style={{ paddingLeft: 18, listStyle: 'disc' }}>
									{content.objectives.map((o) => (
										<li key={o.id}>{o.text}</li>
									))}
								</ul>
							)}
							{avatar?.role_prompt && <p className='tiny mt16'>Роль аватара: {avatar.role_prompt}</p>}
							{avatar?.max_turns > 0 && <p className='tiny mt8'>Ответов ученика в разговоре: {avatar.max_turns}</p>}
						</div>

						<div className='card card-white'>
							<div className='h3'>Что ученик употребит в разговоре</div>
							{goals.length ? (
								<div className='vocab-list mt8'>
									{goals.map((g) => (
										<div className='vocab-row' key={g}>
											<span className='ph'>{g}</span>
										</div>
									))}
								</div>
							) : (
								<div className='tiny mt8'>Цели разговора не заданы</div>
							)}
						</div>
					</div>

					<div className='card card-white mt22'>
						<div className='h3'>Текст урока</div>
						<p className='mt8' style={{ lineHeight: 1.7, whiteSpace: 'pre-line' }}>
							{content.refined_text}
						</p>
					</div>
				</>
			)}

			{assigning && (
				<AssignDialog
					lessonId={lessonId}
					onClose={() => setAssigning(false)}
					onDone={(msg) => {
						setAssigning(false)
						toast(msg)
						load()
					}}
				/>
			)}
		</Shell>
	)
}
