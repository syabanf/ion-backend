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

// =====================================================================
// Wave 124 — SLA service tests (TC-PSL-*).
// =====================================================================

// stubSLAMatrixRepo — in-memory port.SLAMatrixRepository
type stubSLAMatrixRepo struct {
	mu      sync.Mutex
	byID    map[uuid.UUID]*domain.SLAMatrixEntry
	byKey   map[string]*domain.SLAMatrixEntry // (ct|tt|prio)
}

func newStubSLAMatrixRepo() *stubSLAMatrixRepo {
	return &stubSLAMatrixRepo{
		byID:  map[uuid.UUID]*domain.SLAMatrixEntry{},
		byKey: map[string]*domain.SLAMatrixEntry{},
	}
}
func slaKey(ct domain.CustomerType, tt domain.TicketType, p domain.Priority) string {
	return string(ct) + "|" + string(tt) + "|" + string(p)
}
func (r *stubSLAMatrixRepo) FindByKey(_ context.Context, ct domain.CustomerType, tt domain.TicketType, p domain.Priority, _ time.Time) (*domain.SLAMatrixEntry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.byKey[slaKey(ct, tt, p)]
	if !ok {
		return nil, errNotFound
	}
	return e, nil
}
func (r *stubSLAMatrixRepo) FindByID(_ context.Context, id uuid.UUID) (*domain.SLAMatrixEntry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.byID[id]
	if !ok {
		return nil, errNotFound
	}
	return e, nil
}
func (r *stubSLAMatrixRepo) List(_ context.Context, _ port.SLAMatrixFilter) ([]domain.SLAMatrixEntry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []domain.SLAMatrixEntry{}
	for _, e := range r.byID {
		out = append(out, *e)
	}
	return out, nil
}
func (r *stubSLAMatrixRepo) Upsert(_ context.Context, e *domain.SLAMatrixEntry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byID[e.ID] = e
	r.byKey[slaKey(e.CustomerType, e.TicketType, e.Priority)] = e
	return nil
}

// stubCustomerTypeResolver — always returns the configured type.
type stubCustomerTypeResolver struct {
	t domain.CustomerType
}

func (r *stubCustomerTypeResolver) Resolve(_ context.Context, _ uuid.UUID) (domain.CustomerType, error) {
	if r.t == "" {
		return domain.CustomerTypeResidential, nil
	}
	return r.t, nil
}

// stubActiveLister — returns the supplied tickets verbatim.
type stubActiveLister struct {
	rows []domain.Ticket
}

func (s *stubActiveLister) ListActiveForSLAEvaluation(_ context.Context, _ int) ([]domain.Ticket, error) {
	return s.rows, nil
}

// =====================================================================
// Tests
// =====================================================================

func TestSLA_ApplyOnCreateStampsDueDates(t *testing.T) {
	ctx := context.Background()
	repo := newStubSLAMatrixRepo()
	entry, err := domain.NewSLAMatrixEntry(
		domain.CustomerTypeResidential, domain.TicketTypeTechnical, domain.PriorityHigh,
		30, 480, 0.80, time.Now().UTC().AddDate(0, 0, -1),
	)
	if err != nil {
		t.Fatalf("NewSLAMatrixEntry: %v", err)
	}
	if err := repo.Upsert(ctx, entry); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	tickets := &stubTicketRepo{}
	events := &stubEventRepo{}
	sla := NewSLAService(repo, tickets, events, &stubCustomerTypeResolver{t: domain.CustomerTypeResidential}, nil, &stubActiveLister{})

	tk, _ := domain.NewTicket(uuid.New(), uuid.New(), domain.OpenedViaPortal,
		domain.TicketTypeTechnical, "Title", "Desc", domain.PriorityHigh)
	sla.ApplyOnCreate(ctx, tk)
	if tk.SLAMatrixID == nil {
		t.Fatalf("SLAMatrixID should be stamped")
	}
	if *tk.SLAMatrixID != entry.ID {
		t.Fatalf("matrix id mismatch")
	}
	if tk.SLAFirstResponseDueAt == nil || tk.SLAResolveDueAt == nil {
		t.Fatalf("due dates not stamped")
	}
	if got := tk.SLAFirstResponseDueAt.Sub(tk.CreatedAt); got != 30*time.Minute {
		t.Fatalf("first-response due offset = %s, want 30m", got)
	}
}

func TestSLA_ApplyOnCreate_NoMatrixMatch_NoOps(t *testing.T) {
	ctx := context.Background()
	repo := newStubSLAMatrixRepo() // empty
	sla := NewSLAService(repo, &stubTicketRepo{}, &stubEventRepo{}, &stubCustomerTypeResolver{}, nil, &stubActiveLister{})
	tk, _ := domain.NewTicket(uuid.New(), uuid.New(), domain.OpenedViaPortal,
		domain.TicketTypeTechnical, "x", "", domain.PriorityNormal)
	sla.ApplyOnCreate(ctx, tk)
	if tk.SLAMatrixID != nil {
		t.Fatalf("should not stamp matrix id when no match")
	}
}

func TestSLA_EvaluateBreaches_FlipsFlagsAndEmitsEvent(t *testing.T) {
	ctx := context.Background()
	repo := newStubSLAMatrixRepo()
	entry, _ := domain.NewSLAMatrixEntry(
		domain.CustomerTypeResidential, domain.TicketTypeTechnical, domain.PriorityHigh,
		30, 60, 0.80, time.Now().UTC().AddDate(0, 0, -1),
	)
	_ = repo.Upsert(ctx, entry)

	// Build an aged-out ticket: created 2h ago, no first response.
	tk, _ := domain.NewTicket(uuid.New(), uuid.New(), domain.OpenedViaPortal,
		domain.TicketTypeTechnical, "no internet", "", domain.PriorityHigh)
	tk.CreatedAt = time.Now().UTC().Add(-2 * time.Hour)
	tk.SLAMatrixID = &entry.ID
	frDue := tk.CreatedAt.Add(30 * time.Minute)
	rvDue := tk.CreatedAt.Add(60 * time.Minute)
	tk.SLAFirstResponseDueAt = &frDue
	tk.SLAResolveDueAt = &rvDue

	tickets := &stubTicketRepo{byID: map[uuid.UUID]*domain.Ticket{tk.ID: tk}}
	events := &stubEventRepo{}
	lister := &stubActiveLister{rows: []domain.Ticket{*tk}}
	sla := NewSLAService(repo, tickets, events, &stubCustomerTypeResolver{}, nil, lister)

	report, err := sla.EvaluateBreaches(ctx)
	if err != nil {
		t.Fatalf("EvaluateBreaches: %v", err)
	}
	if report.NewFirstResponseBreach != 1 {
		t.Fatalf("expected 1 first-response breach, got %d", report.NewFirstResponseBreach)
	}
	if report.NewResolveBreach != 1 {
		t.Fatalf("expected 1 resolve breach, got %d", report.NewResolveBreach)
	}
	if events.inserted == 0 {
		t.Fatalf("expected breach events recorded")
	}
}

func TestSLA_EvaluateBreaches_Idempotent(t *testing.T) {
	ctx := context.Background()
	repo := newStubSLAMatrixRepo()
	entry, _ := domain.NewSLAMatrixEntry(
		domain.CustomerTypeResidential, domain.TicketTypeTechnical, domain.PriorityHigh,
		30, 60, 0.80, time.Now().UTC().AddDate(0, 0, -1),
	)
	_ = repo.Upsert(ctx, entry)

	tk, _ := domain.NewTicket(uuid.New(), uuid.New(), domain.OpenedViaPortal,
		domain.TicketTypeTechnical, "x", "", domain.PriorityHigh)
	tk.CreatedAt = time.Now().UTC().Add(-2 * time.Hour)
	tk.SLAMatrixID = &entry.ID
	tk.SLABreachedFirstResponse = true
	tk.SLABreachedResolve = true

	tickets := &stubTicketRepo{byID: map[uuid.UUID]*domain.Ticket{tk.ID: tk}}
	events := &stubEventRepo{}
	lister := &stubActiveLister{rows: []domain.Ticket{*tk}}
	sla := NewSLAService(repo, tickets, events, &stubCustomerTypeResolver{}, nil, lister)

	report, err := sla.EvaluateBreaches(ctx)
	if err != nil {
		t.Fatalf("EvaluateBreaches: %v", err)
	}
	if report.NewFirstResponseBreach != 0 || report.NewResolveBreach != 0 {
		t.Fatalf("expected zero new breaches on already-flagged ticket")
	}
}

func TestSLA_TicketServiceCreateStampsSLA(t *testing.T) {
	// Verify the integration point: ticket creation flows through
	// TicketService.CreateTicket → SLAService.ApplyOnCreate.
	ctx := context.Background()
	repo := newStubSLAMatrixRepo()
	entry, _ := domain.NewSLAMatrixEntry(
		domain.CustomerTypeResidential, domain.TicketTypeTechnical, domain.PriorityNormal,
		120, 1440, 0.80, time.Now().UTC().AddDate(0, 0, -1),
	)
	_ = repo.Upsert(ctx, entry)

	tickets := &stubTicketRepo{}
	events := &stubEventRepo{}
	sla := NewSLAService(repo, tickets, events, &stubCustomerTypeResolver{}, nil, &stubActiveLister{})

	ticketSvc := NewTicketService(tickets, events, nil).WithSLA(sla)
	tk, err := ticketSvc.CreateTicket(ctx, port.CreateTicketInput{
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
	if tk.SLAMatrixID == nil {
		t.Fatalf("expected SLA snapshot stamped on create")
	}
	if tk.SLAFirstResponseDueAt == nil {
		t.Fatalf("expected first-response due stamped")
	}
}

func TestSLA_PriorityChangeReStamps(t *testing.T) {
	ctx := context.Background()
	repo := newStubSLAMatrixRepo()
	high, _ := domain.NewSLAMatrixEntry(
		domain.CustomerTypeResidential, domain.TicketTypeTechnical, domain.PriorityHigh,
		15, 240, 0.80, time.Now().UTC().AddDate(0, 0, -1),
	)
	normal, _ := domain.NewSLAMatrixEntry(
		domain.CustomerTypeResidential, domain.TicketTypeTechnical, domain.PriorityNormal,
		120, 1440, 0.80, time.Now().UTC().AddDate(0, 0, -1),
	)
	_ = repo.Upsert(ctx, high)
	_ = repo.Upsert(ctx, normal)

	tickets := &stubTicketRepo{}
	events := &stubEventRepo{}
	sla := NewSLAService(repo, tickets, events, &stubCustomerTypeResolver{}, nil, &stubActiveLister{})
	ticketSvc := NewTicketService(tickets, events, nil).WithSLA(sla)

	tk, _ := ticketSvc.CreateTicket(ctx, port.CreateTicketInput{
		CustomerID: uuid.New(), OpenedBy: uuid.New(),
		OpenedVia: domain.OpenedViaPortal, TicketType: domain.TicketTypeTechnical,
		Title: "x", Description: "y", Priority: domain.PriorityNormal,
	})
	origMatrix := *tk.SLAMatrixID

	if _, err := ticketSvc.ChangePriority(ctx, tk.ID, domain.PriorityHigh, uuid.New(), "cs_supervisor"); err != nil {
		t.Fatalf("ChangePriority: %v", err)
	}
	tk2, _ := tickets.FindByID(ctx, tk.ID)
	if tk2.SLAMatrixID == nil {
		t.Fatalf("matrix id should still be set after priority change")
	}
	if *tk2.SLAMatrixID == origMatrix {
		t.Fatalf("matrix id should have re-resolved to the high-priority row")
	}
}
