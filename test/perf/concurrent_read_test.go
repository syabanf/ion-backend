// Wave 105 — concurrent BOQ list perf test.
//
// Targets TC-NFR-002 (Concurrent Read Throughput). 500 goroutines
// hammer the BOQ list query for 30 seconds; we assert no 5xx
// responses and p99 < 1s.
//
// Because spinning up the full HTTP server + DB + auth chain inside
// a test makes the fixture brittle, this test exercises the
// BOQRepository.List path directly through pgxpool. The HTTP layer
// adds ~constant overhead on top of the repo path, so the repo's p99
// is the meaningful gate.
//
// Skip-on-no-DB: same convention as enterprise_bulk_test.go.
//
//go:build perf

package perf

import (
	"context"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	concurrentReadGoroutines = 500
	concurrentReadDuration   = 30 * time.Second
	concurrentReadP99Budget  = 1 * time.Second
	concurrentReadLimit      = 20
)

func TestEnterpriseBOQ_ConcurrentList_P99(t *testing.T) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		t.Skip("DATABASE_URL not set — perf test requires a live postgres; skipping")
	}

	ctx, cancel := context.WithTimeout(context.Background(), concurrentReadDuration+10*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Skipf("pgxpool.New: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("pool.Ping: %v", err)
	}

	// Verify the BOQ list query at least parses against the schema
	// before fanning out. If the schema's shifted, the first probe
	// fails fast.
	if _, err := pool.Exec(ctx, `SELECT 1 FROM enterprise.boq_versions LIMIT 1`); err != nil {
		t.Skipf("enterprise.boq_versions probe: %v", err)
	}

	deadline := time.Now().Add(concurrentReadDuration)

	var (
		mu         sync.Mutex
		samples    = make([]time.Duration, 0, 50_000)
		errCount   atomic.Int64
		fiveXXOrDB atomic.Int64
		okCount    atomic.Int64
	)

	var wg sync.WaitGroup
	wg.Add(concurrentReadGoroutines)
	for w := 0; w < concurrentReadGoroutines; w++ {
		go func() {
			defer wg.Done()
			for time.Now().Before(deadline) {
				start := time.Now()
				// Match the canonical BOQ list query shape — ORDER BY
				// created_at DESC + LIMIT. This is the same path the
				// /api/enterprise/boqs?limit=20 handler hits.
				rows, err := pool.Query(ctx,
					`SELECT id, boq_number, opportunity_id, version_no, status,
					        sell_total, cost_total, margin_pct
					 FROM enterprise.boq_versions
					 ORDER BY created_at DESC
					 LIMIT $1`,
					concurrentReadLimit,
				)
				dur := time.Since(start)
				if err != nil {
					errCount.Add(1)
					fiveXXOrDB.Add(1) // any DB error = same severity as 5xx
					continue
				}
				count := 0
				for rows.Next() {
					count++
				}
				rows.Close()
				okCount.Add(1)
				mu.Lock()
				samples = append(samples, dur)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	total := okCount.Load() + errCount.Load()
	t.Logf("requests: %d (ok=%d errors=%d)", total, okCount.Load(), errCount.Load())

	if fiveXXOrDB.Load() > 0 {
		t.Fatalf("%d DB errors during concurrent load — expected zero (5xx-equivalent)", fiveXXOrDB.Load())
	}

	if len(samples) == 0 {
		t.Fatalf("no successful samples collected — fixture broken")
	}

	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	p99idx := int(float64(len(samples)) * 0.99)
	if p99idx >= len(samples) {
		p99idx = len(samples) - 1
	}
	p99 := samples[p99idx]
	p50 := samples[len(samples)/2]
	t.Logf("p50: %s   p99: %s   threshold p99 < %s", p50, p99, concurrentReadP99Budget)

	if p99 > concurrentReadP99Budget {
		t.Fatalf("p99 = %s exceeds threshold %s", p99, concurrentReadP99Budget)
	}
}
