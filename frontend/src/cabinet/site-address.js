// site-address.js — адрес сайта так, как его прочитает и наберёт ребёнок.
//
// Домен платформы кириллический, а браузер отдаёт его в location.host в
// виде punycode: «xn--80aaa7dhb1b.xn--…». Напечатать такое на карточке —
// значит дать ученику адрес, который он не прочитает и не наберёт. Поэтому
// метки xn-- раскодируются обратно (RFC 3492).

const BASE = 36
const T_MIN = 1
const T_MAX = 26
const SKEW = 38
const DAMP = 700
const INITIAL_BIAS = 72
const INITIAL_N = 128

function adapt(delta, numPoints, firstTime) {
	delta = firstTime ? Math.floor(delta / DAMP) : delta >> 1
	delta += Math.floor(delta / numPoints)
	let k = 0
	while (delta > ((BASE - T_MIN) * T_MAX) >> 1) {
		delta = Math.floor(delta / (BASE - T_MIN))
		k += BASE
	}
	return k + Math.floor(((BASE - T_MIN + 1) * delta) / (delta + SKEW))
}

function digit(code) {
	if (code >= 48 && code <= 57) return code - 22 // 0–9 → 26–35
	if (code >= 65 && code <= 90) return code - 65 // A–Z
	if (code >= 97 && code <= 122) return code - 97 // a–z
	return BASE
}

/** Раскодирует одну метку punycode без префикса xn--. Ошибка — null. */
function decodeLabel(input) {
	const output = []
	const basic = input.lastIndexOf('-')
	for (let j = 0; j < Math.max(basic, 0); j++) output.push(input.charCodeAt(j))

	let n = INITIAL_N
	let bias = INITIAL_BIAS
	let i = 0
	for (let index = basic > 0 ? basic + 1 : 0; index < input.length; ) {
		const oldi = i
		for (let w = 1, k = BASE; ; k += BASE) {
			if (index >= input.length) return null
			const d = digit(input.charCodeAt(index++))
			if (d >= BASE) return null
			i += d * w
			const t = k <= bias ? T_MIN : k >= bias + T_MAX ? T_MAX : k - bias
			if (d < t) break
			w *= BASE - t
		}
		const len = output.length + 1
		bias = adapt(i - oldi, len, oldi === 0)
		n += Math.floor(i / len)
		i %= len
		output.splice(i++, 0, n)
	}
	return String.fromCodePoint(...output)
}

/** Хост с раскодированными метками xn--; порт сохраняется. */
export function readableHost(host) {
	return String(host || '')
		.split('.')
		.map((label) => {
			if (!label.toLowerCase().startsWith('xn--')) return label
			return decodeLabel(label.slice(4)) ?? label
		})
		.join('.')
}
