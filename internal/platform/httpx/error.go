// Package httpx — единый контракт HTTP-слоя: формат ошибки, чтение и запись JSON.
//
// Формат ошибки один на все эндпоинты (NR-39):
//
//	{"error":{"code":"validation_failed","message":"…","fields":{…},"request_id":"…"}}
//
// Фронт показывает message пользователю; технические детали остаются в логе.
// request_id совпадает с идентификатором в логах и трейсе — по нему поддержка
// находит инцидент.
package httpx

import (
	"errors"
	"fmt"
	"net/http"
)

// Коды ошибок. Клиент ветвится по коду, а не по тексту сообщения.
const (
	CodeValidation   = "validation_failed"
	CodeBadRequest   = "bad_request"
	CodeUnauthorized = "unauthorized"
	CodeForbidden    = "forbidden"
	CodeNotFound     = "not_found"
	CodeConflict     = "conflict"
	CodeRateLimited  = "rate_limited"
	CodeUnavailable  = "unavailable"
	CodeInternal     = "internal_error"
)

// Error — ошибка, которую можно безопасно показать клиенту.
//
// Всё, что не является *Error, отдаётся как internal_error без подробностей:
// текст неизвестной ошибки может содержать данные, которые наружу не нужны.
type Error struct {
	Status  int
	Code    string
	Message string
	// Fields заполняется только для ошибок валидации: поле → причина.
	Fields map[string]string
	// Err — исходная ошибка для лога. Клиенту не показывается.
	Err error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.Err }

// Wrap прикрепляет исходную ошибку — она попадёт в лог, но не в ответ.
func (e *Error) Wrap(err error) *Error {
	e.Err = err
	return e
}

func newErr(status int, code, msg string) *Error {
	return &Error{Status: status, Code: code, Message: msg}
}

func BadRequest(msg string) *Error   { return newErr(http.StatusBadRequest, CodeBadRequest, msg) }
func Unauthorized(msg string) *Error { return newErr(http.StatusUnauthorized, CodeUnauthorized, msg) }
func Forbidden(msg string) *Error    { return newErr(http.StatusForbidden, CodeForbidden, msg) }
func Conflict(msg string) *Error     { return newErr(http.StatusConflict, CodeConflict, msg) }
func RateLimited(msg string) *Error  { return newErr(http.StatusTooManyRequests, CodeRateLimited, msg) }
func Unavailable(msg string) *Error {
	return newErr(http.StatusServiceUnavailable, CodeUnavailable, msg)
}
func Internal(msg string) *Error { return newErr(http.StatusInternalServerError, CodeInternal, msg) }

// NotFound. Чужой объект тоже отдаёт 404, а не 403: иначе по коду ответа
// перебором вычисляется, какие идентификаторы существуют (NR-10).
func NotFound(msg string) *Error { return newErr(http.StatusNotFound, CodeNotFound, msg) }

// Validation — 422 с разбивкой по полям.
func Validation(msg string, fields map[string]string) *Error {
	e := newErr(http.StatusUnprocessableEntity, CodeValidation, msg)
	e.Fields = fields
	return e
}

// FieldError — частый случай: одно неверное поле.
func FieldError(field, reason string) *Error {
	return Validation("Проверьте заполнение формы", map[string]string{field: reason})
}

// AsError приводит любую ошибку к *Error. Неизвестные ошибки становятся
// internal_error: наружу не утекает ничего, чего мы не решили показать.
func AsError(err error) *Error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return Internal("Не удалось выполнить запрос").Wrap(err)
}
