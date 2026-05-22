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

type WarehouseRepository struct {
	pool *pgxpool.Pool
}

func NewWarehouseRepository(pool *pgxpool.Pool) *WarehouseRepository {
	return &WarehouseRepository{pool: pool}
}

var _ port.WarehouseRepository = (*WarehouseRepository)(nil)

const warehouseListSelect = `
SELECT w.id, w.name, w.code, w.branch_id, COALESCE(w.address,''), COALESCE(w.notes,''),
       w.active, w.created_at, w.updated_at,
       COALESCE(b.name,''), COALESCE(b.code,'')
FROM warehouse.warehouses w
LEFT JOIN identity.branches b ON b.id = w.branch_id
`

func (r *WarehouseRepository) List(ctx context.Context, activeOnly bool) ([]port.WarehouseListItem, error) {
	sql := warehouseListSelect
	if activeOnly {
		sql += ` WHERE w.active = TRUE`
	}
	sql += ` ORDER BY w.name`
	rows, err := r.pool.Query(ctx, sql)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.warehouse_list", "list warehouses", err)
	}
	defer rows.Close()
	out := []port.WarehouseListItem{}
	for rows.Next() {
		it, err := scanWarehouseListItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, nil
}

func (r *WarehouseRepository) FindByID(ctx context.Context, id uuid.UUID) (*port.WarehouseListItem, error) {
	row := r.pool.QueryRow(ctx, warehouseListSelect+` WHERE w.id = $1`, id)
	it, err := scanWarehouseListItem(row)
	if err != nil {
		return nil, err
	}
	return &it, nil
}

func (r *WarehouseRepository) Create(ctx context.Context, w *domain.Warehouse) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO warehouse.warehouses
			(id, name, code, branch_id, address, notes, active, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`, w.ID, w.Name, w.Code, w.BranchID, nullableString(w.Address), nullableString(w.Notes),
		w.Active, w.CreatedAt, w.UpdatedAt)
	return mapDBError(err, "warehouse.create", "create warehouse")
}

func (r *WarehouseRepository) Update(ctx context.Context, in port.UpdateWarehouseInput) (*domain.Warehouse, error) {
	sets := []string{}
	args := []any{in.ID}
	idx := 2
	add := func(col string, val any) {
		sets = append(sets, fmt.Sprintf("%s = $%d", col, idx))
		args = append(args, val)
		idx++
	}
	if in.Name != nil {
		add("name", strings.TrimSpace(*in.Name))
	}
	if in.ClearBranch {
		sets = append(sets, "branch_id = NULL")
	} else if in.BranchID != nil {
		add("branch_id", *in.BranchID)
	}
	if in.Address != nil {
		add("address", *in.Address)
	}
	if in.Notes != nil {
		add("notes", *in.Notes)
	}
	if in.Active != nil {
		add("active", *in.Active)
	}

	if len(sets) == 0 {
		row := r.pool.QueryRow(ctx, warehouseBaseSelect+` WHERE id = $1`, in.ID)
		return scanWarehouseBase(row)
	}

	sql := fmt.Sprintf(`UPDATE warehouse.warehouses SET %s WHERE id = $1`, strings.Join(sets, ", "))
	tag, err := r.pool.Exec(ctx, sql, args...)
	if err != nil {
		return nil, mapDBError(err, "warehouse.update", "update warehouse")
	}
	if tag.RowsAffected() == 0 {
		return nil, derrors.NotFound("warehouse.not_found", "warehouse not found")
	}
	row := r.pool.QueryRow(ctx, warehouseBaseSelect+` WHERE id = $1`, in.ID)
	return scanWarehouseBase(row)
}

const warehouseBaseSelect = `
SELECT id, name, code, branch_id, COALESCE(address,''), COALESCE(notes,''),
       active, created_at, updated_at
FROM warehouse.warehouses
`

func scanWarehouseBase(row pgx.Row) (*domain.Warehouse, error) {
	var w domain.Warehouse
	err := row.Scan(&w.ID, &w.Name, &w.Code, &w.BranchID, &w.Address, &w.Notes,
		&w.Active, &w.CreatedAt, &w.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, derrors.NotFound("warehouse.not_found", "warehouse not found")
	}
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.warehouse_scan", "scan warehouse", err)
	}
	return &w, nil
}

func scanWarehouseListItem(row pgx.Row) (port.WarehouseListItem, error) {
	var it port.WarehouseListItem
	err := row.Scan(
		&it.Warehouse.ID, &it.Warehouse.Name, &it.Warehouse.Code, &it.Warehouse.BranchID,
		&it.Warehouse.Address, &it.Warehouse.Notes,
		&it.Warehouse.Active, &it.Warehouse.CreatedAt, &it.Warehouse.UpdatedAt,
		&it.BranchName, &it.BranchCode,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return port.WarehouseListItem{}, derrors.NotFound("warehouse.not_found", "warehouse not found")
	}
	if err != nil {
		return port.WarehouseListItem{}, derrors.Wrap(derrors.KindInternal, "db.warehouse_scan", "scan warehouse", err)
	}
	return it, nil
}
