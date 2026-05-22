package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// BOQ status — lifecycle per CPQ TC-SM-BOQ-*
// =====================================================================
//
//	draft           — operator is editing; lines mutable
//	in_approval     — submitted; immutable (BR-3 no withdraw at MVP)
//	boq_approved    — chain completed; downstream artifacts (Quotation
//	                  in Phase 4) can be generated
//	rejected        — any chain step said no; revision path enabled
//	revision_draft  — editable copy spawned from a rejection; on
//	                  resubmit becomes v(N+1) and the old version is
//	                  retained immutable
//	superseded      — a newer approved version exists; this row is
//	                  read-only historical record
type BOQStatus string

const (
	BOQStatusDraft          BOQStatus = "draft"
	BOQStatusInApproval     BOQStatus = "in_approval"
	BOQStatusApproved       BOQStatus = "boq_approved"
	BOQStatusRejected       BOQStatus = "rejected"
	BOQStatusRevisionDraft  BOQStatus = "revision_draft"
	BOQStatusSuperseded     BOQStatus = "superseded"
)

// BOQLineStatus tracks the per-line workflow independently of the
// header status. A vendor can fill cost on one line while the others
// are still awaiting input — TC-IV-003.
type BOQLineStatus string

const (
	BOQLineStatusAwaitingProviderInput BOQLineStatus = "awaiting_provider_input"
	BOQLineStatusHasCost               BOQLineStatus = "has_cost"
	BOQLineStatusInApproval            BOQLineStatus = "in_approval"
	BOQLineStatusApproved              BOQLineStatus = "approved"
)

// RejectionReasonCode is the closed enum for BOQ-level rejection
// reasons (set when at least one approval step is rejected and the
// usecase rolls that up to the BOQ header).
type RejectionReasonCode string

const (
	RejectionReasonNone          RejectionReasonCode = ""
	RejectionReasonPricing       RejectionReasonCode = "pricing"
	RejectionReasonScope         RejectionReasonCode = "scope"
	RejectionReasonDocumentation RejectionReasonCode = "documentation"
	RejectionReasonCompliance    RejectionReasonCode = "compliance"
	RejectionReasonOther         RejectionReasonCode = "other"
)

// =====================================================================
// BOQ — header
// =====================================================================

type BOQ struct {
	ID                    uuid.UUID
	BOQNumber             string // shared across versions of the same logical BOQ
	OpportunityID         uuid.UUID
	PricebookID           uuid.UUID
	VersionNo             int
	Status                BOQStatus
	// SellTotal is the GRAND TOTAL the customer pays (subtotal + tax).
	// Phase 5 adds SubtotalAmount + TaxPct + TaxAmount so the FE can
	// render the breakdown. We keep SellTotal as the headline number
	// for backwards-compat — older queries don't need to know about tax.
	SellTotal             float64
	SubtotalAmount        float64
	TaxPct                float64
	TaxAmount             float64
	CostTotal             float64
	MarginPct             float64 // header weighted across all lines (computed on SubtotalAmount, pre-tax)
	SnapshotHash          string  // SHA-256 hex; empty while draft
	ApprovalTemplateID    *uuid.UUID
	// Pre-launch E12 — soft backlink to the RFQ that this BOQ fulfilled
	// (if any). Set by FulfillRFQ; FE renders "← fulfilling RFQ-..."
	SourceRFQID           *uuid.UUID
	SubmittedAt           *time.Time
	ApprovedAt            *time.Time
	RejectedAt            *time.Time
	SupersededAt          *time.Time
	RejectionReasonCode   RejectionReasonCode
	RejectionComment      string
	Notes                 string
	Revision              int
	CreatedBy             *uuid.UUID
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

// DefaultTaxPct is the Indonesia PPN default (11%). Per-deal overrides
// land on boq_versions.tax_pct; this constant is the seed.
const DefaultTaxPct = 11.0

// NewBOQ constructs a draft v1 BOQ attached to an opportunity +
// pricebook version. The number is generated separately (so the
// format / branch prefix can evolve without touching domain).
func NewBOQ(opportunityID, pricebookID uuid.UUID) (*BOQ, error) {
	if opportunityID == uuid.Nil {
		return nil, errors.Validation("boq.opportunity_required", "opportunity_id is required")
	}
	if pricebookID == uuid.Nil {
		return nil, errors.Validation("boq.pricebook_required", "pricebook_id is required")
	}
	now := time.Now().UTC()
	return &BOQ{
		ID:            uuid.New(),
		OpportunityID: opportunityID,
		PricebookID:   pricebookID,
		VersionNo:     1,
		Status:        BOQStatusDraft,
		Revision:      1,
		CreatedAt:     now,
		UpdatedAt:     now,
	}, nil
}

// GenerateBOQNumber returns a sortable identifier matching the
// opportunity_number convention.
func GenerateBOQNumber(t time.Time) string {
	return "BOQ-" + t.UTC().Format("20060102") + "-" + uuid.New().String()[:8]
}

// =====================================================================
// BOQ line
// =====================================================================

type BOQLine struct {
	ID                        uuid.UUID
	BOQVersionID              uuid.UUID
	PricebookLineID           uuid.UUID
	// Snapshot fields — copied from pricebook_line at create time,
	// IMMUTABLE thereafter (TC-BQ-002).
	SKU                       string
	Name                      string
	Unit                      string
	BasePriceSnapshot         float64
	MinMarginSnapshot         float64
	MaxDiscountSnapshot       float64
	// Provider assignment (TC-BQ-003 / TC-BQ-004)
	AssignedProviderCompanyID *uuid.UUID
	ProviderUserID            *uuid.UUID
	// Pricing — vendor inputs cost, sales inputs sell + discount
	VendorUnitCost            *float64
	SellUnitPrice             float64
	Quantity                  float64
	LineDiscountPct           float64
	// SLA — FK only (TC-BQ-005)
	SLATemplateID             uuid.UUID
	Status                    BOQLineStatus
	Notes                     string
	SortOrder                 int
	// Pre-launch E4 — vendor SLA window for vendor_unit_cost fill-in.
	// Set when AssignedProviderCompanyID flips from nil; cleared when
	// VendorUnitCost gets populated.
	VendorDueAt               *time.Time
	CreatedAt                 time.Time
	UpdatedAt                 time.Time
}

// NewBOQLine constructs a line from a pricebook line's snapshot
// fields. The provider + cost + sell come later via separate
// SetProvider / SetVendorCost / SetSellPrice calls.
func NewBOQLine(
	boqVersionID, pricebookLineID, slaTemplateID uuid.UUID,
	sku, name, unit string,
	basePriceSnapshot, minMarginSnapshot, maxDiscountSnapshot float64,
	quantity float64,
) (*BOQLine, error) {
	if boqVersionID == uuid.Nil {
		return nil, errors.Validation("boq_line.boq_required", "boq_version_id is required")
	}
	if pricebookLineID == uuid.Nil {
		return nil, errors.Validation("boq_line.pricebook_line_required", "pricebook_line_id is required")
	}
	if slaTemplateID == uuid.Nil {
		return nil, errors.Validation("boq_line.sla_template_required", "sla_template_id is required (free-text SLA not allowed)")
	}
	if quantity <= 0 {
		return nil, errors.Validation("boq_line.quantity_invalid", "quantity must be > 0")
	}
	now := time.Now().UTC()
	return &BOQLine{
		ID:                  uuid.New(),
		BOQVersionID:        boqVersionID,
		PricebookLineID:     pricebookLineID,
		SKU:                 sku,
		Name:                name,
		Unit:                unit,
		BasePriceSnapshot:   basePriceSnapshot,
		MinMarginSnapshot:   minMarginSnapshot,
		MaxDiscountSnapshot: maxDiscountSnapshot,
		SLATemplateID:       slaTemplateID,
		Quantity:            quantity,
		Status:              BOQLineStatusAwaitingProviderInput,
		CreatedAt:           now,
		UpdatedAt:           now,
	}, nil
}

// SetProvider assigns the internal vendor company + user that will
// supply this line. Both are required at BOQ submit time
// (TC-BQ-003 / TC-BQ-004) but can be set incrementally during draft.
func (l *BOQLine) SetProvider(companyID, userID uuid.UUID) {
	l.AssignedProviderCompanyID = &companyID
	l.ProviderUserID = &userID
	l.UpdatedAt = time.Now().UTC()
}

// SetVendorCost is called by the assigned vendor user via the
// vendor-cost endpoint. Flips the line to has_cost if cost is set
// and provider was previously assigned.
func (l *BOQLine) SetVendorCost(cost float64) error {
	if cost < 0 {
		return errors.Validation("boq_line.cost_negative", "vendor_unit_cost must be >= 0")
	}
	l.VendorUnitCost = &cost
	if l.Status == BOQLineStatusAwaitingProviderInput {
		l.Status = BOQLineStatusHasCost
	}
	l.UpdatedAt = time.Now().UTC()
	return nil
}

// SetSellPrice + discount are set by Sales Support during BOQ build.
//
// Boundary checks (BR-1 + TC-BQ-009 + TC-BQ-011):
//   - discount must be <= max_discount_snapshot
//   - if vendor_unit_cost is known, the resulting line margin must
//     clear min_margin_snapshot
//
// The margin check fires at SAVE time (per BR-1: "Sales cannot save a
// line below the min margin floor"). It's skipped when vendor cost is
// still pending — that's a vendor-side gap, not a sales violation, and
// we already gate at SUBMIT via SubmitBOQ → ValidateMarginFloor.
func (l *BOQLine) SetSellPriceAndDiscount(sell, discountPct float64) error {
	if sell < 0 {
		return errors.Validation("boq_line.sell_negative", "sell_unit_price must be >= 0")
	}
	if discountPct < 0 || discountPct > 100 {
		return errors.Validation("boq_line.discount_range", "line_discount_pct must be in [0, 100]")
	}
	const eps = 1e-9
	if discountPct-eps > l.MaxDiscountSnapshot {
		return errors.Validation(
			"boq_line.discount_exceeded",
			"line_discount_pct exceeds the max_discount_snapshot ceiling",
		)
	}
	// Try the new values on a copy first, then validate margin floor.
	// We only enforce when vendor cost is known — otherwise margin is
	// undefined (cost = 0) and would falsely pass.
	if l.VendorUnitCost != nil {
		trial := *l
		trial.SellUnitPrice = sell
		trial.LineDiscountPct = discountPct
		if err := trial.ValidateMarginFloor(); err != nil {
			// Re-wrap with a save-time error code so the FE can
			// distinguish "blocked on save" from "blocked on submit".
			return errors.Validation(
				"boq_line.min_margin_violation_on_save",
				"line margin would fall below the min_margin_snapshot floor — adjust sell price or discount",
			)
		}
	}
	l.SellUnitPrice = sell
	l.LineDiscountPct = discountPct
	l.UpdatedAt = time.Now().UTC()
	return nil
}

// LineSellTotal applies the discount to (sell × quantity).
// CPQ TC-BQ-007 math: sell_unit_price=5M × qty=10 = sell_total 50M
// (with discount 0 in that test). When discount > 0:
//   sell_total = sell × qty × (1 - discount/100)
func (l *BOQLine) LineSellTotal() float64 {
	gross := l.SellUnitPrice * l.Quantity
	if l.LineDiscountPct == 0 {
		return gross
	}
	return gross * (1 - l.LineDiscountPct/100.0)
}

// LineCostTotal is cost × qty (no discount on cost — that's a
// vendor-supplied number we accept as-is).
func (l *BOQLine) LineCostTotal() float64 {
	if l.VendorUnitCost == nil {
		return 0
	}
	return *l.VendorUnitCost * l.Quantity
}

// LineMarginPct computes margin as % of sell. Returns 0 when sell is
// 0 (avoid div-by-zero). Used by ValidateMarginFloor and header roll-up.
func (l *BOQLine) LineMarginPct() float64 {
	sell := l.LineSellTotal()
	if sell <= 0 {
		return 0
	}
	cost := l.LineCostTotal()
	return (sell - cost) / sell * 100.0
}

// ValidateMarginFloor reports whether the line's projected margin
// clears the min_margin_snapshot floor. Used both during draft (so
// the UI can pre-block) and at BOQ submit (per TC-BQ-009 / BR-1).
// Boundary: margin = floor exactly → PASS (TC-BQ-010).
func (l *BOQLine) ValidateMarginFloor() error {
	margin := l.LineMarginPct()
	const eps = 1e-9
	if margin+eps < l.MinMarginSnapshot {
		return errors.Validation(
			"boq_line.min_margin_violation",
			"projected line margin is below the min_margin_snapshot floor",
		)
	}
	return nil
}

// =====================================================================
// Header math — weighted across lines (TC-BQ-008)
// =====================================================================

// RecomputeHeaderTotals re-runs the sums + weighted margin given the
// current line set. Mutates the BOQ in place. Caller is responsible
// for persisting after.
//
// Weighted margin: header_margin = (sum_sell - sum_cost) / sum_sell.
// Example from TC-BQ-008:
//
//	Line A: sell=50M cost=35M
//	Line B: sell=20M cost=12M
//	header sell = 70M, header cost = 47M
//	header margin = 23/70 = 32.857%
func (b *BOQ) RecomputeHeaderTotals(lines []BOQLine) {
	sumSell, sumCost := 0.0, 0.0
	for i := range lines {
		sumSell += lines[i].LineSellTotal()
		sumCost += lines[i].LineCostTotal()
	}
	// Phase 5 tax breakdown (E6 / FN-1). The line sum is the SUBTOTAL
	// (pre-tax); the grand total layered on top is what the customer
	// pays. Margin is computed on the subtotal — tax is a pass-through
	// to the government, not part of our gross profit.
	if b.TaxPct == 0 {
		b.TaxPct = DefaultTaxPct
	}
	b.SubtotalAmount = sumSell
	b.TaxAmount = sumSell * (b.TaxPct / 100.0)
	b.SellTotal = b.SubtotalAmount + b.TaxAmount
	b.CostTotal = sumCost
	if sumSell <= 0 {
		b.MarginPct = 0
	} else {
		b.MarginPct = (sumSell - sumCost) / sumSell * 100.0
	}
}

// =====================================================================
// Lifecycle transitions
// =====================================================================

// Submit transitions draft / revision_draft → in_approval. Validates
// every line meets the margin floor (BR-1 / TC-BQ-009) AND has a
// provider assigned (TC-BQ-003 / TC-BQ-004). Once submitted the BOQ
// is immutable until approve/reject; no withdraw at MVP (BR-3).
//
// `snapshotHash` is the canonical-JSON SHA-256 the usecase computed
// from this BOQ + its lines. Stamping it on submit makes the version
// integrity-checkable from that point forward.
func (b *BOQ) Submit(lines []BOQLine, templateID uuid.UUID, snapshotHash string) error {
	if b.Status != BOQStatusDraft && b.Status != BOQStatusRevisionDraft {
		return errors.Conflict(
			"boq.invalid_state_transition",
			"can only submit from draft or revision_draft (current: "+string(b.Status)+")",
		)
	}
	if len(lines) == 0 {
		return errors.Validation("boq.no_lines", "cannot submit a BOQ with no lines")
	}
	for i := range lines {
		l := &lines[i]
		// Provider must be assigned (TC-BQ-003 / TC-BQ-004)
		if l.AssignedProviderCompanyID == nil {
			return errors.Validation(
				"boq.provider_company_required",
				"every line must have an assigned provider company before submit",
			)
		}
		if l.ProviderUserID == nil {
			return errors.Validation(
				"boq.provider_user_required",
				"every line must have an assigned provider user before submit",
			)
		}
		if l.VendorUnitCost == nil {
			return errors.Validation(
				"boq.vendor_cost_required",
				"every line must have vendor_unit_cost recorded before submit",
			)
		}
		// Margin floor (BR-1 / TC-BQ-009)
		if err := l.ValidateMarginFloor(); err != nil {
			return err
		}
	}
	if templateID == uuid.Nil {
		return errors.Validation("boq.approval_template_required", "approval_template_id is required to submit")
	}
	if snapshotHash == "" {
		return errors.Validation("boq.snapshot_hash_required", "snapshot_hash must be computed before submit")
	}
	now := time.Now().UTC()
	b.Status = BOQStatusInApproval
	b.ApprovalTemplateID = &templateID
	b.SnapshotHash = snapshotHash
	b.SubmittedAt = &now
	b.UpdatedAt = now
	b.Revision++
	return nil
}

// MarkApproved transitions in_approval → boq_approved. Called by the
// usecase after the last approval step completes successfully.
func (b *BOQ) MarkApproved() error {
	if b.Status != BOQStatusInApproval {
		return errors.Conflict(
			"boq.invalid_state_transition",
			"can only approve from in_approval (current: "+string(b.Status)+")",
		)
	}
	now := time.Now().UTC()
	b.Status = BOQStatusApproved
	b.ApprovedAt = &now
	b.UpdatedAt = now
	return nil
}

// MarkRejected transitions in_approval → rejected. Captures the
// rejection reason code + comment rolled up from the rejecting step.
func (b *BOQ) MarkRejected(code RejectionReasonCode, comment string) error {
	if b.Status != BOQStatusInApproval {
		return errors.Conflict(
			"boq.invalid_state_transition",
			"can only reject from in_approval (current: "+string(b.Status)+")",
		)
	}
	comment = strings.TrimSpace(comment)
	if comment == "" {
		return errors.Validation(
			"boq.rejection_comment_required",
			"a rejection comment is required",
		)
	}
	now := time.Now().UTC()
	b.Status = BOQStatusRejected
	b.RejectionReasonCode = code
	b.RejectionComment = comment
	b.RejectedAt = &now
	b.UpdatedAt = now
	return nil
}

// StartRevision moves rejected → revision_draft. The revision_draft
// is editable; on resubmit it becomes v(N+1) (handled by usecase).
func (b *BOQ) StartRevision() error {
	if b.Status != BOQStatusRejected {
		return errors.Conflict(
			"boq.invalid_state_transition",
			"can only start revision from rejected",
		)
	}
	b.Status = BOQStatusRevisionDraft
	b.UpdatedAt = time.Now().UTC()
	return nil
}

// Supersede flips approved → superseded. Called by the usecase when
// a newer version of the same BOQ number gets approved.
func (b *BOQ) Supersede() error {
	if b.Status != BOQStatusApproved {
		return errors.Conflict(
			"boq.invalid_state_transition",
			"can only supersede approved versions",
		)
	}
	now := time.Now().UTC()
	b.Status = BOQStatusSuperseded
	b.SupersededAt = &now
	b.UpdatedAt = now
	return nil
}

// =====================================================================
// Snapshot hash — canonical JSON SHA-256 (NFR-007 deterministic)
// =====================================================================

// ComputeSnapshotHash returns the SHA-256 hex of the canonical JSON
// representation of the BOQ + its lines. The canonical form sorts map
// keys alphabetically (Go's json.Marshal already does this) AND
// sorts lines by ID so insertion order doesn't affect the hash.
//
// Per CPQ TC-BQ-015 + NFR-007: compute the hash 100× against the same
// input → all identical. The hash also pins commercial terms so the
// audit trail can later prove "this exact BOQ was approved at time T."
func ComputeSnapshotHash(b *BOQ, lines []BOQLine) (string, error) {
	// Sort a copy of the lines by ID for deterministic ordering.
	sorted := make([]BOQLine, len(lines))
	copy(sorted, lines)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].ID.String() < sorted[j].ID.String()
	})
	// We marshal a stripped-down representation — timestamps and
	// concurrency/status fields are excluded so a no-op timestamp
	// bump doesn't change the hash. Only the COMMERCIAL state goes
	// into the snapshot.
	snap := struct {
		BOQNumber    string       `json:"boq_number"`
		VersionNo    int          `json:"version_no"`
		Opportunity  string       `json:"opportunity_id"`
		Pricebook    string       `json:"pricebook_id"`
		SellTotal    float64      `json:"sell_total"`
		CostTotal    float64      `json:"cost_total"`
		MarginPct    float64      `json:"margin_pct"`
		Lines        []lineSnap   `json:"lines"`
	}{
		BOQNumber:   b.BOQNumber,
		VersionNo:   b.VersionNo,
		Opportunity: b.OpportunityID.String(),
		Pricebook:   b.PricebookID.String(),
		SellTotal:   b.SellTotal,
		CostTotal:   b.CostTotal,
		MarginPct:   b.MarginPct,
		Lines:       make([]lineSnap, 0, len(sorted)),
	}
	for _, l := range sorted {
		ls := lineSnap{
			ID:                  l.ID.String(),
			PricebookLineID:     l.PricebookLineID.String(),
			SKU:                 l.SKU,
			Name:                l.Name,
			Unit:                l.Unit,
			BasePriceSnapshot:   l.BasePriceSnapshot,
			MinMarginSnapshot:   l.MinMarginSnapshot,
			MaxDiscountSnapshot: l.MaxDiscountSnapshot,
			SellUnitPrice:       l.SellUnitPrice,
			Quantity:            l.Quantity,
			LineDiscountPct:     l.LineDiscountPct,
			SLATemplateID:       l.SLATemplateID.String(),
		}
		if l.VendorUnitCost != nil {
			ls.VendorUnitCost = *l.VendorUnitCost
		}
		if l.AssignedProviderCompanyID != nil {
			ls.ProviderCompanyID = l.AssignedProviderCompanyID.String()
		}
		if l.ProviderUserID != nil {
			ls.ProviderUserID = l.ProviderUserID.String()
		}
		snap.Lines = append(snap.Lines, ls)
	}
	buf, err := json.Marshal(snap)
	if err != nil {
		return "", errors.Wrap(errors.KindInternal, "boq.snapshot_marshal", "marshal snapshot", err)
	}
	sum := sha256.Sum256(buf)
	return hex.EncodeToString(sum[:]), nil
}

type lineSnap struct {
	ID                  string  `json:"id"`
	PricebookLineID     string  `json:"pricebook_line_id"`
	SKU                 string  `json:"sku"`
	Name                string  `json:"name"`
	Unit                string  `json:"unit"`
	BasePriceSnapshot   float64 `json:"base_price_snapshot"`
	MinMarginSnapshot   float64 `json:"min_margin_snapshot"`
	MaxDiscountSnapshot float64 `json:"max_discount_snapshot"`
	SellUnitPrice       float64 `json:"sell_unit_price"`
	Quantity            float64 `json:"quantity"`
	LineDiscountPct     float64 `json:"line_discount_pct"`
	VendorUnitCost      float64 `json:"vendor_unit_cost"`
	ProviderCompanyID   string  `json:"provider_company_id"`
	ProviderUserID      string  `json:"provider_user_id"`
	SLATemplateID       string  `json:"sla_template_id"`
}
