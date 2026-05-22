package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// ItemCategory mirrors the CHECK constraint on warehouse.stock_items.
// PRD §Warehouse §4: four distinct tracking models.
type ItemCategory string

const (
	CategorySerializedDevice ItemCategory = "serialized_device"
	CategoryCable            ItemCategory = "cable"
	CategoryConsumable       ItemCategory = "consumable"
	CategoryInfrastructure   ItemCategory = "infrastructure"
)

func (c ItemCategory) Valid() bool {
	switch c {
	case CategorySerializedDevice, CategoryCable, CategoryConsumable, CategoryInfrastructure:
		return true
	}
	return false
}

// IsSerialized — whether this category requires per-unit asset rows.
func (c ItemCategory) IsSerialized() bool {
	return c == CategorySerializedDevice || c == CategoryInfrastructure
}

// Unit is the dispatch unit. Cable is in meters; everything else is in
// pieces or packs.
type Unit string

const (
	UnitPieces Unit = "pcs"
	UnitMeters Unit = "meters"
	UnitPack   Unit = "pack"
)

func (u Unit) Valid() bool {
	switch u {
	case UnitPieces, UnitMeters, UnitPack:
		return true
	}
	return false
}

// StockItem is the catalog row — one per distinct item type.
type StockItem struct {
	ID              uuid.UUID
	SKU             string
	Name            string
	Category        ItemCategory
	Brand           string
	Model           string
	Spec            string
	Unit            Unit
	Serialized      bool
	DefaultUnitCost *float64
	Active          bool
	Metadata        map[string]any
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// NewStockItem applies category-aware defaults and the consistency rule:
// `serialized` is fully determined by `category`, so callers can omit it.
//
// Cable category implies Unit=meters; consumable defaults to pcs; others
// default to pcs. Callers can override Unit explicitly via the returned
// struct.
func NewStockItem(sku, name string, category ItemCategory) (*StockItem, error) {
	sku = strings.TrimSpace(sku)
	name = strings.TrimSpace(name)
	if sku == "" {
		return nil, errors.Validation("stock_item.sku_required", "sku is required")
	}
	if name == "" {
		return nil, errors.Validation("stock_item.name_required", "name is required")
	}
	if !category.Valid() {
		return nil, errors.Validation("stock_item.category_invalid", "invalid category")
	}

	unit := UnitPieces
	if category == CategoryCable {
		unit = UnitMeters
	}

	now := time.Now().UTC()
	return &StockItem{
		ID:         uuid.New(),
		SKU:        sku,
		Name:       name,
		Category:   category,
		Unit:       unit,
		Serialized: category.IsSerialized(),
		Active:     true,
		Metadata:   map[string]any{},
		CreatedAt:  now,
		UpdatedAt:  now,
	}, nil
}
