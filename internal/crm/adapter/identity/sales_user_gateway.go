// Package identity adapts the identity service / DB to CRM's
// SalesUserGateway port. Round-2 reads identity.sales_rep_profiles
// directly via the shared pool. When identity moves to its own
// process, swap this for an HTTP client to /api/identity/users/{id}.
package identity

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/crm/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type SalesUserGateway struct {
	pool *pgxpool.Pool
}

func NewSalesUserGateway(pool *pgxpool.Pool) *SalesUserGateway {
	return &SalesUserGateway{pool: pool}
}

var _ port.SalesUserGateway = (*SalesUserGateway)(nil)

func (g *SalesUserGateway) SalesTypeFor(ctx context.Context, userID uuid.UUID) (string, error) {
	var stype string
	err := g.pool.QueryRow(ctx,
		`SELECT sales_type FROM identity.sales_rep_profiles WHERE user_id = $1`,
		userID,
	).Scan(&stype)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", derrors.NotFound("sales_user.not_found",
			"user is not registered as a sales rep")
	}
	if err != nil {
		return "", derrors.Wrap(derrors.KindInternal, "db.sales_type", "read sales_type", err)
	}
	return stype, nil
}

// FindForTerritory picks a sales rep for territory-based auto-assign
// (Wave 81 / TC-CRM-011). Match rule: active rep whose user.branch_id
// matches the lead's resolved branch AND whose sales_type is 'both' or
// matches leadType.
//
// Selection is least-loaded by lead count over the last 30 days. The
// LEFT JOIN + COUNT keeps the query single-statement; ties resolve by
// user_id so the same lead would always pick the same rep on retry.
// Returns (nil, nil) when no rep matches — caller falls through to the
// unassigned queue.
func (g *SalesUserGateway) FindForTerritory(
	ctx context.Context, branchID uuid.UUID, leadType string,
) (*uuid.UUID, error) {
	// Empty leadType defaults to broadband — the broadband pipeline is
	// the default for legacy callers that haven't been updated to pass
	// LeadType through yet.
	if leadType == "" {
		leadType = "broadband"
	}
	var picked uuid.UUID
	err := g.pool.QueryRow(ctx, `
		SELECT u.id
		FROM identity.users u
		JOIN identity.sales_rep_profiles srp ON srp.user_id = u.id
		LEFT JOIN crm.leads l
			ON l.sales_id = u.id
			AND l.created_at > NOW() - INTERVAL '30 days'
			AND l.status NOT IN ('converted','lost','rejected')
		WHERE u.branch_id = $1
		  AND u.is_active = TRUE
		  AND (srp.sales_type = $2 OR srp.sales_type = 'both')
		GROUP BY u.id
		ORDER BY COUNT(l.id) ASC, u.id ASC
		LIMIT 1
	`, branchID, leadType).Scan(&picked)
	if errors.Is(err, pgx.ErrNoRows) {
		// No matching rep — caller leaves SalesID nil and the lead
		// shows up in the unassigned queue for a sales manager to
		// triage manually.
		return nil, nil
	}
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal,
			"db.sales_territory_lookup", "find territory sales rep", err)
	}
	return &picked, nil
}
