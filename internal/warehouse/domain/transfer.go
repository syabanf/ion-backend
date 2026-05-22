package domain

import (
	"time"

	"github.com/google/uuid"
)

// TransferStatus mirrors warehouse.transfers.status.
type TransferStatus string

const (
	TransferStatusDraft      TransferStatus = "draft"
	TransferStatusDispatched TransferStatus = "dispatched"
	TransferStatusReceived   TransferStatus = "received"
	TransferStatusCancelled  TransferStatus = "cancelled"
)

func (s TransferStatus) Valid() bool {
	switch s {
	case TransferStatusDraft, TransferStatusDispatched,
		TransferStatusReceived, TransferStatusCancelled:
		return true
	}
	return false
}

// Transfer header — items live in TransferItem.
type Transfer struct {
	ID                     uuid.UUID
	TransferNumber         string
	SourceWarehouseID      uuid.UUID
	DestinationWarehouseID uuid.UUID
	Status                 TransferStatus
	Notes                  string
	CreatedBy              *uuid.UUID
	DispatchedAt           *time.Time
	ReceivedAt             *time.Time
	CreatedAt              time.Time
	UpdatedAt              time.Time
	Items                  []TransferItem
}

// TransferItem is one line in a transfer.
// For serialized assets we identify by AssetID + Quantity=1.
// For non-serialized, AssetID is nil and Quantity is meters/count.
type TransferItem struct {
	ID          uuid.UUID
	TransferID  uuid.UUID
	StockItemID uuid.UUID
	AssetID     *uuid.UUID
	Quantity    float64
}
