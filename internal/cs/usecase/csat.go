package usecase

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/cs/domain"
	"github.com/ion-core/backend/internal/cs/port"
	"github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// CSATService — Wave 124 CSAT capture + invite dispatch.
// =====================================================================

type CSATService struct {
	csat       port.CSATRepository
	tickets    port.TicketRepository
	events     port.TicketEventRepository
	dispatcher port.CSATInviteDispatcher
	notifier   port.NotificationBridge
}

func NewCSATService(
	csat port.CSATRepository,
	tickets port.TicketRepository,
	events port.TicketEventRepository,
	dispatcher port.CSATInviteDispatcher,
	notifier port.NotificationBridge,
) *CSATService {
	return &CSATService{
		csat:       csat,
		tickets:    tickets,
		events:     events,
		dispatcher: dispatcher,
		notifier:   notifier,
	}
}

var _ port.CSATUseCase = (*CSATService)(nil)

// SendInvite dispatches a CSAT invite via the supplied channel. The
// dispatcher is the cross-context bridge (notifyx wrapper). Channel
// defaults to email when empty.
func (s *CSATService) SendInvite(ctx context.Context, ticketID uuid.UUID, channel string) error {
	if s.dispatcher == nil {
		return errors.Internal("cs.csat.no_dispatcher", "csat invite dispatcher not configured")
	}
	t, err := s.tickets.FindByID(ctx, ticketID)
	if err != nil {
		return err
	}
	channel = strings.TrimSpace(channel)
	if channel == "" {
		channel = "email"
	}
	if err := s.dispatcher.SendInvite(ctx, t.ID, t.CustomerID, channel); err != nil {
		return err
	}
	if s.events != nil {
		_ = s.events.Insert(ctx, domain.NewTicketEvent(t.ID, domain.EventKindStatusChange, nil, "system", map[string]any{
			"audit_kind": "csat_invite_sent",
			"channel":    channel,
		}))
	}
	return nil
}

// RecordResponse persists the customer's CSAT rating. If the rating is
// critically low (1-2) we emit a csat_critical event AND ping the
// assigned user's supervisor via the notifier.
func (s *CSATService) RecordResponse(ctx context.Context, ticketID uuid.UUID, rating int, comment, channel string) (*domain.CSATResponse, error) {
	t, err := s.tickets.FindByID(ctx, ticketID)
	if err != nil {
		return nil, err
	}
	ch := domain.CSATChannel(strings.TrimSpace(channel))
	if ch == "" {
		ch = domain.CSATChannelEmail
	}
	resp, err := domain.NewCSATResponse(t.ID, t.CustomerID, rating, comment, ch)
	if err != nil {
		return nil, err
	}
	if err := s.csat.Insert(ctx, resp); err != nil {
		return nil, err
	}
	if s.events != nil {
		_ = s.events.Insert(ctx, domain.NewTicketEvent(t.ID, domain.EventKindStatusChange, nil, "customer", map[string]any{
			"audit_kind": "csat_response",
			"rating":     rating,
			"channel":    string(ch),
		}))
	}
	if domain.IsCriticalLow(rating) {
		if s.events != nil {
			_ = s.events.Insert(ctx, domain.NewTicketEvent(t.ID, domain.EventKindEscalation, nil, "system", map[string]any{
				"audit_kind": "csat_critical",
				"rating":     rating,
			}))
		}
		if s.notifier != nil && t.AssignedUserID != nil {
			s.notifier.NotifyAssignment(ctx, t.ID, *t.AssignedUserID, "Critical CSAT: "+t.Title)
		}
	}
	return resp, nil
}

func (s *CSATService) Aggregations(ctx context.Context, f port.CSATAggregationFilter) (port.CSATAggregations, error) {
	return s.csat.Aggregations(ctx, f)
}

func (s *CSATService) GetByTicket(ctx context.Context, ticketID uuid.UUID) (*domain.CSATResponse, error) {
	return s.csat.FindByTicket(ctx, ticketID)
}

// FollowupTick is the daily cron entrypoint — re-sends CSAT invites
// for tickets resolved 24+ hours ago that haven't been answered.
// Each ticket gets exactly one re-invite (tracked via ticket_events
// kind=status_change with audit_kind=csat_invite_resent).
func (s *CSATService) FollowupTick(ctx context.Context, resolvedSince, resolvedBefore time.Time, limit int) (int, error) {
	if s.csat == nil || s.dispatcher == nil {
		return 0, nil
	}
	ids, err := s.csat.ListTicketsNeedingFollowupInvite(ctx, resolvedSince, resolvedBefore, limit)
	if err != nil {
		return 0, err
	}
	sent := 0
	for _, tid := range ids {
		t, err := s.tickets.FindByID(ctx, tid)
		if err != nil {
			continue
		}
		if err := s.dispatcher.SendInvite(ctx, t.ID, t.CustomerID, "email"); err != nil {
			continue
		}
		if s.events != nil {
			_ = s.events.Insert(ctx, domain.NewTicketEvent(t.ID, domain.EventKindStatusChange, nil, "system", map[string]any{
				"audit_kind": "csat_invite_resent",
				"channel":    "email",
			}))
		}
		sent++
	}
	return sent, nil
}
