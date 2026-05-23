package postgres

import (
	"context"
	"time"

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
	// Wave 88 — LEFT JOIN warehouse.stock_alert_states surfaces
	// open_since + current_level when present. NULL columns mean the
	// cron hasn't synced yet; the frontend treats that as "newly
	// opened" + sub_area level.
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
		    ) AS escalation_path,
		    sas.open_since,
		    sas.current_level,
		    sas.last_escalated_at
		FROM warehouse.stock_levels sl
		JOIN warehouse.warehouses  w  ON w.id = sl.warehouse_id
		LEFT JOIN identity.branches b ON b.id = w.branch_id
		JOIN warehouse.stock_items si ON si.id = sl.stock_item_id
		LEFT JOIN warehouse.stock_alert_states sas
		    ON sas.warehouse_id = sl.warehouse_id
		   AND sas.stock_item_id = sl.stock_item_id
		   AND sas.closed_at IS NULL
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
		var (
			a           domain.StockAlert
			levelStr    *string
		)
		if err := rows.Scan(
			&a.WarehouseID, &a.WarehouseCode, &a.WarehouseName,
			&a.BranchID,
			&a.BranchCode, &a.BranchName, &a.BranchLevel,
			&a.StockItemID, &a.StockItemSKU, &a.StockItemName, &a.Unit,
			&a.Quantity, &a.MinThreshold, &a.Shortfall,
			&a.EscalationPath,
			&a.OpenSince, &levelStr, &a.LastEscalatedAt,
		); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "db.alerts_scan", "scan alert", err)
		}
		if levelStr != nil {
			a.CurrentLevel = domain.AlertLevel(*levelStr)
		}
		out = append(out, a)
	}
	return out, nil
}

// SyncAlertStates is the cron-callable upsert path (Wave 88). For
// each warehouse/item below threshold, insert a state row if missing
// (open_since=now, current_level=sub_area). For each open state row
// whose stock_level is now AT or above threshold, mark closed_at=now.
//
// Designed to be idempotent + cheap to call hourly. The cascade tick
// (bumping current_level when a budget expires) lives in a separate
// method to keep responsibilities single-shot per call.
func (r *AlertRepository) SyncAlertStates(ctx context.Context) (opened, closed int, err error) {
	// Open: rows below threshold but no open state.
	openTag, err := r.pool.Exec(ctx, `
		INSERT INTO warehouse.stock_alert_states (warehouse_id, stock_item_id, open_since, current_level, last_escalated_at)
		SELECT sl.warehouse_id, sl.stock_item_id, NOW(), 'sub_area', NOW()
		FROM warehouse.stock_levels sl
		WHERE sl.min_threshold IS NOT NULL
		  AND sl.quantity < sl.min_threshold
		  AND NOT EXISTS (
		      SELECT 1 FROM warehouse.stock_alert_states s
		      WHERE s.warehouse_id  = sl.warehouse_id
		        AND s.stock_item_id = sl.stock_item_id
		        AND s.closed_at IS NULL
		  )
	`)
	if err != nil {
		return 0, 0, derrors.Wrap(derrors.KindInternal,
			"alert_state.open", "upsert open alert states", err)
	}
	// Close: open state rows whose level is no longer below threshold.
	closeTag, err := r.pool.Exec(ctx, `
		UPDATE warehouse.stock_alert_states s
		   SET closed_at = NOW()
		 WHERE s.closed_at IS NULL
		   AND NOT EXISTS (
		       SELECT 1 FROM warehouse.stock_levels sl
		       WHERE sl.warehouse_id  = s.warehouse_id
		         AND sl.stock_item_id = s.stock_item_id
		         AND sl.min_threshold IS NOT NULL
		         AND sl.quantity < sl.min_threshold
		   )
	`)
	if err != nil {
		return 0, 0, derrors.Wrap(derrors.KindInternal,
			"alert_state.close", "close recovered alert states", err)
	}
	return int(openTag.RowsAffected()), int(closeTag.RowsAffected()), nil
}

// CascadeEscalations bumps open alert states up the branch chain
// when the time budget at the current level expires. Defaults:
//
//	sub_area  → area      after 24h
//	area      → regional  after 24h
//
// Returns the number of rows bumped. The caller schedules this from
// a periodic cron (hourly is fine — bumps are idempotent because the
// WHERE clause checks last_escalated_at).
func (r *AlertRepository) CascadeEscalations(ctx context.Context, subToArea, areaToRegional time.Duration) (int, error) {
	now := time.Now().UTC()
	bumps := 0
	subDeadline := now.Add(-subToArea)
	tag, err := r.pool.Exec(ctx, `
		UPDATE warehouse.stock_alert_states
		   SET current_level     = 'area',
		       last_escalated_at = NOW()
		 WHERE closed_at IS NULL
		   AND current_level = 'sub_area'
		   AND last_escalated_at <= $1
	`, subDeadline)
	if err != nil {
		return 0, derrors.Wrap(derrors.KindInternal,
			"alert_state.cascade_sub", "cascade sub_area → area", err)
	}
	bumps += int(tag.RowsAffected())
	areaDeadline := now.Add(-areaToRegional)
	tag, err = r.pool.Exec(ctx, `
		UPDATE warehouse.stock_alert_states
		   SET current_level     = 'regional',
		       last_escalated_at = NOW()
		 WHERE closed_at IS NULL
		   AND current_level = 'area'
		   AND last_escalated_at <= $1
	`, areaDeadline)
	if err != nil {
		return 0, derrors.Wrap(derrors.KindInternal,
			"alert_state.cascade_area", "cascade area → regional", err)
	}
	bumps += int(tag.RowsAffected())
	return bumps, nil
}
