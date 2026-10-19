// avatarVoice.js — громкость речи из общего <audio>, чтобы рот аватара
// двигался в такт.
//
// Звук, который слышит ученик, мы не трогаем. Обычный путь — пропустить
// элемент через createMediaElementSource — перенаправляет его вывод в Web Audio,
// и на удалённом WebRTC-потоке Chrome при этом может отдать тишину: ученик
// перестал бы слышать аватара. Поэтому:
//   - озвучка ответа (blob в src): тот же файл декодируется отдельно, огибающая
//     считается заранее и читается по currentTime;
//   - живой голос (MediaStream в srcObject): к потоку цепляется только
//     анализатор, в динамики он не подключён.
// Если огибающая файла ещё не готова или не посчиталась, sample() возвращает
// null — сцена тогда двигает рот сгенерированными слогами, и аватар не немой.
// Живой поток играет непрерывно, в том числе пока говорит ученик, поэтому
// для него тишина — это тишина (рот закрыт), а не повод генерировать слоги.

const HOP = 1 / 60                        // шаг огибающей, с
const QUIET = { rms: 0, zcr: 0 }

function features(samples, from, to) {
	let sum = 0
	let crossings = 0
	for (let i = from; i < to; i++) {
		const v = samples[i]
		sum += v * v
		if (i > from && (v >= 0) !== (samples[i - 1] >= 0)) crossings++
	}
	const n = Math.max(1, to - from)
	// rms — громкость; zcr — доля смен знака: высокая у «с», «ш», «и»,
	// низкая у «о», «у» — по ней сцена выбирает форму рта
	return { rms: Math.sqrt(sum / n), zcr: crossings / n }
}

async function envelopeOf(url) {
	const response = await fetch(url)
	const bytes = await response.arrayBuffer()
	const Offline = window.OfflineAudioContext || window.webkitOfflineAudioContext
	const decoded = await new Offline(1, 1, 44100).decodeAudioData(bytes)
	const data = decoded.getChannelData(0)
	const hop = Math.max(1, Math.round(decoded.sampleRate * HOP))
	const frames = []
	for (let i = 0; i < data.length; i += hop) frames.push(features(data, i, Math.min(data.length, i + hop)))
	return frames
}

export function createVoiceMeter() {
	let el = null
	let envelope = null
	let envelopeFor = null
	let ctx = null
	let analyser = null
	let streamNode = null
	let stream = null
	let buffer = null
	let streamBroken = false

	function prepareFile() {
		const url = el?.src
		if (!url || url === envelopeFor) return
		envelopeFor = url
		envelope = null
		envelopeOf(url)
			.then((frames) => { if (envelopeFor === url) envelope = frames })
			.catch((error) => console.warn('avatar voice: envelope unavailable', error))
	}

	function prepareStream() {
		const next = el?.srcObject
		if (!(next instanceof MediaStream) || next === stream) return
		stream = next
		streamBroken = false
		try {
			const Context = window.AudioContext || window.webkitAudioContext
			ctx ??= new Context()
			// К этому моменту ученик уже нажимал кнопку — resume разрешён.
			ctx.resume?.()
			streamNode?.disconnect()
			analyser ??= Object.assign(ctx.createAnalyser(), { fftSize: 1024 })
			buffer ??= new Float32Array(analyser.fftSize)
			streamNode = ctx.createMediaStreamSource(stream)
			streamNode.connect(analyser)            // только анализ — не в ctx.destination
		} catch (error) {
			streamBroken = true
			console.warn('avatar voice: stream analyser unavailable', error)
		}
	}

	const onChange = () => { prepareFile(); prepareStream() }

	return {
		attach(audioEl) {
			if (audioEl === el) return
			this.detach()
			el = audioEl
			if (!el) return
			for (const name of ['loadedmetadata', 'playing']) el.addEventListener(name, onChange)
			onChange()
		},
		detach() {
			if (el) for (const name of ['loadedmetadata', 'playing']) el.removeEventListener(name, onChange)
			el = null
		},
		// { rms, zcr } для текущего момента, или null — «не знаю, генерируйте сами».
		sample() {
			if (!el || el.paused || el.ended) return null
			if (el.srcObject instanceof MediaStream) {
				if (streamBroken || !analyser || ctx.state !== 'running') return QUIET
				analyser.getFloatTimeDomainData(buffer)
				return features(buffer, 0, buffer.length)
			}
			if (!envelope || envelopeFor !== el.src) return null
			return envelope[Math.min(envelope.length - 1, Math.floor(el.currentTime / HOP))] || null
		},
		dispose() {
			this.detach()
			streamNode?.disconnect()
			ctx?.close?.()
			ctx = analyser = streamNode = stream = null
		},
	}
}
