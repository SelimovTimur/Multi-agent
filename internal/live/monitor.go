package live

import (
	"context"
	"strings"

	"github.com/NATIVE/NATIVE/internal/iam"
	"github.com/NATIVE/NATIVE/internal/platform/httpx"
	"github.com/google/uuid"
)

// Event — технический след жизненного цикла GPT Live. Текста, звука и
// сообщения ошибки здесь нет: для разбора доступности достаточно этапа,
// протокола страницы и признака сети, а содержимое разговора к диагностике
// не относится.
type Event struct {
	OrgID     uuid.UUID
	ActorID   uuid.UUID
	ActorRole string
	Stage     string
	Result    string
	// Protocol — протокол, по которому браузер получил страницу (h2,
	// http/1.1…). Школьный фильтр-посредник виден именно по http/1.1.
	Protocol string
	Network  string
}

// Recorder хранит клиентские сигналы о соединении.
type Recorder interface {
	Record(context.Context, Event) error
}

// pageProtocols — значения nextHopProtocol из Navigation Timing. Пустая
// строка — браузер протокол не сообщил.
var pageProtocols = map[string]bool{"": true, "http/0.9": true, "http/1.0": true, "http/1.1": true, "h2": true, "h2c": true, "h3": true}

func liveEvent(actor iam.Actor, stage, result, protocol, network string) (Event, error) {
	stage = strings.TrimSpace(stage)
	result = strings.TrimSpace(result)
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	network = strings.TrimSpace(network)
	if stage != "start" && stage != "connect" && stage != "first_audio" && stage != "session" {
		return Event{}, httpx.FieldError("stage", "Неизвестный этап голосового соединения")
	}
	if result != "ok" && result != "failed" {
		return Event{}, httpx.FieldError("result", "Результат соединения — ok или failed")
	}
	// Закрытый список, а не свободная строка: иначе в журнал можно было бы
	// положить произвольный текст.
	if !pageProtocols[protocol] {
		return Event{}, httpx.FieldError("protocol", "Неизвестный протокол страницы")
	}
	if !knownNetwork(network) {
		return Event{}, httpx.FieldError("network", "Неизвестный признак сети")
	}

	event := Event{OrgID: actor.OrgID, Stage: stage, Result: result, Protocol: protocol, Network: network}
	if actor.IsStudent() {
		event.ActorID, event.ActorRole = actor.StudentID, "student"
	} else {
		event.ActorID, event.ActorRole = actor.UserID, actor.Role
	}
	return event, nil
}

func knownNetwork(network string) bool {
	if network == "online" || network == "offline" {
		return true
	}
	prefix, kind, ok := strings.Cut(network, ":")
	if !ok || (prefix != "online" && prefix != "offline") {
		return false
	}
	return kind == "slow-2g" || kind == "2g" || kind == "3g" || kind == "4g"
}
