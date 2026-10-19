// Avatar.jsx — аватар, спутник ученика.
//
// Одна картинка на все моменты урока: у прежних картинок подпись была
// вписана в изображение, а значит, на одном языке. Что значит момент,
// говорит alt — для экранного диктора.

const MOODS = {
	read: 'Аватар: давайте почитаем',
	practice: 'Аватар: перейдём к практике',
	correct: 'Аватар: отличный результат',
	next: 'Аватар: классное решение',
	talk: 'Аватар: давайте поговорим по-английски',
	finish: 'Аватар: возвращайтесь скорее',
}

/**
 * Avatar — картинка героя под настроение.
 *
 * `size` в пикселях: герой появляется и крупно на переходах между этапами,
 * и мелко в углу шапки.
 */
export default function Avatar({ mood = 'read', size = 120, className = '' }) {
	return (
		<img
			className={`avatar-pic ${className}`.trim()}
			src='/avatar/wave.png'
			alt={MOODS[mood] || MOODS.read}
			width={size}
			height={size}
			style={{ objectFit: 'contain' }}
		/>
	)
}
