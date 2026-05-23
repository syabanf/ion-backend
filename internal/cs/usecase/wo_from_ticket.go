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
// WOFromTicketService — Wave 124 WO-from-Ticket bridge.
//
// Calls the WOFromTicketBridge implementation (an inline SQL adapter
// in cmd/cs-svc/main.go) to spawn a field.work_orders row, then stamps
// the ticket's related_wo_id and emits a ticket_event of kind
// wo_created.
// =====================================================================

type WOFromTicketService struct {
	tickets port.TicketRepository
	events  port.TicketEventRepository
	bridge  port.WOFromTicketBridge
}

func NewWOFromTicketService(
	tickets port.TicketRepository,
	events port.TicketEventRepository,
	bridge port.WOFromTicketBridge,
) *WOFromTicketService {
	return &WOFromTicketService{
		tickets: tickets,
		events:  events,
		bridge:  bridge,
	}
}

var _ port.WOFromTicketUseCase = (*WOFromTicketService)(nil)

func (s *WOFromTicketService) CreateWO(ctx context.Context, ticketID uuid.UUID, woTemplateID *uuid.UUID, scheduledAt *time.Time, byUserID uuid.UUID, actorRole string) (uuid.UUID, error) {
	if s.bridge == nil {
		return uuid.Nil, errors.Internal("cs.wft.no_bridge", "wo-from-ticket bridge not configured")
	}
	t, err := s.tickets.FindByID(ctx, ticketID)
	if err != nil {
		return uuid.Nil, err
	}
	if t.IsTerminal() {
		return uuid.Nil, errors.Conflict("cs.wft.ticket_closed", "cannot create WO from a closed ticket")
	}
	woID, err := s.bridge.CreateWOFromTicket(ctx, ticketID, woTemplateID, scheduledAt, byUserID)
	if err != nil {
		return uuid.Nil, err
	}
	t.RelatedWOID = &woID
	if err := s.tickets.Update(ctx, t); err != nil {
		return uuid.Nil, err
	}
	if s.events != nil {
		payload := map[string]any{
			"wo_id":      woID.String(),
			"created_by": byUserID.String(),
		}
		if woTemplateID != nil {
			payload["wo_template_id"] = woTemplateID.String()
		}
		if scheduledAt != nil {
			payload["scheduled_at"] = scheduledAt.UTC().Format(time.RFC3339)
		}
		_ = s.events.Insert(ctx, domain.NewTicketEvent(ticketID, domain.EventKindStatusChange, ptrIfNotNil(byUserID), actorRole, mergeMaps(payload, map[string]any{
			"audit_kind": "wo_created",
		})))
	}
	return woID, nil
}

func mergeMaps(a, b map[string]any) map[string]any {
	out := make(map[string]any, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}
