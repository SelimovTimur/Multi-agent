import assert from 'node:assert/strict'
import { test } from 'node:test'
import { ApiError, assignments, auth } from '../src/api/v1.js'

test('отмена запроса действует после получения заголовков', async () => {
	const original = globalThis.fetch
	const controller = new AbortController()
	let reading
	const started = new Promise(resolve => { reading = resolve })
	globalThis.fetch = async (_url, { signal }) => ({ ok: true, status: 200, headers: new Headers(),
		json: () => new Promise((resolve, reject) => {
			reading()
			signal.addEventListener('abort', () => reject(new DOMException('Отмена', 'AbortError')), { once: true })
		}) })
	try {
		const request = assignments.live('test', { signal: controller.signal })
		await started
		controller.abort()
		await assert.rejects(request, e => e.name === 'AbortError')
	} finally { globalThis.fetch = original }
})

test('таймаут включает чтение тела ответа', async () => {
	const original = globalThis.fetch
	globalThis.fetch = async (_url, { signal }) => ({ ok: true, status: 200, headers: new Headers(),
		json: () => new Promise((resolve, reject) => {
			signal.addEventListener('abort', () => reject(new DOMException('Отмена', 'AbortError')), { once: true })
		}) })
	try {
		await assert.rejects(assignments.live('test', { timeoutMs: 20 }), e => e instanceof ApiError && e.code === 'timeout')
	} finally { globalThis.fetch = original }
})

test('неполный успешный JSON возвращает явный отказ', async () => {
	const original = globalThis.fetch
	globalThis.fetch = async () => new Response('{"user":', { headers: { 'X-Request-ID': 'test-request' } })
	try {
		await assert.rejects(auth.status(), e => e instanceof ApiError && e.code === 'invalid_response' && e.requestId === 'test-request')
	} finally { globalThis.fetch = original }
})

test('HTTP-ошибка сохраняет конверт и идентификатор запроса', async () => {
	const original = globalThis.fetch
	globalThis.fetch = async () => new Response(JSON.stringify({ error: { code: 'not_found', message: 'Урок не найден' } }),
		{ status: 404, headers: { 'X-Request-ID': 'test-error' } })
	try {
		await assert.rejects(auth.status(), e => e instanceof ApiError && e.status === 404 && e.code === 'not_found' && e.requestId === 'test-error')
	} finally { globalThis.fetch = original }
})
