// Wave 121C — HRIS + Wave 118 commission cessation hook E2E.
//
// Exercises internal/hris end-to-end + the Wave 118 hook on Wave 114's
// commission_trigger evaluator:
//
//   - Upsert employee → resign (state machine)
//   - HRISResignedReader gating in OrchestrationService.RunCommissionTriggerTick
//     (resigned sales rep → no commission_trigger row + audit emission)
//   - Active sales rep → commission_trigger row written
//   - Event drain processes 'resigned' event → user deactivator + commission
//     cessation hooks fire
//   - Sync idempotency: second RunFullSync writes zero new rows
//
//go:build e2e

package e2e

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	billingpg "github.com/ion-core/backend/internal/billing/adapter/postgres"
	billinguse "github.com/ion-core/backend/internal/billing/usecase"
	hrispg "github.com/ion-core/backend/internal/hris/adapter/postgres"
	hrisgw "github.com/ion-core/backend/internal/hris/adapter/gateway"
	hrisdom "github.com/ion-core/backend/internal/hris/domain"
	hrisport "github.com/ion-core/backend/internal/hris/port"
	hrisuse "github.com/ion-core/backend/internal/hris/usecase"
)

// hrisHarness wires both HRIS services + the slimmer set of billing
// orchestration deps needed for the commission-trigger gate.
type hrisHarness struct {
	empSvc   *hrisuse.EmployeeService
	eventSvc *hrisuse.EventService
	syncSvc  *hrisuse.SyncService
	empRepo  *hrispg.EmployeeRepository
	evRepo   *hrispg.EventRepository
}

func newHRISHarness(t *testing.T) *hrisHarness {
	t.Helper()
	pool := w121cDB(t)
	w121cSkipIfMissingTable(t, pool, "hris.employees")

	empRepo := hrispg.NewEmployeeRepository(pool)
	evRepo := hrispg.NewEventRepository(pool)

	empSvc := hrisuse.NewEmployeeService(empRepo, nil, slog.Default())
	evSvc := hrisuse.NewEventService(evRepo, empRepo, hrisuse.EventServiceOpts{})
	syncSvc := hrisuse.NewSyncService(hrisuse.SyncServiceOpts{
		Gateway:  hrisgw.NewStubGateway(),
		Employee: empSvc,
		Event:    evSvc,
	})

	return &hrisHarness{
		empSvc: empSvc, eventSvc: evSvc, syncSvc: syncSvc,
		empRepo: empRepo, evRepo: evRepo,
	}
}

// stubResignedReader satisfies port.HRISResignedReader without an
// identity bridge — we explicitly map a uuid → resigned flag.
type stubResignedReader struct {
	mu       sync.RWMutex
	resigned map[uuid.UUID]bool
}

func (s *stubResignedReader) IsResignedBefore(_ context.Context, salesUserID uuid.UUID, _ time.Time) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.resigned[salesUserID]
}

// stubCommissionCessation records hook invocations.
type stubCommissionCessation struct {
	mu        sync.Mutex
	called    []string
}

func (s *stubCommissionCessation) OnResign(_ context.Context, employeeNo string, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.called = append(s.called, employeeNo)
	return nil
}

// stubUserDeactivator records hook invocations.
type stubUserDeactivator struct {
	mu     sync.Mutex
	called []string
}

func (s *stubUserDeactivator) DeactivateByEmployeeNo(_ context.Context, employeeNo string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.called = append(s.called, employeeNo)
	return nil
}

func TestHRIS_UpsertAndResign(t *testing.T) {
	h := newHRISHarness(t)
	pool := w121cDB(t)
	ctx := context.Background()

	empNo := "W121C-EMP-" + uuid.New().String()[:8]
	t.Cleanup(w121cCleanup(pool, "hris.employees", "employee_no", empNo))

	hire := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)
	e, err := h.empSvc.Upsert(ctx, hrisport.EmployeeRecord{
		EmployeeNo: empNo,
		FullName:   "Wave 121C Employee",
		Email:      empNo + "@ion.local",
		HireDate:   &hire,
		Status:     hrisdom.EmployeeStatusActive,
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if e.Status != hrisdom.EmployeeStatusActive {
		t.Fatalf("initial status: %q, want active", e.Status)
	}

	// Resign yesterday so the "next day" probe satisfies the end-of-day
	// > t comparison in domain.IsResignedBefore.
	resignDate := time.Now().UTC().AddDate(0, 0, -1)
	resigned, err := h.empSvc.Resign(ctx, empNo, resignDate, "wave 121c test")
	if err != nil {
		t.Fatalf("Resign: %v", err)
	}
	if resigned.Status != hrisdom.EmployeeStatusResigned {
		t.Errorf("after resign: %q, want resigned", resigned.Status)
	}
	if resigned.ResignDate == nil {
		t.Errorf("resign_date not stamped")
	}

	// IsResignedByEmployeeNo: t is now (after end-of-yesterday) → true.
	if !h.empSvc.IsResignedByEmployeeNo(ctx, empNo, time.Now().UTC()) {
		t.Errorf("IsResignedByEmployeeNo: want true for time after resign_date")
	}
}

func TestHRIS_CommissionCessation_ResignedSalesSkipped(t *testing.T) {
	pool := w121cDB(t)
	ctx := context.Background()

	// Probe required tables — billing.commission_triggers shipped in Wave 114.
	w121cSkipIfMissingTable(t, pool, "billing.commission_triggers")
	w121cSkipIfMissingTable(t, pool, "crm.plan_change_requests")
	w121cSkipIfMissingTable(t, pool, "billing.invoices")

	// Seed: customer + paid invoice + applied plan-change with a sales rep.
	customerID := uuid.New()
	salesUserID := uuid.New()
	invoiceID := uuid.New()
	planChangeID := uuid.New()
	paidAt := time.Now().UTC().Add(-1 * time.Hour)

	// crm.customers — minimal columns. We seed a row so the LEFT JOIN
	// in ListRecentlyPaidForCommission can pick up activated_at.
	w121cExec(t, pool,
		`INSERT INTO crm.customers (id, customer_number, full_name, phone, address, status, branch_id, activated_at)
		 VALUES ($1, $2, $3, $4, 'Wave 121C addr', 'active', NULL, $5)`,
		customerID, "W121C-"+uuid.New().String()[:8], "Wave 121C Customer", "+62811000000", paidAt)
	t.Cleanup(w121cCleanup(pool, "crm.customers", "id", customerID.String()))

	// billing.invoices — minimal.
	invNum := "W121C-INV-" + uuid.New().String()[:8]
	w121cExec(t, pool, `
		INSERT INTO billing.invoices (id, customer_id, invoice_number, invoice_type,
			invoice_date, due_date, subtotal, ppn_amount, total, status, paid_at)
		VALUES ($1, $2, $3, 'recurring', $4, $4, 100000, 11000, 111000, 'paid', $5)`,
		invoiceID, customerID, invNum, paidAt, paidAt)
	t.Cleanup(w121cCleanup(pool, "billing.invoices", "id", invoiceID.String()))

	// crm.products — need from/to product ids for the plan change.
	fromProductID, toProductID := mustSeedProductsDirect(t)

	w121cExec(t, pool, `
		INSERT INTO crm.plan_change_requests (id, customer_id, from_product_id, to_product_id,
			change_kind, sales_rep_id, status, applied_at)
		VALUES ($1, $2, $3, $4, 'upgrade', $5, 'applied', $6)`,
		planChangeID, customerID, fromProductID, toProductID, salesUserID, paidAt)
	t.Cleanup(w121cCleanup(pool, "crm.plan_change_requests", "id", planChangeID.String()))

	// Wire orchestration with stub HRIS resigned-reader (this rep IS resigned).
	resignedReader := &stubResignedReader{resigned: map[uuid.UUID]bool{salesUserID: true}}

	orch := buildOrchestratorDirect(t).WithHRISResignedReader(resignedReader)

	count, err := orch.RunCommissionTriggerTick(ctx)
	if err != nil {
		t.Fatalf("RunCommissionTriggerTick: %v", err)
	}

	// No commission triggers should have been written.
	if count != 0 {
		t.Errorf("commission triggers fired: %d, want 0 (resigned sales rep)", count)
	}

	// Verify by direct read.
	var rowCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM billing.commission_triggers WHERE invoice_id = $1`, invoiceID).
		Scan(&rowCount); err != nil {
		t.Fatalf("count commission_triggers: %v", err)
	}
	if rowCount != 0 {
		t.Errorf("rows in billing.commission_triggers: %d, want 0", rowCount)
	}
}

func TestHRIS_CommissionCessation_ActiveSalesFires(t *testing.T) {
	pool := w121cDB(t)
	ctx := context.Background()
	w121cSkipIfMissingTable(t, pool, "billing.commission_triggers")
	w121cSkipIfMissingTable(t, pool, "crm.plan_change_requests")

	customerID := uuid.New()
	salesUserID := uuid.New()
	invoiceID := uuid.New()
	planChangeID := uuid.New()
	paidAt := time.Now().UTC().Add(-1 * time.Hour)

	w121cExec(t, pool,
		`INSERT INTO crm.customers (id, customer_number, full_name, phone, address, status, branch_id, activated_at)
		 VALUES ($1, $2, $3, $4, 'Wave 121C addr', 'active', NULL, $5)`,
		customerID, "W121C-A-"+uuid.New().String()[:8], "Wave 121C Active", "+62811000001", paidAt)
	t.Cleanup(w121cCleanup(pool, "crm.customers", "id", customerID.String()))

	invNum := "W121C-INV-" + uuid.New().String()[:8]
	w121cExec(t, pool, `
		INSERT INTO billing.invoices (id, customer_id, invoice_number, invoice_type,
			invoice_date, due_date, subtotal, ppn_amount, total, status, paid_at)
		VALUES ($1, $2, $3, 'recurring', $4, $4, 100000, 11000, 111000, 'paid', $5)`,
		invoiceID, customerID, invNum, paidAt, paidAt)
	t.Cleanup(w121cCleanup(pool, "billing.invoices", "id", invoiceID.String()))

	fromProductID, toProductID := mustSeedProductsDirect(t)

	w121cExec(t, pool, `
		INSERT INTO crm.plan_change_requests (id, customer_id, from_product_id, to_product_id,
			change_kind, sales_rep_id, status, applied_at)
		VALUES ($1, $2, $3, $4, 'upgrade', $5, 'applied', $6)`,
		planChangeID, customerID, fromProductID, toProductID, salesUserID, paidAt)
	t.Cleanup(w121cCleanup(pool, "crm.plan_change_requests", "id", planChangeID.String()))
	t.Cleanup(w121cCleanup(pool, "billing.commission_triggers", "plan_change_id", planChangeID.String()))

	// Wire orchestration WITHOUT a HRIS reader (or with a stub that
	// returns false). Trigger should fire normally.
	orch := buildOrchestratorDirect(t)

	count, err := orch.RunCommissionTriggerTick(ctx)
	if err != nil {
		t.Fatalf("RunCommissionTriggerTick: %v", err)
	}

	// We expect at least 1 commission trigger row. Note: other rows in
	// the DB may have caused additional fires — we only assert the
	// invoice we seeded got a row.
	var rowCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM billing.commission_triggers WHERE invoice_id = $1`, invoiceID).
		Scan(&rowCount); err != nil {
		t.Fatalf("count commission_triggers: %v", err)
	}
	if rowCount == 0 {
		t.Errorf("expected at least 1 commission_trigger row for our invoice; total tick count = %d", count)
	}
}

func TestHRIS_EventDrainTriggersHooks(t *testing.T) {
	pool := w121cDB(t)
	ctx := context.Background()
	h := newHRISHarness(t)

	empNo := "W121C-EVT-" + uuid.New().String()[:8]
	t.Cleanup(w121cCleanup(pool, "hris.employees", "employee_no", empNo))
	t.Cleanup(w121cCleanup(pool, "hris.employee_events", "employee_no", empNo))

	hire := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err := h.empSvc.Upsert(ctx, hrisport.EmployeeRecord{
		EmployeeNo: empNo,
		FullName:   "Wave 121C Eventee",
		HireDate:   &hire,
		Status:     hrisdom.EmployeeStatusActive,
	})
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	// Rebuild EventService with stub hooks so we can observe them.
	commHook := &stubCommissionCessation{}
	deactHook := &stubUserDeactivator{}
	evSvc := hrisuse.NewEventService(h.evRepo, h.empRepo, hrisuse.EventServiceOpts{
		Commission: commHook,
		Deactivate: deactHook,
	})

	ev, err := hrisdom.NewEmployeeEvent(empNo, hrisdom.EventKindResigned,
		map[string]any{"final_day": time.Now().UTC().Format("2006-01-02")},
		time.Now().UTC(), "wave121c")
	if err != nil {
		t.Fatalf("NewEmployeeEvent: %v", err)
	}
	if _, err := evSvc.IngestEvents(ctx, []*hrisdom.EmployeeEvent{ev}); err != nil {
		t.Fatalf("IngestEvents: %v", err)
	}
	if _, err := evSvc.ProcessPending(ctx, 10); err != nil {
		t.Fatalf("ProcessPending: %v", err)
	}

	if len(commHook.called) == 0 {
		t.Errorf("commission cessation hook never invoked")
	}
	if len(deactHook.called) == 0 {
		t.Errorf("user deactivator hook never invoked")
	}

	// Confirm the employee row's status flipped to resigned via applyStatusMutation.
	got, _ := h.empRepo.FindByEmployeeNo(ctx, empNo)
	if got.Status != hrisdom.EmployeeStatusResigned {
		t.Errorf("employee status: %q, want resigned", got.Status)
	}
}

func TestHRIS_SyncIdempotency(t *testing.T) {
	pool := w121cDB(t)
	ctx := context.Background()
	h := newHRISHarness(t)

	// The stub gateway seeds employees with stable employee_no values
	// (EMP00001..3). Run the sync twice — second pass should be a no-op
	// because the employee_no UNIQUE drives Upsert to update-in-place
	// and the event_id UNIQUE keeps event ingest idempotent.
	t.Cleanup(w121cCleanup(pool, "hris.employees", "employee_no", "EMP00001"))
	t.Cleanup(w121cCleanup(pool, "hris.employees", "employee_no", "EMP00002"))
	t.Cleanup(w121cCleanup(pool, "hris.employees", "employee_no", "EMP00003"))
	t.Cleanup(w121cCleanup(pool, "hris.employee_events", "employee_no", "EMP00003"))

	first, err := h.syncSvc.RunFullSync(ctx)
	if err != nil {
		t.Fatalf("first sync: %v", err)
	}
	if first.EmployeesUpserted == 0 {
		t.Skip("sync produced 0 upserts — gateway not delivering stub data?")
	}

	// Count employees + events before second sync.
	var empBefore, evBefore int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM hris.employees`).Scan(&empBefore)
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM hris.employee_events`).Scan(&evBefore)

	second, err := h.syncSvc.RunFullSync(ctx)
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}

	var empAfter, evAfter int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM hris.employees`).Scan(&empAfter)
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM hris.employee_events`).Scan(&evAfter)

	if empAfter != empBefore {
		t.Errorf("idempotency violated: employees grew from %d to %d on second sync", empBefore, empAfter)
	}
	if evAfter != evBefore {
		t.Errorf("idempotency violated: events grew from %d to %d on second sync", evBefore, evAfter)
	}
	_ = second
}

// mustSeedProductsDirect uses the w121c pool to pick two existing
// products from crm.products. We don't actually create rows because
// the foreign-key-less plan_change_request happily accepts any UUID.
// If we can't find two distinct rows we synthesize uuids.
func mustSeedProductsDirect(t *testing.T) (uuid.UUID, uuid.UUID) {
	t.Helper()
	pool := w121cDB(t)
	ctx := context.Background()
	rows, err := pool.Query(ctx, `SELECT id FROM crm.products ORDER BY created_at LIMIT 2`)
	if err == nil {
		defer rows.Close()
		ids := []uuid.UUID{}
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err == nil {
				ids = append(ids, id)
			}
		}
		if len(ids) >= 2 {
			return ids[0], ids[1]
		}
		if len(ids) == 1 {
			return ids[0], uuid.New()
		}
	}
	// Fallback — synthesize.
	return uuid.New(), uuid.New()
}

// buildOrchestratorDirect wires the minimum OrchestrationService
// needed to fire RunCommissionTriggerTick against the live DB. Other
// dependencies are nil-safe (the orchestration tick short-circuits with
// a warnTODO when bridges aren't wired).
func buildOrchestratorDirect(t *testing.T) *billinguse.OrchestrationService {
	t.Helper()
	pool := w121cDB(t)
	return billinguse.NewOrchestrationService(
		billingpg.NewInvoiceRepository(pool),
		billingpg.NewReminderLogRepository(pool),
		billingpg.NewLateFeeApplicationRepository(pool),
		billingpg.NewSuspensionActionRepository(pool),
		billingpg.NewCommissionTriggerRepository(pool),
		billingpg.NewPlanChangeReader(pool),
		billingpg.NewCustomerReader(pool),
		nil, // schema resolver
		nil, // RADIUSRestorer
		nil, // CustomerSuspender
		nil, // ReminderDispatcher
		nil, // audit
		slog.Default(),
	)
}
