// Wave 118 — Event ingestion + drain usecase.

package usecase

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/ion-core/backend/internal/hris/domain"
	"github.com/ion-core/backend/internal/hris/port"
	"github.com/ion-core/backend/pkg/audit"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// EventService owns the ingest queue + the per-tick drain.
//
// Dependencies: only `events` is mandatory. The four hooks
// (commission cessation, user deactivator, queue reassigner, RBAC) are
// all nil-safe — a missing hook logs warnOnce and falls through to a
// pure audit row so the queue stays drained.
type EventService struct {
	events    port.EventRepository
	employees port.EmployeeRepository

	commission port.CommissionCessationHook
	deactivate port.UserDeactivator
	reassign   port.FieldQueueReassigner
	rbac       port.RBACRecalculator

	auditWriter audit.Writer
	log         *slog.Logger

	// warnOnce avoids spamming the logs when a hook is missing across
	// thousands of events.
	warnOnce sync.Map
}

// EventServiceOpts is the option bag for NewEventService. All nil-safe.
type EventServiceOpts struct {
	Commission  port.CommissionCessationHook
	Deactivate  port.UserDeactivator
	Reassign    port.FieldQueueReassigner
	RBAC        port.RBACRecalculator
	AuditWriter audit.Writer
	Log         *slog.Logger
}

// NewEventService builds an EventService. events + employees are
// mandatory; everything else may be nil.
func NewEventService(events port.EventRepository, employees port.EmployeeRepository, opts EventServiceOpts) *EventService {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	auditW := opts.AuditWriter
	if auditW == nil {
		auditW = audit.Nop{}
	}
	return &EventService{
		events:      events,
		employees:   employees,
		commission:  opts.Commission,
		deactivate:  opts.Deactivate,
		reassign:    opts.Reassign,
		rbac:        opts.RBAC,
		auditWriter: auditW,
		log:         log.With("component", "hris.event"),
	}
}

// IngestEvents writes a batch of events. Idempotent: the repo dedupes
// on (id) so a re-poll of the gateway returns the same set with zero
// new rows.
func (s *EventService) IngestEvents(ctx context.Context, events []*domain.EmployeeEvent) (int, error) {
	if s == nil || s.events == nil {
		return 0, derrors.Wrap(derrors.KindInternal, "hris.event.ingest", "event repo not configured", nil)
	}
	if len(events) == 0 {
		return 0, nil
	}
	// Validate each event before persisting — reject the whole batch on
	// the first invalid one so the gateway sees a clean error.
	for _, e := range events {
		if e == nil {
			return 0, derrors.Validation("hris.event.nil", "nil event in batch")
		}
		if strings.TrimSpace(e.EmployeeNo) == "" {
			return 0, derrors.Validation("hris.event.employee_no_required", "employee_no is required on every event")
		}
		if !e.Kind.Valid() {
			return 0, derrors.Validation("hris.event.kind_invalid", "event kind "+string(e.Kind)+" is not supported")
		}
		if e.OccurredAt.IsZero() {
			return 0, derrors.Validation("hris.event.occurred_at_required", "occurred_at is required")
		}
	}
	return s.events.CreateMany(ctx, events)
}

// ProcessPending drains up to `limit` pending events. For each:
//   1. Re-fetch the employee (apply any direct status mutation).
//   2. Run ProcessHook to decide which bridges to call.
//   3. Fire each bridge with nil-safe fallback.
//   4. Mark the event processed (with the processing error if any).
//   5. Write an audit row.
//
// The processing-error column captures any bridge failure so an Ops
// dashboard can surface stuck events. We don't roll back the queue
// mark on bridge failure — that would re-fire commission cancellation
// on the next tick, which is the wrong direction. Instead we log
// processed_with_error so Ops can investigate.
func (s *EventService) ProcessPending(ctx context.Context, limit int) (int, error) {
	if s == nil || s.events == nil {
		return 0, derrors.Wrap(derrors.KindInternal, "hris.event.drain", "event repo not configured", nil)
	}
	if limit <= 0 {
		limit = 100
	}
	pending, err := s.events.ListPending(ctx, limit)
	if err != nil {
		return 0, err
	}
	processed := 0
	for i := range pending {
		ev := pending[i]
		directive := ev.ProcessHook()

		// Apply status mutation to the employee record (best-effort —
		// missing employee just means we audit the event and move on).
		if s.employees != nil {
			s.applyStatusMutation(ctx, &ev)
		}

		var bridgeErr string
		if directive.CancelCommissions {
			if s.commission != nil {
				resignDate := ev.OccurredAt
				if d, ok := ev.Payload["final_day"].(string); ok && d != "" {
					if parsed, perr := time.Parse("2006-01-02", d); perr == nil {
						resignDate = parsed
					}
				}
				if cerr := s.commission.OnResign(ctx, ev.EmployeeNo, resignDate); cerr != nil {
					s.log.Warn("commission cessation hook failed",
						"employee_no", ev.EmployeeNo, "err", cerr)
					bridgeErr = appendErr(bridgeErr, "commission: "+cerr.Error())
				}
			} else {
				s.warnTODO("commission_cessation_hook")
			}
		}
		if directive.DeactivateUser {
			if s.deactivate != nil {
				if derr := s.deactivate.DeactivateByEmployeeNo(ctx, ev.EmployeeNo); derr != nil {
					s.log.Warn("user deactivator hook failed",
						"employee_no", ev.EmployeeNo, "err", derr)
					bridgeErr = appendErr(bridgeErr, "deactivate: "+derr.Error())
				}
			} else {
				s.warnTODO("user_deactivator_hook")
			}
		}
		if directive.ReassignFieldQueue {
			if s.reassign != nil {
				if rerr := s.reassign.OnTransfer(ctx, ev.EmployeeNo); rerr != nil {
					s.log.Warn("field queue reassigner hook failed",
						"employee_no", ev.EmployeeNo, "err", rerr)
					bridgeErr = appendErr(bridgeErr, "reassign: "+rerr.Error())
				}
			} else {
				s.warnTODO("field_queue_reassigner_hook")
			}
		}
		if directive.UpdateRBAC {
			if s.rbac != nil {
				if rerr := s.rbac.OnRoleChange(ctx, ev.EmployeeNo); rerr != nil {
					s.log.Warn("rbac recalculator hook failed",
						"employee_no", ev.EmployeeNo, "err", rerr)
					bridgeErr = appendErr(bridgeErr, "rbac: "+rerr.Error())
				}
			} else {
				s.warnTODO("rbac_recalculator_hook")
			}
		}

		if merr := s.events.MarkProcessed(ctx, ev.ID, bridgeErr); merr != nil {
			s.log.Warn("event mark processed failed",
				"event_id", ev.ID, "err", merr)
			continue
		}
		processed++
		audit.SafeWrite(ctx, s.auditWriter, audit.Entry{
			Module:     "hris",
			RecordType: "hris.employee_event",
			RecordID:   ev.ID.String(),
			After:      string(ev.Kind),
			Reason:     "wave118." + directive.AuditReason,
		})
	}
	if processed > 0 {
		s.log.Info("event drain complete", "processed", processed, "total_in_batch", len(pending))
	}
	return processed, nil
}

// applyStatusMutation updates the employee record's status field based on
// the event kind. Best-effort — a missing employee just no-ops (the
// gateway sync should have created the row, but we don't fail the event
// if it hasn't).
func (s *EventService) applyStatusMutation(ctx context.Context, ev *domain.EmployeeEvent) {
	if s.employees == nil || ev == nil {
		return
	}
	emp, err := s.employees.FindByEmployeeNo(ctx, ev.EmployeeNo)
	if err != nil || emp == nil {
		return
	}
	mutated := false
	switch ev.Kind {
	case domain.EventKindResigned:
		// Compute resign date from payload final_day or occurred_at.
		resignDate := ev.OccurredAt
		if d, ok := ev.Payload["final_day"].(string); ok && d != "" {
			if parsed, perr := time.Parse("2006-01-02", d); perr == nil {
				resignDate = parsed
			}
		}
		if rerr := emp.Resign(resignDate, ""); rerr == nil {
			mutated = true
		}
	case domain.EventKindSuspended:
		if rerr := emp.Suspend(""); rerr == nil {
			mutated = true
		}
	case domain.EventKindReinstated:
		if rerr := emp.Reinstate(); rerr == nil {
			mutated = true
		}
	case domain.EventKindHired:
		if rerr := emp.Hire(ev.OccurredAt); rerr == nil {
			mutated = true
		}
	case domain.EventKindPromoted:
		if pos, ok := ev.Payload["new_position"].(string); ok && pos != "" {
			if rerr := emp.Promote(pos); rerr == nil {
				mutated = true
			}
		}
	}
	if mutated {
		_ = s.employees.Upsert(ctx, emp) // best-effort
	}
}

// ListEvents is the read path for the HTTP layer.
func (s *EventService) ListEvents(ctx context.Context, f port.EventFilter) ([]domain.EmployeeEvent, int, error) {
	if s == nil || s.events == nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "hris.event.list", "event repo not configured", nil)
	}
	if f.Limit <= 0 {
		f.Limit = 100
	}
	if f.Limit > 500 {
		f.Limit = 500
	}
	return s.events.List(ctx, f)
}

// warnTODO emits a structured warn once per (component, message). Mirrors
// the pattern used by Wave 114's orchestration service.
func (s *EventService) warnTODO(component string) {
	if s == nil || s.log == nil {
		return
	}
	if _, loaded := s.warnOnce.LoadOrStore(component, struct{}{}); loaded {
		return
	}
	s.log.Warn("TODO Wave 118 — bridge not wired", "component", component)
}

func appendErr(existing, msg string) string {
	if existing == "" {
		return msg
	}
	return existing + "; " + msg
}
