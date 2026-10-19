package httpx

import (
	"log/slog"
	"net/http"

	"github.com/NATIVE/NATIVE/internal/platform/idgen"
	"github.com/NATIVE/NATIVE/internal/platform/logx"
)

// HeaderRequestID — заголовок сквозного идентификатора запроса.
const HeaderRequestID = "X-Request-ID"

// RequestContext кладёт в контекст идентификатор запроса и логгер.
//
// Идентификатор берётся из входящего заголовка, если он есть: так запрос,
// прошедший через edge и несколько сервисов, склеивается в логах в одну
// цепочку. Иначе генерируется новый.
//
// Тот же идентификатор уходит клиенту — в заголовке и в теле ошибки. Это то
// единственное, что пользователь может назвать поддержке, чтобы та нашла
// инцидент.
func RequestContext(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get(HeaderRequestID)
			if id == "" {
				id = idgen.NewString()
			}
			w.Header().Set(HeaderRequestID, id)

			ctx := logx.WithRequestID(r.Context(), id)
			ctx = logx.Into(ctx, log)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
