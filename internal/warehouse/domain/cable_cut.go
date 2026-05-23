// Wave 117 — immutable audit row per cut from a cable lot.
package domain

import (
	"time"

	"github.com/google/uuid"
)

// CableCut is one segment cut from a CableLot. Append-only: there's no
// update path. The lot's RemainingLength is the live "what's left" view;
// these rows are the audit trail of where each meter went.
type CableCut struct {
	ID              uuid.UUID
	CableLotID      uuid.UUID
	CutLengthMeters float64
	UsedForWOID     *uuid.UUID
	CutBy           *uuid.UUID
	CutAt           time.Time
	Notes           string
}
