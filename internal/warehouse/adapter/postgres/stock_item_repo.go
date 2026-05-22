package postgres

import (
	"context"
	"encoding/json"
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

type StockItemRepository struct {
	pool *pgxpool.Pool
}

func NewStockItemRepository(pool *pgxpool.Pool) *StockItemRepository {
	return &StockItemRepository{pool: pool}
}

var _ port.StockItemRepository = (*StockItemRepository)(nil)

const stockItemSelect = `
SELECT id, sku, name, category, COALESCE(brand,''), COALESCE(model,''), COALESCE(spec,''),
       unit, serialized, default_unit_cost, active, metadata, created_at, updated_at
FROM warehouse.stock_items
`

func (r *StockItemRepository) List(ctx context.Context, f port.StockItemListFilter) ([]domain.StockItem, int, error) {
	conds := []string{"1=1"}
	args := []any{}
	idx := 1
	if s := strings.TrimSpace(f.Search); s != "" {
		conds = append(conds, fmt.Sprintf("(name ILIKE $%d OR sku ILIKE $%d)", idx, idx))
		args = append(args, "%"+s+"%")
		idx++
	}
	if f.Category != "" {
		conds = append(conds, fmt.Sprintf("category = $%d", idx))
		args = append(args, f.Category)
		idx++
	}
	if f.Active != nil {
		conds = append(conds, fmt.Sprintf("active = $%d", idx))
		args = append(args, *f.Active)
		idx++
	}
	where := strings.Join(conds, " AND ")

	var total int
	if err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM warehouse.stock_items WHERE `+where, args...,
	).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.item_count", "count items", err)
	}
	if f.Limit <= 0 {
		f.Limit = 50
	}
	sql := stockItemSelect + ` WHERE ` + where + fmt.Sprintf(` ORDER BY name LIMIT $%d OFFSET $%d`, idx, idx+1)
	args = append(args, f.Limit, f.Offset)

	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.item_list", "list items", err)
	}
	defer rows.Close()

	out := []domain.StockItem{}
	for rows.Next() {
		item, err := scanStockItem(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *item)
	}
	return out, total, nil
}

func (r *StockItemRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.StockItem, error) {
	row := r.pool.QueryRow(ctx, stockItemSelect+` WHERE id = $1`, id)
	return scanStockItem(row)
}

func (r *StockItemRepository) FindBySKU(ctx context.Context, sku string) (*domain.StockItem, error) {
	row := r.pool.QueryRow(ctx, stockItemSelect+` WHERE sku = $1`, sku)
	return scanStockItem(row)
}

func (r *StockItemRepository) Create(ctx context.Context, item *domain.StockItem) error {
	meta, _ := json.Marshal(item.Metadata)
	_, err := r.pool.Exec(ctx, `
		INSERT INTO warehouse.stock_items
			(id, sku, name, category, brand, model, spec, unit, serialized,
			 default_unit_cost, active, metadata, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
	`, item.ID, item.SKU, item.Name, string(item.Category),
		nullableString(item.Brand), nullableString(item.Model), nullableString(item.Spec),
		string(item.Unit), item.Serialized, item.DefaultUnitCost, item.Active, meta,
		item.CreatedAt, item.UpdatedAt)
	return mapDBError(err, "stock_item.create", "create stock item")
}

func (r *StockItemRepository) Update(ctx context.Context, in port.UpdateStockItemInput) (*domain.StockItem, error) {
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
	if in.Brand != nil {
		add("brand", *in.Brand)
	}
	if in.Model != nil {
		add("model", *in.Model)
	}
	if in.Spec != nil {
		add("spec", *in.Spec)
	}
	if in.DefaultUnitCost != nil {
		add("default_unit_cost", *in.DefaultUnitCost)
	}
	if in.Active != nil {
		add("active", *in.Active)
	}

	if len(sets) == 0 {
		return r.FindByID(ctx, in.ID)
	}
	sql := fmt.Sprintf(`UPDATE warehouse.stock_items SET %s WHERE id = $1`, strings.Join(sets, ", "))
	tag, err := r.pool.Exec(ctx, sql, args...)
	if err != nil {
		return nil, mapDBError(err, "stock_item.update", "update stock item")
	}
	if tag.RowsAffected() == 0 {
		return nil, derrors.NotFound("stock_item.not_found", "stock item not found")
	}
	return r.FindByID(ctx, in.ID)
}

func scanStockItem(row pgx.Row) (*domain.StockItem, error) {
	var (
		it       domain.StockItem
		category string
		unit     string
		meta     []byte
	)
	err := row.Scan(&it.ID, &it.SKU, &it.Name, &category, &it.Brand, &it.Model, &it.Spec,
		&unit, &it.Serialized, &it.DefaultUnitCost, &it.Active, &meta, &it.CreatedAt, &it.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, derrors.NotFound("stock_item.not_found", "stock item not found")
	}
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.item_scan", "scan stock item", err)
	}
	it.Category = domain.ItemCategory(category)
	it.Unit = domain.Unit(unit)
	if len(meta) > 0 {
		_ = json.Unmarshal(meta, &it.Metadata)
	}
	if it.Metadata == nil {
		it.Metadata = map[string]any{}
	}
	return &it, nil
}
