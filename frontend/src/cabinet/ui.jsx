// ui.jsx — общие элементы кабинета.
//
// Компоненты выпускают ровно те же классы, что в макете (cabinet.css).
// Правило простое: разметка описывает структуру, вид живёт в CSS. Если
// понадобился inline-стиль — скорее всего, в CSS не хватает класса.

import { createContext, useCallback, useContext, useEffect, useRef, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import Icon from './icons'

/* ---------------------------------------------------------------------------
 * Кнопки и поля
 * ------------------------------------------------------------------------- */

// Размер у кнопок один на весь кабинет, поэтому размера здесь и нет. Раньше
// был проп size='lg': главное действие экрана становилось выше и крупнее
// соседнего «Назад», и пара кнопок в одной строке стояла лесенкой. Выделяет
// главное действие цвет (variant='primary'), а не рост.
export function Button({
	children,
	variant = 'outline',
	icon,
	block,
	to,
	type = 'button',
	className = '',
	...rest
}) {
	const cls = ['btn', `btn-${variant}`, block && 'btn-block', className].filter(Boolean).join(' ')

	// Ссылка остаётся ссылкой: её можно открыть в новой вкладке и скопировать
	// адрес. Кнопка, притворяющаяся ссылкой, это ломает.
	if (to) {
		return (
			<Link className={cls} to={to} {...rest}>
				{icon && <Icon name={icon} size={16} />}
				{children}
			</Link>
		)
	}
	return (
		<button className={cls} type={type} {...rest}>
			{icon && <Icon name={icon} size={16} />}
			{children}
		</button>
	)
}

// Татарские буквы, которых нет на русской раскладке. Порядок — из прототипа.
// Буквы, которых нет на русской клавиатуре, — по языку урока.
//
// Зашитый татарский набор годился, пока язык был один: учителю башкирского
// панель предлагала чужие буквы, а своих ҙ, ҫ, ғ, ҡ он не набирал вовсе.
// Языки — те же семь, что у класса и у llm_service.
const LETTERS = {
	tatar: ['ә', 'ө', 'ү', 'җ', 'ң', 'һ'],
	bashkir: ['ә', 'ө', 'ү', 'һ', 'ҙ', 'ҫ', 'ң', 'ғ', 'ҡ'],
	chuvash: ['ӑ', 'ӗ', 'ҫ', 'ӳ'],
	mari: ['ӓ', 'ӧ', 'ӱ', 'ҥ', 'ӹ'],
	udmurt: ['ӝ', 'ӟ', 'ӥ', 'ӧ', 'ӵ'],
	yakut: ['ҕ', 'ҥ', 'ө', 'ү', 'һ'],
	ossetian: ['ӕ'],
}

/**
 * LetterBar — панель букв языка урока над полем ввода.
 *
 * Без неё ә, ө, ү, җ, ң и һ с обычной клавиатуры не набрать, а править
 * распознанный текст учителю приходится постоянно. В прототипе эта панель
 * стоит у каждого поля с татарским текстом; у нас её не было нигде.
 *
 * Набор букв — от языка урока, а не зашит под татарский: класс может быть
 * башкирский или чувашский, и чужие буквы там не нужны, а своих не хватает.
 *
 * Буква встаёт в позицию курсора, а не в конец: правят середину слова чаще,
 * чем дописывают хвост. Поле управляемое, поэтому значение пересчитывается и
 * уходит в onChange, а каретка возвращается на место уже после перерисовки —
 * иначе она прыгала бы в конец текста на каждой букве.
 */
export function LetterBar({ targetRef, value, onChange, lang = 'tatar' }) {
	// Незнакомый язык — панели нет. Пустая полоса с подписью «буквы» хуже,
	// чем её отсутствие: она обещает помощь, которой нет.
	const letters = LETTERS[String(lang || '').toLowerCase()]
	function insert(letter) {
		const el = targetRef.current
		const text = value || ''
		const start = el && el.selectionStart != null ? el.selectionStart : text.length
		const end = el && el.selectionEnd != null ? el.selectionEnd : start

		onChange(text.slice(0, start) + letter + text.slice(end))

		const at = start + letter.length
		requestAnimationFrame(() => {
			const node = targetRef.current
			if (!node) return
			node.focus()
			node.setSelectionRange(at, at)
		})
	}

	if (!letters) return null

	return (
		<div className='row' style={{ gap: 6 }}>
			{/* Язык называем словом: «Татарские буквы», «Башкирские буквы». */}
			<span className='tiny'>Буквы: {langLabel(lang)}</span>
			<div className='chips'>
				{letters.map((letter) => (
					<button
						className='chip'
						type='button'
						key={letter}
						title={`Вставить ${letter}`}
						onClick={() => insert(letter)}
					>
						{letter}
					</button>
				))}
			</div>
		</div>
	)
}

export function Field({ label, hint, error, htmlFor, children }) {
	return (
		<div className='field'>
			{label && <label htmlFor={htmlFor}>{label}</label>}
			{children}
			{hint && !error && <div className='hint'>{hint}</div>}
			{error && <div className='err'>{error}</div>}
		</div>
	)
}

export function Input({ className = '', ...rest }) {
	return <input className={`input ${className}`.trim()} {...rest} />
}

/**
 * GrowInput — поле на одну строку, которое растёт вместе с текстом.
 *
 * Выражения, переводы и примеры бывают длиннее ширины поля, а однострочный
 * input обрезает их по правому краю: прочитать целиком нельзя ни прокруткой,
 * ни как-то иначе. Проверка урока перед выдачей классу — основная работа
 * учителя на этих экранах, и вслепую её не сделать.
 *
 * Внутри textarea, а не input: перенос строки даёт браузер, нам остаётся
 * высота. Enter гасим — это одна строка списка, а не абзац; вставленный
 * многострочный текст сворачивается в пробелы по той же причине.
 */
export function GrowInput({ value, onChange, className = '', ...rest }) {
	const ref = useRef(null)

	// Высота пересчитывается на каждое значение, в том числе на пришедшее
	// извне: список правят и кнопками «добавить», а не только набором.
	const fit = useCallback(() => {
		const el = ref.current
		if (!el) return
		el.style.height = 'auto'
		el.style.height = `${el.scrollHeight}px`
	}, [])

	useEffect(fit, [fit, value])

	return (
		<textarea
			ref={ref}
			className={`input input-grow ${className}`.trim()}
			rows={1}
			value={value}
			onChange={(e) => {
				// Перевод строки внутрь значения не пускаем: в списке пар он
				// не значит ничего, а в выгрузке ломает строку. Правим само
				// поле, а не копию события: обработчики читают e.target.value.
				const text = e.target.value.replace(/[\r\n]+/g, ' ')
				if (text !== e.target.value) e.target.value = text
				onChange(e)
			}}
			onKeyDown={(e) => {
				if (e.key === 'Enter') e.preventDefault()
			}}
			{...rest}
		/>
	)
}

export function Select({ options, placeholder, className = '', ...rest }) {
	// Класс select, а не input: у него своя стрелка и appearance:none.
	// Раньше стоял input, и списки выглядели системными — по-разному в
	// каждом браузере и мимо остального интерфейса.
	return (
		<select className={`input select ${className}`.trim()} {...rest}>
			{/* Пустой пункт нужен там, где выбирать должен человек: без него
			    список показывает первое значение как выбранное, и учитель
			    жмёт «Назначить», ничего не выбрав. */}
			{placeholder && <option value=''>{placeholder}</option>}
			{options.map((o) => {
				const value = typeof o === 'string' ? o : o.value
				const label = typeof o === 'string' ? o : o.label
				return (
					<option key={value} value={value}>
						{label}
					</option>
				)
			})}
		</select>
	)
}

/* ---------------------------------------------------------------------------
 * Языки
 * ------------------------------------------------------------------------- */

/**
 * Языки, на которых сервис собирает уроки.
 *
 * Список один на весь кабинет — и для фильтров, и для подписей. Пока каждый
 * экран выводил поле языка как ему удобно, учитель видел «Татарский» и
 * «TATAR» в одной карточке и искал между ними разницу, которой нет: на пяти
 * экранах язык был написан тремя способами. С появлением второго языка эта
 * пара размножилась бы на каждый экран.
 *
 * Правило: человеку — название, адресам и выгрузкам — код.
 */
export const LANGS = [{ value: 'english', label: 'Английский' }]

const LANG_LABEL = Object.fromEntries(LANGS.map((l) => [l.value, l.label]))

/**
 * langLabel — название языка для человека.
 *
 * Незнакомый код отдаём как есть: урок на языке, которого ещё нет в списке,
 * должен открываться, а не показывать пустоту вместо подписи.
 */
export function langLabel(code) {
	if (!code) return ''
	return LANG_LABEL[String(code).toLowerCase()] || code
}

/* ---------------------------------------------------------------------------
 * Срок доступа
 * ------------------------------------------------------------------------- */

/** Дата через N дней в заданное время. Возвращает ISO с зоной. */
function inDays(days, hour = 18, minute = 0) {
	const d = new Date()
	d.setDate(d.getDate() + days)
	d.setHours(hour, minute, 0, 0)
	return d.toISOString()
}

/** ISO → «24 августа, 18:00». Пусто — пустая строка. */
export function humanWhen(iso) {
	if (!iso) return ''
	const d = new Date(iso)
	if (Number.isNaN(d.getTime())) return ''
	return d.toLocaleString('ru-RU', {
		day: 'numeric',
		month: 'long',
		hour: '2-digit',
		minute: '2-digit',
	})
}

/** ISO → YYYY-MM-DD в местной зоне (для input type=date). */
function toDatePart(iso) {
	if (!iso) return ''
	const d = new Date(iso)
	if (Number.isNaN(d.getTime())) return ''
	const pad = (n) => String(n).padStart(2, '0')
	return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
}

/** ISO → HH:MM в местной зоне. */
function toTimePart(iso, fallback) {
	if (!iso) return fallback
	const d = new Date(iso)
	if (Number.isNaN(d.getTime())) return fallback
	const pad = (n) => String(n).padStart(2, '0')
	return `${pad(d.getHours())}:${pad(d.getMinutes())}`
}

/** Дата и время в местной зоне → ISO. */
function fromParts(date, time) {
	if (!date) return null
	const [h, m] = (time || '00:00').split(':').map(Number)
	const d = new Date(`${date}T00:00:00`)
	if (Number.isNaN(d.getTime())) return null
	d.setHours(h || 0, m || 0, 0, 0)
	return d.toISOString()
}

// Время урока — получасовой шаг с семи утра до десяти вечера. Список, а не
// поле ввода: набирать «18:00» руками ради типового срока незачем.
const TIME_OPTIONS = (() => {
	const out = []
	for (let h = 7; h <= 22; h += 1) {
		for (const m of [0, 30]) {
			const label = `${String(h).padStart(2, '0')}:${String(m).padStart(2, '0')}`
			out.push({ value: label, label })
		}
	}
	return out
})()

/**
 * DateTimeField — срок доступа кнопками, а не набором даты руками.
 *
 * Типовой случай — «на неделю», «до конца месяца», «без ограничения», и он
 * закрывается одним нажатием. Точная дата остаётся доступной, но не
 * навязывается: набирать «дд.мм.гггг, --:--» ради обычного домашнего
 * задания — работа на пустом месте.
 *
 * value и onChange работают с ISO-строкой (или '' — «не задано»).
 */
export function DateTimeField({ label, hint, value, onChange, presets, defaultTime = '18:00' }) {
	const list = presets || [
		{ key: 'none', label: 'Без срока', iso: () => '' },
		{ key: 'week', label: 'Через неделю', iso: () => inDays(7) },
		{ key: 'two-weeks', label: 'Через 2 недели', iso: () => inDays(14) },
		{ key: 'month', label: 'Через месяц', iso: () => inDays(30) },
	]

	const [custom, setCustom] = useState(false)
	const [date, setDate] = useState(toDatePart(value))
	const [time, setTime] = useState(toTimePart(value, defaultTime))

	// Какой пресет выбран сейчас. Для «не задано» это первый в списке.
	const activeKey = custom ? 'custom' : value ? '' : list[0].key

	function pick(preset) {
		setCustom(false)
		onChange(preset.iso())
	}

	function openCustom() {
		setCustom(true)
		// Дата по умолчанию — завтра: пустой календарь заставляет искать
		// сегодняшнее число глазами.
		const start = value || inDays(1, Number(defaultTime.slice(0, 2)), Number(defaultTime.slice(3)))
		setDate(toDatePart(start))
		setTime(toTimePart(start, defaultTime))
		onChange(start)
	}

	function changeCustom(nextDate, nextTime) {
		setDate(nextDate)
		setTime(nextTime)
		onChange(fromParts(nextDate, nextTime) || '')
	}

	return (
		<div className='field'>
			{label && <label>{label}</label>}

			<div className='chips'>
				{list.map((p) => (
					<button
						className='chip'
						key={p.key}
						type='button'
						aria-pressed={activeKey === p.key}
						onClick={() => pick(p)}
					>
						{p.label}
					</button>
				))}
				<button
					className='chip'
					type='button'
					aria-pressed={custom}
					onClick={openCustom}
				>
					Другой срок
				</button>
			</div>

			{custom && (
				<div className='row mt8'>
					<input
						className='input'
						type='date'
						value={date}
						onChange={(e) => changeCustom(e.target.value, time)}
						style={{ maxWidth: 190 }}
					/>
					<Select
						options={TIME_OPTIONS}
						value={time}
						onChange={(e) => changeCustom(date, e.target.value)}
						style={{ maxWidth: 120 }}
					/>
				</div>
			)}

			<div className='hint'>{value ? humanWhen(value) : hint || 'Без ограничения'}</div>
		</div>
	)
}

export function Toggle({ on, label, onClick, disabled }) {
	return (
		<button
			className='toggle-switch'
			onClick={onClick}
			disabled={disabled}
			type='button'
			aria-pressed={on}
		>
			<span className='toggle-track'>
				<span className='toggle-thumb' />
			</span>
			<span className='toggle-label'>{label}</span>
		</button>
	)
}

/* ---------------------------------------------------------------------------
 * Таблица: на десктопе строки, на телефоне карточки
 * ------------------------------------------------------------------------- */

/**
 * rows: [{ key, cells: [], onClick?, mobile: { title, badge, kv: [[k,v]], actions } }]
 *
 * Две разметки вместо одной адаптивной: таблица из пяти колонок на телефоне
 * либо режется по горизонтали, либо превращается в нечитаемую кашу. Что
 * показывать, решает CSS, а не JS — без замера ширины и перерисовок.
 */
export function Table({ head, rows, empty }) {
	if (!rows.length) return <div className='empty'>{empty || 'Пока пусто'}</div>

	return (
		<>
			<div className='table-wrap'>
				<table>
					<thead>
						<tr>
							{head.map((h, i) => (
								<th key={i}>{h}</th>
							))}
						</tr>
					</thead>
					<tbody>
						{rows.map((r) => (
							<tr key={r.key} className={r.onClick ? 'clickable' : ''} onClick={r.onClick}>
								{r.cells.map((c, i) => (
									<td key={i}>{c}</td>
								))}
							</tr>
						))}
					</tbody>
				</table>
			</div>
			<div className='mobile-cards'>
				{rows.map((r) => {
					const m = r.mobile || {}
					return (
						<div className='mcard' key={r.key}>
							<div className='mcard-hd'>
								<div className='mcard-n'>{m.title || r.cells[0]}</div>
								{m.badge}
							</div>
							{m.kv?.length > 0 && (
								<div className='mcard-kv'>
									{m.kv.map(([k, v]) => (
										<span key={k}>
											{k}: <b>{v}</b>
										</span>
									))}
								</div>
							)}
							{m.actions && <div className='mcard-ft'>{m.actions}</div>}
						</div>
					)
				})}
			</div>
		</>
	)
}

/* ---------------------------------------------------------------------------
 * Меню строки (три точки)
 * ------------------------------------------------------------------------- */

export function Kebab({ items }) {
	const [open, setOpen] = useState(false)
	const ref = useRef(null)

	// Клик мимо и Escape закрывают меню. Без этого оно остаётся висеть
	// поверх экрана, и пользователь думает, что интерфейс подвис.
	useEffect(() => {
		if (!open) return
		const onDown = (e) => {
			if (!ref.current?.contains(e.target)) setOpen(false)
		}
		const onKey = (e) => e.key === 'Escape' && setOpen(false)
		document.addEventListener('mousedown', onDown)
		document.addEventListener('keydown', onKey)
		return () => {
			document.removeEventListener('mousedown', onDown)
			document.removeEventListener('keydown', onKey)
		}
	}, [open])

	return (
		<span className='kebab-wrap' ref={ref}>
			<button
				className='btn-icon'
				type='button'
				aria-label='Действия'
				aria-expanded={open}
				onClick={(e) => {
					e.stopPropagation()
					setOpen((v) => !v)
				}}
			>
				<Icon name='kebab' size={16} />
			</button>
			{open && (
				<span className='kebab-menu'>
					{items.map((it) => (
						<button
							key={it.label}
							type='button'
							className={it.danger ? 'danger' : ''}
							onClick={(e) => {
								e.stopPropagation()
								setOpen(false)
								it.run()
							}}
						>
							{it.label}
						</button>
					))}
				</span>
			)}
		</span>
	)
}

/* ---------------------------------------------------------------------------
 * Модальное окно
 * ------------------------------------------------------------------------- */

// Что в окне можно взять фокусом. Скрытые и отключённые элементы отброшены:
// Tab до них не доходит, и ловушка не должна на них останавливаться.
const FOCUSABLE =
	'a[href],button:not([disabled]),input:not([disabled]),select:not([disabled]),textarea:not([disabled]),[tabindex]:not([tabindex="-1"])'

export function Modal({ title, sub, children, footer, wide, className = '', onClose }) {
	const box = useRef(null)
	// Сохраняем источник фокуса до того, как дочернее поле выполнит autoFocus.
	const opener = useRef(typeof document === 'undefined' ? null : document.activeElement)
	const close = useRef(onClose)
	const initialFocus = useRef(null)

	// Обработчик может меняться при каждом вводе. Его обновление не должно
	// заново запускать установку фокуса и ловушку Tab.
	useEffect(() => {
		close.current = onClose
	}, [onClose])

	/**
	 * Фокус не выходит из окна и возвращается туда, откуда его открыли.
	 *
	 * Без этого Tab через пять нажатий уходил на страницу под затемнением —
	 * на элементы, которых не видно, — а Esc оставлял фокус в шапке. Для
	 * работы с клавиатуры и экранного чтеца это значит потерять место.
	 */
	useEffect(() => {
		// autoFocus поля уже сработал: сохраняем его вместо перехода к крестику.
		// Цель запоминаем и для повторной установки эффекта в StrictMode.
		if (box.current?.contains(document.activeElement)) {
			initialFocus.current = document.activeElement
		} else {
			const first = initialFocus.current || [...(box.current?.querySelectorAll(FOCUSABLE) || [])].find(
				(el) => el.offsetParent !== null,
			)
			if (first) first.focus()
			else box.current?.focus()
		}

		const onKey = (e) => {
			if (e.key === 'Escape') {
				close.current?.()
				return
			}
			if (e.key !== 'Tab' || !box.current) return
			const items = [...box.current.querySelectorAll(FOCUSABLE)].filter(
				(el) => el.offsetParent !== null || el === document.activeElement,
			)
			if (items.length === 0) {
				e.preventDefault()
				return
			}
			const edge = e.shiftKey ? items[0] : items[items.length - 1]
			// По кругу: с последнего элемента на первый и обратно. Проверяем и
			// «фокус уже вне окна» — окно могло открыться поверх страницы,
			// которая успела забрать фокус себе.
			if (document.activeElement === edge || !box.current.contains(document.activeElement)) {
				e.preventDefault()
				;(e.shiftKey ? items[items.length - 1] : items[0]).focus()
			}
		}

		document.addEventListener('keydown', onKey)
		return () => {
			document.removeEventListener('keydown', onKey)
			// Возврат фокуса — только если он остался в закрывшемся окне или
			// потерялся на body. Закрытие бывает вместе с переходом на другой
			// экран, и там фокус уже стоит там, где нужно новому экрану.
			const now = document.activeElement
			const lost = !now || now === document.body || box.current?.contains(now)
			if (lost && opener.current?.isConnected && typeof opener.current.focus === 'function') opener.current.focus()
		}
	}, [])

	return (
		<div className='modal-scrim' onClick={onClose}>
			<div
				className={`modal ${wide ? 'modal-wide' : ''} ${className}`}
				role='dialog'
				aria-modal='true'
				aria-label={title}
				ref={box}
				tabIndex={-1}
				onClick={(e) => e.stopPropagation()}
			>
				<div className='modal-hd'>
					<div>
						<div className='h2'>{title}</div>
						{sub && <div className='lead mt8'>{sub}</div>}
					</div>
					<button className='modal-x' type='button' aria-label='Закрыть' onClick={onClose}>
						<Icon name='x' size={20} />
					</button>
				</div>
				<div className='modal-bd'>{children}</div>
				{footer && <div className='modal-ft'>{footer}</div>}
			</div>
		</div>
	)
}

/* ---------------------------------------------------------------------------
 * Тосты
 * ------------------------------------------------------------------------- */

const ToastContext = createContext(() => {})

const TOAST_MS = 3200

/**
 * ToastProvider — очередь сообщений без дублей.
 *
 * Одинаковые сообщения не размножаются: повтор продлевает показ уже висящей
 * плашки и увеличивает счётчик. Раньше два «Урок не найден» выводились один
 * поверх другого со сдвигом, перекрывая и друг друга, и строку под собой —
 * а повторить действие, которое не работает, человек пробует не один раз.
 *
 * Ключ очереди — сам текст: сообщения различаются только им, и отдельный
 * идентификатор пришлось бы придумывать вне обновления состояния.
 */
export function ToastProvider({ children }) {
	const [items, setItems] = useState([])
	const timers = useRef(new Map())

	const drop = useCallback((text) => {
		const t = timers.current.get(text)
		if (t) clearTimeout(t)
		timers.current.delete(text)
		setItems((prev) => prev.filter((x) => x.message !== text))
	}, [])

	const toast = useCallback(
		(message) => {
			const text = String(message ?? '').trim()
			if (!text) return
			setItems((prev) =>
				prev.some((t) => t.message === text)
					? prev.map((t) => (t.message === text ? { ...t, count: t.count + 1 } : t))
					: [...prev, { message: text, count: 1 }],
			)
			// Таймер перезаводим на каждый повтор: сообщение должно висеть
			// столько же после последнего раза, сколько после первого.
			const prev = timers.current.get(text)
			if (prev) clearTimeout(prev)
			timers.current.set(
				text,
				setTimeout(() => drop(text), TOAST_MS),
			)
		},
		[drop],
	)

	// Провайдер живёт всю сессию, но на горячей перезагрузке и в тестах его
	// размонтируют — висящие таймеры после этого дёргали бы setState.
	useEffect(() => {
		const map = timers.current
		return () => {
			map.forEach((t) => clearTimeout(t))
			map.clear()
		}
	}, [])

	return (
		<ToastContext.Provider value={toast}>
			{children}
			<div className='toasts'>
				{items.map((t) => (
					<div className='toast' key={t.message} role='status'>
						{t.message}
						{t.count > 1 && <span className='toast-n'>×{t.count}</span>}
					</div>
				))}
			</div>
		</ToastContext.Provider>
	)
}

export const useToast = () => useContext(ToastContext)

/* ---------------------------------------------------------------------------
 * Список классов изменился
 * ------------------------------------------------------------------------- */

/**
 * Сайдбар со списком классов и счётчиками живёт в Shell, а состав класса
 * меняют экраны внутри него. Пока связи не было, учитель исключал ученика,
 * таблица показывала двоих, а счётчик в меню — троих, и правильное число
 * появлялось только после перезагрузки: человек решал, что исключение не
 * сработало, и повторял действие.
 *
 * Шина, а не общее состояние: Shell стоит внутри каждого экрана, и поднять
 * список классов выше пришлось бы через ещё один провайдер вокруг всех
 * маршрутов — ради одного числа это дорого.
 */
const classWatchers = new Set()

/** Сказать сайдбару, что класс изменился. */
export function classesChanged() {
	classWatchers.forEach((fn) => fn())
}

/** Подписка на изменения состава классов. */
export function useClassesChanged(handler) {
	useEffect(() => {
		classWatchers.add(handler)
		return () => classWatchers.delete(handler)
	}, [handler])
}

/* ---------------------------------------------------------------------------
 * Вспомогательное
 * ------------------------------------------------------------------------- */

/**
 * Короткая дата или прочерк.
 *
 * Сервер может не прислать её вовсе — у объектов из старого контура поля нет.
 * `new Date(undefined)` даёт «Invalid Date», и эта надпись в таблице выглядит
 * поломкой, хотя урок цел. Общим хелпером, а не копией в каждом экране:
 * защиту уже пришлось писать дважды.
 */
export function shortDate(iso) {
	if (!iso) return '—'
	const d = new Date(iso)
	return Number.isNaN(d.getTime()) ? '—' : d.toLocaleDateString('ru-RU')
}

/**
 * Человеческая давность последнего входа: «сегодня» читается быстрее, чем «06.09».
 *
 * Общим хелпером, а не копией в экране: та же функция стояла дословно и во
 * вкладке «Ученики», и в «Аналитике». Речь в обеих об одном и том же последнем
 * входе, и расходиться формулировки не должны — а с двумя копиями разойдутся.
 */
export function lastSeen(iso) {
	if (!iso) return 'ещё не заходил'
	const d = new Date(iso)
	if (Number.isNaN(d.getTime())) return 'ещё не заходил'

	const days = Math.floor((Date.now() - d.getTime()) / 86400000)
	if (days <= 0) return 'сегодня'
	if (days === 1) return 'вчера'
	if (days < 7) return `${days} дн. назад`
	if (days < 30) return `${Math.floor(days / 7)} нед. назад`
	return d.toLocaleDateString('ru-RU')
}

export { plural, pluralWord } from './plural.js'

/**
 * Копирование в буфер с честным результатом.
 *
 * Два пути, и оба нужны. `navigator.clipboard` есть только в защищённом
 * контексте — по HTTP его нет вовсе, и пилотный сервер работает как раз по
 * HTTP: все кнопки «Копировать» там молча ничего не делали. Поэтому
 * запасной путь через скрытое поле и `execCommand`: он устарел, но
 * работает везде.
 *
 * Возвращаем результат, а не рисуем «скопировано» вслепую: человек,
 * которому соврали, вставит пустоту и не поймёт почему.
 */
export async function copyToClipboard(text) {
	if (navigator.clipboard?.writeText && window.isSecureContext) {
		try {
			await navigator.clipboard.writeText(text)
			return true
		} catch {
			// Разрешение не дали или контекст всё же не тот — пробуем ниже.
		}
	}

	try {
		const area = document.createElement('textarea')
		area.value = text
		// Поле должно быть в документе и доступно выделению: у display:none
		// и visibility:hidden ничего не выделяется, и копирование молчит.
		area.setAttribute('readonly', '')
		area.style.position = 'fixed'
		area.style.top = '-1000px'
		area.style.opacity = '0'
		document.body.appendChild(area)
		area.select()
		area.setSelectionRange(0, text.length)
		const ok = document.execCommand('copy')
		document.body.removeChild(area)
		return ok
	} catch {
		return false
	}
}

/** Хук навигации — чтобы страницы не импортировали router напрямую. */
export const useGo = () => useNavigate()
