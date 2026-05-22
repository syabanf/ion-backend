// Package config adapts the shared platformconfig.Reader to the
// warehouse port.ValuationReader. The platformconfig package handles
// the 60s TTL cache + the eventual swap to HTTP; this file is the
// narrowing.
package config

import (
	"context"
	"strings"

	"github.com/ion-core/backend/internal/warehouse/port"
	"github.com/ion-core/backend/pkg/platformconfig"
)

const inventoryValuationKey = "inventory_valuation_method"

type ValuationReader struct {
	reader *platformconfig.Reader
}

func NewValuationReader(r *platformconfig.Reader) *ValuationReader {
	return &ValuationReader{reader: r}
}

var _ port.ValuationReader = (*ValuationReader)(nil)

// InventoryValuationMethod returns "fifo" or "lifo". Any unexpected
// value (or a missing key) maps to "" so the usecase falls back to
// repo defaults instead of failing the request.
func (v *ValuationReader) InventoryValuationMethod(ctx context.Context) string {
	got := strings.ToLower(strings.TrimSpace(
		v.reader.String(ctx, inventoryValuationKey, ""),
	))
	switch got {
	case "fifo", "lifo":
		return got
	default:
		return ""
	}
}
