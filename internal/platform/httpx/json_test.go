package httpx

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeJSONConsumesWholeBody(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		limit      int64
		ok         bool
	}{
		{"ровно лимит", `{"n":1}`, 7, true},
		{"пробелы после объекта", "{\"n\":1}\n ", 10, true},
		{"второй объект", `{"n":1}{"n":2}`, 100, false},
		{"мусор после объекта", `{"n":1}x`, 100, false},
		{"объект за лимитом", `{"n":12}`, 7, false},
		{"хвост за лимитом", "{\"n\":1} ", 7, false},
		{"длинный хвост", "{\"n\":1}" + strings.Repeat(" ", 100), 8, false},
		{"неизвестное поле", `{"x":1}`, 100, false},
		{"пустое тело", "", 100, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/", strings.NewReader(tt.body))
			var out struct {
				N int `json:"n"`
			}
			err := DecodeJSON(r, &out, tt.limit)
			if (err == nil) != tt.ok {
				t.Fatalf("DecodeJSON: %v, ожидали успех=%v", err, tt.ok)
			}
		})
	}
}
