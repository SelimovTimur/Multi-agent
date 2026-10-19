// Login.jsx — вход учителя и вход ученика по личному коду.
//
// Один экран на двоих — так в макете, и это правильно: ученик и учитель
// приходят по одной ссылке, и решать, кто он, должен человек, а не мы за
// него угадыванием. Карточки визуально разные (зелёная и золотая), чтобы
// ученик не начал вводить код в поле пароля.
//
// Ученик входит только по личному коду с карточки: учеников заводят
// списком учитель или команда платформы, самостоятельного входа в класс по
// коду класса нет. Поэтому ни имени, ни выбора класса здесь не спрашиваем —
// код уже знает и то, и другое.
//
// Учётки учителя заводит команда seed (см. README.md).

import { useState } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import { ApiError, student as studentApi } from '../../api/v1'
import { useAuth } from '../AuthContext'
import { AuthShell } from '../Shell'
import { loginDestination } from '../loginDestination'

export default function Login() {
	const { login, refresh } = useAuth()
	const navigate = useNavigate()
	const location = useLocation()
	// Gate запомнил, куда человек шёл до входа: вернём его туда, а не на
	// главную. Иначе после разлогина по таймауту он теряет своё место.
	const from = location.state?.from

	const [email, setEmail] = useState('')
	const [password, setPassword] = useState('')
	const [teacherErr, setTeacherErr] = useState(null)
	const [teacherBusy, setTeacherBusy] = useState(false)

	const [code, setCode] = useState('')
	const [codeErr, setCodeErr] = useState(null)
	const [codeBusy, setCodeBusy] = useState(false)

	async function submitTeacher(e) {
		e.preventDefault()
		setTeacherErr(null)
		setTeacherBusy(true)
		try {
			await login(email, password)
			navigate(loginDestination(from), { replace: true })
		} catch (err) {
			// Сервер намеренно отвечает одинаково на неверный пароль,
			// несуществующий аккаунт и неподтверждённую почту. Показываем
			// его текст как есть, не пытаясь угадать причину.
			setTeacherErr(err instanceof ApiError ? err.message : 'Не удалось войти')
		} finally {
			setTeacherBusy(false)
		}
	}

	async function submitCode(e) {
		e.preventDefault()
		setCodeErr(null)
		// Код переписывают с бумажной карточки: пробелы и дефисы, которые
		// ребёнок поставит «для красоты», ничего не значат.
		const value = code.replace(/[\s-]/g, '').toUpperCase()
		if (!value) {
			setCodeErr('Введи личный код')
			return
		}
		setCodeBusy(true)
		try {
			await studentApi.restore(value)
			// Спрашиваем сервер, кем он нас видит, вместо того чтобы верить
			// ответу на вход: если состояние разойдётся, экран будет здороваться
			// с учеником, а ручки — отвечать 404.
			await refresh()
			navigate(loginDestination(from, 'student'), { replace: true })
		} catch (err) {
			setCodeErr(err instanceof ApiError ? err.message : 'Не удалось войти')
		} finally {
			setCodeBusy(false)
		}
	}

	return (
		<AuthShell>
			<div className='auth-cols'>
				<form className='auth-card' onSubmit={submitTeacher}>
					<div className='auth-eyebrow'>Доступ учителя</div>
					<h1>Вход в сервис</h1>
					<p>Учитель входит по почте и паролю, ведёт классы и собирает уроки.</p>

					<div className='auth-fields'>
						<div className='auth-field'>
							<label htmlFor='f-email'>Email</label>
							<input
								className='input'
								id='f-email'
								type='email'
								autoComplete='username'
								value={email}
								onChange={(e) => setEmail(e.target.value)}
							/>
						</div>
						<div className='auth-field'>
							<label htmlFor='f-password'>Пароль</label>
							<input
								className='input'
								id='f-password'
								type='password'
								autoComplete='current-password'
								value={password}
								onChange={(e) => setPassword(e.target.value)}
							/>
							{teacherErr && <div className='err'>{teacherErr}</div>}
						</div>
					</div>

					<button className='auth-submit' type='submit' disabled={teacherBusy}>
						{teacherBusy ? 'Входим…' : 'Войти'}
					</button>
				</form>

				<form className='auth-card auth-card-gold' onSubmit={submitCode}>
					<div className='auth-eyebrow auth-eyebrow-gold'>Доступ ученика</div>
					<h1>Вход ученика</h1>
					<p>Введи личный код с карточки, который выдал тебе учитель.</p>

					<div className='auth-fields'>
						<div className='auth-field'>
							<label htmlFor='f-code'>Личный код</label>
							<input
								className='input mono'
								id='f-code'
								value={code}
								// Код переписывают с карточки, поэтому регистр и
								// пробелы не должны ничего значить.
								onChange={(e) => setCode(e.target.value.toUpperCase())}
								placeholder='D993PBEY'
								autoComplete='off'
								autoCapitalize='characters'
								autoCorrect='off'
								spellCheck='false'
							/>
							{codeErr && <div className='err'>{codeErr}</div>}
						</div>
					</div>

					<button className='auth-submit auth-submit-gold' type='submit' disabled={codeBusy}>
						{codeBusy ? 'Входим…' : 'Войти'}
					</button>
					<div className='auth-foot auth-foot-gold'>
						Код потерялся? Попроси учителя — он выдаст его заново.
					</div>
				</form>
			</div>
		</AuthShell>
	)
}
