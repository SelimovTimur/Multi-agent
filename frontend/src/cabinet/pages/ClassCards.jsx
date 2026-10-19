// ClassCards.jsx — карточки с личными кодами для печати.
//
// Учеников заводят списком, и каждый входит по своему коду. Код ребёнку
// нужно отдать в руки: карточку с именем, кодом и адресом сайта учитель
// печатает, разрезает и раздаёт на первом уроке.
//
// Отдельная страница без меню кабинета: на листе должны быть только
// карточки, и браузерная печать не должна тащить за собой сайдбар.
// Исключённых учеников на карточках нет — им входить некуда.

import { useEffect, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { classes as classesApi } from '../../api/v1'
import { Button, useToast } from '../ui'
import { readableHost } from '../site-address'

export default function ClassCards() {
	const { classId } = useParams()
	const navigate = useNavigate()
	const toast = useToast()
	const [klass, setKlass] = useState(null)
	const [students, setStudents] = useState(null)

	useEffect(() => {
		Promise.all([classesApi.get(classId), classesApi.students(classId)])
			.then(([c, s]) => {
				setKlass(c.class)
				setStudents(s.students || [])
			})
			.catch((err) => {
				toast(err.message)
				navigate('/app', { replace: true })
			})
	}, [classId, navigate, toast])

	if (!klass || !students) return <div className='nr nr-loading'>Загружаем…</div>

	const site = readableHost(window.location.host)

	return (
		<div className='nr cards-page'>
			<div className='cards-toolbar no-print'>
				<div>
					<h1 className='h1'>Карточки: {klass.name}</h1>
					<p className='lead mt8'>
						Напечатайте, разрежьте по линиям и раздайте ученикам. По коду с карточки ученик входит на
						сайт без логина и пароля.
					</p>
				</div>
				<div className='row'>
					<Button to={`/app/class/${classId}/students`}>К ученикам</Button>
					<Button variant='primary' icon='printer' onClick={() => window.print()} disabled={!students.length}>
						Напечатать
					</Button>
				</div>
			</div>

			{students.length === 0 ? (
				<div className='empty no-print'>В классе пока нет учеников — сначала добавьте их.</div>
			) : (
				<div className='cards-grid'>
					{students.map((s) => (
						<div className='student-card' key={s.student_id}>
							<div className='tiny'>{klass.name}</div>
							<div className='h3 mt8'>{s.name}</div>
							<div className='eyebrow mt12'>Личный код</div>
							<div className='student-card-code'>{s.code}</div>
							<div className='student-card-how'>
								Открой <b>{site}</b>, в блоке «Вход ученика» введи этот код и нажми «Войти».
							</div>
						</div>
					))}
				</div>
			)}
		</div>
	)
}
