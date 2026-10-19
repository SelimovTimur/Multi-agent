// student-feedback.js — правила короткого опроса ученика после урока.
//
// Отдельно от окна, чтобы проверять их без браузера.

export const RATINGS = [
	{ value: 'bad', label: '🙁', title: 'Плохо' },
	{ value: 'ok', label: '😐', title: 'Нормально' },
	{ value: 'good', label: '🙂', title: 'Хорошо' },
]

export const TEXT_LIMIT = 1000

const ASKED_KEY = 'student-feedback-asked'

function askedMap(storage) {
	try {
		const map = JSON.parse(storage?.getItem(ASKED_KEY) || '{}')
		return map && typeof map === 'object' && !Array.isArray(map) ? map : {}
	} catch {
		return {}
	}
}

// Какие вопросы задать по этой попытке. Каждый — один раз: про урок —
// после первого итога, про аватара — после итога раздела, где был
// разговор. Разговор бывает только в домашней работе, а про урок ученик
// уже ответил после работы на уроке: тогда спрашиваем только про аватара.
// Пустой список — окно не открываем.
export function questionsToAsk(attemptId, talked, storage) {
	if (!attemptId) return []
	const asked = askedMap(storage)[attemptId] || []
	const out = []
	if (!asked.includes('lesson')) out.push('lesson')
	if (talked && !asked.includes('avatar')) out.push('avatar')
	return out
}

// Хранятся последние 50 попыток, чтобы запись не росла без конца.
export function markAsked(attemptId, questions, storage) {
	try {
		const map = askedMap(storage)
		const merged = [...new Set([...(map[attemptId] || []), ...questions])]
		delete map[attemptId]
		const entries = [...Object.entries(map), [attemptId, merged]].slice(-50)
		storage?.setItem(ASKED_KEY, JSON.stringify(Object.fromEntries(entries)))
	} catch {
		// Хранилище закрыто — в худшем случае спросим ещё раз.
	}
}

// Тело первого шага: только отвеченные вопросы. null — не ответил ни на
// один, отправлять нечего.
export function ratingsPayload({ lesson, avatar, unclear }) {
	const out = {}
	if (lesson) out.lesson = lesson
	if (avatar) out.avatar = avatar
	const text = (unclear || '').trim()
	if (text) out.unclear = text
	return Object.keys(out).length ? out : null
}

// После 🙁 переспрашиваем, почему. Список — про что именно спросить.
export function whyQuestions({ lesson, avatar }) {
	const out = []
	if (lesson === 'bad') out.push('lesson')
	if (avatar === 'bad') out.push('avatar')
	return out
}

export function whyPayload({ lessonWhy, avatarWhy }) {
	const out = {}
	const lesson = (lessonWhy || '').trim()
	const avatar = (avatarWhy || '').trim()
	if (lesson) out.lesson_why = lesson
	if (avatar) out.avatar_why = avatar
	return Object.keys(out).length ? out : null
}
