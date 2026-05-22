package postgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/warehouse/domain"
	"github.com/ion-core/backend/internal/warehouse/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type InventoryRepository struct {
	pool *pgxpool.Pool
}

func NewInventoryRepository(pool *pgxpool.Pool) *InventoryRepository {
	return &InventoryRepository{pool: pool}
}

var _ port.InventoryRepository = (*InventoryRepository)(nil)

// Inventory returns one row per stock_item, computing the right quantity
// depending on category:
//
//   serialized → COUNT(*) from warehouse.assets WHERE status='in_stock'
//   non-serialized → stock_levels.quantity (defaulting to 0)
//
// Single query via CASE / sub-select per row. Performant enough for
// hundreds of items per warehouse — beyond that we'd materialize a view.
func (r *InventoryRepository) Inventory(ctx context.Context, f port.InventoryFilter) ([]port.InventoryRow, int, error) {
	conds := []string{}
	args := []any{f.WarehouseID}
	idx := 2
	conds = append(conds, "si.active = TRUE")

	if f.Category != "" {
		conds = append(conds, fmt.Sprintf("si.category = $%d", idx))
		args = append(args, f.Category)
		idx++
	}
	if s := strings.TrimSpace(f.Search); s != "" {
		conds = append(conds, fmt.Sprintf("(si.name ILIKE $%d OR si.sku ILIKE $%d)", idx, idx))
		args = append(args, "%"+s+"%")
		idx++ //nolint:ineffassign // defensive — preserves correctness if a third condition is added below
	}
	_ = idx // explicitly acknowledge the trailing increment
	where := strings.Join(conds, " AND ")

	// FIFO/LIFO sort: secondary ordering by last_movement_at within
	// each (category, name) so operators see the oldest stock first
	// under FIFO (the natural "draw this next" order) and the freshest
	// stock first under LIFO. NULL last_movement_at means the item has
	// never moved — push those to the end regardless of direction.
	sortDir := "DESC"
	if strings.EqualFold(f.OrderBy, "fifo") {
		sortDir = "ASC"
	}

	// quantity = COUNT(*) if serialized, else COALESCE(sl.quantity, 0)
	// last_movement_at = MAX(performed_at) for any movement at this (wh,item)
	listSQL := fmt.Sprintf(`
		SELECT
		  si.id, si.sku, si.name, si.category, COALESCE(si.brand,''), COALESCE(si.model,''),
		  COALESCE(si.spec,''), si.unit, si.serialized, si.default_unit_cost,
		  si.active, si.metadata, si.created_at, si.updated_at,
		  CASE
		    WHEN si.serialized THEN
		      (SELECT COUNT(*)::numeric FROM warehouse.assets a
		        WHERE a.warehouse_id = $1 AND a.stock_item_id = si.id AND a.status = 'in_stock')
		    ELSE
		      COALESCE(sl.quantity, 0)
		  END AS qty,
		  sl.min_threshold,
		  (SELECT MAX(performed_at) FROM warehouse.stock_movements m
		    WHERE m.warehouse_id = $1 AND m.stock_item_id = si.id) AS last_movement_at
		FROM warehouse.stock_items si
		LEFT JOIN warehouse.stock_levels sl
		       ON sl.warehouse_id = $1 AND sl.stock_item_id = si.id
		WHERE %s
		ORDER BY si.category,
		         last_movement_at %s NULLS LAST,
		         si.name
	`, where, sortDir)

	rows, err := r.pool.Query(ctx, listSQL, args...)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.inventory", "list inventory", err)
	}
	defer rows.Close()

	out := []port.InventoryRow{}
	for rows.Next() {
		var (
			it       domain.StockItem
			category string
			unit     string
			meta     []byte
			row      port.InventoryRow
		)
		if err := rows.Scan(
			&it.ID, &it.SKU, &it.Name, &category, &it.Brand, &it.Model, &it.Spec,
			&unit, &it.Serialized, &it.DefaultUnitCost, &it.Active, &meta,
			&it.CreatedAt, &it.UpdatedAt,
			&row.Quantity, &row.MinThreshold, &row.LastMovementAt,
		); err != nil {
			return nil, 0, derrors.Wrap(derrors.KindInternal, "db.inventory_scan", "scan", err)
		}
		it.Category = domain.ItemCategory(category)
		it.Unit = domain.Unit(unit)
		row.StockItem = it
		row.BelowThreshold = row.MinThreshold != nil && row.Quantity < *row.MinThreshold

		if f.BelowOnly && !row.BelowThreshold {
			continue
		}
		out = append(out, row)
	}

	// total = post-filter row count when BelowOnly active; otherwise equal
	// to len(out). Lists are small at this scale; full count is fine.
	return out, len(out), nil
}
