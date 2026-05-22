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
