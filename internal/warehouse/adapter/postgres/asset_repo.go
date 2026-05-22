package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/warehouse/domain"
	"github.com/ion-core/backend/internal/warehouse/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type AssetRepository struct {
	pool *pgxpool.Pool
}

func NewAssetRepository(pool *pgxpool.Pool) *AssetRepository {
	return &AssetRepository{pool: pool}
}

var _ port.AssetRepository = (*AssetRepository)(nil)

const assetSelect = `
SELECT id, stock_item_id, warehouse_id, COALESCE(serial_number,''), COALESCE(qr_code,''),
       COALESCE(mac_address,''), COALESCE(firmware_version,''),
       ownership_type, condition, status, received_at,
       purchase_cost, purchase_date, COALESCE(distributor,''), COALESCE(purchase_order_ref,''),
       warranty_expiry, is_retrofit, customer_id, assigned_technician_id, wo_id, network_node_id,
       COALESCE(notes,''), created_at, updated_at
FROM warehouse.assets
`

func (r *AssetRepository) List(ctx context.Context, f port.AssetListFilter) ([]domain.Asset, int, error) {
	conds := []string{"1=1"}
	args := []any{}
	idx := 1
	if f.WarehouseID != nil {
		conds = append(conds, fmt.Sprintf("warehouse_id = $%d", idx))
		args = append(args, *f.WarehouseID)
		idx++
	}
	if f.StockItemID != nil {
		conds = append(conds, fmt.Sprintf("stock_item_id = $%d", idx))
		args = append(args, *f.StockItemID)
		idx++
	}
	if f.Status != "" {
		conds = append(conds, fmt.Sprintf("status = $%d", idx))
		args = append(args, f.Status)
		idx++
	}
	if s := strings.TrimSpace(f.Search); s != "" {
		conds = append(conds, fmt.Sprintf("(serial_number ILIKE $%d OR qr_code ILIKE $%d)", idx, idx))
		args = append(args, "%"+s+"%")
		idx++
	}
	where := strings.Join(conds, " AND ")

	var total int
	if err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM warehouse.assets WHERE `+where, args...,
	).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.asset_count", "count assets", err)
	}
	if f.Limit <= 0 {
		f.Limit = 50
	}
	// Default sort = LIFO (newest received_at first). FIFO flips the
	// direction. Only the direction varies; the column itself is part
	// of the contract here so we don't take a parameter for it.
	sortDir := "DESC"
	if strings.EqualFold(f.OrderBy, "fifo") {
		sortDir = "ASC"
	}
	sql := assetSelect + ` WHERE ` + where +
		fmt.Sprintf(` ORDER BY received_at %s, created_at %s LIMIT $%d OFFSET $%d`,
			sortDir, sortDir, idx, idx+1)
	args = append(args, f.Limit, f.Offset)

	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.asset_list", "list assets", err)
	}
	defer rows.Close()
	out := []domain.Asset{}
	for rows.Next() {
		a, err := scanAsset(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *a)
	}
	return out, total, nil
}

func (r *AssetRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.Asset, error) {
	row := r.pool.QueryRow(ctx, assetSelect+` WHERE id = $1`, id)
	return scanAsset(row)
}

func (r *AssetRepository) Create(ctx context.Context, a *domain.Asset) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO warehouse.assets (
		  id, stock_item_id, warehouse_id, serial_number, qr_code, mac_address,
		  firmware_version, ownership_type, condition, status, received_at,
		  purchase_cost, purchase_date, distributor, purchase_order_ref, warranty_expiry,
		  is_retrofit, customer_id, assigned_technician_id, wo_id, network_node_id,
		  notes, created_at, updated_at
		) VALUES (
		  $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11,
		  $12, $13, $14, $15, $16, $17, $18, $19, $20, $21,
		  $22, $23, $24
		)
	`,
		a.ID, a.StockItemID, a.WarehouseID,
		nullableString(a.SerialNumber), nullableString(a.QRCode), nullableString(a.MACAddress),
		nullableString(a.FirmwareVersion), string(a.Ownership), string(a.Condition), string(a.Status),
		a.ReceivedAt, a.PurchaseCost, a.PurchaseDate, nullableString(a.Distributor),
		nullableString(a.PurchaseOrderRef), a.WarrantyExpiry, a.IsRetrofit,
		a.CustomerID, a.AssignedTechnicianID, a.WOID, a.NetworkNodeID,
		nullableString(a.Notes), a.CreatedAt, a.UpdatedAt,
	)
	return mapDBError(err, "asset.create", "create asset")
}

func (r *AssetRepository) UpdateStatus(ctx context.Context, id uuid.UUID, status domain.AssetStatus, warehouseID *uuid.UUID) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE warehouse.assets SET status = $2, warehouse_id = $3 WHERE id = $1`,
		id, string(status), warehouseID,
	)
	if err != nil {
		return mapDBError(err, "asset.update_status", "update status")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("asset.not_found", "asset not found")
	}
	return nil
}

func scanAsset(row pgx.Row) (*domain.Asset, error) {
	var (
		a         domain.Asset
		ownership string
		condition string
		status    string
	)
	err := row.Scan(
		&a.ID, &a.StockItemID, &a.WarehouseID, &a.SerialNumber, &a.QRCode,
		&a.MACAddress, &a.FirmwareVersion,
		&ownership, &condition, &status, &a.ReceivedAt,
		&a.PurchaseCost, &a.PurchaseDate, &a.Distributor, &a.PurchaseOrderRef,
		&a.WarrantyExpiry, &a.IsRetrofit, &a.CustomerID, &a.AssignedTechnicianID,
		&a.WOID, &a.NetworkNodeID, &a.Notes, &a.CreatedAt, &a.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, derrors.NotFound("asset.not_found", "asset not found")
	}
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.asset_scan", "scan asset", err)
	}
	a.Ownership = domain.Ownership(ownership)
	a.Condition = domain.Condition(condition)
	a.Status = domain.AssetStatus(status)
	return &a, nil
}
