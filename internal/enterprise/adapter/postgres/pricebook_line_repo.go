package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/enterprise/domain"
	"github.com/ion-core/backend/internal/enterprise/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type PricebookLineRepository struct {
	pool *pgxpool.Pool
}

func NewPricebookLineRepository(pool *pgxpool.Pool) *PricebookLineRepository {
	return &PricebookLineRepository{pool: pool}
}

var _ port.PricebookLineRepository = (*PricebookLineRepository)(nil)

const pricebookLineCols = `
	id, pricebook_id,
	sku, name, COALESCE(category,''), COALESCE(description,''), unit,
	base_price, default_margin_pct, min_margin_pct, max_discount_pct,
	COALESCE(allowed_provider_company_ids, '{}'::uuid[]),
	COALESCE(owner_role,''),
	sort_order, active,
	COALESCE(priority_score, 0),
	created_at, updated_at
`

func (r *PricebookLineRepository) ListByPricebook(ctx context.Context, pricebookID uuid.UUID) ([]domain.PricebookLine, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+pricebookLineCols+`
		 FROM enterprise.pricebook_lines
		 WHERE pricebook_id = $1
		 ORDER BY sort_order, name`,
		pricebookID,
	)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.pricebook_line_list", "list pricebook lines", err)
	}
	defer rows.Close()
	out := []domain.PricebookLine{}
	for rows.Next() {
		l, err := scanPricebookLine(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, nil
}

// ListByPricebookSorted is the Wave 106 ordering variant — when `sort`
// is "priority" the rows come back ordered by (priority_score DESC,
// sku ASC) so the FE can render the recommended-vendor badge first.
// Any other value falls back to the default (sort_order, name) so
// existing callers stay unaffected.
func (r *PricebookLineRepository) ListByPricebookSorted(ctx context.Context, pricebookID uuid.UUID, sort string) ([]domain.PricebookLine, error) {
	orderBy := "sort_order, name"
	if sort == "priority" {
		orderBy = "priority_score DESC, sku ASC"
	}
	rows, err := r.pool.Query(ctx,
		`SELECT `+pricebookLineCols+`
		 FROM enterprise.pricebook_lines
		 WHERE pricebook_id = $1
		 ORDER BY `+orderBy,
		pricebookID,
	)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.pricebook_line_list_sorted", "list pricebook lines sorted", err)
	}
	defer rows.Close()
	out := []domain.PricebookLine{}
	for rows.Next() {
		l, err := scanPricebookLine(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, nil
}

func (r *PricebookLineRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.PricebookLine, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+pricebookLineCols+` FROM enterprise.pricebook_lines WHERE id = $1`, id)
	l, err := scanPricebookLine(row)
	if err != nil {
		return nil, err
	}
	return &l, nil
}

func (r *PricebookLineRepository) Create(ctx context.Context, l *domain.PricebookLine) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO enterprise.pricebook_lines
			(id, pricebook_id, sku, name, category, description, unit,
			 base_price, default_margin_pct, min_margin_pct, max_discount_pct,
			 allowed_provider_company_ids, owner_role,
			 sort_order, active, priority_score, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)
	`,
		l.ID, l.PricebookID, l.SKU, l.Name, l.Category, l.Description, l.Unit,
		l.BasePrice, l.DefaultMarginPct, l.MinMarginPct, l.MaxDiscountPct,
		l.AllowedProviderCompanyIDs, l.OwnerRole,
		l.SortOrder, l.Active, l.PriorityScore, l.CreatedAt, l.UpdatedAt,
	)
	if err != nil {
		return mapDBError(err, "pricebook_line", "insert pricebook line")
	}
	return nil
}

func (r *PricebookLineRepository) Update(ctx context.Context, l *domain.PricebookLine) error {
	// Single dynamic UPDATE — we update only the changeable columns.
	// Domain invariants are re-checked by the caller before the repo
	// is invoked, so this layer only enforces concurrency + storage.
	tag, err := r.pool.Exec(ctx, `
		UPDATE enterprise.pricebook_lines
		SET name = $2, category = $3, description = $4, unit = $5,
		    base_price = $6, default_margin_pct = $7, min_margin_pct = $8,
		    max_discount_pct = $9, allowed_provider_company_ids = $10,
		    owner_role = $11, sort_order = $12, active = $13,
		    priority_score = $14,
		    updated_at = NOW()
		WHERE id = $1
	`,
		l.ID, l.Name, l.Category, l.Description, l.Unit,
		l.BasePrice, l.DefaultMarginPct, l.MinMarginPct,
		l.MaxDiscountPct, l.AllowedProviderCompanyIDs,
		l.OwnerRole, l.SortOrder, l.Active, l.PriorityScore,
	)
	if err != nil {
		return mapDBError(err, "pricebook_line", "update pricebook line")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("pricebook_line.not_found", "pricebook line not found")
	}
	return nil
}

func (r *PricebookLineRepository) Delete(ctx context.Context, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM enterprise.pricebook_lines WHERE id = $1`, id)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "db.pricebook_line_delete", "delete pricebook line", err)
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("pricebook_line.not_found", "pricebook line not found")
	}
	return nil
}

func scanPricebookLine(row pgx.Row) (domain.PricebookLine, error) {
	var l domain.PricebookLine
	err := row.Scan(
		&l.ID, &l.PricebookID,
		&l.SKU, &l.Name, &l.Category, &l.Description, &l.Unit,
		&l.BasePrice, &l.DefaultMarginPct, &l.MinMarginPct, &l.MaxDiscountPct,
		&l.AllowedProviderCompanyIDs, &l.OwnerRole,
		&l.SortOrder, &l.Active, &l.PriorityScore, &l.CreatedAt, &l.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.PricebookLine{}, derrors.NotFound("pricebook_line.not_found", "pricebook line not found")
	}
	if err != nil {
		return domain.PricebookLine{}, derrors.Wrap(derrors.KindInternal, "db.pricebook_line_scan", "scan pricebook line", err)
	}
	return l, nil
}

// Compile-time hint: the dynamic field-pick pattern below is unused
// right now (Update sets all columns at once). Kept here as a reminder
// for the BOQ phase where line-level partial PATCH will matter.
var _ = strings.Builder{}
var _ = fmt.Sprintf
