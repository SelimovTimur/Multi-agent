package iam

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/NATIVE/NATIVE/internal/platform/httpx"
)

// Cookie стирается только при настоящем отказе в токене. Сбой базы под
// нагрузкой раньше стирал её тоже: ученик терял вход, заново входил по коду
// класса и получал второй профиль.
func TestCookieClearedOnlyWhenTokenRejected(t *testing.T) {
	if !authRejected(httpx.Unauthorized("Сессия истекла")) {
		t.Error("истёкший токен — отказ, cookie чистим")
	}
	if authRejected(httpx.Internal("Не удалось проверить доступ").Wrap(errors.New("timeout"))) {
		t.Error("сбой базы — не отказ, cookie не трогаем")
	}
	if authRejected(errors.New("context deadline exceeded")) {
		t.Error("неизвестная ошибка — не отказ")
	}

	rec := httptest.NewRecorder()
	failUnavailable(rec, httptest.NewRequest(http.MethodGet, "/api/v1/my/lessons", nil), errors.New("pool"))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("при сбое проверки — 503, получили %d", rec.Code)
	}
	if rec.Header().Get("Set-Cookie") != "" {
		t.Error("при сбое проверки cookie не трогаем")
	}
}
