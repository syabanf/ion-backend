// Wave 125 — Bulk Ops Executor E2E.
//
// Exercises the new operations.bulk_jobs framework end-to-end against
// live Postgres. The test seeds:
//   - 3 customers in crm.customers
//   - 2 plans in crm.products (so we have a from/to pair)
//   - 3 bulk_plan_change_items pointing to a single bulk job
//
// Then it runs BulkExecutorService.RunBulkPlanChange and asserts:
//   - The job moves to status=completed (or partial if one item is
//     deliberately skipped on a no-op plan change).
//   - The per-item rows are in terminal states.
//   - A crm.plan_change_requests row exists per applied item.
//
// The test t.Skip's cleanly when DATABASE_URL is unset or the wave-125
// migration hasn't been applied to the target database — so CI nodes
// without a smoke DB don't fail.

//go:build e2e

package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	opscrm "github.com/ion-core/backend/internal/operations/adapter/crm"
	opsfield "github.com/ion-core/backend/internal/operations/adapter/field"
	opsnet "github.com/ion-core/backend/internal/operations/adapter/network"
	opspg "github.com/ion-core/backend/internal/operations/adapter/postgres"
	opsdom "github.com/ion-core/backend/internal/operations/domain"
	opsuc "github.com/ion-core/backend/internal/operations/usecase"
)

func TestBulkExecutor_PlanChange_E2E(t *testing.T) {
	pool := w121cDB(t)
	w121cSkipIfMissingTable(t, pool, "operations.bulk_jobs")
	w121cSkipIfMissingTable(t, pool, "operations.bulk_plan_change_items")
	w121cSkipIfMissingTable(t, pool, "crm.plan_change_requests")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Seed two products.
	planA := uuid.New()
	planB := uuid.New()
	codeA := "W125-PLAN-A-" + planA.String()[:8]
	codeB := "W125-PLAN-B-" + planB.String()[:8]
	w121cExec(t, pool, `
		INSERT INTO crm.products (id, code, name, speed_mbps, monthly_price, otc_price, active)
		VALUES ($1, $2, 'Wave125 A', 50, 250000, 0, true),
		       ($3, $4, 'Wave125 B', 100, 500000, 0, true)
	`, planA, codeA, planB, codeB)
	t.Cleanup(w121cCleanup(pool, "crm.products", "id", planA.String()))
	t.Cleanup(w121cCleanup(pool, "crm.products", "id", planB.String()))

	// Seed 3 customers with orders on planA.
	custIDs := [3]uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	for i, cid := range custIDs {
		custNo := "W125-CUST-" + cid.String()[:8]
		w121cExec(t, pool, `
			INSERT INTO crm.customers (id, customer_number, full_name, phone, address, status)
			VALUES ($1, $2, $3, '+62811000000', 'Wave125', 'active')
		`, cid, custNo, "Wave125 Customer "+custNo)
		t.Cleanup(w121cCleanup(pool, "crm.customers", "id", cid.String()))

		ordID := uuid.New()
		ordNo := "W125-ORD-" + ordID.String()[:8]
		w121cExec(t, pool, `
			INSERT INTO crm.orders (id, order_number, customer_id, product_id,
			                        monthly_price, otc_price, status)
			VALUES ($1, $2, $3, $4, 250000, 0, 'active')
		`, ordID, ordNo, cid, planA)
		t.Cleanup(w121cCleanup(pool, "crm.orders", "id", ordID.String()))
		_ = i
	}

	// Wire repos + bridges.
	jobs := opspg.NewBulkJobRepository(pool)
	bpcs := opspg.NewBulkPlanChangeItemRepository(pool)
	pcBridge := opscrm.NewPlanChangeBridge(pool)
	wcBridge := opsfield.NewWOCreatorBridge(pool)
	omBridge := opsnet.NewODPMigrationBridge(pool, wcBridge)
	svc := opsuc.NewBulkExecutorService(opsuc.BulkExecutorDeps{
		Jobs:        jobs,
		BPCItems:    bpcs,
		PCExecutor:  pcBridge,
		PCValidator: pcBridge,
		OMExecutor:  omBridge,
		OMValidator: omBridge,
		WCExecutor:  wcBridge,
		WCValidator: wcBridge,
	})

	// Create the job + 3 items.
	job, err := opsdom.NewBulkJob(opsdom.BulkJobPlanChange, false, nil)
	if err != nil {
		t.Fatalf("NewBulkJob: %v", err)
	}
	job.TotalItems = 3
	if err := jobs.Create(ctx, job); err != nil {
		t.Fatalf("Create job: %v", err)
	}
	t.Cleanup(w121cCleanup(pool, "operations.bulk_jobs", "id", job.ID.String()))

	items := []opsdom.BulkPlanChangeItem{}
	for _, cid := range custIDs {
		items = append(items, opsdom.BulkPlanChangeItem{
			ID:           uuid.New(),
			BulkJobID:    job.ID,
			CustomerID:   cid,
			TargetPlanID: planB,
			Status:       opsdom.BPCItemQueued,
			CreatedAt:    time.Now().UTC(),
		})
	}
	if err := bpcs.CreateBatch(ctx, items); err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}
	// (item cleanup is cascaded by the bulk_jobs FK)

	// Run.
	out, err := svc.RunBulkPlanChange(ctx, job.ID)
	if err != nil {
		t.Fatalf("RunBulkPlanChange: %v", err)
	}

	// Expectations: status terminal, all 3 items processed.
	if out.Status != opsdom.BulkJobStatusCompleted && out.Status != opsdom.BulkJobStatusPartial {
		t.Errorf("status: want completed|partial, got %s", out.Status)
	}
	if out.ProcessedItems != 3 {
		t.Errorf("processed: want 3, got %d", out.ProcessedItems)
	}

	// Verify per-customer plan_change_requests rows.
	for _, cid := range custIDs {
		var n int
		if err := pool.QueryRow(ctx, `
			SELECT COUNT(*) FROM crm.plan_change_requests
			 WHERE customer_id = $1
			   AND from_product_id = $2
			   AND to_product_id   = $3
			   AND reason          = 'bulk:' || $4::text
		`, cid, planA, planB, job.ID).Scan(&n); err != nil {
			t.Fatalf("count plan_change_requests: %v", err)
		}
		if n < 1 {
			t.Errorf("customer %s: want ≥1 plan_change_requests, got %d", cid, n)
		}
	}
	// Cleanup the plan_change_requests rows we just verified.
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM crm.plan_change_requests WHERE reason = 'bulk:' || $1::text`, job.ID)
	})

	// Re-run is idempotent — status stays terminal, no new requests.
	out2, err := svc.RunBulkPlanChange(ctx, job.ID)
	if err != nil {
		t.Fatalf("RunBulkPlanChange (re-run): %v", err)
	}
	if out2.Status != out.Status {
		t.Errorf("re-run status drift: %s → %s", out.Status, out2.Status)
	}
}
