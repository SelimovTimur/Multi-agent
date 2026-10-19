// avatarScene.js — сцена аватара поверх three.js, без React.
//
// Вынесено из React намеренно: сцена живёт императивным циклом кадров, и
// пересоздавать её на каждый рендер компонента нельзя. Модуль ничего не знает
// про экран разговора — его отдельно проверяют на странице прототипа.
import * as THREE from 'three'
import { GLTFLoader } from 'three/addons/loaders/GLTFLoader.js'
import { createFace } from './avatarFace.js'
import { createGestures } from './avatarGestures.js'
import { createVoiceMeter } from './avatarVoice.js'

const STATES = new Set(['idle', 'listening', 'thinking', 'speaking'])
const BONE_AXIS = new THREE.Vector3(0, 1, 0)
const PARENT_X = new THREE.Vector3(1, 0, 0)
const PARENT_Z = new THREE.Vector3(0, 0, 1)
// Ишка — широкий треугольник: руки строго вниз уходят внутрь теста.
const DEFAULT_SPLAY_DEGREES = 50
const HAND_TWIST_DEGREES = 12

const BONE_ROLES = new Map([
	['spine', 'spine'],
	['top', 'top'],
	['l arm', 'armLeft'],
	['r arm', 'armRight'],
	['l hand', 'handLeft'],
	['r hand', 'handRight'],
])

// GLTFLoader переписывает пробелы, поэтому «L arm» приходит как «L_arm».
const normalizeBoneName = (name) => name.toLowerCase().replace(/[^a-z0-9]+/g, ' ').trim()

export function createAvatarScene(mount, options = {}) {
	const {
		modelUrl = '/avatar/echpochmak-face-web.glb',
		backdropUrl = null,
		fallbackEl = null,
		splayDegrees = DEFAULT_SPLAY_DEGREES,
	} = options

	const motion = window.matchMedia('(prefers-reduced-motion: reduce)')
	const spare = new THREE.Quaternion()
	const bones = {}
	let renderer = null
	let scene = null
	let camera = null
	let avatar = null
	let frame = null
	let observer = null
	let bodyHeight = 1
	let frameRadius = 1
	let state = 'idle'
	let ready = false
	let dead = false
	// Рот, моргание и жесты есть только у модели с морфами и локтями
	// (echpochmak-face-web.glb). Со старой моделью сцена работает как раньше.
	let face = null
	let gestures = null
	const voice = createVoiceMeter()
	let lastSeconds = null
	let greeted = false
	let nextExplain = 0

	function dispose3D(root) {
		const used = new Set()
		root?.traverse((object) => {
			if (object.geometry) used.add(object.geometry)
			const materials = Array.isArray(object.material) ? object.material : [object.material]
			for (const material of materials) {
				if (!material) continue
				used.add(material)
				for (const value of Object.values(material)) if (value?.isTexture) used.add(value)
			}
			if (object.skeleton) used.add(object.skeleton)
		})
		for (const item of used) item.dispose()
	}

	function stop() {
		if (frame !== null) cancelAnimationFrame(frame)
		frame = null
	}

	function showFallback(error) {
		if (dead) return
		dead = true
		ready = false
		stop()
		observer?.disconnect()
		window.removeEventListener('resize', resize)
		// Картинку открываем до уборки: уборка сама может бросить исключение.
		if (fallbackEl) fallbackEl.hidden = false
		mount.setAttribute('aria-busy', 'false')
		renderer?.domElement.remove()
		try {
			dispose3D(scene)
			renderer?.dispose()
		} catch (cleanupError) {
			console.warn('avatar stage cleanup:', cleanupError)
		}
		console.warn('avatar stage fallback:', error)
	}

	function identifyBones(model) {
		model.traverse((object) => {
			if (!object.isBone) return
			const role = BONE_ROLES.get(normalizeBoneName(object.name))
			if (!role || bones[role]) return
			bones[role] = { bone: object, bind: object.quaternion.clone(), rest: object.quaternion.clone() }
		})
	}

	function restForArm(entry, outwardSign) {
		const { bone, bind } = entry
		bone.parent.updateWorldMatrix(true, false)
		const parentQuaternion = bone.parent.getWorldQuaternion(new THREE.Quaternion())
		// Кисть сидит на локальном +Y плечевой кости — это направление руки.
		const current = BONE_AXIS.clone().applyQuaternion(bind).normalize()
		const splay = THREE.MathUtils.degToRad(splayDegrees)
		const target = new THREE.Vector3(outwardSign * Math.sin(splay), -Math.cos(splay), 0)
			.applyQuaternion(parentQuaternion.clone().invert())
			.normalize()
		return new THREE.Quaternion().setFromUnitVectors(current, target).multiply(bind)
	}

	// В модели нет клипов, её bind-поза — это T-поза. Заменяем её один раз,
	// дальше каждый кадр отсчитывается от получившейся позы покоя.
	function applyRestPose() {
		if (gestures) {
			// Руками целиком управляет модуль жестов: его поза покоя та же
			// (руки вдоль скатов, кисти чуть внутрь), а плечо и предплечье
			// он наводит сам. Здесь только ставим её один раз для габаритов.
			gestures.update(0)
			return
		}
		for (const [role, sign] of [['armLeft', -1], ['armRight', 1]]) {
			const entry = bones[role]
			if (!entry?.bone.parent) continue
			entry.rest.copy(restForArm(entry, sign))
			entry.bone.quaternion.copy(entry.rest)
		}
		for (const [role, sign] of [['handLeft', 1], ['handRight', -1]]) {
			const entry = bones[role]
			if (!entry) continue
			const twist = spare.setFromAxisAngle(BONE_AXIS, sign * THREE.MathUtils.degToRad(HAND_TWIST_DEGREES))
			entry.rest.copy(entry.bind).multiply(twist)
			entry.bone.quaternion.copy(entry.rest)
		}
	}

	// Box3.setFromObject читает bind-габариты скиннед-меша, то есть T-позу:
	// камера встала бы слишком далеко после сужения силуэта.
	function measurePosedBounds(model) {
		model.updateWorldMatrix(true, true)
		const bounds = new THREE.Box3()
		let measured = false
		model.traverse((object) => {
			if (!object.isSkinnedMesh || typeof object.computeBoundingBox !== 'function') return
			object.computeBoundingBox()
			if (!object.boundingBox) return
			bounds.union(object.boundingBox.clone().applyMatrix4(object.matrixWorld))
			measured = true
		})
		return measured ? bounds : new THREE.Box3().setFromObject(model)
	}

	function rotate(entry, axis, angle) {
		if (!entry || !angle) return
		entry.bone.quaternion.premultiply(spare.setFromAxisAngle(axis, angle))
	}

	function pose(seconds) {
		const moving = !motion.matches
		const dt = lastSeconds === null ? 0 : Math.min(0.05, Math.max(0, seconds - lastSeconds))
		lastSeconds = seconds
		for (const entry of Object.values(bones)) entry.bone.quaternion.copy(entry.rest)
		face?.update(dt, { speaking: state === 'speaking', sample: voice.sample() })
		if (gestures) {
			if (state === 'speaking' && !gestures.active && seconds >= nextExplain) {
				gestures.play('explain')
				nextExplain = seconds + 5 + Math.random() * 3
			}
			if (state !== 'speaking' && gestures.active === 'explain') gestures.stop()
			gestures.update(dt, { speech: face?.speech ?? 0 })
		}
		// Наблюдаемость, как у data-state: без них «рот двигается под звук»
		// и «аватар поздоровался» нечем проверить. Пишем только при изменении.
		if (face) {
			const mouth = String(Math.round(face.speech * 10) / 10)
			if (mount.dataset.mouth !== mouth) mount.dataset.mouth = mouth
		}
		const gesture = gestures?.active || ''
		if (mount.dataset.gesture !== gesture) mount.dataset.gesture = gesture
		avatar.position.set(0, 0, 0)
		avatar.rotation.set(0, 0, 0)
		avatar.scale.set(1, 1, 1)

		if (moving) {
			const breath = Math.sin(seconds * Math.PI * 0.5)
			const sway = Math.sin(seconds * 0.37)
			const weight = Math.sin(seconds * 0.21)
			avatar.position.y = breath * bodyHeight * 0.0015
			avatar.scale.y = 1 + breath * 0.0018
			rotate(bones.spine, PARENT_X, breath * 0.016)
			rotate(bones.spine, PARENT_Z, sway * 0.012)
			rotate(bones.top, PARENT_Z, -sway * 0.009)
			rotate(bones.top, PARENT_X, weight * 0.008)
			// Фазы разведены, иначе руки качаются как метроном.
			rotate(bones.armLeft, PARENT_X, Math.sin(seconds * 0.31) * 0.022)
			rotate(bones.armRight, PARENT_X, Math.sin(seconds * 0.31 + 0.9) * 0.019)
		}

		const wave = moving ? Math.sin(seconds * 1.6) : 0
		const speech = moving ? Math.sin(seconds * 3.2) : 0

		if (state === 'listening') {
			avatar.rotation.x = 0.025
			avatar.rotation.z = -0.012
		} else if (state === 'thinking') {
			const tilt = 0.018 + wave * 0.008
			avatar.rotation.z = tilt
			avatar.rotation.y = 0.025 + wave * 0.012
			rotate(bones.top || bones.spine, PARENT_Z, tilt * 0.5)
		} else if (state === 'speaking') {
			avatar.position.y += speech * bodyHeight * 0.002
			avatar.rotation.z = speech * 0.007
			rotate(bones.spine, PARENT_X, speech * 0.008)
			rotate(bones.armLeft, PARENT_X, speech * 0.025)
			rotate(bones.armRight, PARENT_X, -speech * 0.022)
		}
	}

	function draw(timestamp = performance.now()) {
		frame = null
		if (!ready || dead) return
		try {
			pose(timestamp / 1000)
			renderer.render(scene, camera)
			if (!motion.matches) frame = requestAnimationFrame(draw)
		} catch (error) {
			showFallback(error)
		}
	}

	function restart() {
		stop()
		draw()
	}

	function resize() {
		if (!renderer || dead) return
		try {
			const width = Math.max(1, mount.clientWidth)
			const height = Math.max(1, mount.clientHeight)
			renderer.setPixelRatio(Math.min(window.devicePixelRatio || 1, 1.5))
			renderer.setSize(width, height, false)
			camera.aspect = width / height
			const halfV = THREE.MathUtils.degToRad(camera.fov / 2)
			const limiting = Math.min(halfV, Math.atan(Math.tan(halfV) * camera.aspect))
			const distance = (frameRadius * 1.15) / Math.sin(limiting)
			camera.position.set(0, 0, distance)
			camera.near = Math.max(0.01, distance - frameRadius * 2)
			camera.far = distance + frameRadius * 3
			camera.lookAt(0, 0, 0)
			camera.updateProjectionMatrix()
			restart()
		} catch (error) {
			showFallback(error)
		}
	}

	function applyBackdrop() {
		if (!backdropUrl) return
		const layer = mount.querySelector('[data-avatar-backdrop]')
		if (!layer) return
		const probe = new Image()
		probe.addEventListener('load', () => {
			layer.style.backgroundImage = `url("${backdropUrl}")`
			layer.dataset.loaded = 'true'
		}, { once: true })
		// При неудаче остаётся градиент: урок не теряет сцену.
		probe.addEventListener('error', () => console.warn('backdrop failed:', backdropUrl), { once: true })
		probe.src = backdropUrl
	}

	applyBackdrop()
	mount.setAttribute('aria-busy', 'true')

	try {
		const canvas = document.createElement('canvas')
		// three r179 требует WebGL 2; без него рендерер не создаём.
		const context = canvas.getContext('webgl2', { alpha: true, antialias: true })
		if (!context) throw new Error('WebGL 2 unavailable')
		renderer = new THREE.WebGLRenderer({ canvas, context, alpha: true, antialias: true })
		renderer.outputColorSpace = THREE.SRGBColorSpace
		renderer.setClearColor(0x000000, 0)
		canvas.setAttribute('aria-hidden', 'true')
		canvas.addEventListener('webglcontextlost', (event) => {
			event.preventDefault()
			showFallback(new Error('WebGL context lost'))
		})
		mount.appendChild(canvas)
		scene = new THREE.Scene()
		camera = new THREE.PerspectiveCamera(35, 1, 0.01, 100)
		scene.add(new THREE.HemisphereLight(0xfff8eb, 0x65765e, 2.4))
		const key = new THREE.DirectionalLight(0xfff1d9, 3)
		key.position.set(3, 4, 5)
		scene.add(key)
		resize()
		if (!dead) {
			if ('ResizeObserver' in window) {
				observer = new ResizeObserver(resize)
				observer.observe(mount)
			} else window.addEventListener('resize', resize)
			motion.addEventListener('change', restart)

			new GLTFLoader().load(modelUrl, (gltf) => {
				if (dead) return dispose3D(gltf.scene)
				try {
					avatar = new THREE.Group()
					const centering = new THREE.Group()
					centering.add(gltf.scene)
					avatar.add(centering)
					scene.add(avatar)
					// Сначала поза: камера должна кадрировать покой, а не T-позу.
					identifyBones(gltf.scene)
					face = createFace(gltf.scene)
					try {
						gestures = createGestures(THREE, gltf.scene)
					} catch (error) {
						gestures = null                         // модель без локтей — руки по-старому
					}
					applyRestPose()
					const bounds = measurePosedBounds(gltf.scene)
					if (bounds.isEmpty()) throw new Error('Empty avatar model')
					const center = bounds.getCenter(new THREE.Vector3())
					const size = bounds.getSize(new THREE.Vector3())
					bodyHeight = size.y
					frameRadius = size.length() / 2
					if (!Number.isFinite(frameRadius) || frameRadius <= 0) throw new Error('Invalid bounds')
					centering.position.copy(center).negate()
					ready = true
					mount.setAttribute('aria-busy', 'false')
					if (fallbackEl) fallbackEl.hidden = true
					resize()
				} catch (error) {
					showFallback(error)
				}
			}, undefined, showFallback)
		}
	} catch (error) {
		showFallback(error)
	}

	return {
		setState(next) {
			if (!STATES.has(next) || next === state) return
			state = next
			// Первая реплика в разговоре — с приветствием.
			if (next === 'speaking' && gestures && !greeted) {
				greeted = true
				gestures.play('wave')
			}
			restart()
		},
		// Общий <audio> экрана: по нему рот двигается в такт речи.
		setAudio(element) {
			voice.attach(element || null)
		},
		// Жест снаружи: 'wave' | 'explain' | 'hooray' | 'point'.
		gesture(name) {
			gestures?.play(name)
		},
		dispose() {
			voice.dispose()
			dead = true
			ready = false
			stop()
			observer?.disconnect()
			window.removeEventListener('resize', resize)
			motion.removeEventListener('change', restart)
			renderer?.domElement.remove()
			try {
				dispose3D(scene)
				renderer?.dispose()
			} catch (error) {
				console.warn('avatar stage dispose:', error)
			}
		},
	}
}
