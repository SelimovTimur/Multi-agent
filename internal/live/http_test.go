package live

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/NATIVE/NATIVE/internal/config"
	"github.com/NATIVE/NATIVE/internal/iam"
	"github.com/NATIVE/NATIVE/internal/platform/httpx"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// open открывает сессию разговора и пишет ответ так, как его увидит браузер:
// сессию или отказ в конверте ошибок API.
func open(a *API, sdp string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	session, err := a.OpenConversation(context.Background(), uuid.Nil, uuid.Nil, sdp, ConversationBrief{})
	if err != nil {
		httpx.Fail(w, httptest.NewRequest("POST", "/", nil), err)
		return w
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{
		"session":   map[string]string{"id": session.ID},
		"transport": map[string]string{"type": "webrtc", "sdp": session.SDP},
	})
	return w
}

func TestNewAPIAllowsSlowTLSHandshakeWithinSessionDeadline(t *testing.T) {
	a := NewAPI("key", nil)
	transport, ok := a.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("live client transport = %T, want *http.Transport", a.client.Transport)
	}
	if transport.TLSHandshakeTimeout != 30*time.Second {
		t.Fatalf("TLS handshake timeout = %s, want 30s", transport.TLSHandshakeTimeout)
	}
	if a.client.Timeout != sessionBudget {
		t.Fatalf("client timeout = %s, want %s", a.client.Timeout, sessionBudget)
	}
	// Туннель, через который ходит живой голос, пожимает руки по 5-21 секунде.
	// Прежние 25 секунд не оставляли провайдеру времени собрать сессию.
	if sessionBudget != 60*time.Second {
		t.Fatalf("session budget = %s, want 60s", sessionBudget)
	}
}

// Дедлайн запроса обязан совпадать с бюджетом клиента: раньше две
// двадцатипятисекундные константы жили порознь, и поднять было легко одну.
func TestRequestDeadlineMatchesSessionBudget(t *testing.T) {
	a := NewAPI("key", nil)
	var left time.Duration
	a.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		deadline, ok := r.Context().Deadline()
		if !ok {
			t.Fatal("у запроса нет дедлайна")
		}
		left = time.Until(deadline)
		return nil, context.DeadlineExceeded
	})
	_, _ = a.OpenConversation(context.Background(), uuid.Nil, uuid.Nil, "v=0\nm=audio 9", ConversationBrief{})
	if left < sessionBudget-5*time.Second || left > sessionBudget {
		t.Fatalf("дедлайн запроса = %s, ожидался около %s", left, sessionBudget)
	}
}

func TestSessionUsesServerPolicyAndRedactsResponse(t *testing.T) {
	a := NewAPI("test-server-key", nil)
	a.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://api.openai.com/v1/live/sessions" || r.Header.Get("Authorization") != "Bearer test-server-key" {
			t.Fatal("wrong endpoint or authentication")
		}
		var body struct {
			Session   map[string]any    `json:"session"`
			Transport map[string]string `json:"transport"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		backend := body.Session["delegation"].(map[string]any)["responses"].(map[string]any)
		if body.Session["model"] != "gpt-live-1" || backend["model"] != "gpt-5.4" || backend["reasoning"].(map[string]any)["effort"] != "medium" || backend["tool_choice"] != "none" || len(backend["tools"].([]any)) != 0 {
			t.Fatal("voice/backend policy differs from requested configuration")
		}
		if body.Transport["type"] != "webrtc" || !strings.Contains(body.Transport["sdp"], "m=audio") {
			t.Fatal("missing offer")
		}
		return &http.Response{StatusCode: 201, Body: io.NopCloser(strings.NewReader(`{"session":{"id":"live_test","client_secret":"secret","instructions":"private"},"transport":{"type":"webrtc","sdp":"answer"},"secret":"private"}`))}, nil
	})
	w := open(a, "v=0\r\nm=audio 9 UDP/TLS/RTP/SAVPF 111\r\n")
	if w.Code != 201 || strings.Contains(w.Body.String(), "secret") || strings.Contains(w.Body.String(), "private") {
		t.Fatalf("unsafe/wrong response: %d %s", w.Code, w.Body.String())
	}
}

func TestProviderFailureIsNotLeakedOrRetried(t *testing.T) {
	a := NewAPI("key", nil)
	calls := 0
	a.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 401, Body: io.NopCloser(strings.NewReader(`{"error":"secret-key-details"}`))}, nil
	})
	w := open(a, "v=0\nm=audio 9")
	if w.Code != 503 || calls != 1 || strings.Contains(w.Body.String(), "secret") {
		t.Fatalf("failure handling: %d calls=%d", w.Code, calls)
	}
}

func TestRouteRequiresAuthentication(t *testing.T) {
	g := iam.NewGuard(nil, config.AuthConfig{})
	r := chi.NewRouter()
	NewAPI("key", g).Mount(r)
	if err := g.Verify(r); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/live/events", strings.NewReader(`{}`)))
	if w.Code != 401 {
		t.Fatalf("unauthenticated status: %d", w.Code)
	}
}

func TestNetworkFailureIsDiagnosedWithoutSecretsOrRetry(t *testing.T) {
	for _, cause := range []error{context.DeadlineExceeded, errors.New("secret-provider-details")} {
		t.Run(cause.Error(), func(t *testing.T) {
			a := NewAPI("key", nil)
			calls := 0
			a.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return nil, &url.Error{Op: "Post", URL: "https://secret:credential@example.com", Err: cause}
			})
			w := open(a, "v=0\nm=audio 9")
			if w.Code != 503 || calls != 1 || strings.Contains(w.Body.String(), "secret") {
				t.Fatalf("network failure handling: status=%d calls=%d", w.Code, calls)
			}
			if errors.Is(cause, context.DeadlineExceeded) && !strings.Contains(w.Body.String(), "не дождался") {
				t.Fatal("timeout must have a specific user message")
			}
			if strings.Contains(transportError(cause).Error(), "secret") {
				t.Fatal("transport diagnostics exposed provider details")
			}
		})
	}
}

// Российский сервер ходит к провайдеру голоса через зарубежный шлюз: адрес
// задаётся окружением. Опечатка в нём не должна уводить запрос с ключом
// неизвестно куда — тогда остаётся OpenAI напрямую.
func TestSessionsEndpointFollowsBaseURL(t *testing.T) {
	cases := map[string]string{
		"":                                    "https://api.openai.com/v1/live/sessions",
		"https://213.21.235.212/gw/openai/v1": "https://213.21.235.212/gw/openai/v1/live/sessions",
		"https://gw.example/openai/v1/":       "https://gw.example/openai/v1/live/sessions",
		"http://insecure.example/v1":          "https://api.openai.com/v1/live/sessions",
		"не адрес":                            "https://api.openai.com/v1/live/sessions",
	}
	for base, want := range cases {
		if got := sessionsEndpoint(base); got != want {
			t.Errorf("%q → %q, ожидали %q", base, got, want)
		}
	}
}

// Место голосовой сессии освобождается на нашем шлюзе. У самого OpenAI
// такой ручки нет — туда ничего не отправляем.
func TestReleaseEndpointOnlyForGateway(t *testing.T) {
	cases := map[string]string{
		"":                                      "",
		"https://api.openai.com/v1":             "",
		"https://213.21.235.212:8443/openai/v1": "https://213.21.235.212:8443/_gateway/live/release",
		"https://213.21.235.212/gw/openai/v1/":  "https://213.21.235.212/gw/_gateway/live/release",
		"https://proxy.example/other/v1":        "",
		"http://213.21.235.212:8443/openai/v1":  "",
	}
	for base, want := range cases {
		if got := releaseEndpoint(base); got != want {
			t.Errorf("%q → %q, ожидали %q", base, got, want)
		}
	}
}

// Класс разом нажимает «Начать»: кому не хватило слота, тот ждёт его, а не
// получает отказ (нагрузочный тест 08.10.2026: при 8 слотах без очереди из
// 30 одновременных открытий прошли 11).
func TestOpenWaitsForSlotInsteadOfRejecting(t *testing.T) {
	a := NewAPI("test-server-key", nil)
	a.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 201, Body: io.NopCloser(strings.NewReader(
			`{"session":{"id":"live_wait"},"transport":{"type":"webrtc","sdp":"answer"}}`))}, nil
	})
	for i := 0; i < cap(a.slots); i++ {
		a.slots <- struct{}{} // все слоты заняты чужими открытиями
	}
	offer := "v=0\r\nm=audio 9 UDP/TLS/RTP/SAVPF 111\r\n"
	done := make(chan error, 1)
	go func() {
		_, err := a.OpenConversation(context.Background(), uuid.New(), uuid.New(), offer, ConversationBrief{})
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("открытие не дождалось слота: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	<-a.slots // одно открытие закончилось
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("после освобождения слота открытие не прошло: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("открытие так и не получило освободившийся слот")
	}
}

func TestOpenGivesUpAfterWaitingTooLong(t *testing.T) {
	a := NewAPI("test-server-key", nil)
	prev := openWait
	openWait = 50 * time.Millisecond
	t.Cleanup(func() { openWait = prev })
	for i := 0; i < cap(a.slots); i++ {
		a.slots <- struct{}{}
	}
	_, err := a.OpenConversation(context.Background(), uuid.New(), uuid.New(), "v=0\r\nm=audio 9 UDP/TLS/RTP/SAVPF 111\r\n", ConversationBrief{})
	if err == nil || !strings.Contains(err.Error(), "много подключений") {
		t.Fatalf("ожидался честный отказ после ожидания: %v", err)
	}
}
