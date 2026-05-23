package postgres

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/nocmon/domain"
	"github.com/ion-core/backend/internal/nocmon/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// FaultImpactRepository implements port.FaultImpactRepository against
// `nocmon.fault_impact_links`.
type FaultImpactRepository struct {
	pool *pgxpool.Pool
}

func NewFaultImpactRepository(pool *pgxpool.Pool) *FaultImpactRepository {
	return &FaultImpactRepository{pool: pool}
}

var _ port.FaultImpactRepository = (*FaultImpactRepository)(nil)

// Upsert is idempotent on (fault_event_id, customer_id) — re-running
// the cascade traversal (TC-FIA-001) updates the impact_kind /
// timestamps without spawning duplicates.
func (r *FaultImpactRepository) Upsert(ctx context.Context, i *domain.FaultImpact) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO nocmon.fault_impact_links
			(id, fault_event_id, customer_id, impact_kind,
			 impact_start, impact_end, sla_credit_eligible, notified_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (fault_event_id, customer_id) DO UPDATE
		    SET impact_kind = EXCLUDED.impact_kind,
		        impact_start = COALESCE(EXCLUDED.impact_start, nocmon.fault_impact_links.impact_start),
		        impact_end = COALESCE(EXCLUDED.impact_end, nocmon.fault_impact_links.impact_end),
		        sla_credit_eligible = EXCLUDED.sla_credit_eligible
	`,
		i.ID, i.FaultEventID, i.CustomerID, string(i.ImpactKind),
		i.ImpactStart, i.ImpactEnd, i.SLACreditEligible, i.NotifiedAt,
	)
	if err != nil {
		return mapDBError(err, "impact", "upsert fault_impact_link")
	}
	return nil
}

func (r *FaultImpactRepository) ListForFault(ctx context.Context, faultID uuid.UUID) ([]domain.FaultImpact, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, fault_event_id, customer_id, impact_kind,
		       impact_start, impact_end, sla_credit_eligible, notified_at
		FROM nocmon.fault_impact_links
		WHERE fault_event_id = $1
		ORDER BY impact_start ASC NULLS LAST
	`, faultID)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.impact_list", "list impact", err)
	}
	defer rows.Close()
	out := []domain.FaultImpact{}
	for rows.Next() {
		i, err := scanImpact(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, nil
}

func (r *FaultImpactRepository) CountForFault(ctx context.Context, faultID uuid.UUID) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM nocmon.fault_impact_links WHERE fault_event_id = $1
	`, faultID).Scan(&n)
	if err != nil {
		return 0, derrors.Wrap(derrors.KindInternal, "db.impact_count", "count impact", err)
	}
	return n, nil
}

func scanImpact(row pgx.Row) (domain.FaultImpact, error) {
	var i domain.FaultImpact
	var kind string
	err := row.Scan(
		&i.ID, &i.FaultEventID, &i.CustomerID, &kind,
		&i.ImpactStart, &i.ImpactEnd, &i.SLACreditEligible, &i.NotifiedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.FaultImpact{}, derrors.NotFound("impact.not_found", "impact not found")
	}
	if err != nil {
		return domain.FaultImpact{}, derrors.Wrap(derrors.KindInternal, "db.impact_scan", "scan impact", err)
	}
	i.ImpactKind = domain.ImpactKind(kind)
	return i, nil
}
