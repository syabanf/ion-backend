// Package platformconfig is a read-only client for identity.platform_config.
//
// Other services need a few of these keys at runtime (cable distance limit,
// fiber thresholds, inventory valuation method, etc.). They could call the
// identity-svc HTTP API, but a direct DB read is simpler at this scale —
// the schema is shared, the table is small, and reads are cacheable.
//
// When we split databases per service this turns into an HTTP client to a
// new platform-config endpoint on identity-svc. The interface stays the
// same.
package platformconfig

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Reader fetches platform_config values with a small in-memory cache.
// Cache TTL = 60s (per PRD §12 NFR: "configuration changes propagate
// within 60 seconds").
type Reader struct {
	pool *pgxpool.Pool
	ttl  time.Duration

	mu    sync.RWMutex
	cache map[string]cacheEntry
}

type cacheEntry struct {
	value     string
	expiresAt time.Time
}

func New(pool *pgxpool.Pool) *Reader {
	return &Reader{
		pool:  pool,
		ttl:   60 * time.Second,
		cache: make(map[string]cacheEntry),
	}
}

// String reads a config value. Returns def if the key is unset.
func (r *Reader) String(ctx context.Context, key, def string) string {
	if v, ok := r.lookup(key); ok {
		return v
	}
	v, err := r.fetch(ctx, key)
	if err != nil {
		return def
	}
	return v
}

// Int reads a config value parsed as int. Returns def on missing / parse error.
func (r *Reader) Int(ctx context.Context, key string, def int) int {
	v := r.String(ctx, key, "")
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// Float reads a config value parsed as float64. Returns def on missing / parse error.
func (r *Reader) Float(ctx context.Context, key string, def float64) float64 {
	v := r.String(ctx, key, "")
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return def
	}
	return f
}

func (r *Reader) lookup(key string) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.cache[key]
	if !ok || time.Now().After(e.expiresAt) {
		return "", false
	}
	return e.value, true
}

func (r *Reader) fetch(ctx context.Context, key string) (string, error) {
	var v string
	err := r.pool.QueryRow(ctx,
		`SELECT config_value FROM identity.platform_config WHERE config_key = $1`,
		key,
	).Scan(&v)
	if err != nil {
		if err == pgx.ErrNoRows {
			r.cacheSet(key, "")
			return "", err
		}
		return "", err
	}
	r.cacheSet(key, v)
	return v, nil
}

func (r *Reader) cacheSet(key, value string) {
	r.mu.Lock()
	r.cache[key] = cacheEntry{value: value, expiresAt: time.Now().Add(r.ttl)}
	r.mu.Unlock()
}
