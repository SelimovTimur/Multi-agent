// Отчёт по разговору с аватаром и цели разговора (У-1…У-4).
// Запуск: node scripts/test-conversation-report.mjs
import assert from 'node:assert/strict'
import { test } from 'node:test'
import {
	aboveLevelLabel,
	buildCard,
	closedLabel,
	commonErrorGroups,
	conversationState,
	countsView,
	errorKindLabel,
	generalTypeLabel,
	goalsLine,
	incidentRows,
	levelLabel,
	levelWithSource,
	locate,
	noteFor,
	noteLine,
	readyCount,
	reportState,
	segmentText,
	speechLine,
	statusView,
	studentRows,
	themeGroups,
} from '../src/cabinet/pages/conversation/report.js'
import {
	sampleClassReport,
	sampleStudentConversation,
	sampleStudentInProgress,
	sampleStudentPending,
} from '../src/cabinet/pages/conversation/sample.js'

test('статусы, виды ошибок, типы и уровни называются словами из истории', () => {
	assert.equal(statusView('not_started').label, 'не приступал')
	assert.equal(statusView('in_progress').label, 'разговор не завершён')
	assert.equal(statusView('completed').label, 'завершил')
	assert.equal(statusView('что-то новое').label, 'не приступал')
	assert.equal(errorKindLabel('wrong_form'), 'неверная форма')
	assert.equal(errorKindLabel('misuse'), 'неуместное употребление')
	assert.equal(errorKindLabel('not_used'), 'нужная конструкция не использована')
	assert.equal(errorKindLabel(null), 'ошибка по цели')
	assert.equal(generalTypeLabel('lexical'), 'лексика')
	assert.equal(generalTypeLabel('grammar'), 'грамматика')
	assert.equal(levelLabel('a2'), 'A2')
	assert.equal(levelLabel(''), 'уровень не определён')
	assert.equal(levelLabel(null), 'уровень не определён')
})

test('состояние разговора: отчёт только у завершённого с готовым разбором', () => {
	assert.equal(conversationState('not_started', ''), 'not_started')
	assert.equal(conversationState('in_progress', ''), 'in_progress')
	assert.equal(conversationState('completed', 'ready'), 'ready')
	assert.equal(conversationState('completed', 'pending'), 'pending')
	assert.equal(conversationState('completed', ''), 'pending')
	assert.equal(conversationState('completed', 'failed'), 'failed')
	assert.equal(reportState(null), 'loading')
	assert.equal(reportState(sampleStudentConversation), 'ready')
	assert.equal(reportState(sampleStudentPending), 'pending')
	assert.equal(reportState(sampleStudentInProgress), 'in_progress')
	// Статус ready без самого отчёта — разбор ещё не доехал: ждём, а не рисуем пустоту.
	assert.equal(reportState({ ...sampleStudentPending, report_status: 'ready' }), 'pending')
})

test('по ученикам: числа по целям только у готового отчёта', () => {
	const rows = studentRows(sampleClassReport)
	assert.equal(rows.length, 5)
	const [aidar, gulnara, ilnar, kamila] = rows
	assert.deepEqual(
		[aidar.statusLabel, aidar.answers, aidar.credited, aidar.errors, aidar.misses],
		['завершил', '8 из 8', '3 из 4', 2, 1],
	)
	assert.equal(gulnara.state, 'pending')
	assert.equal(gulnara.credited, '')
	assert.equal(gulnara.errors, null)
	assert.equal(gulnara.note, 'разбор готовится…')
	assert.deepEqual([ilnar.statusLabel, ilnar.answers, ilnar.note], ['разговор не завершён', '3 из 8', ''])
	assert.equal(kamila.statusLabel, 'не приступал')
	assert.equal(kamila.misses, null)
})

test('счётчики класса: из ответа сервера, а без него — по списку учеников', () => {
	assert.deepEqual(
		countsView(sampleClassReport).map((c) => [c.label, c.n]),
		[
			['завершили', 3],
			['разговор не завершён', 1],
			['не приступали', 1],
		],
	)
	const { counts, ...withoutCounts } = sampleClassReport
	assert.ok(counts)
	assert.deepEqual(
		countsView(withoutCounts).map((c) => c.n),
		[3, 1, 1],
	)
	assert.equal(readyCount(sampleClassReport), 2)
	assert.equal(goalsLine(sampleClassReport.goals), '3 фразы и 1 тема')
})

test('по темам: фразы и темы отдельно, зачли k из готовых отчётов, пояснение из целей', () => {
	const groups = themeGroups(sampleClassReport)
	assert.deepEqual(
		groups.map((g) => [g.title, g.items.length]),
		[
			['Фокусные фразы', 3],
			['Грамматические темы', 1],
		],
	)
	const g1 = groups[1].items[0]
	assert.equal(g1.title, 'Местный падеж -да/-дә')
	assert.equal(g1.note, 'где находится человек или предмет')
	assert.deepEqual([g1.correctUses, g1.errors, g1.creditedLabel], [1, 2, '1 из 2'])
	assert.deepEqual(
		[g1.usesText, g1.errorsText, g1.creditedText],
		['1 верное употребление', '2 ошибки', 'зачли 1 из 2 учеников'],
	)
	const p1 = groups[0].items[0]
	assert.deepEqual([p1.usesText, p1.errorsText], ['3 верных употребления', '0 ошибок'])
	assert.deepEqual(
		g1.students.map((s) => [s.name, s.credited, s.errors, s.misses]),
		[
			['Айдар', true, 2, 0],
			['Ләйсән', false, 0, 1],
		],
	)
	// Готовых отчётов ещё нет — цели всё равно видны, с нулями.
	const empty = themeGroups({ ...sampleClassReport, themes: [], students: [] })
	assert.equal(empty[0].items[0].creditedLabel, '0 из 3')
	assert.equal(empty[0].items[0].creditedText, 'зачли 0 из 3 учеников')
	assert.equal(empty[0].items[0].correctUses, 0)
})

test('общие ошибки класса: частые сверху, ученики с цитатами, уровень или «не определён»', () => {
	const groups = commonErrorGroups(sampleClassReport)
	assert.deepEqual(
		groups.map((g) => [g.label, g.countText, g.typeLabel, g.levelLabel]),
		[
			['Притяжательная конструкция -ның/-нең', '3 ошибки у 2 учеников', 'грамматика', 'A1'],
			['әти — папа', '1 ошибка у 1 ученика', 'лексика', 'уровень не определён'],
		],
	)
	assert.equal(groups[1].unknownLevel, true)
	assert.deepEqual(groups[0].students[1].quotes[1], {
		turnNo: 6,
		quote: 'абый машинасы',
		correctForm: 'абыйның машинасы',
	})
	// Счётчик не меньше числа цитат, даже если сервер его не прислал.
	const [noCount] = commonErrorGroups({
		common_errors: [{ ...sampleClassReport.common_errors[1], count: undefined }],
	})
	assert.equal(noCount.count, 3)
})

test('цитата ищется точно, затем без учёта регистра и без многоточия обрезки', () => {
	assert.deepEqual(locate('Мин Казанда яшим.', 'Казанда'), { start: 4, end: 11 })
	assert.deepEqual(locate('Мин Казанда яшим.', 'казанда'), { start: 4, end: 11 })
	assert.deepEqual(locate('Мин футбол яратам, ә син?', 'Мин футбол яратам…'), { start: 0, end: 17 })
	assert.equal(locate('Мин Казанда яшим.', 'Мәскәүдә'), null)
	assert.equal(locate('', 'Казанда'), null)
	assert.equal(locate('Мин Казанда яшим.', ''), null)
})

test('перекрытые цитаты режут ответ на куски, ошибка подсвечена сильнее зачтённого', () => {
	const parts = segmentText('Мин Казанда яшим.', [
		{ id: 'a', start: 0, end: 16, tone: 'credited' },
		{ id: 'b', start: 4, end: 11, tone: 'goal_error' },
	])
	assert.deepEqual(
		parts.map((p) => [p.text, p.ids, p.tone]),
		[
			['Мин ', ['a'], 'credited'],
			['Казанда', ['a', 'b'], 'goal_error'],
			[' яшим', ['a'], 'credited'],
			['.', [], ''],
		],
	)
	assert.equal(parts.map((p) => p.text).join(''), 'Мин Казанда яшим.')
	assert.deepEqual(
		segmentText('Сәлам', []).map((p) => p.text),
		['Сәлам'],
	)
})

test('карточка ученика: отметки всех видов ведут к своему ответу и цитате', () => {
	const card = buildCard(sampleStudentConversation.report)
	assert.equal(card.goalsTotal, 4)
	assert.equal(card.creditedCount, 3)
	assert.equal(card.level, 'A2')

	// Ход за ходом: вступление аватара, ответ 1, реплика 1, ответ 2…
	assert.deepEqual(
		card.turns.slice(0, 4).map((t) => t.label),
		['Аватар', 'Ответ 1', 'Аватар', 'Ответ 2'],
	)

	const { goalErrors, general, misses, repairs, credited } = card.lists
	assert.deepEqual(
		goalErrors.map((n) => [n.turnNo, n.label, n.quote, n.correctForm, n.goalTitle]),
		[
			[4, 'неверная форма', 'мәктәпкә', 'мәктәптә', 'Местный падеж -да/-дә'],
			[7, 'нужная конструкция не использована', 'Казан яшим', 'Казанда яшим', 'Местный падеж -да/-дә'],
		],
	)
	assert.deepEqual(
		general.map((n) => [n.turnNo, n.label, n.level, n.unknownLevel, n.levelText]),
		[
			[5, 'грамматика', 'A1', false, 'уровень A1 · Морфология: притяжательная конструкция'],
			[6, 'лексика', 'уровень не определён', true, 'уровень не определён'],
		],
	)
	assert.deepEqual(
		misses.map((n) => [n.turnNo, n.goalTitle]),
		[[3, 'Миңа … ошый']],
	)
	assert.deepEqual(
		repairs.map((n) => [n.turnNo, n.before, n.after]),
		[[2, 'Мин Казанга яшим.', 'Мин Казанда яшим.']],
	)
	assert.equal(credited.length, 4)

	// Каждая отметка с цитатой нашла её в тексте и ведёт к своему ответу.
	for (const n of card.notes) {
		assert.equal(n.anchor, `cr-answer-${n.turnNo}`)
		if (n.category !== 'repair') assert.equal(n.found, true, n.quote)
	}

	// В ответе 2 зачтены фраза и тема — тема подсвечена внутри фразы.
	const answer2 = card.turns.find((t) => t.anchor === 'cr-answer-2')
	assert.deepEqual(
		answer2.segments.map((s) => [s.text, s.ids.length]),
		[
			['Мин ', 1],
			['Казанда', 2],
			[' яшим', 1],
			['.', 0],
		],
	)
	assert.deepEqual(
		answer2.notes.map((n) => n.category),
		['repair', 'credited', 'credited'],
	)

	assert.equal(card.aboveLevelLabel, 'Ещё 2 ошибки выше уровня урока не показаны')
	assert.deepEqual(
		card.incidents.map((i) => [i.label, i.count]),
		[
			['распознавание речи', 1],
			['озвучка', 1],
		],
	)
	assert.equal(card.speech, 'Речь ученика по справочнику уровней — примерно A1; 10 % слов в справочнике нет')
})

test('строки отметок под ответом и переход из общих ошибок класса', () => {
	const card = buildCard(sampleStudentConversation.report)
	const lines = card.notes.map(noteLine)
	assert.ok(lines.includes('Неверная форма: мәктәпкә → мәктәптә'))
	assert.ok(lines.includes('Грамматика, A1: дустым исеме → дустымның исеме'))
	assert.ok(lines.includes('Лексика, уровень не определён: папка → әти'))
	assert.ok(lines.includes('Промах: Миңа … ошый'))
	assert.ok(lines.includes('Зачтено: Минем исемем …'))
	assert.ok(lines.includes('Исправил распознанный текст'))

	const target = noteFor(card, { turnNo: 6, quote: 'папка' })
	assert.equal(target.category, 'general')
	assert.equal(target.anchor, 'cr-answer-6')
	assert.equal(noteFor(card, { turnNo: 99, quote: 'нет' }), null)
})

test('ошибки выше уровня в основной отчёт не попадают, остаётся только их число', () => {
	const report = {
		...sampleStudentConversation.report,
		general_errors: [
			...sampleStudentConversation.report.general_errors,
			{ turn_no: 5, quote: 'Тимур', type: 'grammar', level: 'b2', visibility: 'above_level' },
		],
		above_level_count: 1,
	}
	const card = buildCard(report)
	assert.equal(card.lists.general.length, 2)
	assert.equal(card.aboveLevelLabel, 'Ещё 1 ошибка выше уровня урока не показана')
	assert.equal(aboveLevelLabel(0), '')
	assert.equal(aboveLevelLabel(5), 'Ещё 5 ошибок выше уровня урока не показаны')
	assert.equal(aboveLevelLabel(21), 'Ещё 21 ошибка выше уровня урока не показана')
})

test('цитата, которой нет в ответе, оставляет отметку в списке и ведёт к ответу целиком', () => {
	const report = {
		...sampleStudentConversation.report,
		marks: [{ turn_no: 3, goal_key: 'p3', verdict: 'miss', quote: 'такого текста нет' }],
		general_errors: [],
	}
	const card = buildCard(report)
	const [miss] = card.lists.misses
	assert.equal(miss.found, false)
	assert.equal(miss.anchor, 'cr-answer-3')
	assert.ok(card.turns.find((t) => t.anchor === 'cr-answer-3').segments.every((s) => s.ids.length === 0))
})

test('счётчики цели считаются по отметкам, если разбор их не прислал', () => {
	const report = {
		...sampleStudentConversation.report,
		goals: sampleStudentConversation.report.goals.map(({ key, kind, title, note }) => ({ key, kind, title, note })),
	}
	const card = buildCard(report)
	const byKey = Object.fromEntries(card.goals.map((g) => [g.key, g]))
	assert.deepEqual([byKey.p1.correctUses, byKey.p1.credited], [2, true])
	assert.deepEqual([byKey.g1.correctUses, byKey.g1.errors], [1, 2])
	assert.deepEqual([byKey.p3.misses, byKey.p3.credited], [1, false])
	assert.equal(card.creditedCount, 3)
})

test('сбои складываются по видам, дата завершения — по местному времени', () => {
	assert.deepEqual(
		incidentRows([{ kind: 'asr' }, { kind: 'asr' }, { kind: 'tts', count: 2 }, { kind: 'llm' }, { kind: 'x' }]).map(
			(i) => [i.label, i.count],
		),
		[
			['распознавание речи', 2],
			['озвучка', 2],
			['ответ аватара', 1],
			['другой сбой', 1],
		],
	)
	assert.equal(closedLabel(new Date(2026, 9, 3, 14, 20).toISOString()), 'разговор завершён 03.10 в 14:20')
	assert.equal(closedLabel(''), '')
	assert.equal(closedLabel('вчера'), '')
	assert.equal(speechLine({ estimated: '' }), '')
	assert.equal(levelWithSource('A2', 'Лексика: ТАНЫШУ'), 'уровень A2 · Лексика: ТАНЫШУ')
	assert.equal(levelWithSource('A2', ''), 'уровень A2')
})
