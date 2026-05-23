package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// PricebookLine is one item in a pricebook — the smallest billable unit
// the enterprise CPQ flow can quote.
//
// Per CPQ TC-PB-003 / TC-PB-004 the line carries three numeric
// guardrails enforced both here and at the database CHECK constraint:
//   - default_margin_pct ≥ min_margin_pct        (paired constraint)
//   - 0 ≤ default_margin_pct, min_margin_pct ≤ 100
//   - 0 ≤ max_discount_pct ≤ 100
//   - base_price ≥ 0
//
// `AllowedProviderCompanyIDs` is the whitelist of internal vendors that
// may supply this item (later: `warehouse.suppliers` with
// `is_internal_vendor=true`, Phase 3). An empty slice means
// "any vendor".
type PricebookLine struct {
	ID                        uuid.UUID
	PricebookID               uuid.UUID
	SKU                       string
	Name                      string
	Category                  string
	Description               string
	Unit                      string
	BasePrice                 float64 // in pricebook.currency units
	DefaultMarginPct          float64
	MinMarginPct              float64
	MaxDiscountPct            float64
	AllowedProviderCompanyIDs []uuid.UUID
	OwnerRole                 string
	SortOrder                 int
	Active                    bool
	// Wave 106 — provider-priority badge per pricebook line (TC-PB-010).
	// Higher values render before lower; ties broken by SKU asc. Default
	// 0 means "unranked" — the pricebook line list endpoint respects this
	// when ?sort=priority is set on the query string. Persisted via
	// migration 0071 ALTER TABLE enterprise.pricebook_lines.
	PriorityScore int
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// NewPricebookLine constructs a line with all invariants checked.
// Use this constructor (never struct-literal init) so the guardrails
// can't be bypassed by a future caller adding a new field.
func NewPricebookLine(
	pricebookID uuid.UUID,
	sku, name string,
	basePrice float64,
	defaultMarginPct, minMarginPct, maxDiscountPct float64,
) (*PricebookLine, error) {
	sku = strings.TrimSpace(sku)
	name = strings.TrimSpace(name)
	if sku == "" {
		return nil, errors.Validation("pricebook_line.sku_required", "sku is required")
	}
	if name == "" {
		return nil, errors.Validation("pricebook_line.name_required", "name is required")
	}
	if pricebookID == uuid.Nil {
		return nil, errors.Validation("pricebook_line.pricebook_id_required", "pricebook_id is required")
	}
	if basePrice < 0 {
		return nil, errors.Validation("pricebook_line.base_price_negative", "base_price must be >= 0")
	}
	if defaultMarginPct < 0 || defaultMarginPct > 100 {
		return nil, errors.Validation(
			"pricebook_line.default_margin_out_of_range",
			"default_margin_pct must be in [0, 100]",
		)
	}
	if minMarginPct < 0 || minMarginPct > 100 {
		return nil, errors.Validation(
			"pricebook_line.min_margin_out_of_range",
			"min_margin_pct must be in [0, 100]",
		)
	}
	if maxDiscountPct < 0 || maxDiscountPct > 100 {
		return nil, errors.Validation(
			"pricebook_line.max_discount_out_of_range",
			"max_discount_pct must be in [0, 100]",
		)
	}
	if minMarginPct > defaultMarginPct {
		// Per CPQ TC-PB-004 — auto-calc could never satisfy the floor
		// otherwise. The DB has the same CHECK so a manual INSERT
		// can't sneak past either.
		return nil, errors.Validation(
			"pricebook_line.min_margin_exceeds_default",
			"min_margin_pct must not exceed default_margin_pct",
		)
	}
	now := time.Now().UTC()
	return &PricebookLine{
		ID:                        uuid.New(),
		PricebookID:               pricebookID,
		SKU:                       sku,
		Name:                      name,
		Unit:                      "unit",
		BasePrice:                 basePrice,
		DefaultMarginPct:          defaultMarginPct,
		MinMarginPct:              minMarginPct,
		MaxDiscountPct:            maxDiscountPct,
		AllowedProviderCompanyIDs: []uuid.UUID{},
		Active:                    true,
		CreatedAt:                 now,
		UpdatedAt:                 now,
	}, nil
}

// AutoCalcSellPrice applies the default margin to a vendor cost and
// returns the implied sell price. Used by CPQ TC-PB-005:
//
//	cost=3.5M, default_margin=30% → sell=5M, margin check 30% ≥ 18% PASS
//
// Formula: sell = cost / (1 - margin_pct/100). Margin is expressed as
// a percentage of sell price (standard CPQ convention), not of cost.
//
// Returns the suggested sell price and the implied margin. The caller
// (usecase) is responsible for asserting margin ≥ MinMarginPct before
// committing.
func (l *PricebookLine) AutoCalcSellPrice(vendorCost float64) (sellPrice, marginPct float64, err error) {
	if vendorCost < 0 {
		return 0, 0, errors.Validation(
			"pricebook_line.vendor_cost_negative",
			"vendor_cost must be >= 0",
		)
	}
	if l.DefaultMarginPct >= 100 {
		return 0, 0, errors.Validation(
			"pricebook_line.default_margin_too_high",
			"default_margin_pct must be strictly less than 100 to auto-calc sell price",
		)
	}
	// Margin expressed as % of sell: sell = cost / (1 - m).
	denominator := 1 - (l.DefaultMarginPct / 100.0)
	sellPrice = vendorCost / denominator
	marginPct = l.DefaultMarginPct
	return sellPrice, marginPct, nil
}

// ValidateMarginFloor reports whether a proposed (sell, cost) pair
// clears the line's min margin. Used by CPQ TC-PB-006 and TC-BQ-009
// (BOQ submit margin floor enforcement) — same rule, used in both
// the pricebook auto-calc path and the BOQ-line submit path.
//
// Returns nil when the margin clears the floor, a typed validation
// error otherwise.
func (l *PricebookLine) ValidateMarginFloor(sellPrice, vendorCost float64) error {
	if sellPrice <= 0 {
		return errors.Validation(
			"pricebook_line.sell_price_invalid",
			"sell_price must be > 0 to compute margin",
		)
	}
	actual := (sellPrice - vendorCost) / sellPrice * 100.0
	// Tolerate floating-point dust at the boundary — TC-BQ-010 expects
	// margin=18.000% exactly to PASS when min=18%.
	const eps = 1e-9
	if actual+eps < l.MinMarginPct {
		return errors.Validation(
			"pricebook_line.min_margin_violation",
			"projected margin is below the min_margin_pct floor",
		)
	}
	return nil
}

// MarginFloorViolation describes a margin-floor breach with both the
// computed margin AND the configured floor so the FE can render a
// helpful "Cost+Margin would be Rp X; floor is Rp Y" toast (TC-PB-006
// auto-calc-below-floor surface, Wave 106).
type MarginFloorViolation struct {
	ComputedMarginPct float64 `json:"computed_margin_pct"`
	MinMarginPct      float64 `json:"min_margin_pct"`
	SellPrice         float64 `json:"sell_price"`
	VendorCost        float64 `json:"vendor_cost"`
}

// AutoCalcSellPriceWithFloor is the Wave 106 surface for the pricebook
// auto-calc endpoint. Returns the implied sell price + margin (same as
// AutoCalcSellPrice) but ALSO surfaces a MarginFloorViolation when the
// default-margin-derived margin would fall below MinMarginPct. The
// usecase wraps this into derrors.Validation("pricebook_line.margin_below_floor", ...)
// with the violation marshalled into the error details field.
//
// Math:
//   sell  = cost / (1 - default_margin/100)
//   actual_margin = default_margin (by construction of the formula)
//   floor_violated = actual_margin < min_margin_pct
//
// Because actual_margin == default_margin by construction, the only
// way this returns a violation is if default_margin < min_margin_pct —
// which the NewPricebookLine constructor + DB CHECK forbid. We still
// run the check so callers passing a custom margin (different from
// DefaultMarginPct) get the right signal.
func (l *PricebookLine) AutoCalcSellPriceWithFloor(vendorCost float64) (sellPrice, marginPct float64, violation *MarginFloorViolation, err error) {
	sellPrice, marginPct, err = l.AutoCalcSellPrice(vendorCost)
	if err != nil {
		return 0, 0, nil, err
	}
	const eps = 1e-9
	if marginPct+eps < l.MinMarginPct {
		v := &MarginFloorViolation{
			ComputedMarginPct: marginPct,
			MinMarginPct:      l.MinMarginPct,
			SellPrice:         sellPrice,
			VendorCost:        vendorCost,
		}
		return sellPrice, marginPct, v, nil
	}
	return sellPrice, marginPct, nil, nil
}

// ValidateDiscountCeiling reports whether a proposed discount % is
// within the configured ceiling. Per CPQ TC-BQ-011:
//
//	max_discount=20%, discount=20.00 → PASS
//	max_discount=20%, discount=20.01 → HTTP 422 discount_exceeded
func (l *PricebookLine) ValidateDiscountCeiling(discountPct float64) error {
	if discountPct < 0 {
		return errors.Validation(
			"pricebook_line.discount_negative",
			"discount_pct must be >= 0",
		)
	}
	const eps = 1e-9
	if discountPct-eps > l.MaxDiscountPct {
		return errors.Validation(
			"pricebook_line.discount_exceeded",
			"discount_pct exceeds the max_discount_pct ceiling",
		)
	}
	return nil
}
