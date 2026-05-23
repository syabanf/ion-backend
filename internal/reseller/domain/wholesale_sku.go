package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// WholesaleSKU is one purchasable item on the wholesale catalog. The
// supplier_subsidiary_id binds each SKU to one fulfilling entity so
// the order routing layer (Wave 95+) knows which warehouse / NOC
// receives the order. Catalog reads from the reseller-platform are
// filtered by `is_active = true` so deactivating a SKU stops new
// orders without breaking already-issued ones.
type WholesaleSKU struct {
	ID                   uuid.UUID
	SupplierSubsidiaryID uuid.UUID
	Name                 string
	SKUCode              string
	UnitPrice            float64
	Unit                 string
	IsActive             bool
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// NewWholesaleSKU constructs an active SKU. SKU codes are
// case-sensitive and globally unique (enforced by the DB) — we keep
// them as opaque strings so partners can use their own conventions.
func NewWholesaleSKU(supplierID uuid.UUID, name, code, unit string, unitPrice float64) (*WholesaleSKU, error) {
	name = strings.TrimSpace(name)
	code = strings.TrimSpace(code)
	unit = strings.TrimSpace(unit)
	if name == "" {
		return nil, errors.Validation("sku.name_required", "name is required")
	}
	if code == "" {
		return nil, errors.Validation("sku.code_required", "sku_code is required")
	}
	if unitPrice < 0 {
		return nil, errors.Validation("sku.price_negative", "unit_price must be >= 0")
	}
	if unit == "" {
		unit = "unit"
	}
	now := time.Now().UTC()
	return &WholesaleSKU{
		ID:                   uuid.New(),
		SupplierSubsidiaryID: supplierID,
		Name:                 name,
		SKUCode:              code,
		UnitPrice:            unitPrice,
		Unit:                 unit,
		IsActive:             true,
		CreatedAt:            now,
		UpdatedAt:            now,
	}, nil
}
