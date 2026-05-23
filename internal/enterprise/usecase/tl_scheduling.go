package usecase

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/enterprise/domain"
	"github.com/ion-core/backend/internal/enterprise/port"
	"github.com/ion-core/backend/pkg/audit"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// Wave 96 — TL Scheduling use cases
//
// Surface:
//   - ScheduleEWO        — first-time assignment of start/end + team lead
//   - RescheduleEWO      — mutate an existing schedule + persist history
//   - ListMyAssignedEWOs — TL dashboard query
//   - MarkEWOInProgress  — flips status + locks schedule (post this,
//                          scheduling fields are read-only via the
//                          domain ScheduleLocked guard)
// =====================================================================

func errEWOSchedulingNotConfigured() error {
	return derrors.Wrap(derrors.KindInternal, "tl_scheduling.not_configured",
		"TL scheduling surface is not configured for this service", nil)
}

// ScheduleEWO is the first-time scheduling call. It runs the conflict
// check before persisting so a TL with an overlapping booking gets a
// clear 409 with the conflicting EWO id.
func (s *Service) ScheduleEWO(
	ctx context.Context,
	ewoID uuid.UUID,
	start, end time.Time,
	teamLeadID uuid.UUID,
	technicianID *uuid.UUID,
) (*domain.EWO, error) {
	if s.ewos == nil {
		return nil, errFinanceNotConfigured()
	}
	e, err := s.ewos.FindByID(ctx, ewoID)
	if err != nil {
		return nil, err
	}
	// Run the domain validation first — it'll reject locked / wrong-status
	// rows before we burn a DB round-trip on the overlap check.
	if err := e.Schedule(start, end, teamLeadID, technicianID); err != nil {
		return nil, err
	}
	// Concurrency check — another EWO under the same team lead must not
	// overlap. Wave 96 keeps the check at the team-lead grain; technician
	// overlap is treated as a soft warning (a TL can pair the same
	// technician across short windows). Excluding the current EWO id
	// keeps a re-schedule from colliding with its own stale row when the
	// repo hasn't yet written the new values (it hasn't at this point).
	if err := s.checkOverlap(ctx, teamLeadID, start, end, &ewoID); err != nil {
		return nil, err
	}
	if err := s.ewos.UpdateSchedule(ctx, e.ID, port.ScheduleUpdate{
		ScheduledStart: *e.ScheduledStartDate,
		ScheduledEnd:   *e.ScheduledEndDate,
		DurationDays:   derefInt(e.DurationDays),
		TeamLead:       teamLeadID,
		Technician:     e.AssignedTechnicianUserID,
	}); err != nil {
		return nil, err
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		Module:     "enterprise",
		RecordType: "enterprise.ewo",
		RecordID:   e.ID.String(),
		After:      "scheduled",
		Reason:     "ewo_scheduled",
	})
	return e, nil
}

// RescheduleEWO mutates an existing schedule. The pre-change snapshot
// is written to ewo_schedule_history as an audit trail; if the history
// repo is nil we fail closed because rescheduling without an audit
// trail violates the wave's acceptance criteria.
func (s *Service) RescheduleEWO(
	ctx context.Context,
	ewoID uuid.UUID,
	newStart, newEnd time.Time,
	byUserID uuid.UUID,
	reason string,
) (*domain.EWO, error) {
	if s.ewos == nil {
		return nil, errFinanceNotConfigured()
	}
	if s.ewoScheduleHistory == nil {
		return nil, errEWOSchedulingNotConfigured()
	}
	e, err := s.ewos.FindByID(ctx, ewoID)
	if err != nil {
		return nil, err
	}
	// Reschedule returns the previous snapshot. The domain method
	// rejects locked / wrong-status rows.
	prev, err := e.Reschedule(newStart, newEnd, byUserID, reason)
	if err != nil {
		return nil, err
	}
	teamLead := derefUUIDLocal(e.AssignedTeamLeadUserID)
	if teamLead != uuid.Nil {
		if err := s.checkOverlap(ctx, teamLead, newStart, newEnd, &ewoID); err != nil {
			return nil, err
		}
	}
	// Two writes — history row first (so a failure between the two
	// doesn't leave a schedule mutation without its audit row). If the
	// schedule update then fails the history row is harmless: it
	// captures the prior state of the EWO row which has not changed.
	if err := s.ewoScheduleHistory.Create(ctx, &prev); err != nil {
		return nil, err
	}
	if err := s.ewos.UpdateSchedule(ctx, e.ID, port.ScheduleUpdate{
		ScheduledStart: *e.ScheduledStartDate,
		ScheduledEnd:   *e.ScheduledEndDate,
		DurationDays:   derefInt(e.DurationDays),
		TeamLead:       teamLead,
		Technician:     e.AssignedTechnicianUserID,
	}); err != nil {
		return nil, err
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		UserID:     byUserID,
		Module:     "enterprise",
		RecordType: "enterprise.ewo",
		RecordID:   e.ID.String(),
		After:      "rescheduled",
		Reason:     "ewo_rescheduled",
	})
	return e, nil
}

// ListMyAssignedEWOs is the TL dashboard query — scopes by the supplied
// team-lead user id. Side filter is optional (defaults to "any side").
func (s *Service) ListMyAssignedEWOs(
	ctx context.Context,
	teamLeadUserID uuid.UUID,
	sideFilter string,
) ([]domain.EWO, error) {
	if s.ewos == nil {
		return nil, errFinanceNotConfigured()
	}
	f := port.EWOListFilter{
		AssignedTeamLeadUserID: &teamLeadUserID,
		Side:                   sideFilter,
		Limit:                  200,
	}
	// Reuse the side-aware list so the index on
	// (assigned_team_lead_user_id, scheduled_start_date) is honored.
	items, _, err := s.ewos.List(ctx, f)
	if err != nil {
		return nil, err
	}
	return items, nil
}

// MarkEWOInProgress flips an EWO into in_progress (via Start) AND
// stamps schedule_locked=true. Once this returns, RescheduleEWO will
// fail closed with ewo.schedule_locked.
//
// We accept a status-flip on a never-scheduled EWO — the legacy
// pre-Wave-96 path created EWOs without a schedule and called Start
// directly, and we preserve that flexibility.
func (s *Service) MarkEWOInProgress(ctx context.Context, ewoID uuid.UUID) (*domain.EWO, error) {
	if s.ewos == nil {
		return nil, errFinanceNotConfigured()
	}
	e, err := s.ewos.FindByID(ctx, ewoID)
	if err != nil {
		return nil, err
	}
	before := string(e.Status)
	if err := e.Start(); err != nil {
		return nil, err
	}
	if err := s.ewos.Update(ctx, e); err != nil {
		return nil, err
	}
	// Belt-and-suspenders — Start already flipped ScheduleLocked in
	// memory, but Update only writes status/assignment columns. Use
	// the dedicated LockSchedule path so the schedule_locked column
	// is independently audited.
	if err := s.ewos.LockSchedule(ctx, e.ID); err != nil {
		return nil, err
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		Module:       "enterprise",
		RecordType:   "enterprise.ewo",
		RecordID:     e.ID.String(),
		FieldChanged: "status",
		Before:       before,
		After:        string(e.Status),
		Reason:       "ewo_in_progress",
	})
	return e, nil
}

// ListEWOScheduleHistory exposes the audit trail to the HTTP layer.
func (s *Service) ListEWOScheduleHistory(
	ctx context.Context,
	ewoID uuid.UUID,
) ([]domain.ScheduleHistoryEntry, error) {
	if s.ewoScheduleHistory == nil {
		return nil, errEWOSchedulingNotConfigured()
	}
	return s.ewoScheduleHistory.ListByEWO(ctx, ewoID)
}

// ListScheduledEWOs is the broader-than-team-lead query used by the
// /ewos/scheduled endpoint. Accepts the full EWOListFilter so an
// operator can filter by team lead, technician, or date range.
func (s *Service) ListScheduledEWOs(
	ctx context.Context,
	f port.EWOListFilter,
) ([]domain.EWO, int, error) {
	if s.ewos == nil {
		return nil, 0, errFinanceNotConfigured()
	}
	return s.ewos.List(ctx, f)
}

// =====================================================================
// Helpers
// =====================================================================

func (s *Service) checkOverlap(
	ctx context.Context,
	teamLeadID uuid.UUID,
	start, end time.Time,
	excludeEWOID *uuid.UUID,
) error {
	conflicts, err := s.ewos.FindOverlappingForTeamLead(ctx, teamLeadID, start, end, excludeEWOID)
	if err != nil {
		return err
	}
	if len(conflicts) == 0 {
		return nil
	}
	first := conflicts[0]
	return derrors.Conflict(
		"ewo.schedule_conflict",
		"team lead "+teamLeadID.String()+" has overlapping assignment "+first.ID.String(),
	)
}

func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func derefUUIDLocal(p *uuid.UUID) uuid.UUID {
	if p == nil {
		return uuid.Nil
	}
	return *p
}
