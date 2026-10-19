package httpx

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// Адрес клиента нужен ровно для одного: считать попытки подбора паролей и
// кодов. Поэтому подделать его быть не должно.
//
// Обычное решение «взять X-Forwarded-For» подделывается тривиально: клиент
// сам присылает этот заголовок, меняет его на каждый запрос — и счётчик
// попыток перестаёт что-либо ограничивать. Поэтому заголовкам мы верим
// только тогда, когда соединение действительно пришло от нашего прокси.

type clientIPKey struct{}

// DefaultTrustedProxies — приватные диапазоны и loopback.
//
// В нашей схеме между клиентом и приложением стоит ровно один nginx, и он
// живёт в docker-сети. Ничего, кроме наших контейнеров, из этих диапазонов
// подключиться не может.
var DefaultTrustedProxies = mustPrefixes(
	"127.0.0.0/8", "::1/128",
	"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16",
	"fc00::/7",
)

// ParseTrustedProxies разбирает список CIDR из настройки.
// Нераспознанные элементы пропускаются: неверная строка в конфиге не должна
// ронять сервис, но и молча расширять доверие тоже не должна.
func ParseTrustedProxies(raw string) []netip.Prefix {
	var out []netip.Prefix
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if p, err := netip.ParsePrefix(part); err == nil {
			out = append(out, p)
		}
	}
	return out
}

// RealIP определяет адрес клиента и кладёт его в контекст.
//
// Порядок такой:
//  1. Берём адрес того, кто реально подключился.
//  2. Если он не из доверенного диапазона — это и есть клиент, заголовки
//     игнорируем полностью.
//  3. Если из доверенного — идём по X-Forwarded-For справа налево и берём
//     первый адрес, который не наш прокси. Справа налево потому, что левую
//     часть цепочки клиент дописывает сам.
func RealIP(trusted []netip.Prefix) func(http.Handler) http.Handler {
	if trusted == nil {
		trusted = DefaultTrustedProxies
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			addr := resolveClientIP(r, trusted)
			ctx := r.Context()
			if addr != nil {
				ctx = context.WithValue(ctx, clientIPKey{}, *addr)
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// ClientIP возвращает адрес клиента или nil, если определить не удалось.
//
// nil — допустимое значение: колонка inet его принимает, а счётчик попыток
// в этом случае работает по паре (ключ, NULL). Молча подставлять чужой
// адрес было бы хуже.
func ClientIP(ctx context.Context) *netip.Addr {
	if v, ok := ctx.Value(clientIPKey{}).(netip.Addr); ok {
		return &v
	}
	return nil
}

func resolveClientIP(r *http.Request, trusted []netip.Prefix) *netip.Addr {
	peer := parseAddr(r.RemoteAddr)
	if peer == nil || !inAny(*peer, trusted) {
		return peer
	}

	forwarded := r.Header.Get("X-Forwarded-For")
	parts := strings.Split(forwarded, ",")
	for i := len(parts) - 1; i >= 0; i-- {
		addr := parseAddr(strings.TrimSpace(parts[i]))
		if addr != nil && !inAny(*addr, trusted) {
			return addr
		}
	}

	// Наш nginx выставляет X-Real-IP из $remote_addr — это последний рубеж,
	// если цепочка X-Forwarded-For состоит из одних доверенных адресов.
	if addr := parseAddr(strings.TrimSpace(r.Header.Get("X-Real-IP"))); addr != nil {
		return addr
	}
	return peer
}

func parseAddr(s string) *netip.Addr {
	if s == "" {
		return nil
	}
	if host, _, err := net.SplitHostPort(s); err == nil {
		s = host
	}
	s = strings.Trim(s, "[]")
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return nil
	}
	addr = addr.Unmap()
	return &addr
}

func inAny(addr netip.Addr, prefixes []netip.Prefix) bool {
	for _, p := range prefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

func mustPrefixes(list ...string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(list))
	for _, s := range list {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			panic("httpx: неверный CIDR в списке доверенных прокси: " + s)
		}
		out = append(out, p)
	}
	return out
}
