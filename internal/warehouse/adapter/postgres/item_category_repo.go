// Wave 117 — Item category repository.
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

type ItemCategoryRepository struct {
	pool *pgxpool.Pool
}

func NewItemCategoryRepository(pool *pgxpool.Pool) *ItemCategoryRepository {
	return &ItemCategoryRepository{pool: pool}
}

var _ port.ItemCategoryRepository = (*ItemCategoryRepository)(nil)

const itemCategoryCols = `id, code, name, parent_id, type_code,
	COALESCE(description,''), COALESCE(default_unit,''),
	sub_warehouse_allowed_default, requires_serial_at_intake, active,
	created_at, updated_at`

func (r *ItemCategoryRepository) Create(ctx context.Context, c *domain.ItemCategoryDef) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO warehouse.item_categories
			(id, code, name, parent_id, type_code, description, default_unit,
			 sub_warehouse_allowed_default, requires_serial_at_intake, active,
			 created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
	`, c.ID, c.Code, c.Name, c.ParentID, string(c.TypeCode),
		nullableString(c.Description), nullableString(c.DefaultUnit),
		c.SubWarehouseAllowedDefault, c.RequiresSerialAtIntake, c.Active,
		c.CreatedAt, c.UpdatedAt)
	return mapDBError(err, "item_category.create", "create item category")
}

func (r *ItemCategoryRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.ItemCategoryDef, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+itemCategoryCols+` FROM warehouse.item_categories WHERE id=$1`, id)
	return scanItemCategory(row)
}

func (r *ItemCategoryRepository) FindByCode(ctx context.Context, code string) (*domain.ItemCategoryDef, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+itemCategoryCols+` FROM warehouse.item_categories WHERE code=$1`, code)
	return scanItemCategory(row)
}

func (r *ItemCategoryRepository) List(ctx context.Context, f port.ItemCategoryListFilter) ([]domain.ItemCategoryDef, error) {
	var wh []string
	var args []any
	if f.TypeCode != "" {
		args = append(args, f.TypeCode)
		wh = append(wh, fmt.Sprintf("type_code=$%d", len(args)))
	}
	if f.ActiveOnly {
		wh = append(wh, "active=TRUE")
	}
	if f.ParentID != nil {
		args = append(args, *f.ParentID)
		wh = append(wh, fmt.Sprintf("parent_id=$%d", len(args)))
	}
	where := ""
	if len(wh) > 0 {
		where = " WHERE " + strings.Join(wh, " AND ")
	}
	rows, err := r.pool.Query(ctx,
		`SELECT `+itemCategoryCols+` FROM warehouse.item_categories`+where+` ORDER BY type_code, name`, args...)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.item_category_list", "list categories", err)
	}
	defer rows.Close()
	out := []domain.ItemCategoryDef{}
	for rows.Next() {
		c, err := scanItemCategory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, nil
}

func (r *ItemCategoryRepository) Update(ctx context.Context, in port.UpdateItemCategoryInput) (*domain.ItemCategoryDef, error) {
	sets := []string{}
	args := []any{in.ID}
	idx := 2
	add := func(col string, v any) {
		sets = append(sets, fmt.Sprintf("%s=$%d", col, idx))
		args = append(args, v)
		idx++
	}
	if in.Name != nil {
		add("name", strings.TrimSpace(*in.Name))
	}
	if in.Description != nil {
		add("description", *in.Description)
	}
	if in.ClearParent {
		sets = append(sets, "parent_id=NULL")
	} else if in.ParentID != nil {
		add("parent_id", *in.ParentID)
	}
	if in.DefaultUnit != nil {
		add("default_unit", *in.DefaultUnit)
	}
	if in.SubWarehouseAllowedDefault != nil {
		add("sub_warehouse_allowed_default", *in.SubWarehouseAllowedDefault)
	}
	if in.RequiresSerialAtIntake != nil {
		add("requires_serial_at_intake", *in.RequiresSerialAtIntake)
	}
	if in.Active != nil {
		add("active", *in.Active)
	}
	if len(sets) == 0 {
		return r.FindByID(ctx, in.ID)
	}
	sets = append(sets, "updated_at=NOW()")
	sql := fmt.Sprintf(`UPDATE warehouse.item_categories SET %s WHERE id=$1`, strings.Join(sets, ", "))
	tag, err := r.pool.Exec(ctx, sql, args...)
	if err != nil {
		return nil, mapDBError(err, "item_category.update", "update category")
	}
	if tag.RowsAffected() == 0 {
		return nil, derrors.NotFound("item_category.not_found", "item category not found")
	}
	return r.FindByID(ctx, in.ID)
}

func scanItemCategory(row pgx.Row) (*domain.ItemCategoryDef, error) {
	var c domain.ItemCategoryDef
	var typeCode string
	err := row.Scan(&c.ID, &c.Code, &c.Name, &c.ParentID, &typeCode,
		&c.Description, &c.DefaultUnit,
		&c.SubWarehouseAllowedDefault, &c.RequiresSerialAtIntake, &c.Active,
		&c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, derrors.NotFound("item_category.not_found", "item category not found")
	}
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.item_category_scan", "scan category", err)
	}
	c.TypeCode = domain.ItemType(typeCode)
	return &c, nil
}
