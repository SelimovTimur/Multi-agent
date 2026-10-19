import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig(() => {
	const apiTarget = process.env.NATIVE_DEV_API_URL || 'http://localhost:8080'

	return {
		envDir: '..',
		plugins: [react()],
		server: {
			port: 3000,
			// Проксируем API-запросы на Go-бэкенд при разработке.
			// В Docker это делает Nginx.
			proxy: {
				'/api': {
					target: apiTarget,
					changeOrigin: true,
				},
				'/health': {
					target: apiTarget,
					changeOrigin: true,
				},
			},
		},
	}
})
