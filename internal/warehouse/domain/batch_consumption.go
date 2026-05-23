// Wave 117 — immutable per-WO audit row for consumable batch consumption.
package domain

import (
	"time"

	"github.com/google/uuid"
)

// BatchConsumptionLog records one decrement against a ConsumableBatch.
// Append-only: there's no update path. The batch's RemainingQty is the
// live view; these rows are the audit trail.
type BatchConsumptionLog struct {
	ID                uuid.UUID
	ConsumableBatchID uuid.UUID
	WOID              *uuid.UUID
	QtyConsumed       int
	ConsumedBy        *uuid.UUID
	ConsumedAt        time.Time
	Notes             string
}
