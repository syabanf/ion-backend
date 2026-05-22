// Package ratelimit ships a sliding-window rate limiter for public
// (unauthenticated) HTTP endpoints. It uses platform.rate_limit_log
// as the backing store so we don't introduce a Redis dependency for
// a low-throughput surface.
//
// Semantics:
//   - bucket key = "<endpoint>|<client-ip>"   (caller chooses)
//   - on each request, DELETE rows older than `window`, COUNT the
//     remaining, INSERT one, and return 429 when count > limit
//   - the count + insert run in a single transaction so two
//     concurrent requests can't both squeak past the limit
//
// Trade-off: this is one round-trip + a small COUNT per request.
// At 1k req/s we'd want Redis. At the volumes a public coverage-
// check endpoint sees (single-digit per second), the SQL approach
// is fine and removes a dep.
package ratelimit

import (
	"context"
	"net"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Config describes a single bucket.
type Config struct {
	// Endpoint is the logical name; combined with the client IP it
	// becomes the bucket key. Use one Config per endpoint so two
	// endpoints don't share quota.
	Endpoint string
	// Limit is the maximum number of allowed calls per Window.
	Limit int
	// Window is the rolling window. 1m is a sensible default for
	// coverage checks; 1h for heavier writes.
	Window time.Duration
}

// Middleware returns a chi-compatible middleware. The pool must be
// pre-opened; the middleware never blocks waiting for a connection.
//
// Failure modes:
//   - 500 if the DB query errors (rare, fail-open is the alternative
//     and we'd rather make the failure visible)
//   - 429 with Retry-After when the bucket is full
func Middleware(pool *pgxpool.Pool, cfg Config) func(http.Handler) http.Handler {
	if cfg.Window == 0 {
		cfg.Window = time.Minute
	}
	if cfg.Limit == 0 {
		cfg.Limit = 30
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := clientIP(r)
			bucket := cfg.Endpoint + "|" + ip
			over, err := count(r.Context(), pool, bucket, cfg.Window, cfg.Limit)
			if err != nil {
				http.Error(w, "rate limit check failed", http.StatusInternalServerError)
				return
			}
			if over {
				w.Header().Set("Retry-After", iSecs(cfg.Window))
				http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func count(ctx context.Context, pool *pgxpool.Pool, bucket string, window time.Duration, limit int) (bool, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	// 1. Purge old rows for this bucket so the table stays small.
	if _, err := tx.Exec(ctx, `
		DELETE FROM platform.rate_limit_log
		WHERE bucket = $1 AND occurred_at < NOW() - $2::interval
	`, bucket, window.String()); err != nil {
		return false, err
	}
	// 2. Count what's left.
	var n int
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*) FROM platform.rate_limit_log WHERE bucket = $1
	`, bucket).Scan(&n); err != nil {
		return false, err
	}
	if n >= limit {
		return true, tx.Commit(ctx)
	}
	// 3. Record this call.
	if _, err := tx.Exec(ctx, `
		INSERT INTO platform.rate_limit_log (bucket) VALUES ($1)
	`, bucket); err != nil {
		return false, err
	}
	return false, tx.Commit(ctx)
}

func clientIP(r *http.Request) string {
	// Trust XFF only when behind a known proxy; here we just take the
	// remote-addr because the gateway sets it correctly for the
	// public surface.
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// iSecs renders a duration as integer seconds for the Retry-After
// header (RFC 7231 §7.1.3).
func iSecs(d time.Duration) string {
	n := int(d.Seconds())
	if n < 1 {
		n = 1
	}
	// Avoid pulling fmt for a one-liner.
	const digits = "0123456789"
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = digits[n%10]
		n /= 10
	}
	return string(buf[i:])
}
