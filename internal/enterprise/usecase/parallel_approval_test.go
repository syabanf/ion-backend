package usecase

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
	"github.com/ion-core/backend/pkg/httpserver"
)

// =====================================================================
// Wave 104 — Edge #2: parallel approval-decision concurrency
//
// Two concurrent approval-decision requests on the same approval_instance
// must serialize via a postgres advisory lock; the second caller observes
// a typed Conflict. This test exercises the lock helper directly — the
// approval-instance usecase wires the same helper, so the lock contract
// is the load-bearing invariant.
//
// Requires DATABASE_URL; t.Skip cleanly otherwise.
// =====================================================================

func parallelApprovalPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping parallel-approval DB test")
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

// TestParallelApproval_AdvisoryLockSerializes — two concurrent approval
// decisions on the same approval-instance id derive the same advisory-
// lock key and only one acquires it. The second gets Conflict.
func TestParallelApproval_AdvisoryLockSerializes(t *testing.T) {
	pool := parallelApprovalPool(t)
	approvalID := uuid.New()
	key := httpserver.LockKeyForApproval(approvalID)

	// First caller — holds the lock for a configurable spin.
	holdStarted := make(chan struct{})
	holdRelease := make(chan struct{})
	var firstErr, secondErr error
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		firstErr = httpserver.WithAdvisoryLock(context.Background(), pool, key,
			func(ctx context.Context) error {
				close(holdStarted)
				<-holdRelease
				return nil
			})
	}()

	<-holdStarted

	go func() {
		defer wg.Done()
		secondErr = httpserver.WithAdvisoryLock(context.Background(), pool, key,
			func(ctx context.Context) error {
				return nil
			})
		close(holdRelease)
	}()

	wg.Wait()
	if firstErr != nil {
		t.Errorf("first caller error = %v, want nil", firstErr)
	}
	if secondErr == nil {
		t.Fatalf("second caller error = nil, want lock.contended")
	}
	var de *derrors.Error
	if !errors.As(secondErr, &de) {
		t.Fatalf("second caller error type: %T %v", secondErr, secondErr)
	}
	if de.Kind != derrors.KindConflict {
		t.Errorf("second caller kind = %v, want Conflict", de.Kind)
	}
	if de.Code != "lock.contended" {
		t.Errorf("second caller code = %q, want lock.contended", de.Code)
	}
}

// TestParallelApproval_DistinctApprovalsDoNotConflict — locks on
// different approval ids must NOT serialize. Two parallel callers on
// disjoint ids both succeed.
func TestParallelApproval_DistinctApprovalsDoNotConflict(t *testing.T) {
	pool := parallelApprovalPool(t)
	a, b := uuid.New(), uuid.New()
	keyA := httpserver.LockKeyForApproval(a)
	keyB := httpserver.LockKeyForApproval(b)
	if keyA == keyB {
		t.Skip("unlucky uuid hash collision — re-run")
	}

	holdARelease := make(chan struct{})
	var aErr, bErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		aErr = httpserver.WithAdvisoryLock(context.Background(), pool, keyA,
			func(ctx context.Context) error {
				<-holdARelease
				return nil
			})
	}()
	go func() {
		defer wg.Done()
		// Tiny sleep to ensure A grabs first.
		time.Sleep(50 * time.Millisecond)
		bErr = httpserver.WithAdvisoryLock(context.Background(), pool, keyB,
			func(ctx context.Context) error { return nil })
		close(holdARelease)
	}()
	wg.Wait()
	if aErr != nil {
		t.Errorf("caller A error = %v, want nil", aErr)
	}
	if bErr != nil {
		t.Errorf("caller B (distinct key) error = %v, want nil — distinct keys must NOT serialize", bErr)
	}
}
