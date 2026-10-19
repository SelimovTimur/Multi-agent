// parts.jsx — части экрана разговора с аватаром.
//
// Только разметка: состояние и действия приходят параметрами. Так каждую
// часть можно отрисовать в smoke-screens без сервера и без браузера, а
// логика остаётся в state.js и в самом экране.
//
// Форма — по макетам «Экраны/» (доступ к микрофону), вид — дизайн-система
// кабинета. Капс макета («АВАТАР», «ПОПРОБОВАТЬ СНОВА») намеренно не
// перенесён. Голос — только голосовой разговор (GPT Live).

import { useEffect, useRef, useState } from 'react'
import Icon from '../../icons'
import { Button, LetterBar, Modal } from '../../ui'
import { usePolite, useFormal } from './address.js'
import { liveReviewHint } from './live.js'
import { formatClock, incidentLine, langTag, MAX_ANSWER_CHARS, onLanguage, pluralWord } from './state.js'

/* ---------------------------------------------------------------------------
 * Переписка
 * ------------------------------------------------------------------------- */

/**
 * VoiceMessage — реплика аватара голосовым сообщением.
 *
 * Текста по умолчанию нет: ученик учится понимать на слух (Уч-1). «Подсказка»
 * открывает текст и перевод сразу — они уже на клиенте — и не трогает звук.
 * Сломалась озвучка — текст открыт экраном, а здесь сказано почему.
 */
export function VoiceMessage({
	turn,
	hint = false,
	status = '',
	playing = false,
	time = 0,
	duration = 0,
	language = '',
	disabled = false,
	onToggle,
	onRepeat,
	onHint,
}) {
	const loading = status === 'loading'
	const failed = status === 'failed'
	const share = duration > 0 ? Math.min(100, (time / duration) * 100) : 0
	let clock = formatClock(time)
	if (loading) clock = 'Готовим озвучку…'
	else if (failed) clock = 'Озвучка не получилась'
	else if (duration > 0) clock = `${formatClock(time)} / ${formatClock(duration)}`

	return (
		<div className='bubble bubble-avatar conv-voice'>
			<div className='conv-player'>
				<button
					type='button'
					className='conv-play'
					onClick={onToggle}
					disabled={disabled || loading}
					aria-label={playing ? 'Пауза' : 'Слушать реплику'}
					title={disabled ? 'Во время голосового разговора звук выключен' : undefined}
				>
					{loading ? <span className='spinner' aria-hidden='true' /> : <Icon name={playing ? 'pause' : 'play'} size={18} />}
				</button>
				<div className='conv-track'>
					<div className='conv-bar' aria-hidden='true'>
						<i style={{ width: `${share}%` }} />
					</div>
					<div className='conv-time'>{clock}</div>
				</div>
			</div>
			<div className='conv-tools'>
				{/* Имя для экранного чтеца длиннее надписи: «Повторить» на экране
				    бывает и у отправки ответа после сбоя, и путать их нельзя. */}
				<button
					type='button'
					className='conv-tool'
					onClick={onRepeat}
					disabled={disabled || loading}
					aria-label='Повторить реплику'
				>
					<Icon name='refresh' size={14} />
					Повторить
				</button>
				<button type='button' className='conv-tool' onClick={onHint} aria-expanded={hint}>
					<Icon name='eye' size={14} />
					{hint ? 'Скрыть подсказку' : 'Подсказка'}
				</button>
			</div>
			{failed && (
				<div className='conv-voice-note' role='status'>
					Не получилось озвучить реплику — вот её текст.
				</div>
			)}
			{hint && (
				<div className='conv-hint'>
					<p lang={langTag(language)}>{turn.content}</p>
					{turn.translation && <p className='conv-hint-tr'>{turn.translation}</p>}
				</div>
			)}
		</div>
	)
}

const STUDENT_STATUS = {
	sending: 'Отправляем…',
	saved: 'Ответ сохранён',
	unsent: 'Не отправлено',
}

/** StudentMessage — ответ ученика текстом: так он видит, что ушло в разговор. */
export function StudentMessage({ turn }) {
	return (
		<>
			<div className={`bubble bubble-student${turn.local ? ' conv-pending' : ''}`}>{turn.content}</div>
			{turn.local && (
				<div className={`conv-status${turn.local === 'unsent' ? ' conv-status-err' : ''}`}>
					{STUDENT_STATUS[turn.local] || ''}
				</div>
			)}
		</>
	)
}

/* ---------------------------------------------------------------------------
 * Начало
 * ------------------------------------------------------------------------- */

/**
 * Intro — до начала разговора. live — голосовой разговор (одна сессия,
 * микрофон по кнопке), иначе ввод текстом. onSwitch — «Голосовой разговор»
 * из ввода текстом, если голосовой доступен. Из голосового к тексту ученик
 * сам не переходит: текст — запасной путь, когда голоса нет. unavailable — плашка «Голосовой разговор недоступен».
 */
export function Intro({ maxTurns = 0, language = '', starting = false, error = '', live = false, unavailable = null, onStart, onSwitch }) {
	// Учитель в пробном разговоре: тот же разговор, но рассказ — о нём, на «вы».
	const formal = useFormal()
	let text
	if (formal) {
		text = (
			<p>
				Аватар заговорит с вами голосом {onLanguage(language)} так же, как с учеником после урока: с той же
				ролью, целями и длиной разговора.{' '}
				{live
					? 'Сначала послушайте вопрос. Потом нажмите на микрофон, ответьте и нажмите ещё раз — аватар ответит. Не поняли вопрос — нажмите «Подсказка».'
					: 'Отвечайте текстом. Не поняли вопрос — нажмите «Подсказка».'}
			</p>
		)
	} else if (live) {
		text = (
			<p>
				Аватар задаст тебе несколько вопросов {onLanguage(language)} о том, что ты только что прошёл. Сначала
				послушай вопрос. Потом нажми на микрофон, ответь и нажми ещё раз — аватар ответит. Не понял вопрос —
				нажми «Подсказка»: появятся текст и перевод.
			</p>
		)
	} else {
		text = (
			<p>
				Аватар задаст тебе несколько вопросов {onLanguage(language)} о том, что ты только что прошёл. Слушай и
				отвечай текстом. Не понял вопрос — нажми «Подсказка»: появятся текст и перевод.
			</p>
		)
	}
	return (
		<div className='conv-intro'>
			{text}
			{/* Длину задал учитель — отдельной строкой, чтобы ученик знал, сколько отвечать. */}
			{maxTurns > 0 && (
				<div className='notice notice-green conv-intro-length'>
					<Icon name='bubble' size={18} />
					<span>
						{formal ? 'Вам' : 'Тебе'} нужно ответить <b>{`${maxTurns} ${pluralWord(maxTurns, 'раз', 'раза', 'раз')}`}</b>
					</span>
				</div>
			)}
			{unavailable}
			{error && (
				<p className='err' role='alert'>
					{error}
				</p>
			)}
			<div className='conv-intro-acts'>
				<Button variant='primary' icon={live ? 'mic' : 'bubble'} onClick={onStart} disabled={starting}>
					{starting ? 'Начинаем…' : 'Начать разговор'}
				</Button>
				{onSwitch && !live && (
					<Button variant='ghost' icon='mic' onClick={onSwitch} disabled={starting}>
						Голосовой разговор
					</Button>
				)}
			</div>
		</div>
	)
}

/**
 * TurnsHint — подсказка под «Осталось N ответов» на первых заходах:
 * стрелочка вверх, на счётчик, и что он значит. onClose — «Понятно».
 */
export function TurnsHint({ onClose }) {
	return (
		<div className='conv-turns-hint' role='note'>
			<span>
				Столько раз тебе ещё нужно ответить аватару. Число уменьшается после каждого ответа. Когда дойдёт до
				нуля, аватар попрощается и разговор закончится.
			</span>
			<button type='button' className='conv-turns-hint-ok' onClick={onClose}>
				Понятно
			</button>
		</div>
	)
}

/**
 * LiveUnavailable — голосового разговора нет: сервер не дал, браузер не
 * умеет, не соединилось или нет микрофона. reason — почему, одной строкой.
 * «Повторить» пробует соединиться снова; пока — ввод текстом под плашкой.
 * Без onRetry (сайт по http, браузер не умеет) кнопки нет: повторять
 * нечего. secureUrl — тот же адрес по https, если дело в адресе.
 */
export function LiveUnavailable({ reason = '', busy = false, retrying = false, onRetry, secureUrl = '' }) {
	const t = usePolite()
	return (
		<div className='notice notice-warn conv-live-off' role='status'>
			<div className='conv-live-off-text'>
				<b>Голосовой разговор недоступен</b>
				<span>
					{reason ? `${reason} ` : ''}
					{onRetry ? t('Пока отвечай текстом — «Повторить» попробует соединиться снова.') : t('Отвечай текстом.')}
				</span>
			</div>
			{onRetry && (
				<Button icon='refresh' onClick={onRetry} disabled={busy} aria-label='Повторить голосовой разговор'>
					{retrying ? 'Соединяем…' : 'Повторить'}
				</Button>
			)}
			{/* Внешний переход на другой протокол — обычная ссылка, а не маршрут. */}
			{secureUrl && (
				<a className='btn btn-outline' href={secureUrl}>
					Открыть по https
				</a>
			)}
		</div>
	)
}

/* ---------------------------------------------------------------------------
 * Панель ответа
 * ------------------------------------------------------------------------- */

/** TextAnswer — ответ текстом с панелью букв языка урока. */
export function TextAnswer({ value = '', language = '', fieldRef, onChange, onSend }) {
	const t = usePolite()
	return (
		<div className='conv-text'>
			<LetterBar targetRef={fieldRef} value={value} onChange={onChange} lang={language} />
			<div className='chat-input'>
				<textarea
					className='input conv-input'
					ref={fieldRef}
					rows={2}
					value={value}
					maxLength={MAX_ANSWER_CHARS}
					lang={langTag(language)}
					placeholder={t('Напиши ответ')}
					aria-label='Ответ текстом'
					onChange={(e) => onChange(e.target.value)}
					onKeyDown={(e) => {
						if (e.key === 'Enter' && !e.shiftKey && !e.nativeEvent?.isComposing) {
							e.preventDefault()
							onSend()
						}
					}}
				/>
				<Button variant='primary' icon='arrowNext' onClick={onSend} disabled={!value.trim()}>
					Отправить
				</Button>
			</div>
		</div>
	)
}

/** Sending — ответ ушёл, аватар думает. */
export function Sending() {
	return (
		<div className='conv-caption conv-wait' role='status'>
			<span className='spinner' aria-hidden='true' />
			Аватар отвечает…
		</div>
	)
}

/**
 * SendError — ответ сохранён или не дошёл. «Повторить» шлёт тот же ответ с
 * тем же идентификатором: сервер не засчитает его второй раз.
 */
export function SendError({ error, onRetry }) {
	// Сохранённый ответ после перезагрузки — не ошибка, а остановка на полпути.
	const quiet = error?.kind === 'resume' || error?.kind === 'unsent'
	const t = usePolite()
	return (
		<div className={`notice ${quiet ? 'notice-warn' : 'notice-err'} conv-send-err`} role='alert'>
			<span>{t(error?.message)}</span>
			<Button variant='primary' icon='refresh' onClick={onRetry}>
				Повторить
			</Button>
		</div>
	)
}

/* ---------------------------------------------------------------------------
 * Доступ к микрофону
 * ------------------------------------------------------------------------- */

const MIC_TEXT = {
	ask: (language) => `Разреши доступ к микрофону, чтобы разговаривать с аватаром ${onLanguage(language)}.`,
	denied: () =>
		'Браузер не даёт доступ к микрофону. Разреши его в настройках сайта — значок слева от адреса — и попробуй снова. Или отвечай текстом.',
	missing: () => 'Микрофон не найден. Подключи его и попробуй снова — или отвечай текстом.',
	failed: () => 'Не удалось включить микрофон. Попробуй снова или отвечай текстом.',
}

/**
 * MicAccess — окно до системного запроса микрофона (макет «Доступ к
 * микрофону»). «Не сейчас» — ввод текстом; закрытие крестиком просто
 * закрывает окно. onClose должен быть стабильным: Modal переводит фокус
 * внутрь окна каждый раз, когда он меняется.
 */
export function MicAccess({ kind = 'ask', language = '', onAllow, onLater, onClose }) {
	const ask = kind === 'ask'
	const t = usePolite()
	const formal = useFormal()
	let text = t((MIC_TEXT[kind] || MIC_TEXT.failed)(language))
	if (formal && ask) text = `Разрешите доступ к микрофону, чтобы разговаривать с аватаром ${onLanguage(language)}.`
	return (
		<Modal
			title='Доступ к микрофону'
			onClose={onClose}
			footer={
				<>
					<Button variant='ghost' onClick={onLater}>
						{ask ? 'Не сейчас' : 'Ответить текстом'}
					</Button>
					<Button variant='primary' icon='mic' onClick={onAllow}>
						{ask ? 'Разрешить доступ' : 'Попробовать снова'}
					</Button>
				</>
			}
		>
			<div className='conv-mic'>
				<span className='auth-icon' aria-hidden='true'>
					<Icon name='mic' size={30} />
				</span>
				<p className='conv-mic-text'>{text}</p>
			</div>
		</Modal>
	)
}

/* ---------------------------------------------------------------------------
 * Конец разговора
 * ------------------------------------------------------------------------- */

/** Closing — разговор закрыт, итог готовится. slow — опрос дошёл до предела. */
export function Closing({ slow = false, failed = false, onRetry, onBack, backLabel = 'К заданиям' }) {
	let note = 'Обычно это занимает меньше минуты.'
	if (failed) note = 'Итог задерживается — пробуем ещё раз.'
	if (slow) note = 'Итог готовится дольше обычного. Можно вернуться к заданиям: итог сохранится, и его можно будет открыть здесь позже.'
	return (
		<section className='card card-white conv-closing' role='status'>
			<div className='row'>
				{!slow && <span className='spinner' aria-hidden='true' />}
				<b>Разговор завершён. Итог готовится…</b>
			</div>
			<p className='small muted'>{note}</p>
			{slow && (
				<div className='row'>
					<Button variant='primary' icon='refresh' onClick={onRetry}>
						Проверить ещё раз
					</Button>
					<Button onClick={onBack}>{backLabel}</Button>
				</div>
			)}
		</section>
	)
}

/**
 * Summary — итог ученика (Уч-5).
 *
 * Что получилось и одна вещь на следующий раз. Списка ошибок здесь нет
 * намеренно: он расстраивает и ничему не учит. Технические сбои — отдельной
 * строкой, чтобы ученик не принял их на свой счёт.
 */
export function Summary({ data, language = '', onBack, backLabel = 'К заданиям', extra = null }) {
	const incidents = incidentLine(data.incidents)
	const t = usePolite()
	return (
		<section className='card card-white conv-summary' aria-label='Итог разговора'>
			<h2 className='h2'>Итог разговора</h2>
			{data.phrasesTotal > 0 && (
				<div className='conv-sum-block'>
					<div className='h3'>{`Зачтено фраз: ${data.credited.length} из ${data.phrasesTotal}`}</div>
					{data.credited.length ? (
						<ul className='sum-list'>
							{data.credited.map((title, i) => (
								<li className='sum-item' key={i}>
									<Icon name='check' size={16} />
									<span lang={langTag(language)}>{title}</span>
								</li>
							))}
						</ul>
					) : (
						<p className='small muted'>Пока ни одной — в следующий раз получится.</p>
					)}
				</div>
			)}
			{data.themesTotal > 0 && (
				<div className='conv-sum-block'>
					<div className='h3'>{`Применено тем: ${data.applied.length} из ${data.themesTotal}`}</div>
					{data.applied.length ? (
						<ul className='sum-list'>
							{data.applied.map((title, i) => (
								<li className='sum-item' key={i}>
									<Icon name='check' size={16} />
									<span>{title}</span>
								</li>
							))}
						</ul>
					) : (
						<p className='small muted'>{t('Пока ни одной — попробуй в следующий раз.')}</p>
					)}
				</div>
			)}
			{data.nextTime && (
				<div className='conv-sum-block conv-next'>
					<div className='h3'>В следующий раз</div>
					{data.nextTime.text && <p>{data.nextTime.text}</p>}
					{data.nextTime.example && (
						<p className='conv-example'>
							Например: «<span lang={langTag(language)}>{data.nextTime.example}</span>»
						</p>
					)}
				</div>
			)}
			{incidents && <p className='tiny'>Технические сбои: {incidents}. Это не влияет на оценку.</p>}
			<div className='conv-sum-acts'>
				<Button variant='primary' icon='check' onClick={onBack}>
					{backLabel}
				</Button>
				{extra}
			</div>
		</section>
	)
}

/* ---------------------------------------------------------------------------
 * Живой режим: одна сессия GPT Live, микрофон по кнопке
 * ------------------------------------------------------------------------- */

/**
 * MicLevel — микрофон ученика: кольцо дышит в такт голосу.
 *
 * Громкость читается сама, раз в 100 мс, и перерисовывается только это
 * кольцо, а не вся переписка. getLevel должен быть стабильным. С onToggle
 * это кнопка «говорю»: open — микрофон включён.
 */
export function MicLevel({ getLevel, hearing = false, quiet = false, open = false, disabled = false, onToggle }) {
	const [level, setLevel] = useState(0)
	useEffect(() => {
		if (!getLevel) return undefined
		const timer = setInterval(() => setLevel(getLevel() || 0), 100)
		return () => clearInterval(timer)
	}, [getLevel])
	// RMS в долю: тишина у нуля, обычная речь — от середины.
	const share = quiet ? 0 : Math.min(1, Math.sqrt(level / 0.2))
	const cls = `conv-live-mic${hearing ? ' is-hearing' : ''}${quiet ? ' is-quiet' : ''}${open ? ' is-open' : ''}`
	const inner = (
		<>
			<span className='conv-live-ring' style={{ '--level': share.toFixed(2) }} />
			<span className='conv-live-dot'>
				<Icon name={open ? 'check' : 'mic'} size={28} />
			</span>
		</>
	)
	if (!onToggle) {
		return (
			<div className={cls} aria-hidden='true'>
				{inner}
			</div>
		)
	}
	return (
		<button
			type='button'
			className={`${cls} conv-live-mic-btn`}
			aria-pressed={open}
			aria-label={open ? 'Закончить ответ и выключить микрофон' : 'Включить микрофон и говорить'}
			disabled={disabled}
			onClick={onToggle}
		>
			{inner}
		</button>
	)
}

const LIVE_CAPTION = {
	connecting: 'Подключаемся…',
	reconnecting: 'Связь прервалась — восстанавливаем…',
	closing: 'Аватар прощается…',
}

/**
 * LivePanel — панель ответа в живом разговоре.
 *
 * Ученик говорит кнопкой: нажал микрофон — говорит, нажал ещё раз — ход
 * окончен, аватар отвечает. Без кнопки аватар не слышит ни ученика, ни
 * класс. Панель показывает, что происходит (звучит аватар, прощание).
 * К вводу текстом ученик сам не переходит. Без сессии (после перезагрузки) — «Продолжить разговор»;
 * resume — что будет с неотправленным ответом.
 */
export function LivePanel({
	status = 'idle',
	talking = false,
	hearing = false,
	mic = false,
	resume = '',
	resuming = false,
	error = '',
	getLevel,
	onStart,
	onCancel,
	onMic,
}) {
	const t = usePolite()
	if (status === 'idle' || status === 'ended') {
		return (
			<div className='conv-stack conv-live'>
				<p className='conv-caption'>
					{t(resume) || t('Разговор на паузе. Нажми «Продолжить разговор» — аватар продолжит с того же места.')}
				</p>
				{error && (
					<p className='err' role='alert'>
						{t(error)}
					</p>
				)}
				<div className='conv-live-acts'>
					<Button variant='primary' icon='mic' onClick={onStart} disabled={resuming}>
						{resuming ? 'Отправляем ответ…' : 'Продолжить разговор'}
					</Button>
				</div>
			</div>
		)
	}
	let caption = LIVE_CAPTION[status]
	if (!caption) {
		if (mic) caption = t('Говори. Когда закончишь, нажми на кнопку ещё раз')
		else caption = talking ? 'Аватар говорит…' : t('Нажми на микрофон и говори')
	}
	return (
		<div className='conv-stack conv-live'>
			<MicLevel
				getLevel={getLevel}
				hearing={hearing}
				quiet={status !== 'active' || !mic}
				open={mic}
				disabled={status !== 'active'}
				onToggle={onMic}
			/>
			<div className='conv-caption' role='status'>
				{caption}
			</div>
			{status === 'connecting' && onCancel && (
				<Button variant='ghost' onClick={onCancel}>
					Отменить
				</Button>
			)}
		</div>
	)
}

const LIVE_STATUS = {
	queued: 'Отправляем…',
	sending: 'Отправляем…',
	unsent: 'Не отправлено',
	late: 'Разговор уже закончился — эта фраза в него не вошла',
}

/**
 * LiveStudentMessage — фраза ученика в живом разговоре.
 *
 * Уверенно распознанная сразу становится ответом. Неуверенная — пузырь с
 * текстом и «Верно» / «Исправить»: разговор при этом идёт, а без ответа
 * ученика текст уходит как есть через 20 секунд. Правка — с панелью букв;
 * «Не отправлять» — фраза не ответ (сказал не ученик или распознался шум).
 */
export function LiveStudentMessage({
	turn,
	language = '',
	fieldRef,
	onConfirm,
	onEdit,
	onDraft,
	onSendEdited,
	onCancelEdit,
	onDiscard,
	onRetry,
}) {
	const live = turn.live || {}
	const stage = live.stage
	const card = useRef(null)
	const asking = stage === 'review' || stage === 'editing'
	const t = usePolite()
	// Просьбу проверить фразу ученик должен увидеть: лента сама доезжает до
	// неё, а в правке — до кнопок под полем, которые иначе остаются за краем.
	// Без плавности: плавную прокрутку обрывает любая следующая (фокус поля).
	useEffect(() => {
		if (asking) card.current?.scrollIntoView?.({ block: 'nearest' })
	}, [asking, stage])
	if (stage === 'hearing') return null
	if (stage === 'recognizing') {
		return (
			<>
				<div className='bubble bubble-student conv-pending bubble-typing' aria-hidden='true'>
					…
				</div>
				<div className='conv-status' role='status'>
					Распознаём фразу…
				</div>
			</>
		)
	}
	if (stage === 'asr_failed') {
		return (
			<div className='conv-status conv-status-err' role='status'>
				{t('Не удалось распознать фразу — скажи её ещё раз')}
			</div>
		)
	}
	if (stage === 'review' || stage === 'editing') {
		const draft = live.draft || ''
		return (
			<section className='conv-live-review' ref={card} aria-label={t('Проверь, что мы услышали')}>
				{stage === 'editing' ? (
					<>
						<LetterBar targetRef={fieldRef} value={draft} onChange={onDraft} lang={language} />
						<textarea
							className='textarea conv-field'
							ref={fieldRef}
							value={draft}
							maxLength={MAX_ANSWER_CHARS}
							lang={langTag(language)}
							aria-label={t('Исправь распознанный текст')}
							onChange={(e) => onDraft(e.target.value)}
							onKeyDown={(e) => {
								if (e.key === 'Enter' && !e.shiftKey && !e.nativeEvent?.isComposing) {
									e.preventDefault()
									if (draft.trim()) onSendEdited()
								}
							}}
							autoFocus
						/>
						<div className='conv-live-review-acts'>
							<Button variant='ghost' onClick={onDiscard}>
								Не отправлять
							</Button>
							<Button variant='ghost' onClick={onCancelEdit}>
								Отмена
							</Button>
							<Button variant='primary' icon='arrowNext' onClick={onSendEdited} disabled={!draft.trim()}>
								Отправить
							</Button>
						</div>
					</>
				) : (
					<>
						<p className='conv-live-text' lang={langTag(language)}>
							{turn.content}
						</p>
						<p className='conv-panel-note'>{t(liveReviewHint(live.asr?.reasons))}</p>
						{/* Полоса тает 20 секунд: столько текст ждёт ученика. Ключ — срок,
						    чтобы после «Отмены» правки отсчёт начался заново. */}
						<div className='conv-live-timer' key={live.deadline || 0} aria-hidden='true'>
							<i />
						</div>
						<div className='conv-live-review-acts'>
							<Button icon='edit' onClick={onEdit}>
								Исправить
							</Button>
							<Button variant='primary' icon='check' onClick={onConfirm}>
								Верно
							</Button>
						</div>
					</>
				)}
			</section>
		)
	}
	const rejected = stage === 'rejected'
	const status = rejected ? `Ответ не принят${live.note ? `: ${live.note}` : ''}` : LIVE_STATUS[stage] || ''
	const error = rejected || stage === 'unsent'
	return (
		<>
			<div className={`bubble bubble-student${stage === 'sent' ? '' : ' conv-pending'}`} lang={langTag(language)}>
				{turn.content}
			</div>
			{status && (
				<div className={`conv-status${error ? ' conv-status-err' : ''}`} role={error ? 'alert' : undefined}>
					{status}
					{stage === 'unsent' && onRetry && (
						<button type='button' className='conv-tool conv-status-act' onClick={onRetry}>
							<Icon name='refresh' size={14} />
							Повторить
						</button>
					)}
				</div>
			)}
		</>
	)
}

/**
 * LiveVoiceMessage — реплика аватара в живом разговоре.
 *
 * Звук идёт из сессии, текста на экране нет (Уч-1). «Подсказка» показывает
 * расшифровку сессии и перевод — он приходит, когда реплика записана.
 * «Повторить» — запись входящего звука этой реплики; не записалась — кнопки
 * нет. Пока звучит аватар, «Повторить» недоступна.
 */
export function LiveVoiceMessage({ turn, hint = false, language = '', replaying = false, disabled = false, onReplay, onHint }) {
	const live = turn.live || {}
	const speaking = live.stage === 'speaking'
	let label = 'Реплика аватара'
	if (speaking) label = 'Аватар говорит…'
	else if (replaying) label = 'Звучит запись реплики'
	let translation = turn.translation
	if (!translation && turn.content && speaking) translation = 'Перевод появится, когда аватар договорит.'
	else if (!translation && turn.content && live.stage === 'done') translation = 'Перевод готовится…'
	return (
		<div className='bubble bubble-avatar conv-voice'>
			<div className='conv-player'>
				<span className={`conv-live-wave${speaking || replaying ? ' is-on' : ''}`} aria-hidden='true'>
					<i />
					<i />
					<i />
					<i />
					<i />
				</span>
				<div className='conv-time'>{label}</div>
			</div>
			<div className='conv-tools'>
				{live.sound && onReplay && (
					<button
						type='button'
						className='conv-tool'
						onClick={onReplay}
						disabled={disabled && !replaying}
						aria-label={replaying ? 'Остановить запись реплики' : 'Повторить реплику'}
						title={disabled && !replaying ? 'Пока говорит аватар, запись не включить' : undefined}
					>
						<Icon name={replaying ? 'pause' : 'refresh'} size={14} />
						{replaying ? 'Остановить' : 'Повторить'}
					</button>
				)}
				<button type='button' className='conv-tool' onClick={onHint} aria-expanded={hint}>
					<Icon name='eye' size={14} />
					{hint ? 'Скрыть подсказку' : 'Подсказка'}
				</button>
			</div>
			{hint && (
				<div className='conv-hint'>
					{turn.content ? (
						<p lang={langTag(language)}>{turn.content}</p>
					) : (
						<p className='conv-hint-tr'>Текст появится, как только аватар начнёт говорить.</p>
					)}
					{translation && <p className='conv-hint-tr'>{translation}</p>}
				</div>
			)}
		</div>
	)
}

/**
 * Draining — после перехода из голосового разговора к вводу текстом: пока
 * дописываются его фразы, новый ответ не начинается, иначе ответы легли бы
 * не по порядку. review — какая-то фраза ждёт проверки.
 */
export function Draining({ review = false }) {
	const t = usePolite()
	return (
		<div className='conv-caption conv-wait' role='status'>
			{!review && <span className='spinner' aria-hidden='true' />}
			{review
				? t('Проверь распознанный текст выше — потом можно отвечать дальше')
				: 'Сохраняем фразы голосового разговора…'}
		</div>
	)
}
