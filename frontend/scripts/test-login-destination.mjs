import test from 'node:test'
import assert from 'node:assert/strict'
import { loginDestination } from '../src/cabinet/loginDestination.js'

test('учитель и ученик возвращаются на свой экран вместе с параметрами и якорем', () => {
 assert.equal(loginDestination('/app/class/1/analytics?tab=conversation#s1'), '/app/class/1/analytics?tab=conversation#s1')
 assert.equal(loginDestination('/student/lesson/1?step=2', 'student'), '/student/lesson/1?step=2')
})

test('внешние адреса, чужие префиксы и кабинет другого актора отклоняются', () => {
 for (const from of [null, {}, 'https://example.org', '//example.org', '/\\example.org', '/app-malicious', '/video_watching/en', '/app/../login']) {
  assert.equal(loginDestination(from), '/app')
 }
 assert.equal(loginDestination('/app/class/1', 'student'), '/student')
 assert.equal(loginDestination('/student/lesson/1'), '/app')
})
