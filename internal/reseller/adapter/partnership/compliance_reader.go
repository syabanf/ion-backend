// Package partnership is the ONLY cross-context adapter in the
// reseller bounded context. It issues a read-only SQL query against
// the partnership schema's `compliance_evaluations` table to surface
// the latest compliance evaluation for the dashboard's compliance
// chip.
//
// Rules (load-bearing):
//   - Zero Go imports from internal/partnership/* — the cross-context
//     line is enforced at the import level. We talk to the partnership
//     schema, not the partnership package.
//   - Read-only. Never writes.
//   - Graceful degradation: if migration 0066 hasn't been applied (the
//     partnership schema/table doesn't exist), we return a typed
//     KindUnavailable error so the dashboard can show "unavailable"
//     instead of crashing.
//
// The reverse direction (partnership reading reseller) is forbidden;
// this adapter is the asymmetric exception, justified by the
// dashboard's read-only consumption pattern and the absence of a
// shared data model.
package partnership

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/reseller/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// ComplianceReader implements port.ComplianceReader against
// `partnership.compliance_evaluations`. It is constructed with the
// same pgxpool the reseller-svc already opens — no extra connection.
type ComplianceReader struct {
	pool *pgxpool.Pool
}

func NewComplianceReader(pool *pgxpool.Pool) *ComplianceReader {
	return &ComplianceReader{pool: pool}
}

var _ port.ComplianceReader = (*ComplianceReader)(nil)

// LatestForReseller returns the most-recent evaluation row for a
// reseller (ordered by evaluated_at DESC). Two non-success returns:
//   - NotFound: no row exists for this reseller yet (e.g. brand-new
//     account, monthly cron hasn't run, partnership migration applied
//     but no eval done).
//   - KindUnavailable: the partnership schema/table isn't reachable.
//     This happens in dev when migration 0066 hasn't been applied;
//     postgres returns SQLSTATE 42P01 (undefined_table) or 3F000
//     (invalid_schema_name). The dashboard catches this kind and
//     surfaces compliance_status="unavailable".
func (r *ComplianceReader) LatestForReseller(ctx context.Context, resellerID uuid.UUID) (*port.ComplianceSnapshot, error) {
	if resellerID == uuid.Nil {
		return nil, derrors.Validation("partnership.compliance.tenant_required", "reseller_account_id is required")
	}
	row := r.pool.QueryRow(ctx, `
		SELECT status, achieved_pct, threshold_pct, evaluated_at
		FROM partnership.compliance_evaluations
		WHERE reseller_account_id = $1
		ORDER BY evaluated_at DESC
		LIMIT 1
	`, resellerID)
	var snap port.ComplianceSnapshot
	var status string
	var achieved, threshold float64
	var evaluatedAt time.Time
	err := row.Scan(&status, &achieved, &threshold, &evaluatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, derrors.NotFound("partnership.compliance.no_evaluation", "no compliance evaluation yet")
	}
	if err != nil {
		// Map "table/schema doesn't exist" to KindUnavailable so the
		// dashboard can degrade gracefully without distinguishing
		// "schema missing" from "schema empty for this reseller".
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			switch pgErr.Code {
			case "42P01", // undefined_table
				"3F000", // invalid_schema_name
				"42704": // undefined_object — defensive
				return nil, derrors.Wrap(
					derrors.KindUnavailable,
					"partnership.compliance.not_available",
					"partnership tables not present",
					err,
				)
			}
		}
		// Some pgconn errors aren't *PgError (e.g. connection-level);
		// inspect the message as a defensive fallback so we don't
		// surface a 500 when the schema simply isn't loaded yet.
		if isMissingSchemaErr(err) {
			return nil, derrors.Wrap(
				derrors.KindUnavailable,
				"partnership.compliance.not_available",
				"partnership tables not present",
				err,
			)
		}
		return nil, derrors.Wrap(derrors.KindInternal, "db.partnership_compliance_read", "read compliance evaluation", err)
	}
	snap.Status = status
	snap.AchievedPct = achieved
	snap.ThresholdPct = threshold
	snap.EvaluatedAt = evaluatedAt
	return &snap, nil
}

// isMissingSchemaErr is the defensive string-sniff fallback for the
// rare driver error path where pgconn.PgError isn't wrapped. We keep
// this minimal — the typed code check above is the primary signal.
func isMissingSchemaErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "schema \"partnership\"") ||
		strings.Contains(msg, "relation \"partnership.compliance_evaluations\" does not exist")
}
