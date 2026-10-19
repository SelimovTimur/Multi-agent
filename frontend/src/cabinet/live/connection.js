// Столько после соединения ждём первую реплику аватара, прежде чем записать
// тишину в журнал.
const FIRST_AUDIO_MS = 10000

// One instance owns one call, including late microphone grants and cancellation.
export class LiveConnection {
	constructor({ audio, onState, onError, createSession, greeting, onTranscript, onPartial, onEnd, onEvent, onTelemetry, inputEnabled = true, captureInput = true, createSilentStream,
		mediaDevices = navigator.mediaDevices, PeerConnection = RTCPeerConnection }) {
		Object.assign(this, { audio, onState, onError, createSession, greeting, onTranscript, onPartial, onEnd, onEvent, inputEnabled, captureInput, createSilentStream,
			onTelemetry, mediaDevices, PeerConnection })
		// Идентификатор сессии у провайдера. Нужен, чтобы сообщить о конце
		// разговора: ключ OpenAI держит ограниченное число сессий, и место
		// освобождает только этот сигнал (или таймаут шлюза).
		this.sessionId = ''
		this.abort = new AbortController()
		this.stopped = false
		// Расшифровка приходит кусками и без явной границы реплики: собираем её
		// здесь и отдаём целой фразой, когда заговорила другая сторона. Иначе
		// разговор в истории урока распадётся на обрывки слов.
		this.buffer = { student: '', avatar: '' }
		this.idle = { student: null, avatar: null }
	}

	async start() {
		this.onState('connecting')
		this.telemetry('start', 'ok')
		this.startTimer = setTimeout(() => this.fail('Подключение заняло слишком много времени. Попробуйте ещё раз'), 40000)
		try {
			const stream = this.captureInput ? await this.ensureMicrophone() : (this.silentStream = this.createSilentStream?.())
			if (this.stopped) return
			if (!stream) throw new Error('Браузер не смог подготовить звук для разговора')
			const pc = this.pc = new this.PeerConnection()
			// Начальная дорожка может передавать тишину без захвата устройства.
			// Микрофон подключается позже через replaceTrack без новой сессии.
			this.sender = pc.addTrack(stream.getTracks()[0], stream)
			pc.ontrack = ({ streams, track }) => {
				if (this.stopped) return
				this.audio.srcObject = streams[0] || new MediaStream([track])
				// Дорожка играет с подключения, ещё в тишине: первый звук аватара
				// отмечает его реплика (heard), а не запуск воспроизведения.
				this.audio.play().catch(() => this.fail('Не удалось включить звук. Проверьте разрешение на воспроизведение и попробуйте снова', 'first_audio'))
			}
			pc.onconnectionstatechange = () => {
				clearTimeout(this.disconnectTimer)
				if (this.stopped) return
				if (pc.connectionState === 'failed' || pc.connectionState === 'closed') {
					this.fail('Соединение прервано. Начните разговор заново')
				} else if (pc.connectionState === 'disconnected') {
					this.onState('reconnecting')
					this.disconnectTimer = setTimeout(() => this.fail('Связь потеряна. Начните разговор заново'), 10000)
				} else if (pc.connectionState === 'connected' && this.started) this.onState('active')
			}
			const dc = this.dc = pc.createDataChannel('oai-events')
			dc.onmessage = ({ data }) => {
				let event
				try { event = JSON.parse(data) } catch { return }
				if (import.meta.env?.DEV && ['session.usage.updated', 'session.closed'].includes(event.type)) {
					console.debug('GPT-Live usage', JSON.stringify({ seconds: event.usage?.seconds, closed: event.type === 'session.closed' }))
				}
				if (!this.stopped) this.onEvent?.(event)
				if (event.type === 'session.closed') {
					if (!this.stopped) {
						// Закрытие без local stop означает разрыв у провайдера.
						this.report('session', 'failed')
						this.stopped = true
						this.onState('ended')
					}
					this.finish()
					return
				}
				// После остановки микрофона провайдер ещё досылает расшифровку.
				// Принимаем её до подтверждения закрытия или таймаута.
				if (this.closed || (this.stopped && !event.type?.startsWith('session.input_transcript.') && !event.type?.startsWith('session.output_transcript.'))) return
				if (event.type === 'session.started' && !this.started) {
					this.started = true
					clearTimeout(this.startTimer)
					this.telemetry('connect', 'ok')
					// Тишина в первые секунды — сигнал для журнала, а не повод
					// обрывать разговор: в медленной школьной сети звук может
					// прийти позже, и разговор при этом состоится. Порог —
					// с запасом на ожидание вступления (INTRO_WAIT_MS, 4 с).
					if (!this.firstAudio) this.firstAudioTimer = setTimeout(() => this.report('first_audio', 'failed'), FIRST_AUDIO_MS)
					this.onState('active')
					if (this.greeting !== false) this.send({ type: 'session.instructions.append', event_id: 'greeting', delegation_id: null,
						content: this.greeting || 'Begin now, without waiting for the learner: greet them briefly in Tatar without using a personal name, and ask one easy question in Tatar about their day. Then pause and listen. Follow the existing language policy.' })
				} else if (event.type === 'session.input_transcript.delta') {
					this.transcript('student', event.delta || '')
				} else if (event.type === 'session.output_transcript.delta') {
					this.heard()
					this.transcript('avatar', event.delta || '')
				} else if (event.type === 'session.input_transcript.done') {
					this.flush('student')
				} else if (event.type === 'session.output_transcript.done') {
					this.flush('avatar')
				} else if (event.type === 'error' ||
					(event.type === 'response.event' && ['response.failed', 'error'].includes(event.event?.type))) {
					if (import.meta.env?.DEV) console.warn('GPT-Live command failed', JSON.stringify(event.error || event.event?.response?.error || event.event?.error))
					this.fail('Голосовой сервис не смог продолжить разговор. Попробуйте ещё раз')
				}
			}
			dc.onclose = () => { if (!this.stopped) this.fail('Разговор завершён из-за потери связи. Попробуйте снова') }
			dc.onerror = () => { if (!this.stopped) this.fail('Ошибка голосового соединения. Попробуйте снова') }
			await pc.setLocalDescription(await pc.createOffer())
			await this.waitForIce()
			if (this.stopped) return
			// Запрос сессии ограничен своим таймаутом — дольше серверной очереди
			// и бюджета провайдера. Общий таймер старта его не обрывает: иначе
			// в час пик, когда весь класс нажимает «Начать» разом, клиент сдавался
			// раньше сервера, и место на ключе висело без id до таймаута шлюза.
			clearTimeout(this.startTimer)
			const result = await this.createSession(pc.localDescription.sdp, this.abort.signal)
			this.sessionId = result?.session?.id || ''
			if (this.stopped) { this.ended(); return }
			if (!result?.transport?.sdp) throw new Error('Не удалось согласовать аудиосоединение')
			await pc.setRemoteDescription({ type: 'answer', sdp: result.transport.sdp })
			// Сессия создана — до её начала остаётся только звук.
			if (!this.started) this.startTimer = setTimeout(() => this.fail('Подключение заняло слишком много времени. Попробуйте ещё раз'), 20000)
		} catch (err) {
			if (this.stopped) return
			const message = err.name === 'NotAllowedError'
				? 'Разрешите доступ к микрофону в браузере и попробуйте снова'
				: err.name === 'NotFoundError' ? 'Микрофон не найден. Подключите его и попробуйте снова'
				: err.name === 'NotReadableError' ? 'Микрофон занят другим приложением или недоступен'
				: err.message || 'Не удалось начать разговор. Попробуйте ещё раз'
			this.fail(message)
		}
	}

	async ensureMicrophone() {
		if (this.stopped) return null
		if (this.stream?.getTracks().some(track => track.readyState !== 'ended')) return this.stream
		if (this.microphonePromise) return this.microphonePromise
		this.microphonePromise = (async () => {
			if (!this.mediaDevices?.getUserMedia) throw new Error('Микрофон недоступен. Откройте страницу через HTTPS или localhost')
			const stream = await this.mediaDevices.getUserMedia({ audio: {
				echoCancellation: true, noiseSuppression: true, autoGainControl: true,
			} })
			try {
				if (this.stopped) { stream.getTracks().forEach(track => track.stop()); return null }
				stream.getTracks().forEach(track => { track.enabled = this.inputEnabled })
				if (this.sender) await this.sender.replaceTrack(stream.getTracks()[0])
				if (this.stopped) { stream.getTracks().forEach(track => track.stop()); return null }
				this.stream = stream
				return stream
			} catch (error) { stream.getTracks().forEach(track => track.stop()); throw error }
		})()
		try { return await this.microphonePromise }
		finally { this.microphonePromise = null }
	}

	waitForIce() {
		if (this.pc.iceGatheringState === 'complete') return Promise.resolve()
		return new Promise((resolve, reject) => {
			const done = (err) => {
				clearTimeout(timer)
				this.pc.removeEventListener('icegatheringstatechange', change)
				this.abort.signal.removeEventListener('abort', cancel)
				err ? reject(err) : resolve()
			}
			const change = () => { if (this.pc.iceGatheringState === 'complete') done() }
			const cancel = () => done(new Error('Подключение отменено'))
			const timer = setTimeout(() => done(new Error('Не удалось подготовить соединение. Проверьте сеть')), 10000)
			this.pc.addEventListener('icegatheringstatechange', change)
			this.abort.signal.addEventListener('abort', cancel, { once: true })
			if (this.abort.signal.aborted) cancel()
		})
	}

	send(event) { if (this.dc?.readyState === 'open') this.dc.send(JSON.stringify(event)) }

	setInputEnabled(enabled) {
		this.inputEnabled = enabled
		this.stream?.getTracks().forEach(track => { track.enabled = enabled })
		if (this.started && !this.stopped) this.send({ type: enabled ? 'session.input_audio.unmute' : 'session.input_audio.mute', event_id: `mic_${Date.now()}` })
	}

	/**
	 * Копит расшифровку и отдаёт её целой репликой.
	 *
	 * Закрываем реплику не по тому, что заговорила другая сторона, а по
	 * паузе: расшифровки собеседников перекрываются (ученик ещё договаривает,
	 * аватар уже отвечает), и закрытие «по чужому слову» рвало реплику на
	 * куски посреди фразы. Событие *.done, если провайдер его пришлёт,
	 * закрывает реплику сразу.
	 */
	transcript(role, delta) {
		if (!this.onTranscript || !delta) return
		this.buffer[role] += delta
		// Недосказанная реплика видна сразу, по мере речи: ученик смотрит, как
		// его слова превращаются в текст, и читает ответ аватара, пока слушает.
		this.onPartial?.(role, this.buffer[role])
		clearTimeout(this.idle[role])
		this.idle[role] = setTimeout(() => this.flush(role), 1500)
	}

	flush(role) {
		clearTimeout(this.idle[role])
		const text = this.buffer[role].trim()
		this.buffer[role] = ''
		this.onPartial?.(role, '')
		if (text && this.onTranscript) this.onTranscript(role, text)
	}

	flushAll() {
		this.flush('student')
		this.flush('avatar')
	}

	network() {
		const n = globalThis.navigator
		const online = n?.onLine === false ? 'offline' : 'online'
		const type = n?.connection?.effectiveType
		return type ? `${online}:${type}` : online
	}

	// По какому протоколу браузер получил страницу: школьный фильтр-посредник
	// виден по http/1.1 там, где без него был бы h2.
	pageProtocol() {
		try { return globalThis.performance?.getEntriesByType?.('navigation')?.[0]?.nextHopProtocol || '' } catch { return '' }
	}

	// Аватар заговорил: расшифровка его речи идёт вместе со звуком.
	heard() {
		if (this.stopped || this.firstAudio) return
		this.firstAudio = true
		clearTimeout(this.firstAudioTimer)
		this.telemetry('first_audio', 'ok')
	}

	telemetry(stage, result) {
		try { this.onTelemetry?.({ stage, result, protocol: this.pageProtocol(), network: this.network() }) } catch {}
	}

	// Один сбой на разговор: иначе обрыв после тишины в начале посчитался бы
	// в отчёте как два упавших разговора.
	report(stage, result) {
		if (result === 'failed') {
			if (this.failureReported) return
			this.failureReported = true
		}
		this.telemetry(stage, result)
	}

	fail(message, stage = '') {
		if (this.stopped) return
		this.report(stage || (this.started ? (this.firstAudio ? 'session' : 'first_audio') : 'connect'), 'failed')
		this.onError(message)
		void this.stop()
	}

	stop() {
		if (this.stopPromise) return this.stopPromise
		this.stopped = true
		this.abort.abort()
		clearTimeout(this.startTimer)
		clearTimeout(this.disconnectTimer)
		clearTimeout(this.firstAudioTimer)
		this.stream?.getTracks().forEach((track) => track.stop())
		this.silentStream?.getTracks().forEach((track) => track.stop())
		this.audio.pause()
		this.audio.srcObject = null
		this.stopPromise = new Promise((resolve) => { this.resolveStop = resolve })
		if (this.dc?.readyState === 'open') {
			// Stop local capture/playback immediately, but let the close event arrive.
			this.closeTimer = setTimeout(() => this.finish(), 5000)
			try { this.send({ type: 'session.close' }) } catch { this.finish() }
		} else this.finish()
		return this.stopPromise
	}

	/** Сообщить о конце разговора — один раз за сессию. */
	ended() {
		if (this.endReported || !this.sessionId) return
		this.endReported = true
		this.onEnd?.(this.sessionId)
	}

	finish() {
		if (this.closed) return
		this.closed = true
		this.ended()
		// Незаконченная реплика всё равно уходит в историю: обрыв связи не
		// повод терять то, что ученик уже сказал.
		this.flushAll()
		clearTimeout(this.idle.student)
		clearTimeout(this.idle.avatar)
		clearTimeout(this.closeTimer)
		clearTimeout(this.startTimer)
		clearTimeout(this.disconnectTimer)
		clearTimeout(this.firstAudioTimer)
		this.stream?.getTracks().forEach((track) => track.stop())
		this.silentStream?.getTracks().forEach((track) => track.stop())
		this.audio.pause()
		this.audio.srcObject = null
		this.dc?.close()
		this.pc?.close()
		this.resolveStop?.()
	}
}
