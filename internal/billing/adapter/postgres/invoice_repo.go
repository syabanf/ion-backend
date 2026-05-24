package postgres

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/billing/domain"
	"github.com/ion-core/backend/internal/billing/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type InvoiceRepository struct {
	pool *pgxpool.Pool
}

func NewInvoiceRepository(pool *pgxpool.Pool) *InvoiceRepository {
	return &InvoiceRepository{pool: pool}
}

var _ port.InvoiceRepository = (*InvoiceRepository)(nil)

const invoiceSelect = `
SELECT i.id, i.invoice_number, i.customer_id, i.order_id,
       i.invoice_type, i.invoice_date, i.due_date,
       i.subtotal, i.ppn_rate, i.ppn_amount, i.total,
       i.status, i.paid_at, COALESCE(i.notes,''),
       i.created_by, i.created_at, i.updated_at,
       COALESCE(c.full_name,''), COALESCE(c.customer_number,''),
       COALESCE(o.order_number,'')
FROM billing.invoices i
LEFT JOIN crm.customers c ON c.id = i.customer_id
LEFT JOIN crm.orders    o ON o.id = i.order_id
`

func (r *InvoiceRepository) Create(ctx context.Context, inv *domain.Invoice, lines []domain.LineItem) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "db.tx_begin", "begin tx", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		INSERT INTO billing.invoices (
			id, invoice_number, customer_id, order_id, invoice_type,
			invoice_date, due_date, subtotal, ppn_rate, ppn_amount, total,
			status, paid_at, notes, created_by, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$16)
	`,
		inv.ID, inv.InvoiceNumber, inv.CustomerID, inv.OrderID, string(inv.InvoiceType),
		inv.InvoiceDate, inv.DueDate, inv.Subtotal, inv.PPNRate, inv.PPNAmount, inv.Total,
		string(inv.Status), inv.PaidAt, nullableString(inv.Notes), inv.CreatedBy, inv.CreatedAt,
	); err != nil {
		return mapDBError(err, "invoice.create", "create invoice")
	}

	for _, l := range lines {
		if _, err := tx.Exec(ctx, `
			INSERT INTO billing.invoice_items (id, invoice_id, line_order, description, item_type, quantity, unit_price, amount)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		`, l.ID, l.InvoiceID, l.LineOrder, l.Description, l.ItemType, l.Quantity, l.UnitPrice, l.Amount); err != nil {
			return mapDBError(err, "invoice.line", "create invoice line")
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return derrors.Wrap(derrors.KindInternal, "db.tx_commit", "commit tx", err)
	}
	return nil
}

func (r *InvoiceRepository) UpdateStatus(ctx context.Context, id uuid.UUID, status domain.InvoiceStatus, paidAt *time.Time) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE billing.invoices SET status = $2, paid_at = $3, updated_at = NOW() WHERE id = $1`,
		id, string(status), paidAt)
	if err != nil {
		return mapDBError(err, "invoice.update_status", "update invoice status")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("invoice.not_found", "invoice not found")
	}
	return nil
}

func (r *InvoiceRepository) FindByID(ctx context.Context, id uuid.UUID) (*port.InvoiceView, error) {
	row := r.pool.QueryRow(ctx, invoiceSelect+" WHERE i.id = $1", id)
	v, err := scanInvoice(row)
	if err != nil {
		return nil, err
	}
	lines, err := r.linesForInvoice(ctx, id)
	if err != nil {
		return nil, err
	}
	pays, err := r.paymentsForInvoice(ctx, id)
	if err != nil {
		return nil, err
	}
	v.Lines = lines
	v.Payments = pays
	v.PaidAmount = sumConfirmed(pays)
	v.OutstandingAmount = clampZero(v.Invoice.Total - v.PaidAmount)
	return v, nil
}

func (r *InvoiceRepository) FindOTCForOrder(ctx context.Context, orderID uuid.UUID) (*port.InvoiceView, error) {
	row := r.pool.QueryRow(ctx, invoiceSelect+`
		WHERE i.order_id = $1 AND i.invoice_type = 'otc'
		LIMIT 1`, orderID)
	v, err := scanInvoice(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		// scanInvoice translates pgx.ErrNoRows to NotFound; that's the
		// path we still want to treat as nil.
		var de *derrors.Error
		if errors.As(err, &de) && de.Code == "invoice.not_found" {
			return nil, nil
		}
		return nil, err
	}
	pays, err := r.paymentsForInvoice(ctx, v.Invoice.ID)
	if err != nil {
		return nil, err
	}
	v.Payments = pays
	v.PaidAmount = sumConfirmed(pays)
	v.OutstandingAmount = clampZero(v.Invoice.Total - v.PaidAmount)
	return v, nil
}

func (r *InvoiceRepository) List(ctx context.Context, f port.InvoiceListFilter) ([]port.InvoiceView, int, error) {
	var (
		args  []any
		conds []string
	)
	if f.Status != "" {
		args = append(args, f.Status)
		conds = append(conds, "i.status = $"+itoa(len(args)))
	}
	if f.InvoiceType != "" {
		args = append(args, f.InvoiceType)
		conds = append(conds, "i.invoice_type = $"+itoa(len(args)))
	}
	if f.CustomerID != nil {
		args = append(args, *f.CustomerID)
		conds = append(conds, "i.customer_id = $"+itoa(len(args)))
	}
	if f.OrderID != nil {
		args = append(args, *f.OrderID)
		conds = append(conds, "i.order_id = $"+itoa(len(args)))
	}
	if s := strings.TrimSpace(f.Search); s != "" {
		args = append(args, "%"+s+"%")
		conds = append(conds, "(i.invoice_number ILIKE $"+itoa(len(args))+" OR c.full_name ILIKE $"+itoa(len(args))+")")
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}

	var total int
	if err := r.pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM billing.invoices i LEFT JOIN crm.customers c ON c.id=i.customer_id"+where, args...,
	).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.invoice_count", "count invoices", err)
	}

	if f.Limit <= 0 {
		f.Limit = 50
	}
	sql := invoiceSelect + where + " ORDER BY i.created_at DESC LIMIT $" + itoa(len(args)+1) + " OFFSET $" + itoa(len(args)+2)
	args = append(args, f.Limit, f.Offset)
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.invoice_list", "list invoices", err)
	}
	defer rows.Close()
	out := []port.InvoiceView{}
	for rows.Next() {
		v, err := scanInvoice(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *v)
	}
	// Populate PaidAmount + OutstandingAmount for each returned view.
	// scanInvoice can't do this — it only sees the row, not the related
	// payments. Wave 128 (late-fee cron observability): RunLateFeeTick
	// reads OutstandingAmount off the List view to decide eligibility;
	// when this stayed zero the tick silently no-op'd. We backfill with
	// one aggregate query keyed on the page we just returned.
	if len(out) > 0 {
		ids := make([]uuid.UUID, len(out))
		for i := range out {
			ids[i] = out[i].Invoice.ID
		}
		paidByInv, perr := r.sumPaidForInvoices(ctx, ids)
		if perr != nil {
			return nil, 0, perr
		}
		for i := range out {
			paid := paidByInv[out[i].Invoice.ID]
			out[i].PaidAmount = paid
			out[i].OutstandingAmount = clampZero(out[i].Invoice.Total - paid)
		}
	}
	return out, total, nil
}

// sumPaidForInvoices returns a per-invoice sum of confirmed payments.
// Empty input yields an empty map. Used by List() to backfill
// PaidAmount / OutstandingAmount without an N+1 query.
func (r *InvoiceRepository) sumPaidForInvoices(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]float64, error) {
	out := make(map[uuid.UUID]float64, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT invoice_id, COALESCE(SUM(amount), 0)
		FROM billing.payments
		WHERE invoice_id = ANY($1) AND status = 'confirmed'
		GROUP BY invoice_id
	`, ids)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.payment_sum_list", "sum payments for invoices", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id   uuid.UUID
			sum  float64
		)
		if err := rows.Scan(&id, &sum); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "db.payment_sum_scan", "scan payment sum", err)
		}
		out[id] = sum
	}
	return out, nil
}

func (r *InvoiceRepository) linesForInvoice(ctx context.Context, invoiceID uuid.UUID) ([]domain.LineItem, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, invoice_id, line_order, description, item_type, quantity, unit_price, amount
		FROM billing.invoice_items WHERE invoice_id = $1 ORDER BY line_order
	`, invoiceID)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.line_list", "list lines", err)
	}
	defer rows.Close()
	out := []domain.LineItem{}
	for rows.Next() {
		var l domain.LineItem
		if err := rows.Scan(&l.ID, &l.InvoiceID, &l.LineOrder, &l.Description, &l.ItemType,
			&l.Quantity, &l.UnitPrice, &l.Amount); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "db.line_scan", "scan line", err)
		}
		out = append(out, l)
	}
	return out, nil
}

func (r *InvoiceRepository) paymentsForInvoice(ctx context.Context, invoiceID uuid.UUID) ([]domain.Payment, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, invoice_id, customer_id, amount, payment_method,
		       COALESCE(gateway_transaction_id,''), payment_date, confirmed_by,
		       status, COALESCE(notes,''), created_at, updated_at
		FROM billing.payments WHERE invoice_id = $1 ORDER BY payment_date DESC
	`, invoiceID)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.payment_list", "list payments", err)
	}
	defer rows.Close()
	out := []domain.Payment{}
	for rows.Next() {
		var (
			p      domain.Payment
			status string
		)
		if err := rows.Scan(&p.ID, &p.InvoiceID, &p.CustomerID, &p.Amount, &p.PaymentMethod,
			&p.GatewayTransactionID, &p.PaymentDate, &p.ConfirmedBy, &status, &p.Notes,
			&p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "db.payment_scan", "scan payment", err)
		}
		p.Status = domain.PaymentStatus(status)
		out = append(out, p)
	}
	return out, nil
}

func scanInvoice(row pgx.Row) (*port.InvoiceView, error) {
	var (
		i      domain.Invoice
		typ    string
		status string
		v      port.InvoiceView
	)
	err := row.Scan(&i.ID, &i.InvoiceNumber, &i.CustomerID, &i.OrderID,
		&typ, &i.InvoiceDate, &i.DueDate,
		&i.Subtotal, &i.PPNRate, &i.PPNAmount, &i.Total,
		&status, &i.PaidAt, &i.Notes,
		&i.CreatedBy, &i.CreatedAt, &i.UpdatedAt,
		&v.CustomerName, &v.CustomerNumber, &v.OrderNumber)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, derrors.NotFound("invoice.not_found", "invoice not found")
	}
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.invoice_scan", "scan invoice", err)
	}
	i.InvoiceType = domain.InvoiceType(typ)
	i.Status = domain.InvoiceStatus(status)
	v.Invoice = i
	return &v, nil
}

func sumConfirmed(pays []domain.Payment) float64 {
	t := 0.0
	for _, p := range pays {
		if p.Status == domain.PaymentStatusConfirmed {
			t += p.Amount
		}
	}
	return t
}

func clampZero(f float64) float64 {
	if f < 0 {
		return 0
	}
	return f
}
