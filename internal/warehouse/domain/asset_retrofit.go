package domain

import (
	"time"

	"github.com/google/uuid"
)

// AssetRetrofit is the audit row paired with the
// retrofit_consume + retrofit_produce movement pair. Read-only after
// creation — the workflow is atomic so amending a retrofit isn't a
// valid operation; corrections happen by creating a counter-retrofit.
type AssetRetrofit struct {
	ID                uuid.UUID
	SourceAssetID     uuid.UUID
	ProducedAssetID   uuid.UUID
	Reason            string
	PerformedBy       *uuid.UUID
	PerformedAt       time.Time
	ConsumeMovementID *uuid.UUID
	ProduceMovementID *uuid.UUID
}
