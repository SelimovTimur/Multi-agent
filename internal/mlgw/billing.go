package mlgw

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"sync"
)

var decimalCost = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?$`)

const UsageHeader = "X-Native-Usage"

// Billing сохраняет валюту провайдера: долларовые credits нельзя складывать
// с историческим cost_micro в рублях. nil Cost означает неизвестную стоимость.
type Billing struct {
	Provider  string  `json:"provider"`
	Model     string  `json:"model"`
	Currency  string  `json:"currency"`
	Source    string  `json:"source"`
	Cost      *string `json:"cost"`
	TokensIn  int     `json:"tokens_in"`
	TokensOut int     `json:"tokens_out"`
	Calls     int     `json:"calls"`
}

type billingKey struct{}
type billingCapture struct {
	mu       sync.Mutex
	rows     []Billing
	received bool
}

// CaptureUsage вызывается транспортом даже при 4xx/5xx. Неудачный аудит
// после успешной генерации тоже мог потратить деньги.
func CaptureUsage(ctx context.Context, encoded string) {
	capture, _ := ctx.Value(billingKey{}).(*billingCapture)
	if capture == nil || encoded == "" || len(encoded) > 24000 {
		return
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return
	}
	var rows []Billing
	if json.Unmarshal(data, &rows) != nil || len(rows) > 64 {
		return
	}
	for _, row := range rows {
		if row.Calls < 1 || row.Calls > 10000 || row.TokensIn < 0 || row.TokensOut < 0 || len(row.Model) > 256 || len(row.Provider) > 64 {
			return
		}
		if row.Source != "api" && row.Source != "estimated" && row.Source != "unknown" {
			return
		}
		if row.Currency != "USD" && row.Currency != "RUB" && row.Currency != "" {
			return
		}
		if row.Cost != nil {
			if len(*row.Cost) > 40 {
				return
			}
			n, err := strconv.ParseFloat(*row.Cost, 64)
			if !decimalCost.MatchString(*row.Cost) || err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || n > 1e12 {
				return
			}
		}
		if (row.Source == "unknown") != (row.Cost == nil) || (row.Cost != nil && row.Currency == "") {
			return
		}
	}
	capture.mu.Lock()
	defer capture.mu.Unlock()
	capture.rows = append(capture.rows, rows...)
	capture.received = true
}
