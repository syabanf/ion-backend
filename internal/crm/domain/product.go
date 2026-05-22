// Package domain holds the CRM context's entities and value objects.
// Same rules as identity / network / warehouse: no framework imports,
// invariants enforced by constructors, errors via pkg/errors.
package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// Product is a broadband package row from crm.products.
//
// The full PRD calls for a schema-driven catalog (versioning + draft/publish)
// — that's M1 territory and was deferred. This flat shape unblocks M4.
type Product struct {
	ID                       uuid.UUID
	Code                     string
	Name                     string
	SpeedMbps                int
	MonthlyPrice             float64
	OTCPrice                 float64
	TempActivationWindowHrs  int
	Active                   bool
	CreatedAt                time.Time
}

func NewProduct(code, name string, speedMbps int, monthly, otc float64) (*Product, error) {
	code = strings.TrimSpace(strings.ToUpper(code))
	name = strings.TrimSpace(name)
	if code == "" {
		return nil, errors.Validation("product.code_required", "code is required")
	}
	if name == "" {
		return nil, errors.Validation("product.name_required", "name is required")
	}
	if speedMbps <= 0 {
		return nil, errors.Validation("product.speed_invalid", "speed_mbps must be > 0")
	}
	if monthly < 0 || otc < 0 {
		return nil, errors.Validation("product.price_invalid", "prices cannot be negative")
	}
	return &Product{
		ID:                      uuid.New(),
		Code:                    code,
		Name:                    name,
		SpeedMbps:               speedMbps,
		MonthlyPrice:            monthly,
		OTCPrice:                otc,
		TempActivationWindowHrs: 72,
		Active:                  true,
		CreatedAt:               time.Now().UTC(),
	}, nil
}
