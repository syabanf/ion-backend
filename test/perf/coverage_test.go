// Coverage API perf — concurrent load against /api/network/coverage/check.
// The Phase-1 NFR is "<3s response at 100 concurrent." We fan out the
// requested concurrency with a fixed total of requests per worker, then
// compute the worst-case latency.
//
//go:build perf

package perf

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestCoverageThroughput(t *testing.T) {
	concurrency := 100
	if v := os.Getenv("CONCURRENCY"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			t.Fatalf("CONCURRENCY=%q invalid", v)
		}
		concurrency = n
	}
	perWorker := 10
	if v := os.Getenv("REQUESTS_PER_WORKER"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			t.Fatalf("REQUESTS_PER_WORKER=%q invalid", v)
		}
		perWorker = n
	}

	token := login(t)
	client := &http.Client{Timeout: 30 * time.Second}

	// Spread the GPS pin across a small grid so coverage-cache hits and
	// misses are both exercised. We deliberately stay near a known
	// ODP-covered area; the test cares about latency under load, not
	// the verdict correctness.
	body := func(i int) []byte {
		buf, _ := json.Marshal(map[string]any{
			"lat":            -6.2 + float64(i%5)*0.0001,
			"lng":            106.8 + float64(i%7)*0.0001,
			"max_candidates": 3,
		})
		return buf
	}

	var (
		wg              sync.WaitGroup
		mu              sync.Mutex
		latencies       []time.Duration
		errCount        int
		nonOKStatusHits int
	)
	start := time.Now()
	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				req, _ := http.NewRequest("POST", baseURL+"/api/network/coverage/check",
					bytes.NewReader(body(workerID*perWorker+i)))
				req.Header.Set("Authorization", "Bearer "+token)
				req.Header.Set("Content-Type", "application/json")
				t0 := time.Now()
				resp, err := client.Do(req)
				lat := time.Since(t0)
				mu.Lock()
				latencies = append(latencies, lat)
				if err != nil {
					errCount++
				} else {
					if resp.StatusCode != http.StatusOK {
						nonOKStatusHits++
					}
					resp.Body.Close()
				}
				mu.Unlock()
			}
		}(w)
	}
	wg.Wait()
	elapsed := time.Since(start)

	stats := summarise(latencies)
	t.Logf("coverage perf: c=%d × %d = %d reqs in %s",
		concurrency, perWorker, concurrency*perWorker, elapsed)
	t.Logf("  p50=%s p95=%s p99=%s max=%s",
		stats.p50, stats.p95, stats.p99, stats.max)
	t.Logf("  errors=%d non-OK=%d", errCount, nonOKStatusHits)

	// NFR check: p99 must stay under 3s when the concurrency knob is
	// exactly the spec'd target. For other knobs we still log but
	// don't fail.
	if concurrency >= 100 {
		if stats.p99 > 3*time.Second {
			t.Fatalf("p99 %s exceeds 3s NFR at concurrency=%d", stats.p99, concurrency)
		}
	}
	_ = fmt.Sprintf
}

type latencyStats struct {
	p50, p95, p99, max time.Duration
}

func summarise(in []time.Duration) latencyStats {
	if len(in) == 0 {
		return latencyStats{}
	}
	cp := make([]time.Duration, len(in))
	copy(cp, in)
	// Insertion sort — list is small (≤ a few thousand at our knobs).
	for i := 1; i < len(cp); i++ {
		j := i
		for j > 0 && cp[j] < cp[j-1] {
			cp[j], cp[j-1] = cp[j-1], cp[j]
			j--
		}
	}
	at := func(p float64) time.Duration {
		idx := int(float64(len(cp)-1) * p)
		return cp[idx]
	}
	return latencyStats{
		p50: at(0.50), p95: at(0.95), p99: at(0.99), max: cp[len(cp)-1],
	}
}
