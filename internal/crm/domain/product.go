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
// Wave 77 (QA TC-PRD-014/016/018/022): products can now carry per-kind
// schema assignments. The 5 schema slots are independent — a plan can
// override its Onboarding schema while leaving Billing on the customer-
// type default. Each slot is nullable; nil falls through to the global
// default at resolve time.
type Product struct {
	ID                      uuid.UUID
	Code                    string
	Name                    string
	SpeedMbps               int
	MonthlyPrice            float64
	OTCPrice                float64
	TempActivationWindowHrs int
	Active                  bool
	CreatedAt               time.Time

	// Wave 77 — schema slot FKs (TC-PRD-014/016/018/022). Nullable;
	// resolver falls through to customer-type DEFAULT when null.
	OnboardingSchemaID  *uuid.UUID
	BillingSchemaID     *uuid.UUID
	ServiceSchemaID     *uuid.UUID
	CommissionSchemaID  *uuid.UUID
	SuspensionSchemaID  *uuid.UUID
}

// SchemaSlots lists the 5 kinds in canonical order. Useful for
// resolver loops + tests that need to assert "all 5 slots resolved".
//
// Kept in sync with platform.schema_kind enum from 0032 migration.
var SchemaSlots = []string{
	"onboarding",
	"billing",
	"service",
	"commission",
	"suspension",
}

// SchemaSlotID returns the FK column matching the given kind, or nil
// if the kind isn't one of the 5 canonical slots. Used by repos +
// resolvers to avoid 5-way switch statements.
func (p *Product) SchemaSlotID(kind string) *uuid.UUID {
	switch kind {
	case "onboarding":
		return p.OnboardingSchemaID
	case "billing":
		return p.BillingSchemaID
	case "service":
		return p.ServiceSchemaID
	case "commission":
		return p.CommissionSchemaID
	case "suspension":
		return p.SuspensionSchemaID
	}
	return nil
}

// SetSchemaSlot assigns a kind's schema_id, returning Validation if
// the kind is not one of the 5 canonical slots.
func (p *Product) SetSchemaSlot(kind string, schemaID *uuid.UUID) error {
	switch kind {
	case "onboarding":
		p.OnboardingSchemaID = schemaID
	case "billing":
		p.BillingSchemaID = schemaID
	case "service":
		p.ServiceSchemaID = schemaID
	case "commission":
		p.CommissionSchemaID = schemaID
	case "suspension":
		p.SuspensionSchemaID = schemaID
	default:
		return errors.Validation("product.schema_kind_invalid",
			"schema kind '"+kind+"' must be one of: onboarding, billing, service, commission, suspension")
	}
	return nil
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
