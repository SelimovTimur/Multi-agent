// icons.jsx — иконки кабинета.
//
// Набор перенесён из прототипа один в один: те же пути, та же толщина
// линии, тот же viewBox. Иконка рисуется через dangerouslySetInnerHTML,
// потому что пути хранятся строкой — так их видно рядом друг с другом
// и легко сверить с макетом, а разбирать каждую на JSX-элементы смысла нет.

const PATHS = {
  play: '<path d="m8 5 11 7-11 7z"/>',
  pause: '<path d="M8 5v14M16 5v14"/>',
  search: '<circle cx="10.5" cy="10.5" r="6.5"/><path d="m16 16 5 5"/>',
  plus: '<path d="M12 5v14M5 12h14"/>',
  home: '<path d="M3.8 11 12 4.2 20.2 11"/><path d="M6.2 9.9V20h11.6V9.9"/>',
  book: '<path d="M4.5 5.2A2.2 2.2 0 0 1 6.7 3H19.5v15.4H6.7a2.2 2.2 0 0 0-2.2 2.2z"/><path d="M19.5 18.4H6.7"/>',
  user: '<circle cx="12" cy="8" r="3.6"/><path d="M4.6 20c0-3.9 3.3-6 7.4-6s7.4 2.1 7.4 6"/>',
  card: '<rect x="3" y="5.6" width="18" height="12.8" rx="2"/><path d="M3 10.2h18"/>',
  users: '<circle cx="9" cy="8" r="3.4"/><path d="M3 20c0-3.4 2.7-5.2 6-5.2s6 1.8 6 5.2"/><path d="M16.2 5.4a3.4 3.4 0 0 1 0 6.6M17.4 15.2c2.4.5 3.6 2 3.6 4.8"/>',
  exit: '<path d="M9 4H5.5A1.5 1.5 0 0 0 4 5.5v13A1.5 1.5 0 0 0 5.5 20H9"/><path d="M16 16.5 20.5 12 16 7.5"/><path d="M20.5 12H9.5"/>',
  back: '<path d="M15 6l-6 6 6 6"/>',
  fwd: '<path d="M9 6l6 6-6 6"/>',
  down: '<path d="M6 9l6 6 6-6"/>',
  up: '<path d="M18 15l-6-6-6 6"/>',
  x: '<path d="M6 6l12 12M18 6L6 18"/>',
  check: '<path d="M4 12.5l5 5L20 6.5"/>',
  menu: '<path d="M4 7h16M4 12h16M4 17h16"/>',
  trash: '<path d="M4 7h16"/><path d="M9 7V5h6v2"/><path d="M6.5 7l1 13h9l1-13"/>',
  copy: '<rect x="9" y="9" width="11" height="11" rx="2"/><path d="M15 9V6a2 2 0 0 0-2-2H6a2 2 0 0 0-2 2v7a2 2 0 0 0 2 2h3"/>',
  eye: '<path d="M2.5 12S6 6.5 12 6.5 21.5 12 21.5 12 18 17.5 12 17.5 2.5 12 2.5 12z"/><circle cx="12" cy="12" r="2.6"/>',
  edit: '<path d="M4 20h4L20.5 7.5a2 2 0 0 0 0-2.8l-1.2-1.2a2 2 0 0 0-2.8 0L4 16z"/>',
  calendar: '<rect x="3.5" y="5.5" width="17" height="15" rx="2"/><path d="M3.5 10h17M8 3.5v4M16 3.5v4"/>',
  kebab: '<circle cx="12" cy="5.5" r="1.4" fill="currentColor" stroke="none"/><circle cx="12" cy="12" r="1.4" fill="currentColor" stroke="none"/><circle cx="12" cy="18.5" r="1.4" fill="currentColor" stroke="none"/>',
  face: '<circle cx="12" cy="12" r="8.6"/><circle cx="9" cy="10.3" r="1" fill="currentColor" stroke="none"/><circle cx="15" cy="10.3" r="1" fill="currentColor" stroke="none"/><path d="M8.6 14.4c1 1.2 2.1 1.8 3.4 1.8s2.4-.6 3.4-1.8"/>',
  printer: '<path d="M6.5 9V4.5h11V9"/><rect x="4" y="9" width="16" height="7.4" rx="1.6"/><path d="M6.5 14.4h11v5.1h-11z"/>',
  drag: '<circle cx="9" cy="6.5" r="1.3" fill="currentColor" stroke="none"/><circle cx="15" cy="6.5" r="1.3" fill="currentColor" stroke="none"/><circle cx="9" cy="12" r="1.3" fill="currentColor" stroke="none"/><circle cx="15" cy="12" r="1.3" fill="currentColor" stroke="none"/><circle cx="9" cy="17.5" r="1.3" fill="currentColor" stroke="none"/><circle cx="15" cy="17.5" r="1.3" fill="currentColor" stroke="none"/>',
  share: '<circle cx="6" cy="12" r="2.4"/><circle cx="18" cy="6" r="2.4"/><circle cx="18" cy="18" r="2.4"/><path d="M8.2 10.8 15.8 7.2M8.2 13.2l7.6 3.6"/>',
  tag: '<path d="M4 11.4V4.6h6.8l8.6 8.6-6.8 6.8z"/><circle cx="8.2" cy="8.2" r="1.3"/>',
  puzzle: '<path d="M9.5 4.5h5v2.2a1.8 1.8 0 1 0 2.8 1.5v-.2h2.2v5h-2.2a1.8 1.8 0 1 0-1.5 2.8h.2v3.7h-5v-2.2a1.8 1.8 0 1 0-2.8-1.5H7V17H4.5v-5h2.2A1.8 1.8 0 1 0 8.2 9.2H7V4.5z"/>',
  arrowNext: '<path d="M9.5 6l6 6-6 6"/>',
  grammarA: '<path d="M4.5 19.5 10 5h1.6l5.5 14.5"/><path d="M7 15h8"/><path d="M18.5 8.5h2"/>',
  bubble: '<path d="M20 13.5A3.5 3.5 0 0 1 16.5 17H9l-4.5 3.4V7.5A3.5 3.5 0 0 1 8 4h8.5A3.5 3.5 0 0 1 20 7.5z"/>',
  flag: '<path d="M6 21V4.5h12l-2.6 4.2L18 13H6"/>',
  camera: '<path d="M4 8.6A2 2 0 0 1 6 6.6h1.6l1.1-1.9h6.6l1.1 1.9H18a2 2 0 0 1 2 2V18a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2z"/><circle cx="12" cy="13" r="3.5"/>',
  upload: '<path d="M12 16V4"/><path d="M7.5 8.5 12 4l4.5 4.5"/><path d="M4 16v3.5h16V16"/>',
  link: '<path d="M10 13.5a4 4 0 0 0 5.7 0l3-3a4 4 0 0 0-5.7-5.7l-1.3 1.3"/><path d="M14 10.5a4 4 0 0 0-5.7 0l-3 3a4 4 0 0 0 5.7 5.7l1.3-1.3"/>',
  text: '<path d="M5 6h14"/><path d="M5 11h14"/><path d="M5 16h9"/>',
  grid: '<rect x="4" y="4" width="7" height="7" rx="1.4"/><rect x="13" y="4" width="7" height="7" rx="1.4"/><rect x="4" y="13" width="7" height="7" rx="1.4"/><rect x="13" y="13" width="7" height="7" rx="1.4"/>',
  mic: '<rect x="9" y="3" width="6" height="11" rx="3"/><path d="M5.5 11.5a6.5 6.5 0 0 0 13 0"/><path d="M12 18v3"/>',
  target: '<circle cx="12" cy="12" r="8"/><circle cx="12" cy="12" r="3.4"/>',
  spark: '<path d="M12 3.5 13.8 9l5.5 1.8-5.5 1.8L12 18.1l-1.8-5.5L4.7 10.8 10.2 9z"/>',
  folder: '<path d="M3.5 7.5A1.5 1.5 0 0 1 5 6h4l2 2.4h8a1.5 1.5 0 0 1 1.5 1.5v8A1.5 1.5 0 0 1 19 19.4H5a1.5 1.5 0 0 1-1.5-1.5z"/>',
  warn: '<path d="M12 4.5 21 19.5H3z"/><path d="M12 10v4"/><path d="M12 16.6v.2"/>',
  clock: '<circle cx="12" cy="12" r="8"/><path d="M12 7.5V12l3 2"/>',
  chart: '<path d="M4 20V9"/><path d="M10 20V4"/><path d="M16 20v-7"/><path d="M21 20H3"/>',
  refresh: '<path d="M19.5 12a7.5 7.5 0 1 1-2.4-5.5"/><path d="M19.8 4.5V9h-4.5"/>',
}

/**
 * Icon — иконка по имени из набора.
 *
 * Неизвестное имя рисует пустой svg вместо падения: недостающая иконка не
 * должна ронять экран целиком.
 */
export default function Icon({ name, size = 18, className }) {
	return (
		<svg
			width={size}
			height={size}
			viewBox='0 0 24 24'
			fill='none'
			stroke='currentColor'
			strokeWidth='1.7'
			strokeLinecap='round'
			strokeLinejoin='round'
			className={className}
			aria-hidden='true'
			dangerouslySetInnerHTML={{ __html: PATHS[name] || '' }}
		/>
	)
}
