// Package usecase wires the customer-service bounded context together.
//
// Service depends only on the port interfaces, never on the postgres
// adapters directly — that's what lets the bounded context move to
// its own service binary (cmd/cs-svc) later without touching domain
// rules.
package usecase

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/cs/domain"
	"github.com/ion-core/backend/internal/cs/port"
)

// TicketService implements port.TicketUseCase.
type TicketService struct {
	tickets  port.TicketRepository
	events   port.TicketEventRepository
	notifier port.NotificationBridge

	// Wave 124 — optional add-ons. nil-safe.
	sla     *SLAService                       // applies SLA on create / priority change
	csat    *CSATService                      // dispatches CSAT invite on resolve
	history port.AssignmentHistoryRepository  // writes ticket_assignments_history rows
}

// NewTicketService wires the ticket orchestration. notifier is
// optional — pass nil to skip notifications.
func NewTicketService(tickets port.TicketRepository, events port.TicketEventRepository, notifier port.NotificationBridge) *TicketService {
	return &TicketService{tickets: tickets, events: events, notifier: notifier}
}

// WithSLA wires the SLA service. nil-safe — if not called, CreateTicket
// skips matrix lookup.
func (s *TicketService) WithSLA(sla *SLAService) *TicketService {
	s.sla = sla
	return s
}

// WithCSAT wires the CSAT service. nil-safe — if not called,
// ResolveTicket skips invite dispatch.
func (s *TicketService) WithCSAT(csat *CSATService) *TicketService {
	s.csat = csat
	return s
}

// WithAssignmentHistory wires the audit-row repo. nil-safe.
func (s *TicketService) WithAssignmentHistory(h port.AssignmentHistoryRepository) *TicketService {
	s.history = h
	return s
}

var _ port.TicketUseCase = (*TicketService)(nil)

// CreateTicket persists a new ticket in `open` status + writes the
// first status_change event.
func (s *TicketService) CreateTicket(ctx context.Context, in port.CreateTicketInput) (*domain.Ticket, error) {
	t, err := domain.NewTicket(in.CustomerID, in.OpenedBy, in.OpenedVia, in.TicketType, in.Title, in.Description, in.Priority)
	if err != nil {
		return nil, err
	}
	t.SourceMetadata = in.SourceMetadata

	no, err := s.tickets.NextTicketNo(ctx, t.CreatedAt.Year())
	if err != nil {
		return nil, err
	}
	t.TicketNo = no

	// Wave 124 — stamp SLA snapshot before the INSERT so the ticket
	// row carries the due dates from the moment it lands in storage.
	if s.sla != nil {
		s.sla.ApplyOnCreate(ctx, t)
	}

	if err := s.tickets.Create(ctx, t); err != nil {
		return nil, err
	}
	s.recordEvent(ctx, t.ID, domain.EventKindStatusChange, &in.OpenedBy, "system", map[string]any{
		"to":         string(domain.TicketStatusOpen),
		"opened_via": string(in.OpenedVia),
	})
	return t, nil
}

func (s *TicketService) GetTicket(ctx context.Context, id uuid.UUID) (*domain.Ticket, error) {
	return s.tickets.FindByID(ctx, id)
}

func (s *TicketService) ListTickets(ctx context.Context, f port.TicketListFilter) ([]domain.Ticket, int, error) {
	return s.tickets.List(ctx, f)
}

// AssignTicket flips open → assigned + records assignee. Idempotent
// re-assign is allowed via the domain method.
func (s *TicketService) AssignTicket(ctx context.Context, ticketID, assignedUserID, byUserID uuid.UUID, actorRole string) (*domain.Ticket, error) {
	t, err := s.tickets.FindByID(ctx, ticketID)
	if err != nil {
		return nil, err
	}
	prev := t.Status
	prevUser := t.AssignedUserID
	if err := t.Assign(assignedUserID); err != nil {
		return nil, err
	}
	if err := s.tickets.Update(ctx, t); err != nil {
		return nil, err
	}
	s.recordEvent(ctx, t.ID, domain.EventKindAssignment, &byUserID, actorRole, map[string]any{
		"from_status":        string(prev),
		"to_status":          string(t.Status),
		"assignee_user_id":   assignedUserID.String(),
	})
	// Wave 124 — append-only audit row (cs.ticket_assignments_history).
	if s.history != nil {
		assignee := assignedUserID
		var byPtr *uuid.UUID
		if byUserID != uuid.Nil {
			b := byUserID
			byPtr = &b
		}
		_ = s.history.Insert(ctx, domain.NewAssignmentEvent(
			t.ID, domain.AssignmentKindUser,
			prevUser, &assignee,
			nil, nil,
			byPtr, "user-assignment",
		))
	}
	if s.notifier != nil {
		s.notifier.NotifyAssignment(ctx, t.ID, assignedUserID, t.Title)
	}
	return t, nil
}

// StartTicket flips open|assigned → in_progress + stamps first_response_at.
func (s *TicketService) StartTicket(ctx context.Context, ticketID, byUserID uuid.UUID, actorRole string) (*domain.Ticket, error) {
	t, err := s.tickets.FindByID(ctx, ticketID)
	if err != nil {
		return nil, err
	}
	prev := t.Status
	if err := t.Start(byUserID); err != nil {
		return nil, err
	}
	if err := s.tickets.Update(ctx, t); err != nil {
		return nil, err
	}
	s.recordEvent(ctx, t.ID, domain.EventKindStatusChange, &byUserID, actorRole, map[string]any{
		"from": string(prev), "to": string(t.Status),
	})
	return t, nil
}

// PauseTicket flips in_progress → pending_customer|pending_internal.
func (s *TicketService) PauseTicket(ctx context.Context, ticketID uuid.UUID, kind domain.PauseKind, reason string, byUserID uuid.UUID, actorRole string) (*domain.Ticket, error) {
	t, err := s.tickets.FindByID(ctx, ticketID)
	if err != nil {
		return nil, err
	}
	prev := t.Status
	if err := t.Pause(kind, reason); err != nil {
		return nil, err
	}
	if err := s.tickets.Update(ctx, t); err != nil {
		return nil, err
	}
	s.recordEvent(ctx, t.ID, domain.EventKindStatusChange, &byUserID, actorRole, map[string]any{
		"from": string(prev), "to": string(t.Status), "pause_kind": string(kind), "reason": reason,
	})
	return t, nil
}

// ResumeTicket flips pending_* → in_progress + accumulates pause_seconds.
func (s *TicketService) ResumeTicket(ctx context.Context, ticketID, byUserID uuid.UUID, actorRole string) (*domain.Ticket, error) {
	t, err := s.tickets.FindByID(ctx, ticketID)
	if err != nil {
		return nil, err
	}
	prev := t.Status
	if err := t.Resume(byUserID); err != nil {
		return nil, err
	}
	if err := s.tickets.Update(ctx, t); err != nil {
		return nil, err
	}
	s.recordEvent(ctx, t.ID, domain.EventKindStatusChange, &byUserID, actorRole, map[string]any{
		"from": string(prev), "to": string(t.Status), "pause_seconds": t.PauseSeconds,
	})
	return t, nil
}

// ResolveTicket flips in_progress → resolved + records resolution +
// dispatches the CSAT invite (Wave 124, nil-safe if no CSAT service
// is wired).
func (s *TicketService) ResolveTicket(ctx context.Context, ticketID uuid.UUID, resolution string, byUserID uuid.UUID, actorRole string) (*domain.Ticket, error) {
	t, err := s.tickets.FindByID(ctx, ticketID)
	if err != nil {
		return nil, err
	}
	prev := t.Status
	if err := t.Resolve(byUserID, resolution); err != nil {
		return nil, err
	}
	if err := s.tickets.Update(ctx, t); err != nil {
		return nil, err
	}
	s.recordEvent(ctx, t.ID, domain.EventKindStatusChange, &byUserID, actorRole, map[string]any{
		"from": string(prev), "to": string(t.Status), "resolution": resolution,
	})
	// Wave 124 — fire CSAT invite. Best-effort — if the dispatcher
	// glitches the ticket still resolves.
	if s.csat != nil {
		_ = s.csat.SendInvite(ctx, t.ID, "email")
	}
	return t, nil
}

// CloseTicket flips resolved → closed.
func (s *TicketService) CloseTicket(ctx context.Context, ticketID, byUserID uuid.UUID, actorRole string) (*domain.Ticket, error) {
	t, err := s.tickets.FindByID(ctx, ticketID)
	if err != nil {
		return nil, err
	}
	prev := t.Status
	if err := t.Close(byUserID); err != nil {
		return nil, err
	}
	if err := s.tickets.Update(ctx, t); err != nil {
		return nil, err
	}
	s.recordEvent(ctx, t.ID, domain.EventKindClose, &byUserID, actorRole, map[string]any{
		"from": string(prev), "to": string(t.Status),
	})
	return t, nil
}

// ReopenTicket flips resolved|closed → in_progress + increments escalation_level.
func (s *TicketService) ReopenTicket(ctx context.Context, ticketID, byUserID uuid.UUID, reason, actorRole string) (*domain.Ticket, error) {
	t, err := s.tickets.FindByID(ctx, ticketID)
	if err != nil {
		return nil, err
	}
	prev := t.Status
	if err := t.Reopen(byUserID, reason); err != nil {
		return nil, err
	}
	if err := s.tickets.Update(ctx, t); err != nil {
		return nil, err
	}
	s.recordEvent(ctx, t.ID, domain.EventKindReopen, &byUserID, actorRole, map[string]any{
		"from": string(prev), "to": string(t.Status), "reason": reason, "escalation_level": t.EscalationLevel,
	})
	return t, nil
}

// ChangePriority on a non-closed ticket.
func (s *TicketService) ChangePriority(ctx context.Context, ticketID uuid.UUID, newPriority domain.Priority, byUserID uuid.UUID, actorRole string) (*domain.Ticket, error) {
	t, err := s.tickets.FindByID(ctx, ticketID)
	if err != nil {
		return nil, err
	}
	prev := t.Priority
	if err := t.ChangePriority(newPriority); err != nil {
		return nil, err
	}
	// Wave 124 — re-resolve the matrix for the new priority before
	// persisting. The new due dates land in the same Update.
	if s.sla != nil {
		s.sla.ApplyOnPriorityChange(ctx, t)
	}
	if err := s.tickets.Update(ctx, t); err != nil {
		return nil, err
	}
	s.recordEvent(ctx, t.ID, domain.EventKindPriorityChange, &byUserID, actorRole, map[string]any{
		"from": string(prev), "to": string(newPriority),
	})
	return t, nil
}

func (s *TicketService) ListEvents(ctx context.Context, ticketID uuid.UUID, limit, offset int) ([]domain.TicketEvent, error) {
	return s.events.List(ctx, ticketID, limit, offset)
}

// recordEvent is a best-effort timeline write. A failure here is
// logged via the event repo's typed error but doesn't fail the
// transition — the ticket row update has already committed.
func (s *TicketService) recordEvent(ctx context.Context, ticketID uuid.UUID, kind domain.EventKind, actorID *uuid.UUID, actorRole string, payload map[string]any) {
	if s.events == nil {
		return
	}
	ev := domain.NewTicketEvent(ticketID, kind, actorID, actorRole, payload)
	_ = s.events.Insert(ctx, ev)
}

// AutoCloseResolved is the daily cron entrypoint. Resolved tickets
// older than cutoff (e.g. 14 days) auto-flip to closed.
func (s *TicketService) AutoCloseResolved(ctx context.Context, repo port.AutoCloseRepository, cutoff time.Time, limit int) (int, error) {
	if repo == nil {
		return 0, nil
	}
	rows, err := repo.ListResolvedOlderThan(ctx, cutoff, limit)
	if err != nil {
		return 0, err
	}
	closed := 0
	systemActor := uuid.Nil
	for i := range rows {
		t := rows[i]
		if err := t.Close(systemActor); err != nil {
			continue
		}
		if err := s.tickets.Update(ctx, &t); err != nil {
			continue
		}
		s.recordEvent(ctx, t.ID, domain.EventKindClose, nil, "system", map[string]any{
			"auto":  true,
			"after": "resolved_timeout",
		})
		closed++
	}
	return closed, nil
}
