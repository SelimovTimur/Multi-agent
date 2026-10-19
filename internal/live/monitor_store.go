package live

import (
	"context"
	"fmt"

	"github.com/NATIVE/NATIVE/internal/platform/pg"
	"github.com/google/uuid"
)

// Monitor хранит только технические исходы соединения. Org и участник
// снимком приходят из уже аутентифицированного запроса, поэтому таблицы
// учебного модуля ему не нужны.
type Monitor struct {
	q pg.Querier
}

func NewMonitor(q pg.Querier) *Monitor { return &Monitor{q: q} }

func (m *Monitor) Record(ctx context.Context, event Event) error {
	_, err := m.q.Exec(ctx, `
		INSERT INTO ops.live_events
			(id, org_id, actor_id, actor_role, stage, result, protocol, network)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		uuid.New(), event.OrgID, event.ActorID, event.ActorRole,
		event.Stage, event.Result, event.Protocol, event.Network)
	if err != nil {
		return fmt.Errorf("live: не удалось записать состояние голосового соединения: %w", err)
	}
	return nil
}
