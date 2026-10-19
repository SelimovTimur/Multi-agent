// AuthContext.jsx — кто сейчас в кабинете.
//
// Источник истины — сервер: при загрузке спрашиваем /auth/status. Токенов в
// localStorage нет и быть не может, сессия живёт в HttpOnly-cookie — до неё
// не дотягивается ни наш код, ни чужой скрипт.
//
// В контексте лежит и учитель, и ученик: приложение одно, и обоим нужен
// ответ на вопрос «кто я». Различает их поле kind.

import { createContext, useCallback, useContext, useEffect, useRef, useState } from 'react'
import { ApiError, auth as authApi, student as studentApi } from '../api/v1'

/** Пауза перед повторной проверкой входа без связи: 3, 6, 12, затем 15 с. */
const retryDelay = (n) => Math.min(15000, 3000 * 2 ** n)

const AuthContext = createContext(null)

export function AuthProvider({ children }) {
	// ready=false — мы ещё не знаем, кто пришёл. Показывать в этот момент
	// экран входа нельзя: вошедший учитель увидит мигание формы входа.
	const [ready, setReady] = useState(false)
	const [actor, setActor] = useState(null)
	// Проверить вход не вышло: нет связи, таймаут, сервер ответил 5xx.
	const [offline, setOffline] = useState(false)
	const retry = useRef({ timer: 0, n: 0 })

	const refresh = useCallback(async () => {
		clearTimeout(retry.current.timer)
		try {
			const res = await authApi.status()
			retry.current.n = 0
			setOffline(false)
			if (!res.authenticated) {
				setActor(null)
			} else {
				setActor({
					kind: res.kind,
					...(res.kind === 'student' ? res.student : res.user),
					// Сессия просмотра администратором. Пока она открыта, интерфейс
					// обязан показывать баннер на каждом экране (NEW-18).
					impersonated: Boolean(res.impersonated),
					readOnly: Boolean(res.read_only),
				})
			}
			setReady(true)
		} catch (err) {
			// Отказ сервера 4xx — честное «не вошёл». А сбой связи, таймаут или
			// 5xx не значат, что человек не вошёл: раньше на школьном Wi-Fi
			// такой сбой уводил на экран входа, ученик заново входил по коду
			// класса и получал второй профиль, а прогресс оставался в первом.
			// Поэтому известного человека не сбрасываем, а проверку повторяем.
			if (err instanceof ApiError && err.status >= 400 && err.status < 500) {
				setActor(null)
				setOffline(false)
				setReady(true)
				return
			}
			setOffline(true)
			const n = retry.current.n++
			retry.current.timer = setTimeout(() => refresh(), retryDelay(n))
		}
	}, [])

	useEffect(() => () => clearTimeout(retry.current.timer), [])

	useEffect(() => {
		refresh()
	}, [refresh])

	const login = useCallback(async (email, password) => {
		const res = await authApi.login(email, password)
		setActor({ kind: 'user', ...res.user })
		return res.user
	}, [])

	const logout = useCallback(async () => {
		try {
			if (actor?.kind === 'student') await studentApi.logout()
			else await authApi.logout()
		} finally {
			// Локальное состояние чистим в любом случае: если сервер не ответил,
			// оставлять человека «внутри» нельзя — он нажал «Выйти».
			setActor(null)
		}
	}, [actor])

	const value = {
		ready,
		offline,
		actor,
		refresh,
		login,
		logout,
		setActor,
		isTeacher: actor?.kind === 'user',
		isStudent: actor?.kind === 'student',
		can: (permission) => Boolean(actor?.permissions?.includes(permission)),
	}

	return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}

export function useAuth() {
	const ctx = useContext(AuthContext)
	if (!ctx) throw new Error('useAuth вызван вне AuthProvider')
	return ctx
}
