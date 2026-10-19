package mlgw

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCancelledCallDoesNotAcquireFreeSlot(t *testing.T) {
	p := NewPool("test", 1, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if release, _, err := p.Acquire(ctx); !errors.Is(err, context.Canceled) || release != nil {
		t.Fatalf("отменённый вызов занял слот: %v", err)
	}
	if st := p.Stats(); st.InFlight != 0 || st.Total != 0 || st.Waiting != 0 || st.Rejected != 1 {
		t.Fatalf("счётчики отменённого вызова: %+v", st)
	}
	release, _, err := p.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	release()
	release()
	if p.Stats().InFlight != 0 {
		t.Fatal("двойное освобождение изменило число слотов")
	}
}
