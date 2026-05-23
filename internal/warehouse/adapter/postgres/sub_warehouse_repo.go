// Wave 117 — Sub-warehouse repository.
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

type SubWarehouseRepository struct {
	pool *pgxpool.Pool
}

func NewSubWarehouseRepository(pool *pgxpool.Pool) *SubWarehouseRepository {
	return &SubWarehouseRepository{pool: pool}
}

var _ port.SubWarehouseRepository = (*SubWarehouseRepository)(nil)

const subWarehouseCols = `id, parent_warehouse_id, name, code, owner_user_id,
	owner_role, is_mobile, COALESCE(vehicle_id,''), can_purchase, active,
	created_at, updated_at`

func (r *SubWarehouseRepository) Create(ctx context.Context, s *domain.SubWarehouse) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO warehouse.sub_warehouses
			(id, parent_warehouse_id, name, code, owner_user_id, owner_role,
			 is_mobile, vehicle_id, can_purchase, active, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
	`, s.ID, s.ParentWarehouseID, s.Name, s.Code, s.OwnerUserID, string(s.OwnerRole),
		s.IsMobile, nullableString(s.VehicleID), s.CanPurchase, s.Active,
		s.CreatedAt, s.UpdatedAt)
	return mapDBError(err, "sub_warehouse.create", "create sub-warehouse")
}

func (r *SubWarehouseRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.SubWarehouse, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+subWarehouseCols+` FROM warehouse.sub_warehouses WHERE id=$1`, id)
	return scanSubWarehouse(row)
}

func (r *SubWarehouseRepository) List(ctx context.Context, f port.SubWarehouseListFilter) ([]domain.SubWarehouse, error) {
	var wh []string
	var args []any
	if f.ParentWarehouseID != nil {
		args = append(args, *f.ParentWarehouseID)
		wh = append(wh, fmt.Sprintf("parent_warehouse_id=$%d", len(args)))
	}
	if f.OwnerUserID != nil {
		args = append(args, *f.OwnerUserID)
		wh = append(wh, fmt.Sprintf("owner_user_id=$%d", len(args)))
	}
	if f.ActiveOnly {
		wh = append(wh, "active=TRUE")
	}
	where := ""
	if len(wh) > 0 {
		where = " WHERE " + strings.Join(wh, " AND ")
	}
	rows, err := r.pool.Query(ctx,
		`SELECT `+subWarehouseCols+` FROM warehouse.sub_warehouses`+where+` ORDER BY name`, args...)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.sub_warehouse_list", "list sub-warehouses", err)
	}
	defer rows.Close()
	out := []domain.SubWarehouse{}
	for rows.Next() {
		s, err := scanSubWarehouse(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, nil
}

func (r *SubWarehouseRepository) Update(ctx context.Context, s *domain.SubWarehouse) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE warehouse.sub_warehouses
		   SET name=$2, vehicle_id=$3, is_mobile=$4, can_purchase=$5,
		       active=$6, owner_role=$7, updated_at=NOW()
		 WHERE id=$1
	`, s.ID, s.Name, nullableString(s.VehicleID), s.IsMobile,
		s.CanPurchase, s.Active, string(s.OwnerRole))
	if err != nil {
		return mapDBError(err, "sub_warehouse.update", "update sub-warehouse")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("sub_warehouse.not_found", "sub-warehouse not found")
	}
	return nil
}

func scanSubWarehouse(row pgx.Row) (*domain.SubWarehouse, error) {
	var s domain.SubWarehouse
	var role string
	err := row.Scan(&s.ID, &s.ParentWarehouseID, &s.Name, &s.Code, &s.OwnerUserID,
		&role, &s.IsMobile, &s.VehicleID, &s.CanPurchase, &s.Active,
		&s.CreatedAt, &s.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, derrors.NotFound("sub_warehouse.not_found", "sub-warehouse not found")
	}
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.sub_warehouse_scan", "scan sub-warehouse", err)
	}
	s.OwnerRole = domain.SubWarehouseRole(role)
	return &s, nil
}
