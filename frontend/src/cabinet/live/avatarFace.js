// avatarFace.js — рот и моргание Ишки через морф-таргеты модели
// (mouth_open, mouth_round, mouth_wide, blink).
//
//   const face = createFace(gltf.scene)          // null, если в модели нет морфов
//   face.update(dt, { speaking, sample })         // каждый кадр
//
// sample — { rms, zcr } из avatarVoice.js или null. При null во время речи
// рот двигается сгенерированными слогами: лучше приблизительно, чем немо.

const KEYS = ['mouth_open', 'mouth_round', 'mouth_wide', 'blink']
const clamp01 = (x) => Math.min(1, Math.max(0, x))
const smoothstep = (a, b, x) => { const t = clamp01((x - a) / (b - a)); return t * t * (3 - 2 * t) }

export function createFace(root) {
	const meshes = []
	root.traverse((object) => {
		if (object.morphTargetDictionary && KEYS.some((k) => k in object.morphTargetDictionary)) meshes.push(object)
	})
	if (!meshes.length) return null

	const value = { mouth_open: 0, mouth_round: 0, mouth_wide: 0, blink: 0 }
	let peak = 0.05
	let syllable = { left: 0, open: 0, round: 0, wide: 0 }
	let nextBlink = 1.5 + Math.random() * 2
	let blinkAt = -1
	let clock = 0

	function write() {
		for (const mesh of meshes) {
			for (const key of KEYS) {
				const index = mesh.morphTargetDictionary[key]
				if (index !== undefined) mesh.morphTargetInfluences[index] = value[key]
			}
		}
	}

	function fromSound({ rms, zcr }, dt) {
		// Автоусиление: громкость озвучки разная, рот должен открываться
		// по-разному на тихой и громкой фразе, а не упираться в предел.
		peak = Math.max(rms, peak * Math.exp(-dt / 2.5), 0.02)
		const open = smoothstep(0.12, 0.75, rms / peak)
		// zcr (доля смен знака): высокий — «с», «ш», «и» → рот шире;
		// низкий — «о», «у» → рот круглее.
		return {
			open,
			wide: open * clamp01((zcr - 0.07) / 0.08) * 0.8,
			round: open * clamp01((0.03 - zcr) / 0.015) * 0.6,
		}
	}

	function generated(dt) {
		syllable.left -= dt
		if (syllable.left <= 0) {
			const pause = Math.random() < 0.15
			const kind = Math.random()
			syllable = {
				left: 0.09 + Math.random() * 0.14,
				open: pause ? 0 : 0.35 + Math.random() * 0.65,
				round: !pause && kind < 0.3 ? 0.4 + Math.random() * 0.5 : 0,
				wide: !pause && kind > 0.75 ? 0.3 + Math.random() * 0.5 : 0,
			}
		}
		return syllable
	}

	return {
		// громкость речи 0..1 — для жестов «в такт»
		get speech() { return value.mouth_open },
		update(dt, { speaking = false, sample = null } = {}) {
			clock += dt
			const target = !speaking ? { open: 0, round: 0, wide: 0 } : sample ? fromSound(sample, dt) : generated(dt)
			const openTarget = target.open * (1 - target.round * 0.5)
			// рот открывается быстрее, чем закрывается — так речь читается живее
			for (const [key, t] of [['mouth_open', openTarget], ['mouth_round', target.round], ['mouth_wide', target.wide]]) {
				const rate = t > value[key] ? 30 : 14
				value[key] += (t - value[key]) * (1 - Math.exp(-dt * rate))
			}
			if (clock >= nextBlink) {
				blinkAt = clock
				nextBlink = clock + 2.5 + Math.random() * 3
			}
			if (blinkAt >= 0) {
				const t = (clock - blinkAt) / 0.16
				value.blink = t < 1 ? Math.sin(t * Math.PI) : 0
				if (t >= 1) blinkAt = -1
			}
			write()
		},
	}
}
