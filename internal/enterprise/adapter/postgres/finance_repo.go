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
	revision, created_at, updated_at,
	tax_snapshot_hash, faktur_pajak_id,
	reminder_sent_at, COALESCE(pph23_withheld_amount, 0), COALESCE(is_pph23_applicable, FALSE)
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
			 revision, created_at, updated_at,
			 tax_snapshot_hash, faktur_pajak_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12,
		        $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24,
		        $25, $26)
	`,
		inv.ID, inv.InvoiceNumber, inv.QuotationID, inv.OpportunityID, inv.BOQVersionID,
		string(inv.Status),
		inv.TotalAmount, inv.SubtotalAmount, inv.TaxPct, inv.TaxAmount,
		inv.PaidAmount, inv.Currency,
		inv.IssuedAt, inv.DueAt, inv.PaidAt, inv.VoidedAt, inv.VoidReason, inv.Notes,
		inv.IssuedBy,
		inv.InvoicePlanID, inv.InvoicePlanItemID,
		inv.Revision, inv.CreatedAt, inv.UpdatedAt,
		inv.TaxSnapshotHash, inv.FakturPajakID,
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
			    reminder_sent_at = $9,
			    pph23_withheld_amount = $10, is_pph23_applicable = $11,
			    revision = revision + 1, updated_at = NOW()
			WHERE id = $1 AND revision = $8
		`,
			inv.ID, string(inv.Status), inv.PaidAmount, inv.PaidAt,
			inv.VoidedAt, inv.VoidReason, inv.Notes, *ifRevision,
			inv.ReminderSentAt, inv.PPh23WithheldAmount, inv.IsPPh23Applicable,
		)
	} else {
		tag, err = r.pool.Exec(ctx, `
			UPDATE enterprise.invoices
			SET status = $2, paid_amount = $3, paid_at = $4,
			    voided_at = $5, void_reason = $6, notes = $7,
			    reminder_sent_at = $8,
			    pph23_withheld_amount = $9, is_pph23_applicable = $10,
			    revision = revision + 1, updated_at = NOW()
			WHERE id = $1
		`,
			inv.ID, string(inv.Status), inv.PaidAmount, inv.PaidAt,
			inv.VoidedAt, inv.VoidReason, inv.Notes,
			inv.ReminderSentAt, inv.PPh23WithheldAmount, inv.IsPPh23Applicable,
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
		&inv.TaxSnapshotHash, &inv.FakturPajakID,
		&inv.ReminderSentAt, &inv.PPh23WithheldAmount, &inv.IsPPh23Applicable,
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
	revision, created_at, updated_at,
	COALESCE(side, 'x'),
	executing_subsidiary_id, intercompany_po_id, paired_ewo_id,
	scheduled_start_date, scheduled_end_date, duration_days,
	assigned_technician_user_id, assigned_team_lead_user_id,
	COALESCE(schedule_locked, false)
`

func (r *EWORepository) List(ctx context.Context, f port.EWOListFilter) ([]domain.EWO, int, error) {
	where, args := buildEWOFilter(f)
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
	side := string(e.Side)
	if side == "" {
		side = "x"
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO enterprise.ewos
			(id, ewo_number, quotation_id, opportunity_id, boq_version_id, status,
			 assigned_to, started_at, completed_at, cancelled_at,
			 cancel_reason, notes, progress_pct, field_work_order_id,
			 revision, created_at, updated_at,
			 side, executing_subsidiary_id, intercompany_po_id, paired_ewo_id,
			 scheduled_start_date, scheduled_end_date, duration_days,
			 assigned_technician_user_id, assigned_team_lead_user_id,
			 schedule_locked)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17,
		        $18, $19, $20, $21, $22, $23, $24, $25, $26, $27)
	`,
		e.ID, e.EWONumber, e.QuotationID, e.OpportunityID, e.BOQVersionID, string(e.Status),
		e.AssignedTo, e.StartedAt, e.CompletedAt, e.CancelledAt,
		e.CancelReason, e.Notes, e.ProgressPct, e.FieldWorkOrderID,
		e.Revision, e.CreatedAt, e.UpdatedAt,
		side, e.ExecutingSubsidiaryID, e.IntercompanyPOID, e.PairedEWOID,
		e.ScheduledStartDate, e.ScheduledEndDate, e.DurationDays,
		e.AssignedTechnicianUserID, e.AssignedTeamLeadUserID,
		e.ScheduleLocked,
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
		side   string
	)
	err := row.Scan(
		&e.ID, &e.EWONumber, &e.QuotationID, &e.OpportunityID, &e.BOQVersionID, &status,
		&e.AssignedTo, &e.StartedAt, &e.CompletedAt, &e.CancelledAt,
		&e.CancelReason, &e.Notes,
		&e.ProgressPct, &e.FieldWorkOrderID,
		&e.Revision, &e.CreatedAt, &e.UpdatedAt,
		&side,
		&e.ExecutingSubsidiaryID, &e.IntercompanyPOID, &e.PairedEWOID,
		&e.ScheduledStartDate, &e.ScheduledEndDate, &e.DurationDays,
		&e.AssignedTechnicianUserID, &e.AssignedTeamLeadUserID,
		&e.ScheduleLocked,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.EWO{}, derrors.NotFound("ewo.not_found", "ewo not found")
	}
	if err != nil {
		return domain.EWO{}, derrors.Wrap(derrors.KindInternal, "db.ewo_scan", "scan ewo", err)
	}
	e.Status = domain.EWOStatus(status)
	e.Side = domain.EWOSide(side)
	return e, nil
}

// buildEWOFilter centralises the WHERE-clause builder so List + FindBySide
// share the same predicates. Returns the rendered " WHERE ... " fragment
// (empty when no filters) and the positional args.
func buildEWOFilter(f port.EWOListFilter) (string, []any) {
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
	if f.Side != "" {
		args = append(args, f.Side)
		wh = append(wh, fmt.Sprintf("side = $%d", len(args)))
	}
	if f.ExecutingSubsidiaryID != nil {
		args = append(args, *f.ExecutingSubsidiaryID)
		wh = append(wh, fmt.Sprintf("executing_subsidiary_id = $%d", len(args)))
	}
	if f.IntercompanyPOID != nil {
		args = append(args, *f.IntercompanyPOID)
		wh = append(wh, fmt.Sprintf("intercompany_po_id = $%d", len(args)))
	}
	if f.PairedEWOID != nil {
		args = append(args, *f.PairedEWOID)
		wh = append(wh, fmt.Sprintf("paired_ewo_id = $%d", len(args)))
	}
	if f.AssignedTeamLeadUserID != nil {
		args = append(args, *f.AssignedTeamLeadUserID)
		wh = append(wh, fmt.Sprintf("assigned_team_lead_user_id = $%d", len(args)))
	}
	if f.AssignedTechnicianUserID != nil {
		args = append(args, *f.AssignedTechnicianUserID)
		wh = append(wh, fmt.Sprintf("assigned_technician_user_id = $%d", len(args)))
	}
	if f.ScheduledFrom != nil {
		args = append(args, *f.ScheduledFrom)
		wh = append(wh, fmt.Sprintf("scheduled_end_date >= $%d", len(args)))
	}
	if f.ScheduledTo != nil {
		args = append(args, *f.ScheduledTo)
		wh = append(wh, fmt.Sprintf("scheduled_start_date <= $%d", len(args)))
	}
	if len(wh) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(wh, " AND "), args
}

// FindBySide returns EWOs filtered by side, layered with the standard
// filter predicates. No pagination — used by auto-spawn path which
// expects a small result set.
func (r *EWORepository) FindBySide(
	ctx context.Context,
	side domain.EWOSide,
	f port.EWOListFilter,
) ([]domain.EWO, error) {
	f.Side = string(side)
	where, args := buildEWOFilter(f)
	sql := `SELECT ` + ewoCols + ` FROM enterprise.ewos` + where + ` ORDER BY created_at DESC LIMIT 100`
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.ewo_find_by_side", "find by side", err)
	}
	defer rows.Close()
	out := []domain.EWO{}
	for rows.Next() {
		e, err := scanEWO(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

// FindByPair walks the paired_ewo_id pointer one hop. Returns NotFound
// if the current EWO has no pair set.
func (r *EWORepository) FindByPair(ctx context.Context, ewoID uuid.UUID) (*domain.EWO, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+ewoCols+`
		 FROM enterprise.ewos
		 WHERE id = (SELECT paired_ewo_id FROM enterprise.ewos WHERE id = $1)
		   AND paired_ewo_id IS NOT NULL`, ewoID,
	)
	e, err := scanEWO(row)
	if err != nil {
		return nil, err
	}
	return &e, nil
}

// UpdateSchedule writes only the scheduling columns. We keep Update
// (status/assignment) and this method separate so audit trails can
// distinguish "Bob changed status" from "Carol changed schedule".
func (r *EWORepository) UpdateSchedule(
	ctx context.Context,
	ewoID uuid.UUID,
	sched port.ScheduleUpdate,
) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE enterprise.ewos
		SET scheduled_start_date = $2, scheduled_end_date = $3,
		    duration_days = $4,
		    assigned_team_lead_user_id = $5,
		    assigned_technician_user_id = $6,
		    revision = revision + 1, updated_at = NOW()
		WHERE id = $1
	`,
		ewoID, sched.ScheduledStart, sched.ScheduledEnd,
		sched.DurationDays, sched.TeamLead, sched.Technician,
	)
	if err != nil {
		return mapDBError(err, "ewo", "update schedule")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("ewo.not_found", "ewo not found")
	}
	return nil
}

// LockSchedule flips schedule_locked → true without touching status or
// the schedule columns. Idempotent.
func (r *EWORepository) LockSchedule(ctx context.Context, ewoID uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE enterprise.ewos
		SET schedule_locked = TRUE,
		    revision = revision + 1, updated_at = NOW()
		WHERE id = $1
	`, ewoID)
	if err != nil {
		return mapDBError(err, "ewo", "lock schedule")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("ewo.not_found", "ewo not found")
	}
	return nil
}

// UpdatePair stamps paired_ewo_id on a single row. The auto-spawn path
// calls this twice (once for X → Y, once for Y → X) because LinkPair
// is symmetric but persistence is not.
func (r *EWORepository) UpdatePair(ctx context.Context, ewoID, pairedID uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE enterprise.ewos
		SET paired_ewo_id = $2,
		    revision = revision + 1, updated_at = NOW()
		WHERE id = $1
	`, ewoID, pairedID)
	if err != nil {
		return mapDBError(err, "ewo", "update pair")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("ewo.not_found", "ewo not found")
	}
	return nil
}

// FindOverlappingForTeamLead surfaces EWOs whose schedule window
// intersects [start, end] for the same team lead. Two windows overlap
// when start_a < end_b AND start_b < end_a. excludeEWOID is honored
// when set so Reschedule doesn't conflict with itself.
func (r *EWORepository) FindOverlappingForTeamLead(
	ctx context.Context,
	teamLeadID uuid.UUID,
	start, end time.Time,
	excludeEWOID *uuid.UUID,
) ([]domain.EWO, error) {
	q := `SELECT ` + ewoCols + `
		FROM enterprise.ewos
		WHERE assigned_team_lead_user_id = $1
		  AND scheduled_start_date IS NOT NULL
		  AND scheduled_end_date   IS NOT NULL
		  AND scheduled_start_date < $3
		  AND scheduled_end_date   > $2
		  AND status IN ('pending', 'in_progress')`
	args := []any{teamLeadID, start, end}
	if excludeEWOID != nil {
		q += ` AND id <> $4`
		args = append(args, *excludeEWOID)
	}
	q += ` ORDER BY scheduled_start_date`
	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.ewo_overlap", "find overlapping", err)
	}
	defer rows.Close()
	out := []domain.EWO{}
	for rows.Next() {
		e, err := scanEWO(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

// silence unused: time import is used in domain only when scanning
// optional times via *time.Time pointers above. Keeping the import for
// future extensions to this file.
var _ = time.Now
