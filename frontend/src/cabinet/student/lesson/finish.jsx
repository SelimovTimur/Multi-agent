// finish.jsx — итог выдачи: «Почти всё!» и, после сохранения, звёзды этой
// выдачи, счёт, лучшая серия и ошибки (docs/product/student-screens/story.md,
// JOB-10).

import { Button } from '../../ui'
import Avatar from '../Avatar'
import { SECTION_LABEL, tasksWord } from './kit'
import { MistakesButton, StarsGrid } from './parts'

const SAVE = { class: '✓ Завершить задания', home: '✓ Сдать домашнюю работу', '': '✓ Завершить' }
const DONE_TITLE = { class: 'Все задания урока сделаны', home: 'Домашняя работа сделана', '': 'Все задания сделаны' }

/**
 * IssueFinish — итог выдачи.
 *
 * almost — ученик только что сделал последнее задание: сначала «Почти
 * всё!» и кнопка сохранить, потом итог с конфетти. Ответы и отметки к этому
 * моменту уже записаны — кнопка перечитывает выдачу и показывает итог.
 * Открывший итог заново (перезагрузка, возврат) видит его сразу.
 *
 * issue — выдача ученика из «Результатов урока» (звёзды и счёт); null —
 * ещё не пришла или в разделе её нет. party — номер залпа: звёзды
 * загораются по очереди только после сохранения. onLeave — «К моим
 * занятиям»: перед уходом экран урока открывает опрос.
 */
export function IssueFinish({
	section, title, total, done, issue, loading, almost, saving, onConfirm, party, mistakes, onMistakes, best, onLeave,
}) {
	const label = SECTION_LABEL[section]
	if (almost) {
		return (
			<div className='lk-finish'>
				<Avatar mood='finish' size={140} className='lk-avatar' />
				<h2 className='h2 mt16'>Почти всё!</h2>
				<div className='lk-sub'>
					{label ? `${label}: сделано` : 'Сделано'} {done} из {total}. Сохрани — учитель увидит результат.
				</div>
				<div className='lk-actions'>
					<Button variant='primary' disabled={saving} onClick={onConfirm}>
						{saving ? 'Сохраняем…' : SAVE[section] || SAVE['']}
					</Button>
				</div>
			</div>
		)
	}

	const items = issue?.items || 0
	return (
		<div className='lk-finish'>
			<h2 className='h2'>{DONE_TITLE[section] || DONE_TITLE['']} 🎉</h2>
			<div className='lk-sub'>
				{[title, label?.toLowerCase(), `${tasksWord(done)} из ${total}`].filter(Boolean).join(' · ')}
			</div>
			{issue && <StarsGrid issue={issue} animate={party > 0} />}
			{!issue && loading && <div className='lk-sub'>Считаем звёзды…</div>}
			{items > 0 && (
				<div className='lk-stats'>
					<div className='lk-stat'>
						<b>
							{issue.correct} / {items}
						</b>
						<span>верных ответов</span>
					</div>
					<div className='lk-stat'>
						<b>🔥 {Math.max(issue.best_streak || 0, best || 0)}</b>
						<span>лучшая серия</span>
					</div>
				</div>
			)}
			{(items > 0 || mistakes > 0) && <MistakesButton count={mistakes} onOpen={onMistakes} />}
			{issue && <div className='lk-note'>Звёзды за эту выдачу. Все звёзды урока — в «Завершённых».</div>}
			<div className='lk-actions'>
				{/* Через onLeave: уходя с итога, ученик попадает в опрос. */}
				<Button variant='primary' onClick={onLeave}>
					К моим занятиям
				</Button>
			</div>
		</div>
	)
}
