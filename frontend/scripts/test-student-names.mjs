import assert from 'node:assert/strict'
import { test } from 'node:test'
import { parseNames } from '../src/cabinet/pages/student-names.js'

test('номер и фамилия на разных строках, как при копировании таблицы из Word', () => {
	const pasted = '№\nФИ\n1\nАбдуллина Азалия \n2\nГарипова Инзиля \n\n3\nГаязов Айваз'
	assert.deepEqual(parseNames(pasted), ['Абдуллина Азалия', 'Гарипова Инзиля', 'Гаязов Айваз'])
})

test('строки таблицы из Excel с номером в отдельной ячейке', () => {
	const pasted = '№\tФИО\n1\tГимаева Альбина\t\n2\tГарипова  Ясмина'
	assert.deepEqual(parseNames(pasted), ['Гимаева Альбина', 'Гарипова Ясмина'])
})

test('нумерованный список и фамилия с именем в разных ячейках', () => {
	assert.deepEqual(parseNames('1. Акберов Нияз\n2) Акберова Алия\nКамаев\tКарим'), [
		'Акберов Нияз',
		'Акберова Алия',
		'Камаев Карим',
	])
})

test('повторы убираются без учёта регистра и ё', () => {
	assert.deepEqual(parseNames('Семёнова Камилла\nсеменова камилла\nСеменова Камилла'), ['Семёнова Камилла'])
})

test('пустой ввод — пустой список', () => {
	assert.deepEqual(parseNames(''), [])
	assert.deepEqual(parseNames(' \n\t\n12\n'), [])
})
