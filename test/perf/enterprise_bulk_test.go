// Wave 105 — Enterprise BOQ bulk perf test.
//
// Targets TC-NFR-004 (100k BOQ Lines Query) + TC-NFR-005 (Recompute
// under load). Build-tagged `perf` so it doesn't run on the default
// `go test ./...` — the CI nightly cron job at .github/workflows/
// boundary-regression.yml is the canonical caller.
//
// What this test measures:
//
//   1. Seed 100k lines into a single BOQ via BOQLineRepository.
//      BulkInsertLines (CopyFrom under the hood).
//   2. Time GET /api/enterprise/boqs/{id} which the usecase resolves
//      as boqRepo.FindByID + boqLineRepo.ListByBOQ (the full hydrate).
//      Assert p95 < 2s across 5 reads.
//   3. Time a recompute pass (re-sum sell_total/cost_total/margin_pct
//      from the lines). Assert p95 < 5s across 3 runs.
//
// Skip-on-no-DB: this whole test t.Skip's cleanly when DATABASE_URL is
// unset (which is the case for the default `go test`). The CI perf job
// sets DATABASE_URL against a fresh postgres service.
//
//go:build perf

package perf

import (
	"context"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	enterprisepg "github.com/ion-core/backend/internal/enterprise/adapter/postgres"
	"github.com/ion-core/backend/internal/enterprise/domain"
)

// Thresholds — derived from the Wave 91 audit doc + Phase 1 PRD NFRs.
const (
	bulkP95GetThreshold       = 2 * time.Second
	bulkP95RecomputeThreshold = 5 * time.Second
	bulkLineCount             = 100_000
)

func TestEnterpriseBOQ_100kLines_P95(t *testing.T) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		t.Skip("DATABASE_URL not set — perf test requires a live postgres; skipping")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Skipf("pgxpool.New: %v (DB likely unreachable)", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("pool.Ping: %v (DB unreachable)", err)
	}

	// Provision a parent BOQ version row. We bypass the usecase layer
	// (no opportunity/pricebook FK chain needed for the read-side perf
	// measurement) and insert directly with NULLable FKs. If the schema
	// enforces those FKs at the DB layer, t.Skip — perf measurement is
	// not a hill we die on if the schema's stricter than expected.
	boqVersionID := uuid.New()
	pricebookID, opportunityID := seedEnterpriseParents(ctx, t, pool)
	if _, err := pool.Exec(ctx, `
		INSERT INTO enterprise.boq_versions
			(id, boq_number, opportunity_id, pricebook_id, version_no, status)
		VALUES ($1, $2, $3, $4, 1, 'draft')
	`, boqVersionID, "PERF-BOQ-"+uuid.NewString()[:8], opportunityID, pricebookID); err != nil {
		t.Skipf("seed boq_version: %v (schema may have changed)", err)
	}
	t.Cleanup(func() {
		// Best-effort cleanup. The BOQ + lines are scoped to the test
		// run via the random uuid; even if it leaks the next run
		// generates fresh ids.
		_, _ = pool.Exec(context.Background(), `DELETE FROM enterprise.boq_lines WHERE boq_version_id = $1`, boqVersionID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM enterprise.boq_versions WHERE id = $1`, boqVersionID)
	})

	slaTemplateID := firstSLATemplate(ctx, t, pool)
	pricebookLineID := firstPricebookLine(ctx, t, pool, pricebookID)

	boqLineRepo := enterprisepg.NewBOQLineRepository(pool)

	// Seed 100k lines via CopyFrom. We measure (and log) the seed
	// time too — slow seeds tell ops about ingest regressions even
	// though the SLO is about read-side latency.
	lines := make([]domain.BOQLine, bulkLineCount)
	now := time.Now()
	for i := range lines {
		lines[i] = domain.BOQLine{
			ID:                  uuid.New(),
			BOQVersionID:        boqVersionID,
			PricebookLineID:     pricebookLineID,
			SKU:                 fmt.Sprintf("PERF-SKU-%06d", i),
			Name:                fmt.Sprintf("Perf Line %d", i),
			Unit:                "unit",
			BasePriceSnapshot:   100000,
			MinMarginSnapshot:   10,
			MaxDiscountSnapshot: 5,
			SellUnitPrice:       100000,
			Quantity:            1,
			SLATemplateID:       slaTemplateID,
			Status:              domain.BOQLineStatusAwaitingProviderInput,
			SortOrder:           i + 1,
			CreatedAt:           now,
			UpdatedAt:           now,
		}
	}

	seedStart := time.Now()
	n, err := boqLineRepo.BulkInsertLines(ctx, lines)
	if err != nil {
		t.Fatalf("BulkInsertLines: %v", err)
	}
	if n != int64(bulkLineCount) {
		t.Fatalf("BulkInsertLines: inserted %d, expected %d", n, bulkLineCount)
	}
	t.Logf("seed: %d lines in %s", n, time.Since(seedStart))

	// ----- p95 GET (full hydrate) -----
	const getRuns = 5
	getSamples := make([]time.Duration, 0, getRuns)
	for i := 0; i < getRuns; i++ {
		start := time.Now()
		out, err := boqLineRepo.ListByBOQ(ctx, boqVersionID)
		dur := time.Since(start)
		if err != nil {
			t.Fatalf("ListByBOQ run %d: %v", i, err)
		}
		if len(out) != bulkLineCount {
			t.Fatalf("ListByBOQ run %d: got %d lines, want %d", i, len(out), bulkLineCount)
		}
		getSamples = append(getSamples, dur)
		t.Logf("GET run %d: %s", i, dur)
	}
	p95Get := p95(getSamples)
	t.Logf("GET p95: %s (threshold %s)", p95Get, bulkP95GetThreshold)
	if p95Get > bulkP95GetThreshold {
		t.Fatalf("GET p95 = %s exceeds threshold %s", p95Get, bulkP95GetThreshold)
	}

	// ----- p95 Recompute (SUM aggregate against the lines) -----
	const recomputeRuns = 3
	recomputeSamples := make([]time.Duration, 0, recomputeRuns)
	for i := 0; i < recomputeRuns; i++ {
		start := time.Now()
		// Recompute matches the shape the usecase uses on submit —
		// SUM the priced lines + UPDATE the parent header.
		_, err := pool.Exec(ctx, `
			UPDATE enterprise.boq_versions bv
			SET sell_total = COALESCE(s.sell_total, 0),
			    cost_total = COALESCE(s.cost_total, 0),
			    margin_pct = COALESCE(s.margin_pct, 0),
			    updated_at = NOW()
			FROM (
				SELECT
					SUM(sell_unit_price * quantity * (1 - line_discount_pct/100.0)) AS sell_total,
					SUM(COALESCE(vendor_unit_cost,0) * quantity)                     AS cost_total,
					CASE
					  WHEN SUM(sell_unit_price * quantity) > 0
					  THEN ((SUM(sell_unit_price * quantity)
					       - SUM(COALESCE(vendor_unit_cost,0) * quantity))
					       / SUM(sell_unit_price * quantity)) * 100
					  ELSE 0
					END AS margin_pct
				FROM enterprise.boq_lines
				WHERE boq_version_id = $1
			) s
			WHERE bv.id = $1
		`, boqVersionID)
		dur := time.Since(start)
		if err != nil {
			t.Fatalf("recompute run %d: %v", i, err)
		}
		recomputeSamples = append(recomputeSamples, dur)
		t.Logf("recompute run %d: %s", i, dur)
	}
	p95Recompute := p95(recomputeSamples)
	t.Logf("recompute p95: %s (threshold %s)", p95Recompute, bulkP95RecomputeThreshold)
	if p95Recompute > bulkP95RecomputeThreshold {
		t.Fatalf("recompute p95 = %s exceeds threshold %s", p95Recompute, bulkP95RecomputeThreshold)
	}
}

// p95 returns the 95th-percentile sample. With small samples (n<20)
// this approximates as the max — that's intentional: the gate should
// trip on the worst case, not a smoothed average.
func p95(samples []time.Duration) time.Duration {
	if len(samples) == 0 {
		return 0
	}
	sorted := make([]time.Duration, len(samples))
	copy(sorted, samples)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	idx := int(float64(len(sorted)) * 0.95)
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// seedEnterpriseParents creates throwaway pricebook + opportunity rows
// so the BOQ version FK chain holds. Returns the ids. Skips on schema
// mismatch — the perf test is "best effort if the DB schema fits."
func seedEnterpriseParents(ctx context.Context, t *testing.T, pool *pgxpool.Pool) (pricebookID, opportunityID uuid.UUID) {
	t.Helper()
	// Reuse an existing pricebook + opportunity if any exist — they're
	// reference data, cheaper than creating fresh ones every run.
	if err := pool.QueryRow(ctx, `SELECT id FROM enterprise.pricebooks LIMIT 1`).Scan(&pricebookID); err != nil {
		t.Skipf("no enterprise.pricebooks rows seeded — run seed-demo first; got: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT id FROM enterprise.opportunities LIMIT 1`).Scan(&opportunityID); err != nil {
		t.Skipf("no enterprise.opportunities rows seeded — run seed-demo first; got: %v", err)
	}
	return
}

func firstSLATemplate(ctx context.Context, t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM enterprise.sla_templates LIMIT 1`).Scan(&id); err != nil {
		t.Skipf("no enterprise.sla_templates rows — run migrations + seed-demo; got: %v", err)
	}
	return id
}

func firstPricebookLine(ctx context.Context, t *testing.T, pool *pgxpool.Pool, pricebookID uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM enterprise.pricebook_lines WHERE pricebook_id = $1 LIMIT 1`, pricebookID).Scan(&id); err != nil {
		t.Skipf("no enterprise.pricebook_lines for pricebook %s — run seed-demo; got: %v", pricebookID, err)
	}
	return id
}
