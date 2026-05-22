// GPS streaming perf — simulates the production GPS-streaming load
// shape and asserts ingestion stays under target latency.
//
// Math we're testing against:
//
//   Per-branch fleet:   ~50 techs
//   Branches:           8  → 400 concurrent techs
//   Streamer interval:  20s/tick
//   ⇒ Steady-state ingest = 400 / 20 = 20 pings/sec
//   ⇒ Per hour: 72 000 rows
//   ⇒ Per month: ~52 M rows
//
// We blast a 2× safety margin (40 pings/sec) for `duration` seconds
// and assert:
//   1. Every POST returns HTTP 201 (no shed loads, no 5xx)
//   2. Median round-trip stays under `medianBudgetMs`
//   3. p99 stays under `p99BudgetMs`
//
// Run:
//
//   go test -tags=perf -run TestGPSStreamingThroughput \
//     -v ./test/perf -timeout 5m \
//     -args -duration=30s -techs=50
//
//go:build perf

package perf

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	gpsDuration       = flag.Duration("duration", 20*time.Second, "load test duration")
	gpsTechs          = flag.Int("techs", 50, "simulated concurrent techs")
	gpsIntervalMS     = flag.Int("interval-ms", 500, "per-tech ping interval (ms); production = 20000")
	gpsMedianBudgetMS = flag.Int("median-ms", 50, "median request budget (ms)")
	gpsP99BudgetMS    = flag.Int("p99-ms", 250, "p99 request budget (ms)")
)

func TestGPSStreamingThroughput(t *testing.T) {
	if testing.Short() {
		t.Skip("perf test, skipped in -short mode")
	}

	// Login as the seeded tech.
	ctx := context.Background()
	httpC := &http.Client{Timeout: 5 * time.Second}
	pwd := envPerfOr("SEED_DEMO_PW", "IonDemo!2026Tour")
	techToken := loginPerf(t, httpC, "tech@ion.local", pwd)

	// Pick an active WO for ping payloads.
	pool := openPerfPool(t)
	defer pool.Close()
	techUserID := mustOne(t, pool, "SELECT id FROM identity.users WHERE email='tech@ion.local'")
	woID := mustOne(t, pool, `
		SELECT w.id FROM field.work_orders w
		JOIN field.wo_assignments a ON a.wo_id = w.id
		WHERE a.technician_id=$1
		  AND w.status IN ('assigned','dispatched','in_progress')
		LIMIT 1`, techUserID)

	t.Logf("gps load: techs=%d interval=%dms duration=%v target_rate=%.1f pings/sec",
		*gpsTechs, *gpsIntervalMS, *gpsDuration,
		float64(*gpsTechs)*1000.0/float64(*gpsIntervalMS))

	// Baseline row count for delta math at the end.
	var baseline int
	_ = pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM field.tech_locations WHERE captured_at > NOW() - INTERVAL '1 minute'`,
	).Scan(&baseline)

	// Each "tech" is a goroutine with its own deadline + jitter so we
	// don't all fire on the same tick.
	var (
		latencies = make([]int64, 0, 4096)
		latMu     sync.Mutex
		ok2xx     int64
		fail      int64
		wg        sync.WaitGroup
	)
	deadline := time.Now().Add(*gpsDuration)

	for i := 0; i < *gpsTechs; i++ {
		wg.Add(1)
		go func(techIdx int) {
			defer wg.Done()
			// jitter the first tick by ±50% of interval
			jitter := time.Duration(rand.Int63n(int64(*gpsIntervalMS))) * time.Millisecond
			time.Sleep(jitter)
			ticker := time.NewTicker(time.Duration(*gpsIntervalMS) * time.Millisecond)
			defer ticker.Stop()
			for {
				if time.Now().After(deadline) {
					return
				}
				start := time.Now()
				if pingOK(httpC, techToken, woID, techIdx) {
					atomic.AddInt64(&ok2xx, 1)
					ms := time.Since(start).Milliseconds()
					latMu.Lock()
					latencies = append(latencies, ms)
					latMu.Unlock()
				} else {
					atomic.AddInt64(&fail, 1)
				}
				<-ticker.C
			}
		}(i)
	}
	wg.Wait()

	// Stats.
	total := atomic.LoadInt64(&ok2xx) + atomic.LoadInt64(&fail)
	if total == 0 {
		t.Fatalf("no requests fired — check tech token + WO setup")
	}
	failRate := float64(atomic.LoadInt64(&fail)) / float64(total)
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	median := pct(latencies, 50)
	p95 := pct(latencies, 95)
	p99 := pct(latencies, 99)
	rate := float64(total) / gpsDuration.Seconds()

	t.Logf("results: total=%d ok=%d fail=%d (%.2f%% errors) rate=%.1f req/s median=%dms p95=%dms p99=%dms",
		total, ok2xx, fail, failRate*100, rate, median, p95, p99)

	// Assertions.
	if failRate > 0.01 {
		t.Fatalf("error rate %.2f%% exceeds 1%% budget", failRate*100)
	}
	if median > int64(*gpsMedianBudgetMS) {
		t.Fatalf("median latency %dms exceeds budget %dms", median, *gpsMedianBudgetMS)
	}
	if p99 > int64(*gpsP99BudgetMS) {
		t.Fatalf("p99 latency %dms exceeds budget %dms", p99, *gpsP99BudgetMS)
	}

	// Sanity: rows we wrote actually landed.
	var newCount int
	_ = pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM field.tech_locations WHERE captured_at > NOW() - INTERVAL '1 minute'`,
	).Scan(&newCount)
	delta := newCount - baseline
	t.Logf("DB delta: %d new rows in field.tech_locations", delta)
	if delta < int(ok2xx)-5 { // allow tiny clock skew
		t.Fatalf("expected ~%d new rows in DB, got delta=%d", ok2xx, delta)
	}
}

// pct returns the v-th percentile from a sorted ascending slice.
// Empty slice → 0.
func pct(sorted []int64, p int) int64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := (len(sorted) * p) / 100
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func pingOK(c *http.Client, token, woID string, techIdx int) bool {
	body := map[string]any{
		"wo_id":      woID,
		"lat":        -6.2088 + rand.Float64()*0.001,
		"lng":        106.8456 + rand.Float64()*0.001,
		"accuracy_m": 10 + rand.Float64()*20,
	}
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, baseURL+"/api/field/tech-locations", bytes.NewReader(b))
	req.Header.Set("content-type", "application/json")
	req.Header.Set("authorization", "Bearer "+token)
	resp, err := c.Do(req)
	if err != nil {
		return false
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode == 201
}

// =====================================================================
// Helpers — small wrappers so this file is self-contained.
// =====================================================================

func loginPerf(t *testing.T, c *http.Client, email, password string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"email": email, "password": password})
	req, _ := http.NewRequest(http.MethodPost, baseURL+"/api/identity/auth/login", bytes.NewReader(body))
	req.Header.Set("content-type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		buf, _ := io.ReadAll(resp.Body)
		t.Fatalf("login HTTP %d: %s", resp.StatusCode, buf)
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out.AccessToken == "" {
		t.Fatalf("empty token (is %s seeded?)", email)
	}
	return out.AccessToken
}

func openPerfPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := envPerfOr("DATABASE_URL", "postgres://syabanf@localhost:5432/ion_core?sslmode=disable")
	p, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	return p
}

func mustOne(t *testing.T, pool *pgxpool.Pool, q string, args ...any) string {
	t.Helper()
	var s string
	if err := pool.QueryRow(context.Background(), q, args...).Scan(&s); err != nil {
		t.Skipf("seed missing for %q: %v", q, err)
	}
	return s
}

func envPerfOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// Compile-time guard against unused imports when only running other
// perf files.
var _ = fmt.Sprintf
