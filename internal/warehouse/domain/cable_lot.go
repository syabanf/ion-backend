// Wave 117 — Type 2 (cable) length-tracked drums.
package domain

import (
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// CableLotStatus mirrors the CHECK on warehouse.cable_lots.status.
//
//	in_stock  → allocated → consumed
//	any       → disposed   (terminal)
type CableLotStatus string

const (
	CableLotStatusInStock   CableLotStatus = "in_stock"
	CableLotStatusAllocated CableLotStatus = "allocated"
	CableLotStatusConsumed  CableLotStatus = "consumed"
	CableLotStatusDisposed  CableLotStatus = "disposed"
)

func (s CableLotStatus) Valid() bool {
	switch s {
	case CableLotStatusInStock, CableLotStatusAllocated,
		CableLotStatusConsumed, CableLotStatusDisposed:
		return true
	}
	return false
}

// CableLot is one physical drum/spool of cable.
type CableLot struct {
	ID                     uuid.UUID
	ItemID                 uuid.UUID
	LotNumber              string
	TotalLengthMeters      float64
	RemainingLengthMeters  float64
	DrumSerial             string
	SupplierID             *uuid.UUID
	ReceivedAt             time.Time
	Status                 CableLotStatus
	CurrentWarehouseID     *uuid.UUID
	UnitCostPerMeter       *float64
	Notes                  string
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

// NewCableLot constructs a fresh lot at intake. The whole drum starts
// in_stock with remaining = total.
func NewCableLot(itemID uuid.UUID, totalLengthMeters float64) (*CableLot, error) {
	if itemID == uuid.Nil {
		return nil, errors.Validation("cable_lot.item_required", "item_id is required")
	}
	if totalLengthMeters <= 0 {
		return nil, errors.Validation("cable_lot.length_invalid", "total_length_meters must be positive")
	}
	now := time.Now().UTC()
	return &CableLot{
		ID:                    uuid.New(),
		ItemID:                itemID,
		TotalLengthMeters:     totalLengthMeters,
		RemainingLengthMeters: totalLengthMeters,
		ReceivedAt:            now,
		Status:                CableLotStatusInStock,
		CreatedAt:             now,
		UpdatedAt:             now,
	}, nil
}

// CutSegment is the atomic operation: reduce remaining by `length`,
// refuse if remaining < length. Returns the audit row to persist
// alongside the lot update. Caller wraps both writes in a tx.
//
// State transitions encoded:
//   in_stock or allocated → consumed when remaining hits 0
//   disposed → always refused (terminal)
func (l *CableLot) CutSegment(length float64, woID *uuid.UUID, byUserID *uuid.UUID) (*CableCut, error) {
	if l.Status == CableLotStatusDisposed {
		return nil, errors.Conflict("cable_lot.disposed",
			"cannot cut from a disposed lot")
	}
	if l.Status == CableLotStatusConsumed {
		return nil, errors.Conflict("cable_lot.consumed",
			"cable lot is fully consumed")
	}
	if length <= 0 {
		return nil, errors.Validation("cable_lot.length_invalid",
			"cut length must be positive")
	}
	if length > l.RemainingLengthMeters {
		return nil, errors.Conflict("cable_lot.insufficient_remaining",
			"insufficient remaining length on lot")
	}
	l.RemainingLengthMeters -= length
	if l.RemainingLengthMeters == 0 {
		l.Status = CableLotStatusConsumed
	} else if l.Status == CableLotStatusInStock {
		// First cut from a previously-untouched drum moves it to
		// allocated. Subsequent cuts keep it allocated until consumed.
		l.Status = CableLotStatusAllocated
	}
	l.UpdatedAt = time.Now().UTC()
	cut := &CableCut{
		ID:              uuid.New(),
		CableLotID:      l.ID,
		CutLengthMeters: length,
		UsedForWOID:     woID,
		CutBy:           byUserID,
		CutAt:           time.Now().UTC(),
	}
	return cut, nil
}

// Dispose terminates a lot. Used for damaged drums.
func (l *CableLot) Dispose(reason string) error {
	if l.Status == CableLotStatusConsumed {
		return errors.Conflict("cable_lot.already_consumed",
			"lot already consumed — no remainder to dispose")
	}
	l.Status = CableLotStatusDisposed
	l.Notes = reason
	l.UpdatedAt = time.Now().UTC()
	return nil
}
