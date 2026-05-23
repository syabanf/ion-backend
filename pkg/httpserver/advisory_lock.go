// Package httpserver — Wave 104 advisory-lock helper.
//
// Postgres advisory locks let two concurrent goroutines serialize on a
// 64-bit key without taking a row lock. The Wave-91 audit doc's
// "Parallel Concurrent Approvers" entry requires this for Edge #2 —
// two operators clicking Approve at the same instant must not both win;
// one must observe a Conflict.
//
// We use `pg_try_advisory_lock(key)` (not the blocking variant) so a
// contended call returns a typed Conflict error immediately rather than
// hanging the request thread.
package httpserver

import (
	"context"
	"encoding/binary"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	derrors "github.com/ion-core/backend/pkg/errors"
)

// WithAdvisoryLock acquires the postgres session-level advisory lock
// identified by `key`, runs `fn`, then unlocks. If the lock is already
// held by another session a typed Conflict (`lock.contended`) is
// returned WITHOUT invoking `fn`.
//
// The lock is released in a deferred call so a panic / error inside `fn`
// still frees it. We acquire a connection from the pool and pin both
// the lock + unlock to that connection — advisory locks are per-session
// in postgres, so the same pgx.Conn must run both calls.
//
// Returning the result of `fn` unchanged keeps the helper transparent
// to callers — a successful run reflects the wrapped function's error.
func WithAdvisoryLock(
	ctx context.Context,
	pool *pgxpool.Pool,
	key int64,
	fn func(ctx context.Context) error,
) error {
	if pool == nil {
		// Nil pool — caller forgot to wire the DB. Surfacing as Internal
		// keeps the contract uniform with the rest of the package.
		return derrors.Wrap(derrors.KindInternal, "lock.not_configured",
			"advisory lock requires a pgx pool", nil)
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return derrors.Wrap(derrors.KindUnavailable, "lock.acquire",
			"failed to acquire pool connection for advisory lock", err)
	}
	defer conn.Release()

	var got bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", key).Scan(&got); err != nil {
		return derrors.Wrap(derrors.KindInternal, "lock.try",
			"pg_try_advisory_lock failed", err)
	}
	if !got {
		return derrors.Conflict("lock.contended",
			"the requested operation is already in flight; please retry")
	}
	defer func() {
		// Best-effort unlock. If the connection died mid-flight the lock
		// is auto-released when the session ends, so an error here is
		// non-fatal — log via the conn's auto-release on Release() above.
		_, _ = conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", key)
	}()

	return fn(ctx)
}

// LockKeyForBOQ derives a stable int64 advisory-lock key from a BOQ-version
// UUID. We take the first 8 bytes of the canonical big-endian byte form
// and re-interpret them as a signed int64.
//
// Collision probability across the working set (likely O(10^3) live
// approval flows) is negligible (~10^-13 by the birthday bound on 2^63).
// If two unrelated keys ever collide the worst-case outcome is one
// caller observes a spurious lock.contended; the caller's retry path
// resolves it.
//
// Negative values are acceptable to postgres advisory locks — the API
// signature is `bigint`, signed.
func LockKeyForBOQ(boqID uuid.UUID) int64 {
	b := boqID[:8]
	return int64(binary.BigEndian.Uint64(b))
}

// LockKeyForApproval is the same derivation for an approval-instance id.
// The lock-namespace is shared (we don't xor with a discriminator)
// because the audit's Edge #2 scenario covers parallel decisions on the
// same approval row — two callers reaching the same approval id is the
// exact case we want to serialize.
func LockKeyForApproval(approvalID uuid.UUID) int64 {
	b := approvalID[:8]
	return int64(binary.BigEndian.Uint64(b))
}
