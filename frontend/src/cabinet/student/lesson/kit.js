// kit.js — правила экранов ученика без разметки: этапы урока, строка «Задание
// 2 из 4», звёзды словами, пропуск в предложении, перевод по фразам и разбор
// ошибок (docs/product/student-screens/story.md).
//
// Отдельным файлом без JSX: эти правила проверяет node-тест
// (scripts/test-student-lesson.mjs), а не глаза на уроке.

import { plural } from '../../plural.js'

/** Разделы «Моих занятий» — как они звучат в шапке урока. */
export const SECTION_LABEL = { class: 'На уроке', home: 'Домашняя работа' }

/** «3 задания» — число вместе со словом. */
export const tasksWord = (n) => plural(n, 'задание', 'задания', 'заданий')

/**
 * Этапы урока: иконка и подпись в шапке, у каждого свой фон шага.
 * Порядок — порядок урока; итог всегда последний.
 */
export const STAGES = [
	{ key: 'text', icon: '📖', label: 'Текст' },
	{ key: 'lexicon', icon: '💬', label: 'Выражения' },
	{ key: 'exercise', icon: '✏️', label: 'Упражнения' },
	{ key: 'grammar', icon: '🧩', label: 'Правило' },
	{ key: 'talk', icon: '🗣', label: 'Разговор' },
	{ key: 'finish', icon: '🏁', label: 'Итог' },
]

/** Этап задания по его виду. Цели урока — вводный экран этапа «Текст». */
export function stageOf(kind) {
	switch (kind) {
		case 'objectives':
		case 'text':
			return 'text'
		case 'lexicon':
			return 'lexicon'
		case 'grammar':
			return 'grammar'
		case 'dialog':
			return 'talk'
		default:
			return 'exercise'
	}
}

/**
 * stageBar — иконки этапов в шапке урока.
 *
 * Только этапы, задания которых есть в разделе, и итог. tasks — задания
 * раздела в порядке урока, at — ключ текущего задания или null на итоге,
 * done(task) — сделано ли задание. Пройденный этап — тот, где сделано всё;
 * отрезок после текущего этапа заполняется по шагам внутри него. По иконке
 * можно перейти к первому заданию этапа, а к итогу — когда сделано всё.
 */
export function stageBar(tasks, at, done) {
	const current = at ? stageOf(tasks.find((t) => t.key === at)?.kind) : 'finish'
	const present = STAGES.filter((s) => s.key === 'finish' || tasks.some((t) => stageOf(t.kind) === s.key))
	const ci = present.findIndex((s) => s.key === current)
	return present.map((s, i) => {
		const inStage = tasks.filter((t) => stageOf(t.kind) === s.key)
		let state = ''
		if (i === ci) state = 'cur'
		else if (inStage.length > 0 && inStage.every(done)) state = 'done'
		let fill = 0
		if (i < ci) fill = 100
		else if (i === ci && inStage.length > 0) {
			const pos = inStage.findIndex((t) => t.key === at)
			fill = Math.round(((pos + 1) / (inStage.length + 1)) * 100)
		}
		const reachable = s.key === 'finish' ? tasks.length > 0 && tasks.every(done) : inStage.length > 0
		return { ...s, state, fill, first: inStage[0]?.key || null, reachable }
	})
}

/**
 * Строка под названием урока: «На уроке · Задание 2 из 4», на вводном
 * экране — «… · цели урока», на итоге — «… · итог». Цели урока — не
 * задание и в счёт не идут.
 */
export function stepLine(section, tasks, at) {
	let tail
	if (!at) {
		tail = 'итог'
	} else if (tasks.find((t) => t.key === at)?.kind === 'objectives') {
		tail = 'цели урока'
	} else {
		const list = tasks.filter((t) => t.kind !== 'objectives')
		tail = `Задание ${list.findIndex((t) => t.key === at) + 1} из ${list.length}`
	}
	const label = SECTION_LABEL[section]
	return label ? `${label} · ${tail}` : tail[0].toUpperCase() + tail.slice(1)
}

/* ---------------------------------------------------------------------------
 * Упражнения
 * ------------------------------------------------------------------------- */

const EXERCISE_NAMES = {
	matching: 'Найди пары',
	gap_fill: 'Вставь пропущенное',
	logical: 'Продолжи фразу',
}

/** Название упражнения для ученика — одно на вид, как в макете. */
export function exerciseName(ex) {
	return EXERCISE_NAMES[ex?.type] || ex?.title || 'Упражнение'
}

/** Маркер пропуска в вопросе: генератор ставит «___». */
const SLOT = /_{2,}/

/** Буквы татарского алфавита, которых нет в русском. */
const TATAR_LETTERS = /[әөүҗңһ]/i

/**
 * dropHint — убрать русскую подсказку после « — » в конце предложения.
 *
 * Подсказкой считается только хвост из нескольких слов без татарских букв:
 * тире бывает и в самом татарском предложении («Камыр өчен — омырка»).
 */
function dropHint(text) {
	const i = text.lastIndexOf(' — ')
	if (i < 0) return text
	const tail = text.slice(i + 3)
	if (TATAR_LETTERS.test(tail) || !/[а-яё]/i.test(tail) || tail.trim().split(/\s+/).length < 2) return text
	return text.slice(0, i)
}

/**
 * gapParts — предложение с пропуском: что до слота и что после.
 * slot: false — маркера в вопросе нет (урок собран до маркеров): слот
 * встаёт после предложения.
 */
export function gapParts(prompt) {
	const text = String(prompt || '').trim()
	const m = SLOT.exec(text)
	if (!m) return { before: dropHint(text), after: '', slot: false }
	return { before: text.slice(0, m.index), after: dropHint(text.slice(m.index + m[0].length)), slot: true }
}

/** leadOf — начало фразы для «Продолжи фразу»: без маркера и многоточия. */
export function leadOf(prompt) {
	const text = String(prompt || '').trim()
	const m = SLOT.exec(text)
	const start = m ? text.slice(0, m.index) : text
	return start.replace(/(\s*(…|\.\.\.))+\s*$/, '').trim()
}

/* ---------------------------------------------------------------------------
 * Текст урока
 * ------------------------------------------------------------------------- */

/** Абзацы текста урока. */
export function paragraphs(text) {
	return String(text || '')
		.split(/\n+/)
		.map((p) => p.trim())
		.filter(Boolean)
}

/**
 * frankPairs — перевод по фразам: «фраза, под ней перевод».
 *
 * Генератор пишет перевод блоками «фраза\n— перевод» через пустую строку.
 * Блок без перевода значит, что текст правили руками и разметки нет, — тогда
 * null, и перевод показывается одним текстом.
 */
export function frankPairs(frank) {
	const blocks = String(frank || '')
		.split(/\n\s*\n/)
		.map((b) => b.trim())
		.filter(Boolean)
	if (!blocks.length) return null
	const out = []
	for (const block of blocks) {
		const lines = block
			.split('\n')
			.map((l) => l.trim())
			.filter(Boolean)
		const i = lines.findIndex((l) => /^[—–-]\s/.test(l))
		if (i <= 0) return null
		out.push({
			text: lines.slice(0, i).join(' '),
			translation: lines
				.slice(i)
				.join(' ')
				.replace(/^[—–-]\s*/, ''),
		})
	}
	return out
}

/* ---------------------------------------------------------------------------
 * Звёзды и выдачи
 * ------------------------------------------------------------------------- */

/** «29.09» из времени сервера. Число — из самой строки: она во времени школы. */
export function dayOf(iso) {
	const m = /^(\d{4})-(\d{2})-(\d{2})/.exec(iso || '')
	return m ? `${m[3]}.${m[2]}` : ''
}

/** Срок домашней работы: «до 03.10». Без срока бывают только старые назначения. */
export function dueText(iso) {
	const day = dayOf(iso)
	return day ? `до ${day}` : 'без срока'
}

/** «На уроке · 29.09» / «Домашняя работа · до 03.10». */
export function issueLine(issue) {
	if (issue.kind === 'home') return `${SECTION_LABEL.home} · ${dueText(issue.due_at)}`
	return `${SECTION_LABEL.class} · ${dayOf(issue.opened_at)}`
}

/**
 * sectionIssue — выдача, которую закрывает раздел урока: «на уроке» —
 * открытая сейчас (или последняя, если день уже кончился), «на дом» —
 * домашняя.
 */
export function sectionIssue(issues, section) {
	const list = (issues || []).filter((i) => i.kind === section)
	if (!list.length) return null
	if (section === 'class') return list.find((i) => i.open) || list[list.length - 1]
	return list[list.length - 1]
}

/**
 * starView — звезда словами: условие и подпись под ним.
 * pending — разбор разговора ещё считается, звезда может загореться.
 */
export function starView(star, issue) {
	if (star.kind === 'done') {
		const done = issue.tasks_done
		const total = issue.tasks_total
		return { title: 'Всё сделано', note: done >= total ? `${done} из ${total}` : `сделано ${done} из ${total}` }
	}
	if (star.kind === 'correct') return { title: 'От 80% верных', note: `у тебя ${issue.pct}%` }
	const w = issue.words || {}
	if (w.state === 'pending') return { title: 'Слова в разговоре', note: 'считаем…', pending: true }
	if (w.state !== 'ready') return { title: 'Слова в разговоре', note: 'разговора не было' }
	return {
		title: 'Слова в разговоре',
		note: star.on ? `${w.used} из ${w.total}` : `${w.used} из ${w.total}, нужно ${w.need}`,
	}
}

/** Сколько звёзд горит. */
export const litStars = (stars) => (stars || []).filter((s) => s.on).length

/** Разбор разговора ещё считается — итог стоит перечитать. */
export const wordsPending = (issue) => issue?.words?.state === 'pending'

/* ---------------------------------------------------------------------------
 * Ответы и ошибки
 * ------------------------------------------------------------------------- */

/**
 * restoreAnswers — прежние ответы попытки в виде экрана:
 * «упражнение:вопрос» → { chosen, correct, expected }.
 *
 * Урок, открытый со второго устройства, и пройденный урок в результатах
 * показывают ответы, которые сервер уже хранит: отвечать второй раз он не
 * даёт, и пустые варианты на экране были бы обманом.
 */
export function restoreAnswers(attempt) {
	const given = attempt?.answers
	if (!given || typeof given !== 'object') return {}
	const out = {}
	for (const [key, value] of Object.entries(given)) {
		out[key] = {
			chosen: value.answer ?? '',
			correct: Boolean(value.correct),
			expected: value.expected ?? '',
		}
	}
	return out
}

/**
 * mistakesOf — ошибки ученика словами урока.
 *
 * content — содержимое урока, answers — ответы попытки по ключу
 * «упражнение:вопрос» ({ chosen, correct, expected }), only — какие
 * упражнения брать (Set идентификаторов) или null — все. Порядок — по
 * упражнениям в порядке урока, внутри — по вопросам. Неотвеченное ошибкой не
 * считается.
 *
 * В парах отвечают словами: выбранный и верный перевод — сами тексты. В
 * тестах — идентификаторы вариантов, их тексты берутся из вопроса.
 */
export function mistakesOf(content, answers, only = null) {
	const out = []
	for (const ex of content?.exercises || []) {
		if (only && !only.has(ex.id)) continue
		const name = exerciseName(ex)
		if (ex.type === 'matching') {
			for (const it of ex.payload?.items || []) {
				const key = `${ex.id}:${it.phrase}`
				const a = answers?.[key]
				if (!a || a.correct || a.pending) continue
				out.push({
					key, exercise: ex.id, name, type: 'matching',
					prompt: it.phrase, expected: it.translation, chosen: a.chosen || '',
				})
			}
			continue
		}
		for (const q of ex.payload?.questions || []) {
			const key = `${ex.id}:${q.id}`
			const a = answers?.[key]
			if (!a || a.correct || a.pending) continue
			const text = (id) => (q.choices || []).find((c) => c.id === id)?.text
			out.push({
				key, exercise: ex.id, name, type: ex.type,
				prompt: q.prompt, expected: text(a.expected) ?? a.expected ?? '', chosen: text(a.chosen) ?? a.chosen ?? '',
			})
		}
	}
	return out
}

/** Число ошибок словами: «Мои ошибки · 8». */
export const mistakesWord = (n) => plural(n, 'ошибка', 'ошибки', 'ошибок')
