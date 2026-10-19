// Язык и название продукта. MVP — английский.
const LANGUAGE_CONFIG = {
	brand: 'English', masculine: 'английский', feminine: 'английская',
	feminineOblique: 'английской', plural: 'английские', prepositional: 'английском',
	adverb: 'по-английски', fileSlug: 'english', encouragement: 'Well done!',
}

export const APP_LANGUAGE = 'english'
export const APP_LANGUAGE_CONFIG = LANGUAGE_CONFIG
export const APP_LANGUAGE_BRAND = LANGUAGE_CONFIG.brand
export const APP_TITLE = `На родном. ${APP_LANGUAGE_BRAND}.`

export const APP_AVATAR = {
	displayName: 'Аватар',
	dialogTitle: 'Диалог с аватаром',
	subject: 'аватар',
	imageAlt: 'Аватар урока',
	introTitle: 'Сегодня с тобой говорит аватар',
	connecting: 'Аватар подключается к диалогу...',
	typing: 'Аватар печатает...',
	resultTitle: 'Аватар доволен твоим прогрессом',
	resultImageAlt: 'Аватар на финальном экране',
	feedbackQuestion: 'Как тебе общение с аватаром?',
}
