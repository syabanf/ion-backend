package postgres

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/warehouse/domain"
	"github.com/ion-core/backend/internal/warehouse/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type AlertRepository struct {
	pool *pgxpool.Pool
}

func NewAlertRepository(pool *pgxpool.Pool) *AlertRepository {
	return &AlertRepository{pool: pool}
}

var _ port.AlertRepository = (*AlertRepository)(nil)

// ListBelowThreshold returns all stock_levels where quantity < min_threshold
// (and threshold is set), joined with warehouse + branch metadata.
//
// When branchID is non-nil we constrain to warehouses whose branch_id
// matches OR descends from that branch via the parent_id chain. We use
// a recursive CTE to build the descendant set so a Regional manager
// sees alerts from every Area + Sub Area under their regional.
//
// We also compute escalation_path: the ordered list of branch IDs from
// the warehouse's branch up to its regional root. The handler exposes
// it so the UI can show "escalates to: Area X → Regional Y" badges.
func (r *AlertRepository) ListBelowThreshold(ctx context.Context, branchID *uuid.UUID) ([]domain.StockAlert, error) {
	// One query, three CTEs:
	//   filtered_branches — the user-scope branch + its descendants (or all branches if NULL)
	//   ancestors          — for every branch, the ordered chain of parent_ids up to root
	// We then join stock_levels → warehouses → filtered_branches → ancestors → stock_items.
	q := `
		WITH RECURSIVE filtered_branches AS (
		    SELECT id FROM identity.branches
		    WHERE $1::uuid IS NULL OR id = $1
		    UNION
		    SELECT b.id FROM identity.branches b
		    JOIN filtered_branches f ON b.parent_id = f.id
		),
		ancestors AS (
		    -- Anchor row per branch (depth 0 = self).
		    SELECT id AS branch_id, id AS ancestor_id, 0 AS depth
		    FROM identity.branches
		    UNION ALL
		    SELECT a.branch_id, b.parent_id, a.depth + 1
		    FROM ancestors a
		    JOIN identity.branches b ON b.id = a.ancestor_id
		    WHERE b.parent_id IS NOT NULL
		)
		SELECT
		    w.id, w.code, w.name,
		    w.branch_id,
		    COALESCE(b.code, ''), COALESCE(b.name, ''), COALESCE(b.level, ''),
		    si.id, si.sku, si.name, si.unit,
		    sl.quantity::float8, sl.min_threshold::float8,
		    (sl.min_threshold - sl.quantity)::float8 AS shortfall,
		    COALESCE(
		        (SELECT array_agg(a.ancestor_id ORDER BY a.depth)
		         FROM ancestors a WHERE a.branch_id = w.branch_id),
		        ARRAY[]::uuid[]
		    ) AS escalation_path
		FROM warehouse.stock_levels sl
		JOIN warehouse.warehouses  w  ON w.id = sl.warehouse_id
		LEFT JOIN identity.branches b ON b.id = w.branch_id
		JOIN warehouse.stock_items si ON si.id = sl.stock_item_id
		WHERE sl.min_threshold IS NOT NULL
		  AND sl.quantity < sl.min_threshold
		  AND ($1::uuid IS NULL OR w.branch_id IN (SELECT id FROM filtered_branches))
		ORDER BY shortfall DESC, w.name, si.sku
	`
	var branchArg any
	if branchID != nil {
		branchArg = *branchID
	}
	rows, err := r.pool.Query(ctx, q, branchArg)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.alerts_list", "list alerts", err)
	}
	defer rows.Close()

	out := []domain.StockAlert{}
	for rows.Next() {
		var a domain.StockAlert
		if err := rows.Scan(
			&a.WarehouseID, &a.WarehouseCode, &a.WarehouseName,
			&a.BranchID,
			&a.BranchCode, &a.BranchName, &a.BranchLevel,
			&a.StockItemID, &a.StockItemSKU, &a.StockItemName, &a.Unit,
			&a.Quantity, &a.MinThreshold, &a.Shortfall,
			&a.EscalationPath,
		); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "db.alerts_scan", "scan alert", err)
		}
		out = append(out, a)
	}
	return out, nil
}
