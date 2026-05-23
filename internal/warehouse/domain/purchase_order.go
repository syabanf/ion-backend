package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// POStatus is the lifecycle of a purchase order. The transitions are
// enforced by the usecase; the DB CHECK guards the column against
// out-of-band writes. PRD §WHS-PO defines the flow:
//
//	draft       — admin is composing the lines
//	submitted   — sent to procurement for approval
//	approved    — procurement signed off; supplier is engaged
//	receiving   — goods receipt in progress (Wave 86 will land this)
//	closed      — all lines received OR variance accepted
//	cancelled   — abandoned before approval OR by procurement decision
type POStatus string

const (
	POStatusDraft     POStatus = "draft"
	POStatusSubmitted POStatus = "submitted"
	POStatusApproved  POStatus = "approved"
	POStatusReceiving POStatus = "receiving"
	POStatusClosed    POStatus = "closed"
	POStatusCancelled POStatus = "cancelled"
)

// PurchaseOrder is the header row in warehouse.purchase_orders.
//
// Wave 85 ships the create/list/detail surface. The status transitions
// (submit / approve / receive / close / cancel) land in their own
// methods so the audit trail captures who-did-what; this round only
// supports `draft → submitted` and `draft → cancelled` since the GR
// machinery for receiving doesn't exist yet.
type PurchaseOrder struct {
	ID                   uuid.UUID
	PONumber             string
	SupplierID           uuid.UUID
	BranchID             uuid.UUID
	ReceivingWarehouseID uuid.UUID
	Status               POStatus
	Subtotal             float64
	PPNRate              float64
	Total                float64
	ExpectedAt           *time.Time
	Notes                string

	CreatedBy       *uuid.UUID
	SubmittedBy     *uuid.UUID
	SubmittedAt     *time.Time
	ApprovedBy      *uuid.UUID
	ApprovedAt      *time.Time
	ClosedAt        *time.Time
	CancelledAt     *time.Time
	CancelledReason string

	CreatedAt time.Time
	UpdatedAt time.Time
}

// PurchaseOrderLine is one row in warehouse.purchase_order_lines. The
// (quantity_ordered, unit_cost) pair × the parent PO's PPN rate
// produces line_subtotal — the parent's Subtotal/Total are the sum
// across lines.
type PurchaseOrderLine struct {
	ID                uuid.UUID
	PurchaseOrderID   uuid.UUID
	LineNo            int
	StockItemID       uuid.UUID
	QuantityOrdered   float64
	QuantityReceived  float64
	UnitCost          float64
	LineSubtotal      float64
	Notes             string
	CreatedAt         time.Time
}

// NewPurchaseOrder constructs a draft PO. Lines are passed alongside
// because a PO with no lines is meaningless — the usecase enforces
// the invariant at the API boundary too, but the domain refuses to
// build one without at least one line so the rule lives in one place.
//
// The PO number is generated here (PO-YYYYMMDD-XXXX) — callers don't
// pass it in; that keeps numbering centralized and easy to format.
//
// PPN defaults to 11 (Indonesia's current standard rate). Callers
// who serve enterprise customers with negotiated rates can override
// before submitting.
func NewPurchaseOrder(
	supplierID, branchID, receivingWarehouseID uuid.UUID,
	lines []PurchaseOrderLineInput,
	ppnRate float64,
	notes string,
	createdBy *uuid.UUID,
	now time.Time,
) (*PurchaseOrder, []PurchaseOrderLine, error) {
	if supplierID == uuid.Nil {
		return nil, nil, errors.Validation("po.supplier_required", "supplier_id is required")
	}
	if branchID == uuid.Nil {
		return nil, nil, errors.Validation("po.branch_required", "branch_id is required")
	}
	if receivingWarehouseID == uuid.Nil {
		return nil, nil, errors.Validation("po.warehouse_required",
			"receiving_warehouse_id is required")
	}
	if len(lines) == 0 {
		return nil, nil, errors.Validation("po.lines_required",
			"at least one line item is required")
	}
	if ppnRate < 0 {
		return nil, nil, errors.Validation("po.ppn_invalid",
			"ppn_rate cannot be negative")
	}
	if ppnRate == 0 {
		ppnRate = 11.0
	}

	id := uuid.New()
	po := &PurchaseOrder{
		ID:                   id,
		PONumber:             generatePONumber(now),
		SupplierID:           supplierID,
		BranchID:             branchID,
		ReceivingWarehouseID: receivingWarehouseID,
		Status:               POStatusDraft,
		PPNRate:              ppnRate,
		Notes:                strings.TrimSpace(notes),
		CreatedBy:            createdBy,
		CreatedAt:            now,
		UpdatedAt:            now,
	}

	var (
		subtotal float64
		out      = make([]PurchaseOrderLine, 0, len(lines))
	)
	for i, l := range lines {
		if l.StockItemID == uuid.Nil {
			return nil, nil, errors.Validation("po.line_item_required",
				"stock_item_id is required on every line")
		}
		if l.QuantityOrdered <= 0 {
			return nil, nil, errors.Validation("po.line_quantity_invalid",
				"quantity_ordered must be greater than zero")
		}
		if l.UnitCost < 0 {
			return nil, nil, errors.Validation("po.line_cost_invalid",
				"unit_cost cannot be negative")
		}
		lineSubtotal := l.QuantityOrdered * l.UnitCost
		subtotal += lineSubtotal
		out = append(out, PurchaseOrderLine{
			ID:              uuid.New(),
			PurchaseOrderID: id,
			LineNo:          i + 1,
			StockItemID:     l.StockItemID,
			QuantityOrdered: l.QuantityOrdered,
			UnitCost:        l.UnitCost,
			LineSubtotal:    lineSubtotal,
			Notes:           strings.TrimSpace(l.Notes),
			CreatedAt:       now,
		})
	}
	po.Subtotal = subtotal
	po.Total = subtotal + subtotal*ppnRate/100
	return po, out, nil
}

// PurchaseOrderLineInput is the unsealed constructor input — same
// shape as the line domain row but without the parent FK / line_no /
// computed subtotal, all of which are assigned at construction.
type PurchaseOrderLineInput struct {
	StockItemID     uuid.UUID
	QuantityOrdered float64
	UnitCost        float64
	Notes           string
}

// generatePONumber renders PO-YYYYMMDD-XXXX with an 8-char uuid suffix
// so two POs created the same second don't collide.
func generatePONumber(t time.Time) string {
	return "PO-" + t.UTC().Format("20060102") + "-" + uuid.New().String()[:8]
}

// Submit advances a draft PO to submitted. Idempotent on already-
// submitted POs (no-op return) so dashboard double-clicks don't
// produce spurious errors. Any other source state is a Conflict.
func (p *PurchaseOrder) Submit(by uuid.UUID, now time.Time) error {
	if p.Status == POStatusSubmitted {
		return nil
	}
	if p.Status != POStatusDraft {
		return errors.Conflict("po.not_submittable",
			"only draft POs can be submitted; current status: "+string(p.Status))
	}
	p.Status = POStatusSubmitted
	p.SubmittedBy = &by
	t := now.UTC()
	p.SubmittedAt = &t
	p.UpdatedAt = t
	return nil
}

// Approve advances submitted → approved. Procurement decision. The
// usecase carries the actor for the audit trail. Wave 86 adds this so
// the goods-receipt workflow has a valid precondition state.
func (p *PurchaseOrder) Approve(by uuid.UUID, now time.Time) error {
	if p.Status == POStatusApproved {
		return nil
	}
	if p.Status != POStatusSubmitted {
		return errors.Conflict("po.not_approvable",
			"only submitted POs can be approved; current status: "+string(p.Status))
	}
	p.Status = POStatusApproved
	p.ApprovedBy = &by
	t := now.UTC()
	p.ApprovedAt = &t
	p.UpdatedAt = t
	return nil
}

// MarkReceiving flips approved → receiving on the first goods-receipt
// event. Idempotent on already-receiving so a multi-batch shipment
// doesn't error on the second receipt. The usecase calls this from
// inside the GR creation tx.
func (p *PurchaseOrder) MarkReceiving(now time.Time) error {
	if p.Status == POStatusReceiving {
		return nil
	}
	if p.Status != POStatusApproved {
		return errors.Conflict("po.not_receiving_eligible",
			"only approved POs can begin receiving; current status: "+string(p.Status))
	}
	p.Status = POStatusReceiving
	p.UpdatedAt = now.UTC()
	return nil
}

// Close advances receiving → closed when all lines are fully received.
// The usecase decides "all received" by summing quantity_received
// against quantity_ordered; this method only enforces the state
// transition.
func (p *PurchaseOrder) Close(now time.Time) error {
	if p.Status == POStatusClosed {
		return nil
	}
	if p.Status != POStatusReceiving {
		return errors.Conflict("po.not_closeable",
			"only receiving POs can be closed; current status: "+string(p.Status))
	}
	p.Status = POStatusClosed
	t := now.UTC()
	p.ClosedAt = &t
	p.UpdatedAt = t
	return nil
}

// Cancel ends the PO without receiving. Allowed from any non-terminal
// state; closing and re-opening isn't supported (callers create a new
// PO instead). Reason is required for the audit trail.
func (p *PurchaseOrder) Cancel(by uuid.UUID, reason string, now time.Time) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return errors.Validation("po.cancel_reason_required",
			"reason is required when cancelling a PO")
	}
	if p.Status == POStatusClosed || p.Status == POStatusCancelled {
		return errors.Conflict("po.terminal",
			"cannot cancel a "+string(p.Status)+" PO")
	}
	p.Status = POStatusCancelled
	t := now.UTC()
	p.CancelledAt = &t
	p.CancelledReason = reason
	p.UpdatedAt = t
	_ = by // captured at audit-log level
	return nil
}
