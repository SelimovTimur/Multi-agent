/** @type {import('tailwindcss').Config} */
export default {
	content: ['./index.html', './src/**/*.{js,jsx}'],
	theme: {
		extend: {
			colors: {
				primary: 'rgb(var(--color-primary) / <alpha-value>)',
				'accent-tan': '#D9C3A3',
				'accent-gold': '#D4A755',
				'text-main': '#1F2933',
				'background-light': '#FDFDFB',
				'background-alt': '#F5EFE6',
			},
			fontFamily: {
				display: ['Inter', 'sans-serif'],
			},
			borderRadius: {
				DEFAULT: '0.125rem',
				lg: '0.25rem',
				xl: '0.5rem',
				full: '0.75rem',
			},
		},
	},
	plugins: [require('@tailwindcss/typography')],
}
