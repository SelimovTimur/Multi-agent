// report.js — подписи и группировки отчёта по разговору с аватаром (У-2…У-4).
//
// Данные — ручки учителя из docs/product/avatar-goals/contract.md (раздел 6
// «Учитель»), экраны — docs/product/avatar-goals/teacher-ui.md. Модуль без
// React: экраны отчёта только рисуют то, что посчитано здесь, а проверяется
// он обычным node — scripts/test-conversation-report.mjs.

/* ---------------------------------------------------------------------------
 * Подписи
 * ------------------------------------------------------------------------- */

/**
 * plural(3, 'ошибка', 'ошибки', 'ошибок') → '3 ошибки'.
 *
 * То же правило, что у plural из ui.jsx. Копия, а не импорт: ui.jsx — модуль
 * с JSX, и node, на котором идёт проверка подписей, его не загрузит.
 */
export function plural(n, one, few, many) {
	const abs = Math.abs(n) % 100
	const last = abs % 10
	let form = many
	if (abs < 11 || abs > 14) {
		if (last === 1) form = one
		else if (last >= 2 && last <= 4) form = few
	}
	return `${n} ${form}`
}

// Статус ученика — словами из истории У-2. Цвет повторяет таблицу заданий:
// серый — не начинал, жёлтый — в работе, зелёный — сделано.
const STATUS_VIEW = {
	not_started: { label: 'не приступал', cls: 'badge-gray' },
	in_progress: { label: 'разговор не завершён', cls: 'badge-warn' },
	completed: { label: 'завершил', cls: 'badge-green' },
}

/** Статус ученика: { label, cls }. Незнакомый статус — «не приступал». */
export function statusView(status) {
	return STATUS_VIEW[status] || STATUS_VIEW.not_started
}

// Подписи счётчиков над отчётом — во множественном числе: это про класс.
const COUNT_LABELS = [
	['completed', 'завершили'],
	['in_progress', 'разговор не завершён'],
	['not_started', 'не приступали'],
]

const ERROR_KIND_LABELS = {
	wrong_form: 'неверная форма',
	misuse: 'неуместное употребление',
	not_used: 'нужная конструкция не использована',
}

/** Вид ошибки по цели (раздел 1 договора). Без вида — «ошибка по цели». */
export function errorKindLabel(kind) {
	return ERROR_KIND_LABELS[kind] || 'ошибка по цели'
}

const GENERAL_TYPE_LABELS = { lexical: 'лексика', grammar: 'грамматика' }

/** Тип общей ошибки: «лексика» или «грамматика». */
export function generalTypeLabel(type) {
	return GENERAL_TYPE_LABELS[type] || 'ошибка'
}

const GOAL_KIND_LABELS = { phrase: 'фраза', grammar: 'тема' }

/** Вид цели: «фраза» (фокусная фраза) или «тема» (грамматическая тема). */
export function goalKindLabel(kind) {
	return GOAL_KIND_LABELS[kind] || 'цель'
}

export const UNKNOWN_LEVEL = 'уровень не определён'

/** Уровень строкой: «a2» → «A2», пусто → «уровень не определён». */
export function levelLabel(level) {
	const value = String(level || '').trim()
	return value ? value.toUpperCase() : UNKNOWN_LEVEL
}

// Технические сбои — отдельно от качества речи: ошибкой ученика они не
// считаются (Уч-4), но учитель должен видеть, что разговор шёл не гладко.
const INCIDENT_LABELS = {
	asr: 'распознавание речи',
	tts: 'озвучка',
	avatar: 'ответ аватара',
	avatar_unavailable: 'ответ аватара',
	llm: 'ответ аватара',
}

const pad = (n) => String(n).padStart(2, '0')

/** «разговор завершён 03.10 в 14:20» по местному времени; пусто или мусор — ''. */
export function closedLabel(iso) {
	if (!iso) return ''
	const d = new Date(iso)
	if (Number.isNaN(d.getTime())) return ''
	return `разговор завершён ${pad(d.getDate())}.${pad(d.getMonth() + 1)} в ${pad(d.getHours())}:${pad(d.getMinutes())}`
}

/* ---------------------------------------------------------------------------
 * Состояние разговора ученика
 * ------------------------------------------------------------------------- */

/**
 * Что показывать по ученику: 'not_started' | 'in_progress' | 'pending' |
 * 'failed' | 'ready'.
 *
 * Отчёт есть только у завершённого разговора с готовым разбором. Завершён,
 * а разбора ещё нет (статус пустой или pending) — «разбор готовится»: сервер
 * запускает его сразу после последнего ответа. failed сервер перезапускает
 * сам при следующем запросе отчёта.
 *
 * hasReport — есть ли сам отчёт (карточка ученика). В строке класса отчёта
 * нет, и решает report_status.
 */
export function conversationState(status, reportStatus, hasReport) {
	if (status === 'in_progress') return 'in_progress'
	if (status !== 'completed') return 'not_started'
	const ready = hasReport === undefined ? reportStatus === 'ready' : hasReport
	if (ready) return 'ready'
	return reportStatus === 'failed' ? 'failed' : 'pending'
}

/** Состояние карточки ученика по ответу `…/students/{id}/conversation`. */
export function reportState(data) {
	if (!data) return 'loading'
	return conversationState(data.status, data.report_status, Boolean(data.report))
}

/* ---------------------------------------------------------------------------
 * Отчёт класса
 * ------------------------------------------------------------------------- */

const isReady = (s) => conversationState(s?.status, s?.report_status) === 'ready'

/** Ученик из ответа: { id, name, tag }. */
function who(entry) {
	const s = entry?.student || {}
	return { id: s.id || '', name: s.name || 'Ученик', tag: s.tag || '' }
}

/**
 * Счётчики над отчётом: [{ key, label, n }] — завершили, не завершили, не
 * приступали. Сервер присылает их готовыми; нет — считаем по списку.
 */
export function countsView(data) {
	const students = data?.students || []
	return COUNT_LABELS.map(([key, label]) => ({
		key,
		label,
		n: data?.counts?.[key] ?? students.filter((s) => (s.status || 'not_started') === key).length,
	}))
}

/** Сколько учеников с готовым отчётом — знаменатель «зачли k из n». */
export function readyCount(data) {
	const students = data?.students
	if (Array.isArray(students) && students.length) return students.filter(isReady).length
	return data?.counts?.completed ?? 0
}

/**
 * Цели разговора одной строкой: «3 фразы и 1 тема». Пусто — ''.
 */
export function goalsLine(goals) {
	const list = goals || []
	const phrases = list.filter((g) => g.kind === 'phrase').length
	const themes = list.filter((g) => g.kind === 'grammar').length
	return [phrases && plural(phrases, 'фраза', 'фразы', 'фраз'), themes && plural(themes, 'тема', 'темы', 'тем')]
		.filter(Boolean)
		.join(' и ')
}

/**
 * Строки представления «По ученикам».
 *
 * Числа по целям — только у готового отчёта: у незавершённого разговора их
 * нет вовсе (учитель не видит разговор в реальном времени), а у завершённого
 * без разбора они ещё не посчитаны — вместо них подпись состояния.
 */
export function studentRows(data) {
	const maxTurns = data?.max_turns
	return (data?.students || []).map((s) => {
		const { id, name, tag } = who(s)
		const state = conversationState(s.status, s.report_status)
		const status = statusView(s.status)
		const ready = state === 'ready'
		return {
			id,
			name,
			tag,
			state,
			statusLabel: status.label,
			statusCls: status.cls,
			answers: maxTurns ? `${s.turns_taken ?? 0} из ${maxTurns}` : String(s.turns_taken ?? 0),
			credited: ready ? `${s.credited ?? 0} из ${s.goals_total ?? 0}` : '',
			errors: ready ? (s.errors ?? 0) : null,
			misses: ready ? (s.misses ?? 0) : null,
			note:
				state === 'pending'
					? 'разбор готовится…'
					: state === 'failed'
						? 'разбор не получился, повторяем'
						: '',
		}
	})
}

/**
 * Представление «По темам»: группы «Фокусные фразы» и «Грамматические темы».
 *
 * У каждой цели — число верных употреблений и ошибок по всему классу и
 * сколько учеников её зачли из тех, у кого отчёт готов. Пояснение цели
 * берётся из списка целей: в строке темы его нет.
 */
export function themeGroups(data) {
	const ready = readyCount(data)
	const goals = data?.goals || []
	const byKey = new Map(goals.map((g) => [g.key, g]))
	// Тем нет, а цели есть — ни одного готового отчёта: цели всё равно
	// показываем, с нулями. Учитель видит, по чему будет отчёт.
	const themes = data?.themes?.length
		? data.themes
		: goals.map((g) => ({ key: g.key, kind: g.kind, title: g.title }))

	const items = themes.map((t) => {
		const goal = byKey.get(t.key) || {}
		const credited = t.credited_students ?? 0
		const correctUses = t.correct_uses ?? 0
		const errors = t.errors ?? 0
		return {
			key: t.key,
			kind: t.kind || goal.kind || 'phrase',
			title: t.title || goal.title || t.key,
			note: goal.note || '',
			correctUses,
			errors,
			creditedStudents: credited,
			creditedLabel: `${credited} из ${ready}`,
			usesText: plural(correctUses, 'верное употребление', 'верных употребления', 'верных употреблений'),
			errorsText: plural(errors, 'ошибка', 'ошибки', 'ошибок'),
			// «из» требует родительного падежа: «из 1 ученика», «из 5 учеников».
			creditedText: `зачли ${credited} из ${plural(ready, 'ученика', 'учеников', 'учеников')}`,
			students: (t.students || []).map((s) => ({
				...who(s),
				correctUses: s.correct_uses ?? 0,
				errors: s.errors ?? 0,
				misses: s.misses ?? 0,
				credited: (s.correct_uses ?? 0) > 0,
			})),
		}
	})

	return [
		{ kind: 'phrase', title: 'Фокусные фразы', items: items.filter((t) => t.kind === 'phrase') },
		{ kind: 'grammar', title: 'Грамматические темы', items: items.filter((t) => t.kind === 'grammar') },
	].filter((g) => g.items.length > 0)
}

/**
 * Общие ошибки класса: группы по слову или конструкции, частые сверху.
 *
 * Число ошибок — не меньше, чем цитат в группе: сервер мог прислать счётчик,
 * а мог и нет. Учеников считаем по списку — у одного ученика в группе
 * бывает несколько цитат.
 */
export function commonErrorGroups(data) {
	const groups = (data?.common_errors || []).map((g, i) => {
		const students = (g.students || []).map((s) => ({
			...who(s),
			quotes: (s.quotes || []).map((q) => ({
				turnNo: q.turn_no,
				quote: q.quote || '',
				correctForm: q.correct_form || '',
			})),
		}))
		const quotes = students.reduce((n, s) => n + s.quotes.length, 0)
		const count = Math.max(g.count ?? 0, quotes)
		return {
			key: g.group_key || `group-${i}`,
			label: g.label || g.group_key || 'Без названия',
			type: g.type || '',
			typeLabel: generalTypeLabel(g.type),
			levelLabel: levelLabel(g.level),
			unknownLevel: !String(g.level || '').trim(),
			count,
			// «у» требует родительного падежа: «у 1 ученика», «у 2 учеников».
			countText: `${plural(count, 'ошибка', 'ошибки', 'ошибок')} у ${plural(
				students.length,
				'ученика',
				'учеников',
				'учеников',
			)}`,
			students,
		}
	})
	return groups.sort((a, b) => b.count - a.count || a.label.localeCompare(b.label, 'ru'))
}

/* ---------------------------------------------------------------------------
 * Карточка ученика: расшифровка с отметками
 * ------------------------------------------------------------------------- */

/**
 * Где цитата стоит в тексте ответа: { start, end } или null.
 *
 * По договору цитата — точная подстрока ответа этого хода. Запасные пути на
 * случай, если разбор разошёлся с текстом в мелочи: регистр и обрезанная
 * цитата промаха (текст ответа не длиннее 120 символов — с многоточием,
 * которого в ответе нет). Не нашлась — null: отметка остаётся в списке и
 * ведёт к ответу целиком.
 */
export function locate(text, quote) {
	const t = String(text || '')
	let q = String(quote || '').trim()
	if (!t || !q) return null
	let at = t.indexOf(q)
	if (at < 0) {
		const lower = t.toLowerCase()
		const lowerQ = q.toLowerCase()
		// Сдвиг по строке в нижнем регистре годится, только если длины не
		// поменялись: у пары редких букв нижний регистр длиннее.
		if (lower.length === t.length && lowerQ.length === q.length) at = lower.indexOf(lowerQ)
	}
	if (at < 0) {
		const cut = q.replace(/(…|\.\.\.)$/u, '').trimEnd()
		if (cut && cut !== q) {
			at = t.indexOf(cut)
			if (at >= 0) q = cut
		}
	}
	return at < 0 ? null : { start: at, end: at + q.length }
}

// Какая подсветка сильнее, когда цитаты перекрываются: ошибки заметнее
// зачтённого, а промах — подложка под всем ответом — слабее всех.
const TONE_RANK = { goal_error: 4, general: 3, credited: 2, miss: 1 }

/**
 * Текст ответа кусками по границам цитат: [{ text, ids, tone }].
 *
 * ranges — [{ id, start, end, tone }]. Цитаты могут перекрываться: зачтённая
 * фраза внутри ответа-промаха, общая ошибка внутри зачтённой фразы. Кусок
 * помнит все отметки, что его покрывают (ids), — по ним подсвечивается
 * выбранная отметка, — и тон самой сильной из них.
 */
export function segmentText(text, ranges) {
	const t = String(text || '')
	const list = (ranges || []).filter((r) => r && r.end > r.start)
	const cuts = new Set([0, t.length])
	for (const r of list) {
		cuts.add(Math.max(0, Math.min(t.length, r.start)))
		cuts.add(Math.max(0, Math.min(t.length, r.end)))
	}
	const points = [...cuts].sort((a, b) => a - b)
	const out = []
	for (let i = 0; i + 1 < points.length; i += 1) {
		const from = points[i]
		const to = points[i + 1]
		if (from === to) continue
		const covering = list.filter((r) => r.start <= from && r.end >= to)
		const top = covering.reduce((best, r) => ((TONE_RANK[r.tone] || 0) > (TONE_RANK[best?.tone] || 0) ? r : best), null)
		out.push({ text: t.slice(from, to), ids: covering.map((r) => r.id), tone: top ? top.tone : '' })
	}
	return out
}

// Порядок отметок под ответом и в списке: сначала то, что требует внимания.
const CATEGORY_ORDER = { goal_error: 0, general: 1, miss: 2, repair: 3, credited: 4 }

/** Подпись хода: «Ответ 3» у ученика, «Аватар» у реплики аватара. */
export function turnLabel(turn) {
	return turn.role === 'student' ? `Ответ ${turn.turn_no}` : 'Аватар'
}

/**
 * Ходы по порядку разговора: вступление аватара (0), ответ 1, реплика 1,
 * ответ 2… Сервер присылает их так же; сортировка — страховка, устойчивая к
 * одинаковым ключам.
 */
function orderTurns(turns) {
	const pos = (t) => (t.turn_no ?? 0) * 2 + (t.role === 'student' ? -1 : 0)
	return (turns || [])
		.map((t, i) => ({ t, i }))
		.sort((a, b) => pos(a.t) - pos(b.t) || a.i - b.i)
		.map(({ t }) => t)
}

/**
 * Цели с итогом по ученику. Счётчики — из разбора; если их нет, считаем по
 * отметкам: верное употребление — каждая отметка «зачтено».
 */
function goalRows(report) {
	const marks = report?.marks || []
	return (report?.goals || []).map((g) => {
		const own = marks.filter((m) => m.goal_key === g.key)
		const correctUses = g.correct_uses ?? own.filter((m) => m.verdict === 'credited').length
		const errors = g.errors ?? own.filter((m) => m.verdict === 'error').length
		const misses = g.misses ?? own.filter((m) => m.verdict === 'miss').length
		return {
			key: g.key,
			kind: g.kind,
			kindLabel: goalKindLabel(g.kind),
			title: g.title || g.key,
			note: g.note || '',
			credited: g.credited ?? correctUses > 0,
			correctUses,
			errors,
			misses,
		}
	})
}

/** Технические сбои по видам: [{ kind, label, count }]. */
export function incidentRows(incidents) {
	const byLabel = new Map()
	for (const it of incidents || []) {
		const label = INCIDENT_LABELS[it?.kind] || 'другой сбой'
		const count = Number.isFinite(it?.count) ? it.count : 1
		const prev = byLabel.get(label)
		byLabel.set(label, { kind: it?.kind || '', label, count: (prev?.count || 0) + count })
	}
	return [...byLabel.values()]
}

/**
 * Уровень общей ошибки с источником из справочника: «уровень A1 ·
 * Морфология: притяжательная конструкция». Источник сам начинается с уровня
 * («A1 · Морфология: …») — второй раз его не повторяем.
 */
export function levelWithSource(level, source) {
	const text = String(source || '').trim()
	const prefix = `${level} · `
	const rest = text.toUpperCase().startsWith(prefix.toUpperCase()) ? text.slice(prefix.length) : text
	return rest ? `уровень ${level} · ${rest}` : `уровень ${level}`
}

/** Сколько ошибок выше уровня урока скрыто — фразой; ноль — ''. */
export function aboveLevelLabel(n) {
	if (!n || n < 0) return ''
	const errors = plural(n, 'ошибка', 'ошибки', 'ошибок')
	const verb = n % 10 === 1 && n % 100 !== 11 ? 'не показана' : 'не показаны'
	return `Ещё ${errors} выше уровня урока ${verb}`
}

/**
 * Уровень речи ученика по справочнику — одной фразой; оценить нельзя — ''.
 */
export function speechLine(speech) {
	const level = String(speech?.estimated || '').trim()
	if (!level) return ''
	const share = Number(speech?.unknown_share)
	const unknown = Number.isFinite(share) && share > 0 ? `; ${Math.round(share * 100)} % слов в справочнике нет` : ''
	return `Речь ученика по справочнику уровней — примерно ${level.toUpperCase()}${unknown}`
}

/**
 * buildCard — карточка ученика из готового отчёта.
 *
 * Отметки всех видов сводятся в один список `notes` с общими полями:
 *   id        'm<i>' — отметка по цели, 'e<i>' — общая ошибка,
 *             'r<ход>' — ученик исправил распознанный текст
 *   category  goal_error | general | miss | credited | repair
 *   turnNo    номер ответа, anchor — id ответа в расшифровке
 *   quote     цитата; found — нашлась ли она в тексте ответа
 * Каждая отметка ведёт к своему ответу в расшифровке, а внутри ответа
 * подсвечен кусок с цитатой (turns[].segments).
 */
export function buildCard(report) {
	const goals = goalRows(report)
	const goalByKey = new Map(goals.map((g) => [g.key, g]))
	const turns = orderTurns(report?.turns)
	const studentTurn = new Map(turns.filter((t) => t.role === 'student').map((t) => [t.turn_no, t]))
	const anchorOf = (turnNo) => (studentTurn.has(turnNo) ? `cr-answer-${turnNo}` : '')

	const notes = []
	;(report?.marks || []).forEach((m, i) => {
		const category = m.verdict === 'credited' ? 'credited' : m.verdict === 'miss' ? 'miss' : 'goal_error'
		const goal = goalByKey.get(m.goal_key)
		notes.push({
			id: `m${i}`,
			category,
			turnNo: m.turn_no,
			quote: m.quote || '',
			goalTitle: goal?.title || m.goal_key || '',
			goalKind: goal?.kindLabel || '',
			label:
				category === 'credited' ? 'зачтено' : category === 'miss' ? 'промах' : errorKindLabel(m.error_kind),
			correctForm: m.correct_form || '',
			comment: m.note_ru || '',
		})
	})
	;(report?.general_errors || []).forEach((e, i) => {
		// Ошибки выше уровня урока сервер в отчёт не кладёт (их только число),
		// но если такая доедет — в основной отчёт она не попадает.
		if (e.visibility === 'above_level') return
		const unknownLevel = e.visibility === 'unknown_level' || !String(e.level || '').trim()
		const level = levelLabel(unknownLevel ? '' : e.level)
		notes.push({
			id: `e${i}`,
			category: 'general',
			turnNo: e.turn_no,
			quote: e.quote || '',
			label: generalTypeLabel(e.type),
			level,
			unknownLevel,
			levelText: unknownLevel ? UNKNOWN_LEVEL : levelWithSource(level, e.level_source),
			correctForm: e.correct_form || '',
			rule: e.rule_ru || '',
			group: e.group_label || '',
		})
	})
	for (const t of turns) {
		if (t.role !== 'student' || !t.edited) continue
		notes.push({
			id: `r${t.turn_no}`,
			category: 'repair',
			turnNo: t.turn_no,
			quote: '',
			label: 'исправил распознанный текст',
			before: t.asr_text || '',
			after: t.content || '',
		})
	}

	const ranges = new Map()
	for (const n of notes) {
		n.anchor = anchorOf(n.turnNo)
		n.found = false
		if (n.category === 'repair' || !n.quote) continue
		const range = locate(studentTurn.get(n.turnNo)?.content, n.quote)
		if (!range) continue
		n.found = true
		if (!ranges.has(n.turnNo)) ranges.set(n.turnNo, [])
		ranges.get(n.turnNo).push({ id: n.id, tone: n.category, ...range })
	}

	const byOrder = (a, b) =>
		(a.turnNo ?? 0) - (b.turnNo ?? 0) || CATEGORY_ORDER[a.category] - CATEGORY_ORDER[b.category]
	const sorted = [...notes].sort(byOrder)
	const pick = (category) => sorted.filter((n) => n.category === category)

	return {
		goals,
		goalsTotal: goals.length,
		creditedCount: goals.filter((g) => g.credited).length,
		turns: turns.map((t, i) => {
			const student = t.role === 'student'
			return {
				key: `${t.role}-${t.turn_no}-${i}`,
				role: t.role,
				turnNo: t.turn_no,
				label: turnLabel(t),
				anchor: student ? `cr-answer-${t.turn_no}` : '',
				content: t.content || '',
				translation: t.translation || '',
				voice: t.input === 'voice',
				edited: Boolean(t.edited),
				asrText: t.asr_text || '',
				segments: student
					? segmentText(t.content, ranges.get(t.turn_no))
					: [{ text: t.content || '', ids: [], tone: '' }],
				// sorted уже упорядочен по ответу и виду отметки.
				notes: student ? sorted.filter((n) => n.turnNo === t.turn_no) : [],
			}
		}),
		notes: sorted,
		lists: {
			goalErrors: pick('goal_error'),
			general: pick('general'),
			misses: pick('miss'),
			repairs: pick('repair'),
			credited: pick('credited'),
		},
		aboveLevel: report?.above_level_count || 0,
		aboveLevelLabel: aboveLevelLabel(report?.above_level_count),
		incidents: incidentRows(report?.incidents),
		speech: speechLine(report?.speech_level),
		closed: closedLabel(report?.closed_at),
		level: levelLabel(report?.level),
	}
}

/** Первая буква заглавная: подписи хранятся строчными, заголовок — с заглавной. */
const capital = (text) => (text ? text.charAt(0).toUpperCase() + text.slice(1) : '')

/**
 * Заголовок отметки в списке: «Неверная форма», «Грамматика», «Промах»,
 * «Зачтено», «Исправил распознанный текст».
 */
export function noteTitle(note) {
	return capital(note?.label || '')
}

/**
 * Отметка одной строкой — под ответом в расшифровке:
 *   «Неверная форма: мәктәпкә → мәктәптә»
 *   «Грамматика, A1: дустым исеме → дустымның исеме»
 *   «Лексика, уровень не определён: папка → әти»
 *   «Зачтено: Минем исемем …» и «Промах: Миңа … ошый» — по названию цели
 */
export function noteLine(note) {
	if (!note) return ''
	const fix = (quote) => (note.correctForm ? `${quote} → ${note.correctForm}` : quote)
	switch (note.category) {
		case 'goal_error':
			return `${noteTitle(note)}: ${fix(note.quote || note.goalTitle)}`
		case 'general':
			return `${noteTitle(note)}, ${note.unknownLevel ? UNKNOWN_LEVEL : note.level}: ${fix(note.quote)}`
		case 'repair':
			return 'Исправил распознанный текст'
		default:
			return `${noteTitle(note)}: ${note.goalTitle || note.quote}`
	}
}

/**
 * Отметка, к которой ведёт клик по цитате в общих ошибках класса: общая
 * ошибка того же ответа с той же цитатой, а если её не нашлось — любая
 * отметка этого ответа. Нет и такой — null.
 */
export function noteFor(card, focus) {
	if (!card || !focus) return null
	const same = card.notes.filter((n) => n.turnNo === focus.turnNo)
	return (
		same.find((n) => n.category === 'general' && n.quote === focus.quote) ||
		same.find((n) => n.quote === focus.quote) ||
		same[0] ||
		null
	)
}
