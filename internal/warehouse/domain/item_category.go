// Wave 117 — item categories (configurable taxonomy replacing the hardcoded
// stock_items.category enum). The four "ItemType" buckets drive the
// downstream typed dispatch / consumption flows.
package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// ItemType is the four-bucket taxonomy used by every typed flow.
//
//	type1 — serialized devices (one DB row per unit)
//	type2 — cable (length-tracked drums)
//	type3 — consumable (bulk-qty batches)
//	type4 — network infra (serialized + location-bound, cross-context to netdev)
type ItemType string

const (
	ItemTypeSerialized  ItemType = "type1"
	ItemTypeCable       ItemType = "type2"
	ItemTypeConsumable  ItemType = "type3"
	ItemTypeNetworkInfra ItemType = "type4"
)

func (t ItemType) Valid() bool {
	switch t {
	case ItemTypeSerialized, ItemTypeCable, ItemTypeConsumable, ItemTypeNetworkInfra:
		return true
	}
	return false
}

// IsSerialized — Type 1 + Type 4 mint one asset row per physical unit.
func (t ItemType) IsSerialized() bool {
	return t == ItemTypeSerialized || t == ItemTypeNetworkInfra
}

// IsCable — Type 2 alone uses length tracking.
func (t ItemType) IsCable() bool { return t == ItemTypeCable }

// IsConsumable — Type 3 alone uses batch + qty tracking.
func (t ItemType) IsConsumable() bool { return t == ItemTypeConsumable }

// RequiresLocation — Type 4 (infra) needs a network node / POP location
// before it can leave 'in_stock' status.
func (t ItemType) RequiresLocation() bool { return t == ItemTypeNetworkInfra }

// ItemCategoryDef is the configurable taxonomy row (Wave 117). Admin-
// managed; provides the defaults a new stock_item inherits at create
// time. Distinct from the legacy `ItemCategory` enum in stock_item.go,
// which mirrors the hardcoded CHECK on warehouse.stock_items.category.
type ItemCategoryDef struct {
	ID                            uuid.UUID
	Code                          string
	Name                          string
	ParentID                      *uuid.UUID
	TypeCode                      ItemType
	Description                   string
	DefaultUnit                   string
	SubWarehouseAllowedDefault    bool
	RequiresSerialAtIntake        bool
	Active                        bool
	CreatedAt                     time.Time
	UpdatedAt                     time.Time
}

// NewItemCategoryDef constructs a category with validated invariants.
func NewItemCategoryDef(code, name string, typeCode ItemType) (*ItemCategoryDef, error) {
	code = strings.TrimSpace(code)
	name = strings.TrimSpace(name)
	if code == "" {
		return nil, errors.Validation("item_category.code_required", "code is required")
	}
	if name == "" {
		return nil, errors.Validation("item_category.name_required", "name is required")
	}
	if !typeCode.Valid() {
		return nil, errors.Validation("item_category.type_invalid", "type_code must be one of type1/type2/type3/type4")
	}
	now := time.Now().UTC()
	return &ItemCategoryDef{
		ID:                         uuid.New(),
		Code:                       code,
		Name:                       name,
		TypeCode:                   typeCode,
		SubWarehouseAllowedDefault: !typeCode.RequiresLocation(),
		RequiresSerialAtIntake:     typeCode.IsSerialized(),
		Active:                     true,
		CreatedAt:                  now,
		UpdatedAt:                  now,
	}, nil
}
