package postgres

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/warehouse/domain"
	"github.com/ion-core/backend/internal/warehouse/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type StockLevelRepository struct {
	pool *pgxpool.Pool
}

func NewStockLevelRepository(pool *pgxpool.Pool) *StockLevelRepository {
	return &StockLevelRepository{pool: pool}
}

var _ port.StockLevelRepository = (*StockLevelRepository)(nil)

func (r *StockLevelRepository) Get(ctx context.Context, warehouseID, itemID uuid.UUID) (*domain.StockLevel, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, warehouse_id, stock_item_id, quantity, min_threshold, updated_at
		FROM warehouse.stock_levels
		WHERE warehouse_id = $1 AND stock_item_id = $2
	`, warehouseID, itemID)
	var l domain.StockLevel
	err := row.Scan(&l.ID, &l.WarehouseID, &l.StockItemID, &l.Quantity, &l.MinThreshold, &l.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil // not an error — just no row yet
	}
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.level_get", "read stock level", err)
	}
	return &l, nil
}

// UpsertDelta increments (or decrements) the (warehouse, item) quantity.
// Used by intake (+meters/+count), transfer_out (negative), transfer_in,
// and opname_adjustment.
//
// We deliberately AVOID `INSERT ... ON CONFLICT DO UPDATE` here because
// Postgres evaluates CHECK constraints on the proposed INSERT row BEFORE
// running the conflict handler — so a delta of -100 against an existing
// row with quantity=500 would fail the `CHECK (quantity >= 0)` on the
// candidate row even though the resulting quantity (400) is legal.
//
// Instead: UPDATE first; INSERT only if no row exists AND the delta is
// non-negative. A negative delta against a non-existing row is an
// explicit "no_row" error — you can't decrement out of nothing.
//
// The CHECK still guards the UPDATE path against going below zero;
// callers should pre-validate sufficient stock to give nicer errors.
func (r *StockLevelRepository) UpsertDelta(ctx context.Context, warehouseID, itemID uuid.UUID, delta float64) (*domain.StockLevel, error) {
	row := r.pool.QueryRow(ctx, `
		WITH upd AS (
		  UPDATE warehouse.stock_levels
		  SET quantity = quantity + $3, updated_at = NOW()
		  WHERE warehouse_id = $1 AND stock_item_id = $2
		  RETURNING id, warehouse_id, stock_item_id, quantity, min_threshold, updated_at
		),
		ins AS (
		  INSERT INTO warehouse.stock_levels (warehouse_id, stock_item_id, quantity, updated_at)
		  SELECT $1, $2, $3, NOW()
		  WHERE NOT EXISTS (SELECT 1 FROM upd) AND $3 >= 0
		  RETURNING id, warehouse_id, stock_item_id, quantity, min_threshold, updated_at
		)
		SELECT id, warehouse_id, stock_item_id, quantity, min_threshold, updated_at FROM upd
		UNION ALL
		SELECT id, warehouse_id, stock_item_id, quantity, min_threshold, updated_at FROM ins
	`, warehouseID, itemID, delta)
	var l domain.StockLevel
	err := row.Scan(&l.ID, &l.WarehouseID, &l.StockItemID, &l.Quantity, &l.MinThreshold, &l.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, derrors.Validation("stock_level.no_row",
			"cannot apply negative delta — no existing stock_level row")
	}
	if err != nil {
		return nil, mapDBError(err, "stock_level.upsert", "upsert stock level")
	}
	return &l, nil
}
