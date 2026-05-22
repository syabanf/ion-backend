package httpserver

import (
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/ion-core/backend/pkg/errors"
)

// RateLimit is a per-IP token bucket suitable for unauthenticated endpoints
// (login, refresh). It's not a substitute for upstream WAF/CDN limits, but
// it does slow down credential stuffing from a single misbehaving client.
//
// Tuning:
//
//	burst   — max requests allowed before throttling kicks in (e.g. 10)
//	refill  — tokens added per second per IP (e.g. 0.5 = 1 request per 2s)
//
// State is per-process, so a multi-replica deployment splits the budget
// across replicas — fine for round 1, since the goal here is to slow,
// not block. Upstream NGINX/CF should also have its own bucket.
type RateLimit struct {
	burst  float64
	refill float64

	mu      sync.Mutex
	buckets map[string]*bucket
	// Janitor removes idle buckets so the map doesn't grow without bound
	// (one entry per unique source IP that has ever hit the endpoint).
	lastJanitor time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

func NewRateLimit(burst, refillPerSec float64) *RateLimit {
	return &RateLimit{
		burst:       burst,
		refill:      refillPerSec,
		buckets:     map[string]*bucket{},
		lastJanitor: time.Now(),
	}
}

// Allow consumes one token for `key` (a stable identifier like an IP). It
// returns true when the request is allowed, false when throttled.
func (rl *RateLimit) Allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	b, ok := rl.buckets[key]
	if !ok {
		b = &bucket{tokens: rl.burst, last: now}
		rl.buckets[key] = b
	}
	// Refill since last request.
	elapsed := now.Sub(b.last).Seconds()
	b.tokens += elapsed * rl.refill
	if b.tokens > rl.burst {
		b.tokens = rl.burst
	}
	b.last = now

	if b.tokens < 1 {
		rl.maybeJanitor(now)
		return false
	}
	b.tokens--
	rl.maybeJanitor(now)
	return true
}

// maybeJanitor periodically prunes idle buckets. Called under the lock.
// We run it at most once per 30s and drop buckets that have been silent
// for ≥ 5 minutes (more than enough recovery time at our refill rate).
func (rl *RateLimit) maybeJanitor(now time.Time) {
	if now.Sub(rl.lastJanitor) < 30*time.Second {
		return
	}
	rl.lastJanitor = now
	cutoff := now.Add(-5 * time.Minute)
	for k, b := range rl.buckets {
		if b.last.Before(cutoff) {
			delete(rl.buckets, k)
		}
	}
}

// Middleware returns chi middleware that throttles by IP. The key is the
// remote-IP that chi.RealIP sets (header-aware), falling back to RemoteAddr.
func (rl *RateLimit) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := clientIP(r)
			if !rl.Allow(ip) {
				// retry_after = the time needed to earn 1 token at the
				// configured refill rate. Header AND JSON envelope are
				// populated by WriteError when the domain error carries
				// the hint.
				retry := 2
				if rl.refill > 0 {
					if seconds := int(1.0/rl.refill) + 1; seconds > retry {
						retry = seconds
					}
				}
				WriteError(w, errors.New(errors.KindRateLimited, "auth.rate_limit",
					"too many login attempts, slow down").WithRetryAfter(retry))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func clientIP(r *http.Request) string {
	// chi.RealIP middleware rewrites RemoteAddr to the original client IP
	// when X-Forwarded-For/X-Real-IP is set. We strip the port if present.
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
