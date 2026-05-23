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
//
// Wave 96 — scheduling is allowed while `pending` (legacy "created/ready"
// in spec parlance). Once `Start` flips to `in_progress`, `ScheduleLocked`
// flips true and Reschedule is rejected.
type EWOStatus string

const (
	EWOStatusPending    EWOStatus = "pending"
	EWOStatusInProgress EWOStatus = "in_progress"
	EWOStatusCompleted  EWOStatus = "completed"
	EWOStatusCancelled  EWOStatus = "cancelled"
)

// EWOSide discriminates the dual-EWO model introduced in Wave 96.
//
//   - EWOSideX = commercial-owner side; the subsidiary holding the
//     customer relationship. Created from the customer PO acceptance
//     path (or the legacy single-EWO path; backfilled to 'x').
//   - EWOSideY = executing-sister side; auto-spawned when its parent
//     IntercompanyPO is accepted.
//
// A pair of EWO-X + EWO-Y share work via `PairedEWOID` (symmetric on
// both rows). Legacy rows have side='x' and no pair.
type EWOSide string

const (
	EWOSideX EWOSide = "x"
	EWOSideY EWOSide = "y"
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
	ProgressPct      float64
	FieldWorkOrderID *uuid.UUID
	Revision         int
	CreatedAt        time.Time
	UpdatedAt        time.Time

	// Wave 96 — dual EWO + scheduling.
	Side                     EWOSide
	ExecutingSubsidiaryID    *uuid.UUID // populated for side='y'
	IntercompanyPOID         *uuid.UUID // populated for side='y'
	PairedEWOID              *uuid.UUID // symmetric pair link
	ScheduledStartDate       *time.Time
	ScheduledEndDate         *time.Time
	DurationDays             *int
	AssignedTechnicianUserID *uuid.UUID
	AssignedTeamLeadUserID   *uuid.UUID
	ScheduleLocked           bool // flipped true when status → in_progress
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
		// Default side is commercial — matches legacy single-EWO semantics.
		Side: EWOSideX,
	}, nil
}

// NewEWOY constructs an EWO-Y (executing sister side). Caller supplies
// the IC-PO id + executing subsidiary id; both are required because the
// Validate() invariant rejects a side='y' row without them. Quotation
// + opportunity + BOQ-version ids are copied from the IC-PO's parent
// customer PO so downstream joins (status reporting, field deep-link)
// continue to work via the existing FK columns.
func NewEWOY(
	quotationID, opportunityID, boqVersionID uuid.UUID,
	executingSubsidiaryID, intercompanyPOID uuid.UUID,
	number, notes string,
) (*EWO, error) {
	if executingSubsidiaryID == uuid.Nil {
		return nil, derrors.Validation(
			"ewo.executing_subsidiary_required",
			"executing_subsidiary_id is required for an EWO-Y",
		)
	}
	if intercompanyPOID == uuid.Nil {
		return nil, derrors.Validation(
			"ewo.intercompany_po_required",
			"intercompany_po_id is required for an EWO-Y",
		)
	}
	e, err := NewEWO(quotationID, opportunityID, boqVersionID, number, notes)
	if err != nil {
		return nil, err
	}
	e.Side = EWOSideY
	e.ExecutingSubsidiaryID = &executingSubsidiaryID
	e.IntercompanyPOID = &intercompanyPOID
	return e, nil
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
	// Once work begins the schedule is locked — Reschedule will reject
	// further mutations. TL Scheduling TC-TL-009.
	e.ScheduleLocked = true
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

// =====================================================================
// Wave 96 — Dual EWO + scheduling
// =====================================================================

// Validate enforces the dual-side invariants:
//   - side='y' MUST carry ExecutingSubsidiaryID + IntercompanyPOID
//   - side='x' MUST NOT carry those (commercial side has no IC-PO link)
//
// Called from the repo layer + the auto-spawn path; cheap to invoke
// after every state mutation.
func (e *EWO) Validate() error {
	switch e.Side {
	case EWOSideY:
		if e.ExecutingSubsidiaryID == nil {
			return derrors.Validation(
				"ewo.y_executing_required",
				"side='y' requires executing_subsidiary_id",
			)
		}
		if e.IntercompanyPOID == nil {
			return derrors.Validation(
				"ewo.y_intercompany_po_required",
				"side='y' requires intercompany_po_id",
			)
		}
	case EWOSideX:
		if e.ExecutingSubsidiaryID != nil {
			return derrors.Validation(
				"ewo.x_executing_forbidden",
				"side='x' must not carry executing_subsidiary_id",
			)
		}
		if e.IntercompanyPOID != nil {
			return derrors.Validation(
				"ewo.x_intercompany_po_forbidden",
				"side='x' must not carry intercompany_po_id",
			)
		}
	default:
		return derrors.Validation(
			"ewo.invalid_side",
			fmt.Sprintf("side must be 'x' or 'y' (got %q)", e.Side),
		)
	}
	return nil
}

// LinkPair symmetrically chains two EWOs via PairedEWOID. Caller is
// responsible for persisting both rows after this method returns.
// The pair is rejected if both sides are the same row, or if both
// rows are the same side (an X must always pair with a Y).
func (e *EWO) LinkPair(other *EWO) error {
	if other == nil {
		return derrors.Validation("ewo.pair_other_nil", "pair partner is nil")
	}
	if e.ID == other.ID {
		return derrors.Validation(
			"ewo.pair_self",
			"cannot pair an EWO with itself",
		)
	}
	if e.Side == other.Side {
		return derrors.Validation(
			"ewo.pair_same_side",
			fmt.Sprintf("cannot pair two EWOs with the same side (%s)", e.Side),
		)
	}
	otherID := other.ID
	selfID := e.ID
	e.PairedEWOID = &otherID
	other.PairedEWOID = &selfID
	now := time.Now().UTC()
	e.UpdatedAt = now
	other.UpdatedAt = now
	return nil
}

// Schedule stamps the scheduling fields on a never-yet-scheduled EWO.
// Validation:
//   - status must be EWOStatusPending (the only schedulable state)
//   - ScheduleLocked must be false
//   - start < end
//   - team lead is required (TC-TL-001); technician optional
//
// Caller persists via repo.UpdateSchedule after this returns.
func (e *EWO) Schedule(start, end time.Time, teamLead uuid.UUID, technician *uuid.UUID) error {
	if e.Status != EWOStatusPending {
		return derrors.Conflict(
			"ewo.schedule_invalid_status",
			fmt.Sprintf("can only schedule a pending EWO (current status: %q)", e.Status),
		)
	}
	if e.ScheduleLocked {
		return derrors.Conflict(
			"ewo.schedule_locked",
			"schedule is locked — work has already begun",
		)
	}
	if teamLead == uuid.Nil {
		return derrors.Validation(
			"ewo.team_lead_required",
			"team_lead_user_id is required for scheduling",
		)
	}
	if !start.Before(end) {
		return derrors.Validation(
			"ewo.schedule_window_invalid",
			"scheduled_start_date must be strictly before scheduled_end_date",
		)
	}
	s := start.UTC()
	en := end.UTC()
	e.ScheduledStartDate = &s
	e.ScheduledEndDate = &en
	days := int(en.Sub(s).Hours()/24) + 1
	if days < 1 {
		days = 1
	}
	e.DurationDays = &days
	tl := teamLead
	e.AssignedTeamLeadUserID = &tl
	if technician != nil {
		t := *technician
		e.AssignedTechnicianUserID = &t
	}
	e.UpdatedAt = time.Now().UTC()
	return nil
}

// Reschedule mutates an already-scheduled EWO and returns the *previous*
// snapshot so the caller can append a row to ewo_schedule_history.
// Validation mirrors Schedule but the EWO must already carry a start/
// end (you can't Reschedule a never-scheduled row — call Schedule).
//
// Reschedule is rejected once ScheduleLocked=true (TC-TL-009).
func (e *EWO) Reschedule(
	newStart, newEnd time.Time,
	by uuid.UUID,
	reason string,
) (ScheduleHistoryEntry, error) {
	if e.ScheduleLocked {
		return ScheduleHistoryEntry{}, derrors.Conflict(
			"ewo.schedule_locked",
			"schedule is locked — work has already begun",
		)
	}
	if e.Status != EWOStatusPending {
		return ScheduleHistoryEntry{}, derrors.Conflict(
			"ewo.reschedule_invalid_status",
			fmt.Sprintf("can only reschedule a pending EWO (current status: %q)", e.Status),
		)
	}
	if e.ScheduledStartDate == nil || e.ScheduledEndDate == nil {
		return ScheduleHistoryEntry{}, derrors.Conflict(
			"ewo.reschedule_not_scheduled",
			"EWO is not yet scheduled — use Schedule instead of Reschedule",
		)
	}
	if !newStart.Before(newEnd) {
		return ScheduleHistoryEntry{}, derrors.Validation(
			"ewo.schedule_window_invalid",
			"scheduled_start_date must be strictly before scheduled_end_date",
		)
	}
	prev := ScheduleHistoryEntry{
		ID:             uuid.New(),
		EWOID:          e.ID,
		PrevStart:      *e.ScheduledStartDate,
		PrevEnd:        *e.ScheduledEndDate,
		PrevTeamLead:   derefUUID(e.AssignedTeamLeadUserID),
		PrevTechnician: derefUUID(e.AssignedTechnicianUserID),
		ChangedBy:      by,
		ChangedAt:      time.Now().UTC(),
		Reason:         reason,
	}
	s := newStart.UTC()
	en := newEnd.UTC()
	e.ScheduledStartDate = &s
	e.ScheduledEndDate = &en
	days := int(en.Sub(s).Hours()/24) + 1
	if days < 1 {
		days = 1
	}
	e.DurationDays = &days
	e.UpdatedAt = time.Now().UTC()
	return prev, nil
}

// LockSchedule flips the schedule-locked flag manually. Normally Start()
// does this implicitly; this method is exposed so an external caller
// (e.g. the in-progress endpoint that doesn't go through StartEWO) can
// achieve the same effect.
func (e *EWO) LockSchedule() {
	e.ScheduleLocked = true
	e.UpdatedAt = time.Now().UTC()
}

func derefUUID(p *uuid.UUID) uuid.UUID {
	if p == nil {
		return uuid.Nil
	}
	return *p
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
