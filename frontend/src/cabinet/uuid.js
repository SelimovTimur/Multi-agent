// uuid.js — случайный UUID v4 в любом контексте страницы.
//
// crypto.randomUUID есть только в защищённом контексте, а сервис может
// работать и по http: там он undefined, и отправка реплики ученика падала бы
// на первом же вызове. Запасной путь — UUID v4 из getRandomValues, который
// доступен везде.

export function randomId(c = globalThis.crypto) {
	if (typeof c?.randomUUID === 'function') {
		try {
			return c.randomUUID()
		} catch {
			// Небезопасный контекст в старом браузере — собираем сами.
		}
	}
	const bytes = new Uint8Array(16)
	if (typeof c?.getRandomValues === 'function') c.getRandomValues(bytes)
	else for (let i = 0; i < 16; i++) bytes[i] = Math.floor(Math.random() * 256)
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	const hex = [...bytes].map((b) => b.toString(16).padStart(2, '0')).join('')
	return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`
}
