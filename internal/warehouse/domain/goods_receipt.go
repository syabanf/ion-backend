package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// GoodsReceipt is the header row in warehouse.goods_receipts — one
// per physical arrival event against an approved/receiving PO. A
// single PO can have multiple receipts (back-orders, partial
// shipments); the GR lines bump the parent PO line's
// quantity_received in lock-step with the asset/stock_level writes.
type GoodsReceipt struct {
	ID                uuid.UUID
	ReceiptNumber     string
	PurchaseOrderID   uuid.UUID
	WarehouseID       uuid.UUID
	ReceivedAt        time.Time
	ReceivedBy        *uuid.UUID
	CarrierRef        string
	Notes             string
	CreatedAt         time.Time
}

// GoodsReceiptLine is one row in goods_receipt_lines. For serialized
// items AssetID is set (one row per physical serial); for
// non-serialized items AssetID is nil and QuantityReceived covers the
// bulk (meters / count).
type GoodsReceiptLine struct {
	ID                    uuid.UUID
	GoodsReceiptID        uuid.UUID
	PurchaseOrderLineID   uuid.UUID
	QuantityReceived      float64
	UnitCost              float64
	AssetID               *uuid.UUID
	Notes                 string
	CreatedAt             time.Time
}

// NewGoodsReceipt constructs a draft receipt header. Lines are
// attached separately at the repo layer because they're built up by
// the usecase as it walks the PO lines + serial entries.
func NewGoodsReceipt(
	purchaseOrderID, warehouseID uuid.UUID,
	receivedBy *uuid.UUID,
	carrierRef, notes string,
	now time.Time,
) (*GoodsReceipt, error) {
	if purchaseOrderID == uuid.Nil {
		return nil, errors.Validation("gr.po_required", "purchase_order_id is required")
	}
	if warehouseID == uuid.Nil {
		return nil, errors.Validation("gr.warehouse_required", "warehouse_id is required")
	}
	return &GoodsReceipt{
		ID:              uuid.New(),
		ReceiptNumber:   generateReceiptNumber(now),
		PurchaseOrderID: purchaseOrderID,
		WarehouseID:     warehouseID,
		ReceivedAt:      now.UTC(),
		ReceivedBy:      receivedBy,
		CarrierRef:      strings.TrimSpace(carrierRef),
		Notes:           strings.TrimSpace(notes),
		CreatedAt:       now.UTC(),
	}, nil
}

// generateReceiptNumber — GR-YYYYMMDD-XXXX with an 8-char uuid suffix.
// Same pattern as PO numbers for visual consistency in audit logs.
func generateReceiptNumber(t time.Time) string {
	return "GR-" + t.UTC().Format("20060102") + "-" + uuid.New().String()[:8]
}
