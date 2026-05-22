package domain

import (
	"fmt"
	"time"

	"github.com/google/uuid"

	derrors "github.com/ion-core/backend/pkg/errors"
)

// EWOStatus is the lifecycle of an enterprise engineering work order.
//
//   pending     -> in_progress (StartEWO)
//   in_progress -> completed   (CompleteEWO)
//   pending | in_progress -> cancelled (CancelEWO with reason)
//
// Completion and cancellation are terminal. Reopening would require a
// new EWO row — keeps the audit trail clean.
type EWOStatus string

const (
	EWOStatusPending    EWOStatus = "pending"
	EWOStatusInProgress EWOStatus = "in_progress"
	EWOStatusCompleted  EWOStatus = "completed"
	EWOStatusCancelled  EWOStatus = "cancelled"
)

type EWO struct {
	ID            uuid.UUID
	EWONumber     string
	QuotationID   uuid.UUID
	OpportunityID uuid.UUID
	BOQVersionID  uuid.UUID
	Status        EWOStatus
	AssignedTo    *uuid.UUID
	StartedAt     *time.Time
	CompletedAt   *time.Time
	CancelledAt   *time.Time
	CancelReason  string
	Notes         string
	// Pre-launch E9 — checklist progress + soft link to field WO.
	ProgressPct       float64
	FieldWorkOrderID  *uuid.UUID
	Revision          int
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

func NewEWO(
	quotationID, opportunityID, boqVersionID uuid.UUID,
	number, notes string,
) (*EWO, error) {
	if number == "" {
		return nil, derrors.Validation(
			"ewo.number_required",
			"ewo_number is required",
		)
	}
	now := time.Now().UTC()
	return &EWO{
		ID:            uuid.New(),
		EWONumber:     number,
		QuotationID:   quotationID,
		OpportunityID: opportunityID,
		BOQVersionID:  boqVersionID,
		Status:        EWOStatusPending,
		Notes:         notes,
		Revision:      1,
		CreatedAt:     now,
		UpdatedAt:     now,
	}, nil
}

func (e *EWO) Assign(userID uuid.UUID) error {
	if e.Status == EWOStatusCompleted || e.Status == EWOStatusCancelled {
		return derrors.Conflict(
			"ewo.terminal",
			"cannot assign a completed or cancelled EWO",
		)
	}
	e.AssignedTo = &userID
	return nil
}

func (e *EWO) Start() error {
	if e.Status != EWOStatusPending {
		return derrors.Conflict(
			"ewo.invalid_transition",
			fmt.Sprintf("cannot start an EWO in status %q", e.Status),
		)
	}
	now := time.Now().UTC()
	e.Status = EWOStatusInProgress
	e.StartedAt = &now
	return nil
}

func (e *EWO) Complete() error {
	if e.Status != EWOStatusInProgress {
		return derrors.Conflict(
			"ewo.invalid_transition",
			fmt.Sprintf("cannot complete an EWO in status %q — must be in_progress", e.Status),
		)
	}
	now := time.Now().UTC()
	e.Status = EWOStatusCompleted
	e.CompletedAt = &now
	return nil
}

func (e *EWO) Cancel(reason string) error {
	if e.Status == EWOStatusCompleted {
		return derrors.Conflict(
			"ewo.already_completed",
			"completed EWOs cannot be cancelled",
		)
	}
	if e.Status == EWOStatusCancelled {
		return derrors.Conflict(
			"ewo.already_cancelled",
			"this EWO is already cancelled",
		)
	}
	if reason == "" {
		return derrors.Validation(
			"ewo.cancel_reason_required",
			"cancel reason is required",
		)
	}
	now := time.Now().UTC()
	e.Status = EWOStatusCancelled
	e.CancelledAt = &now
	e.CancelReason = reason
	return nil
}

// GenerateEWONumber yields EWO-YYYYMMDD-<short> — same pattern as
// invoice / opportunity / quotation so the FE can render uniform IDs.
func GenerateEWONumber(now time.Time) string {
	if now.IsZero() {
		now = time.Now()
	}
	return fmt.Sprintf("EWO-%s-%s",
		now.UTC().Format("20060102"),
		uuid.New().String()[:8],
	)
}
