// Wave 87 (Tier 3) — postgres adapter for asset retrofit.
//
// One tx, four writes:
//  1. UPDATE source asset → status='cannibalized'
//  2. INSERT produced asset (full row, is_retrofit=true)
//  3. INSERT both stock_movements (consume + produce)
//  4. INSERT audit log row in asset_retrofits
//
// All-or-nothing so a partial retrofit can't leave a cannibalized
// source without the produced row, or vice versa.
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

type AssetRetrofitRepository struct {
	pool *pgxpool.Pool
}

func NewAssetRetrofitRepository(pool *pgxpool.Pool) *AssetRetrofitRepository {
	return &AssetRetrofitRepository{pool: pool}
}

var _ port.AssetRetrofitRepository = (*AssetRetrofitRepository)(nil)

func (r *AssetRetrofitRepository) RecordRetrofit(
	ctx context.Context, in port.RecordRetrofitPersist,
) (*port.RetrofitResult, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "retrofit.tx_begin",
			"begin retrofit tx", err)
	}
	defer tx.Rollback(ctx)

	// 1) Flip source asset to cannibalized. The WHERE clause includes
	// the current status guard so a concurrent retrofit can't silently
	// re-cannibalize an already-cannibalized row.
	tag, err := tx.Exec(ctx, `
		UPDATE warehouse.assets
		   SET status = 'cannibalized',
		       updated_at = NOW()
		 WHERE id = $1
		   AND status != 'cannibalized'
	`, in.Retrofit.SourceAssetID)
	if err != nil {
		return nil, mapDBError(err, "retrofit.source", "cannibalize source asset")
	}
	if tag.RowsAffected() == 0 {
		return nil, derrors.Conflict("retrofit.source_invalid",
			"source asset is missing or already cannibalized")
	}

	// 2) Produced asset row.
	a := in.ProducedAsset
	if _, err := tx.Exec(ctx, `
		INSERT INTO warehouse.assets (
		  id, stock_item_id, warehouse_id, serial_number, qr_code, mac_address,
		  firmware_version, ownership_type, condition, status, received_at,
		  purchase_cost, purchase_date, distributor, purchase_order_ref, warranty_expiry,
		  is_retrofit, customer_id, assigned_technician_id, wo_id, network_node_id,
		  notes, created_at, updated_at, purchase_order_id
		) VALUES (
		  $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,
		  $12,$13,$14,$15,$16,TRUE,$17,$18,$19,$20,
		  $21,$22,$22,$23
		)
	`,
		a.ID, a.StockItemID, a.WarehouseID,
		nullableString(a.SerialNumber), nullableString(a.QRCode), nullableString(a.MACAddress),
		nullableString(a.FirmwareVersion), string(a.Ownership), string(a.Condition), string(a.Status),
		a.ReceivedAt, a.PurchaseCost, a.PurchaseDate, nullableString(a.Distributor),
		nullableString(a.PurchaseOrderRef), a.WarrantyExpiry,
		a.CustomerID, a.AssignedTechnicianID, a.WOID, a.NetworkNodeID,
		nullableString(a.Notes), a.CreatedAt, a.PurchaseOrderID,
	); err != nil {
		return nil, mapDBError(err, "retrofit.produced", "create produced asset")
	}

	// 3) Both stock movement rows. We grab the inserted ids back so
	//    the audit log can FK-link to them.
	consumeID := uuid.New()
	produceID := uuid.New()
	cm := in.ConsumeMovement
	if _, err := tx.Exec(ctx, `
		INSERT INTO warehouse.stock_movements (
			id, warehouse_id, stock_item_id, asset_id, movement_type,
			quantity, reason, reference_type, reference_id,
			performed_by, performed_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
	`,
		consumeID, cm.WarehouseID, cm.StockItemID, cm.AssetID,
		string(cm.MovementType), cm.Quantity,
		nullableString(cm.Reason), nullableString(cm.ReferenceType),
		cm.ReferenceID, cm.PerformedBy, cm.PerformedAt,
	); err != nil {
		return nil, mapDBError(err, "retrofit.consume_mov",
			"record retrofit_consume movement")
	}
	pm := in.ProduceMovement
	if _, err := tx.Exec(ctx, `
		INSERT INTO warehouse.stock_movements (
			id, warehouse_id, stock_item_id, asset_id, movement_type,
			quantity, reason, reference_type, reference_id,
			performed_by, performed_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
	`,
		produceID, pm.WarehouseID, pm.StockItemID, pm.AssetID,
		string(pm.MovementType), pm.Quantity,
		nullableString(pm.Reason), nullableString(pm.ReferenceType),
		pm.ReferenceID, pm.PerformedBy, pm.PerformedAt,
	); err != nil {
		return nil, mapDBError(err, "retrofit.produce_mov",
			"record retrofit_produce movement")
	}

	// 4) Audit log row linking source ↔ produced ↔ both movements.
	rec := in.Retrofit
	if _, err := tx.Exec(ctx, `
		INSERT INTO warehouse.asset_retrofits (
			id, source_asset_id, produced_asset_id, reason,
			performed_by, performed_at,
			consume_movement_id, produce_movement_id
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
	`,
		rec.ID, rec.SourceAssetID, rec.ProducedAssetID, rec.Reason,
		rec.PerformedBy, rec.PerformedAt,
		consumeID, produceID,
	); err != nil {
		return nil, mapDBError(err, "retrofit.log",
			"write retrofit audit row")
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "retrofit.tx_commit",
			"commit retrofit tx", err)
	}
	rec.ConsumeMovementID = &consumeID
	rec.ProduceMovementID = &produceID
	return &port.RetrofitResult{
		Retrofit:      rec,
		ProducedAsset: in.ProducedAsset,
	}, nil
}

func (r *AssetRetrofitRepository) ListForSource(
	ctx context.Context, sourceAssetID uuid.UUID,
) ([]domain.AssetRetrofit, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, source_asset_id, produced_asset_id, reason,
		       performed_by, performed_at,
		       consume_movement_id, produce_movement_id
		FROM warehouse.asset_retrofits
		WHERE source_asset_id = $1
		ORDER BY performed_at DESC
	`, sourceAssetID)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "retrofit.list",
			"list retrofits for source", err)
	}
	defer rows.Close()
	out := []domain.AssetRetrofit{}
	for rows.Next() {
		var r domain.AssetRetrofit
		if err := rows.Scan(&r.ID, &r.SourceAssetID, &r.ProducedAssetID, &r.Reason,
			&r.PerformedBy, &r.PerformedAt,
			&r.ConsumeMovementID, &r.ProduceMovementID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return out, nil
			}
			return nil, derrors.Wrap(derrors.KindInternal, "retrofit.scan",
				"scan retrofit row", err)
		}
		out = append(out, r)
	}
	return out, nil
}
