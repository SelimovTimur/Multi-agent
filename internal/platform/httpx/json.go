package httpx

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/NATIVE/NATIVE/internal/platform/logx"
)

type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code      string            `json:"code"`
	Message   string            `json:"message"`
	Fields    map[string]string `json:"fields,omitempty"`
	RequestID string            `json:"request_id"`
}

// JSON пишет успешный ответ.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if v == nil || status == http.StatusNoContent {
		w.WriteHeader(status)
		return
	}
	body, err := json.Marshal(v)
	if err != nil {
		// Сериализация ответа сломалась уже после того, как обработчик отработал.
		// Отдаём 500 в том же формате, чтобы клиент не получил пустое тело.
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"code":"internal_error","message":"Не удалось сформировать ответ","request_id":""}}`))
		return
	}
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// Fail пишет ошибку в едином формате и логирует её.
//
// Клиент получает message; в лог уходит обёрнутая исходная ошибка.
func Fail(w http.ResponseWriter, r *http.Request, err error) {
	e := AsError(err)
	if e == nil {
		return
	}
	ctx := r.Context()
	reqID := logx.RequestID(ctx)

	log := logx.From(ctx).With("code", e.Code, "status", e.Status, "path", r.URL.Path)
	if e.Status >= http.StatusInternalServerError {
		log.Error("запрос завершился ошибкой", "err", e.Error())
	} else {
		log.Info("запрос отклонён", "reason", e.Message)
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(e.Status)
	_ = json.NewEncoder(w).Encode(errorEnvelope{Error: errorBody{
		Code:      e.Code,
		Message:   e.Message,
		Fields:    e.Fields,
		RequestID: reqID,
	}})
}

// DecodeJSON читает тело запроса в dst.
//
// Тело ограничено limit байт: без ограничения любой клиент может занять
// память процесса одним запросом. Неизвестные поля запрещены — опечатка в
// имени поля должна быть видна сразу, а не молча игнорироваться.
func DecodeJSON(r *http.Request, dst any, limit int64) error {
	if limit <= 0 {
		limit = 1 << 20 // 1 МБ по умолчанию
	}
	// Один дополнительный байт отличает конец тела от обрезания по лимиту.
	body := &io.LimitedReader{R: r.Body, N: limit + 1}
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return BadRequest("Тело запроса пустое")
		}
		return BadRequest("Не удалось разобрать тело запроса").Wrap(err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return BadRequest("Тело запроса должно содержать один JSON-объект").Wrap(err)
	}
	if body.N == 0 {
		return BadRequest("Тело запроса слишком большое")
	}
	return nil
}
