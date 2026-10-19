import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync, readdirSync, statSync, existsSync } from 'node:fs'
import { join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'
import { createRequire } from 'node:module'

// Школьный фильтр режет Google: шрифт и иконки обязаны приходить с нашего
// сервера, иначе вместо иконок на экране остаются слова «refresh», «mic».
const root = join(dirname(fileURLToPath(import.meta.url)), '..')
const require = createRequire(join(root, 'package.json'))
const externalFontHost = /fonts\.googleapis\.com|fonts\.gstatic\.com/

function sourceFiles(dir) {
	return readdirSync(dir).flatMap((name) => {
		const path = join(dir, name)
		if (statSync(path).isDirectory()) return sourceFiles(path)
		return /\.(m?jsx?|css|html)$/.test(name) ? [path] : []
	})
}

test('страницы не ходят за шрифтами к внешним доменам', () => {
	const files = [join(root, 'index.html'), ...sourceFiles(join(root, 'src')), ...sourceFiles(join(root, 'public'))]
	const offenders = files.filter((path) => externalFontHost.test(readFileSync(path, 'utf8')))
	assert.deepEqual(offenders, [])
})

test('Inter подключён из пакета во всех весах интерфейса', () => {
	const main = readFileSync(join(root, 'src/main.jsx'), 'utf8')
	for (const weight of [400, 500, 600, 700, 900]) {
		const specifier = `@fontsource/inter/${weight}.css`
		assert.ok(main.includes(`'${specifier}'`), specifier)
		assert.ok(existsSync(require.resolve(specifier)), specifier)
	}
})

test('иконки объявлены своим @font-face и не показываются словами при загрузке', () => {
	const css = readFileSync(join(root, 'src/index.css'), 'utf8')
	const face = css.match(/@font-face\s*{[^}]*font-family:\s*'Material Symbols Outlined'[^}]*}/)
	assert.ok(face, '@font-face для Material Symbols Outlined')
	assert.match(face[0], /font-display:\s*block/)
	const src = face[0].match(/url\('([^']+)'\)/)
	assert.ok(src, 'src шрифта иконок')
	assert.doesNotMatch(src[1], /^https?:/)
	// Файл должен нести оси FILL и wght: иконки заливаются через 'FILL' 1.
	assert.match(src[1], /-fill-normal\.woff2$/)
	assert.ok(existsSync(require.resolve(src[1])), src[1])
	// Класс иконки — в слое base, как прежняя таблица Google перед сборкой:
	// иначе он перебивает text-sm, text-2xl и block у каждой иконки.
	const layer = css.match(/@layer base\s*{\s*\.material-symbols-outlined\s*{[^}]*}/)
	assert.ok(layer, 'класс .material-symbols-outlined внутри @layer base')
	assert.match(layer[0], /font-family:\s*'Material Symbols Outlined'/)
	assert.match(layer[0], /font-feature-settings:\s*'liga'/)
})
