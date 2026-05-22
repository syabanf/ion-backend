package postgres

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/field/domain"
	"github.com/ion-core/backend/internal/field/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type ResolutionRepository struct {
	pool *pgxpool.Pool
}

func NewResolutionRepository(pool *pgxpool.Pool) *ResolutionRepository {
	return &ResolutionRepository{pool: pool}
}

var _ port.ResolutionRepository = (*ResolutionRepository)(nil)

func (r *ResolutionRepository) Add(ctx context.Context, ri *domain.ResolutionItem) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO field.wo_resolution_items (
			id, wo_id, item_order, item_label, category,
			finding, action_taken, resolution_status,
			time_spent_minutes, resolved_by, logged_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
	`,
		ri.ID, ri.WOID, ri.ItemOrder, ri.ItemLabel, string(ri.Category),
		nullableString(ri.Finding), nullableString(ri.ActionTaken),
		string(ri.ResolutionStatus),
		ri.TimeSpentMinutes, ri.ResolvedBy, ri.LoggedAt,
	)
	return mapDBError(err, "resolution.add", "add resolution item")
}

func (r *ResolutionRepository) ListForWO(ctx context.Context, woID uuid.UUID) ([]domain.ResolutionItem, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, wo_id, item_order, item_label, COALESCE(category,'other'),
		       COALESCE(finding,''), COALESCE(action_taken,''), resolution_status,
		       time_spent_minutes, resolved_by, logged_at
		FROM field.wo_resolution_items
		WHERE wo_id = $1
		ORDER BY item_order, logged_at
	`, woID)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.res_list", "list resolutions", err)
	}
	defer rows.Close()
	out := []domain.ResolutionItem{}
	for rows.Next() {
		var (
			ri    domain.ResolutionItem
			cat   string
			rstat string
		)
		if err := rows.Scan(&ri.ID, &ri.WOID, &ri.ItemOrder, &ri.ItemLabel,
			&cat, &ri.Finding, &ri.ActionTaken, &rstat,
			&ri.TimeSpentMinutes, &ri.ResolvedBy, &ri.LoggedAt); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "db.res_scan", "scan resolution", err)
		}
		ri.Category = domain.ResolutionCategory(cat)
		ri.ResolutionStatus = domain.ResolutionStatus(rstat)
		out = append(out, ri)
	}
	return out, nil
}
