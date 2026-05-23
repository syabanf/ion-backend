// Wave 118 — RADIUS failover retry + circuit-breaker (TC-RAD-* regression edge).
//
// The Wave 110 audit flagged RADIUS failover as missing. The existing
// LocalRadiusClient is just a DB-backed stub so failures are rare, but
// when the real FreeRadiusClient lands (phase 2), network blips are
// inevitable. This adapter wraps any RadiusClient with:
//
//   - Exponential backoff retry (configurable attempts + base delay)
//   - Circuit breaker (opens after N consecutive failures, stays open
//     until a probe call succeeds)
//
// The wrapper is opt-in — services that want the failover semantics
// build a FailoverRadiusClient(inner, cfg) and pass it where they'd
// pass any other RadiusClient. The interface signature is unchanged
// so the wrapper is a pure decorator.
package radius

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/network/domain"
	"github.com/ion-core/backend/internal/network/port"
)

// FailoverConfig tunes the retry + circuit-breaker behaviour.
type FailoverConfig struct {
	MaxAttempts        int           // total attempts including the first try; default 3
	InitialBackoff     time.Duration // first retry delay; default 100ms
	MaxBackoff         time.Duration // ceiling; default 5s
	BackoffMultiplier  float64       // exponential factor; default 2.0
	CircuitFailureThreshold int      // consecutive failures before opening; default 5
	CircuitResetTimeout time.Duration // how long to stay open before half-open; default 30s
}

// Default returns a sensible config for production use.
func DefaultFailoverConfig() FailoverConfig {
	return FailoverConfig{
		MaxAttempts:             3,
		InitialBackoff:          100 * time.Millisecond,
		MaxBackoff:              5 * time.Second,
		BackoffMultiplier:       2.0,
		CircuitFailureThreshold: 5,
		CircuitResetTimeout:     30 * time.Second,
	}
}

// circuitState is the internal breaker state.
type circuitState int

const (
	circuitClosed   circuitState = iota // normal operations
	circuitOpen                          // all calls fail-fast
	circuitHalfOpen                      // one probe call allowed
)

// ErrCircuitOpen is returned by the failover wrapper when the breaker is
// open and the call is fail-fast.
var ErrCircuitOpen = errors.New("radius failover: circuit open")

// FailoverRadiusClient decorates any RadiusClient with retry + breaker.
// All methods of port.RadiusClient are wrapped uniformly — a failure
// in any one increments the failure counter; a success resets it.
type FailoverRadiusClient struct {
	inner port.RadiusClient
	cfg   FailoverConfig

	mu              sync.Mutex
	state           circuitState
	failures        int
	openedAt        time.Time
	// sleeper is the package-level time.Sleep, but injectable for tests
	// so unit tests don't actually block on backoff.
	sleeper func(time.Duration)
	// nower is package-level time.Now, also injectable for tests.
	nower func() time.Time
}

// NewFailoverRadiusClient builds a failover decorator. cfg may be zero-
// valued; missing fields are filled from DefaultFailoverConfig.
func NewFailoverRadiusClient(inner port.RadiusClient, cfg FailoverConfig) *FailoverRadiusClient {
	d := DefaultFailoverConfig()
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = d.MaxAttempts
	}
	if cfg.InitialBackoff <= 0 {
		cfg.InitialBackoff = d.InitialBackoff
	}
	if cfg.MaxBackoff <= 0 {
		cfg.MaxBackoff = d.MaxBackoff
	}
	if cfg.BackoffMultiplier <= 0 {
		cfg.BackoffMultiplier = d.BackoffMultiplier
	}
	if cfg.CircuitFailureThreshold <= 0 {
		cfg.CircuitFailureThreshold = d.CircuitFailureThreshold
	}
	if cfg.CircuitResetTimeout <= 0 {
		cfg.CircuitResetTimeout = d.CircuitResetTimeout
	}
	return &FailoverRadiusClient{
		inner:   inner,
		cfg:     cfg,
		state:   circuitClosed,
		sleeper: time.Sleep,
		nower:   time.Now,
	}
}

// WithSleeper allows tests to inject a fast (or counting) sleeper so the
// suite doesn't actually wait on backoff.
func (f *FailoverRadiusClient) WithSleeper(s func(time.Duration)) *FailoverRadiusClient {
	if s != nil {
		f.sleeper = s
	}
	return f
}

// WithNower allows tests to inject a deterministic clock.
func (f *FailoverRadiusClient) WithNower(n func() time.Time) *FailoverRadiusClient {
	if n != nil {
		f.nower = n
	}
	return f
}

// =====================================================================
// Decorator methods — one per RadiusClient surface
// =====================================================================

var _ port.RadiusClient = (*FailoverRadiusClient)(nil)

func (f *FailoverRadiusClient) Provision(ctx context.Context, in domain.ProvisionInput) (*domain.RadiusAccount, error) {
	var out *domain.RadiusAccount
	err := f.callWithRetry(ctx, func(ctx context.Context) error {
		a, err := f.inner.Provision(ctx, in)
		out = a
		return err
	})
	return out, err
}

func (f *FailoverRadiusClient) PromoteToPermanent(ctx context.Context, customerID uuid.UUID) (*domain.RadiusAccount, error) {
	var out *domain.RadiusAccount
	err := f.callWithRetry(ctx, func(ctx context.Context) error {
		a, err := f.inner.PromoteToPermanent(ctx, customerID)
		out = a
		return err
	})
	return out, err
}

func (f *FailoverRadiusClient) Suspend(ctx context.Context, customerID uuid.UUID) (*domain.RadiusAccount, error) {
	var out *domain.RadiusAccount
	err := f.callWithRetry(ctx, func(ctx context.Context) error {
		a, err := f.inner.Suspend(ctx, customerID)
		out = a
		return err
	})
	return out, err
}

func (f *FailoverRadiusClient) Restore(ctx context.Context, customerID uuid.UUID) (*domain.RadiusAccount, error) {
	var out *domain.RadiusAccount
	err := f.callWithRetry(ctx, func(ctx context.Context) error {
		a, err := f.inner.Restore(ctx, customerID)
		out = a
		return err
	})
	return out, err
}

func (f *FailoverRadiusClient) Deactivate(ctx context.Context, customerID uuid.UUID) (*domain.RadiusAccount, error) {
	var out *domain.RadiusAccount
	err := f.callWithRetry(ctx, func(ctx context.Context) error {
		a, err := f.inner.Deactivate(ctx, customerID)
		out = a
		return err
	})
	return out, err
}

// Find passes through without retry — reads are idempotent and a failure
// here typically means "not found" rather than a network blip. Still
// wrapped with the breaker gate so the system fails fast when circuit
// is open.
func (f *FailoverRadiusClient) Find(ctx context.Context, customerID uuid.UUID) (*domain.RadiusAccount, error) {
	if !f.canCall() {
		return nil, ErrCircuitOpen
	}
	return f.inner.Find(ctx, customerID)
}

// =====================================================================
// Internals — retry + breaker
// =====================================================================

// callWithRetry is the per-call retry + breaker loop. Returns the last
// error if all attempts fail.
func (f *FailoverRadiusClient) callWithRetry(ctx context.Context, op func(context.Context) error) error {
	// Breaker gate.
	if !f.canCall() {
		return ErrCircuitOpen
	}

	delay := f.cfg.InitialBackoff
	var lastErr error
	for attempt := 0; attempt < f.cfg.MaxAttempts; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err := op(ctx)
		if err == nil {
			f.recordSuccess()
			return nil
		}
		lastErr = err
		// Sleep before next attempt (skip after the last one).
		if attempt+1 < f.cfg.MaxAttempts {
			f.sleeper(delay)
			delay = time.Duration(float64(delay) * f.cfg.BackoffMultiplier)
			if delay > f.cfg.MaxBackoff {
				delay = f.cfg.MaxBackoff
			}
		}
	}
	f.recordFailure()
	return lastErr
}

// canCall returns whether the breaker permits a call right now. The
// half-open state lets a single probe through; if it succeeds the
// breaker resets, otherwise it re-opens.
func (f *FailoverRadiusClient) canCall() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch f.state {
	case circuitClosed:
		return true
	case circuitOpen:
		if f.nower().Sub(f.openedAt) >= f.cfg.CircuitResetTimeout {
			// Move to half-open — allow a single probe.
			f.state = circuitHalfOpen
			return true
		}
		return false
	case circuitHalfOpen:
		// Half-open already; let the (presumably) only outstanding probe
		// run. A naive single-flight could be added here, but for Wave 118
		// the simple form is enough.
		return true
	default:
		return true
	}
}

func (f *FailoverRadiusClient) recordSuccess() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures = 0
	f.state = circuitClosed
}

func (f *FailoverRadiusClient) recordFailure() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures++
	if f.failures >= f.cfg.CircuitFailureThreshold {
		f.state = circuitOpen
		f.openedAt = f.nower()
	}
}

// State returns the current circuit state — exposed for observability +
// tests. (Not part of the RadiusClient interface; cast to the concrete
// type to read it.)
func (f *FailoverRadiusClient) State() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch f.state {
	case circuitClosed:
		return "closed"
	case circuitOpen:
		return "open"
	case circuitHalfOpen:
		return "half_open"
	default:
		return "unknown"
	}
}
