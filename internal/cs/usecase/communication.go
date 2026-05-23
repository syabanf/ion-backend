package usecase

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/cs/domain"
	"github.com/ion-core/backend/internal/cs/port"
	"github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// CommunicationService — Wave 124 inbound + outbound message log.
//
// Outbound: agents call LogOutbound after sending a reply via the
// relevant channel adapter (the actual send is the adapter's
// responsibility; this service writes the audit row).
//
// Inbound: webhook handlers (email pollers / WhatsApp gateway) call
// LogInbound. If we can find a ticket by external_message_id / In-Reply-To,
// we auto-link + resume the ticket from PendingCustomer.
// =====================================================================

type CommunicationService struct {
	comms   port.CommunicationRepository
	tickets port.TicketRepository
	events  port.TicketEventRepository
}

func NewCommunicationService(
	comms port.CommunicationRepository,
	tickets port.TicketRepository,
	events port.TicketEventRepository,
) *CommunicationService {
	return &CommunicationService{
		comms:   comms,
		tickets: tickets,
		events:  events,
	}
}

var _ port.CommunicationUseCase = (*CommunicationService)(nil)

func (s *CommunicationService) LogOutbound(ctx context.Context, in port.LogOutboundInput) (*domain.Communication, error) {
	tid := in.TicketID
	c, err := domain.NewCommunication(
		&tid,
		in.Kind,
		in.CounterpartyKind,
		in.CounterpartyID,
		in.CounterpartyLabel,
		in.Subject, in.Body,
		in.Attachments,
	)
	if err != nil {
		return nil, err
	}
	if c.Direction != domain.CommDirectionOut {
		return nil, errors.Validation("cs.comm.direction_mismatch",
			"outbound log used with an inbound kind: "+string(in.Kind))
	}
	if err := s.comms.Insert(ctx, c); err != nil {
		return nil, err
	}
	if s.events != nil && tid != uuid.Nil {
		_ = s.events.Insert(ctx, domain.NewTicketEvent(tid, domain.EventKindStatusChange, ptrIfNotNil(in.ByUserID), in.ActorRole, map[string]any{
			"audit_kind":         "communication_sent",
			"communication_id":   c.ID.String(),
			"kind":               string(in.Kind),
			"counterparty_kind":  string(in.CounterpartyKind),
			"counterparty_label": in.CounterpartyLabel,
		}))
	}
	return c, nil
}

// LogInbound handles a webhook payload. Best-effort ticket linking:
//   - If TicketID is explicitly supplied, use it.
//   - Else try FindByExternalMessageID on the In-Reply-To / thread root.
//   - Else create the communication row with NULL ticket_id (orphan;
//     an agent can attach it to a ticket later).
func (s *CommunicationService) LogInbound(ctx context.Context, in port.LogInboundInput) (*domain.Communication, error) {
	var ticketID *uuid.UUID
	if in.TicketID != nil {
		ticketID = in.TicketID
	} else if mid := strings.TrimSpace(in.ExternalMessageID); mid != "" {
		// Try to find a prior communication with this external_message_id.
		// (For Wave 124 the lookup matches the EXACT same message id,
		// which is a thin first cut — Wave 127 will expand to In-Reply-To
		// headers + WhatsApp thread ids.)
		if prior, err := s.comms.FindByExternalMessageID(ctx, mid); err == nil && prior != nil && prior.TicketID != nil {
			ticketID = prior.TicketID
		}
	}

	c, err := domain.NewCommunication(
		ticketID,
		in.Kind,
		domain.CounterpartyCustomer,
		nil,
		in.CounterpartyLabel,
		in.Subject, in.Body,
		in.Attachments,
	)
	if err != nil {
		return nil, err
	}
	c.ExternalMessageID = strings.TrimSpace(in.ExternalMessageID)
	if c.Direction != domain.CommDirectionIn {
		return nil, errors.Validation("cs.comm.direction_mismatch",
			"inbound log used with an outbound kind: "+string(in.Kind))
	}
	if err := s.comms.Insert(ctx, c); err != nil {
		return nil, err
	}

	// Auto-resume the ticket if it's in pending_customer.
	if ticketID != nil {
		t, err := s.tickets.FindByID(ctx, *ticketID)
		if err == nil && t != nil && t.Status == domain.TicketStatusPendingCustomer {
			if err := t.Resume(uuid.Nil); err == nil {
				_ = s.tickets.Update(ctx, t)
				if s.events != nil {
					_ = s.events.Insert(ctx, domain.NewTicketEvent(t.ID, domain.EventKindStatusChange, nil, "system", map[string]any{
						"audit_kind": "resumed_by_inbound_communication",
						"from":       string(domain.TicketStatusPendingCustomer),
						"to":         string(t.Status),
					}))
				}
			}
		}
		if s.events != nil {
			_ = s.events.Insert(ctx, domain.NewTicketEvent(*ticketID, domain.EventKindStatusChange, nil, "customer", map[string]any{
				"audit_kind":         "communication_received",
				"communication_id":   c.ID.String(),
				"kind":               string(in.Kind),
				"counterparty_label": in.CounterpartyLabel,
			}))
		}
	}
	return c, nil
}

func (s *CommunicationService) List(ctx context.Context, ticketID uuid.UUID, limit, offset int) ([]domain.Communication, error) {
	return s.comms.ListByTicket(ctx, ticketID, limit, offset)
}
