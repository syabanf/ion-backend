// Wave 117 — Asset location history repository.
package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/warehouse/domain"
	"github.com/ion-core/backend/internal/warehouse/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type AssetLocationHistoryRepository struct {
	pool *pgxpool.Pool
}

func NewAssetLocationHistoryRepository(pool *pgxpool.Pool) *AssetLocationHistoryRepository {
	return &AssetLocationHistoryRepository{pool: pool}
}

var _ port.AssetLocationHistoryRepository = (*AssetLocationHistoryRepository)(nil)

const assetLocationCols = `id, asset_id, from_warehouse_id, to_warehouse_id,
	from_sub_warehouse_id, to_sub_warehouse_id, movement_kind, wo_id,
	customer_id, moved_by, moved_at, COALESCE(reason,''), COALESCE(location_label,'')`

// Record persists the audit row AND syncs the denormalized
// current_location_id + last_movement_at on warehouse.assets, in one tx.
func (r *AssetLocationHistoryRepository) Record(ctx context.Context, mv *domain.LocationMovement) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "db.asset_location_tx", "begin tx", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		INSERT INTO warehouse.asset_location_history
			(id, asset_id, from_warehouse_id, to_warehouse_id,
			 from_sub_warehouse_id, to_sub_warehouse_id, movement_kind, wo_id,
			 customer_id, moved_by, moved_at, reason, location_label)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
	`, mv.ID, mv.AssetID, mv.FromWarehouseID, mv.ToWarehouseID,
		mv.FromSubWarehouseID, mv.ToSubWarehouseID, string(mv.MovementKind), mv.WOID,
		mv.CustomerID, mv.MovedBy, mv.MovedAt, nullableString(mv.Reason), nullableString(mv.LocationLabel)); err != nil {
		return mapDBError(err, "asset_location.insert", "insert location history")
	}
	// Denormalized current location — preference: to_warehouse > to_sub.
	// When neither target is set (consume / decommission), we leave the
	// existing pointer + bump last_movement_at only.
	var locationID *uuid.UUID
	if mv.ToWarehouseID != nil {
		locationID = mv.ToWarehouseID
	} else if mv.ToSubWarehouseID != nil {
		locationID = mv.ToSubWarehouseID
	}
	if locationID != nil {
		if _, err := tx.Exec(ctx, `
			UPDATE warehouse.assets
			   SET current_location_id=$2, last_movement_at=$3, updated_at=NOW()
			 WHERE id=$1
		`, mv.AssetID, *locationID, mv.MovedAt); err != nil {
			return mapDBError(err, "asset_location.denorm", "update denorm location")
		}
	} else {
		if _, err := tx.Exec(ctx, `
			UPDATE warehouse.assets
			   SET last_movement_at=$2, updated_at=NOW()
			 WHERE id=$1
		`, mv.AssetID, mv.MovedAt); err != nil {
			return mapDBError(err, "asset_location.denorm_time", "update last movement")
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return derrors.Wrap(derrors.KindInternal, "db.asset_location_commit", "commit location", err)
	}
	return nil
}

func (r *AssetLocationHistoryRepository) ListForAsset(ctx context.Context, assetID uuid.UUID, limit, offset int) ([]domain.LocationMovement, int, error) {
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	var total int
	if err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM warehouse.asset_location_history WHERE asset_id=$1`,
		assetID).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.asset_location_count", "count location history", err)
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+assetLocationCols+` FROM warehouse.asset_location_history
		 WHERE asset_id=$1 ORDER BY moved_at DESC LIMIT $2 OFFSET $3
	`, assetID, limit, offset)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.asset_location_list", "list location history", err)
	}
	defer rows.Close()
	out := []domain.LocationMovement{}
	for rows.Next() {
		mv, err := scanLocationMovement(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *mv)
	}
	return out, total, nil
}

func (r *AssetLocationHistoryRepository) CurrentLocation(ctx context.Context, assetID uuid.UUID) (*uuid.UUID, *time.Time, error) {
	var loc *uuid.UUID
	var ts *time.Time
	err := r.pool.QueryRow(ctx, `
		SELECT current_location_id, last_movement_at
		  FROM warehouse.assets WHERE id=$1
	`, assetID).Scan(&loc, &ts)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, derrors.NotFound("asset.not_found", "asset not found")
	}
	if err != nil {
		return nil, nil, derrors.Wrap(derrors.KindInternal, "db.asset_current_location", "read current location", err)
	}
	return loc, ts, nil
}

func (r *AssetLocationHistoryRepository) ListInTransitOlderThan(ctx context.Context, threshold time.Duration) ([]domain.LocationMovement, error) {
	cutoff := time.Now().UTC().Add(-threshold)
	// Latest movement per asset; flagged when the latest is in_transit and older than cutoff.
	rows, err := r.pool.Query(ctx, `
		WITH latest AS (
		    SELECT DISTINCT ON (asset_id) `+assetLocationCols+`
		      FROM warehouse.asset_location_history
		     ORDER BY asset_id, moved_at DESC
		)
		SELECT * FROM latest
		 WHERE movement_kind='in_transit' AND moved_at < $1
		 ORDER BY moved_at
	`, cutoff)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.asset_in_transit", "list in_transit", err)
	}
	defer rows.Close()
	out := []domain.LocationMovement{}
	for rows.Next() {
		mv, err := scanLocationMovement(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *mv)
	}
	return out, nil
}

func scanLocationMovement(row pgx.Row) (*domain.LocationMovement, error) {
	var mv domain.LocationMovement
	var kind string
	err := row.Scan(&mv.ID, &mv.AssetID, &mv.FromWarehouseID, &mv.ToWarehouseID,
		&mv.FromSubWarehouseID, &mv.ToSubWarehouseID, &kind, &mv.WOID,
		&mv.CustomerID, &mv.MovedBy, &mv.MovedAt, &mv.Reason, &mv.LocationLabel)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, derrors.NotFound("asset_location.not_found", "location movement not found")
	}
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.asset_location_scan", "scan location movement", err)
	}
	mv.MovementKind = domain.MovementKind(kind)
	return &mv, nil
}
