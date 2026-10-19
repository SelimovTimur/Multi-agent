// CabinetApp.jsx — маршруты приложения.
//
// Кабинет учителя живёт под /app, экраны ученика — под /student.

import { Navigate, Route, Routes, useLocation } from 'react-router-dom'
import { AuthProvider, useAuth } from './AuthContext'
import { ToastProvider } from './ui'

import Login from './pages/Login'
import Home from './pages/Home'
import Profile from './pages/Profile'
import ClassLessons from './pages/ClassLessons'
import ClassLessonLive from './pages/ClassLessonLive'
import ClassCreate from './pages/ClassCreate'
import ClassStudents from './pages/ClassStudents'
import ClassCards from './pages/ClassCards'
import ClassAnalytics from './pages/ClassAnalytics'
import LessonAnalytics from './pages/LessonAnalytics'
import StudentCard from './pages/StudentCard'
import Library from './pages/Library'
import LessonView from './pages/LessonView'
import StudentHome from './student/StudentHome'
import AvatarChat from './student/AvatarChat'
import StudentLesson from './student/StudentLesson'
import StudentResults from './student/StudentResults'
import { loginDestination } from './loginDestination'

/**
 * Gate — пускает дальше только нужного актора.
 *
 * Пока статус не известен, не показываем ничего: иначе вошедший учитель на
 * долю секунды видит форму входа, и это выглядит как разлогин.
 */
/** Пока вход не проверен: «проверяем» или, без связи, «повторяем сами». */
function Checking() {
	const { offline, refresh } = useAuth()
	if (!offline) return <div className='nr nr-loading'>Проверяем доступ…</div>
	return (
		<div className='nr nr-loading'>
			<div>
				<div>Нет связи с сервером. Пробуем снова…</div>
				<div className='mt12'>
					<button className='btn btn-outline' type='button' onClick={() => refresh()}>
						Повторить сейчас
					</button>
				</div>
			</div>
		</div>
	)
}

function Gate({ kind, children }) {
	const { ready, offline, actor } = useAuth()
	const location = useLocation()

	// Без связи не уводим на вход: например, ученик только что вошёл по коду,
	// а проверка не дошла — увидев форму входа, он вошёл бы второй раз.
	if (!ready || (offline && !actor)) return <Checking />

	if (!actor) {
		// Куда человек шёл, запоминаем: после входа вернём его туда же,
		// а не на главную.
		return <Navigate to='/login' state={{ from: location.pathname + location.search + location.hash }} replace />
	}
	if (kind && actor.kind !== kind) {
		return <Navigate to={actor.kind === 'student' ? '/student' : '/app'} replace />
	}
	return children
}

/** Вошедшему на экранах входа делать нечего — уводим в его кабинет. */
function GuestOnly({ children }) {
	const { ready, actor } = useAuth()
	const location = useLocation()
	if (!ready) return <Checking />
	if (actor) return <Navigate to={loginDestination(location.state?.from, actor.kind)} replace />
	return children
}

export default function CabinetApp() {
	return (
		<AuthProvider>
			<ToastProvider>
				<Routes>
					{/* Учитель входит по почте и паролю, ученик — по личному коду
					    на том же экране. */}
					<Route path='/login' element={<GuestOnly><Login /></GuestOnly>} />

					<Route path='/student' element={<Gate kind='student'><StudentHome /></Gate>} />
					<Route path='/student/lesson/:attemptId' element={<Gate kind='student'><StudentLesson /></Gate>} />
					<Route path='/student/results/:assignmentId/:view?' element={<Gate kind='student'><StudentResults /></Gate>} />
					<Route path='/student/attempt/:attemptId/chat' element={<Gate kind='student'><AvatarChat /></Gate>} />

					{/* Кабинет учителя */}
					<Route path='/app' element={<Gate kind='user'><Home /></Gate>} />
					<Route path='/app/profile' element={<Gate kind='user'><Profile /></Gate>} />
					<Route path='/app/class/new' element={<Gate kind='user'><ClassCreate /></Gate>} />
					<Route path='/app/class/:classId/students' element={<Gate kind='user'><ClassStudents /></Gate>} />
					<Route path='/app/class/:classId/cards' element={<Gate kind='user'><ClassCards /></Gate>} />
					<Route path='/app/class/:classId/student/:studentId' element={<Gate kind='user'><StudentCard /></Gate>} />

					{/* Библиотека готовых уроков: отсюда урок назначается классу. */}
					<Route path='/app/library' element={<Gate kind='user'><Library /></Gate>} />
					<Route path='/app/library/:lessonId' element={<Gate kind='user'><LessonView /></Gate>} />

					<Route path='/app/class/:classId/lessons' element={<Gate kind='user'><ClassLessons /></Gate>} />
					{/* Урок в классе: выдача заданий на уроке и на дом и таблица «как
					    проходит класс». */}
					<Route path='/app/class/:classId/lesson/:assignmentId' element={<Gate kind='user'><ClassLessonLive /></Gate>} />
					<Route path='/app/class/:classId/analytics' element={<Gate kind='user'><ClassAnalytics /></Gate>} />
					{/* Страница урока в аналитике: сводка и разговор с аватаром по классу. */}
					<Route
						path='/app/class/:classId/analytics/lesson/:assignmentId'
						element={<Gate kind='user'><LessonAnalytics /></Gate>}
					/>

					<Route path='*' element={<Navigate to='/login' replace />} />
				</Routes>
			</ToastProvider>
		</AuthProvider>
	)
}
