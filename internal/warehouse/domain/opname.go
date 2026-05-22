package domain

import (
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// OpnameStatus — open: still collecting counts; committed: variances
// have been applied to stock_levels via opname_adjustment movements;
// cancelled: session abandoned, no live state changed.
type OpnameStatus string

const (
	OpnameStatusOpen      OpnameStatus = "open"
	OpnameStatusCommitted OpnameStatus = "committed"
	OpnameStatusCancelled OpnameStatus = "cancelled"
)

// CableRemnantDecision — what to do with the leftover bit when a cable's
// counted_qty differs from expected by a small remainder. The two PRD
// paths: keep the remnant (accept the new figure as live), or scrap it
// (write off the difference and set live qty to whatever's logical).
//
// In round-2 we model this minimally: the decision is recorded on the
// count row and we always set live qty = counted_qty on commit. The
// scrap path additionally emits a 'dispose' movement for the difference
// so the audit trail is honest about the write-off.
type CableRemnantDecision string

const (
	CableRemnantKeep  CableRemnantDecision = "keep_partial"
	CableRemnantScrap CableRemnantDecision = "scrap"
)

type OpnameSession struct {
	ID             uuid.UUID
	SessionNumber  string
	WarehouseID    uuid.UUID
	Status         OpnameStatus
	StartedBy      *uuid.UUID
	StartedAt      time.Time
	CommittedAt    *time.Time
	CancelledAt    *time.Time
	Notes          string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type OpnameCount struct {
	ID                   uuid.UUID
	SessionID            uuid.UUID
	StockItemID          uuid.UUID
	ExpectedQty          float64
	CountedQty           float64
	Variance             float64
	CableRemnantDecision *CableRemnantDecision
	Notes                string
	CountedBy            *uuid.UUID
	CountedAt            time.Time
}

func GenerateOpnameNumber(t time.Time) string {
	return "OPN-" + t.UTC().Format("20060102") + "-" + uuid.New().String()[:8]
}

// AssertCanCount — only open sessions accept new counts.
func (s *OpnameSession) AssertCanCount() error {
	if s.Status != OpnameStatusOpen {
		return errors.Conflict("opname.not_open",
			"opname session is not open (status="+string(s.Status)+")")
	}
	return nil
}

// AssertCanCommit — open sessions can commit, anything else can't.
func (s *OpnameSession) AssertCanCommit() error {
	if s.Status != OpnameStatusOpen {
		return errors.Conflict("opname.not_open",
			"only open opname sessions can be committed")
	}
	return nil
}

// AssertCanCancel — open sessions only.
func (s *OpnameSession) AssertCanCancel() error {
	if s.Status != OpnameStatusOpen {
		return errors.Conflict("opname.not_open",
			"only open opname sessions can be cancelled")
	}
	return nil
}
