// Wave 118 — RADIUS failover tests (TC-RAD-* regression edge).

package radius

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/network/domain"
)

// fakeRadius is a programmable RadiusClient stub. attemptsBeforeOK is
// decremented per call to Provision; when it reaches 0 the call returns
// success.
type fakeRadius struct {
	attemptsBeforeOK int32
	totalCalls       int32
	failAlways       bool
}

func (f *fakeRadius) Provision(_ context.Context, _ domain.ProvisionInput) (*domain.RadiusAccount, error) {
	atomic.AddInt32(&f.totalCalls, 1)
	if f.failAlways {
		return nil, errors.New("boom")
	}
	left := atomic.AddInt32(&f.attemptsBeforeOK, -1)
	if left > 0 {
		return nil, errors.New("transient")
	}
	return &domain.RadiusAccount{}, nil
}
func (f *fakeRadius) PromoteToPermanent(_ context.Context, _ uuid.UUID) (*domain.RadiusAccount, error) {
	return nil, nil
}
func (f *fakeRadius) Suspend(_ context.Context, _ uuid.UUID) (*domain.RadiusAccount, error) {
	return nil, nil
}
func (f *fakeRadius) Restore(_ context.Context, _ uuid.UUID) (*domain.RadiusAccount, error) {
	return nil, nil
}
func (f *fakeRadius) Deactivate(_ context.Context, _ uuid.UUID) (*domain.RadiusAccount, error) {
	return nil, nil
}
func (f *fakeRadius) Find(_ context.Context, _ uuid.UUID) (*domain.RadiusAccount, error) {
	return nil, nil
}

func TestFailover_Success_FirstAttempt(t *testing.T) {
	inner := &fakeRadius{attemptsBeforeOK: 1}
	wrap := NewFailoverRadiusClient(inner, FailoverConfig{MaxAttempts: 3}).
		WithSleeper(func(time.Duration) {})

	_, err := wrap.Provision(context.Background(), domain.ProvisionInput{})
	if err != nil {
		t.Fatalf("success path failed: %v", err)
	}
	if atomic.LoadInt32(&inner.totalCalls) != 1 {
		t.Fatalf("calls: want 1, got %d", inner.totalCalls)
	}
	if wrap.State() != "closed" {
		t.Fatalf("circuit should stay closed; got %s", wrap.State())
	}
}

func TestFailover_FirstAttemptFail_SecondPass(t *testing.T) {
	inner := &fakeRadius{attemptsBeforeOK: 2}
	wrap := NewFailoverRadiusClient(inner, FailoverConfig{MaxAttempts: 3}).
		WithSleeper(func(time.Duration) {})

	_, err := wrap.Provision(context.Background(), domain.ProvisionInput{})
	if err != nil {
		t.Fatalf("expected eventual success: %v", err)
	}
	if atomic.LoadInt32(&inner.totalCalls) != 2 {
		t.Fatalf("retry attempts: want 2, got %d", inner.totalCalls)
	}
	if wrap.State() != "closed" {
		t.Fatalf("circuit should be closed after success: %s", wrap.State())
	}
}

func TestFailover_AllAttemptsFail_CircuitOpens(t *testing.T) {
	inner := &fakeRadius{failAlways: true}
	wrap := NewFailoverRadiusClient(inner, FailoverConfig{
		MaxAttempts:             3,
		CircuitFailureThreshold: 2, // open after 2 failed *calls* (each = 3 attempts)
	}).WithSleeper(func(time.Duration) {})

	// First call burns through 3 attempts and ticks failures→1.
	_, err := wrap.Provision(context.Background(), domain.ProvisionInput{})
	if err == nil {
		t.Fatal("expected error from all-fail inner")
	}
	if wrap.State() != "closed" {
		t.Fatalf("circuit should still be closed after 1 fail: %s", wrap.State())
	}
	// Second call ticks failures→2 → opens circuit.
	_, err = wrap.Provision(context.Background(), domain.ProvisionInput{})
	if err == nil {
		t.Fatal("expected error from all-fail inner (second call)")
	}
	if wrap.State() != "open" {
		t.Fatalf("circuit should open after threshold: %s", wrap.State())
	}
	// Third call should fail-fast with ErrCircuitOpen — inner.totalCalls
	// should NOT increase by another 3 attempts.
	prev := atomic.LoadInt32(&inner.totalCalls)
	_, err = wrap.Provision(context.Background(), domain.ProvisionInput{})
	if !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("want ErrCircuitOpen, got %v", err)
	}
	if got := atomic.LoadInt32(&inner.totalCalls); got != prev {
		t.Fatalf("circuit open should fail-fast; calls bumped by %d", got-prev)
	}
}

func TestFailover_Recovery_HalfOpenToClosed(t *testing.T) {
	inner := &fakeRadius{failAlways: true}
	// Manually drive time so we can step past the reset timeout.
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	wrap := NewFailoverRadiusClient(inner, FailoverConfig{
		MaxAttempts:             1,
		CircuitFailureThreshold: 1,
		CircuitResetTimeout:     500 * time.Millisecond,
	}).WithSleeper(func(time.Duration) {}).
		WithNower(func() time.Time { return now })

	// Open the circuit.
	_, _ = wrap.Provision(context.Background(), domain.ProvisionInput{})
	if wrap.State() != "open" {
		t.Fatalf("circuit should be open: %s", wrap.State())
	}
	// Step time past the reset timeout and flip inner to success.
	now = now.Add(1 * time.Second)
	inner.failAlways = false
	inner.attemptsBeforeOK = 1

	_, err := wrap.Provision(context.Background(), domain.ProvisionInput{})
	if err != nil {
		t.Fatalf("half-open probe should succeed: %v", err)
	}
	if wrap.State() != "closed" {
		t.Fatalf("circuit should reset to closed after probe success: %s", wrap.State())
	}
}
