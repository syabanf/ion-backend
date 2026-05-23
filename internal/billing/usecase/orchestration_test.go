// Wave 114 — Orchestration usecase tests.
//
// Stub repos + bridges exercise each evaluator's happy path, idempotent
// re-run, and no-schema fallback. Schema resolution is bypassed (nil
// resolver) so the default policies (DefaultReminderPolicy / etc.) drive
// each tick.

package usecase

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/billing/domain"
	"github.com/ion-core/backend/internal/billing/port"
	"github.com/ion-core/backend/pkg/audit"
)

// =====================================================================
// Stubs
// =====================================================================

type stubInvoiceRepo struct {
	views []port.InvoiceView
}

func (s *stubInvoiceRepo) Create(context.Context, *domain.Invoice, []domain.LineItem) error {
	return nil
}
func (s *stubInvoiceRepo) UpdateStatus(context.Context, uuid.UUID, domain.InvoiceStatus, *time.Time) error {
	return nil
}
func (s *stubInvoiceRepo) FindByID(context.Context, uuid.UUID) (*port.InvoiceView, error) {
	if len(s.views) == 0 {
		return nil, nil
	}
	v := s.views[0]
	return &v, nil
}
func (s *stubInvoiceRepo) FindOTCForOrder(context.Context, uuid.UUID) (*port.InvoiceView, error) {
	return nil, nil
}
func (s *stubInvoiceRepo) List(context.Context, port.InvoiceListFilter) ([]port.InvoiceView, int, error) {
	return s.views, len(s.views), nil
}

type stubReminderRepo struct {
	mu      sync.Mutex
	rows    []port.ReminderLogRow
	lastBy  map[uuid.UUID]port.ReminderLogRow
}

func newStubReminderRepo() *stubReminderRepo {
	return &stubReminderRepo{lastBy: make(map[uuid.UUID]port.ReminderLogRow)}
}

func (s *stubReminderRepo) Create(_ context.Context, row *port.ReminderLogRow) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Simulate UNIQUE (invoice_id, kind) by checking last.
	if existing, ok := s.lastBy[row.InvoiceID]; ok && existing.Kind == row.Kind {
		// ON CONFLICT no-op
		return nil
	}
	s.rows = append(s.rows, *row)
	s.lastBy[row.InvoiceID] = *row
	return nil
}

func (s *stubReminderRepo) FindLastByInvoice(_ context.Context, id uuid.UUID) (*port.ReminderLogRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if row, ok := s.lastBy[id]; ok {
		r := row
		return &r, nil
	}
	return nil, nil
}

func (s *stubReminderRepo) ListPending(context.Context, port.ReminderLogFilter) ([]port.ReminderLogRow, error) {
	return nil, nil
}

type stubLateFeeRepo struct {
	mu      sync.Mutex
	rows    map[uuid.UUID]port.LateFeeApplicationRow
}

func newStubLateFeeRepo() *stubLateFeeRepo {
	return &stubLateFeeRepo{rows: make(map[uuid.UUID]port.LateFeeApplicationRow)}
}

func (s *stubLateFeeRepo) Create(_ context.Context, row *port.LateFeeApplicationRow) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.rows[row.InvoiceID]; exists {
		return false, nil
	}
	s.rows[row.InvoiceID] = *row
	return true, nil
}

func (s *stubLateFeeRepo) FindByInvoice(_ context.Context, id uuid.UUID) (*port.LateFeeApplicationRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.rows[id]; ok {
		return &r, nil
	}
	return nil, nil
}

func (s *stubLateFeeRepo) Undo(context.Context, uuid.UUID, string) error { return nil }

type stubSuspensionRepo struct {
	mu      sync.Mutex
	rows    []port.SuspensionActionRow
	lastBy  map[uuid.UUID]port.SuspensionActionRow
}

func newStubSuspensionRepo() *stubSuspensionRepo {
	return &stubSuspensionRepo{lastBy: make(map[uuid.UUID]port.SuspensionActionRow)}
}

func (s *stubSuspensionRepo) Create(_ context.Context, row *port.SuspensionActionRow) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows = append(s.rows, *row)
	s.lastBy[row.CustomerID] = *row
	return nil
}

func (s *stubSuspensionRepo) FindLastByCustomer(_ context.Context, id uuid.UUID) (*port.SuspensionActionRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.lastBy[id]; ok {
		x := r
		return &x, nil
	}
	return nil, nil
}

func (s *stubSuspensionRepo) ListByActionInWindow(context.Context, port.SuspensionActionFilter) ([]port.SuspensionActionRow, error) {
	return nil, nil
}

type stubCommissionTriggerRepo struct {
	mu      sync.Mutex
	rows    []port.CommissionTriggerRow
	dedupe  map[string]struct{} // key = planChangeID|kind
}

func newStubCommissionTriggerRepo() *stubCommissionTriggerRepo {
	return &stubCommissionTriggerRepo{dedupe: make(map[string]struct{})}
}

func (s *stubCommissionTriggerRepo) Create(_ context.Context, row *port.CommissionTriggerRow) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var key string
	if row.PlanChangeID != nil {
		key = row.PlanChangeID.String() + "|" + string(row.TriggerKind)
		if _, exists := s.dedupe[key]; exists {
			return false, nil
		}
		s.dedupe[key] = struct{}{}
	}
	s.rows = append(s.rows, *row)
	return true, nil
}

func (s *stubCommissionTriggerRepo) ListByPlanChange(context.Context, uuid.UUID) ([]port.CommissionTriggerRow, error) {
	return nil, nil
}
func (s *stubCommissionTriggerRepo) ListByCustomer(context.Context, uuid.UUID) ([]port.CommissionTriggerRow, error) {
	return nil, nil
}

type stubCustomerReader struct {
	suspensionList []port.SuspensionCandidate
	restoreList    []port.RestoreCandidate
	reminderHit    map[uuid.UUID]port.ReminderTarget
}

func (s *stubCustomerReader) ReadForReminder(_ context.Context, id uuid.UUID) (*port.ReminderTarget, error) {
	if t, ok := s.reminderHit[id]; ok {
		x := t
		return &x, nil
	}
	return &port.ReminderTarget{CustomerID: id, CustomerName: "Test"}, nil
}
func (s *stubCustomerReader) ListSuspensionCandidates(context.Context, int) ([]port.SuspensionCandidate, error) {
	return s.suspensionList, nil
}
func (s *stubCustomerReader) ListRestoreCandidates(context.Context, int) ([]port.RestoreCandidate, error) {
	return s.restoreList, nil
}

type stubPlanChangeReader struct {
	rows []port.PlanChangePaidInvoice
}

func (s *stubPlanChangeReader) ListRecentlyPaidForCommission(context.Context, time.Time, int) ([]port.PlanChangePaidInvoice, error) {
	return s.rows, nil
}

type stubDispatcher struct {
	mu    sync.Mutex
	calls []struct {
		Kind    domain.ReminderKind
		Channel string
	}
	failNext bool
}

func (s *stubDispatcher) SendReminder(_ context.Context, _ port.ReminderTarget, _ port.ReminderInvoiceSnapshot, kind domain.ReminderKind, channel string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failNext {
		s.failNext = false
		return "", errors.New("dispatcher boom")
	}
	s.calls = append(s.calls, struct {
		Kind    domain.ReminderKind
		Channel string
	}{kind, channel})
	return "msg-" + uuid.NewString(), nil
}

type stubRadius struct {
	calls []uuid.UUID
	fail  bool
}

func (s *stubRadius) RestoreCustomer(_ context.Context, id uuid.UUID) error {
	if s.fail {
		return errors.New("radius boom")
	}
	s.calls = append(s.calls, id)
	return nil
}

type stubSuspender struct {
	calls []struct {
		CustomerID uuid.UUID
		State      domain.CustomerSuspensionState
	}
}

func (s *stubSuspender) SetSuspensionState(_ context.Context, id uuid.UUID, state domain.CustomerSuspensionState) error {
	s.calls = append(s.calls, struct {
		CustomerID uuid.UUID
		State      domain.CustomerSuspensionState
	}{id, state})
	return nil
}

// =====================================================================
// Helpers
// =====================================================================

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(discardWriter{}, &slog.HandlerOptions{Level: slog.LevelError}))
}

type discardWriter struct{}

func (discardWriter) Write(b []byte) (int, error) { return len(b), nil }

func mkInvoice(due time.Time, status domain.InvoiceStatus, total float64) port.InvoiceView {
	id := uuid.New()
	return port.InvoiceView{
		Invoice: domain.Invoice{
			ID:            id,
			InvoiceNumber: "INV-" + id.String()[:8],
			CustomerID:    uuid.New(),
			DueDate:       due,
			Total:         total,
			Status:        status,
		},
		OutstandingAmount: total,
	}
}

// =====================================================================
// RunReminderTick
// =====================================================================

func TestRunReminderTick_HappyPath(t *testing.T) {
	now := time.Now().UTC()
	due := now.AddDate(0, 0, -8) // 8 days overdue → overdue_d7 fires (downtime catch-up)
	inv := mkInvoice(due, domain.InvoiceStatusIssued, 250000)
	invRepo := &stubInvoiceRepo{views: []port.InvoiceView{inv}}
	logRepo := newStubReminderRepo()
	disp := &stubDispatcher{}

	svc := NewOrchestrationService(
		invRepo, logRepo, nil, nil, nil, nil,
		&stubCustomerReader{reminderHit: map[uuid.UUID]port.ReminderTarget{}},
		nil, // schema resolver — fall through to defaults
		nil, nil, disp, audit.Nop{}, quietLogger(),
	)

	n, err := svc.RunReminderTick(context.Background())
	if err != nil {
		t.Fatalf("RunReminderTick: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 reminder; got %d", n)
	}
	if len(disp.calls) != 1 {
		t.Fatalf("dispatcher called %d times; want 1", len(disp.calls))
	}
	// Idempotency: a second run sees the lastBy map populated and does
	// not fire again because LastSent now equals overdue_d7 (or
	// pre_suspend would only fire near the suspension cutoff).
	n2, err := svc.RunReminderTick(context.Background())
	if err != nil {
		t.Fatalf("RunReminderTick re-run: %v", err)
	}
	if n2 != 0 {
		t.Fatalf("expected idempotent 0 on second run; got %d", n2)
	}
}

func TestRunReminderTick_NoDispatcher_NoOp(t *testing.T) {
	now := time.Now().UTC()
	inv := mkInvoice(now.AddDate(0, 0, -8), domain.InvoiceStatusIssued, 250000)
	svc := NewOrchestrationService(
		&stubInvoiceRepo{views: []port.InvoiceView{inv}},
		newStubReminderRepo(),
		nil, nil, nil, nil,
		&stubCustomerReader{},
		nil, nil, nil,
		nil, // <-- dispatcher missing
		audit.Nop{}, quietLogger(),
	)
	n, err := svc.RunReminderTick(context.Background())
	if err != nil {
		t.Fatalf("RunReminderTick: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 sends without dispatcher; got %d", n)
	}
}

// =====================================================================
// RunLateFeeTick
// =====================================================================

func TestRunLateFeeTick_AppliesOnce(t *testing.T) {
	now := time.Now().UTC()
	due := now.AddDate(0, 0, -10) // 10 days past due, default grace=3 → eligible
	inv := mkInvoice(due, domain.InvoiceStatusIssued, 200000)
	invRepo := &stubInvoiceRepo{views: []port.InvoiceView{inv}}
	lateRepo := newStubLateFeeRepo()
	svc := NewOrchestrationService(
		invRepo, nil, lateRepo, nil, nil, nil, nil, nil, nil, nil, nil,
		audit.Nop{}, quietLogger(),
	)
	n, err := svc.RunLateFeeTick(context.Background())
	if err != nil {
		t.Fatalf("RunLateFeeTick: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 application; got %d", n)
	}
	// Re-run is idempotent.
	n2, _ := svc.RunLateFeeTick(context.Background())
	if n2 != 0 {
		t.Fatalf("expected 0 on re-run; got %d", n2)
	}
}

func TestRunLateFeeTick_NoLateFeeRepo_NoOp(t *testing.T) {
	svc := NewOrchestrationService(
		&stubInvoiceRepo{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		audit.Nop{}, quietLogger(),
	)
	n, err := svc.RunLateFeeTick(context.Background())
	if err != nil {
		t.Fatalf("RunLateFeeTick: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0; got %d", n)
	}
}

// =====================================================================
// RunSuspensionTick
// =====================================================================

func TestRunSuspensionTick_EscalatesWarn(t *testing.T) {
	now := time.Now().UTC()
	custID := uuid.New()
	candidates := []port.SuspensionCandidate{
		{
			CustomerID:       custID,
			CurrentState:     domain.CustomerSuspensionStateActive,
			OldestOverdueDue: now.AddDate(0, 0, -10), // past warn cutoff (7)
		},
	}
	suspender := &stubSuspender{}
	logRepo := newStubSuspensionRepo()
	svc := NewOrchestrationService(
		&stubInvoiceRepo{}, nil, nil, logRepo, nil, nil,
		&stubCustomerReader{suspensionList: candidates}, nil,
		nil, suspender, nil, audit.Nop{}, quietLogger(),
	)
	n, err := svc.RunSuspensionTick(context.Background())
	if err != nil {
		t.Fatalf("RunSuspensionTick: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 action; got %d", n)
	}
	if len(logRepo.rows) != 1 || logRepo.rows[0].Action != domain.SuspensionActionWarn {
		t.Fatalf("expected warn action; got %#v", logRepo.rows)
	}
	// Warn does NOT call the suspender (it's a dispatch-only stage).
	if len(suspender.calls) != 0 {
		t.Fatalf("warn must not call suspender; got %d calls", len(suspender.calls))
	}
}

func TestRunSuspensionTick_NoCandidateReader_NoOp(t *testing.T) {
	svc := NewOrchestrationService(
		&stubInvoiceRepo{}, nil, nil, newStubSuspensionRepo(), nil, nil,
		nil, // no reader
		nil, nil, nil, nil, audit.Nop{}, quietLogger(),
	)
	n, err := svc.RunSuspensionTick(context.Background())
	if err != nil {
		t.Fatalf("RunSuspensionTick: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0; got %d", n)
	}
}

// =====================================================================
// RunRestoreTick
// =====================================================================

func TestRunRestoreTick_HappyPath(t *testing.T) {
	custID := uuid.New()
	radius := &stubRadius{}
	suspender := &stubSuspender{}
	logRepo := newStubSuspensionRepo()
	svc := NewOrchestrationService(
		&stubInvoiceRepo{}, nil, nil, logRepo, nil, nil,
		&stubCustomerReader{
			restoreList: []port.RestoreCandidate{
				{CustomerID: custID, CurrentState: domain.CustomerSuspensionStateSoftSuspend},
			},
		},
		nil, radius, suspender, nil, audit.Nop{}, quietLogger(),
	)
	n, err := svc.RunRestoreTick(context.Background())
	if err != nil {
		t.Fatalf("RunRestoreTick: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 restore; got %d", n)
	}
	if len(radius.calls) != 1 || radius.calls[0] != custID {
		t.Fatalf("expected radius restore for %s; got %#v", custID, radius.calls)
	}
	if len(suspender.calls) != 1 || suspender.calls[0].State != domain.CustomerSuspensionStateActive {
		t.Fatalf("expected suspender to flip to active; got %#v", suspender.calls)
	}
	if len(logRepo.rows) != 1 || logRepo.rows[0].Action != domain.SuspensionActionRestore {
		t.Fatalf("expected restore log row; got %#v", logRepo.rows)
	}
}

func TestRunRestoreTick_RadiusFailure_HaltsCustomer(t *testing.T) {
	custID := uuid.New()
	radius := &stubRadius{fail: true}
	suspender := &stubSuspender{}
	logRepo := newStubSuspensionRepo()
	svc := NewOrchestrationService(
		&stubInvoiceRepo{}, nil, nil, logRepo, nil, nil,
		&stubCustomerReader{
			restoreList: []port.RestoreCandidate{
				{CustomerID: custID, CurrentState: domain.CustomerSuspensionStateSoftSuspend},
			},
		},
		nil, radius, suspender, nil, audit.Nop{}, quietLogger(),
	)
	n, _ := svc.RunRestoreTick(context.Background())
	if n != 0 {
		t.Fatalf("radius failure must skip the restore; got n=%d", n)
	}
	if len(suspender.calls) != 0 {
		t.Fatalf("suspender must not flip when radius fails; got %#v", suspender.calls)
	}
	if len(logRepo.rows) != 0 {
		t.Fatalf("no log row should be written; got %#v", logRepo.rows)
	}
}

func TestRunRestoreTick_NoReader_NoOp(t *testing.T) {
	svc := NewOrchestrationService(
		&stubInvoiceRepo{}, nil, nil, newStubSuspensionRepo(), nil, nil,
		nil, nil, nil, nil, nil, audit.Nop{}, quietLogger(),
	)
	n, err := svc.RunRestoreTick(context.Background())
	if err != nil {
		t.Fatalf("RunRestoreTick: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0; got %d", n)
	}
}

// =====================================================================
// RunCommissionTriggerTick
// =====================================================================

func TestRunCommissionTriggerTick_HappyPath(t *testing.T) {
	now := time.Now().UTC()
	planChange := uuid.New()
	row := port.PlanChangePaidInvoice{
		InvoiceID:    uuid.New(),
		CustomerID:   uuid.New(),
		PlanChangeID: planChange,
		SalesUserID:  uuid.New(),
		AmountBasis:  500000,
		PaidAt:       now.Add(-1 * time.Hour),
	}
	repo := newStubCommissionTriggerRepo()
	svc := NewOrchestrationService(
		&stubInvoiceRepo{}, nil, nil, nil, repo,
		&stubPlanChangeReader{rows: []port.PlanChangePaidInvoice{row}},
		nil, nil, nil, nil, nil, audit.Nop{}, quietLogger(),
	)
	n, err := svc.RunCommissionTriggerTick(context.Background())
	if err != nil {
		t.Fatalf("RunCommissionTriggerTick: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 queued; got %d", n)
	}
	if got := repo.rows[0].CommissionAmount; got == nil || *got != 75000 {
		// Default policy: 15% of 500000 = 75000.
		t.Fatalf("expected commission 75000; got %v", got)
	}
	// Re-run dedupes.
	n2, _ := svc.RunCommissionTriggerTick(context.Background())
	if n2 != 0 {
		t.Fatalf("expected 0 on re-run; got %d", n2)
	}
}

func TestRunCommissionTriggerTick_NoReader_NoOp(t *testing.T) {
	repo := newStubCommissionTriggerRepo()
	svc := NewOrchestrationService(
		&stubInvoiceRepo{}, nil, nil, nil, repo, nil, nil, nil, nil, nil, nil,
		audit.Nop{}, quietLogger(),
	)
	n, err := svc.RunCommissionTriggerTick(context.Background())
	if err != nil {
		t.Fatalf("RunCommissionTriggerTick: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0; got %d", n)
	}
}
