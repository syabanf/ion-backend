package postgres

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/warehouse/port"
)

type ThresholdRepository struct {
	pool *pgxpool.Pool
}

func NewThresholdRepository(pool *pgxpool.Pool) *ThresholdRepository {
	return &ThresholdRepository{pool: pool}
}

var _ port.ThresholdRepository = (*ThresholdRepository)(nil)

// Set upserts (warehouse, item) with the given min_threshold.
//
// We need INSERT...ON CONFLICT here, but the same CHECK trap as the
// CTE pattern in stock_level_repo doesn't apply: threshold is nullable
// and quantity defaults to 0. A vanilla upsert is fine.
func (r *ThresholdRepository) Set(ctx context.Context, warehouseID, itemID uuid.UUID, threshold *float64) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO warehouse.stock_levels (warehouse_id, stock_item_id, quantity, min_threshold, updated_at)
		VALUES ($1, $2, 0, $3, NOW())
		ON CONFLICT (warehouse_id, stock_item_id) DO UPDATE
		   SET min_threshold = EXCLUDED.min_threshold, updated_at = NOW()
	`, warehouseID, itemID, threshold)
	return mapDBError(err, "stock_level.threshold_set", "set threshold")
}
