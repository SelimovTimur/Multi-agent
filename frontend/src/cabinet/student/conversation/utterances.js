// utterances.js — фразы ученика в живом разговоре: захват звука, нарезка, WAV.
//
// В живом режиме ученик включает микрофон кнопкой, и его голос сразу слышит
// сессия GPT Live. Отметки и отчёт строятся не по её расшифровке (в ней
// бывают казахские буквы), а по нашему распознаванию, и каждую фразу ученика
// нужно отдельно отправить в /my/transcribe. Здесь всё, что для этого нужно,
// кроме сети:
//
// - startCapture снимает PCM с того же микрофонного потока, что ушёл в
//   сессию: AudioWorklet, а где его нет — ScriptProcessor;
// - Segmenter находит начало фразы по громкости и конец — по паузе около
//   секунды, держит предзахват, чтобы не терять начало первого слова, и
//   режет слишком длинную речь;
// - phraseFile упаковывает фразу в WAV 16 кГц моно PCM16: распознавание
//   принимает его без перекодирования, и он одинаков во всех браузерах.
//
// Нарезка и WAV работают без браузера — тест на синтетическом сигнале:
// frontend/scripts/test-conversation-live.mjs. Браузер трогает только
// startCapture, и только при вызове.

import { fileNameFor } from './state.js'

/** Частота WAV для распознавания: речи хватает, файл втрое меньше 48 кГц. */
export const WAV_RATE = 16000

/** Настройки нарезки. Время — в миллисекундах, громкость — RMS от 0 до 1. */
export const PHRASE = Object.freeze({
	// Шаг анализа: слог не проскочит, а работы немного.
	frameMs: 20,
	// Предзахват: начало слова тише середины, и порог срабатывает на нём с
	// опозданием. Без этих 300 мс распознавание теряло бы первый звук.
	preRollMs: 300,
	// Конец фразы — пауза около секунды. Короче — фраза рвалась бы на
	// раздумьях посреди предложения, длиннее — ответ уходил бы с задержкой.
	endSilenceMs: 900,
	// Тишина в конце, которая остаётся во фразе: последний слог затухает.
	tailMs: 250,
	// Начало — когда громких шагов не меньше onsetFrames из последних
	// onsetWindow: щелчок или стук по столу фразой не становится.
	onsetWindow: 6,
	onsetFrames: 4,
	// Короче этого звучания — кашель или вздох, а не ответ.
	minVoicedMs: 220,
	// Предел одной фразы: распознавание ответа рассчитано на полминуты.
	maxPhraseMs: 25000,
	// Порог громкости: не ниже minLevel и в floorRatio раз выше фона.
	minLevel: 0.015,
	floorRatio: 3,
	// Внутри фразы порог ниже: тихий конец слова фразу не обрывает.
	releaseRatio: 0.6,
	// Пока звучит аватар, порог выше: остаток эха его речи не должен
	// становиться «ответом ученика». Перебить аватара можно — громче.
	duckRatio: 2.5,
	// Аватар заговорил, а ученик уже молчит столько — фраза закрывается
	// сразу: сессия сама решила, что ученик договорил.
	earlyEndMs: 300,
	// Скорость, с которой порог подстраивается под фон в паузах.
	floorAlpha: 0.05,
})

/** Громкость отрезка — среднеквадратичное значение. */
export function levelOf(samples, from = 0, to = samples.length) {
	if (to <= from) return 0
	let sum = 0
	for (let i = from; i < to; i++) sum += samples[i] * samples[i]
	return Math.sqrt(sum / (to - from))
}

function joinFrames(frames) {
	let length = 0
	for (const f of frames) length += f.samples.length
	const out = new Float32Array(length)
	let at = 0
	for (const f of frames) {
		out.set(f.samples, at)
		at += f.samples.length
	}
	return out
}

/**
 * Segmenter — нарезка непрерывного звука микрофона на фразы.
 *
 * push() принимает куски PCM любой длины и режет их на шаги по frameMs.
 * Обратные вызовы — синхронно, по ходу push():
 *   onStart({ id, startMs, continuation }) — началась фраза;
 *   onPhrase({ id, samples, sampleRate, startMs, durationMs, voicedMs, cut }) —
 *     фраза закончилась паузой (cut=false) или пределом длины (cut=true);
 *   onDrop({ id, reason }) — началось, но не фраза: слишком коротко или
 *     захват остановлен.
 *
 * Порог подстраивается под фон в паузах: в шумном классе тихий шум сам
 * фразой не становится. setDucking(true) — звучит аватар: порог выше.
 */
export class Segmenter {
	constructor({ sampleRate, onStart, onPhrase, onDrop, ...options } = {}) {
		if (!(sampleRate > 0)) throw new Error('Segmenter: нужна частота дискретизации')
		this.o = { ...PHRASE, ...options }
		this.sampleRate = sampleRate
		this.onStart = onStart
		this.onPhrase = onPhrase
		this.onDrop = onDrop
		const frames = (ms) => Math.max(1, Math.round(ms / this.o.frameMs))
		this.frameSize = Math.max(1, Math.round((sampleRate * this.o.frameMs) / 1000))
		this.preRollFrames = frames(this.o.preRollMs)
		this.endFrames = frames(this.o.endSilenceMs)
		this.tailFrames = Math.min(frames(this.o.tailMs), this.endFrames)
		this.maxFrames = frames(this.o.maxPhraseMs)
		this.minVoicedFrames = frames(this.o.minVoicedMs)
		this.earlyEndFrames = frames(this.o.earlyEndMs)
		this.floor = this.o.minLevel / this.o.floorRatio
		this.ducking = false
		this.level = 0
		this.lastId = 0
		this.frameNo = 0
		this.buffer = new Float32Array(this.frameSize)
		this.filled = 0
		// Последние шаги до начала фразы: из них берётся предзахват.
		this.history = []
		this.phrase = null
	}

	/** Идёт ли сейчас фраза. */
	get active() {
		return Boolean(this.phrase)
	}

	/** Пороги начала (on) и продолжения (off) фразы. */
	thresholds() {
		const base = Math.max(this.o.minLevel, this.floor * this.o.floorRatio)
		const on = this.ducking ? base * this.o.duckRatio : base
		return { on, off: on * this.o.releaseRatio }
	}

	push(chunk) {
		if (!chunk?.length) return
		const data = chunk instanceof Float32Array ? chunk : Float32Array.from(chunk)
		let offset = 0
		while (offset < data.length) {
			const take = Math.min(this.frameSize - this.filled, data.length - offset)
			this.buffer.set(data.subarray(offset, offset + take), this.filled)
			this.filled += take
			offset += take
			if (this.filled === this.frameSize) {
				const samples = this.buffer
				this.buffer = new Float32Array(this.frameSize)
				this.filled = 0
				this.frame(samples)
			}
		}
	}

	setDucking(on) {
		const next = Boolean(on)
		if (next === this.ducking) return
		this.ducking = next
		const p = this.phrase
		if (next && p && p.silent >= this.earlyEndFrames) this.close(false)
	}

	/** Захват остановлен: недосказанная фраза уходит, если она уже фраза. */
	end() {
		if (this.phrase) this.close(false, true)
	}

	/** Захват остановлен, недосказанное не нужно. */
	cancel() {
		const p = this.phrase
		if (!p) return
		this.phrase = null
		this.history = []
		this.onDrop?.({ id: p.id, reason: 'cancelled' })
	}

	frame(samples) {
		const level = levelOf(samples)
		this.level = level
		const t = this.frameNo++
		const { on, off } = this.thresholds()
		const p = this.phrase
		if (!p) {
			this.history.push({ samples, level })
			if (this.history.length > this.preRollFrames + this.o.onsetWindow) this.history.shift()
			// Фон учится только на тишине и не на эхе аватара: иначе порог
			// после его реплики оставался бы задранным.
			if (level < on && !this.ducking) this.floor += (level - this.floor) * this.o.floorAlpha
			let loud = 0
			for (let i = Math.max(0, this.history.length - this.o.onsetWindow); i < this.history.length; i++) {
				if (this.history[i].level >= on) loud += 1
			}
			if (loud >= this.o.onsetFrames) {
				const frames = this.history
				this.history = []
				const voiced = frames.filter((f) => f.level >= off).length
				this.open(t - frames.length + 1, frames, voiced, false)
			}
			return
		}
		p.frames.push({ samples, level })
		if (level >= off) {
			p.voiced += 1
			p.silent = 0
		} else {
			p.silent += 1
		}
		if (p.silent >= this.endFrames) {
			this.close(false)
		} else if (p.frames.length >= this.maxFrames) {
			this.close(true)
			// Речь не прервалась — продолжение становится следующей фразой.
			if (level >= off) this.open(t + 1, [], 0, true)
		}
	}

	open(startFrame, frames, voiced, continuation) {
		const id = ++this.lastId
		this.phrase = { id, frames, voiced, silent: 0, startFrame, continuation }
		this.onStart?.({ id, startMs: startFrame * this.o.frameMs, continuation })
	}

	close(cut, final = false) {
		const p = this.phrase
		if (!p) return
		this.phrase = null
		// Длинную тишину в конце не отправляем: хватит хвоста.
		const extra = cut || final ? 0 : Math.max(0, p.silent - this.tailFrames)
		const frames = extra ? p.frames.slice(0, p.frames.length - extra) : p.frames
		// Тишина после фразы — предзахват следующей.
		this.history = cut ? [] : p.frames.slice(p.frames.length - Math.min(p.silent, this.preRollFrames))
		if (p.voiced < this.minVoicedFrames) {
			this.onDrop?.({ id: p.id, reason: 'short' })
			return
		}
		const samples = joinFrames(frames)
		this.onPhrase?.({
			id: p.id,
			samples,
			sampleRate: this.sampleRate,
			startMs: p.startFrame * this.o.frameMs,
			durationMs: Math.round((samples.length / this.sampleRate) * 1000),
			voicedMs: p.voiced * this.o.frameMs,
			cut,
		})
	}
}

/**
 * resample — звук в другую частоту.
 *
 * Вниз — средним по окну каждого нового отсчёта: простой фильтр против
 * наложения частот, речи для распознавания его хватает. Вверх (микрофон
 * 8 кГц) — линейно.
 */
export function resample(samples, fromRate, toRate = WAV_RATE) {
	if (!(fromRate > 0) || !(toRate > 0)) throw new Error('resample: нужны частоты')
	if (fromRate === toRate) return Float32Array.from(samples)
	const ratio = fromRate / toRate
	const length = Math.floor(samples.length / ratio)
	const out = new Float32Array(length)
	if (ratio > 1) {
		for (let i = 0; i < length; i++) {
			const from = Math.floor(i * ratio)
			const to = Math.min(samples.length, Math.max(from + 1, Math.floor((i + 1) * ratio)))
			let sum = 0
			for (let j = from; j < to; j++) sum += samples[j]
			out[i] = sum / (to - from)
		}
		return out
	}
	for (let i = 0; i < length; i++) {
		const pos = i * ratio
		const j = Math.floor(pos)
		const a = samples[j] ?? 0
		const b = samples[j + 1] ?? a
		out[i] = a + (b - a) * (pos - j)
	}
	return out
}

/** encodeWav — WAV PCM16 моно: заголовок в 44 байта и отсчёты. */
export function encodeWav(samples, sampleRate = WAV_RATE) {
	const n = samples.length
	const view = new DataView(new ArrayBuffer(44 + n * 2))
	const ascii = (at, text) => {
		for (let i = 0; i < text.length; i++) view.setUint8(at + i, text.charCodeAt(i))
	}
	ascii(0, 'RIFF')
	view.setUint32(4, 36 + n * 2, true)
	ascii(8, 'WAVE')
	ascii(12, 'fmt ')
	view.setUint32(16, 16, true)
	view.setUint16(20, 1, true) // PCM
	view.setUint16(22, 1, true) // моно
	view.setUint32(24, sampleRate, true)
	view.setUint32(28, sampleRate * 2, true)
	view.setUint16(32, 2, true)
	view.setUint16(34, 16, true)
	ascii(36, 'data')
	view.setUint32(40, n * 2, true)
	for (let i = 0; i < n; i++) {
		const s = Math.max(-1, Math.min(1, samples[i] || 0))
		view.setInt16(44 + i * 2, s < 0 ? Math.round(s * 32768) : Math.round(s * 32767), true)
	}
	return new Uint8Array(view.buffer)
}

/** Фраза — файлом для /my/transcribe: WAV 16 кГц моно. */
export function phraseFile(phrase, rate = WAV_RATE) {
	const wav = encodeWav(resample(phrase.samples, phrase.sampleRate, rate), rate)
	return new File([wav], fileNameFor('audio/wav'), { type: 'audio/wav' })
}

// Процессор AudioWorklet: копит отсчёты и отдаёт их странице кусками —
// сообщение на каждые 128 отсчётов загрузило бы страницу зря. Каналы
// сводятся в один.
const WORKLET = `
class NrPcmTap extends AudioWorkletProcessor {
	constructor(options) {
		super()
		this.size = (options && options.processorOptions && options.processorOptions.size) || 2048
		this.buffer = new Float32Array(this.size)
		this.length = 0
	}
	process(inputs) {
		const input = inputs[0]
		if (input && input.length) {
			const channels = input.length
			const frames = input[0].length
			for (let i = 0; i < frames; i++) {
				let v = 0
				for (let c = 0; c < channels; c++) v += input[c][i]
				this.buffer[this.length++] = v / channels
				if (this.length === this.size) {
					this.port.postMessage(this.buffer)
					this.buffer = new Float32Array(this.size)
					this.length = 0
				}
			}
		}
		return true
	}
}
registerProcessor('nr-pcm-tap', NrPcmTap)
`

/**
 * startCapture — PCM с микрофонного потока.
 *
 * context — AudioContext, созданный по нажатию ученика: Safari без жеста
 * звук не включает. Поток — тот же, что ушёл в сессию: и обработка
 * (эхоподавление, шумоподавление), и громкость у нас и у сессии одни.
 * Узел подключён к выходу через нулевую громкость: без выхода Chrome
 * ScriptProcessor не вызывает, а в динамики ничего не идёт.
 *
 * Возвращает { sampleRate, stop }. stop отсоединяет узлы; контекст и поток
 * закрывает их владелец.
 */
export async function startCapture(stream, { context, onChunk, chunkSize = 2048 } = {}) {
	if (!context) throw new Error('Нет AudioContext для захвата звука')
	const g = globalThis
	const source = context.createMediaStreamSource(stream)
	const sink = context.createGain()
	sink.gain.value = 0
	sink.connect(context.destination)
	let node = null
	let url = ''
	if (context.audioWorklet && typeof g.AudioWorkletNode === 'function') {
		try {
			url = g.URL.createObjectURL(new Blob([WORKLET], { type: 'application/javascript' }))
			await context.audioWorklet.addModule(url)
			node = new g.AudioWorkletNode(context, 'nr-pcm-tap', {
				numberOfInputs: 1,
				numberOfOutputs: 1,
				outputChannelCount: [1],
				processorOptions: { size: chunkSize },
			})
			node.port.onmessage = (event) => onChunk?.(event.data)
		} catch {
			// Worklet не загрузился (старый браузер, запрет blob:) — запасной путь ниже.
			node = null
		}
	}
	if (!node) {
		node = context.createScriptProcessor(chunkSize, 1, 1)
		node.onaudioprocess = (event) => onChunk?.(new Float32Array(event.inputBuffer.getChannelData(0)))
	}
	source.connect(node)
	node.connect(sink)
	let stopped = false
	return {
		sampleRate: context.sampleRate,
		stop() {
			if (stopped) return
			stopped = true
			try {
				source.disconnect()
				node.disconnect()
				sink.disconnect()
			} catch {
				// Узлы уже отсоединены вместе с закрытым контекстом.
			}
			if (node.port) {
				node.port.onmessage = null
				node.port.close?.()
			} else {
				node.onaudioprocess = null
			}
			if (url) g.URL.revokeObjectURL(url)
		},
	}
}
