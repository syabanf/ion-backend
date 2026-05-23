package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/reseller/domain"
	"github.com/ion-core/backend/internal/reseller/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// WholesaleSKURepository implements port.WholesaleSKURepository
// against `reseller.wholesale_skus`.
type WholesaleSKURepository struct {
	pool *pgxpool.Pool
}

func NewWholesaleSKURepository(pool *pgxpool.Pool) *WholesaleSKURepository {
	return &WholesaleSKURepository{pool: pool}
}

var _ port.WholesaleSKURepository = (*WholesaleSKURepository)(nil)

const skuCols = `
	id, supplier_subsidiary_id,
	COALESCE(name, ''), COALESCE(sku_code, ''),
	COALESCE(unit_price, 0), unit, is_active,
	created_at, updated_at
`

func (r *WholesaleSKURepository) Create(ctx context.Context, s *domain.WholesaleSKU) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO reseller.wholesale_skus
			(id, supplier_subsidiary_id, name, sku_code, unit_price, unit, is_active,
			 created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`,
		s.ID, s.SupplierSubsidiaryID, s.Name, s.SKUCode, s.UnitPrice, s.Unit, s.IsActive,
		s.CreatedAt, s.UpdatedAt,
	)
	if err != nil {
		return mapDBError(err, "wholesale_sku", "insert wholesale sku")
	}
	return nil
}

func (r *WholesaleSKURepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.WholesaleSKU, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+skuCols+` FROM reseller.wholesale_skus WHERE id = $1`, id)
	s, err := scanSKU(row)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *WholesaleSKURepository) FindByIDs(ctx context.Context, ids []uuid.UUID) ([]domain.WholesaleSKU, error) {
	if len(ids) == 0 {
		return []domain.WholesaleSKU{}, nil
	}
	rows, err := r.pool.Query(ctx,
		`SELECT `+skuCols+` FROM reseller.wholesale_skus WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.wholesale_sku_find_ids", "find skus by ids", err)
	}
	defer rows.Close()

	out := []domain.WholesaleSKU{}
	for rows.Next() {
		s, err := scanSKU(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

func (r *WholesaleSKURepository) List(ctx context.Context, f port.WholesaleSKUListFilter) ([]domain.WholesaleSKU, int, error) {
	var wh []string
	var args []any
	if f.SupplierSubsidiaryID != nil {
		args = append(args, *f.SupplierSubsidiaryID)
		wh = append(wh, fmt.Sprintf("supplier_subsidiary_id = $%d", len(args)))
	}
	if f.OnlyActive {
		wh = append(wh, "is_active = TRUE")
	}
	where := ""
	if len(wh) > 0 {
		where = " WHERE " + strings.Join(wh, " AND ")
	}

	var total int
	if err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM reseller.wholesale_skus`+where, args...,
	).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.wholesale_sku_count", "count skus", err)
	}

	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}
	args = append(args, limit, offset)
	sql := `SELECT ` + skuCols + ` FROM reseller.wholesale_skus` + where +
		` ORDER BY sku_code LIMIT $` + fmt.Sprint(len(args)-1) +
		` OFFSET $` + fmt.Sprint(len(args))

	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.wholesale_sku_list", "list skus", err)
	}
	defer rows.Close()

	out := []domain.WholesaleSKU{}
	for rows.Next() {
		s, err := scanSKU(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, s)
	}
	return out, total, nil
}

func (r *WholesaleSKURepository) Update(ctx context.Context, s *domain.WholesaleSKU) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE reseller.wholesale_skus
		SET name = $2,
		    unit_price = $3,
		    unit = $4,
		    is_active = $5,
		    updated_at = NOW()
		WHERE id = $1
	`,
		s.ID, s.Name, s.UnitPrice, s.Unit, s.IsActive,
	)
	if err != nil {
		return mapDBError(err, "wholesale_sku", "update wholesale sku")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("wholesale_sku.not_found", "wholesale sku not found")
	}
	return nil
}

func scanSKU(row pgx.Row) (domain.WholesaleSKU, error) {
	var s domain.WholesaleSKU
	err := row.Scan(
		&s.ID, &s.SupplierSubsidiaryID,
		&s.Name, &s.SKUCode,
		&s.UnitPrice, &s.Unit, &s.IsActive,
		&s.CreatedAt, &s.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.WholesaleSKU{}, derrors.NotFound("wholesale_sku.not_found", "wholesale sku not found")
	}
	if err != nil {
		return domain.WholesaleSKU{}, derrors.Wrap(derrors.KindInternal, "db.wholesale_sku_scan", "scan sku", err)
	}
	return s, nil
}
