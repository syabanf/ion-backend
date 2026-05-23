// Package crm adapts the platform's CustomerLockReader port to the
// crm.customers table. Wave 82 Tier 2c — the platform resolver auto-
// loads a customer's locked_<kind>_schema_version_id whenever the
// caller didn't pass an explicit LockedVersionID, closing the loop
// from Wave 80b's snapshot writer.
//
// Cross-context FYI: this reader lives under platform/ because the
// platform service consumes it, but it reads from crm.customers. The
// pool is shared today (single DB); when crm splits out, swap this
// adapter for an HTTP client to a /api/crm/customers/{id}/locks
// endpoint with identical semantics.
package crm

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/platform/domain"
	"github.com/ion-core/backend/internal/platform/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type LockReader struct {
	pool *pgxpool.Pool
}

func NewLockReader(pool *pgxpool.Pool) *LockReader {
	return &LockReader{pool: pool}
}

var _ port.CustomerLockReader = (*LockReader)(nil)

// LockedVersionFor returns crm.customers.locked_<kind>_schema_version_id
// for a customer + kind. Returns (nil, nil) for:
//   - unknown customer (a freshly-resolved schema for a not-yet-
//     persisted lead/customer is a legitimate non-error case)
//   - column is NULL (legacy customers from before Wave 78 / 80b)
//   - kind that doesn't have a lock column (onboarding lives in
//     crm.onboarding_schemas; treated as "no lock" here)
func (r *LockReader) LockedVersionFor(
	ctx context.Context, customerID uuid.UUID, kind domain.SchemaKind,
) (*uuid.UUID, error) {
	col := lockColumnFor(kind)
	if col == "" {
		return nil, nil
	}
	// Query is parameterized except for the column name, which comes
	// from a closed enum (we constructed it via lockColumnFor) — safe
	// from injection.
	q := "SELECT " + col + " FROM crm.customers WHERE id = $1"
	var locked *uuid.UUID
	if err := r.pool.QueryRow(ctx, q, customerID).Scan(&locked); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, derrors.Wrap(derrors.KindInternal,
			"customer_lock.read", "read customer schema lock", err)
	}
	return locked, nil
}

// lockColumnFor maps a SchemaKind to its lock column on crm.customers.
// Returns "" for kinds without a lock column (onboarding).
func lockColumnFor(kind domain.SchemaKind) string {
	switch kind {
	case domain.SchemaKindBilling:
		return "locked_billing_schema_version_id"
	case domain.SchemaKindService:
		return "locked_service_schema_version_id"
	case domain.SchemaKindCommission:
		return "locked_commission_schema_version_id"
	case domain.SchemaKindSuspension:
		return "locked_suspension_schema_version_id"
	}
	return ""
}
