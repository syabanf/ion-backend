// Wave 89 (Tier 3) — postgres adapter for product BOM templates.
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

type ProductBOMRepository struct {
	pool *pgxpool.Pool
}

func NewProductBOMRepository(pool *pgxpool.Pool) *ProductBOMRepository {
	return &ProductBOMRepository{pool: pool}
}

var _ port.ProductBOMTemplateRepository = (*ProductBOMRepository)(nil)

const bomSelect = `
SELECT id, product_id, name, COALESCE(description,''), active,
       created_by, created_at, updated_at
FROM warehouse.product_bom_templates
`

func (r *ProductBOMRepository) Create(
	ctx context.Context, tpl *domain.ProductBOMTemplate, items []domain.ProductBOMTemplateItem,
) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "bom.tx_begin", "begin BOM tx", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		INSERT INTO warehouse.product_bom_templates (
			id, product_id, name, description, active, created_by, created_at, updated_at
		) VALUES ($1,$2,$3,NULLIF($4,''),$5,$6,$7,$7)
	`,
		tpl.ID, tpl.ProductID, tpl.Name, tpl.Description, tpl.Active,
		tpl.CreatedBy, tpl.CreatedAt,
	); err != nil {
		return mapDBError(err, "bom.create", "create BOM template")
	}
	for _, it := range items {
		if _, err := tx.Exec(ctx, `
			INSERT INTO warehouse.product_bom_template_items (
				id, template_id, stock_item_id, default_quantity,
				required, sort_order, notes
			) VALUES ($1,$2,$3,$4,$5,$6,NULLIF($7,''))
		`,
			it.ID, it.TemplateID, it.StockItemID, it.DefaultQuantity,
			it.Required, it.SortOrder, it.Notes,
		); err != nil {
			return mapDBError(err, "bom.item", "create BOM line")
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return derrors.Wrap(derrors.KindInternal, "bom.tx_commit", "commit BOM tx", err)
	}
	return nil
}

func (r *ProductBOMRepository) FindByID(
	ctx context.Context, id uuid.UUID,
) (*port.BOMTemplateDetail, error) {
	return r.scanDetail(ctx, bomSelect+" WHERE id = $1", id)
}

func (r *ProductBOMRepository) FindActiveForProduct(
	ctx context.Context, productID uuid.UUID,
) (*port.BOMTemplateDetail, error) {
	return r.scanDetail(ctx,
		bomSelect+" WHERE product_id = $1 AND active = TRUE LIMIT 1",
		productID)
}

func (r *ProductBOMRepository) scanDetail(
	ctx context.Context, q string, arg any,
) (*port.BOMTemplateDetail, error) {
	row := r.pool.QueryRow(ctx, q, arg)
	var t domain.ProductBOMTemplate
	if err := row.Scan(&t.ID, &t.ProductID, &t.Name, &t.Description, &t.Active,
		&t.CreatedBy, &t.CreatedAt, &t.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, derrors.NotFound("bom.not_found", "BOM template not found")
		}
		return nil, derrors.Wrap(derrors.KindInternal, "bom.scan", "scan BOM template", err)
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id, template_id, stock_item_id, default_quantity,
		       required, sort_order, COALESCE(notes,'')
		FROM warehouse.product_bom_template_items
		WHERE template_id = $1
		ORDER BY sort_order, id
	`, t.ID)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "bom.items_q", "load BOM items", err)
	}
	defer rows.Close()
	items := []domain.ProductBOMTemplateItem{}
	for rows.Next() {
		var it domain.ProductBOMTemplateItem
		if err := rows.Scan(&it.ID, &it.TemplateID, &it.StockItemID, &it.DefaultQuantity,
			&it.Required, &it.SortOrder, &it.Notes); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "bom.item_scan", "scan BOM item", err)
		}
		items = append(items, it)
	}
	return &port.BOMTemplateDetail{Template: t, Items: items}, nil
}

func (r *ProductBOMRepository) ListForProduct(
	ctx context.Context, productID uuid.UUID, activeOnly bool,
) ([]domain.ProductBOMTemplate, error) {
	q := bomSelect + " WHERE product_id = $1"
	if activeOnly {
		q += " AND active = TRUE"
	}
	q += " ORDER BY created_at DESC"
	rows, err := r.pool.Query(ctx, q, productID)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "bom.list", "list BOM templates", err)
	}
	defer rows.Close()
	out := []domain.ProductBOMTemplate{}
	for rows.Next() {
		var t domain.ProductBOMTemplate
		if err := rows.Scan(&t.ID, &t.ProductID, &t.Name, &t.Description, &t.Active,
			&t.CreatedBy, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "bom.list_scan",
				"scan BOM template list row", err)
		}
		out = append(out, t)
	}
	return out, nil
}

func (r *ProductBOMRepository) Deactivate(ctx context.Context, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE warehouse.product_bom_templates
		   SET active = FALSE, updated_at = NOW()
		 WHERE id = $1
	`, id)
	if err != nil {
		return mapDBError(err, "bom.deactivate", "deactivate BOM template")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("bom.not_found", "BOM template not found")
	}
	return nil
}
