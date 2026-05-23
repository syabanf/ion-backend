// Wave 126 — MaintenanceService: orchestration on top of the existing
// field.maintenance_events handler (phase2.go owns CRUD).
//
// Adds:
//   - MaterializeAffectedCustomers — walk the network cascade and
//     populate operations.maintenance_affected_customers rows
//   - RequestApproval/Approve — joint-approval gate for >100 customers
//   - NotifyLeadTime — dispatch per-customer 24h / 72h notifications
//   - DetectOverrun + EscalateOverrun — past-window auto-flag + escalate
package usecase

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/operations/domain"
	"github.com/ion-core/backend/internal/operations/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// MaintenanceService is the orchestrator. Wire dependencies once;
// the service is goroutine-safe (no internal state beyond the deps).
type MaintenanceService struct {
	reader        port.MaintenanceReader
	affected      port.MaintenanceAffectedCustomerRepository
	escalations   port.MaintenanceEscalationRepository
	segmentRes    port.CustomerSegmentResolver
	dispatcher    port.MaintenanceNotificationDispatcher
	overrunTol    time.Duration
	log           *slog.Logger
}

// MaintenanceDeps groups dependencies. Optional ports may be nil; the
// service degrades to no-ops on those code paths.
type MaintenanceDeps struct {
	Reader      port.MaintenanceReader
	Affected    port.MaintenanceAffectedCustomerRepository
	Escalations port.MaintenanceEscalationRepository
	SegmentRes  port.CustomerSegmentResolver
	Dispatcher  port.MaintenanceNotificationDispatcher
	OverrunTol  time.Duration // default 30 min
	Log         *slog.Logger
}

// NewMaintenanceService builds the orchestrator.
func NewMaintenanceService(deps MaintenanceDeps) *MaintenanceService {
	tol := deps.OverrunTol
	if tol <= 0 {
		tol = 30 * time.Minute
	}
	log := deps.Log
	if log == nil {
		log = slog.Default()
	}
	return &MaintenanceService{
		reader:      deps.Reader,
		affected:    deps.Affected,
		escalations: deps.Escalations,
		segmentRes:  deps.SegmentRes,
		dispatcher:  deps.Dispatcher,
		overrunTol:  tol,
		log:         log.With("service", "operations.maintenance"),
	}
}

// MaterializeAffectedCustomers walks the event's affected-nodes
// cascade (network.ports -> crm.customers) and persists per-customer
// rows. Updates affected_customer_count + approval_required on the
// event. Idempotent — re-runs add new customers but never duplicate.
func (s *MaintenanceService) MaterializeAffectedCustomers(ctx context.Context, eventID uuid.UUID) (int, error) {
	if s == nil || s.affected == nil || s.segmentRes == nil || s.reader == nil {
		return 0, derrors.Internal("operations.maintenance.no_deps", "maintenance dependencies not wired")
	}
	event, err := s.reader.FindEvent(ctx, eventID)
	if err != nil {
		return 0, err
	}
	if event == nil {
		return 0, derrors.NotFound("operations.maintenance.event_not_found", "maintenance event not found")
	}
	customers, err := s.segmentRes.ResolveByMaintenanceEvent(ctx, eventID)
	if err != nil {
		return 0, err
	}
	if len(customers) == 0 {
		// Update count anyway in case re-run after schema drift.
		_ = s.reader.UpdateAffectedCount(ctx, eventID, 0)
		return 0, nil
	}
	rows := make([]domain.MaintenanceAffectedCustomer, 0, len(customers))
	for _, c := range customers {
		rows = append(rows, domain.MaintenanceAffectedCustomer{
			ID:                 uuid.New(),
			MaintenanceEventID: eventID,
			CustomerID:         c.CustomerID,
			CustomerSegment:    c.CustomerSegment,
		})
	}
	written, err := s.affected.CreateBatch(ctx, rows)
	if err != nil {
		return 0, err
	}
	// Update the event's count + approval requirement.
	total := len(customers)
	_ = s.reader.UpdateAffectedCount(ctx, eventID, total)
	s.log.Info("materialized affected customers", "event_id", eventID,
		"resolved", total, "inserted", written)
	return total, nil
}

// IsApprovalRequired checks the threshold rules.
func (s *MaintenanceService) IsApprovalRequired(affectedCount int, segment domain.CustomerSegment) bool {
	return domain.ApprovalRequired(affectedCount, segment)
}

// Approve stamps approved_by + approved_at on the maintenance event.
// Returns Conflict if the caller isn't authorized (delegated to RBAC at
// the handler; this is a state-machine guard only).
func (s *MaintenanceService) Approve(ctx context.Context, eventID, byUserID uuid.UUID) error {
	if s == nil || s.reader == nil {
		return derrors.Internal("operations.maintenance.no_reader", "maintenance reader not wired")
	}
	event, err := s.reader.FindEvent(ctx, eventID)
	if err != nil {
		return err
	}
	if event == nil {
		return derrors.NotFound("operations.maintenance.event_not_found", "maintenance event not found")
	}
	if event.ApprovedAt != nil {
		return derrors.Conflict("operations.maintenance.already_approved", "maintenance event already approved")
	}
	return s.reader.MarkApproved(ctx, eventID, byUserID, time.Now().UTC())
}

// NotifyLeadTime is the cron entry point: find affected-customer rows
// whose maintenance event's scheduled_start is within lead_time_hours
// + has approval (if required), dispatch the customer notification
// per-channel, and stamp notified_at.
//
// Returns the count of notifications dispatched.
func (s *MaintenanceService) NotifyLeadTime(ctx context.Context) (int, error) {
	if s == nil || s.reader == nil || s.affected == nil {
		return 0, nil
	}
	// We treat 72h as the outer envelope; the per-event lead_time_hours
	// is on the event row, so we re-check before dispatch.
	events, err := s.reader.ListPendingLeadTimeNotify(ctx, 72, 50)
	if err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	dispatched := 0
	for _, ev := range events {
		if ev.ApprovalRequired && ev.ApprovedAt == nil {
			continue
		}
		leadHours := ev.LeadTimeNotifyHours
		if leadHours <= 0 {
			leadHours = domain.LeadTimeHours(ev.CustomerSegment)
		}
		notifyFrom := ev.ScheduledStart.Add(-time.Duration(leadHours) * time.Hour)
		if now.Before(notifyFrom) {
			continue
		}
		pending, perr := s.affected.ListPendingNotification(ctx, ev.ID, 500)
		if perr != nil {
			s.log.Warn("list pending notifications failed", "event_id", ev.ID, "err", perr)
			continue
		}
		for _, row := range pending {
			if s.dispatcher == nil {
				_ = s.affected.MarkNotifyError(ctx, row.ID, "dispatcher not wired")
				continue
			}
			channel, derr := s.dispatcher.NotifyCustomer(ctx, ev.ID, row.CustomerID, row.CustomerSegment)
			if derr != nil {
				_ = s.affected.MarkNotifyError(ctx, row.ID, derr.Error())
				continue
			}
			if err := s.affected.MarkNotified(ctx, row.ID, channel); err == nil {
				dispatched++
			}
		}
	}
	return dispatched, nil
}

// DetectOverrun sweeps in-progress events past scheduled_end+tolerance
// and stamps overrun_at. Idempotent via the overrun_at IS NULL filter
// inside ListInProgress (the reader's SQL ignores already-flagged rows).
func (s *MaintenanceService) DetectOverrun(ctx context.Context) (int, error) {
	if s == nil || s.reader == nil {
		return 0, nil
	}
	events, err := s.reader.ListInProgress(ctx, 200)
	if err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	flagged := 0
	for _, ev := range events {
		if !domain.IsOverrun(ev.ScheduledEnd, ev.Status, now, s.overrunTol) {
			continue
		}
		if ev.OverrunAt != nil {
			continue
		}
		if err := s.reader.MarkOverrun(ctx, ev.ID, now); err != nil {
			s.log.Warn("mark overrun failed", "event_id", ev.ID, "err", err)
			continue
		}
		flagged++
	}
	if flagged > 0 {
		s.log.Info("maintenance overrun detection swept", "flagged", flagged)
	}
	return flagged, nil
}

// EscalateOverrun creates an escalation row at the next level. Reason
// is required (validated at the handler). The maintenance event itself
// isn't re-routed here — escalation is purely metadata + notification.
func (s *MaintenanceService) EscalateOverrun(ctx context.Context, eventID uuid.UUID, reason string, escalatedToUserID *uuid.UUID) (*domain.MaintenanceEscalation, error) {
	if s == nil || s.escalations == nil {
		return nil, derrors.Internal("operations.maintenance.no_escalations_repo", "escalation repo not wired")
	}
	if reason == "" {
		return nil, derrors.Validation("operations.maintenance.escalation_reason_required", "reason is required")
	}
	current, err := s.escalations.HighestLevel(ctx, eventID)
	if err != nil {
		return nil, err
	}
	level := domain.NextLevel(current)
	e := domain.NewEscalation(eventID, level, reason, escalatedToUserID)
	if err := s.escalations.Create(ctx, &e); err != nil {
		return nil, err
	}
	return &e, nil
}

// ListAffected returns the per-event affected-customer roster.
func (s *MaintenanceService) ListAffected(ctx context.Context, eventID uuid.UUID) ([]domain.MaintenanceAffectedCustomer, error) {
	if s == nil || s.affected == nil {
		return nil, nil
	}
	return s.affected.ListByEvent(ctx, eventID)
}

// ListEscalations returns the per-event escalation chain.
func (s *MaintenanceService) ListEscalations(ctx context.Context, eventID uuid.UUID) ([]domain.MaintenanceEscalation, error) {
	if s == nil || s.escalations == nil {
		return nil, nil
	}
	return s.escalations.ListByEvent(ctx, eventID)
}
