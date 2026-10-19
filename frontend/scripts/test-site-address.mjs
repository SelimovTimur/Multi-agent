import assert from 'node:assert/strict'
import { test } from 'node:test'
import { readableHost } from '../src/cabinet/site-address.js'

test('кириллический домен прода печатается по-русски', () => {
	assert.equal(readableHost('xn--80aaa7dhb1b.xn--80aiybcecm.xn--p1ai'), 'татарча.народном.рф')
})

test('раскодированный адрес браузер кодирует обратно в тот же домен', () => {
	for (const host of ['xn--80aaa7dhb1b.xn--80aiybcecm.xn--p1ai', 'xn--d1acufc.xn--p1ai']) {
		assert.equal(new URL(`http://${readableHost(host)}/`).hostname, host)
	}
})

test('латинский адрес, IP и порт не меняются', () => {
	assert.equal(readableHost('localhost:5173'), 'localhost:5173')
	assert.equal(readableHost('77.110.104.170'), '77.110.104.170')
	assert.equal(readableHost('example.com'), 'example.com')
})

test('битая метка остаётся как есть', () => {
	assert.equal(readableHost('xn--!!!.ru'), 'xn--!!!.ru')
})
