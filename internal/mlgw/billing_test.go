package mlgw

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/NATIVE/NATIVE/internal/config"
)

type billingRecorder struct{ records []Record }

func (r *billingRecorder) Record(v Record)             { r.records = append(r.records, v) }
func (r *billingRecorder) Close(context.Context) error { return nil }

func TestUsageOnFailedCallAndCurrency(t *testing.T) {
	rec := &billingRecorder{}
	g := New(config.MLConfig{}, rec)
	header := base64.StdEncoding.EncodeToString([]byte(`[{"provider":"openrouter","model":"test","currency":"USD","source":"api","cost":"0.00125","tokens_in":18,"tokens_out":5,"calls":2}]`))
	err := g.Do(context.Background(), Call{Pool: PoolLLMBuild}, func(ctx context.Context) (Usage, error) {
		CaptureUsage(ctx, header)
		return Usage{}, errors.New("Ошибка проверки формата")
	})
	if err == nil || len(rec.records) != 1 {
		t.Fatalf("не записана неудача: %v", err)
	}
	r := rec.records[0]
	if r.OK || r.TokensIn != 18 || len(r.Billing) != 1 || r.Billing[0].Currency != "USD" || *r.Billing[0].Cost != "0.00125" {
		t.Fatalf("учёт потерян: %+v", r)
	}
}

func TestMissingCostIsUnknownAndInvalidHeaderDoesNotFailCall(t *testing.T) {
	rec := &billingRecorder{}
	g := New(config.MLConfig{}, rec)
	for _, header := range []string{"", "не base64", base64.StdEncoding.EncodeToString([]byte(`[{"cost":"NaN","source":"api","calls":1}]`))} {
		if err := g.Do(context.Background(), Call{Pool: PoolLLMChat}, func(ctx context.Context) (Usage, error) { CaptureUsage(ctx, header); return Usage{}, nil }); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range rec.records {
		if len(r.Billing) != 1 || r.Billing[0].Cost != nil || r.Billing[0].Source != "unknown" {
			t.Fatalf("неизвестное принято за бесплатное: %+v", r.Billing)
		}
	}
}

func TestEmptyUsageDoesNotInventProviderCall(t *testing.T) {
	rec := &billingRecorder{}
	g := New(config.MLConfig{}, rec)
	err := g.Do(context.Background(), Call{Pool: PoolLLMBuild}, func(ctx context.Context) (Usage, error) {
		CaptureUsage(ctx, base64.StdEncoding.EncodeToString([]byte(`[]`)))
		return Usage{}, nil
	})
	if err != nil || len(rec.records) != 1 || len(rec.records[0].Billing) != 0 {
		t.Fatalf("создан несуществующий расход: %v %+v", err, rec.records)
	}
}
