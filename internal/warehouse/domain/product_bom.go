package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// ProductBOMTemplate is the per-product default bill-of-materials.
// One active template per product (enforced by the unique partial
// index in migration 0059); the dispatch flow pre-fills from this
// template when creating a WO dispatch.
type ProductBOMTemplate struct {
	ID          uuid.UUID
	ProductID   uuid.UUID
	Name        string
	Description string
	Active      bool
	CreatedBy   *uuid.UUID
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// ProductBOMTemplateItem is one line in a template.
type ProductBOMTemplateItem struct {
	ID              uuid.UUID
	TemplateID      uuid.UUID
	StockItemID     uuid.UUID
	DefaultQuantity float64
	Required        bool
	SortOrder       int
	Notes           string
}

// NewProductBOMTemplate constructs a template + lines. A template
// with zero lines is rejected — that's a recipe with no ingredients.
func NewProductBOMTemplate(
	productID uuid.UUID, name, description string,
	items []ProductBOMTemplateItemInput,
	createdBy *uuid.UUID,
	now time.Time,
) (*ProductBOMTemplate, []ProductBOMTemplateItem, error) {
	if productID == uuid.Nil {
		return nil, nil, errors.Validation("bom_tpl.product_required",
			"product_id is required")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, nil, errors.Validation("bom_tpl.name_required",
			"name is required")
	}
	if len(items) == 0 {
		return nil, nil, errors.Validation("bom_tpl.items_required",
			"at least one item is required")
	}
	id := uuid.New()
	tpl := &ProductBOMTemplate{
		ID:          id,
		ProductID:   productID,
		Name:        name,
		Description: strings.TrimSpace(description),
		Active:      true,
		CreatedBy:   createdBy,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	out := make([]ProductBOMTemplateItem, 0, len(items))
	seen := map[uuid.UUID]bool{}
	for i, in := range items {
		if in.StockItemID == uuid.Nil {
			return nil, nil, errors.Validation("bom_tpl.item_required",
				"stock_item_id is required on every line")
		}
		if seen[in.StockItemID] {
			return nil, nil, errors.Validation("bom_tpl.item_duplicate",
				"each stock_item_id can appear only once per template")
		}
		seen[in.StockItemID] = true
		if in.DefaultQuantity <= 0 {
			return nil, nil, errors.Validation("bom_tpl.quantity_invalid",
				"default_quantity must be positive")
		}
		sortOrder := in.SortOrder
		if sortOrder == 0 {
			sortOrder = i + 1
		}
		out = append(out, ProductBOMTemplateItem{
			ID:              uuid.New(),
			TemplateID:      id,
			StockItemID:     in.StockItemID,
			DefaultQuantity: in.DefaultQuantity,
			Required:        in.Required,
			SortOrder:       sortOrder,
			Notes:           strings.TrimSpace(in.Notes),
		})
	}
	return tpl, out, nil
}

type ProductBOMTemplateItemInput struct {
	StockItemID     uuid.UUID
	DefaultQuantity float64
	Required        bool
	SortOrder       int
	Notes           string
}
