package usecase

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/cs/domain"
	"github.com/ion-core/backend/internal/cs/port"
)

// TestTicketService_AssignFiresEventAndNotification verifies the
// orchestration: domain.Assign + event row + NotifyAssignment.
func TestTicketService_AssignFiresEventAndNotification(t *testing.T) {
	ctx := context.Background()
	customer := uuid.New()
	opener := uuid.New()
	assignee := uuid.New()

	tk := mustNewTicket(t, customer, opener)
	tickets := &stubTicketRepo{byID: map[uuid.UUID]*domain.Ticket{tk.ID: tk}}
	events := &stubEventRepo{}
	notifier := &stubNotifier{}

	svc := NewTicketService(tickets, events, notifier)
	out, err := svc.AssignTicket(ctx, tk.ID, assignee, uuid.New(), "cs_supervisor")
	if err != nil {
		t.Fatalf("AssignTicket: %v", err)
	}
	if out.Status != domain.TicketStatusAssigned {
		t.Fatalf("status = %v want assigned", out.Status)
	}
	if events.inserted != 1 {
		t.Fatalf("expected 1 event row, got %d", events.inserted)
	}
	if notifier.assignmentCalls != 1 {
		t.Fatalf("NotifyAssignment calls = %d want 1", notifier.assignmentCalls)
	}
}

// TestTicketService_CreateTicketAssignsNumber verifies the
// repository.NextTicketNo plumbing.
func TestTicketService_CreateTicketAssignsNumber(t *testing.T) {
	ctx := context.Background()
	tickets := &stubTicketRepo{}
	svc := NewTicketService(tickets, &stubEventRepo{}, nil)

	tk, err := svc.CreateTicket(ctx, port.CreateTicketInput{
		CustomerID:  uuid.New(),
		OpenedBy:    uuid.New(),
		OpenedVia:   domain.OpenedViaPortal,
		TicketType:  domain.TicketTypeTechnical,
		Title:       "test",
		Description: "desc",
		Priority:    domain.PriorityNormal,
	})
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	if tk.TicketNo != "TKT-2026-00000001" {
		t.Fatalf("ticket_no = %q want stub value", tk.TicketNo)
	}
}

// TestTicketService_FullLifecycleHappyPath walks through
// open → assigned → in_progress → resolved → closed with events
// asserted along the way.
func TestTicketService_FullLifecycleHappyPath(t *testing.T) {
	ctx := context.Background()
	customer := uuid.New()
	opener := uuid.New()
	assignee := uuid.New()

	tk := mustNewTicket(t, customer, opener)
	tickets := &stubTicketRepo{byID: map[uuid.UUID]*domain.Ticket{tk.ID: tk}}
	events := &stubEventRepo{}
	svc := NewTicketService(tickets, events, nil)

	if _, err := svc.AssignTicket(ctx, tk.ID, assignee, uuid.New(), "cs_supervisor"); err != nil {
		t.Fatalf("Assign: %v", err)
	}
	if _, err := svc.StartTicket(ctx, tk.ID, assignee, "cs_agent"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := svc.PauseTicket(ctx, tk.ID, domain.PauseWaitCustomer, "wait", assignee, "cs_agent"); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if _, err := svc.ResumeTicket(ctx, tk.ID, assignee, "cs_agent"); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if _, err := svc.ResolveTicket(ctx, tk.ID, "fixed", assignee, "cs_agent"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if _, err := svc.CloseTicket(ctx, tk.ID, assignee, "cs_agent"); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// 6 transitions → 6 events
	if events.inserted != 6 {
		t.Fatalf("expected 6 events, got %d", events.inserted)
	}
}
