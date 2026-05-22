package domain

import (
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// RescheduleReason mirrors the PRD enum on the wo_reschedules table.
type RescheduleReason string

const (
	RescheduleCustomerNotAvail RescheduleReason = "customer_not_available"
	RescheduleSiteNotReady     RescheduleReason = "site_not_ready"
	RescheduleEquipmentIssue   RescheduleReason = "equipment_issue"
	RescheduleCustomerRequest  RescheduleReason = "customer_request"
	RescheduleOther            RescheduleReason = "other"
)

// Reschedule is the audit row that captures "we moved this WO to a new date".
// The wo_reschedules table existed in M5 r1 but had no service/UI; r2 wires it.
type Reschedule struct {
	ID             uuid.UUID
	WOID           uuid.UUID
	Reason         RescheduleReason
	Notes          string
	OriginalDate   *time.Time
	NewDate        *time.Time
	RescheduledBy  *uuid.UUID
	CreatedAt      time.Time
}

// AssertCanReschedule checks the WO is in a status that allows a date move.
// Per the state machine, only assigned / dispatched / in_progress qualify.
// We block reschedule from final states (completed/cancelled) and from the
// pre-assignment states (created/unassigned) — those should just be
// updated via the normal scheduled_date setter.
func (w *WorkOrder) AssertCanReschedule() error {
	switch w.Status {
	case WOStatusAssigned, WOStatusDispatched, WOStatusInProgress, WOStatusRescheduled:
		return nil
	}
	return errors.Conflict("wo.cannot_reschedule",
		"cannot reschedule a WO in status "+string(w.Status))
}
