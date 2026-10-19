// Package live creates authenticated GPT-Live voice sessions. Audio travels
// directly between the browser and OpenAI; credentials and startup policy stay here.
package live

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/NATIVE/NATIVE/internal/iam"
	"github.com/NATIVE/NATIVE/internal/mlgw"
	"github.com/NATIVE/NATIVE/internal/platform/httpx"
	envsettings "github.com/NATIVE/NATIVE/internal/platform/settings"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

//go:embed session.json
var sessionConfig json.RawMessage

type API struct {
	key      string
	endpoint string
	client   *http.Client
	guard    *iam.Guard
	// Bound simultaneous outbound handshakes; ongoing audio is carried by WebRTC.
	slots   chan struct{}
	gateway *mlgw.Gateway
	monitor Recorder
	// release — адрес освобождения места голосовой сессии на нашем шлюзе.
	// Пустой, когда ходим в OpenAI напрямую: тогда освобождать нечего.
	release string
}

// sessionBudget — сколько всего ждём создания голосовой сессии.
//
// Живой голос обязан ходить в api.openai.com напрямую: зеркало (ProxyAPI)
// этот путь не отдаёт, там 404. А прямой путь идёт через туннель, и
// рукопожатие TLS в нём занимает от долей секунды до двадцати с лишним:
// пакеты теряются, и повтор приходит кратно пяти секундам. Прежних 25
// секунд хватало на само рукопожатие и не оставалось на ответ провайдера.
//
// Одна константа на клиент и на дедлайн запроса: раньше это были две
// независимые двадцатипятисекундные величины, и поднять можно было одну.
const sessionBudget = 60 * time.Second

// BaseURLEnv — переменная окружения с адресом провайдера голоса.
//
// По умолчанию — сам OpenAI. Российский сервер туда не пускают («Country,
// region, or territory not supported»), и там адрес указывает на наш
// зарубежный шлюз (cmd/gateway): https://<шлюз>/gw/openai/v1. Ключ при этом
// — токен шлюза, настоящий ключ OpenAI лежит только на зарубежном сервере.
const BaseURLEnv = "LIVE_API_BASE_URL"

func NewAPI(key string, guard *iam.Guard) *API {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSHandshakeTimeout = 30 * time.Second
	base := envsettings.String(BaseURLEnv)
	return &API{key: key, endpoint: sessionsEndpoint(base), release: releaseEndpoint(base),
		client: &http.Client{Timeout: sessionBudget, Transport: transport}, guard: guard,
		slots: make(chan struct{}, openConcurrency)}
}

// openConcurrency — сколько сессий открывается одновременно. Открытие —
// один запрос к провайдеру на 0,5–1 с; кому не хватило слота, тот ждёт его,
// а не получает отказ. При 8 слотах без очереди залп из 30 учеников (класс
// разом нажал «Начать») оставлял 19 без живого режима — нагрузочный тест
// 08.10.2026. Сколько сессий идёт одновременно, ограничивает шлюз, а не это.
const openConcurrency = 32

// openWait — сколько ждать слот, прежде чем честно отказать. Клиент ждёт
// рукопожатия 90 с (LIVE_HANDSHAKE_TIMEOUT_MS во фронтенде) — дольше, чем
// openWait и sessionBudget вместе.
var openWait = 20 * time.Second

// releaseEndpoint — где шлюз освобождает место сессии.
//
// Шлюз держит несколько ключей OpenAI, и на каждом ограниченное число
// голосовых сессий. Открытие он видит сам, а конец разговора — нет: звук
// идёт между браузером и провайдером напрямую. Поэтому о конце сообщаем мы.
// Базовый адрес шлюза — …/openai/v1; служебные ручки — рядом, в
// …/_gateway/. У самого OpenAI такой ручки нет, и тогда адрес пустой.
func releaseEndpoint(base string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	u, err := url.Parse(base)
	if base == "" || err != nil || u.Scheme != "https" || u.Host == "" || u.Host == "api.openai.com" {
		return ""
	}
	prefix, ok := strings.CutSuffix(u.Path, "/openai/v1")
	if !ok {
		return ""
	}
	u.Path = prefix + "/_gateway/live/release"
	return u.String()
}

// Release сообщает шлюзу, что голосовой разговор закончен и место на ключе
// свободно. Без этого место держалось бы до таймаута шлюза, и в школьный час
// новые ученики получали бы «линии заняты» при пустых линиях.
//
// Ошибку не возвращаем наверх: ученик уже закончил разговор, и сбой
// освобождения — забота шлюза (он снимет место сам через ttl), а не его.
func (a *API) Release(ctx context.Context, sessionID string) {
	sessionID = strings.TrimSpace(sessionID)
	if a.release == "" || sessionID == "" || len(sessionID) > 200 {
		return
	}
	body, _ := json.Marshal(map[string]string{"session_id": sessionID})
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.release, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+a.key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return
	}
	resp.Body.Close()
}

// sessionsEndpoint — адрес создания сессии от базового адреса провайдера.
// Пустой или негодный адрес — OpenAI напрямую: опечатка в настройке не
// должна уводить запросы с ключом неизвестно куда.
func sessionsEndpoint(base string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	u, err := url.Parse(base)
	if base == "" || err != nil || u.Scheme != "https" || u.Host == "" {
		base = envsettings.Default("LIVE_API_BASE_URL")
	}
	return base + "/live/sessions"
}

// WithGateway records and limits the session handshake in the shared ML gateway.
// Full voice duration and delegated token usage arrive over WebRTC, not this HTTP call.
func (a *API) WithGateway(gateway *mlgw.Gateway) *API {
	a.gateway = gateway
	return a
}

// WithMonitor подключает технический журнал голосовых соединений. Отчёт по
// нему строит скрипт пилота (scripts/pilot-report), отдельной ручки нет.
func (a *API) WithMonitor(monitor Recorder) *API {
	a.monitor = monitor
	return a
}

func (a *API) Mount(r chi.Router) {
	// Голосовую сессию открывает только урок (POST
	// /my/attempts/{id}/conversation/live): там она ограничена настройкой
	// учителя и счётчиком сессий.
	// Браузер сообщает только технический исход WebRTC. Реплики и звук не
	// проходят через эту ручку и не могут попасть в журнал доступности.
	a.guard.Handle(r, http.MethodPost, "/live/events", iam.PermAuthenticated, a.event)
}

// Session — то, что клиенту нужно для соединения. Всё остальное из ответа
// провайдера (секреты, инструкции) наружу не выходит.
type Session struct {
	ID  string
	SDP string
}

func (a *API) event(w http.ResponseWriter, r *http.Request) {
	if a.monitor == nil {
		httpx.Fail(w, r, httpx.Unavailable("Журнал голосовых соединений пока недоступен"))
		return
	}
	actor, ok := iam.ActorFrom(r.Context())
	if !ok {
		httpx.Fail(w, r, httpx.Unauthorized("Нужно войти"))
		return
	}
	var in struct {
		Stage    string `json:"stage"`
		Result   string `json:"result"`
		Protocol string `json:"protocol"`
		Network  string `json:"network"`
	}
	if err := httpx.DecodeJSON(r, &in, 4<<10); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	event, err := liveEvent(actor, in.Stage, in.Result, in.Protocol, in.Network)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if err := a.monitor.Record(r.Context(), event); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) openPolicy(ctx context.Context, orgID, userID uuid.UUID, sdp string, policy json.RawMessage) (Session, error) {
	if a.key == "" {
		return Session{}, httpx.Unavailable("Голосовая практика пока не настроена")
	}
	if !strings.HasPrefix(strings.TrimSpace(sdp), "v=0") || !strings.Contains(sdp, "m=audio ") {
		return Session{}, httpx.FieldError("sdp", "Не удалось подготовить аудиосоединение")
	}
	wait := time.NewTimer(openWait)
	defer wait.Stop()
	select {
	case a.slots <- struct{}{}:
		defer func() { <-a.slots }()
	case <-ctx.Done():
		return Session{}, httpx.Unavailable("Подключение отменено").Wrap(ctx.Err())
	case <-wait.C:
		return Session{}, httpx.RateLimited("Сейчас много подключений. Попробуйте чуть позже")
	}

	body, _ := json.Marshal(struct {
		Session   json.RawMessage   `json:"session"`
		Transport map[string]string `json:"transport"`
	}{policy, map[string]string{"type": "webrtc", "sdp": sdp}})

	ctx, cancel := context.WithTimeout(ctx, sessionBudget)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint, bytes.NewReader(body))
	if err != nil {
		return Session{}, httpx.Internal("Не удалось создать голосовую сессию")
	}
	req.Header.Set("Authorization", "Bearer "+a.key)
	req.Header.Set("Content-Type", "application/json")

	// No automatic retries: session creation is a billable operation.
	var resp *http.Response
	invoke := func(ctx context.Context) (mlgw.Usage, error) {
		var callErr error
		resp, callErr = a.client.Do(req.WithContext(ctx))
		usage := mlgw.Usage{Provider: "openai", Model: envsettings.String("LIVE_MODEL")}
		if callErr != nil {
			// Record a useful cause without URLs, proxy credentials or request data.
			return usage, transportError(callErr)
		}
		if callErr == nil && (resp.StatusCode < 200 || resp.StatusCode >= 300) {
			// Keep provider bodies out of logs as well as client responses.
			callErr = fmt.Errorf("OpenAI Live status %d", resp.StatusCode)
		}
		return usage, callErr
	}
	if a.gateway != nil {
		err = a.gateway.Do(ctx, mlgw.Call{Pipeline: "live", Stage: "session_create", Pool: mlgw.PoolLLMChat,
			OrgID: &orgID, UserID: &userID}, invoke)
	} else {
		_, err = invoke(ctx)
	}
	if err != nil && resp == nil {
		message := "Нет связи с голосовым сервисом. Попробуйте ещё раз"
		if errors.Is(err, context.DeadlineExceeded) {
			message = "Сервер не дождался ответа голосового сервиса. Попробуйте подключиться ещё раз"
		}
		return Session{}, httpx.Unavailable(message).Wrap(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := "Не удалось подключить голосовую практику. Попробуйте позже"
		if resp.StatusCode == http.StatusTooManyRequests {
			msg = "Голосовой сервис сейчас занят. Попробуйте чуть позже"
		}
		// Never forward provider bodies: these can contain credentials or policy details.
		return Session{}, httpx.Unavailable(msg).Wrap(fmt.Errorf("OpenAI Live status %d", resp.StatusCode))
	}
	var out struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
		Transport struct {
			Type string `json:"type"`
			SDP  string `json:"sdp"`
		} `json:"transport"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 128<<10)).Decode(&out); err != nil ||
		out.Session.ID == "" || out.Transport.Type != "webrtc" || out.Transport.SDP == "" {
		return Session{}, httpx.Unavailable("Голосовой сервис вернул неполный ответ")
	}
	return Session{ID: out.Session.ID, SDP: out.Transport.SDP}, nil
}

func transportError(err error) error {
	var networkError net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &networkError) && networkError.Timeout()) {
		return fmt.Errorf("OpenAI Live network timeout: %w", context.DeadlineExceeded)
	}
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("OpenAI Live request canceled: %w", context.Canceled)
	}
	var dnsError *net.DNSError
	if errors.As(err, &dnsError) {
		return errors.New("OpenAI Live DNS lookup failed")
	}
	return fmt.Errorf("OpenAI Live transport failed (%T)", err)
}
