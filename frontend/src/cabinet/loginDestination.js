// Возвращаемся только в доступный этому типу пользователя раздел приложения.
export function loginDestination(from, kind = 'user') {
 const home = kind === 'student' ? '/student' : '/app'
 if (typeof from !== 'string' || !from.startsWith('/') || from.startsWith('//') || from.includes('\\')) return home
 const target = new URL(from, 'https://native.invalid')
 const path = target.pathname
 if (target.origin !== 'https://native.invalid' || !(
  path === home || path.startsWith(home + '/')
 )) return home
 return path + target.search + target.hash
}
