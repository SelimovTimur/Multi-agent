// sample.js — образец ответов ручек отчёта по разговору с аватаром.
//
// Формы — ровно из docs/product/avatar-goals/contract.md (раздел 6
// «Учитель»). Нужен проверкам: scripts/test-conversation-report.mjs считает
// по нему подписи и группировки, scripts/smoke-screens.jsx рисует экраны
// отчёта. Приложение этот модуль не импортирует, в сборку он не попадает.

const aidar = { id: 's1', name: 'Айдар', tag: 'KZ4F' }
const gulnara = { id: 's2', name: 'Гөлнара', tag: 'M2QX' }
const ilnar = { id: 's3', name: 'Илнар', tag: 'T7RB' }
const kamila = { id: 's4', name: 'Камилә', tag: 'P9DL' }
const leysan = { id: 's5', name: 'Ләйсән', tag: 'W3HN' }

export const sampleGoals = [
	{ key: 'p1', kind: 'phrase', title: 'Минем исемем …', note: 'представиться, назвать своё имя' },
	{ key: 'p2', kind: 'phrase', title: 'Мин … яшим', note: 'сказать, где живёшь' },
	{ key: 'p3', kind: 'phrase', title: 'Миңа … ошый', note: 'сказать, что нравится' },
	{ key: 'g1', kind: 'grammar', title: 'Местный падеж -да/-дә', note: 'где находится человек или предмет' },
]

/** GET /assignments/{id}/conversation */
export const sampleClassReport = {
	enabled: true,
	level: 'a2',
	max_turns: 8,
	goals: sampleGoals,
	counts: { not_started: 1, in_progress: 1, completed: 3 },
	students: [
		{ student: aidar, status: 'completed', turns_taken: 8, report_status: 'ready', credited: 3, goals_total: 4, errors: 2, misses: 1 },
		{ student: gulnara, status: 'completed', turns_taken: 8, report_status: 'pending', credited: 0, goals_total: 4, errors: 0, misses: 0 },
		{ student: ilnar, status: 'in_progress', turns_taken: 3, report_status: '', credited: 0, goals_total: 4, errors: 0, misses: 0 },
		{ student: kamila, status: 'not_started', turns_taken: 0, report_status: '', credited: 0, goals_total: 4, errors: 0, misses: 0 },
		{ student: leysan, status: 'completed', turns_taken: 8, report_status: 'ready', credited: 2, goals_total: 4, errors: 1, misses: 2 },
	],
	themes: [
		{
			key: 'p1', kind: 'phrase', title: 'Минем исемем …', correct_uses: 3, errors: 0, credited_students: 2,
			students: [
				{ student: aidar, correct_uses: 2, errors: 0, misses: 0 },
				{ student: leysan, correct_uses: 1, errors: 0, misses: 0 },
			],
		},
		{
			key: 'p2', kind: 'phrase', title: 'Мин … яшим', correct_uses: 1, errors: 1, credited_students: 1,
			students: [
				{ student: aidar, correct_uses: 1, errors: 0, misses: 0 },
				{ student: leysan, correct_uses: 0, errors: 1, misses: 1 },
			],
		},
		{
			key: 'p3', kind: 'phrase', title: 'Миңа … ошый', correct_uses: 1, errors: 0, credited_students: 1,
			students: [
				{ student: aidar, correct_uses: 0, errors: 0, misses: 1 },
				{ student: leysan, correct_uses: 1, errors: 0, misses: 0 },
			],
		},
		{
			key: 'g1', kind: 'grammar', title: 'Местный падеж -да/-дә', correct_uses: 1, errors: 2, credited_students: 1,
			students: [
				{ student: aidar, correct_uses: 1, errors: 2, misses: 0 },
				{ student: leysan, correct_uses: 0, errors: 0, misses: 1 },
			],
		},
	],
	common_errors: [
		{
			group_key: 'lex:әти', label: 'әти — папа', type: 'lexical', level: '', count: 1,
			students: [{ student: aidar, quotes: [{ turn_no: 6, quote: 'папка', correct_form: 'әти' }] }],
		},
		{
			group_key: 'izafet', label: 'Притяжательная конструкция -ның/-нең', type: 'grammar', level: 'a1', count: 3,
			students: [
				{ student: aidar, quotes: [{ turn_no: 5, quote: 'дустым исеме', correct_form: 'дустымның исеме' }] },
				{
					student: leysan,
					quotes: [
						{ turn_no: 2, quote: 'әни исеме', correct_form: 'әниемнең исеме' },
						{ turn_no: 6, quote: 'абый машинасы', correct_form: 'абыйның машинасы' },
					],
				},
			],
		},
	],
}

const turns = [
	{ turn_no: 0, role: 'avatar', content: 'Исәнме! Синең исемең ничек?', translation: 'Привет! Как тебя зовут?', input: 'text', edited: false, asr_text: null },
	{ turn_no: 1, role: 'student', content: 'Исәнме! Минем исемем Айдар.', translation: '', input: 'voice', edited: false, asr_text: 'Исәнме! Минем исемем Айдар.' },
	{ turn_no: 1, role: 'avatar', content: 'Бик матур исем! Син кайда яшисең?', translation: 'Очень красивое имя! Где ты живёшь?', input: 'text', edited: false, asr_text: null },
	{ turn_no: 2, role: 'student', content: 'Мин Казанда яшим.', translation: '', input: 'voice', edited: true, asr_text: 'Мин Казанга яшим.' },
	{ turn_no: 2, role: 'avatar', content: 'Казан — матур шәһәр. Сиңа нәрсә ошый?', translation: 'Казань — красивый город. Что тебе нравится?', input: 'text', edited: false, asr_text: null },
	{ turn_no: 3, role: 'student', content: 'Мин футбол яратам.', translation: '', input: 'voice', edited: false, asr_text: 'Мин футбол яратам.' },
	{ turn_no: 3, role: 'avatar', content: 'Футбол — кызык! Син кайда укыйсың?', translation: 'Футбол — это интересно! Где ты учишься?', input: 'text', edited: false, asr_text: null },
	{ turn_no: 4, role: 'student', content: 'Мин мәктәпкә укыйм.', translation: '', input: 'voice', edited: false, asr_text: 'Мин мәктәпкә укыйм.' },
	{ turn_no: 4, role: 'avatar', content: 'Мәктәптә укыйсың, яхшы. Дустың бармы?', translation: 'Учишься в школе, хорошо. У тебя есть друг?', input: 'text', edited: false, asr_text: null },
	{ turn_no: 5, role: 'student', content: 'Әйе, минем дустым исеме Тимур.', translation: '', input: 'voice', edited: false, asr_text: 'Әйе, минем дустым исеме Тимур.' },
	{ turn_no: 5, role: 'avatar', content: 'Сез Тимур белән нәрсә эшлисез?', translation: 'Что вы делаете с Тимуром?', input: 'text', edited: false, asr_text: null },
	{ turn_no: 6, role: 'student', content: 'Без папка белән футбол уйныйбыз.', translation: '', input: 'voice', edited: false, asr_text: 'Без папка белән футбол уйныйбыз.' },
	{ turn_no: 6, role: 'avatar', content: 'Әтиең белән уйныйсызмы? Ә кем син, кайда яшисең?', translation: 'Играете с папой? А кто ты и где живёшь?', input: 'text', edited: false, asr_text: null },
	{ turn_no: 7, role: 'student', content: 'Минем исемем Айдар, мин Казан яшим.', translation: '', input: 'voice', edited: false, asr_text: 'Минем исемем Айдар, мин Казан яшим.' },
	{ turn_no: 7, role: 'avatar', content: 'Казанда яшисең! Рәхмәт, Айдар.', translation: 'Живёшь в Казани! Спасибо, Айдар.', input: 'text', edited: false, asr_text: null },
	{ turn_no: 8, role: 'student', content: 'Рәхмәт, сау бул!', translation: '', input: 'text', edited: false, asr_text: null },
	{ turn_no: 8, role: 'avatar', content: 'Сау бул, Айдар! Сөйләшүең өчен рәхмәт.', translation: 'До свидания, Айдар! Спасибо за разговор.', input: 'text', edited: false, asr_text: null },
]

/** GET /assignments/{id}/students/{studentId}/conversation — готовый отчёт. */
export const sampleStudentConversation = {
	student: aidar,
	status: 'completed',
	turns_taken: 8,
	max_turns: 8,
	report_status: 'ready',
	report: {
		closed_at: '2026-10-03T14:20:00+03:00',
		level: 'a2',
		goals: [
			{ ...sampleGoals[0], credited: true, credited_turn: 1, correct_uses: 2, errors: 0, misses: 0 },
			{ ...sampleGoals[1], credited: true, credited_turn: 2, correct_uses: 1, errors: 0, misses: 0 },
			{ ...sampleGoals[2], credited: false, credited_turn: null, correct_uses: 0, errors: 0, misses: 1 },
			{ ...sampleGoals[3], credited: true, credited_turn: 2, correct_uses: 1, errors: 2, misses: 0 },
		],
		turns,
		marks: [
			{ turn_no: 1, goal_key: 'p1', verdict: 'credited', error_kind: null, quote: 'Минем исемем Айдар', correct_form: '', note_ru: '' },
			{ turn_no: 2, goal_key: 'p2', verdict: 'credited', error_kind: null, quote: 'Мин Казанда яшим', correct_form: '', note_ru: '' },
			{ turn_no: 2, goal_key: 'g1', verdict: 'credited', error_kind: null, quote: 'Казанда', correct_form: '', note_ru: '' },
			{ turn_no: 3, goal_key: 'p3', verdict: 'miss', error_kind: null, quote: 'Мин футбол яратам.', correct_form: '', note_ru: '' },
			{ turn_no: 4, goal_key: 'g1', verdict: 'error', error_kind: 'wrong_form', quote: 'мәктәпкә', correct_form: 'мәктәптә', note_ru: 'Где учишься — местный падеж' },
			{ turn_no: 7, goal_key: 'p1', verdict: 'credited', error_kind: null, quote: 'Минем исемем Айдар', correct_form: '', note_ru: '' },
			{ turn_no: 7, goal_key: 'g1', verdict: 'error', error_kind: 'not_used', quote: 'Казан яшим', correct_form: 'Казанда яшим', note_ru: '' },
		],
		general_errors: [
			{
				turn_no: 5, quote: 'дустым исеме', type: 'grammar', correct_form: 'дустымның исеме',
				rule_ru: 'Чей-то предмет: родительный падеж -ның/-нең и окончание принадлежности',
				level: 'a1', level_source: 'A1 · Морфология: притяжательная конструкция', visibility: 'main',
				group_key: 'izafet', group_label: 'Притяжательная конструкция -ның/-нең',
			},
			{
				turn_no: 6, quote: 'папка', type: 'lexical', correct_form: 'әти', rule_ru: '', level: '',
				level_source: '', visibility: 'unknown_level', group_key: 'lex:әти', group_label: 'әти — папа',
			},
		],
		above_level_count: 2,
		speech_level: { estimated: 'a1', shares: { a1: 0.8, a2: 0.1, b1: 0, b2: 0, c1: 0 }, unknown_share: 0.1 },
		incidents: [
			{ kind: 'asr', turn_no: 2, message: 'Распознавание не ответило' },
			{ kind: 'tts', turn_no: 5, message: '' },
		],
	},
}

/** Тот же ученик, пока разговор идёт: отчёта нет. */
export const sampleStudentInProgress = {
	student: ilnar,
	status: 'in_progress',
	turns_taken: 3,
	max_turns: 8,
	report_status: '',
	report: null,
}

/** Разговор завершён, разбор ещё готовится. */
export const sampleStudentPending = {
	student: gulnara,
	status: 'completed',
	turns_taken: 8,
	max_turns: 8,
	report_status: 'pending',
	report: null,
}
