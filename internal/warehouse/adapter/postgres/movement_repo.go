package postgres

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/warehouse/domain"
	"github.com/ion-core/backend/internal/warehouse/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type MovementRepository struct {
	pool *pgxpool.Pool
}

func NewMovementRepository(pool *pgxpool.Pool) *MovementRepository {
	return &MovementRepository{pool: pool}
}

var _ port.MovementRepository = (*MovementRepository)(nil)

func (r *MovementRepository) Record(ctx context.Context, m *domain.StockMovement) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO warehouse.stock_movements
			(warehouse_id, stock_item_id, asset_id, movement_type, quantity,
			 reason, reference_type, reference_id, performed_by, performed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`,
		m.WarehouseID, m.StockItemID, m.AssetID, string(m.MovementType), m.Quantity,
		nullableString(m.Reason), nullableString(m.ReferenceType), m.ReferenceID,
		m.PerformedBy, m.PerformedAt,
	)
	return mapDBError(err, "movement.record", "record movement")
}

func (r *MovementRepository) List(ctx context.Context, warehouseID uuid.UUID, limit, offset int) ([]domain.StockMovement, int, error) {
	if limit <= 0 {
		limit = 50
	}
	var total int
	if err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM warehouse.stock_movements WHERE warehouse_id = $1`,
		warehouseID,
	).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.move_count", "count movements", err)
	}

	rows, err := r.pool.Query(ctx, `
		SELECT id, warehouse_id, stock_item_id, asset_id, movement_type, quantity,
		       COALESCE(reason,''), COALESCE(reference_type,''), reference_id,
		       performed_by, performed_at
		FROM warehouse.stock_movements
		WHERE warehouse_id = $1
		ORDER BY performed_at DESC
		LIMIT $2 OFFSET $3
	`, warehouseID, limit, offset)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.move_list", "list movements", err)
	}
	defer rows.Close()

	out := []domain.StockMovement{}
	for rows.Next() {
		var (
			m  domain.StockMovement
			mt string
		)
		if err := rows.Scan(&m.ID, &m.WarehouseID, &m.StockItemID, &m.AssetID, &mt, &m.Quantity,
			&m.Reason, &m.ReferenceType, &m.ReferenceID, &m.PerformedBy, &m.PerformedAt); err != nil {
			return nil, 0, derrors.Wrap(derrors.KindInternal, "db.move_scan", "scan movement", err)
		}
		m.MovementType = domain.MovementType(mt)
		out = append(out, m)
	}
	return out, total, nil
}
