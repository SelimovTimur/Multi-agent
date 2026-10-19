// Profile.jsx — профиль учителя (NR-11).
//
// Две формы рядом, как в макете: личные данные и смена пароля. Email
// показан, но не редактируется — он привязан к аккаунту, и его смена это
// отдельная процедура с подтверждением нового адреса, а не поле в форме.

import { useState } from 'react'
import { ApiError, profile as profileApi } from '../../api/v1'
import { useAuth } from '../AuthContext'
import { Shell } from '../Shell'
import { Button, Field, Input, useToast } from '../ui'

export default function Profile() {
	const { actor, refresh } = useAuth()
	const toast = useToast()

	const [name, setName] = useState(actor?.display_name || '')
	const [nameErr, setNameErr] = useState(null)
	const [nameBusy, setNameBusy] = useState(false)

	const [pwd, setPwd] = useState({ cur: '', p1: '', p2: '' })
	const [pwdErrors, setPwdErrors] = useState({})
	const [pwdBusy, setPwdBusy] = useState(false)

	async function saveName(e) {
		e.preventDefault()
		setNameErr(null)
		setNameBusy(true)
		try {
			await profileApi.updateName(name.trim())
			await refresh()
			toast('Профиль сохранён')
		} catch (err) {
			setNameErr(err instanceof ApiError && err.isValidation ? err.fields?.name || err.message : err.message)
		} finally {
			setNameBusy(false)
		}
	}

	async function changePassword(e) {
		e.preventDefault()
		if (pwd.p1 !== pwd.p2) {
			setPwdErrors({ p2: 'Пароли не совпадают' })
			return
		}
		setPwdErrors({})
		setPwdBusy(true)
		try {
			await profileApi.changePassword(pwd.cur, pwd.p1)
			setPwd({ cur: '', p1: '', p2: '' })
			toast('Пароль изменён. Другие устройства вышли из аккаунта')
		} catch (err) {
			if (err instanceof ApiError && err.isValidation) setPwdErrors(err.fields || {})
			else setPwdErrors({ cur: err.message })
		} finally {
			setPwdBusy(false)
		}
	}

	return (
		<Shell>
			<h1 className='h1'>Профиль</h1>

			<div className='grid grid-2 mt22' style={{ maxWidth: 900, alignItems: 'start' }}>
				<form className='card-form card card-white' onSubmit={saveName}>
					<div className='h3'>Личные данные</div>
					<Field label='Имя' error={nameErr}>
						<Input value={name} onChange={(e) => setName(e.target.value)} />
					</Field>
					<Field label='Email' hint='Email изменить нельзя — он привязан к аккаунту'>
						<Input value={actor?.email || ''} disabled />
					</Field>
					<div>
						<Button variant='primary' type='submit' disabled={nameBusy}>
							{nameBusy ? 'Сохраняем…' : 'Сохранить'}
						</Button>
					</div>
				</form>

				<form className='card-form card card-white' onSubmit={changePassword}>
					<div className='h3'>Смена пароля</div>
					<Field label='Текущий пароль' error={pwdErrors.cur || pwdErrors.current_password}>
						<Input
							type='password'
							autoComplete='current-password'
							value={pwd.cur}
							onChange={(e) => setPwd((v) => ({ ...v, cur: e.target.value }))}
						/>
					</Field>
					<Field label='Новый пароль' hint='Не короче 8 символов' error={pwdErrors.new_password || pwdErrors.p1}>
						<Input
							type='password'
							autoComplete='new-password'
							value={pwd.p1}
							onChange={(e) => setPwd((v) => ({ ...v, p1: e.target.value }))}
						/>
					</Field>
					<Field label='Повторите пароль' error={pwdErrors.p2}>
						<Input
							type='password'
							autoComplete='new-password'
							value={pwd.p2}
							onChange={(e) => setPwd((v) => ({ ...v, p2: e.target.value }))}
						/>
					</Field>
					<div className='tiny'>После смены пароля все другие устройства выйдут из аккаунта.</div>
					<div>
						<Button type='submit' disabled={pwdBusy}>
							{pwdBusy ? 'Меняем…' : 'Изменить пароль'}
						</Button>
					</div>
				</form>
			</div>
		</Shell>
	)
}
