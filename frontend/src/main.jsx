import ReactDOM from 'react-dom/client'
import { BrowserRouter } from 'react-router-dom'
import CabinetApp from './cabinet/CabinetApp'
import ErrorBoundary from './cabinet/ErrorBoundary'
// Шрифт интерфейса и иконки отдаёт наш сервер: школьный фильтр режет Google.
import '@fontsource/inter/400.css'
import '@fontsource/inter/500.css'
import '@fontsource/inter/600.css'
import '@fontsource/inter/700.css'
import '@fontsource/inter/900.css'
import './index.css'
// Строго после index.css: там @tailwind base со сбросом, в котором
// button и [type=submit] обнуляют фон. Вес у атрибутного селектора такой
// же, как у класса, поэтому при равном весе побеждает тот, кто позже —
// и дизайн-система обязана идти второй, иначе кнопки теряют заливку,
// а белый текст на них становится невидимым.
import './cabinet/cabinet.css'
import { APP_LANGUAGE, APP_TITLE } from './branding'

document.title = APP_TITLE
document.documentElement.dataset.lessonLanguage = APP_LANGUAGE

ReactDOM.createRoot(document.getElementById('root')).render(
	<ErrorBoundary>
		<BrowserRouter>
			<CabinetApp />
		</BrowserRouter>
	</ErrorBoundary>,
)
