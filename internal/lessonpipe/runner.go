package lessonpipe

import (
	"github.com/NATIVE/NATIVE/internal/mlgw"
	"github.com/google/uuid"
)

// Runner — вызовы llm_service под контролем пулов и учёта.
//
// Движок разговора живёт в Python. Runner добавляет к нему то, чего у
// отдельного сервиса нет и быть не должно: ограничение параллельности по
// приоритетам и запись каждого вызова в ops.pipeline_calls.
//
// Зачем это здесь, а не в Python: пулы делятся между всеми потребителями
// моделей — разговором ученика, распознаванием, синтезом. Решать, кто кого
// ждёт, можно только там, где видно всех сразу.
type Runner struct {
	client *Client
	gw     *mlgw.Gateway
}

func NewRunner(client *Client, gw *mlgw.Gateway) *Runner {
	return &Runner{client: client, gw: gw}
}

// nilable превращает нулевой UUID в отсутствующее значение.
//
// В ops.pipeline_calls эти колонки допускают NULL: нулевой UUID там означал
// бы «есть такой объект», и выборки по организации ловили бы призрак.
func nilable(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}

// usage задаёт резервную атрибуцию при транспортном обрыве. Токены и расходы
// шлюз берёт из X-Native-Usage, включая неудачные попытки внутри Python.
func (r *Runner) usage() mlgw.Usage {
	return mlgw.Usage{Provider: "llm_service", Model: ""}
}
