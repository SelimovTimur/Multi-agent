// avatarGestures.js — жесты руками для Ишки (нужна модель с локтями: кости L_forearm / R_forearm).
//
//   const g = createGestures(THREE, gltf.scene)
//   g.play('wave')                    // запустить жест
//   g.update(dt, { speech })          // каждый кадр; speech 0..1 — громкость речи
//
// Локальные оси костей в модели повёрнуты по-разному, поэтому позы задаются
// не углами, а направлениями в мире: куда смотрит плечо, куда предплечье.
// Код сам наводит кость на направление (как опускание рук в прототипе).
// Мировые оси: x — вправо на экране, y — вверх, z — к зрителю.
// «L» в модели — рука слева на экране, «R» — справа.
//
// Предел сгиба локтя ≈ 90°: рука короткая и толстая, сильнее — сминается.

const SIDES = { L: -1, R: 1 }
const FINGERS = {
	L: { thumb: 'finger1', index: 'finger2', rest: ['finger3', 'finger4', 'finger5'] },
	R: { thumb: 'finger1_1', index: 'Finger2_1', rest: ['Finger3_1', 'Finger4_1', 'Finger5_1'] },
}

// Поза одной руки: up/fore — направления плеча и предплечья (x — наружу от тела,
// знак подставляется по стороне), twist — поворот кисти вокруг руки, градусы,
// curl — сгиб пальцев 0..1.
const pose = (up, fore, twist = 0, curl = {}) => ({ up, fore, twist, curl: { thumb: 0, index: 0, rest: 0, ...curl } })

const IDLE = pose([0.77, -0.64, 0.05], [0.62, -0.76, 0.2], 12)

const GESTURES = {
	// Машет правой: рука вверх-наружу, предплечье качается влево-вправо.
	wave: {
		dur: 2.2,
		R: pose([0.85, 0.2, 0.25], [0.2, 1, 0.15], 30),
		wobble: { R: { axis: [0, 0, 1], deg: 22, hz: 2.6 } },
	},
	// Объясняет: руки вперёд, ладони вверх; движение — от громкости речи.
	explain: {
		dur: 3.5,
		L: pose([0.55, -0.62, 0.55], [0.45, -0.05, 0.9], 70),
		R: pose([0.55, -0.62, 0.55], [0.45, -0.05, 0.9], 70),
		speechLift: 18,
	},
	// Ура: обе руки вверх.
	hooray: {
		dur: 1.8,
		L: pose([0.7, 0.7, 0.1], [0.35, 1, 0.1], 0),
		R: pose([0.7, 0.7, 0.1], [0.35, 1, 0.1], 0),
		wobble: { L: { axis: [0, 0, 1], deg: 8, hz: 3.2 }, R: { axis: [0, 0, 1], deg: 8, hz: 3.2 } },
	},
	// Смотри сюда: правая открытой ладонью в сторону переписки («вот, посмотри»).
	// Указательный палец и «класс» пробовали — толстая перчатка сжимается в
	// бесформенный комок, жест не читается. Поворот кисти держим в ±60°:
	// сильнее — предплечье перекручивается «фантиком».
	point: {
		dur: 2.2,
		R: pose([0.9, -0.1, 0.4], [0.9, 0.05, 0.45], 10),
	},
}

export function createGestures(THREE, root) {
	const V = (a) => new THREE.Vector3(...a).normalize()
	const Y = new THREE.Vector3(0, 1, 0)
	const bones = {}
	root.traverse((o) => { if (o.isBone) bones[o.name] = { bone: o, rest: o.quaternion.clone() } })
	if (!bones.L_forearm) throw new Error('в модели нет локтей (L_forearm)')

	// Ось сгиба каждого пальца: та локальная ось, поворот вокруг которой ведёт
	// кончик к ладони. В исходной позе ладони смотрят на зрителя (+z).
	const curlAxis = {}
	root.updateMatrixWorld(true)
	for (const side of ['L', 'R']) {
		for (const name of [FINGERS[side].thumb, FINGERS[side].index, ...FINGERS[side].rest]) {
			const { bone, rest } = bones[name]
			const tip = () => { bone.updateMatrixWorld(true); return bone.localToWorld(new THREE.Vector3(0, 0.07, 0)) }
			const base = tip()
			let best = null
			for (const ax of [[1, 0, 0], [-1, 0, 0], [0, 0, 1], [0, 0, -1]]) {
				bone.quaternion.copy(rest).multiply(new THREE.Quaternion().setFromAxisAngle(V(ax), 0.6))
				const dz = tip().z - base.z
				if (!best || dz > best.dz) best = { dz, axis: V(ax) }
			}
			bone.quaternion.copy(rest)
			curlAxis[name] = best.axis
		}
	}

	const q = new THREE.Quaternion(), pq = new THREE.Quaternion(), tmp = new THREE.Vector3()
	function aim(name, dirWorld, twistDeg = 0) {
		const { bone, rest } = bones[name]
		bone.parent.updateWorldMatrix(true, false)
		bone.parent.getWorldQuaternion(pq).invert()
		const want = tmp.copy(dirWorld).applyQuaternion(pq).normalize()
		const now = Y.clone().applyQuaternion(rest)
		bone.quaternion.setFromUnitVectors(now, want).multiply(rest)
		if (twistDeg) bone.quaternion.multiply(q.setFromAxisAngle(Y, THREE.MathUtils.degToRad(twistDeg)))
		bone.updateMatrixWorld(true)
	}
	function curl(name, amount, maxDeg) {
		const { bone, rest } = bones[name]
		bone.quaternion.copy(rest).multiply(q.setFromAxisAngle(curlAxis[name], THREE.MathUtils.degToRad(maxDeg * amount)))
	}

	// текущее состояние каждой руки — плавно тянется к цели
	const cur = {}
	for (const s of ['L', 'R']) cur[s] = { up: V(IDLE.up), fore: V(IDLE.fore), twist: IDLE.twist, curl: { ...IDLE.curl } }
	let active = null, t = 0

	function target(side) {
		const sx = SIDES[side]
		const g = active && GESTURES[active]
		const p = (g && g[side]) || IDLE
		const flip = (a) => [a[0] * sx, a[1], a[2]]
		return { up: V(flip(p.up)), fore: V(flip(p.fore)), twist: p.twist * sx, curl: p.curl }
	}

	return {
		names: Object.keys(GESTURES),
		get active() { return active },
		play(name) { if (GESTURES[name]) { active = name; t = 0 } },
		stop() { active = null },
		update(dt, { speech = 0 } = {}) {
			t += dt
			const g = active && GESTURES[active]
			if (g && t > g.dur) active = null
			const k = 1 - Math.exp(-dt * 9)                 // сглаживание переходов
			for (const side of ['L', 'R']) {
				const tg = target(side), c = cur[side]
				c.up.lerp(tg.up, k).normalize()
				c.fore.lerp(tg.fore, k).normalize()
				c.twist += (tg.twist - c.twist) * k
				for (const f of ['thumb', 'index', 'rest']) c.curl[f] += (tg.curl[f] - c.curl[f]) * k
				let fore = c.fore.clone()
				const w = g?.wobble?.[side]
				if (w) fore.applyAxisAngle(V(w.axis), THREE.MathUtils.degToRad(w.deg) * Math.sin(t * w.hz * Math.PI * 2))
				if (g?.speechLift && g[side]) {
					const lift = THREE.MathUtils.degToRad(g.speechLift) * speech * (side === 'L' ? 1 : 0.8)
					fore.applyAxisAngle(new THREE.Vector3(1, 0, 0), -lift)
				}
				aim(`${side}_arm`, c.up)
				aim(`${side}_forearm`, fore, c.twist)
				const F = FINGERS[side]
				curl(F.thumb, c.curl.thumb, 60)
				curl(F.index, c.curl.index, 85)
				for (const n of F.rest) curl(n, c.curl.rest, 85)
			}
		},
	}
}
