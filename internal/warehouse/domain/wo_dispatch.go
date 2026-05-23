// Package domain — WO dispatch entities + state machine.
//
// A WODispatch is one warehouse handing gear over to a technician for
// one work order. The header walks the lifecycle:
//
//   planned    → BOM drafted, nothing reserved yet
//   staged     → counter clerk has the gear ready
//   picked_up  → all lines scanned + technician took it
//   returned   → some/all lines came back
//   cancelled  → planned/staged only — gear never left
//
// Each line walks its own smaller lifecycle:
//
//   planned   → not yet scanned
//   picked    → scanned, handed to technician
//   returned  → fully or partially returned (returned_qty == qty)
//
// State transitions live as methods on the types so the usecase layer
// can call into validated, well-named steps instead of stringly-typed
// status flips.
package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	derrors "github.com/ion-core/backend/pkg/errors"
)

// WODispatchStatus mirrors warehouse.wo_dispatch_records.status.
type WODispatchStatus string

const (
	WODispatchStatusPlanned   WODispatchStatus = "planned"
	WODispatchStatusStaged    WODispatchStatus = "staged"
	WODispatchStatusPickedUp  WODispatchStatus = "picked_up"
	WODispatchStatusReturned  WODispatchStatus = "returned"
	WODispatchStatusCancelled WODispatchStatus = "cancelled"
)

func (s WODispatchStatus) Valid() bool {
	switch s {
	case WODispatchStatusPlanned, WODispatchStatusStaged, WODispatchStatusPickedUp,
		WODispatchStatusReturned, WODispatchStatusCancelled:
		return true
	}
	return false
}

// WODispatchItemStatus mirrors warehouse.wo_dispatch_items.status.
type WODispatchItemStatus string

const (
	WODispatchItemStatusPlanned  WODispatchItemStatus = "planned"
	WODispatchItemStatusPicked   WODispatchItemStatus = "picked"
	WODispatchItemStatusReturned WODispatchItemStatus = "returned"
)

func (s WODispatchItemStatus) Valid() bool {
	switch s {
	case WODispatchItemStatusPlanned, WODispatchItemStatusPicked, WODispatchItemStatusReturned:
		return true
	}
	return false
}

// WODispatch is the dispatch header. Items are eagerly loaded by the
// repo; usecase methods read/mutate them as a unit.
type WODispatch struct {
	ID            uuid.UUID
	WOID          uuid.UUID
	WarehouseID   uuid.UUID
	DispatchedBy  *uuid.UUID
	Status        WODispatchStatus
	PlannedAt     time.Time
	StagedAt      *time.Time
	PickedUpAt    *time.Time
	ReturnedAt    *time.Time
	CancelledAt   *time.Time
	CancelReason  string
	Notes         string
	Revision      int
	CreatedAt     time.Time
	UpdatedAt     time.Time
	Items         []WODispatchItem

	// Wave 89b — when the BOM was materialized from a product BOM
	// template, the template id is stamped here so an auditor can
	// trace "which template seeded this dispatch". Nil for ad-hoc
	// dispatches built by hand.
	SourceBOMTemplateID *uuid.UUID
}

// WODispatchItem is one BOM line.
type WODispatchItem struct {
	ID          uuid.UUID
	DispatchID  uuid.UUID
	ItemID      uuid.UUID
	Qty         float64
	ReturnedQty float64
	SerialOrQR  *string
	Status      WODispatchItemStatus
	PickedAt    *time.Time
	PickedBy    *uuid.UUID
	Notes       string
}

// NewWODispatch constructs a dispatch in `planned` state. The items
// slice is the BOM; each line is created in `planned` status with a
// fresh ID. We validate here so the usecase doesn't have to repeat
// itself for every entry point (HTTP, future RPC, future bulk import).
func NewWODispatch(woID, warehouseID uuid.UUID, dispatchedBy uuid.UUID, items []WODispatchItem, notes string) (*WODispatch, error) {
	if woID == uuid.Nil {
		return nil, derrors.Validation("wo_dispatch.wo_required", "wo_id is required")
	}
	if warehouseID == uuid.Nil {
		return nil, derrors.Validation("wo_dispatch.warehouse_required", "warehouse_id is required")
	}
	if len(items) == 0 {
		return nil, derrors.Validation("wo_dispatch.items_required", "at least one BOM item is required")
	}
	now := time.Now().UTC()
	d := &WODispatch{
		ID:           uuid.New(),
		WOID:         woID,
		WarehouseID:  warehouseID,
		DispatchedBy: &dispatchedBy,
		Status:       WODispatchStatusPlanned,
		PlannedAt:    now,
		Notes:        strings.TrimSpace(notes),
		Revision:     1,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	for _, it := range items {
		if it.ItemID == uuid.Nil {
			return nil, derrors.Validation("wo_dispatch.item_id_required", "item_id is required on every BOM line")
		}
		if it.Qty <= 0 {
			return nil, derrors.Validation("wo_dispatch.qty_positive", "qty must be > 0")
		}
		d.Items = append(d.Items, WODispatchItem{
			ID:         uuid.New(),
			DispatchID: d.ID,
			ItemID:     it.ItemID,
			Qty:        it.Qty,
			Status:     WODispatchItemStatusPlanned,
			Notes:      strings.TrimSpace(it.Notes),
		})
	}
	return d, nil
}

// Stage moves planned → staged. The counter clerk has the gear pulled
// from the shelves and ready for the technician.
func (d *WODispatch) Stage() error {
	if d.Status != WODispatchStatusPlanned {
		return derrors.Conflict("wo_dispatch.not_planned",
			"only planned dispatches can be staged")
	}
	now := time.Now().UTC()
	d.Status = WODispatchStatusStaged
	d.StagedAt = &now
	return nil
}

// MarkPickedUp moves staged → picked_up. Caller must verify every line
// is in 'picked' state before invoking this — that's the technician
// having scanned each unit.
func (d *WODispatch) MarkPickedUp() error {
	if d.Status != WODispatchStatusStaged {
		return derrors.Conflict("wo_dispatch.not_staged",
			"only staged dispatches can be marked picked up")
	}
	for _, it := range d.Items {
		if it.Status != WODispatchItemStatusPicked {
			return derrors.Conflict("wo_dispatch.items_not_all_picked",
				"every BOM line must be scanned (status='picked') before the dispatch can be marked picked up")
		}
	}
	now := time.Now().UTC()
	d.Status = WODispatchStatusPickedUp
	d.PickedUpAt = &now
	return nil
}

// Cancel — only planned or staged. After pickup the gear's already in
// the field, so the right exit is a return (or installation +
// commissioning), never a cancel.
func (d *WODispatch) Cancel(reason string) error {
	switch d.Status {
	case WODispatchStatusPlanned, WODispatchStatusStaged:
	default:
		return derrors.Conflict("wo_dispatch.cannot_cancel",
			"only planned or staged dispatches can be cancelled")
	}
	now := time.Now().UTC()
	d.Status = WODispatchStatusCancelled
	d.CancelledAt = &now
	d.CancelReason = strings.TrimSpace(reason)
	return nil
}

// PickByScan marks one BOM line as picked. The serial is optional —
// non-serialized lines (cable meters, consumables) leave it empty and
// the line's planned qty is treated as the full amount being handed
// over. The caller is responsible for the idempotency guard via the
// (dispatch, item, serial) unique index.
func (it *WODispatchItem) PickByScan(serialOrQR string, by uuid.UUID) error {
	if it.Status == WODispatchItemStatusPicked {
		// Idempotency: re-scanning the same serial is a no-op.
		if it.SerialOrQR != nil && *it.SerialOrQR == strings.TrimSpace(serialOrQR) {
			return nil
		}
		return derrors.Conflict("wo_dispatch_item.already_picked",
			"this BOM line has already been picked with a different serial")
	}
	if it.Status != WODispatchItemStatusPlanned {
		return derrors.Conflict("wo_dispatch_item.not_planned",
			"only planned items can be picked")
	}
	now := time.Now().UTC()
	if v := strings.TrimSpace(serialOrQR); v != "" {
		it.SerialOrQR = &v
	}
	it.Status = WODispatchItemStatusPicked
	it.PickedAt = &now
	it.PickedBy = &by
	return nil
}

// Return registers qty units coming back. Returning more than was
// picked is rejected; returning the full picked qty flips the line
// status to 'returned'.
func (it *WODispatchItem) Return(qty float64) error {
	if it.Status != WODispatchItemStatusPicked && it.Status != WODispatchItemStatusReturned {
		return derrors.Conflict("wo_dispatch_item.cannot_return",
			"only picked or partially-returned items can be returned")
	}
	if qty <= 0 {
		return derrors.Validation("wo_dispatch_item.qty_positive", "return qty must be > 0")
	}
	if it.ReturnedQty+qty > it.Qty {
		return derrors.Validation("wo_dispatch_item.qty_over",
			"return qty exceeds the picked quantity for this line")
	}
	it.ReturnedQty += qty
	if it.ReturnedQty >= it.Qty {
		it.Status = WODispatchItemStatusReturned
	}
	return nil
}
