package usecase

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/cs/domain"
	"github.com/ion-core/backend/internal/cs/port"
)

// stubSRRepo — in-memory port.ServiceRequestRepository.
type stubSRRepo struct {
	mu       sync.Mutex
	byID     map[uuid.UUID]*domain.ServiceRequest
	inserted int
}

func newStubSRRepo() *stubSRRepo {
	return &stubSRRepo{byID: map[uuid.UUID]*domain.ServiceRequest{}}
}
func (r *stubSRRepo) Insert(_ context.Context, sr *domain.ServiceRequest) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byID[sr.ID] = sr
	r.inserted++
	return nil
}
func (r *stubSRRepo) Update(_ context.Context, sr *domain.ServiceRequest) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byID[sr.ID] = sr
	return nil
}
func (r *stubSRRepo) FindByID(_ context.Context, id uuid.UUID) (*domain.ServiceRequest, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	sr, ok := r.byID[id]
	if !ok {
		return nil, errNotFound
	}
	return sr, nil
}
func (r *stubSRRepo) List(_ context.Context, _ port.ServiceRequestFilter) ([]domain.ServiceRequest, int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []domain.ServiceRequest{}
	for _, sr := range r.byID {
		out = append(out, *sr)
	}
	return out, len(out), nil
}
func (r *stubSRRepo) ListPendingApproval(_ context.Context, _ int) ([]domain.ServiceRequest, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []domain.ServiceRequest{}
	for _, sr := range r.byID {
		if sr.Status == domain.SRStatusSubmitted {
			out = append(out, *sr)
		}
	}
	return out, nil
}

// woBridgeRecorder implements port.WOFromTicketBridge.
type woBridgeRecorder struct {
	calls   int
	lastTID uuid.UUID
	lastBy  uuid.UUID
}

func (b *woBridgeRecorder) CreateWOFromTicket(_ context.Context, ticketID uuid.UUID, _ *uuid.UUID, _ *time.Time, by uuid.UUID) (uuid.UUID, error) {
	b.calls++
	b.lastTID = ticketID
	b.lastBy = by
	return uuid.New(), nil
}

// =====================================================================
// Tests
// =====================================================================

func TestSR_Submit_AutoCreatesTicket(t *testing.T) {
	ctx := context.Background()
	srs := newStubSRRepo()
	tickets := &stubTicketRepo{}
	events := &stubEventRepo{}
	ticketSvc := NewTicketService(tickets, events, nil)
	srSvc := NewServiceRequestService(srs, ticketSvc, events)

	customerID := uuid.New()
	submitter := uuid.New()
	sr, err := srSvc.Submit(ctx, port.SubmitServiceRequestInput{
		CustomerID:  customerID,
		RequestType: domain.SRTypeSpeedUpgrade, // no approval → auto-approved
		SubmittedBy: submitter,
		OpenedVia:   domain.OpenedViaPortal,
		Title:       "Upgrade to 100Mbps",
		Priority:    domain.PriorityNormal,
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if sr.Status != domain.SRStatusApproved {
		t.Fatalf("expected auto-approved status, got %s", sr.Status)
	}
	if sr.TicketID == uuid.Nil {
		t.Fatalf("expected auto-created ticket id")
	}
	if srs.inserted != 1 {
		t.Fatalf("expected 1 sr inserted, got %d", srs.inserted)
	}
}

func TestSR_StartFulfillmentCallsWOBridge(t *testing.T) {
	ctx := context.Background()
	srs := newStubSRRepo()
	tickets := &stubTicketRepo{}
	events := &stubEventRepo{}
	ticketSvc := NewTicketService(tickets, events, nil)
	bridge := &woBridgeRecorder{}
	srSvc := NewServiceRequestService(srs, ticketSvc, events).WithWOBridge(bridge)

	sr, err := srSvc.Submit(ctx, port.SubmitServiceRequestInput{
		CustomerID:  uuid.New(),
		RequestType: domain.SRTypeAddOn, // auto-approved
		SubmittedBy: uuid.New(),
		OpenedVia:   domain.OpenedViaPortal,
		Title:       "Add IP",
		Priority:    domain.PriorityNormal,
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if _, err := srSvc.StartFulfillment(ctx, sr.ID, uuid.New()); err != nil {
		t.Fatalf("StartFulfillment: %v", err)
	}
	if bridge.calls != 1 {
		t.Fatalf("expected exactly one WO-bridge call, got %d", bridge.calls)
	}
	if bridge.lastTID != sr.TicketID {
		t.Fatalf("WO bridge called with wrong ticket id")
	}
}

func TestSR_LifecycleApprovalToFulfillment(t *testing.T) {
	ctx := context.Background()
	srs := newStubSRRepo()
	tickets := &stubTicketRepo{}
	events := &stubEventRepo{}
	ticketSvc := NewTicketService(tickets, events, nil)
	srSvc := NewServiceRequestService(srs, ticketSvc, events)

	sr, err := srSvc.Submit(ctx, port.SubmitServiceRequestInput{
		CustomerID:  uuid.New(),
		RequestType: domain.SRTypeSpeedDowngrade, // requires approval
		SubmittedBy: uuid.New(),
		OpenedVia:   domain.OpenedViaPortal,
		Title:       "Downgrade",
		Priority:    domain.PriorityNormal,
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if sr.Status != domain.SRStatusSubmitted {
		t.Fatalf("expected submitted, got %s", sr.Status)
	}

	approver := uuid.New()
	sr2, err := srSvc.Approve(ctx, sr.ID, approver)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if sr2.Status != domain.SRStatusApproved {
		t.Fatalf("expected approved, got %s", sr2.Status)
	}
	// StartFulfillment without a WO bridge should still flip to in_progress.
	sr3, err := srSvc.StartFulfillment(ctx, sr.ID, approver)
	if err != nil {
		t.Fatalf("StartFulfillment: %v", err)
	}
	if sr3.Status != domain.SRStatusInProgress {
		t.Fatalf("expected in_progress, got %s", sr3.Status)
	}
	sr4, err := srSvc.MarkFulfilled(ctx, sr.ID, approver)
	if err != nil {
		t.Fatalf("MarkFulfilled: %v", err)
	}
	if sr4.Status != domain.SRStatusFulfilled {
		t.Fatalf("expected fulfilled, got %s", sr4.Status)
	}
}
