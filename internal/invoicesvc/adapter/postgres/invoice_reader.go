package postgres

import (
	"context"
	stderrors "errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/invoicesvc/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// InvoiceReader is the SQL-only cross-context adapter. It reads:
//
//   - billing.invoices + billing.payments + billing.reminder_log
//     (and optionally enterprise.invoices when source filter allows)
//   - crm.customers (for customer_name on TopOverdueCustomers)
//
// We deliberately do NOT import internal/billing or internal/enterprise
// Go packages — only the SQL schema is the contract. Schema drift will
// surface as a typed error at query time, which is exactly the
// extraction signal we want.
type InvoiceReader struct {
	pool *pgxpool.Pool
	// includeEnterprise gates the UNION ALL with enterprise.invoices.
	// Off by default (broadband-only Phase 1); enable when the
	// enterprise schema also conforms to the projection columns.
	includeEnterprise bool
}

func NewInvoiceReader(pool *pgxpool.Pool) *InvoiceReader {
	return &InvoiceReader{pool: pool}
}

// WithEnterprise toggles the union-across-modules read path. Tests +
// the SVC binary use this when the deployment also runs the enterprise
// service in the same DB.
func (r *InvoiceReader) WithEnterprise(on bool) *InvoiceReader {
	r.includeEnterprise = on
	return r
}

var _ port.InvoiceReader = (*InvoiceReader)(nil)

const projectionSelect = `
	id, invoice_number, customer_id, order_id,
	invoice_type, invoice_date, due_date,
	subtotal, ppn_amount, total, status, paid_at,
	created_at
`

func (r *InvoiceReader) FindByID(ctx context.Context, id uuid.UUID) (*port.InvoiceProjection, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+projectionSelect+` FROM billing.invoices WHERE id = $1`, id)
	proj, err := scanProjection(row, "billing")
	if stderrors.Is(err, pgx.ErrNoRows) {
		if r.includeEnterprise {
			row := r.pool.QueryRow(ctx, `SELECT `+projectionSelect+` FROM enterprise.invoices WHERE id = $1`, id)
			p2, err2 := scanProjection(row, "enterprise")
			if stderrors.Is(err2, pgx.ErrNoRows) {
				return nil, nil
			}
			if err2 != nil {
				return nil, err2
			}
			if err := r.fillPaymentAggregate(ctx, &p2); err != nil {
				return nil, err
			}
			return &p2, nil
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := r.fillPaymentAggregate(ctx, &proj); err != nil {
		return nil, err
	}
	return &proj, nil
}

func (r *InvoiceReader) ListByCustomer(ctx context.Context, customerID uuid.UUID, limit, offset int) ([]port.InvoiceProjection, int, error) {
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	var total int
	if err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM billing.invoices WHERE customer_id = $1`,
		customerID).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "invoice_reader.count", "count customer invoices", err)
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+projectionSelect+`
		FROM billing.invoices
		WHERE customer_id = $1
		ORDER BY invoice_date DESC, created_at DESC
		LIMIT $2 OFFSET $3
	`, customerID, limit, offset)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "invoice_reader.list_customer", "list customer invoices", err)
	}
	defer rows.Close()
	out := []port.InvoiceProjection{}
	for rows.Next() {
		p, err := scanProjection(rows, "billing")
		if err != nil {
			return nil, 0, err
		}
		_ = r.fillPaymentAggregate(ctx, &p)
		out = append(out, p)
	}
	return out, total, nil
}

func (r *InvoiceReader) ListByCycle(ctx context.Context, cycleID uuid.UUID) ([]port.InvoiceProjection, error) {
	// Cycles live in billing.billing_cycles; the FK from invoice→cycle
	// is via the order_id or via a soft cycle_id column. We follow the
	// pragmatic path: select invoices created in the same window as
	// the cycle's period. If billing.invoices ever grows a cycle_id
	// column this query becomes a one-liner.
	rows, err := r.pool.Query(ctx, `
		SELECT i.id, i.invoice_number, i.customer_id, i.order_id,
		       i.invoice_type, i.invoice_date, i.due_date,
		       i.subtotal, i.ppn_amount, i.total, i.status, i.paid_at,
		       i.created_at
		FROM billing.invoices i
		JOIN billing.billing_cycles c
		  ON c.customer_id = i.customer_id
		WHERE c.id = $1
		  AND i.invoice_date >= c.period_start
		  AND i.invoice_date <= c.period_end
		ORDER BY i.invoice_date DESC
	`, cycleID)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "invoice_reader.list_cycle", "list cycle invoices", err)
	}
	defer rows.Close()
	out := []port.InvoiceProjection{}
	for rows.Next() {
		p, err := scanProjection(rows, "billing")
		if err != nil {
			return nil, err
		}
		_ = r.fillPaymentAggregate(ctx, &p)
		out = append(out, p)
	}
	return out, nil
}

// FindForBulkRun translates the opaque filter map into a customer-id set.
// Supported keys:
//   - "branch_id"    (uuid string): customers in that branch
//   - "customer_ids" ([]string of uuids): explicit set
//   - "customer_type" (string): residential | business | enterprise
//   - "active_only"  (bool, default true)
func (r *InvoiceReader) FindForBulkRun(ctx context.Context, filter map[string]any) ([]uuid.UUID, error) {
	var wh []string
	var args []any

	activeOnly := true
	if v, ok := filter["active_only"]; ok {
		if b, ok2 := v.(bool); ok2 {
			activeOnly = b
		}
	}
	if activeOnly {
		wh = append(wh, "status = 'active'")
	}
	if v, ok := filter["branch_id"]; ok {
		if s, ok2 := v.(string); ok2 && s != "" {
			if u, err := uuid.Parse(s); err == nil {
				args = append(args, u)
				wh = append(wh, fmt.Sprintf("branch_id = $%d", len(args)))
			}
		}
	}
	if v, ok := filter["customer_type"]; ok {
		if s, ok2 := v.(string); ok2 && s != "" {
			args = append(args, s)
			wh = append(wh, fmt.Sprintf("customer_type = $%d", len(args)))
		}
	}
	if v, ok := filter["customer_ids"]; ok {
		if arr, ok2 := v.([]any); ok2 && len(arr) > 0 {
			ids := []uuid.UUID{}
			for _, e := range arr {
				if s, ok := e.(string); ok {
					if u, err := uuid.Parse(s); err == nil {
						ids = append(ids, u)
					}
				}
			}
			if len(ids) > 0 {
				args = append(args, ids)
				wh = append(wh, fmt.Sprintf("id = ANY($%d)", len(args)))
			}
		}
	}
	where := ""
	if len(wh) > 0 {
		where = " WHERE " + strings.Join(wh, " AND ")
	}
	rows, err := r.pool.Query(ctx, `SELECT id FROM crm.customers`+where+` ORDER BY id LIMIT 200000`, args...)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "invoice_reader.bulk_resolve", "resolve bulk customer set", err)
	}
	defer rows.Close()
	out := []uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "invoice_reader.bulk_scan", "scan customer id", err)
		}
		out = append(out, id)
	}
	return out, nil
}

// Aggregations runs the dashboard rollup against billing.invoices in
// one round-trip. Status counts + amounts + aging buckets + sum-paid
// all share the same WHERE clause for filter consistency.
func (r *InvoiceReader) Aggregations(ctx context.Context, f port.InvoiceQueryFilter) (*port.AggregationResult, error) {
	var wh []string
	var args []any
	if f.From != nil {
		args = append(args, *f.From)
		wh = append(wh, fmt.Sprintf("invoice_date >= $%d", len(args)))
	}
	if f.To != nil {
		args = append(args, *f.To)
		wh = append(wh, fmt.Sprintf("invoice_date <= $%d", len(args)))
	}
	if f.CustomerID != nil {
		args = append(args, *f.CustomerID)
		wh = append(wh, fmt.Sprintf("customer_id = $%d", len(args)))
	}
	if f.Status != "" {
		args = append(args, f.Status)
		wh = append(wh, fmt.Sprintf("status = $%d", len(args)))
	}
	where := ""
	if len(wh) > 0 {
		where = " WHERE " + strings.Join(wh, " AND ")
	}
	sql := fmt.Sprintf(`
		SELECT
		  COUNT(*) AS total_count,
		  COALESCE(SUM(total), 0) AS total_amount,
		  COUNT(*) FILTER (WHERE status = 'paid') AS paid_count,
		  COALESCE(SUM(total) FILTER (WHERE status = 'paid'), 0) AS paid_amount,
		  COUNT(*) FILTER (WHERE status = 'overdue') AS overdue_count,
		  COALESCE(SUM(total) FILTER (WHERE status = 'overdue'), 0) AS overdue_amount,
		  COUNT(*) FILTER (WHERE status = 'issued') AS issued_count,
		  COALESCE(SUM(total) FILTER (WHERE status = 'issued'), 0) AS issued_amount,
		  COUNT(*) FILTER (WHERE status = 'cancelled') AS credited_count,
		  COALESCE(SUM(total) FILTER (
			  WHERE status != 'paid'
			    AND (NOW()::date - due_date) BETWEEN 0 AND 30
			), 0) AS bucket_0_30,
		  COALESCE(SUM(total) FILTER (
			  WHERE status != 'paid'
			    AND (NOW()::date - due_date) BETWEEN 31 AND 60
			), 0) AS bucket_31_60,
		  COALESCE(SUM(total) FILTER (
			  WHERE status != 'paid'
			    AND (NOW()::date - due_date) BETWEEN 61 AND 90
			), 0) AS bucket_61_90,
		  COALESCE(SUM(total) FILTER (
			  WHERE status != 'paid'
			    AND (NOW()::date - due_date) > 90
			), 0) AS bucket_91_up
		FROM billing.invoices %s
	`, where)
	row := r.pool.QueryRow(ctx, sql, args...)
	var (
		out                  port.AggregationResult
		b0, b1, b2, b3       float64
	)
	if err := row.Scan(
		&out.TotalCount, &out.TotalAmount,
		&out.PaidCount, &out.PaidAmount,
		&out.OverdueCount, &out.OverdueAmount,
		&out.IssuedCount, &out.IssuedAmount,
		&out.CreditedCount,
		&b0, &b1, &b2, &b3,
	); err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "invoice_reader.agg", "aggregate invoices", err)
	}
	out.AgingBuckets = port.AgingBuckets{
		Bucket0_30:  b0,
		Bucket31_60: b1,
		Bucket61_90: b2,
		Bucket91Up:  b3,
	}
	// ByStatus: separate cheap query for the per-status pivot.
	byStatusSQL := `
		SELECT status, COUNT(*)
		FROM billing.invoices ` + where + `
		GROUP BY status
	`
	rows, err := r.pool.Query(ctx, byStatusSQL, args...)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "invoice_reader.agg_status", "aggregate by status", err)
	}
	defer rows.Close()
	out.ByStatus = map[string]int{}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "invoice_reader.agg_status_scan", "scan by-status row", err)
		}
		out.ByStatus[st] = n
	}
	return &out, nil
}

func (r *InvoiceReader) CycleHealth(ctx context.Context, cycleID uuid.UUID) (*port.CycleHealthResult, error) {
	out := &port.CycleHealthResult{CycleID: cycleID}
	// last_run + counts derived from invoices in the cycle's window.
	row := r.pool.QueryRow(ctx, `
		SELECT
		  MAX(i.created_at)                                       AS last_run_at,
		  COUNT(*) FILTER (WHERE i.status IN ('issued','paid'))   AS success_count,
		  COUNT(*) FILTER (WHERE i.status = 'cancelled')          AS failure_count,
		  COALESCE(AVG(EXTRACT(EPOCH FROM (i.updated_at - i.created_at)) * 1000.0), 0) AS avg_latency_ms
		FROM billing.invoices i
		JOIN billing.billing_cycles c
		  ON c.customer_id = i.customer_id
		WHERE c.id = $1
		  AND i.invoice_date >= c.period_start
		  AND i.invoice_date <= c.period_end
	`, cycleID)
	var lastRun *time.Time
	if err := row.Scan(&lastRun, &out.SuccessCount, &out.FailureCount, &out.AvgLatencyMS); err != nil {
		if stderrors.Is(err, pgx.ErrNoRows) {
			return out, nil
		}
		return nil, derrors.Wrap(derrors.KindInternal, "invoice_reader.cycle_health", "cycle health", err)
	}
	out.LastRunAt = lastRun
	if lastRun != nil {
		out.StaleBy24h = time.Since(*lastRun) > 24*time.Hour
	} else {
		out.StaleBy24h = true
	}
	return out, nil
}

func (r *InvoiceReader) TopOverdueCustomers(ctx context.Context, limit int) ([]port.TopOverdueRow, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := r.pool.Query(ctx, `
		SELECT
		  i.customer_id,
		  COALESCE(c.full_name, '') AS customer_name,
		  COALESCE(SUM(i.total), 0) AS overdue_amount,
		  MAX(NOW()::date - i.due_date) AS oldest_days,
		  COUNT(*) AS invoice_count
		FROM billing.invoices i
		LEFT JOIN crm.customers c ON c.id = i.customer_id
		WHERE i.status = 'overdue'
		   OR (i.status != 'paid' AND i.due_date < NOW()::date)
		GROUP BY i.customer_id, c.full_name
		ORDER BY overdue_amount DESC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "invoice_reader.top_overdue", "top overdue customers", err)
	}
	defer rows.Close()
	out := []port.TopOverdueRow{}
	for rows.Next() {
		var row port.TopOverdueRow
		var oldestDays *int
		if err := rows.Scan(&row.CustomerID, &row.CustomerName, &row.OverdueAmount, &oldestDays, &row.InvoiceCount); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "invoice_reader.top_overdue_scan", "scan top overdue row", err)
		}
		if oldestDays != nil {
			row.OldestOverdueDays = *oldestDays
		}
		out = append(out, row)
	}
	return out, nil
}

func (r *InvoiceReader) PaymentHistory(ctx context.Context, customerID uuid.UUID, limit int) ([]port.PaymentHistoryRow, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id, invoice_id, amount,
		       COALESCE(payment_method, ''),
		       COALESCE(gateway_transaction_id, ''),
		       payment_date,
		       COALESCE(status, '')
		FROM billing.payments
		WHERE customer_id = $1
		ORDER BY payment_date DESC
		LIMIT $2
	`, customerID, limit)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "invoice_reader.payment_history", "payment history", err)
	}
	defer rows.Close()
	out := []port.PaymentHistoryRow{}
	for rows.Next() {
		var p port.PaymentHistoryRow
		if err := rows.Scan(&p.ID, &p.InvoiceID, &p.Amount, &p.Method, &p.GatewayRef, &p.PaymentDate, &p.Status); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "invoice_reader.payment_history_scan", "scan payment row", err)
		}
		out = append(out, p)
	}
	return out, nil
}

func (r *InvoiceReader) ReminderHistory(ctx context.Context, customerID uuid.UUID, limit int) ([]port.ReminderHistoryRow, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.pool.Query(ctx, `
		SELECT rl.id, rl.invoice_id, rl.kind, rl.channel, rl.sent_at,
		       rl.delivered, COALESCE(rl.error_msg, '')
		FROM billing.reminder_log rl
		JOIN billing.invoices i ON i.id = rl.invoice_id
		WHERE i.customer_id = $1
		ORDER BY rl.sent_at DESC
		LIMIT $2
	`, customerID, limit)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "invoice_reader.reminder_history", "reminder history", err)
	}
	defer rows.Close()
	out := []port.ReminderHistoryRow{}
	for rows.Next() {
		var rrow port.ReminderHistoryRow
		if err := rows.Scan(&rrow.ID, &rrow.InvoiceID, &rrow.Kind, &rrow.Channel, &rrow.SentAt, &rrow.Delivered, &rrow.ErrorMsg); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "invoice_reader.reminder_history_scan", "scan reminder row", err)
		}
		out = append(out, rrow)
	}
	return out, nil
}

func (r *InvoiceReader) IssuedInLast24h(ctx context.Context, limit int) ([]uuid.UUID, error) {
	if limit <= 0 {
		limit = 1000
	}
	rows, err := r.pool.Query(ctx, `
		SELECT i.id
		FROM billing.invoices i
		WHERE i.status IN ('issued','paid','overdue')
		  AND i.created_at >= NOW() - INTERVAL '24 hours'
		  AND NOT EXISTS (
		    SELECT 1 FROM invoicesvc.invoice_snapshots s WHERE s.invoice_id = i.id
		  )
		ORDER BY i.created_at DESC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "invoice_reader.backfill_scan", "scan invoices for backfill", err)
	}
	defer rows.Close()
	out := []uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "invoice_reader.backfill_scan_id", "scan id", err)
		}
		out = append(out, id)
	}
	return out, nil
}

// fillPaymentAggregate folds (amount_paid, outstanding) onto the
// projection. Best-effort: on error we leave the fields at zero (the
// dashboards already handle the "unknown" case).
func (r *InvoiceReader) fillPaymentAggregate(ctx context.Context, p *port.InvoiceProjection) error {
	if p == nil || p.ID == uuid.Nil {
		return nil
	}
	var paid float64
	err := r.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(amount), 0)
		FROM billing.payments
		WHERE invoice_id = $1 AND status = 'confirmed'
	`, p.ID).Scan(&paid)
	if err != nil {
		if stderrors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return derrors.Wrap(derrors.KindInternal, "invoice_reader.pay_agg", "aggregate payments", err)
	}
	p.AmountPaid = paid
	p.Outstanding = p.Total - paid
	if p.Outstanding < 0 {
		p.Outstanding = 0
	}
	// PaymentMethod: pick the most recent confirmed payment.
	row := r.pool.QueryRow(ctx, `
		SELECT COALESCE(payment_method, '')
		FROM billing.payments
		WHERE invoice_id = $1 AND status = 'confirmed'
		ORDER BY payment_date DESC
		LIMIT 1
	`, p.ID)
	_ = row.Scan(&p.PaymentMethod)
	return nil
}

func scanProjection(row pgx.Row, source string) (port.InvoiceProjection, error) {
	var p port.InvoiceProjection
	err := row.Scan(
		&p.ID, &p.InvoiceNumber, &p.CustomerID, &p.OrderID,
		&p.InvoiceType, &p.InvoiceDate, &p.DueDate,
		&p.Subtotal, &p.PPNAmount, &p.Total, &p.Status, &p.PaidAt,
		&p.CreatedAt,
	)
	if stderrors.Is(err, pgx.ErrNoRows) {
		return port.InvoiceProjection{}, pgx.ErrNoRows
	}
	if err != nil {
		return port.InvoiceProjection{}, derrors.Wrap(derrors.KindInternal, "invoice_reader.scan", "scan invoice", err)
	}
	p.SourceModule = source
	return p, nil
}
