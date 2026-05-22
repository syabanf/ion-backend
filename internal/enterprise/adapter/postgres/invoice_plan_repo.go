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

type InvoicePlanRepository struct {
	pool *pgxpool.Pool
}

func NewInvoicePlanRepository(pool *pgxpool.Pool) *InvoicePlanRepository {
	return &InvoicePlanRepository{pool: pool}
}

var _ port.InvoicePlanRepository = (*InvoicePlanRepository)(nil)

const invoicePlanCols = `
	id, quotation_id, opportunity_id, boq_version_id, plan_number, status,
	total_amount, COALESCE(subtotal_amount, 0), COALESCE(tax_pct, 11.0), COALESCE(tax_amount, 0),
	planned_amount, currency, tolerance_pct,
	COALESCE(notes, ''), created_by, revision, created_at, updated_at
`

func (r *InvoicePlanRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.InvoicePlan, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+invoicePlanCols+` FROM enterprise.invoice_plans WHERE id = $1`, id)
	p, err := scanInvoicePlan(row)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (r *InvoicePlanRepository) FindByQuotationID(ctx context.Context, quotationID uuid.UUID) (*domain.InvoicePlan, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+invoicePlanCols+` FROM enterprise.invoice_plans WHERE quotation_id = $1`, quotationID)
	p, err := scanInvoicePlan(row)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (r *InvoicePlanRepository) Create(ctx context.Context, p *domain.InvoicePlan) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO enterprise.invoice_plans
			(id, quotation_id, opportunity_id, boq_version_id, plan_number, status,
			 total_amount, subtotal_amount, tax_pct, tax_amount,
			 planned_amount, currency, tolerance_pct, notes, created_by,
			 revision, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10,
		        $11, $12, $13, $14, $15, $16, $17, $18)
	`,
		p.ID, p.QuotationID, p.OpportunityID, p.BOQVersionID, p.PlanNumber, string(p.Status),
		p.TotalAmount, p.SubtotalAmount, p.TaxPct, p.TaxAmount,
		p.PlannedAmount, p.Currency, p.TolerancePct, p.Notes, p.CreatedBy,
		p.Revision, p.CreatedAt, p.UpdatedAt,
	)
	if err != nil {
		return mapDBError(err, "invoice_plan", "insert plan")
	}
	return nil
}

func (r *InvoicePlanRepository) Update(ctx context.Context, p *domain.InvoicePlan) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE enterprise.invoice_plans
		SET status = $2, planned_amount = $3, tolerance_pct = $4,
		    subtotal_amount = $5, tax_pct = $6, tax_amount = $7,
		    notes = $8, revision = revision + 1, updated_at = NOW()
		WHERE id = $1
	`,
		p.ID, string(p.Status), p.PlannedAmount, p.TolerancePct,
		p.SubtotalAmount, p.TaxPct, p.TaxAmount,
		p.Notes,
	)
	if err != nil {
		return mapDBError(err, "invoice_plan", "update plan")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("invoice_plan.not_found", "invoice plan not found")
	}
	return nil
}

func (r *InvoicePlanRepository) ListItems(ctx context.Context, planID uuid.UUID) ([]domain.InvoicePlanItem, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, plan_id, seq_no, label, amount, due_offset_days, invoice_id, issued_at, COALESCE(notes,''), created_at
		FROM enterprise.invoice_plan_items
		WHERE plan_id = $1
		ORDER BY seq_no
	`, planID)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.invoice_plan_items_list", "list items", err)
	}
	defer rows.Close()
	out := []domain.InvoicePlanItem{}
	for rows.Next() {
		var it domain.InvoicePlanItem
		if err := rows.Scan(
			&it.ID, &it.PlanID, &it.SeqNo, &it.Label, &it.Amount, &it.DueOffsetDays,
			&it.InvoiceID, &it.IssuedAt, &it.Notes, &it.CreatedAt,
		); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "db.invoice_plan_item_scan", "scan", err)
		}
		out = append(out, it)
	}
	return out, nil
}

func (r *InvoicePlanRepository) ReplaceItems(ctx context.Context, planID uuid.UUID, items []domain.InvoicePlanItem) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "db.invoice_plan_items_tx", "begin tx", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Hard delete is safe — we don't allow item replacement after activation
	// (usecase gates this), so no issued-invoice linkage can exist yet.
	if _, err := tx.Exec(ctx, `DELETE FROM enterprise.invoice_plan_items WHERE plan_id = $1`, planID); err != nil {
		return derrors.Wrap(derrors.KindInternal, "db.invoice_plan_items_clear", "delete", err)
	}
	for _, it := range items {
		if _, err := tx.Exec(ctx, `
			INSERT INTO enterprise.invoice_plan_items
				(id, plan_id, seq_no, label, amount, due_offset_days, notes, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		`, it.ID, planID, it.SeqNo, it.Label, it.Amount, it.DueOffsetDays, it.Notes, it.CreatedAt); err != nil {
			return mapDBError(err, "invoice_plan_item", "insert")
		}
	}
	return tx.Commit(ctx)
}

func (r *InvoicePlanRepository) FindItemByID(ctx context.Context, id uuid.UUID) (*domain.InvoicePlanItem, error) {
	var it domain.InvoicePlanItem
	err := r.pool.QueryRow(ctx, `
		SELECT id, plan_id, seq_no, label, amount, due_offset_days, invoice_id, issued_at, COALESCE(notes,''), created_at
		FROM enterprise.invoice_plan_items WHERE id = $1
	`, id).Scan(
		&it.ID, &it.PlanID, &it.SeqNo, &it.Label, &it.Amount, &it.DueOffsetDays,
		&it.InvoiceID, &it.IssuedAt, &it.Notes, &it.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, derrors.NotFound("invoice_plan_item.not_found", "termin item not found")
	}
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.invoice_plan_item_find", "find", err)
	}
	return &it, nil
}

func (r *InvoicePlanRepository) UpdateItem(ctx context.Context, it *domain.InvoicePlanItem) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE enterprise.invoice_plan_items
		SET invoice_id = $2, issued_at = $3, notes = $4
		WHERE id = $1
	`, it.ID, it.InvoiceID, it.IssuedAt, it.Notes)
	if err != nil {
		return mapDBError(err, "invoice_plan_item", "update")
	}
	return nil
}

func scanInvoicePlan(row pgx.Row) (domain.InvoicePlan, error) {
	var (
		p      domain.InvoicePlan
		status string
	)
	err := row.Scan(
		&p.ID, &p.QuotationID, &p.OpportunityID, &p.BOQVersionID, &p.PlanNumber, &status,
		&p.TotalAmount, &p.SubtotalAmount, &p.TaxPct, &p.TaxAmount,
		&p.PlannedAmount, &p.Currency, &p.TolerancePct,
		&p.Notes, &p.CreatedBy, &p.Revision, &p.CreatedAt, &p.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.InvoicePlan{}, derrors.NotFound("invoice_plan.not_found", "invoice plan not found")
	}
	if err != nil {
		return domain.InvoicePlan{}, derrors.Wrap(derrors.KindInternal, "db.invoice_plan_scan", "scan", err)
	}
	p.Status = domain.InvoicePlanStatus(status)
	return p, nil
}
