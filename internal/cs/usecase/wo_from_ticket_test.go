package usecase

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/cs/domain"
)

func TestWOFromTicket_StampsRelatedWOID(t *testing.T) {
	ctx := context.Background()
	tk := mustNewTicket(t, uuid.New(), uuid.New())
	if err := tk.Start(uuid.New()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	tickets := &stubTicketRepo{byID: map[uuid.UUID]*domain.Ticket{tk.ID: tk}}
	events := &stubEventRepo{}
	bridge := &woBridgeRecorder{}

	svc := NewWOFromTicketService(tickets, events, bridge)
	by := uuid.New()
	woID, err := svc.CreateWO(ctx, tk.ID, nil, nil, by, "cs_agent")
	if err != nil {
		t.Fatalf("CreateWO: %v", err)
	}
	if woID == uuid.Nil {
		t.Fatalf("expected non-nil wo id")
	}
	if bridge.calls != 1 {
		t.Fatalf("expected 1 bridge call, got %d", bridge.calls)
	}
	got, _ := tickets.FindByID(ctx, tk.ID)
	if got.RelatedWOID == nil || *got.RelatedWOID != woID {
		t.Fatalf("RelatedWOID not stamped on ticket")
	}
	if events.inserted == 0 {
		t.Fatalf("expected wo_created event recorded")
	}
}

func TestWOFromTicket_RefusesClosedTicket(t *testing.T) {
	ctx := context.Background()
	tk := mustNewTicket(t, uuid.New(), uuid.New())
	if err := tk.Start(uuid.New()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := tk.Resolve(uuid.New(), "done"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if err := tk.Close(uuid.New()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	tickets := &stubTicketRepo{byID: map[uuid.UUID]*domain.Ticket{tk.ID: tk}}
	bridge := &woBridgeRecorder{}
	svc := NewWOFromTicketService(tickets, &stubEventRepo{}, bridge)
	if _, err := svc.CreateWO(ctx, tk.ID, nil, nil, uuid.New(), "cs_agent"); err == nil {
		t.Fatalf("expected conflict creating WO from closed ticket")
	}
	if bridge.calls != 0 {
		t.Fatalf("bridge should not be called for closed tickets")
	}
}
