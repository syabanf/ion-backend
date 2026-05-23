package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/reseller/domain"
	"github.com/ion-core/backend/internal/reseller/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// SubscriberInvoiceRepository implements port.SubscriberInvoiceRepository
// against `reseller.subscriber_invoices`. Tenant guard: every read /
// write refuses uuid.Nil tenant filter — same contract as
// SubscriberRepository.
type SubscriberInvoiceRepository struct {
	pool *pgxpool.Pool
}

func NewSubscriberInvoiceRepository(pool *pgxpool.Pool) *SubscriberInvoiceRepository {
	return &SubscriberInvoiceRepository{pool: pool}
}

var _ port.SubscriberInvoiceRepository = (*SubscriberInvoiceRepository)(nil)

const invoiceCols = `
	id, reseller_account_id, subscriber_id, invoice_no,
	COALESCE(period_year, 0), COALESCE(period_month, 0),
	COALESCE(amount, 0),
	status,
	issued_at, due_at, paid_at,
	created_at, updated_at
`

func (r *SubscriberInvoiceRepository) Create(ctx context.Context, i *domain.SubscriberInvoice) error {
	if i.ResellerAccountID == uuid.Nil {
		return derrors.Validation("invoice.reseller_required", "reseller_account_id is required")
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO reseller.subscriber_invoices
			(id, reseller_account_id, subscriber_id, invoice_no,
			 period_year, period_month, amount, status,
			 issued_at, due_at, paid_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
	`,
		i.ID, i.ResellerAccountID, i.SubscriberID, i.InvoiceNo,
		nullableInt(i.PeriodYear), nullableInt(i.PeriodMonth),
		i.Amount, string(i.Status),
		i.IssuedAt, i.DueAt, i.PaidAt, i.CreatedAt, i.UpdatedAt,
	)
	if err != nil {
		return mapDBError(err, "invoice", "insert subscriber invoice")
	}
	return nil
}

func (r *SubscriberInvoiceRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.SubscriberInvoice, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+invoiceCols+` FROM reseller.subscriber_invoices WHERE id = $1`, id)
	i, err := scanInvoice(row)
	if err != nil {
		return nil, err
	}
	return &i, nil
}

func (r *SubscriberInvoiceRepository) FindForReseller(ctx context.Context, resellerID, id uuid.UUID) (*domain.SubscriberInvoice, error) {
	if resellerID == uuid.Nil {
		return nil, derrors.Validation("invoice.reseller_required", "reseller_account_id is required")
	}
	row := r.pool.QueryRow(ctx,
		`SELECT `+invoiceCols+` FROM reseller.subscriber_invoices
		 WHERE reseller_account_id = $1 AND id = $2`,
		resellerID, id)
	i, err := scanInvoice(row)
	if err != nil {
		return nil, err
	}
	return &i, nil
}

func (r *SubscriberInvoiceRepository) List(ctx context.Context, f port.InvoiceListFilter) ([]domain.SubscriberInvoice, int, error) {
	if f.ResellerAccountID == uuid.Nil {
		return nil, 0, derrors.Validation("invoice.tenant_filter_required", "reseller_account_id filter is required")
	}
	args := []any{f.ResellerAccountID}
	wh := []string{"reseller_account_id = $1"}
	if f.SubscriberID != nil {
		args = append(args, *f.SubscriberID)
		wh = append(wh, fmt.Sprintf("subscriber_id = $%d", len(args)))
	}
	if f.Status != "" {
		args = append(args, f.Status)
		wh = append(wh, fmt.Sprintf("status = $%d", len(args)))
	}
	if f.PeriodYear != 0 {
		args = append(args, f.PeriodYear)
		wh = append(wh, fmt.Sprintf("period_year = $%d", len(args)))
	}
	if f.PeriodMonth != 0 {
		args = append(args, f.PeriodMonth)
		wh = append(wh, fmt.Sprintf("period_month = $%d", len(args)))
	}
	where := " WHERE " + strings.Join(wh, " AND ")

	var total int
	if err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM reseller.subscriber_invoices`+where, args...,
	).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.invoice_count", "count invoices", err)
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
	sql := `SELECT ` + invoiceCols + ` FROM reseller.subscriber_invoices` + where +
		` ORDER BY issued_at DESC LIMIT $` + fmt.Sprint(len(args)-1) +
		` OFFSET $` + fmt.Sprint(len(args))

	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.invoice_list", "list invoices", err)
	}
	defer rows.Close()
	out := []domain.SubscriberInvoice{}
	for rows.Next() {
		i, err := scanInvoice(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, i)
	}
	return out, total, nil
}

func (r *SubscriberInvoiceRepository) UpdateStatus(ctx context.Context, i *domain.SubscriberInvoice) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE reseller.subscriber_invoices
		SET status = $2,
		    paid_at = $3,
		    updated_at = $4
		WHERE id = $1 AND reseller_account_id = $5
	`,
		i.ID, string(i.Status), i.PaidAt, i.UpdatedAt, i.ResellerAccountID,
	)
	if err != nil {
		return mapDBError(err, "invoice", "update invoice status")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("invoice.not_found", "invoice not found")
	}
	return nil
}

// ListOverdueForReseller pulls invoices whose status is still 'open'
// AND due_at has passed `asOf`. The status filter on 'open' is
// deliberate — if a future cron flips them to 'overdue' the dashboard
// counts already-overdue rows separately via List(status=overdue).
func (r *SubscriberInvoiceRepository) ListOverdueForReseller(ctx context.Context, resellerID uuid.UUID, asOf time.Time) ([]domain.SubscriberInvoice, error) {
	if resellerID == uuid.Nil {
		return nil, derrors.Validation("invoice.tenant_filter_required", "reseller_account_id filter is required")
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+invoiceCols+`
		FROM reseller.subscriber_invoices
		WHERE reseller_account_id = $1
		  AND status IN ('open','overdue')
		  AND due_at IS NOT NULL
		  AND due_at < $2
		ORDER BY due_at ASC
	`, resellerID, asOf)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.invoice_overdue", "list overdue", err)
	}
	defer rows.Close()
	out := []domain.SubscriberInvoice{}
	for rows.Next() {
		i, err := scanInvoice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, nil
}

// SumPaidMTD sums `amount` for paid invoices whose paid_at falls
// within [monthStart, asOf]. NULL paid_at is excluded (defensive — a
// paid invoice without paid_at would be a data bug).
func (r *SubscriberInvoiceRepository) SumPaidMTD(ctx context.Context, resellerID uuid.UUID, monthStart, asOf time.Time) (float64, error) {
	if resellerID == uuid.Nil {
		return 0, derrors.Validation("invoice.tenant_filter_required", "reseller_account_id filter is required")
	}
	var sum float64
	err := r.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(amount), 0)
		FROM reseller.subscriber_invoices
		WHERE reseller_account_id = $1
		  AND status = 'paid'
		  AND paid_at IS NOT NULL
		  AND paid_at >= $2 AND paid_at <= $3
	`, resellerID, monthStart, asOf).Scan(&sum)
	if err != nil {
		return 0, derrors.Wrap(derrors.KindInternal, "db.invoice_sum_paid", "sum paid MTD", err)
	}
	return sum, nil
}

// SumOpen returns the sum of amount across status='open' invoices for
// the tenant. Drives the dashboard tile "open invoices outstanding".
func (r *SubscriberInvoiceRepository) SumOpen(ctx context.Context, resellerID uuid.UUID) (float64, error) {
	if resellerID == uuid.Nil {
		return 0, derrors.Validation("invoice.tenant_filter_required", "reseller_account_id filter is required")
	}
	var sum float64
	err := r.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(amount), 0)
		FROM reseller.subscriber_invoices
		WHERE reseller_account_id = $1
		  AND status = 'open'
	`, resellerID).Scan(&sum)
	if err != nil {
		return 0, derrors.Wrap(derrors.KindInternal, "db.invoice_sum_open", "sum open", err)
	}
	return sum, nil
}

func scanInvoice(row pgx.Row) (domain.SubscriberInvoice, error) {
	var i domain.SubscriberInvoice
	var status string
	err := row.Scan(
		&i.ID, &i.ResellerAccountID, &i.SubscriberID, &i.InvoiceNo,
		&i.PeriodYear, &i.PeriodMonth,
		&i.Amount,
		&status,
		&i.IssuedAt, &i.DueAt, &i.PaidAt,
		&i.CreatedAt, &i.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.SubscriberInvoice{}, derrors.NotFound("invoice.not_found", "invoice not found")
	}
	if err != nil {
		return domain.SubscriberInvoice{}, derrors.Wrap(derrors.KindInternal, "db.invoice_scan", "scan invoice", err)
	}
	i.Status = domain.InvoiceStatus(status)
	return i, nil
}

// nullableInt returns nil for zero so we don't write 0 into nullable
// integer columns (period_year, period_month). Symmetric with
// nullableString in reseller_account_repo.go.
func nullableInt(n int) any {
	if n == 0 {
		return nil
	}
	return n
}
