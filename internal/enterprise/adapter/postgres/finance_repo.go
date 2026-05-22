package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/enterprise/domain"
	"github.com/ion-core/backend/internal/enterprise/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// InvoiceRepository
// =====================================================================

type InvoiceRepository struct {
	pool *pgxpool.Pool
}

func NewInvoiceRepository(pool *pgxpool.Pool) *InvoiceRepository {
	return &InvoiceRepository{pool: pool}
}

var _ port.InvoiceRepository = (*InvoiceRepository)(nil)

const invoiceCols = `
	id, invoice_number, quotation_id, opportunity_id, boq_version_id,
	status,
	total_amount, COALESCE(subtotal_amount, 0),
	COALESCE(tax_pct, 11.0), COALESCE(tax_amount, 0),
	paid_amount, currency,
	issued_at, due_at, paid_at, voided_at,
	COALESCE(void_reason, ''), COALESCE(notes, ''),
	issued_by,
	invoice_plan_id, invoice_plan_item_id,
	revision, created_at, updated_at
`

func (r *InvoiceRepository) List(ctx context.Context, f port.InvoiceListFilter) ([]domain.Invoice, int, error) {
	var wh []string
	var args []any
	if f.Status != "" {
		args = append(args, f.Status)
		wh = append(wh, fmt.Sprintf("status = $%d", len(args)))
	}
	if f.OpportunityID != nil {
		args = append(args, *f.OpportunityID)
		wh = append(wh, fmt.Sprintf("opportunity_id = $%d", len(args)))
	}
	if f.QuotationID != nil {
		args = append(args, *f.QuotationID)
		wh = append(wh, fmt.Sprintf("quotation_id = $%d", len(args)))
	}
	where := ""
	if len(wh) > 0 {
		where = " WHERE " + strings.Join(wh, " AND ")
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}

	// Total before paging.
	var total int
	if err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM enterprise.invoices`+where, args...,
	).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.invoice_count", "count", err)
	}

	args = append(args, limit, f.Offset)
	sql := `SELECT ` + invoiceCols + ` FROM enterprise.invoices` + where +
		` ORDER BY created_at DESC LIMIT $` + fmt.Sprint(len(args)-1) +
		` OFFSET $` + fmt.Sprint(len(args))
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.invoice_list", "list", err)
	}
	defer rows.Close()
	out := []domain.Invoice{}
	for rows.Next() {
		inv, err := scanInvoice(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, inv)
	}
	return out, total, nil
}

func (r *InvoiceRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.Invoice, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+invoiceCols+` FROM enterprise.invoices WHERE id = $1`, id)
	inv, err := scanInvoice(row)
	if err != nil {
		return nil, err
	}
	return &inv, nil
}

func (r *InvoiceRepository) FindByQuotationID(ctx context.Context, quotationID uuid.UUID) (*domain.Invoice, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+invoiceCols+` FROM enterprise.invoices WHERE quotation_id = $1`, quotationID)
	inv, err := scanInvoice(row)
	if err != nil {
		return nil, err
	}
	return &inv, nil
}

func (r *InvoiceRepository) Create(ctx context.Context, inv *domain.Invoice) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO enterprise.invoices
			(id, invoice_number, quotation_id, opportunity_id, boq_version_id,
			 status,
			 total_amount, subtotal_amount, tax_pct, tax_amount,
			 paid_amount, currency,
			 issued_at, due_at, paid_at, voided_at, void_reason, notes,
			 issued_by,
			 invoice_plan_id, invoice_plan_item_id,
			 revision, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12,
		        $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24)
	`,
		inv.ID, inv.InvoiceNumber, inv.QuotationID, inv.OpportunityID, inv.BOQVersionID,
		string(inv.Status),
		inv.TotalAmount, inv.SubtotalAmount, inv.TaxPct, inv.TaxAmount,
		inv.PaidAmount, inv.Currency,
		inv.IssuedAt, inv.DueAt, inv.PaidAt, inv.VoidedAt, inv.VoidReason, inv.Notes,
		inv.IssuedBy,
		inv.InvoicePlanID, inv.InvoicePlanItemID,
		inv.Revision, inv.CreatedAt, inv.UpdatedAt,
	)
	if err != nil {
		return mapDBError(err, "invoice", "insert invoice")
	}
	return nil
}

func (r *InvoiceRepository) Update(ctx context.Context, inv *domain.Invoice, ifRevision *int) error {
	var (
		tag pgconn.CommandTag
		err error
	)
	if ifRevision != nil {
		tag, err = r.pool.Exec(ctx, `
			UPDATE enterprise.invoices
			SET status = $2, paid_amount = $3, paid_at = $4,
			    voided_at = $5, void_reason = $6, notes = $7,
			    revision = revision + 1, updated_at = NOW()
			WHERE id = $1 AND revision = $8
		`,
			inv.ID, string(inv.Status), inv.PaidAmount, inv.PaidAt,
			inv.VoidedAt, inv.VoidReason, inv.Notes, *ifRevision,
		)
	} else {
		tag, err = r.pool.Exec(ctx, `
			UPDATE enterprise.invoices
			SET status = $2, paid_amount = $3, paid_at = $4,
			    voided_at = $5, void_reason = $6, notes = $7,
			    revision = revision + 1, updated_at = NOW()
			WHERE id = $1
		`,
			inv.ID, string(inv.Status), inv.PaidAmount, inv.PaidAt,
			inv.VoidedAt, inv.VoidReason, inv.Notes,
		)
	}
	if err != nil {
		return mapDBError(err, "invoice", "update invoice")
	}
	if tag.RowsAffected() == 0 {
		if ifRevision != nil {
			return derrors.Conflict(
				"invoice.revision_mismatch",
				"invoice has been modified by another request — refresh and retry",
			)
		}
		return derrors.NotFound("invoice.not_found", "invoice not found")
	}
	return nil
}

func scanInvoice(row pgx.Row) (domain.Invoice, error) {
	var (
		inv    domain.Invoice
		status string
	)
	err := row.Scan(
		&inv.ID, &inv.InvoiceNumber, &inv.QuotationID, &inv.OpportunityID, &inv.BOQVersionID,
		&status,
		&inv.TotalAmount, &inv.SubtotalAmount, &inv.TaxPct, &inv.TaxAmount,
		&inv.PaidAmount, &inv.Currency,
		&inv.IssuedAt, &inv.DueAt, &inv.PaidAt, &inv.VoidedAt, &inv.VoidReason, &inv.Notes,
		&inv.IssuedBy,
		&inv.InvoicePlanID, &inv.InvoicePlanItemID,
		&inv.Revision, &inv.CreatedAt, &inv.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Invoice{}, derrors.NotFound("invoice.not_found", "invoice not found")
	}
	if err != nil {
		return domain.Invoice{}, derrors.Wrap(derrors.KindInternal, "db.invoice_scan", "scan invoice", err)
	}
	inv.Status = domain.InvoiceStatus(status)
	return inv, nil
}

// =====================================================================
// InvoicePaymentRepository
// =====================================================================

type InvoicePaymentRepository struct {
	pool *pgxpool.Pool
}

func NewInvoicePaymentRepository(pool *pgxpool.Pool) *InvoicePaymentRepository {
	return &InvoicePaymentRepository{pool: pool}
}

var _ port.InvoicePaymentRepository = (*InvoicePaymentRepository)(nil)

const paymentCols = `
	id, invoice_id, amount, method, COALESCE(reference, ''), paid_at,
	COALESCE(notes, ''), recorded_by, created_at
`

func (r *InvoicePaymentRepository) ListByInvoice(ctx context.Context, invoiceID uuid.UUID) ([]domain.InvoicePayment, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+paymentCols+`
		FROM enterprise.invoice_payments
		WHERE invoice_id = $1
		ORDER BY paid_at DESC, created_at DESC
	`, invoiceID)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.invoice_payments_list", "list payments", err)
	}
	defer rows.Close()
	out := []domain.InvoicePayment{}
	for rows.Next() {
		var (
			p      domain.InvoicePayment
			method string
		)
		if err := rows.Scan(
			&p.ID, &p.InvoiceID, &p.Amount, &method, &p.Reference, &p.PaidAt,
			&p.Notes, &p.RecordedBy, &p.CreatedAt,
		); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "db.invoice_payment_scan", "scan", err)
		}
		p.Method = domain.PaymentMethod(method)
		out = append(out, p)
	}
	return out, nil
}

func (r *InvoicePaymentRepository) Create(ctx context.Context, p *domain.InvoicePayment) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO enterprise.invoice_payments
			(id, invoice_id, amount, method, reference, paid_at, notes, recorded_by, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`,
		p.ID, p.InvoiceID, p.Amount, string(p.Method), p.Reference,
		p.PaidAt, p.Notes, p.RecordedBy, p.CreatedAt,
	)
	if err != nil {
		return mapDBError(err, "invoice_payment", "insert payment")
	}
	return nil
}

// =====================================================================
// EWORepository
// =====================================================================

type EWORepository struct {
	pool *pgxpool.Pool
}

func NewEWORepository(pool *pgxpool.Pool) *EWORepository {
	return &EWORepository{pool: pool}
}

var _ port.EWORepository = (*EWORepository)(nil)

const ewoCols = `
	id, ewo_number, quotation_id, opportunity_id, boq_version_id, status,
	assigned_to, started_at, completed_at, cancelled_at,
	COALESCE(cancel_reason, ''), COALESCE(notes, ''),
	COALESCE(progress_pct, 0), field_work_order_id,
	revision, created_at, updated_at
`

func (r *EWORepository) List(ctx context.Context, f port.EWOListFilter) ([]domain.EWO, int, error) {
	var wh []string
	var args []any
	if f.Status != "" {
		args = append(args, f.Status)
		wh = append(wh, fmt.Sprintf("status = $%d", len(args)))
	}
	if f.OpportunityID != nil {
		args = append(args, *f.OpportunityID)
		wh = append(wh, fmt.Sprintf("opportunity_id = $%d", len(args)))
	}
	if f.QuotationID != nil {
		args = append(args, *f.QuotationID)
		wh = append(wh, fmt.Sprintf("quotation_id = $%d", len(args)))
	}
	if f.AssignedTo != nil {
		args = append(args, *f.AssignedTo)
		wh = append(wh, fmt.Sprintf("assigned_to = $%d", len(args)))
	}
	where := ""
	if len(wh) > 0 {
		where = " WHERE " + strings.Join(wh, " AND ")
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}

	var total int
	if err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM enterprise.ewos`+where, args...,
	).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.ewo_count", "count", err)
	}

	args = append(args, limit, f.Offset)
	sql := `SELECT ` + ewoCols + ` FROM enterprise.ewos` + where +
		` ORDER BY created_at DESC LIMIT $` + fmt.Sprint(len(args)-1) +
		` OFFSET $` + fmt.Sprint(len(args))
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.ewo_list", "list", err)
	}
	defer rows.Close()
	out := []domain.EWO{}
	for rows.Next() {
		e, err := scanEWO(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, e)
	}
	return out, total, nil
}

func (r *EWORepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.EWO, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+ewoCols+` FROM enterprise.ewos WHERE id = $1`, id)
	e, err := scanEWO(row)
	if err != nil {
		return nil, err
	}
	return &e, nil
}

func (r *EWORepository) FindByQuotationID(ctx context.Context, quotationID uuid.UUID) (*domain.EWO, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+ewoCols+` FROM enterprise.ewos WHERE quotation_id = $1`, quotationID)
	e, err := scanEWO(row)
	if err != nil {
		return nil, err
	}
	return &e, nil
}

func (r *EWORepository) Create(ctx context.Context, e *domain.EWO) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO enterprise.ewos
			(id, ewo_number, quotation_id, opportunity_id, boq_version_id, status,
			 assigned_to, started_at, completed_at, cancelled_at,
			 cancel_reason, notes, progress_pct, field_work_order_id,
			 revision, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
	`,
		e.ID, e.EWONumber, e.QuotationID, e.OpportunityID, e.BOQVersionID, string(e.Status),
		e.AssignedTo, e.StartedAt, e.CompletedAt, e.CancelledAt,
		e.CancelReason, e.Notes, e.ProgressPct, e.FieldWorkOrderID,
		e.Revision, e.CreatedAt, e.UpdatedAt,
	)
	if err != nil {
		return mapDBError(err, "ewo", "insert ewo")
	}
	return nil
}

// LogCompletion drops a fresh row into enterprise.ewo_completion_log so
// the vendor-metrics derivation cron has something to roll up. We pull
// the vendor (assigned_provider_company_id) from any boq_line on the
// EWO's BOQ version — most EWOs cover a single vendor's lines, and the
// derivation tolerates nulls (rows without a vendor are skipped). The
// insert is best-effort: if the row already exists or the FK isn't
// present, we swallow the error rather than block the completion.
func (r *EWORepository) LogCompletion(ctx context.Context, ewoID uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO enterprise.ewo_completion_log
			(ewo_id, vendor_id, planned_finish, actual_finish)
		SELECT
			e.id,
			(SELECT bl.assigned_provider_company_id
			   FROM enterprise.boq_lines bl
			  WHERE bl.boq_version_id = e.boq_version_id
			    AND bl.assigned_provider_company_id IS NOT NULL
			  LIMIT 1),
			NULL,
			COALESCE(e.completed_at, NOW())
		FROM enterprise.ewos e
		WHERE e.id = $1
	`, ewoID)
	if err != nil {
		return mapDBError(err, "ewo_completion_log", "insert completion log")
	}
	return nil
}

func (r *EWORepository) Update(ctx context.Context, e *domain.EWO) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE enterprise.ewos
		SET status = $2, assigned_to = $3,
		    started_at = $4, completed_at = $5, cancelled_at = $6,
		    cancel_reason = $7, notes = $8,
		    progress_pct = $9, field_work_order_id = $10,
		    revision = revision + 1, updated_at = NOW()
		WHERE id = $1
	`,
		e.ID, string(e.Status), e.AssignedTo,
		e.StartedAt, e.CompletedAt, e.CancelledAt,
		e.CancelReason, e.Notes,
		e.ProgressPct, e.FieldWorkOrderID,
	)
	if err != nil {
		return mapDBError(err, "ewo", "update ewo")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("ewo.not_found", "ewo not found")
	}
	return nil
}

func scanEWO(row pgx.Row) (domain.EWO, error) {
	var (
		e      domain.EWO
		status string
	)
	err := row.Scan(
		&e.ID, &e.EWONumber, &e.QuotationID, &e.OpportunityID, &e.BOQVersionID, &status,
		&e.AssignedTo, &e.StartedAt, &e.CompletedAt, &e.CancelledAt,
		&e.CancelReason, &e.Notes,
		&e.ProgressPct, &e.FieldWorkOrderID,
		&e.Revision, &e.CreatedAt, &e.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.EWO{}, derrors.NotFound("ewo.not_found", "ewo not found")
	}
	if err != nil {
		return domain.EWO{}, derrors.Wrap(derrors.KindInternal, "db.ewo_scan", "scan ewo", err)
	}
	e.Status = domain.EWOStatus(status)
	return e, nil
}

// silence unused: time import is used in domain only when scanning
// optional times via *time.Time pointers above. Keeping the import for
// future extensions to this file.
var _ = time.Now
