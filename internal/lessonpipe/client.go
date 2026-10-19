package lessonpipe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/NATIVE/NATIVE/internal/mlgw"
	"github.com/NATIVE/NATIVE/internal/platform/httpx"
)

// Client — HTTP-клиент к llm_service.
//
// Промпты, выбор провайдера, валидация схем и ретраи живут в Python-сервисе.
// Go со стороны нового контура отвечает за другое: очередь и приоритеты
// (mlgw), права (iam), сохранение результата (content) и учёт стоимости.
//
// Разделение осознанное. Промпты меняются каждую неделю и требуют
// лингвиста, а не сборки Go-бинарника; ограничения ёмкости и права
// меняются редко и должны быть в одном месте с остальным бэкендом.
type Client struct {
	baseURL string
	http    *http.Client
}

// NewClient создаёт клиента.
//
// Таймаут запроса большой: разбор разговора — это несколько последовательных
// обращений к модели внутри Python-сервиса. Ограничение сверху всё равно
// нужно, иначе зависший апстрим держит слот в пуле до конца жизни процесса.
func NewClient(baseURL string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http: &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 60 * time.Second}).DialContext,
				MaxIdleConns:          32,
				MaxIdleConnsPerHost:   16,
				IdleConnTimeout:       120 * time.Second,
				TLSHandshakeTimeout:   10 * time.Second,
				ResponseHeaderTimeout: timeout,
			},
		},
	}
}

// ---------------------------------------------------------------------------
// Контракт llm_service. Имена полей повторяют его схемы 1:1.
// ---------------------------------------------------------------------------

// Сценарии генерации.
const (
	ScenarioMaterial = "material"
	ScenarioTopic    = "topic"
)

// ---------------------------------------------------------------------------
// Вызовы
// ---------------------------------------------------------------------------

// post отправляет JSON и разбирает ответ. Возвращает и сырое тело — оно
// нужно там, где ответ надо передать обратно без потерь.
func (c *Client) post(ctx context.Context, path string, in, out any) (json.RawMessage, error) {
	status, raw, err := c.exchange(ctx, http.MethodPost, path, in)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, upstreamError(status, raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return nil, httpx.Internal("Не удалось разобрать ответ ML-сервиса").Wrap(err)
		}
	}
	return raw, nil
}

// exchange отправляет запрос и отдаёт код и тело ответа как есть.
//
// Отдельно от post ради разговора: там отказ 409 — не сбой сервиса, а
// осмысленный ответ движка («этот ход уже применён»), и вызывающему нужен
// сам код, а не готовый конверт ошибки. in == nil — запрос без тела.
func (c *Client) exchange(ctx context.Context, method, path string, in any) (int, []byte, error) {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return 0, nil, httpx.Internal("Не удалось собрать запрос к ML-сервису").Wrap(err)
		}
		body = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return 0, nil, httpx.Internal("Не удалось обратиться к ML-сервису").Wrap(err)
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		// Недоступность апстрима — это 503, а не 500: клиенту стоит повторить,
		// а нам не стоит поднимать тревогу как на своей ошибке.
		return 0, nil, httpx.Unavailable("ML-сервис недоступен, попробуйте ещё раз").Wrap(err)
	}
	defer resp.Body.Close()

	// Расход модели приходит заголовком и при отказе: неудачная проверка
	// после успешной генерации тоже стоила денег.
	mlgw.CaptureUsage(ctx, resp.Header.Get(mlgw.UsageHeader))
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return 0, nil, httpx.Internal("Не удалось прочитать ответ ML-сервиса").Wrap(err)
	}
	return resp.StatusCode, raw, nil
}

// upstreamError переводит отказ Python-сервиса в наш конверт ошибки.
//
// 422 от него — это претензия к тому, что прислал учитель (неподдерживаемый
// уровень, пустой текст, недоступный веб-поиск по теме). Такое надо показать
// человеку дословно, а не прятать за «внутренней ошибкой».
func upstreamError(status int, raw []byte) error {
	detail := detailOf(raw)

	switch {
	case status == http.StatusUnprocessableEntity || status == http.StatusBadRequest:
		return httpx.Validation(humanize(detail), map[string]string{"input": detail})
	case status == http.StatusTooManyRequests:
		return httpx.RateLimited("Слишком много запросов к модели, попробуйте позже")
	case status >= 500:
		var failure struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(raw, &failure)
		message := "ML-сервис не смог обработать запрос"
		switch failure.Code {
		case "provider_timeout":
			message = "Сервис генерации не ответил вовремя. Материал сохранён — попробуйте позже."
		case "provider_connection":
			message = "Нет соединения с сервисом генерации. Материал сохранён — попробуйте позже."
		case "provider_balance":
			message = "Недостаточно средств на балансе сервиса генерации. Материал сохранён — обратитесь к администратору."
		}
		return httpx.Unavailable(message).
			Wrap(fmt.Errorf("статус %d: %s", status, detail))
	default:
		return httpx.Internal("ML-сервис вернул неожиданный ответ").
			Wrap(fmt.Errorf("статус %d: %s", status, detail))
	}
}

// detailOf вытаскивает из ответа читаемое описание ошибки.
//
// FastAPI отдаёт валидацию массивом объектов с полями loc, msg, type.
// Показывать этот JSON учителю нельзя, поэтому склеиваем только msg.
func detailOf(raw []byte) string {
	var withDetail struct {
		Detail json.RawMessage `json:"detail"`
	}
	if json.Unmarshal(raw, &withDetail) != nil || len(withDetail.Detail) == 0 {
		return strings.TrimSpace(string(raw))
	}

	// Строкой отвечают наши же HTTPException — их и показываем как есть.
	var asString string
	if json.Unmarshal(withDetail.Detail, &asString) == nil {
		return asString
	}

	var items []struct {
		Msg string `json:"msg"`
	}
	if json.Unmarshal(withDetail.Detail, &items) == nil && len(items) > 0 {
		msgs := make([]string, 0, len(items))
		for _, it := range items {
			if m := strings.TrimSpace(strings.TrimPrefix(it.Msg, "Value error, ")); m != "" {
				msgs = append(msgs, m)
			}
		}
		if len(msgs) > 0 {
			return strings.Join(msgs, "; ")
		}
	}
	return strings.TrimSpace(string(withDetail.Detail))
}

// humanize переводит известные отказы ML-сервиса на язык учителя.
//
// Сообщения приходят на английском из его схем. Переводим только те два,
// в которые реально можно упереться из интерфейса; остальное показываем
// как есть — лучше английский текст, чем «внутренняя ошибка».
//
// Оба этих случая интерфейс обязан не допускать: /ml/capabilities отдаёт
// difficulty_support, по которому видно, каким языкам уровень положен.
// Это подстраховка, а не основной путь.
func humanize(detail string) string {
	switch {
	case strings.Contains(detail, "level is required for tatar"):
		return "Для татарского нужно выбрать уровень"
	case strings.Contains(detail, "levels are currently supported only for tatar"):
		return "Уровень пока задаётся только для татарского"
	case strings.Contains(detail, "unsupported language"):
		return "Этот язык пока не поддерживается"
	case detail == "":
		return "ML-сервис не принял материал"
	default:
		return detail
	}
}

// ---------------------------------------------------------------------------
// Аватар
// ---------------------------------------------------------------------------
//
// Состояние диалога хранит клиент (у нас — таблица learning.dialog_turns),
// а llm_service принимает его целиком в каждом запросе. Так сервис остаётся
// без памяти, и его можно масштабировать репликами, не думая о том, на какой
// из них лежит чей разговор.

// transcriptLimits — сколько расшифровки уходит в разбор.
//
// Реплики голосового разговора присылает браузер, и верить их числу и
// длине нельзя: разбор не должен превращаться в способ отправить модели
// мегабайт текста за чужой счёт.
const (
	transcriptMaxTurns    = 60
	transcriptMaxRunes    = 12000
	transcriptTurnMaxRune = 1000
)
