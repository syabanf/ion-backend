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
// ServiceRequestService — Wave 124 service-request lifecycle.
//
// Auto-creates a parent ticket when one isn't supplied. State machine
// lives in domain.ServiceRequest; this service just orchestrates the
// repo writes + event audit + optional WO bridge.
// =====================================================================

type ServiceRequestService struct {
	srs      port.ServiceRequestRepository
	tickets  *TicketService // for auto-ticket creation
	woBridge port.WOFromTicketBridge
	events   port.TicketEventRepository
}

func NewServiceRequestService(
	srs port.ServiceRequestRepository,
	tickets *TicketService,
	events port.TicketEventRepository,
) *ServiceRequestService {
	return &ServiceRequestService{
		srs:     srs,
		tickets: tickets,
		events:  events,
	}
}

// WithWOBridge wires the WO-from-Ticket bridge. nil-safe — if not set,
// StartFulfillment just flips status to in_progress without a WO.
func (s *ServiceRequestService) WithWOBridge(b port.WOFromTicketBridge) *ServiceRequestService {
	s.woBridge = b
	return s
}

var _ port.ServiceRequestUseCase = (*ServiceRequestService)(nil)

// Submit creates a service request. If TicketID is nil, auto-creates
// a `service_request` ticket using the supplied OpenedVia/Title/etc.
func (s *ServiceRequestService) Submit(ctx context.Context, in port.SubmitServiceRequestInput) (*domain.ServiceRequest, error) {
	if s.srs == nil {
		return nil, errors.Internal("cs.sr.no_repo", "service request repository not configured")
	}

	var ticketID uuid.UUID
	if in.TicketID != nil {
		ticketID = *in.TicketID
	} else if s.tickets != nil {
		// Auto-create the parent ticket.
		title := in.Title
		if title == "" {
			title = "Service request: " + string(in.RequestType)
		}
		via := in.OpenedVia
		if via == "" {
			via = domain.OpenedViaPortal
		}
		prio := in.Priority
		if prio == "" {
			prio = domain.PriorityNormal
		}
		t, err := s.tickets.CreateTicket(ctx, port.CreateTicketInput{
			CustomerID:  in.CustomerID,
			OpenedBy:    in.SubmittedBy,
			OpenedVia:   via,
			TicketType:  domain.TicketTypeServiceRequest,
			Title:       title,
			Description: in.Description,
			Priority:    prio,
		})
		if err != nil {
			return nil, err
		}
		ticketID = t.ID
	} else {
		return nil, errors.Validation("cs.sr.ticket_required", "ticket_id is required (or auto-ticket service must be configured)")
	}

	submitterPtr := &in.SubmittedBy
	if in.SubmittedBy == uuid.Nil {
		submitterPtr = nil
	}
	sr, err := domain.NewServiceRequest(ticketID, in.CustomerID, in.RequestType, submitterPtr, in.Payload)
	if err != nil {
		return nil, err
	}
	if err := s.srs.Insert(ctx, sr); err != nil {
		return nil, err
	}

	// Audit event on the parent ticket.
	s.recordEvent(ctx, ticketID, "service_request_submitted", &in.SubmittedBy, map[string]any{
		"sr_id":        sr.ID.String(),
		"request_type": string(sr.RequestType),
		"status":       string(sr.Status),
		"auto_approved": sr.Status == domain.SRStatusApproved,
	})

	return sr, nil
}

func (s *ServiceRequestService) Approve(ctx context.Context, srID, byUserID uuid.UUID) (*domain.ServiceRequest, error) {
	sr, err := s.srs.FindByID(ctx, srID)
	if err != nil {
		return nil, err
	}
	if err := sr.Approve(byUserID); err != nil {
		return nil, err
	}
	if err := s.srs.Update(ctx, sr); err != nil {
		return nil, err
	}
	s.recordEvent(ctx, sr.TicketID, "service_request_approved", &byUserID, map[string]any{
		"sr_id": sr.ID.String(),
	})
	return sr, nil
}

func (s *ServiceRequestService) Reject(ctx context.Context, srID, byUserID uuid.UUID, reason string) (*domain.ServiceRequest, error) {
	sr, err := s.srs.FindByID(ctx, srID)
	if err != nil {
		return nil, err
	}
	if err := sr.Reject(byUserID, reason); err != nil {
		return nil, err
	}
	if err := s.srs.Update(ctx, sr); err != nil {
		return nil, err
	}
	s.recordEvent(ctx, sr.TicketID, "service_request_rejected", &byUserID, map[string]any{
		"sr_id":  sr.ID.String(),
		"reason": reason,
	})
	return sr, nil
}

// StartFulfillment moves the SR to in_progress and, if a WO bridge is
// wired, spawns a WO so the operations team picks it up.
func (s *ServiceRequestService) StartFulfillment(ctx context.Context, srID, byUserID uuid.UUID) (*domain.ServiceRequest, error) {
	sr, err := s.srs.FindByID(ctx, srID)
	if err != nil {
		return nil, err
	}
	if err := sr.StartFulfillment(); err != nil {
		return nil, err
	}
	// Spawn a WO if a bridge is wired. Failure here flows the SR back
	// to approved so the caller can retry.
	if s.woBridge != nil {
		_, woErr := s.woBridge.CreateWOFromTicket(ctx, sr.TicketID, nil, nil, byUserID)
		if woErr != nil {
			return nil, woErr
		}
	}
	if err := s.srs.Update(ctx, sr); err != nil {
		return nil, err
	}
	s.recordEvent(ctx, sr.TicketID, "service_request_in_progress", &byUserID, map[string]any{
		"sr_id": sr.ID.String(),
	})
	return sr, nil
}

func (s *ServiceRequestService) MarkFulfilled(ctx context.Context, srID, byUserID uuid.UUID) (*domain.ServiceRequest, error) {
	sr, err := s.srs.FindByID(ctx, srID)
	if err != nil {
		return nil, err
	}
	if err := sr.MarkFulfilled(time.Now().UTC()); err != nil {
		return nil, err
	}
	if err := s.srs.Update(ctx, sr); err != nil {
		return nil, err
	}
	s.recordEvent(ctx, sr.TicketID, "service_request_fulfilled", &byUserID, map[string]any{
		"sr_id": sr.ID.String(),
	})
	return sr, nil
}

func (s *ServiceRequestService) Cancel(ctx context.Context, srID, byUserID uuid.UUID, reason string) (*domain.ServiceRequest, error) {
	sr, err := s.srs.FindByID(ctx, srID)
	if err != nil {
		return nil, err
	}
	if err := sr.Cancel(byUserID, reason); err != nil {
		return nil, err
	}
	if err := s.srs.Update(ctx, sr); err != nil {
		return nil, err
	}
	s.recordEvent(ctx, sr.TicketID, "service_request_cancelled", &byUserID, map[string]any{
		"sr_id":  sr.ID.String(),
		"reason": reason,
	})
	return sr, nil
}

func (s *ServiceRequestService) Get(ctx context.Context, id uuid.UUID) (*domain.ServiceRequest, error) {
	return s.srs.FindByID(ctx, id)
}

func (s *ServiceRequestService) List(ctx context.Context, f port.ServiceRequestFilter) ([]domain.ServiceRequest, int, error) {
	return s.srs.List(ctx, f)
}

func (s *ServiceRequestService) recordEvent(ctx context.Context, ticketID uuid.UUID, kindStr string, by *uuid.UUID, payload map[string]any) {
	if s.events == nil {
		return
	}
	// Service-request transitions use the generic status_change kind
	// with payload.kind = "service_request_*".
	payload["audit_kind"] = kindStr
	_ = s.events.Insert(ctx, domain.NewTicketEvent(ticketID, domain.EventKindStatusChange, by, "system", payload))
}
