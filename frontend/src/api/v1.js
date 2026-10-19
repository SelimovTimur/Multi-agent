// v1.js — клиент нового контура /api/v1.
//
// Отдельно от api.js намеренно: тот работает с legacy-контуром /api и уйдёт
// вместе с ним. Здесь другой контракт — единый конверт ошибки, cookie вместо
// заголовка сессии, права в ответе профиля.
//
// Никаких токенов в localStorage: сессия живёт в HttpOnly-cookie, и JS до неё
// не дотягивается. Это защищает от кражи сессии через XSS.

const BASE = '/api/v1'

/**
 * Причина отказа, когда она одна.
 *
 * Только для одного поля: там причина и есть всё сообщение. Если сервер
 * назвал несколько, шапка нужна — иначе в окошке окажется одна из причин, и
 * какая именно, решит порядок ключей.
 */
function onlyReason(code, fields) {
	if (code !== 'validation_failed' || !fields) return ''
	const reasons = Object.values(fields).filter(Boolean)
	return reasons.length === 1 ? reasons[0] : ''
}

/**
 * ApiError — ошибка, разобранная из единого конверта бэкенда.
 *
 * Ветвиться нужно по `code`, а не по тексту: текст пишется для человека и
 * меняется без предупреждения.
 *
 *   code      машинный код: validation_failed, unauthorized, not_found...
 *   message   что показать пользователю
 *   fields    { поле: причина } — только у validation_failed
 *   requestId то, что пользователь называет поддержке
 */
export class ApiError extends Error {
	constructor({ status, code, message, fields, requestId }) {
		// У отказа по одному полю причина лежит в `fields`, а `message` —
		// служебная шапка вроде «Проверьте заполнение формы». В окошке
		// сообщения шапка бесполезна: сервер назвал причину — «Ссылка должна
		// начинаться с http://», — а человек читал, что с формой что-то не
		// так, и не знал, что именно. Поля остаются на месте: экраны, которые
		// подсвечивают их под полями, читают `fields`.
		super(onlyReason(code, fields) || message || 'Что-то пошло не так')
		this.name = 'ApiError'
		this.status = status
		this.code = code || 'internal_error'
		this.fields = fields || null
		this.requestId = requestId || null
	}

	/** Ошибка заполнения формы — есть что подсветить под полями. */
	get isValidation() {
		return this.code === 'validation_failed'
	}

	/** Нужно отправить на экран входа. */
	get isUnauthorized() {
		return this.code === 'unauthorized'
	}

	/** Модели перегружены — предложить повторить, а не показывать «ошибка». */
	get isBusy() {
		return this.code === 'unavailable' || this.code === 'rate_limited'
	}
}

/**
 * Отказ сборки из кадра потока или из упавшей сборочной задачи — конверт у
 * них один.
 */
export function buildFailure(e) {
	return new ApiError({
		status: e.status || 502,
		code: e.code,
		message: e.message || 'Не удалось собрать урок',
		// Разбор по полям сервер шлёт и здесь. Без него отказ проверки
		// превращался бы в общее «не удалось собрать урок», хотя сервер
		// назвал поле и причину.
		fields: e.fields,
		requestId: e.request_id,
	})
}

async function request(path, { method = 'GET', body, signal, timeoutMs = 30000, headers = {} } = {}) {
	// Свой таймаут поверх fetch: браузерный ждёт минутами, а пользователь
	// за это время успевает нажать кнопку ещё пять раз.
	const controller = new AbortController()
	const timer = setTimeout(() => controller.abort(), timeoutMs)
	const onAbort = () => controller.abort()
	signal?.addEventListener('abort', onAbort, { once: true })
	if (signal?.aborted) onAbort()

	try {
		const res = await fetch(`${BASE}${path}`, {
			method,
			credentials: 'include', // без этого cookie сессии не уходит
			headers: body === undefined ? headers : { 'Content-Type': 'application/json', ...headers },
			body: body === undefined ? undefined : JSON.stringify(body),
			signal: controller.signal,
		})
		if (res.status === 204) return null
		let payload = null
		try {
			payload = await res.json()
		} catch (err) {
			if (err.name === 'AbortError' || controller.signal.aborted) throw err
			if (res.ok) throw new ApiError({ status: res.status, code: 'invalid_response',
				message: 'Сервер вернул неполный ответ. Попробуйте ещё раз', requestId: res.headers.get('X-Request-ID') })
		}
		if (!res.ok) {
			const envelope = payload?.error || {}
			throw new ApiError({ status: res.status, code: envelope.code, message: envelope.message,
				fields: envelope.fields, requestId: envelope.request_id || res.headers.get('X-Request-ID') })
		}
		return payload
	} catch (err) {
		if (err instanceof ApiError) throw err
		if (signal?.aborted) throw new DOMException('Запрос отменён', 'AbortError')
		if (err.name === 'AbortError' || controller.signal.aborted) {
			throw new ApiError({
				status: 0,
				code: 'timeout',
				message: 'Сервер долго не отвечает. Попробуйте ещё раз',
			})
		}
		throw new ApiError({
			status: 0,
			code: 'network',
			message: 'Нет связи с сервером. Проверьте интернет',
		})
	} finally {
		clearTimeout(timer)
		signal?.removeEventListener('abort', onAbort)
	}
}

const get = (path, opts) => request(path, { ...opts, method: 'GET' })
const post = (path, body, opts) => request(path, { ...opts, method: 'POST', body })
const patch = (path, body, opts) => request(path, { ...opts, method: 'PATCH', body })
const del = (path, opts) => request(path, { ...opts, method: 'DELETE' })

// Рукопожатие живого голоса ждёт дольше остальных запросов: оно идёт в
// api.openai.com напрямую, через туннель, где TLS занимает до двадцати с
// лишним секунд. Сервер ждёт свободное место до 20 секунд (live.openWait),
// когда весь класс нажимает «Начать» разом, и отмеряет провайдеру ещё 60
// (live.sessionBudget); здесь на десять больше, чтобы до ученика дошла
// причина от сервера, а не молчаливый обрыв браузером, — и чтобы id уже
// созданной сессии дошёл до клиента и место на ключе освободилось.
const LIVE_HANDSHAKE_TIMEOUT_MS = 90000

const liveEvent = ({ stage, result, protocol = '', network = '' }) =>
	post('/live/events', { stage, result, protocol, network }, { timeoutMs: 5000 })

// Наблюдаемость не должна задерживать голосовой разговор и менять его ошибку.
const reportLiveEvent = (event) => { void liveEvent(event).catch(() => {}) }

export const live = {
	create: (sdp, opts) => post('/live/sessions', { sdp }, { ...opts, timeoutMs: LIVE_HANDSHAKE_TIMEOUT_MS }),

	// Только технический исход соединения: в журнал доступности не передаются
	// звук, расшифровка или текст ошибки браузера.
	event: liveEvent,
	report: reportLiveEvent,
}

/* ---------------------------------------------------------------------------
 * Аккаунт учителя
 * ------------------------------------------------------------------------- */

export const auth = {
	login: (email, password) => post('/auth/login', { email, password }),

	logout: () => post('/auth/logout', {}),

	/**
	 * Кто я сейчас. Вызывается при загрузке приложения.
	 * Отвечает 200 и для неаутентифицированного — со значением false.
	 */
	status: () => get('/auth/status'),

}

export const profile = {
	me: () => get('/me'),
	updateName: (name) => patch('/me', { name }),
	/**
	 * Смена пароля убивает остальные сессии, текущую оставляет.
	 *
	 * Имена полей — current и new, как ждёт сервер. Раньше здесь уходили
	 * current_password и new_password: сервер видел пустые строки и отвечал
	 * «Не короче 8 символов» на любой правильный пароль.
	 */
	changePassword: (currentPassword, newPassword) =>
		post('/me/password', { current: currentPassword, new: newPassword }),
}

/* ---------------------------------------------------------------------------
 * Классы
 * ------------------------------------------------------------------------- */

export const classes = {
	list: ({ archived = false } = {}) => get(`/classes${archived ? '?archived=1' : ''}`),

	create: ({ name, subject, level, lang }) => post('/classes', { name, subject, level, lang }),

	get: (classId) => get(`/classes/${classId}`),

	/** Принимает любое подмножество полей. Язык класса не меняется. */
	update: (classId, changes) => patch(`/classes/${classId}`, changes),

	/** Класс не удаляется, а архивируется: у него есть ответы и уроки. */
	archive: (classId, archived = true) => post(`/classes/${classId}/archive`, { archived }),

	/** Состав класса с личными кодами учеников. removed=true добавляет исключённых. */
	students: (classId, { removed = false } = {}) =>
		get(`/classes/${classId}/students${removed ? '?removed=1' : ''}`),

	/**
	 * Завести учеников списком имён. Каждому выдаётся личный код.
	 * Ответ: `{added: [...], skipped: [...]}` — skipped это имена, которые в
	 * классе уже есть: повторная вставка того же списка ничего не дублирует.
	 */
	addStudents: (classId, names) => post(`/classes/${classId}/students`, { names }),

	/**
	 * Новый личный код ученика: старый перестаёт работать, а ученик выходит
	 * со всех устройств. Нужен, когда карточку потеряли или подсмотрели.
	 */
	reissueCode: (classId, studentId) => post(`/classes/${classId}/students/${studentId}/code`, {}),

	/** Мягкое исключение: доступ закрыт, ответы сохранены. */
	removeStudent: (classId, studentId) => del(`/classes/${classId}/students/${studentId}`),

	restoreStudent: (classId, studentId) =>
		post(`/classes/${classId}/students/${studentId}/restore`, {}),

	/**
	 * Сводка по классу: сколько выдано, сколько пройдено, доля верных
	 * ответов и разрез по урокам и ученикам.
	 *
	 * Считает сервер, а не экран: у него есть события ответов, а у
	 * браузера — только то, что ему отдали.
	 */
	analytics: (classId) => get(`/classes/${classId}/analytics`),

	/**
	 * Трудные слова класса — блок «Что повторить на следующем уроке»:
	 * слова из лексики уроков, где класс ошибается чаще всего, с ошибками
	 * в заданиях и в разговоре. Отдельно от таблицы: считать дольше, а
	 * таблица должна открываться сразу. assignmentId — только один урок
	 * класса: вкладка «Трудные слова» на странице урока.
	 */
	hardWords: (classId, assignmentId) =>
		get(`/classes/${classId}/analytics/words${assignmentId ? `?assignment_id=${assignmentId}` : ''}`),
}

/* ---------------------------------------------------------------------------
 * Ученик
 * ------------------------------------------------------------------------- */

export const student = {
	/**
	 * Вход по личному коду с карточки — единственный способ входа ученика.
	 * Код в ответе не приходит — ученик его только что ввёл, он у него есть.
	 */
	restore: (code, device = '') => post('/students/restore', { code, device }),

	me: () => get('/students/me'),
	updateName: (displayName) => patch('/students/me', { display_name: displayName }),
	myClasses: () => get('/students/me/classes'),

	/** Выход только с этого устройства, остальные продолжают работать. */
	logout: () => post('/students/logout', {}),
}

/* ---------------------------------------------------------------------------
 * ML-пайплайн
 * ------------------------------------------------------------------------- */

/* ---------------------------------------------------------------------------
 * Библиотека уроков
 * ------------------------------------------------------------------------- */

export const lessons = {
	/** Библиотека автора. Фильтры: level, lang, status, q. */
	list: (filters = {}) => {
		const q = new URLSearchParams(
			Object.entries(filters).filter(([, v]) => v !== '' && v != null),
		).toString()
		return get(`/lessons${q ? `?${q}` : ''}`)
	},

	/** Урок со списком его версий. */
	get: (lessonId) => get(`/lessons/${lessonId}`),

	archive: (lessonId, archived = true) => post(`/lessons/${lessonId}/archive`, { archived }),
	remove: (lessonId) => del(`/lessons/${lessonId}`),
	versions: (lessonId) => get(`/lessons/${lessonId}/versions`),

	/** Версия со всем содержимым. Учителю — вместе с ключом ответов. */
	version: (versionId) => get(`/lesson-versions/${versionId}`),

	/**
	 * Смена статуса версии: draft → ready → published, archived с любого места.
	 * В ответе приходит allowed_next — по нему интерфейс гасит недоступные кнопки.
	 */
	/**
	 * Смена статуса версии: draft → ready → published, archived с любого места.
	 * В ответе приходит allowed_next — по нему интерфейс гасит недоступные кнопки.
	 *
	 * `extra` — название и заметка к версии. Их принимает только этот запрос:
	 * опубликованная версия неизменяема, и записать их отдельным сохранением
	 * уже нельзя. Поэтому форма публикации отдаёт их вместе со статусом.
	 */
	setStatus: (versionId, status, extra = {}) =>
		post(`/lesson-versions/${versionId}/status`, { status, ...extra }),

}

/* ---------------------------------------------------------------------------
 * Назначения
 * ------------------------------------------------------------------------- */

export const assignments = {
	/**
	 * Назначить урок классу. Задания при этом закрыты: ученики ничего не
	 * видят, пока учитель не выдаст задания на уроке или на дом
	 * (docs/backend/class-lessons-api.md). Сроков у назначения больше нет —
	 * срок бывает только у домашней работы.
	 */
	create: ({ lessonVersionId, classId }) =>
		post('/assignments', {
			lesson_version_id: lessonVersionId,
			class_id: classId,
		}),

	/**
	 * Экран «Урок в классе»: задания урока, выдачи и таблица «кто как
	 * проходит». Опрашивается раз в 5 секунд, пока экран открыт.
	 */
	live: (assignmentId, opts) => get(`/assignments/${assignmentId}/live`, opts),

	/**
	 * Выдать задания: kind 'class' — на уроке, сразу; 'home' — на дом до
	 * dueDate (YYYY-MM-DD, конец дня по времени школы).
	 */
	issue: (assignmentId, { kind, taskKeys, dueDate }) =>
		post(`/assignments/${assignmentId}/issues`, {
			kind,
			task_keys: taskKeys,
			...(kind === 'home' ? { due_date: dueDate } : {}),
		}),

	/** Панель ученика на экране «Урок в классе»: его ответы по заданиям. */
	student: (assignmentId, studentId, opts) =>
		get(`/assignments/${assignmentId}/students/${studentId}`, opts),

	/** Вкладка «Уроки» класса. state: scheduled | active | closed. */
	ofClass: (classId, state = '') =>
		get(`/classes/${classId}/assignments${state ? `?state=${state}` : ''}`),

	/** Продлить или сдвинуть сроки. Действует на весь класс сразу. */
	reschedule: (assignmentId, { opensAt, closesAt }) =>
		patch(`/assignments/${assignmentId}`, {
			opens_at: opensAt || null,
			closes_at: closesAt || null,
		}),

	/** Закрыть доступ досрочно. Ответы учеников сохраняются. */
	close: (assignmentId) => post(`/assignments/${assignmentId}/close`, {}),

	/**
	 * Снять назначение.
	 *
	 * В ответе `result`: "deleted" — записи не было попыток и она удалена,
	 * "closed" — попытки были, поэтому назначение только закрыто. Ответы
	 * учеников не удаляются никогда.
	 */
	unassign: (assignmentId) => del(`/assignments/${assignmentId}`),

	/**
	 * Страница урока в аналитике: сводка по классу (те же числа, что в
	 * столбце таблицы результатов) и по каждому письменному заданию — доля
	 * верных и частые неверные ответы, одинаковые склеены с числом.
	 */
	analytics: (assignmentId) => get(`/assignments/${assignmentId}/analytics`),

	/** Прогресс класса по назначению. */
	progress: (assignmentId) => get(`/assignments/${assignmentId}/progress`),

	/**
	 * Отчёт по разговору с аватаром для всего класса: статусы учеников,
	 * результаты по целям («По темам») и общие ошибки класса
	 * (docs/product/avatar-goals/contract.md, раздел 6 «Учитель»). В темы и
	 * общие ошибки идут только готовые отчёты.
	 */
	conversationReport: (assignmentId, opts) => get(`/assignments/${assignmentId}/conversation`, opts),

	/**
	 * Разговор одного ученика: статус и, после завершения с готовым разбором,
	 * отчёт — расшифровка, отметки по целям, общие ошибки и промахи. Пока
	 * разговор не закончен или разбор готовится, `report` — null.
	 */
	studentConversation: (assignmentId, studentId, opts) =>
		get(`/assignments/${assignmentId}/students/${studentId}/conversation`, opts),
}

/**
 * Выдачи заданий (docs/backend/class-lessons-api.md).
 */
export const issues = {
	/** Завершить работу на уроке: задания закрываются у всех учеников. */
	finish: (issueId) => post(`/issues/${issueId}/finish`, {}),

	/** Продлить домашнюю работу; после срока — задания снова открываются. */
	extend: (issueId, dueDate) => post(`/issues/${issueId}/extend`, { due_date: dueDate }),

	/** Отменить выдачу, пока её никто не начал. */
	cancel: (issueId) => del(`/issues/${issueId}`),
}

/* ---------------------------------------------------------------------------
 * Ученик: мои уроки и прохождение
 * ------------------------------------------------------------------------- */

export const my = {
	/**
	 * «Мои занятия»: уроки, у которых выдано хоть одно задание, с разделами
	 * «Сейчас на уроке» (class_section), «Домашняя работа» (home_section) и
	 * признаком «Завершённые» (finished). Форма — в
	 * docs/backend/class-lessons-api.md.
	 */
	lessons: (classId, opts) => get(`/my/lessons${classId ? `?class_id=${classId}` : ''}`, opts),

	/** Открыть урок. Повторный вызов продолжает начатую попытку. */
	start: (assignmentId) => post(`/my/lessons/${assignmentId}/start`, {}),

	/**
	 * «Результаты урока»: выдачи ученика со счётом и звёздами — и закрытые
	 * тоже, которых в разделах «Моих занятий» уже нет. Только просмотр.
	 * Форма — в docs/backend/class-lessons-api.md.
	 */
	results: (assignmentId, opts) => get(`/my/lessons/${assignmentId}/results`, opts),

	/**
	 * Содержимое урока: тексты, выражения, упражнения.
	 *
	 * Берётся по попытке, а не по версии: право доступа у ученика
	 * выводится из того, что урок ему выдан. Ключа ответов здесь нет.
	 */
	lesson: (attemptId) => get(`/my/attempts/${attemptId}/lesson`),

	/**
	 * Ответить на вопрос упражнения.
	 *
	 * Правильность решает сервер: ключа ответов на клиенте нет и быть не
	 * должно. В ответе приходит `correct` и `expected` — верный вариант,
	 * который показывается уже ПОСЛЕ попытки.
	 *
	 * itemID — что именно отвечено: идентификатор вопроса в тесте или
	 * левая часть пары в сопоставлении.
	 */
	answer: (attemptId, { exerciseId, itemId, step, answer }, opts) =>
		post(`/my/attempts/${attemptId}/answer`, {
			exercise_id: exerciseId || '',
			item_id: itemId || '',
			step,
			answer,
		}, opts),

	finish: (attemptId) => post(`/my/attempts/${attemptId}/finish`, {}),

	/** Ответы на опрос после урока; пропущенные вопросы не шлём. */
	feedback: (attemptId, answers) => post(`/my/attempts/${attemptId}/feedback`, answers),

	/**
	 * Состояние попытки: счёт, «отвечено N из M» и прежние ответы.
	 *
	 * Нужно при открытии урока: без прежних ответов ученик видит пустые
	 * варианты там, где уже отвечал, и отвечает второй раз — а сервер такой
	 * ответ больше не принимает.
	 */
	attempt: (attemptId) => get(`/my/attempts/${attemptId}`),

	/** Докуда ученик дошёл. Отвечает на вопрос «где он остановился». */
	setStep: (attemptId, step) => post(`/my/attempts/${attemptId}/step`, { step }),

	/* --- выданные задания (урок в классе) --- */

	/**
	 * Открытые задания раздела: section 'class' — на уроке, 'home' — на дом,
	 * без section — все открытые. Опрашивается раз в 5 секунд: новое задание
	 * от учителя появляется само, завершённая учителем работа — тоже.
	 */
	tasks: (attemptId, section, opts) =>
		get(`/my/attempts/${attemptId}/tasks${section ? `?section=${section}` : ''}`, opts),

	/** Ученик открыл задание — учитель видит «открыл» в таблице класса. */
	openTask: (attemptId, taskKey) =>
		post(`/my/attempts/${attemptId}/tasks/${encodeURIComponent(taskKey)}/open`, {}),

	/** Задание без вопросов сделано: текст прочитан, разговор окончен. */
	taskDone: (attemptId, taskKey) =>
		post(`/my/attempts/${attemptId}/tasks/${encodeURIComponent(taskKey)}/done`, {}),

	/* --- разговор с аватаром по целям урока --- */

	/**
	 * Состояние разговора: реплики, счёт ответов, статус и `pending_answer` —
	 * ответ, который сервер сохранил, а аватар на него не ответил. По нему
	 * экран после перезагрузки продолжает с того же места.
	 */
	conversation: (attemptId, opts) => get(`/my/attempts/${attemptId}/conversation`, opts),

	/** Начать разговор. Повторный вызов не начинает заново: отдаёт последнюю реплику аватара. */
	startConversation: (attemptId) =>
		post(`/my/attempts/${attemptId}/conversation/start`, {}, { timeoutMs: 120000 }),

	/**
	 * Ответ ученика.
	 *
	 * clientMsgId обязателен и один на ответ: повтор с тем же идентификатором
	 * сервер второй раз не засчитывает. Распознанный текст и уверенность
	 * уходят только у ответа голосом — у набранного их нет, и пустые поля
	 * читались бы как «распознавание ничего не услышало».
	 *
	 * mode 'live' — ответ из живого разговора (договор, 10.2): реплику
	 * произносит сессия, сервер вместо неё отдаёт steer — указание сессии на
	 * следующий ход; reply и translation пустые. Без mode поле не уходит.
	 */
	answerConversation: (attemptId, { clientMsgId, text, input, asrText, asrConfidence, edited, mode }) =>
		post(
			`/my/attempts/${attemptId}/conversation/answer`,
			{
				client_msg_id: clientMsgId,
				text,
				input,
				...(input === 'voice' ? { asr_text: asrText ?? '', asr_confidence: asrConfidence ?? null } : {}),
				edited: Boolean(edited),
				...(mode ? { mode } : {}),
			},
			{ timeoutMs: 120000 },
		),

	/**
	 * Технический сбой у ученика: kind 'asr' — распознавание, 'tts' — озвучка.
	 * Учитель видит сбои отдельно от качества речи, ошибкой ученика они не считаются.
	 */
	conversationIncident: (attemptId, { kind, turnNo, message } = {}) =>
		post(`/my/attempts/${attemptId}/conversation/incident`, {
			kind,
			...(Number.isInteger(turnNo) ? { turn_no: turnNo } : {}),
			...(message ? { message } : {}),
		}),

	/** Итог ученика после закрытия разговора: status pending → ready. Списка ошибок в нём нет. */
	conversationSummary: (attemptId, opts) => get(`/my/attempts/${attemptId}/conversation/summary`, opts),

	/* --- живой режим разговора урока: одна сессия GPT Live --- */
	// Договор — docs/product/avatar-goals/contract.md, раздел 10.

	/**
	 * Открыть живую сессию разговора: сервер меняет предложение соединения
	 * на ответ провайдера. В ответе — session.id, transport.sdp и steer —
	 * указание сессии на вступление (или на следующий ход, если разговор
	 * уже идёт), а также счёт ответов и статус.
	 */
	openConversationLive: (attemptId, sdp, opts) =>
		post(`/my/attempts/${attemptId}/conversation/live`, { sdp }, { ...opts, timeoutMs: LIVE_HANDSHAKE_TIMEOUT_MS }),

	/**
	 * Реплика аватара из расшифровки сессии. turnNo — номер ответа ученика,
	 * на который она отвечает (вступление — 0). steerTurn — номер хода
	 * последнего указания, ушедшего в сессию до первого слова реплики
	 * (вступление — 0): по нему движок понимает, к какой цели реплика вела.
	 * Повтор с тем же clientMsgId ничего не дублирует. В ответе — translation
	 * для «Подсказки».
	 */
	conversationAvatar: (attemptId, { clientMsgId, turnNo, content, steerTurn }) =>
		post(`/my/attempts/${attemptId}/conversation/avatar`, {
			client_msg_id: clientMsgId,
			turn_no: turnNo,
			content,
			...(Number.isInteger(steerTurn) ? { steer_turn: steerTurn } : {}),
		}),

	/** Инструмент делегата lesson_progress: открытые цели, что не произносить, верные формы. */
	conversationProgress: (attemptId, opts) => get(`/my/attempts/${attemptId}/conversation/progress`, opts),

	/** Инструмент делегата check_reply: проверка задуманной реплики аватара. */
	conversationCheck: (attemptId, text, opts) => post(`/my/attempts/${attemptId}/conversation/check`, { text }, opts),

	/**
	 * Живая сессия закончена: место на ключе освобождается. Без этого
	 * сигнала место держалось бы до таймаута шлюза.
	 */
	endConversationLive: (attemptId, sessionId) =>
		post(`/my/attempts/${attemptId}/conversation/live/end`, { session_id: sessionId }),

	/** То же при уходе со страницы: sendBeacon доставляет запрос после закрытия вкладки. */
	endConversationLiveBeacon: (attemptId, sessionId) => {
		try {
			const body = new Blob([JSON.stringify({ session_id: sessionId })], { type: 'application/json' })
			return navigator.sendBeacon(`${BASE}/my/attempts/${attemptId}/conversation/live/end`, body)
		} catch {
			return false
		}
	},
}

/* ---------------------------------------------------------------------------
 * Статистика ученика для учителя
 * ------------------------------------------------------------------------- */

export const learning = {
	/**
	 * Карточка ученика: сводка, разбивка по видам заданий, история уроков.
	 *
	 * Разбивка важнее суммы: 90% в сопоставлении и 40% в грамматике — это
	 * разговор про грамматику, а не про «слабого ученика».
	 */
	studentCard: (classId, studentId) => get(`/classes/${classId}/students/${studentId}/card`),
}

/* ---------------------------------------------------------------------------
 * Голос: распознавание, ссылки, синтез
 * ------------------------------------------------------------------------- */

export const speech = {
	/**
	 * Голосовой ответ ученика в разговоре с аватаром.
	 *
	 * language — язык урока: распознаванию нужна его орфография (ә, ө, ү…),
	 * иначе татарскую речь записывает казахскими или башкирскими буквами.
	 * В ответе, кроме текста, — confidence, needs_review и review_reasons:
	 * по ним экран решает, показать ли ученику распознанное до отправки.
	 */
	transcribeMine: (file, { language = '', ...opts } = {}) =>
		upload('/my/transcribe', file, { timeoutMs: 180000, ...opts, fields: language ? { language } : null }),

	/**
	 * Озвучить текст. Возвращает Blob со звуком.
	 *
	 * Ошибка не должна срывать урок: в разговоре с аватаром текста реплики на
	 * экране нет, поэтому при сбое экран сам открывает её текст и пишет сбой в
	 * журнал разговора (`my.conversationIncident`).
	 */
	speak: async (text, attemptId = '') => {
		const res = await fetch(`${BASE}/my/tts`, {
			method: 'POST',
			credentials: 'include',
			headers: { 'Content-Type': 'application/json' },
			// Номер попытки — ради тона разговора: сам тон задаёт учитель, и
			// сервер берёт его по уроку, а не с клиента.
			body: JSON.stringify({ text, attempt_id: attemptId }),
		})
		if (!res.ok) throw new ApiError({ status: res.status, code: 'unavailable', message: 'Синтез недоступен' })
		return res.blob()
	},
}

/** upload отправляет файл формой. Content-Type задаёт браузер — с границей. */
async function upload(path, file, { timeoutMs = 300000, fields = null, signal = null, headers = undefined } = {}) {
	const form = new FormData()
	form.append('file', file)
	// Дополнительные поля формы — язык записи. Пустые не отправляем: сервер
	// отличает «не задано» от «задано пустым».
	if (fields) {
		for (const [key, value] of Object.entries(fields)) {
			if (value) form.append(key, value)
		}
	}

	const controller = new AbortController()
	const timer = setTimeout(() => controller.abort(), timeoutMs)
	// Экран может передумать ждать: ученик перезаписал фразу, и прежний
	// запрос не должен висеть. Обрывается только ожидание.
	const onAbort = () => controller.abort()
	signal?.addEventListener('abort', onAbort, { once: true })
	if (signal?.aborted) onAbort()
	try {
		const res = await fetch(`${BASE}${path}`, {
			method: 'POST',
			credentials: 'include',
			// Content-Type не задаётся и здесь: границу формы ставит браузер.
			headers,
			body: form,
			signal: controller.signal,
		})
		const payload = await res.json().catch(() => null)
		if (!res.ok) {
			const e = payload?.error || {}
			throw new ApiError({
				status: res.status, code: e.code, message: e.message,
				fields: e.fields, requestId: e.request_id || res.headers.get('X-Request-ID'),
			})
		}
		return payload
	} catch (err) {
		if (err instanceof ApiError) throw err
		if (err.name === 'AbortError') {
			throw new ApiError({ status: 0, code: 'timeout', message: 'Загрузка не уложилась во время' })
		}
		throw new ApiError({ status: 0, code: 'network', message: 'Нет связи с сервером' })
	} finally {
		clearTimeout(timer)
		signal?.removeEventListener('abort', onAbort)
	}
}

/* ---------------------------------------------------------------------------
 * Каталог исходных материалов
 * ------------------------------------------------------------------------- */

/** Состояние сервиса: база и пулы моделей. */
export const health = () => fetch('/health').then((r) => r.json())

export default { auth, profile, classes, student, lessons, assignments, my, speech, learning, health, ApiError }
