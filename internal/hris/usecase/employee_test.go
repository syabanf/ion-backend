// Wave 118 — HRIS usecase tests.
//
// Coverage:
//   - EmployeeService.Upsert idempotent on employee_no (TC-HRI-001/002)
//   - EmployeeService.Resign + IsResignedByEmployeeNo (TC-HRI-005/006)
//   - EventService.IngestEvents + ProcessPending (TC-HRI-005/007/008)
//   - SyncService.RunFullSync with stub gateway (TC-HRI-003/004)
//   - Bridge invocation on resign (TC-HRI-006)

package usecase

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/hris/domain"
	"github.com/ion-core/backend/internal/hris/port"
)

// =====================================================================
// Stub repositories
// =====================================================================

type stubEmployeeRepo struct {
	mu  sync.Mutex
	rows map[string]*domain.Employee
}

func newStubEmployeeRepo() *stubEmployeeRepo {
	return &stubEmployeeRepo{rows: make(map[string]*domain.Employee)}
}

func (r *stubEmployeeRepo) Upsert(_ context.Context, e *domain.Employee) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *e
	r.rows[e.EmployeeNo] = &cp
	return nil
}

func (r *stubEmployeeRepo) FindByEmployeeNo(_ context.Context, employeeNo string) (*domain.Employee, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.rows[employeeNo]
	if !ok {
		return nil, nil
	}
	cp := *e
	return &cp, nil
}

func (r *stubEmployeeRepo) FindByID(_ context.Context, id uuid.UUID) (*domain.Employee, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.rows {
		if e.ID == id {
			cp := *e
			return &cp, nil
		}
	}
	return nil, nil
}

func (r *stubEmployeeRepo) List(_ context.Context, f port.EmployeeFilter) ([]domain.Employee, int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]domain.Employee, 0, len(r.rows))
	for _, e := range r.rows {
		out = append(out, *e)
	}
	return out, len(out), nil
}

type stubEventRepo struct {
	mu   sync.Mutex
	rows []domain.EmployeeEvent
}

func (r *stubEventRepo) CreateMany(_ context.Context, events []*domain.EmployeeEvent) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	inserted := 0
	for _, ev := range events {
		dup := false
		for _, ex := range r.rows {
			if ex.ID == ev.ID {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		r.rows = append(r.rows, *ev)
		inserted++
	}
	return inserted, nil
}

func (r *stubEventRepo) ListPending(_ context.Context, limit int) ([]domain.EmployeeEvent, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []domain.EmployeeEvent
	for _, ev := range r.rows {
		if !ev.Processed {
			out = append(out, ev)
		}
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (r *stubEventRepo) List(_ context.Context, f port.EventFilter) ([]domain.EmployeeEvent, int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]domain.EmployeeEvent(nil), r.rows...), len(r.rows), nil
}

func (r *stubEventRepo) MarkProcessed(_ context.Context, id uuid.UUID, processingError string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.rows {
		if r.rows[i].ID == id {
			now := time.Now().UTC()
			r.rows[i].Processed = true
			r.rows[i].ProcessedAt = &now
			r.rows[i].ProcessingError = processingError
			return nil
		}
	}
	return errors.New("event not found")
}

// =====================================================================
// Stub bridges
// =====================================================================

type stubCommissionHook struct{ calls []string }

func (s *stubCommissionHook) OnResign(_ context.Context, employeeNo string, _ time.Time) error {
	s.calls = append(s.calls, employeeNo)
	return nil
}

type stubDeactivator struct{ calls []string }

func (s *stubDeactivator) DeactivateByEmployeeNo(_ context.Context, employeeNo string) error {
	s.calls = append(s.calls, employeeNo)
	return nil
}

type stubReassign struct{ calls []string }

func (s *stubReassign) OnTransfer(_ context.Context, employeeNo string) error {
	s.calls = append(s.calls, employeeNo)
	return nil
}

type stubRBAC struct{ calls []string }

func (s *stubRBAC) OnRoleChange(_ context.Context, employeeNo string) error {
	s.calls = append(s.calls, employeeNo)
	return nil
}

type stubGateway struct {
	employees []port.EmployeeRecord
	events    []*domain.EmployeeEvent
}

func (g *stubGateway) FetchEmployees(_ context.Context, _ time.Time) ([]port.EmployeeRecord, error) {
	out := make([]port.EmployeeRecord, len(g.employees))
	copy(out, g.employees)
	return out, nil
}

func (g *stubGateway) FetchEvents(_ context.Context, _ time.Time) ([]*domain.EmployeeEvent, error) {
	out := make([]*domain.EmployeeEvent, len(g.events))
	copy(out, g.events)
	return out, nil
}

// =====================================================================
// Tests — EmployeeService
// =====================================================================

func TestEmployeeService_Upsert_Idempotent(t *testing.T) {
	repo := newStubEmployeeRepo()
	svc := NewEmployeeService(repo, nil, nil)
	ctx := context.Background()

	rec := port.EmployeeRecord{
		EmployeeNo: "EMP1",
		FullName:   "Alice",
		Email:      "alice@ion.example",
		Status:     domain.EmployeeStatusActive,
	}
	e1, err := svc.Upsert(ctx, rec)
	if err != nil {
		t.Fatalf("first Upsert: %v", err)
	}
	e2, err := svc.Upsert(ctx, rec)
	if err != nil {
		t.Fatalf("second Upsert: %v", err)
	}
	if e1.ID != e2.ID {
		t.Fatalf("Upsert is not idempotent on employee_no: %v vs %v", e1.ID, e2.ID)
	}

	// Update the name; same employee_no should keep the same ID.
	rec2 := rec
	rec2.FullName = "Alice Updated"
	e3, err := svc.Upsert(ctx, rec2)
	if err != nil {
		t.Fatalf("third Upsert: %v", err)
	}
	if e3.ID != e1.ID {
		t.Fatalf("Upsert with same employee_no must preserve id: got %v want %v", e3.ID, e1.ID)
	}
	if e3.FullName != "Alice Updated" {
		t.Fatalf("Upsert did not update full_name: %s", e3.FullName)
	}
}

func TestEmployeeService_Resign_BridgesIntoCessation(t *testing.T) {
	repo := newStubEmployeeRepo()
	svc := NewEmployeeService(repo, nil, nil)
	ctx := context.Background()

	_, err := svc.Upsert(ctx, port.EmployeeRecord{
		EmployeeNo: "EMP1", FullName: "Alice", Status: domain.EmployeeStatusActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	resignDate := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	e, err := svc.Resign(ctx, "EMP1", resignDate, "moved")
	if err != nil {
		t.Fatalf("Resign: %v", err)
	}
	if e.Status != domain.EmployeeStatusResigned {
		t.Fatalf("Resign did not transition status: %s", e.Status)
	}

	// IsResignedByEmployeeNo gate for Wave 114's tick:
	// paid_at BEFORE resign date → still employed → false.
	if svc.IsResignedByEmployeeNo(ctx, "EMP1", time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)) {
		t.Fatal("paid_at before resign should be IsResignedByEmployeeNo=false")
	}
	// paid_at AFTER resign date → had already left → true.
	if !svc.IsResignedByEmployeeNo(ctx, "EMP1", time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)) {
		t.Fatal("paid_at after resign should be IsResignedByEmployeeNo=true")
	}
}

func TestEmployeeService_Resign_NotFound(t *testing.T) {
	repo := newStubEmployeeRepo()
	svc := NewEmployeeService(repo, nil, nil)
	if _, err := svc.Resign(context.Background(), "GHOST", time.Now(), ""); err == nil {
		t.Fatal("expected NotFound for missing employee")
	}
}

// =====================================================================
// Tests — EventService
// =====================================================================

func TestEventService_Ingest_Idempotent(t *testing.T) {
	events := &stubEventRepo{}
	repo := newStubEmployeeRepo()
	svc := NewEventService(events, repo, EventServiceOpts{})

	ev, _ := domain.NewEmployeeEvent("EMP1", domain.EventKindResigned, nil, time.Now().UTC(), "stub")

	n1, err := svc.IngestEvents(context.Background(), []*domain.EmployeeEvent{ev})
	if err != nil {
		t.Fatalf("first ingest: %v", err)
	}
	if n1 != 1 {
		t.Fatalf("first ingest: want inserted=1, got %d", n1)
	}
	// Re-ingest the same event id: 0 new rows.
	n2, err := svc.IngestEvents(context.Background(), []*domain.EmployeeEvent{ev})
	if err != nil {
		t.Fatalf("second ingest: %v", err)
	}
	if n2 != 0 {
		t.Fatalf("second ingest must be idempotent: want 0, got %d", n2)
	}
}

func TestEventService_ProcessPending_FiresAllBridges(t *testing.T) {
	events := &stubEventRepo{}
	repo := newStubEmployeeRepo()
	// Seed the employee so ProcessPending can find it for the status mutation.
	_, _ = (NewEmployeeService(repo, nil, nil)).Upsert(context.Background(), port.EmployeeRecord{
		EmployeeNo: "EMP1", FullName: "Alice", Status: domain.EmployeeStatusActive,
	})

	hook := &stubCommissionHook{}
	deact := &stubDeactivator{}
	reassign := &stubReassign{}
	rbac := &stubRBAC{}

	svc := NewEventService(events, repo, EventServiceOpts{
		Commission: hook,
		Deactivate: deact,
		Reassign:   reassign,
		RBAC:       rbac,
	})
	ctx := context.Background()

	// Three events: resigned, transferred, role_changed.
	resign, _ := domain.NewEmployeeEvent("EMP1", domain.EventKindResigned,
		map[string]any{"final_day": "2026-03-31"},
		time.Date(2026, 3, 31, 23, 59, 0, 0, time.UTC), "stub")
	transfer, _ := domain.NewEmployeeEvent("EMP1", domain.EventKindTransferred, nil,
		time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), "stub")
	role, _ := domain.NewEmployeeEvent("EMP1", domain.EventKindRoleChanged, nil,
		time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC), "stub")

	if _, err := svc.IngestEvents(ctx, []*domain.EmployeeEvent{resign, transfer, role}); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	n, err := svc.ProcessPending(ctx, 100)
	if err != nil {
		t.Fatalf("ProcessPending: %v", err)
	}
	if n != 3 {
		t.Fatalf("processed: want 3, got %d", n)
	}

	if len(hook.calls) != 1 || hook.calls[0] != "EMP1" {
		t.Fatalf("commission cessation hook: want 1 call for EMP1, got %v", hook.calls)
	}
	if len(deact.calls) != 1 {
		t.Fatalf("user deactivator: want 1 call, got %v", deact.calls)
	}
	if len(reassign.calls) != 1 {
		t.Fatalf("queue reassigner: want 1 call, got %v", reassign.calls)
	}
	if len(rbac.calls) != 1 {
		t.Fatalf("rbac recalculator: want 1 call, got %v", rbac.calls)
	}

	// Second drain should find no work — all events are marked processed.
	n2, err := svc.ProcessPending(ctx, 100)
	if err != nil {
		t.Fatalf("ProcessPending second: %v", err)
	}
	if n2 != 0 {
		t.Fatalf("second drain: want 0, got %d", n2)
	}
}

func TestEventService_ProcessPending_NilBridgesDoesNotFail(t *testing.T) {
	events := &stubEventRepo{}
	repo := newStubEmployeeRepo()
	svc := NewEventService(events, repo, EventServiceOpts{}) // all bridges nil
	ctx := context.Background()

	ev, _ := domain.NewEmployeeEvent("EMP1", domain.EventKindResigned, nil, time.Now().UTC(), "stub")
	if _, err := svc.IngestEvents(ctx, []*domain.EmployeeEvent{ev}); err != nil {
		t.Fatal(err)
	}
	n, err := svc.ProcessPending(ctx, 10)
	if err != nil {
		t.Fatalf("ProcessPending: %v", err)
	}
	if n != 1 {
		t.Fatalf("nil bridges: event should still drain. got %d", n)
	}
}

// =====================================================================
// Tests — SyncService
// =====================================================================

func TestSyncService_RunFullSync_WithStubGateway(t *testing.T) {
	repo := newStubEmployeeRepo()
	events := &stubEventRepo{}
	empSvc := NewEmployeeService(repo, nil, nil)
	evSvc := NewEventService(events, repo, EventServiceOpts{})

	hire := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)
	gw := &stubGateway{
		employees: []port.EmployeeRecord{
			{EmployeeNo: "EMP1", FullName: "Alice", HireDate: &hire, Status: domain.EmployeeStatusActive},
			{EmployeeNo: "EMP2", FullName: "Bob", HireDate: &hire, Status: domain.EmployeeStatusActive},
		},
	}
	syncSvc := NewSyncService(SyncServiceOpts{Gateway: gw, Employee: empSvc, Event: evSvc})

	res, err := syncSvc.RunFullSync(context.Background())
	if err != nil {
		t.Fatalf("RunFullSync: %v", err)
	}
	if res.EmployeesUpserted != 2 {
		t.Fatalf("upserted: want 2, got %d", res.EmployeesUpserted)
	}

	// Re-run: same gateway data, should still upsert (idempotent at repo)
	// but events_ingested stays 0 because we still have no events.
	res2, err := syncSvc.RunFullSync(context.Background())
	if err != nil {
		t.Fatalf("RunFullSync second: %v", err)
	}
	if res2.EventsIngested != 0 {
		t.Fatalf("second sync should ingest 0 new events, got %d", res2.EventsIngested)
	}
}

func TestSyncService_NilGateway_NoOps(t *testing.T) {
	syncSvc := NewSyncService(SyncServiceOpts{Gateway: nil})
	res, err := syncSvc.RunFullSync(context.Background())
	if err != nil {
		t.Fatalf("RunFullSync with nil gateway: %v", err)
	}
	if res.EmployeesUpserted != 0 || res.EventsIngested != 0 {
		t.Fatal("nil gateway should no-op")
	}
}

// =====================================================================
// IsResignedReader integration with Wave 114's orchestration
// =====================================================================

// reseignedReaderViaService wraps EmployeeService so it satisfies
// port.HRISResignedReader. We use a custom resolver that maps any
// uuid → a fixed employee_no, simulating an identity bridge.
type resignedReaderViaService struct {
	empSvc *EmployeeService
	// map user_id → employee_no
	identityMap map[uuid.UUID]string
}

func (r *resignedReaderViaService) IsResignedBefore(ctx context.Context, salesUserID uuid.UUID, t time.Time) bool {
	if r == nil {
		return false
	}
	employeeNo, ok := r.identityMap[salesUserID]
	if !ok {
		return false
	}
	return r.empSvc.IsResignedByEmployeeNo(ctx, employeeNo, t)
}

func TestHRISResignedReader_GateInCommissionFlow(t *testing.T) {
	repo := newStubEmployeeRepo()
	empSvc := NewEmployeeService(repo, nil, nil)
	ctx := context.Background()

	salesUserID := uuid.New()
	employeeNo := "EMP-SALES-1"
	_, _ = empSvc.Upsert(ctx, port.EmployeeRecord{
		EmployeeNo: employeeNo, FullName: "Sales Rep", Status: domain.EmployeeStatusActive,
	})

	reader := &resignedReaderViaService{
		empSvc:      empSvc,
		identityMap: map[uuid.UUID]string{salesUserID: employeeNo},
	}

	// Active sales — should NOT be flagged.
	if reader.IsResignedBefore(ctx, salesUserID, time.Now().UTC()) {
		t.Fatal("active sales should not be flagged as resigned")
	}

	// Resign on 2026-03-31; invoice paid 2026-04-15 means sales rep had
	// already left → should be flagged.
	if _, err := empSvc.Resign(ctx, employeeNo, time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC), ""); err != nil {
		t.Fatal(err)
	}
	if !reader.IsResignedBefore(ctx, salesUserID, time.Date(2026, 4, 15, 0, 0, 0, 0, time.UTC)) {
		t.Fatal("invoice paid after resign date should be flagged")
	}
	if reader.IsResignedBefore(ctx, salesUserID, time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)) {
		t.Fatal("invoice paid before resign date should not be flagged")
	}
}
