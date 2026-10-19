// ClassHeader.jsx — шапка класса и вкладки.
//
// В макете все три экрана класса — «Уроки», «Ученики», «Аналитика» —
// открываются под одной шапкой: название и предмет с уровнем. Раньше эта
// шапка была только у «Учеников», а «Уроки» выглядели отдельной таблицей
// без вкладок — учитель терял класс.
//
// Кода класса и переключателя приёма здесь больше нет: самостоятельного
// входа в класс нет, учеников заводят списком, и каждый входит по своему
// личному коду. Коды и карточки — на вкладке «Ученики».
//
// Один компонент на три экрана, чтобы они не разъезжались снова.

import { Link } from 'react-router-dom'

const TABS = [
	['lessons', 'Уроки'],
	['students', 'Ученики'],
	['analytics', 'Аналитика'],
]

/**
 * ClassHeader — шапка и вкладки класса.
 *
 * `klass` — объект класса (из конверта `{class: …}`), `active` — ключ
 * открытой вкладки.
 */
export default function ClassHeader({ klass, active }) {
	return (
		<>
			<div>
				<h1 className='h1'>{klass.name}</h1>
				{/* Пустой предмет отфильтровываем, чтобы строка не начиналась с запятой. */}
				<div className='lead mt8'>
					{[klass.subject, klass.level && `уровень ${klass.level}`].filter(Boolean).join(', ')}
				</div>
			</div>

			<div className='tabs'>
				{TABS.map(([key, label]) => (
					<Link
						className='tab'
						key={key}
						aria-selected={active === key}
						to={`/app/class/${klass.id}/${key}`}
					>
						{label}
					</Link>
				))}
			</div>
		</>
	)
}
