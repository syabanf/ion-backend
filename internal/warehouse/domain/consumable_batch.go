// Wave 117 — Type 3 (consumable) bulk-qty batches with FIFO ordering.
package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// ConsumableBatchStatus mirrors the CHECK on warehouse.consumable_batches.status.
type ConsumableBatchStatus string

const (
	ConsumableBatchStatusInStock   ConsumableBatchStatus = "in_stock"
	ConsumableBatchStatusAllocated ConsumableBatchStatus = "allocated"
	ConsumableBatchStatusConsumed  ConsumableBatchStatus = "consumed"
	ConsumableBatchStatusExpired   ConsumableBatchStatus = "expired"
	ConsumableBatchStatusDisposed  ConsumableBatchStatus = "disposed"
)

func (s ConsumableBatchStatus) Valid() bool {
	switch s {
	case ConsumableBatchStatusInStock, ConsumableBatchStatusAllocated,
		ConsumableBatchStatusConsumed, ConsumableBatchStatusExpired,
		ConsumableBatchStatusDisposed:
		return true
	}
	return false
}

// ConsumableBatch is one received lot of a Type 3 item.
type ConsumableBatch struct {
	ID                   uuid.UUID
	ItemID               uuid.UUID
	BatchNo              string
	TotalQty             int
	RemainingQty         int
	ExpiryDate           *time.Time
	ReceivedAt           time.Time
	SupplierID           *uuid.UUID
	CurrentWarehouseID   *uuid.UUID
	UnitCost             *float64
	Status               ConsumableBatchStatus
	Notes                string
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// NewConsumableBatch constructs a batch at intake.
func NewConsumableBatch(itemID uuid.UUID, batchNo string, totalQty int) (*ConsumableBatch, error) {
	if itemID == uuid.Nil {
		return nil, errors.Validation("consumable_batch.item_required", "item_id is required")
	}
	batchNo = strings.TrimSpace(batchNo)
	if batchNo == "" {
		return nil, errors.Validation("consumable_batch.batch_no_required", "batch_no is required")
	}
	if totalQty <= 0 {
		return nil, errors.Validation("consumable_batch.qty_invalid", "total_qty must be positive")
	}
	now := time.Now().UTC()
	return &ConsumableBatch{
		ID:           uuid.New(),
		ItemID:       itemID,
		BatchNo:      batchNo,
		TotalQty:     totalQty,
		RemainingQty: totalQty,
		ReceivedAt:   now,
		Status:       ConsumableBatchStatusInStock,
		CreatedAt:    now,
		UpdatedAt:    now,
	}, nil
}

// Consume reduces remaining_qty by qty. Refuses if remaining < qty or
// the batch is in a terminal state. Returns the audit log row for the
// caller to persist alongside the batch update.
func (b *ConsumableBatch) Consume(qty int, woID *uuid.UUID, byUserID *uuid.UUID) (*BatchConsumptionLog, error) {
	if b.Status == ConsumableBatchStatusDisposed {
		return nil, errors.Conflict("consumable_batch.disposed",
			"cannot consume from a disposed batch")
	}
	if b.Status == ConsumableBatchStatusExpired {
		return nil, errors.Conflict("consumable_batch.expired",
			"cannot consume from an expired batch")
	}
	if b.Status == ConsumableBatchStatusConsumed {
		return nil, errors.Conflict("consumable_batch.exhausted",
			"batch is fully consumed")
	}
	if qty <= 0 {
		return nil, errors.Validation("consumable_batch.qty_invalid", "qty must be positive")
	}
	if qty > b.RemainingQty {
		return nil, errors.Conflict("consumable_batch.insufficient",
			"insufficient remaining quantity")
	}
	b.RemainingQty -= qty
	if b.RemainingQty == 0 {
		b.Status = ConsumableBatchStatusConsumed
	} else if b.Status == ConsumableBatchStatusInStock {
		b.Status = ConsumableBatchStatusAllocated
	}
	b.UpdatedAt = time.Now().UTC()
	return &BatchConsumptionLog{
		ID:                  uuid.New(),
		ConsumableBatchID:   b.ID,
		WOID:                woID,
		QtyConsumed:         qty,
		ConsumedBy:          byUserID,
		ConsumedAt:          time.Now().UTC(),
	}, nil
}

// IsExpired returns true when the batch has an expiry_date and `now`
// has crossed it. Stateless — caller decides whether to flip status.
func (b *ConsumableBatch) IsExpired(now time.Time) bool {
	if b.ExpiryDate == nil {
		return false
	}
	return now.After(*b.ExpiryDate)
}

// MarkExpired moves a batch into the terminal expired status. Used by
// the daily housekeeping pass on batches with past expiry_date.
func (b *ConsumableBatch) MarkExpired() error {
	if b.Status == ConsumableBatchStatusConsumed {
		return errors.Conflict("consumable_batch.already_consumed",
			"batch already fully consumed")
	}
	if b.Status == ConsumableBatchStatusDisposed {
		return errors.Conflict("consumable_batch.disposed",
			"batch already disposed")
	}
	b.Status = ConsumableBatchStatusExpired
	b.UpdatedAt = time.Now().UTC()
	return nil
}
