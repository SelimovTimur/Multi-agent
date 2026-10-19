// ClassLessons.jsx — вкладка «Уроки» класса.
//
// Учитель приходит сюда за тремя вещами: открыть урок в классе и выдать
// задания, посмотреть урок глазами ученика и снять его с класса. Назначенный
// урок закрыт, пока учитель не выдаст задания, поэтому главное в строке —
// сколько уже выдано и идёт ли сейчас работа на уроке.
//
// Правки урока и переноса срока в строке нет. Класс работает с той
// редакцией, которую получил при назначении, и правка до учеников не
// доходит — кнопка здесь обещала обратное. Урок правится в библиотеке, а
// срок бывает только у домашней работы и меняется на экране «Урок в классе».
//
// Шапка, код и вкладки живут в ClassHeader — одном компоненте на все три
// экрана класса, чтобы «Уроки», «Ученики» и «Аналитика» не разъехались снова.
//
// Снятие не удаляет ответы учеников. Если попытки уже были, сервер закрывает
// назначение вместо удаления и говорит об этом в ответе — интерфейс
// пересказывает результат словами, а не решает за сервер.

import { useCallback, useEffect, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { assignments as api, classes as classesApi } from '../../api/v1'
import ClassHeader from '../ClassHeader'
import { Shell } from '../Shell'
import Icon from '../icons'
import { Button, Modal, Table, useToast } from '../ui'
import { dayMonth, homeLabel } from './ClassLessonLive'
import '../class-lesson.css'

/**
 * Доступ ученика одной строкой: что задано на дом и до какого числа.
 *
 * Своих сроков у назначения больше нет — срок бывает только у домашней
 * работы, поэтому колонка говорит о ней: «на дом до 03.10». Работа на уроке
 * сюда не попадает: она видна меткой «идёт урок» в статусе.
 *
 * Снятый с класса урок закрыт целиком, какой бы срок ни стоял у домашней
 * работы: ученики его больше не видят. Дата — из `closed_at`, как и раньше.
 */
const access = (a) => {
	if (a.state === 'closed') {
		const closedAt = dayMonth(a.closed_at)
		return closedAt ? `закрыт ${closedAt}` : 'закрыт'
	}
	return a.home ? homeLabel(a.home) : '—'
}

/**
 * Есть ли что смотреть в предпросмотре.
 *
 * Предпросмотр открывается по уроку, а не по назначению, и адрес
 * собирается из `lesson_id`. Сервер оставляет его пустым, когда версия урока
 * не нашлась, — тогда идти некуда. Назначить можно только опубликованную
 * (готовую) версию, сервер это проверяет при назначении, поэтому непустой
 * `lesson_id` и означает «версия опубликована, смотреть есть что».
 */
const canOpenLesson = (a) => Boolean(a.lesson_id)

export default function ClassLessons() {
	const { classId } = useParams()
	const navigate = useNavigate()
	const toast = useToast()

	const [klass, setKlass] = useState(null)
	const [items, setItems] = useState(null)
	// Что открыто поверх списка: null | {kind:'unassign', item}
	const [dialog, setDialog] = useState(null)

	const load = useCallback(() => {
		api.ofClass(classId)
			.then((res) => setItems(res.assignments || res.items || []))
			.catch((err) => {
				setItems([])
				toast(err.message)
			})
	}, [classId, toast])

	useEffect(() => {
		// Ответ приходит конвертом {class: {...}} — как и у остальных экранов.
		// Без класса рисовать нечего: вкладка без шапки, кода и названия — это
		// таблица неизвестно чьих уроков, поэтому возвращаемся на главную.
		classesApi
			.get(classId)
			.then((res) => setKlass(res.class))
			.catch((err) => {
				toast(err.message)
				navigate('/app', { replace: true })
			})
	}, [classId, navigate, toast])

	useEffect(load, [load])

	if (!klass) {
		return (
			<Shell activeClassId={classId}>
				<div className='tiny'>Загружаем…</div>
			</Shell>
		)
	}

	const rows = (items || []).map((a) => {
		// Урок в классе открывается и названием, и главной кнопкой: учитель
		// идёт туда выдавать задания и смотреть, как класс их проходит.
		const live = `/app/class/${classId}/lesson/${a.id}`
		const status = <LessonStatus item={a} />
		const actions = (
			<>
				<IssueButton item={a} to={live} />
				<RowActions item={a} onUnassign={() => setDialog({ kind: 'unassign', item: a })} />
			</>
		)

		return {
			key: a.id,
			cells: [
				// Колонки — как в прототипе, поэтому отдельного столбца «редакция»
				// больше нет. Номер редакции остаётся строкой под названием:
				// после переиздания урока классу открыта та версия, что была
				// выдана, и учителю нужно видеть, какая именно.
				<div className='stack-s'>
					<Link className='t-name' to={live}>
						{a.title}
					</Link>
					<span className='tiny'>редакция {a.version}</span>
				</div>,
				<span className='badge badge-lvl'>{a.level}</span>,
				status,
				<span className='tiny'>{access(a)}</span>,
				<span className='t-actions'>{actions}</span>,
			],
			mobile: {
				title: <Link to={live}>{a.title}</Link>,
				badge: status,
				kv: [
					['Уровень', a.level],
					['Редакция', a.version],
					['Доступ', access(a)],
				],
				actions,
			},
		}
	})

	return (
		<Shell activeClassId={classId}>
			<ClassHeader klass={klass} active='lessons' />

			<div className='row mt22'>
				<Button variant='primary' icon='book' to={`/app/library?assignTo=${classId}`}>
					Назначить из библиотеки
				</Button>
			</div>

			<div className='mt16'>
				{items === null ? (
					<div className='tiny'>Загружаем…</div>
				) : (
					<Table
						head={['Урок', 'Уровень', 'Статус', 'Доступ', '']}
						rows={rows}
						empty='В классе пока нет уроков. Создайте урок или назначьте готовый из библиотеки.'
					/>
				)}
			</div>

			{dialog?.kind === 'unassign' && (
				<UnassignDialog
					item={dialog.item}
					classTitle={klass.name}
					onClose={() => setDialog(null)}
					onDone={(msg) => {
						setDialog(null)
						toast(msg)
						load()
					}}
				/>
			)}
		</Shell>
	)
}

/**
 * RowIcon — действие строки одной иконкой.
 *
 * Недоступное действие остаётся на месте серым, а не исчезает: пропавшая
 * иконка сдвигает соседние, и в следующей строке учитель жмёт не туда.
 *
 * Недоступная ссылка становится кнопкой: у `<Link>` нет отключения, и серая
 * на вид ссылка всё равно увела бы на страницу, которой для этого назначения
 * нет. Название обязательно: иконки подряд без подписи — ребус.
 */
function RowIcon({ icon, title, to, onClick, enabled }) {
	if (enabled && to) {
		return (
			<Link className='btn-icon' to={to} title={title} aria-label={title}>
				<Icon name={icon} size={16} />
			</Link>
		)
	}
	return (
		<button
			className='btn-icon'
			type='button'
			title={title}
			aria-label={title}
			onClick={onClick}
			disabled={!enabled}
		>
			<Icon name={icon} size={16} />
		</button>
	)
}

/**
 * LessonStatus — сколько заданий урока уже открыто классу и идёт ли урок.
 *
 * «выдано 0 из 8» серым — урок назначен, но ученики его ещё не видят: это
 * нормальное состояние, а не забытая выдача. Точка «идёт урок» мигает, пока
 * открыта работа на уроке, — по ней видно, в каком классе учитель сейчас.
 */
function LessonStatus({ item }) {
	const issued = item.tasks_issued ?? 0
	return (
		<span className='cl-status'>
			<span className={`badge ${issued > 0 ? 'badge-green' : 'badge-gray'}`}>
				выдано {issued} из {item.tasks_total ?? 0}
			</span>
			{item.class_open && (
				<span className='cl-live-tag'>
					<i className='cl-dot cl-pulse' aria-hidden='true' /> идёт урок
				</span>
			)}
			{item.state === 'closed' && <span className='badge badge-gray'>закрыт</span>}
		</span>
	)
}

/**
 * IssueButton — главная кнопка строки: «Выдать задания».
 *
 * Зелёная, пока у урока есть невыданные задания; когда выдано всё — обычная:
 * в класс заходят уже смотреть, а не выдавать. Так в макете.
 *
 * У снятого с класса урока выдавать нечего, и кнопка остаётся на месте
 * серой — как иконки рядом. Результаты по-прежнему открываются названием.
 */
function IssueButton({ item, to }) {
	if (item.state === 'closed') {
		return (
			<Button disabled title='Урок снят с класса — выдавать задания нельзя'>
				Выдать задания
			</Button>
		)
	}
	const left = (item.tasks_issued ?? 0) < (item.tasks_total ?? 0)
	return (
		<Button variant={left ? 'primary' : 'outline'} to={to}>
			Выдать задания
		</Button>
	)
}

/**
 * RowActions — действия назначения иконками: посмотреть и снять с класса.
 *
 * Один компонент на таблицу и на карточку телефона: разойдись они, на узком
 * экране незаметно потерялась бы половина действий.
 */
function RowActions({ item, onUnassign }) {
	const hasLesson = canOpenLesson(item)

	return (
		<span className='row-icons'>
			<RowIcon
				icon='eye'
				title={hasLesson ? 'Посмотреть урок' : 'Редакция урока не найдена — смотреть нечего'}
				to={`/app/library/${item.lesson_id}`}
				enabled={hasLesson}
			/>
			{/* У выключенной кнопки подпись объясняет, почему она выключена:
			    иначе учитель наводит курсор и читает то же название действия,
			    которое не работает. */}
			<RowIcon
				icon='x'
				title={
					item.state === 'closed'
						? 'Урок уже закрыт — снимать с класса нечего'
						: 'Снять с класса'
				}
				onClick={onUnassign}
				enabled={item.state !== 'closed'}
			/>
		</span>
	)
}

/** UnassignDialog — снять урок с класса. */
function UnassignDialog({ item, classTitle, onClose, onDone }) {
	const toast = useToast()
	const [busy, setBusy] = useState(false)

	async function confirm() {
		setBusy(true)
		try {
			const res = await api.unassign(item.id)
			// Сервер сам решает, удалить назначение или закрыть: если попытки
			// уже были, удалить нельзя — ответы учеников остаются. Что именно
			// произошло, говорим словами, а не догадками.
			onDone(
				res?.result === 'closed'
					? 'Урок снят: попытки уже были, поэтому назначение закрыто, а ответы сохранены'
					: 'Урок снят с класса — он остался в вашей библиотеке',
			)
		} catch (err) {
			toast(err.message)
			setBusy(false)
		}
	}

	return (
		<Modal
			title={`Снять урок «${item.title}»${classTitle ? ` с класса ${classTitle}` : ''}?`}
			onClose={onClose}
			footer={
				<>
					<Button onClick={onClose}>Отмена</Button>
					<Button variant='primary' onClick={confirm} disabled={busy}>
						Снять с класса
					</Button>
				</>
			}
		>
			<div className='row'>
				<Icon name='warn' size={16} />
				<p className='small'>
					Урок пропадёт из активного списка класса, но останется в вашей библиотеке — вы
					сможете назначить его снова в любой момент. Уже полученные ответы учеников
					сохранятся.
				</p>
			</div>
		</Modal>
	)
}
