package usecase

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/cs/domain"
	"github.com/ion-core/backend/internal/cs/port"
	"github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// SLAService — Wave 124's SLA matrix application + breach evaluator.
//
// Hexagonal: depends only on the SLAMatrixRepository + TicketRepository
// + (optional) CustomerTypeResolver + TicketEventRepository for the
// audit trail. The cron in internal/cs/cron/cron.go calls
// EvaluateBreaches every 5 minutes.
//
// SLA values are stamped onto the ticket via domain.Ticket.ApplySLA at
// open time (and again on priority / type change). The breach evaluator
// uses domain.Ticket.EffectiveAge to subtract pause_seconds — this is
// the integration point with Wave 123.
// =====================================================================

type SLAService struct {
	matrix    port.SLAMatrixRepository
	tickets   port.TicketRepository
	events    port.TicketEventRepository
	resolver  port.CustomerTypeResolver
	notifier  port.NotificationBridge
	active    SLAActiveLister
}

// SLAActiveLister is what the evaluator needs from the ticket repo — a
// list of in-flight tickets to sweep. Wave 124 implemented on the
// postgres TicketRepository.
type SLAActiveLister interface {
	ListActiveForSLAEvaluation(ctx context.Context, limit int) ([]domain.Ticket, error)
}

func NewSLAService(
	matrix port.SLAMatrixRepository,
	tickets port.TicketRepository,
	events port.TicketEventRepository,
	resolver port.CustomerTypeResolver,
	notifier port.NotificationBridge,
	active SLAActiveLister,
) *SLAService {
	return &SLAService{
		matrix:   matrix,
		tickets:  tickets,
		events:   events,
		resolver: resolver,
		notifier: notifier,
		active:   active,
	}
}

var _ port.SLAUseCase = (*SLAService)(nil)

// ApplyOnCreate resolves the matrix entry for this ticket and stamps
// the SLA snapshot onto it. Caller must Update the ticket afterwards
// (the TicketService does this in its own CreateTicket flow).
//
// Best-effort: matrix-resolution failures fall back to a NoSLA stamp
// (zero-valued due dates + nil matrix id) so the ticket creation never
// fails because of an SLA infra glitch.
func (s *SLAService) ApplyOnCreate(ctx context.Context, t *domain.Ticket) {
	if s == nil || s.matrix == nil || t == nil {
		return
	}
	customerType := s.resolveCustomerType(ctx, t.CustomerID)
	entry, err := s.matrix.FindByKey(ctx, customerType, t.TicketType, t.Priority, time.Now().UTC())
	if err != nil || entry == nil {
		return
	}
	frDue, rvDue := entry.ResolveDueDates(t.CreatedAt)
	t.ApplySLA(entry.ID, frDue, rvDue)
}

// ApplyOnPriorityChange re-resolves the matrix for the new priority
// and stamps a fresh snapshot. Called by TicketService after the new
// priority has been persisted.
func (s *SLAService) ApplyOnPriorityChange(ctx context.Context, t *domain.Ticket) {
	if s == nil || s.matrix == nil || t == nil {
		return
	}
	customerType := s.resolveCustomerType(ctx, t.CustomerID)
	entry, err := s.matrix.FindByKey(ctx, customerType, t.TicketType, t.Priority, time.Now().UTC())
	if err != nil || entry == nil {
		return
	}
	// Recompute due dates from the original CreatedAt — priority change
	// doesn't reset the clock; the catalog calls this "re-anchoring".
	frDue, rvDue := entry.ResolveDueDates(t.CreatedAt)
	t.ApplySLA(entry.ID, frDue, rvDue)
}

// ApplyOnTypeChange is identical to ApplyOnPriorityChange — the matrix
// is keyed on ticket_type so a type change re-resolves to a new row.
func (s *SLAService) ApplyOnTypeChange(ctx context.Context, t *domain.Ticket) {
	s.ApplyOnPriorityChange(ctx, t)
}

func (s *SLAService) resolveCustomerType(ctx context.Context, customerID uuid.UUID) domain.CustomerType {
	if s.resolver == nil {
		return domain.CustomerTypeResidential
	}
	ct, err := s.resolver.Resolve(ctx, customerID)
	if err != nil {
		return domain.CustomerTypeResidential
	}
	return ct
}

// GetMatrix lists matrix entries for the admin UI.
func (s *SLAService) GetMatrix(ctx context.Context, f port.SLAMatrixFilter) ([]domain.SLAMatrixEntry, error) {
	if s.matrix == nil {
		return nil, errors.Internal("cs.sla.no_matrix", "sla matrix repository not configured")
	}
	return s.matrix.List(ctx, f)
}

// UpsertMatrix updates / inserts a matrix row.
func (s *SLAService) UpsertMatrix(ctx context.Context, e *domain.SLAMatrixEntry) error {
	if s.matrix == nil {
		return errors.Internal("cs.sla.no_matrix", "sla matrix repository not configured")
	}
	return s.matrix.Upsert(ctx, e)
}

// TicketSLA returns the per-ticket SLA snapshot + live remaining time.
func (s *SLAService) TicketSLA(ctx context.Context, ticketID uuid.UUID) (port.TicketSLASummary, error) {
	t, err := s.tickets.FindByID(ctx, ticketID)
	if err != nil {
		return port.TicketSLASummary{}, err
	}
	now := time.Now().UTC()
	return port.TicketSLASummary{
		TicketID:                  t.ID,
		SLAMatrixID:               t.SLAMatrixID,
		FirstResponseDueAt:        t.SLAFirstResponseDueAt,
		ResolveDueAt:              t.SLAResolveDueAt,
		RemainingFirstRespSeconds: t.RemainingFirstResponseSeconds(now),
		RemainingResolveSeconds:   t.RemainingResolveSeconds(now),
		BreachedFirstResponse:     t.SLABreachedFirstResponse,
		BreachedResolve:           t.SLABreachedResolve,
		WarnedAt:                  t.SLAWarnedAt,
	}, nil
}

// EvaluateBreaches sweeps in-flight tickets, flips breach flags, emits
// breach events, and dispatches warning notifications for tickets that
// crossed the breach-warn pct. Idempotent — already-flagged tickets
// are no-ops.
func (s *SLAService) EvaluateBreaches(ctx context.Context) (port.BreachReport, error) {
	out := port.BreachReport{}
	if s == nil || s.matrix == nil || s.active == nil {
		return out, nil
	}
	rows, err := s.active.ListActiveForSLAEvaluation(ctx, 500)
	if err != nil {
		return out, err
	}
	now := time.Now().UTC()
	for i := range rows {
		t := rows[i]
		out.Evaluated++

		entry, lookupErr := s.matrixForTicket(ctx, &t)
		if lookupErr != nil || entry == nil {
			continue
		}
		dirty := false

		// First-response breach.
		if !t.SLABreachedFirstResponse && entry.IsBreachedFirstResponse(now, &t) {
			t.MarkFirstResponseBreach(now)
			out.NewFirstResponseBreach++
			dirty = true
			s.recordBreachEvent(ctx, t.ID, "first_response", entry)
		}

		// Resolve breach.
		if !t.SLABreachedResolve && entry.IsBreachedResolve(now, &t) {
			t.MarkResolveBreach(now)
			out.NewResolveBreach++
			dirty = true
			s.recordBreachEvent(ctx, t.ID, "resolve", entry)
		}

		// Warn-window (only dispatched once via warned_at).
		if t.SLAWarnedAt == nil && entry.IsInWarnWindow(now, &t) {
			t.MarkWarned(now)
			out.WarningsDispatched++
			dirty = true
			s.dispatchWarning(ctx, &t, entry)
		}

		if dirty {
			if err := s.tickets.Update(ctx, &t); err != nil {
				// Best-effort — keep sweeping.
				continue
			}
		}
	}
	return out, nil
}

func (s *SLAService) matrixForTicket(ctx context.Context, t *domain.Ticket) (*domain.SLAMatrixEntry, error) {
	if t.SLAMatrixID != nil {
		entry, err := s.matrix.FindByID(ctx, *t.SLAMatrixID)
		if err == nil {
			return entry, nil
		}
	}
	customerType := s.resolveCustomerType(ctx, t.CustomerID)
	return s.matrix.FindByKey(ctx, customerType, t.TicketType, t.Priority, time.Now().UTC())
}

func (s *SLAService) recordBreachEvent(ctx context.Context, ticketID uuid.UUID, which string, entry *domain.SLAMatrixEntry) {
	if s.events == nil {
		return
	}
	payload := map[string]any{
		"kind":         which,
		"matrix_id":    entry.ID.String(),
		"customer_type": string(entry.CustomerType),
		"ticket_type":   string(entry.TicketType),
		"priority":      string(entry.Priority),
	}
	_ = s.events.Insert(ctx, domain.NewTicketEvent(ticketID, domain.EventKindSLABreach, nil, "system", payload))
}

func (s *SLAService) dispatchWarning(ctx context.Context, t *domain.Ticket, entry *domain.SLAMatrixEntry) {
	if s.events != nil {
		_ = s.events.Insert(ctx, domain.NewTicketEvent(t.ID, domain.EventKindSLABreach, nil, "system", map[string]any{
			"kind":      "warn",
			"matrix_id": entry.ID.String(),
		}))
	}
	if s.notifier != nil && t.AssignedUserID != nil {
		s.notifier.NotifyAssignment(ctx, t.ID, *t.AssignedUserID, "SLA warning: "+t.Title)
	}
}
