package httpserver

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	derrors "github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// Wave 104 — advisory-lock helper tests
//
// Requires a live DATABASE_URL; t.Skip cleanly when unset so the
// in-process suite stays green on a clean checkout.
// =====================================================================

func advisoryTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping advisory-lock DB tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("could not connect to DB: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// TestWithAdvisoryLock_HappyPath — fn runs and returns its value.
func TestWithAdvisoryLock_HappyPath(t *testing.T) {
	pool := advisoryTestPool(t)
	key := int64(time.Now().UnixNano())
	ran := false
	err := WithAdvisoryLock(context.Background(), pool, key, func(ctx context.Context) error {
		ran = true
		return nil
	})
	if err != nil {
		t.Fatalf("WithAdvisoryLock: %v", err)
	}
	if !ran {
		t.Error("fn was not invoked")
	}
}

// TestWithAdvisoryLock_FirstWinsSecondConflicts — concurrent callers on
// the SAME key must serialize; the second observer gets Conflict.
func TestWithAdvisoryLock_FirstWinsSecondConflicts(t *testing.T) {
	pool := advisoryTestPool(t)
	key := int64(time.Now().UnixNano())

	holdReleased := make(chan struct{})
	holdStarted := make(chan struct{})
	var firstErr, secondErr error
	var wg sync.WaitGroup
	wg.Add(2)

	// Caller A — holds the lock until we signal release.
	go func() {
		defer wg.Done()
		firstErr = WithAdvisoryLock(context.Background(), pool, key, func(ctx context.Context) error {
			close(holdStarted)
			<-holdReleased
			return nil
		})
	}()

	// Wait for A to acquire.
	<-holdStarted

	// Caller B — try to acquire the same key; expect Conflict.
	go func() {
		defer wg.Done()
		secondErr = WithAdvisoryLock(context.Background(), pool, key, func(ctx context.Context) error {
			return nil
		})
		close(holdReleased)
	}()

	wg.Wait()
	if firstErr != nil {
		t.Errorf("first caller error = %v, want nil", firstErr)
	}
	if secondErr == nil {
		t.Fatalf("second caller error = nil, want lock.contended Conflict")
	}
	var de *derrors.Error
	if !errors.As(secondErr, &de) {
		t.Fatalf("second caller error is not *derrors.Error: %T %v", secondErr, secondErr)
	}
	if de.Kind != derrors.KindConflict {
		t.Errorf("second caller error kind = %v, want Conflict", de.Kind)
	}
	if de.Code != "lock.contended" {
		t.Errorf("second caller error code = %q, want lock.contended", de.Code)
	}
}

// TestWithAdvisoryLock_ReleasesOnFnReturn — after fn returns, the next
// caller acquires the same key successfully.
func TestWithAdvisoryLock_ReleasesOnFnReturn(t *testing.T) {
	pool := advisoryTestPool(t)
	key := int64(time.Now().UnixNano())
	if err := WithAdvisoryLock(context.Background(), pool, key, func(ctx context.Context) error {
		return nil
	}); err != nil {
		t.Fatalf("1st WithAdvisoryLock: %v", err)
	}
	if err := WithAdvisoryLock(context.Background(), pool, key, func(ctx context.Context) error {
		return nil
	}); err != nil {
		t.Fatalf("2nd WithAdvisoryLock (lock should have been released): %v", err)
	}
}

// TestWithAdvisoryLock_ReleasesOnFnError — lock is freed even when the
// wrapped function returns an error.
func TestWithAdvisoryLock_ReleasesOnFnError(t *testing.T) {
	pool := advisoryTestPool(t)
	key := int64(time.Now().UnixNano())
	sentinel := errors.New("boom")
	err := WithAdvisoryLock(context.Background(), pool, key, func(ctx context.Context) error {
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want sentinel", err)
	}
	// The lock should now be free.
	if err := WithAdvisoryLock(context.Background(), pool, key, func(ctx context.Context) error {
		return nil
	}); err != nil {
		t.Fatalf("re-acquire after fn error failed: %v", err)
	}
}

func TestWithAdvisoryLock_NilPool(t *testing.T) {
	err := WithAdvisoryLock(context.Background(), nil, 1, func(ctx context.Context) error { return nil })
	if err == nil {
		t.Fatal("nil pool should error")
	}
	var de *derrors.Error
	if !errors.As(err, &de) {
		t.Fatalf("nil-pool error is not *derrors.Error: %T %v", err, err)
	}
	if de.Code != "lock.not_configured" {
		t.Errorf("nil-pool error code = %q, want lock.not_configured", de.Code)
	}
}

func TestLockKeyForBOQ_Deterministic(t *testing.T) {
	id := uuid.New()
	a := LockKeyForBOQ(id)
	b := LockKeyForBOQ(id)
	if a != b {
		t.Errorf("LockKeyForBOQ not deterministic: %d vs %d", a, b)
	}
}

func TestLockKeyForBOQ_DistinctForDifferentIDs(t *testing.T) {
	id1 := uuid.New()
	id2 := uuid.New()
	if LockKeyForBOQ(id1) == LockKeyForBOQ(id2) {
		t.Errorf("LockKeyForBOQ(%s) == LockKeyForBOQ(%s) — unlucky collision", id1, id2)
	}
}

func TestLockKeyForApproval_Deterministic(t *testing.T) {
	id := uuid.New()
	if LockKeyForApproval(id) != LockKeyForApproval(id) {
		t.Errorf("LockKeyForApproval not deterministic")
	}
}
