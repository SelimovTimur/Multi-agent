package httpx

import (
	"net/http/httptest"
	"testing"
)

// Проверяем главное: подделать адрес заголовком нельзя, если запрос пришёл
// не от доверенного прокси. Иначе счётчик попыток подбора обходится сменой
// одного заголовка на каждый запрос.
func TestClientIP(t *testing.T) {
	cases := []struct {
		name       string
		remoteAddr string
		forwarded  string
		realIP     string
		want       string
	}{
		{
			name:       "прямое подключение, заголовок игнорируется",
			remoteAddr: "203.0.113.7:51234",
			forwarded:  "1.2.3.4",
			want:       "203.0.113.7",
		},
		{
			name:       "за доверенным прокси берём адрес из заголовка",
			remoteAddr: "172.18.0.5:40000",
			forwarded:  "203.0.113.7",
			want:       "203.0.113.7",
		},
		{
			name:       "клиент дописал слева свой адрес — берём правый настоящий",
			remoteAddr: "172.18.0.5:40000",
			forwarded:  "1.2.3.4, 203.0.113.7",
			want:       "203.0.113.7",
		},
		{
			name:       "цепочка из доверенных адресов — падаем на X-Real-IP",
			remoteAddr: "172.18.0.5:40000",
			forwarded:  "10.0.0.1, 172.18.0.5",
			realIP:     "203.0.113.7",
			want:       "203.0.113.7",
		},
		{
			name:       "заголовков нет — адрес прокси",
			remoteAddr: "172.18.0.5:40000",
			want:       "172.18.0.5",
		},
		{
			name:       "мусор в заголовке не подменяет адрес",
			remoteAddr: "172.18.0.5:40000",
			forwarded:  "не-адрес",
			want:       "172.18.0.5",
		},
		{
			name:       "IPv6 в скобках с портом",
			remoteAddr: "[2001:db8::1]:40000",
			forwarded:  "203.0.113.7",
			want:       "2001:db8::1",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = c.remoteAddr
			if c.forwarded != "" {
				r.Header.Set("X-Forwarded-For", c.forwarded)
			}
			if c.realIP != "" {
				r.Header.Set("X-Real-IP", c.realIP)
			}

			addr := resolveClientIP(r, DefaultTrustedProxies)
			if addr == nil {
				t.Fatalf("адрес не определён, ожидали %s", c.want)
			}
			if addr.String() != c.want {
				t.Errorf("получили %s, ожидали %s", addr, c.want)
			}
		})
	}
}

func TestParseTrustedProxies(t *testing.T) {
	list := ParseTrustedProxies("10.0.0.0/8, мусор, 192.168.0.0/16")
	if len(list) != 2 {
		t.Fatalf("ожидали 2 диапазона, получили %d: %v", len(list), list)
	}
	// Неразобранная строка не должна расширять доверие — она просто пропущена.
	if list[0].String() != "10.0.0.0/8" || list[1].String() != "192.168.0.0/16" {
		t.Errorf("неверный разбор списка: %v", list)
	}
}
