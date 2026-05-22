package postgres

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/enterprise/domain"
	"github.com/ion-core/backend/internal/enterprise/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type BOQLineRepository struct {
	pool *pgxpool.Pool
}

func NewBOQLineRepository(pool *pgxpool.Pool) *BOQLineRepository {
	return &BOQLineRepository{pool: pool}
}

var _ port.BOQLineRepository = (*BOQLineRepository)(nil)

const boqLineCols = `
	id, boq_version_id, pricebook_line_id,
	sku, name, unit,
	base_price_snapshot, min_margin_snapshot, max_discount_snapshot,
	assigned_provider_company_id, provider_user_id,
	vendor_unit_cost, sell_unit_price, quantity, line_discount_pct,
	sla_template_id, status, COALESCE(notes,''), sort_order,
	vendor_due_at,
	created_at, updated_at
`

func (r *BOQLineRepository) ListByBOQ(ctx context.Context, boqVersionID uuid.UUID) ([]domain.BOQLine, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+boqLineCols+`
		FROM enterprise.boq_lines
		WHERE boq_version_id = $1
		ORDER BY sort_order, sku
	`, boqVersionID)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.boq_line_list", "list boq lines", err)
	}
	defer rows.Close()
	out := []domain.BOQLine{}
	for rows.Next() {
		l, err := scanBOQLine(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, nil
}

func (r *BOQLineRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.BOQLine, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+boqLineCols+` FROM enterprise.boq_lines WHERE id = $1`, id)
	l, err := scanBOQLine(row)
	if err != nil {
		return nil, err
	}
	return &l, nil
}

func (r *BOQLineRepository) Create(ctx context.Context, l *domain.BOQLine) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO enterprise.boq_lines
			(id, boq_version_id, pricebook_line_id,
			 sku, name, unit,
			 base_price_snapshot, min_margin_snapshot, max_discount_snapshot,
			 assigned_provider_company_id, provider_user_id,
			 vendor_unit_cost, sell_unit_price, quantity, line_discount_pct,
			 sla_template_id, status, notes, sort_order,
			 vendor_due_at,
			 created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11,
		        $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22)
	`,
		l.ID, l.BOQVersionID, l.PricebookLineID,
		l.SKU, l.Name, l.Unit,
		l.BasePriceSnapshot, l.MinMarginSnapshot, l.MaxDiscountSnapshot,
		l.AssignedProviderCompanyID, l.ProviderUserID,
		l.VendorUnitCost, l.SellUnitPrice, l.Quantity, l.LineDiscountPct,
		l.SLATemplateID, string(l.Status), l.Notes, l.SortOrder,
		l.VendorDueAt,
		l.CreatedAt, l.UpdatedAt,
	)
	if err != nil {
		return mapDBError(err, "boq_line", "insert boq line")
	}
	return nil
}

func (r *BOQLineRepository) Update(ctx context.Context, l *domain.BOQLine) error {
	// Snapshot fields (base_price_snapshot etc.) are intentionally NOT
	// in the SET clause — TC-BQ-002 says they're immutable.
	tag, err := r.pool.Exec(ctx, `
		UPDATE enterprise.boq_lines
		SET assigned_provider_company_id = $2, provider_user_id = $3,
		    vendor_unit_cost = $4, sell_unit_price = $5, quantity = $6,
		    line_discount_pct = $7, sla_template_id = $8, status = $9,
		    notes = $10, sort_order = $11,
		    vendor_due_at = $12,
		    updated_at = NOW()
		WHERE id = $1
	`,
		l.ID, l.AssignedProviderCompanyID, l.ProviderUserID,
		l.VendorUnitCost, l.SellUnitPrice, l.Quantity,
		l.LineDiscountPct, l.SLATemplateID, string(l.Status),
		l.Notes, l.SortOrder,
		l.VendorDueAt,
	)
	if err != nil {
		return mapDBError(err, "boq_line", "update boq line")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("boq_line.not_found", "boq line not found")
	}
	return nil
}

// ListVendorDueLines — E4 sweep helper. Returns lines that are still
// awaiting vendor cost, parent BOQ still mutable.
func (r *BOQLineRepository) ListVendorDueLines(ctx context.Context) ([]port.VendorDueLine, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT bl.id, bl.boq_version_id, bl.provider_user_id,
		       bl.sku, bl.vendor_due_at
		FROM enterprise.boq_lines bl
		JOIN enterprise.boq_versions bv ON bv.id = bl.boq_version_id
		WHERE bl.vendor_due_at IS NOT NULL
		  AND bl.vendor_unit_cost IS NULL
		  AND bv.status IN ('draft', 'revision_draft')
	`)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.boq_line_vendor_due", "list due", err)
	}
	defer rows.Close()
	out := []port.VendorDueLine{}
	for rows.Next() {
		var v port.VendorDueLine
		if err := rows.Scan(&v.LineID, &v.BOQVersionID, &v.ProviderUserID, &v.SKU, &v.VendorDueAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// RecordVendorReminder is the idempotent reminder insert. Returns true
// if a new row was inserted (i.e., we should notify); false if the
// (line_id, bucket) pair already fired previously.
func (r *BOQLineRepository) RecordVendorReminder(ctx context.Context, lineID uuid.UUID, bucket string) (bool, error) {
	tag, err := r.pool.Exec(ctx, `
		INSERT INTO enterprise.boq_line_reminders (boq_line_id, bucket)
		VALUES ($1, $2)
		ON CONFLICT (boq_line_id, bucket) DO NOTHING
	`, lineID, bucket)
	if err != nil {
		return false, derrors.Wrap(derrors.KindInternal, "db.boq_line_reminder_insert", "insert reminder", err)
	}
	return tag.RowsAffected() > 0, nil
}

func (r *BOQLineRepository) Delete(ctx context.Context, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM enterprise.boq_lines WHERE id = $1`, id)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "db.boq_line_delete", "delete boq line", err)
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("boq_line.not_found", "boq line not found")
	}
	return nil
}

func scanBOQLine(row pgx.Row) (domain.BOQLine, error) {
	var (
		l      domain.BOQLine
		status string
	)
	err := row.Scan(
		&l.ID, &l.BOQVersionID, &l.PricebookLineID,
		&l.SKU, &l.Name, &l.Unit,
		&l.BasePriceSnapshot, &l.MinMarginSnapshot, &l.MaxDiscountSnapshot,
		&l.AssignedProviderCompanyID, &l.ProviderUserID,
		&l.VendorUnitCost, &l.SellUnitPrice, &l.Quantity, &l.LineDiscountPct,
		&l.SLATemplateID, &status, &l.Notes, &l.SortOrder,
		&l.VendorDueAt,
		&l.CreatedAt, &l.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.BOQLine{}, derrors.NotFound("boq_line.not_found", "boq line not found")
	}
	if err != nil {
		return domain.BOQLine{}, derrors.Wrap(derrors.KindInternal, "db.boq_line_scan", "scan boq line", err)
	}
	l.Status = domain.BOQLineStatus(status)
	return l, nil
}
