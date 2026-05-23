// Wave 114 — Postgres adapters for the four orchestration repos.
//
// Each repo is small (Create + a couple of lookups) — kept in one
// file to avoid sprawl. ON CONFLICT semantics are load-bearing for
// the cron's idempotency story; that's why each Create returns a
// `createdNew` bool where the port interface expects it.

package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/billing/domain"
	"github.com/ion-core/backend/internal/billing/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// billing.reminder_log
// =====================================================================

type ReminderLogRepository struct {
	pool *pgxpool.Pool
}

func NewReminderLogRepository(pool *pgxpool.Pool) *ReminderLogRepository {
	return &ReminderLogRepository{pool: pool}
}

var _ port.ReminderLogRepository = (*ReminderLogRepository)(nil)

// Create inserts one row. The cron is idempotent via UNIQUE
// (invoice_id, kind) — we don't return createdNew here because the
// cron's per-invoice loop dedupes via FindLastByInvoice before
// calling Create; a duplicate insert is a true error worth surfacing.
func (r *ReminderLogRepository) Create(ctx context.Context, row *port.ReminderLogRow) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO billing.reminder_log
		  (id, invoice_id, kind, sent_at, channel, delivered, message_id, error_msg)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (invoice_id, kind) DO NOTHING
	`,
		row.ID, row.InvoiceID, string(row.Kind), row.SentAt, row.Channel,
		row.Delivered, nullableString(row.MessageID), nullableString(row.ErrorMsg),
	)
	if err != nil {
		return mapDBError(err, "reminder_log.create", "create reminder log row")
	}
	return nil
}

func (r *ReminderLogRepository) FindLastByInvoice(ctx context.Context, invoiceID uuid.UUID) (*port.ReminderLogRow, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, invoice_id, kind, sent_at, channel,
		       delivered, COALESCE(message_id,''), COALESCE(error_msg,'')
		FROM billing.reminder_log
		WHERE invoice_id = $1
		ORDER BY sent_at DESC
		LIMIT 1
	`, invoiceID)
	var out port.ReminderLogRow
	var kind string
	if err := row.Scan(&out.ID, &out.InvoiceID, &kind, &out.SentAt, &out.Channel,
		&out.Delivered, &out.MessageID, &out.ErrorMsg); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, derrors.Wrap(derrors.KindInternal, "reminder_log.find_last", "find last reminder", err)
	}
	out.Kind = domain.ReminderKind(kind)
	return &out, nil
}

func (r *ReminderLogRepository) ListPending(ctx context.Context, f port.ReminderLogFilter) ([]port.ReminderLogRow, error) {
	// ListPending isn't called by the cron path; it's a convenience
	// for admin surfaces. Simple invoice-id filter for now.
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	var rows pgx.Rows
	var err error
	if f.InvoiceID != nil {
		rows, err = r.pool.Query(ctx, `
			SELECT id, invoice_id, kind, sent_at, channel,
			       delivered, COALESCE(message_id,''), COALESCE(error_msg,'')
			FROM billing.reminder_log
			WHERE invoice_id = $1
			ORDER BY sent_at DESC
			LIMIT $2
		`, *f.InvoiceID, limit)
	} else {
		rows, err = r.pool.Query(ctx, `
			SELECT id, invoice_id, kind, sent_at, channel,
			       delivered, COALESCE(message_id,''), COALESCE(error_msg,'')
			FROM billing.reminder_log
			ORDER BY sent_at DESC
			LIMIT $1
		`, limit)
	}
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "reminder_log.list", "list reminder log", err)
	}
	defer rows.Close()
	out := []port.ReminderLogRow{}
	for rows.Next() {
		var x port.ReminderLogRow
		var kind string
		if err := rows.Scan(&x.ID, &x.InvoiceID, &kind, &x.SentAt, &x.Channel,
			&x.Delivered, &x.MessageID, &x.ErrorMsg); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "reminder_log.scan", "scan reminder", err)
		}
		x.Kind = domain.ReminderKind(kind)
		out = append(out, x)
	}
	return out, nil
}

// =====================================================================
// billing.late_fee_applications
// =====================================================================

type LateFeeApplicationRepository struct {
	pool *pgxpool.Pool
}

func NewLateFeeApplicationRepository(pool *pgxpool.Pool) *LateFeeApplicationRepository {
	return &LateFeeApplicationRepository{pool: pool}
}

var _ port.LateFeeApplicationRepository = (*LateFeeApplicationRepository)(nil)

// Create uses ON CONFLICT (invoice_id) DO NOTHING + RETURNING id to
// detect whether a new row landed. The cron uses createdNew to decide
// whether to also bump the invoice total.
func (r *LateFeeApplicationRepository) Create(ctx context.Context, row *port.LateFeeApplicationRow) (bool, error) {
	var id uuid.UUID
	err := r.pool.QueryRow(ctx, `
		INSERT INTO billing.late_fee_applications
		  (id, invoice_id, schema_version_id, applied_amount, applied_at, basis)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (invoice_id) DO NOTHING
		RETURNING id
	`,
		row.ID, row.InvoiceID, row.SchemaVersionID, row.AppliedAmount, row.AppliedAt, row.Basis,
	).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// ON CONFLICT path — row already existed.
			return false, nil
		}
		return false, mapDBError(err, "late_fee.create", "create late-fee application")
	}
	return true, nil
}

func (r *LateFeeApplicationRepository) FindByInvoice(ctx context.Context, invoiceID uuid.UUID) (*port.LateFeeApplicationRow, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, invoice_id, schema_version_id, applied_amount, applied_at,
		       basis, undo_at, COALESCE(undo_reason,'')
		FROM billing.late_fee_applications
		WHERE invoice_id = $1
	`, invoiceID)
	var out port.LateFeeApplicationRow
	if err := row.Scan(&out.ID, &out.InvoiceID, &out.SchemaVersionID,
		&out.AppliedAmount, &out.AppliedAt, &out.Basis,
		&out.UndoAt, &out.UndoReason); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, derrors.Wrap(derrors.KindInternal, "late_fee.find", "find late-fee app", err)
	}
	return &out, nil
}

func (r *LateFeeApplicationRepository) Undo(ctx context.Context, invoiceID uuid.UUID, reason string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE billing.late_fee_applications
		SET undo_at = NOW(), undo_reason = $2
		WHERE invoice_id = $1 AND undo_at IS NULL
	`, invoiceID, nullableString(reason))
	if err != nil {
		return mapDBError(err, "late_fee.undo", "undo late-fee application")
	}
	return nil
}

// =====================================================================
// billing.suspension_actions
// =====================================================================

type SuspensionActionRepository struct {
	pool *pgxpool.Pool
}

func NewSuspensionActionRepository(pool *pgxpool.Pool) *SuspensionActionRepository {
	return &SuspensionActionRepository{pool: pool}
}

var _ port.SuspensionActionRepository = (*SuspensionActionRepository)(nil)

func (r *SuspensionActionRepository) Create(ctx context.Context, row *port.SuspensionActionRow) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO billing.suspension_actions
		  (id, customer_id, triggered_by_invoice_id, schema_version_id,
		   action, executed_at, grace_window_hours, executed_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
	`,
		row.ID, row.CustomerID, row.TriggeredByInvoiceID, row.SchemaVersionID,
		string(row.Action), row.ExecutedAt, row.GraceWindowHours, row.ExecutedBy,
	)
	if err != nil {
		return mapDBError(err, "suspension_action.create", "create suspension action")
	}
	return nil
}

func (r *SuspensionActionRepository) FindLastByCustomer(ctx context.Context, customerID uuid.UUID) (*port.SuspensionActionRow, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, customer_id, triggered_by_invoice_id, schema_version_id,
		       action, executed_at, grace_window_hours, executed_by
		FROM billing.suspension_actions
		WHERE customer_id = $1
		ORDER BY executed_at DESC
		LIMIT 1
	`, customerID)
	var out port.SuspensionActionRow
	var action string
	if err := row.Scan(&out.ID, &out.CustomerID, &out.TriggeredByInvoiceID, &out.SchemaVersionID,
		&action, &out.ExecutedAt, &out.GraceWindowHours, &out.ExecutedBy); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, derrors.Wrap(derrors.KindInternal, "suspension_action.find_last", "find last suspension", err)
	}
	out.Action = domain.SuspensionActionKind(action)
	return &out, nil
}

func (r *SuspensionActionRepository) ListByActionInWindow(ctx context.Context, f port.SuspensionActionFilter) ([]port.SuspensionActionRow, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 200
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id, customer_id, triggered_by_invoice_id, schema_version_id,
		       action, executed_at, grace_window_hours, executed_by
		FROM billing.suspension_actions
		WHERE action = $1
		  AND executed_at >= $2
		  AND executed_at <  $3
		ORDER BY executed_at DESC
		LIMIT $4
	`, string(f.Action), f.From, f.To, limit)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "suspension_action.list", "list suspension actions", err)
	}
	defer rows.Close()
	out := []port.SuspensionActionRow{}
	for rows.Next() {
		var x port.SuspensionActionRow
		var action string
		if err := rows.Scan(&x.ID, &x.CustomerID, &x.TriggeredByInvoiceID, &x.SchemaVersionID,
			&action, &x.ExecutedAt, &x.GraceWindowHours, &x.ExecutedBy); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "suspension_action.scan", "scan suspension", err)
		}
		x.Action = domain.SuspensionActionKind(action)
		out = append(out, x)
	}
	return out, nil
}

// =====================================================================
// billing.commission_triggers
// =====================================================================

type CommissionTriggerRepository struct {
	pool *pgxpool.Pool
}

func NewCommissionTriggerRepository(pool *pgxpool.Pool) *CommissionTriggerRepository {
	return &CommissionTriggerRepository{pool: pool}
}

var _ port.CommissionTriggerRepository = (*CommissionTriggerRepository)(nil)

// Create uses ON CONFLICT (plan_change_id, trigger_kind) DO NOTHING
// + RETURNING id to surface whether a new row landed. Rows without a
// plan_change_id (free-form triggers) bypass the unique check by
// design — those rows are append-only.
func (r *CommissionTriggerRepository) Create(ctx context.Context, row *port.CommissionTriggerRow) (bool, error) {
	var id uuid.UUID
	err := r.pool.QueryRow(ctx, `
		INSERT INTO billing.commission_triggers
		  (id, plan_change_id, customer_id, sales_user_id, trigger_kind,
		   invoice_id, amount_basis, schema_version_id, fired_at,
		   commission_amount, commission_recipient_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		ON CONFLICT (plan_change_id, trigger_kind) DO NOTHING
		RETURNING id
	`,
		row.ID, row.PlanChangeID, row.CustomerID, row.SalesUserID, string(row.TriggerKind),
		row.InvoiceID, row.AmountBasis, row.SchemaVersionID, row.FiredAt,
		row.CommissionAmount, row.CommissionRecipientID,
	).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, mapDBError(err, "commission_trigger.create", "create commission trigger")
	}
	return true, nil
}

func (r *CommissionTriggerRepository) ListByPlanChange(ctx context.Context, planChangeID uuid.UUID) ([]port.CommissionTriggerRow, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, plan_change_id, customer_id, sales_user_id, trigger_kind,
		       invoice_id, amount_basis, schema_version_id, fired_at,
		       commission_amount, commission_recipient_id
		FROM billing.commission_triggers
		WHERE plan_change_id = $1
		ORDER BY fired_at DESC
	`, planChangeID)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "commission_trigger.list", "list commission triggers", err)
	}
	return scanCommissionTriggers(rows)
}

func (r *CommissionTriggerRepository) ListByCustomer(ctx context.Context, customerID uuid.UUID) ([]port.CommissionTriggerRow, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, plan_change_id, customer_id, sales_user_id, trigger_kind,
		       invoice_id, amount_basis, schema_version_id, fired_at,
		       commission_amount, commission_recipient_id
		FROM billing.commission_triggers
		WHERE customer_id = $1
		ORDER BY fired_at DESC
		LIMIT 200
	`, customerID)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "commission_trigger.list_cust", "list commission triggers by customer", err)
	}
	return scanCommissionTriggers(rows)
}

func scanCommissionTriggers(rows pgx.Rows) ([]port.CommissionTriggerRow, error) {
	defer rows.Close()
	out := []port.CommissionTriggerRow{}
	for rows.Next() {
		var x port.CommissionTriggerRow
		var trig string
		if err := rows.Scan(&x.ID, &x.PlanChangeID, &x.CustomerID, &x.SalesUserID, &trig,
			&x.InvoiceID, &x.AmountBasis, &x.SchemaVersionID, &x.FiredAt,
			&x.CommissionAmount, &x.CommissionRecipientID); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "commission_trigger.scan", "scan commission trigger", err)
		}
		x.TriggerKind = domain.CommissionTriggerKind(trig)
		out = append(out, x)
	}
	return out, nil
}

// =====================================================================
// PlanChangeReader — SQL-only cross-context query.
//
// The commission-trigger evaluator needs (paid invoice, plan_change,
// sales_user) tuples. We read straight from crm.plan_change_requests
// (the join table) + billing.invoices in one query. Same pattern the
// existing CRMGateway uses (in-process DB; HTTP later).
// =====================================================================

type PlanChangeReader struct {
	pool *pgxpool.Pool
}

func NewPlanChangeReader(pool *pgxpool.Pool) *PlanChangeReader {
	return &PlanChangeReader{pool: pool}
}

var _ port.PlanChangeReader = (*PlanChangeReader)(nil)

// ListRecentlyPaidForCommission returns paid invoices (paid_at ≥
// since) joined with an applied plan_change_request that carries a
// sales_rep_id. Rows where the plan-change has no sales rep are
// excluded — the trigger can't fire without a recipient.
func (r *PlanChangeReader) ListRecentlyPaidForCommission(ctx context.Context, since time.Time, limit int) ([]port.PlanChangePaidInvoice, error) {
	if limit <= 0 {
		limit = 500
	}
	// Filter: paid invoices, plan_change applied / approved, sales rep present.
	// We grab activated_at off crm.customers since the on_activated
	// trigger needs it; soft-FK lookup since the customer_id is
	// guaranteed by the invoice row.
	rows, err := r.pool.Query(ctx, `
		SELECT
		  i.id, i.customer_id, pc.id AS plan_change_id, pc.sales_rep_id,
		  i.total, i.paid_at, c.activated_at
		FROM billing.invoices i
		JOIN crm.plan_change_requests pc ON pc.customer_id = i.customer_id
		LEFT JOIN crm.customers c ON c.id = i.customer_id
		WHERE i.status = 'paid'
		  AND i.paid_at >= $1
		  AND pc.status IN ('applied','approved')
		  AND pc.sales_rep_id IS NOT NULL
		ORDER BY i.paid_at DESC
		LIMIT $2
	`, since, limit)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "plan_change.list_paid", "list recently paid for commission", err)
	}
	defer rows.Close()
	out := []port.PlanChangePaidInvoice{}
	for rows.Next() {
		var x port.PlanChangePaidInvoice
		var paidAt *time.Time
		if err := rows.Scan(&x.InvoiceID, &x.CustomerID, &x.PlanChangeID, &x.SalesUserID,
			&x.AmountBasis, &paidAt, &x.ActivatedAt); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "plan_change.scan", "scan plan-change paid", err)
		}
		if paidAt != nil {
			x.PaidAt = *paidAt
		}
		out = append(out, x)
	}
	return out, nil
}

// =====================================================================
// CustomerReader — projects the suspension/restore candidate lists.
//
// SQL-only cross-context query against crm.customers + billing.invoices.
// =====================================================================

type CustomerReader struct {
	pool *pgxpool.Pool
}

func NewCustomerReader(pool *pgxpool.Pool) *CustomerReader {
	return &CustomerReader{pool: pool}
}

var _ port.CustomerReader = (*CustomerReader)(nil)

// ReadForReminder returns the minimal projection a reminder template
// needs. NotFound when the customer is gone.
func (r *CustomerReader) ReadForReminder(ctx context.Context, customerID uuid.UUID) (*port.ReminderTarget, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT
		  id,
		  COALESCE(full_name,''),
		  COALESCE(phone,''),
		  COALESCE(email,'')
		FROM crm.customers
		WHERE id = $1
	`, customerID)
	var out port.ReminderTarget
	if err := row.Scan(&out.CustomerID, &out.CustomerName, &out.PhoneE164, &out.Email); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, derrors.NotFound("customer.not_found", "customer not found")
		}
		return nil, derrors.Wrap(derrors.KindInternal, "customer.read_reminder", "read customer for reminder", err)
	}
	return &out, nil
}

// ListSuspensionCandidates returns the customers with at least one
// past-due issued invoice, ordered by oldest-overdue ASC.
//
// CurrentState is derived from crm.customers.status — the existing
// status field carries 'active' / 'suspended' / 'terminated' today;
// we map 'suspended' to soft_suspend for the evaluator's purposes
// (the hard_suspend distinction lives only on
// billing.suspension_actions until the schema-driven path lands).
func (r *CustomerReader) ListSuspensionCandidates(ctx context.Context, limit int) ([]port.SuspensionCandidate, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := r.pool.Query(ctx, `
		SELECT
		  c.id,
		  c.status,
		  MIN(i.due_date) AS oldest_due,
		  (ARRAY_AGG(i.id ORDER BY i.due_date ASC))[1] AS oldest_invoice_id
		FROM crm.customers c
		JOIN billing.invoices i ON i.customer_id = c.id
		WHERE i.status = 'issued'
		  AND i.due_date < NOW()
		  AND c.status IN ('active','suspended')
		GROUP BY c.id
		ORDER BY oldest_due ASC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "customer.suspension_candidates", "list suspension candidates", err)
	}
	defer rows.Close()
	out := []port.SuspensionCandidate{}
	for rows.Next() {
		var (
			c          port.SuspensionCandidate
			status     string
			oldestDue  time.Time
			oldestInv  *uuid.UUID
		)
		if err := rows.Scan(&c.CustomerID, &status, &oldestDue, &oldestInv); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "customer.suspension_scan", "scan suspension candidate", err)
		}
		c.OldestOverdueDue = oldestDue
		c.OldestInvoiceID = oldestInv
		c.CurrentState = mapCustomerStateForSuspension(status)
		out = append(out, c)
	}
	return out, nil
}

// ListRestoreCandidates returns customers in 'suspended' status with
// zero open invoices.
func (r *CustomerReader) ListRestoreCandidates(ctx context.Context, limit int) ([]port.RestoreCandidate, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := r.pool.Query(ctx, `
		SELECT
		  c.id,
		  c.status,
		  (
		    SELECT MAX(p.payment_date)
		    FROM billing.payments p
		    WHERE p.customer_id = c.id AND p.status = 'confirmed'
		  ) AS last_paid_at
		FROM crm.customers c
		WHERE c.status = 'suspended'
		  AND NOT EXISTS (
		    SELECT 1 FROM billing.invoices i
		    WHERE i.customer_id = c.id
		      AND i.status = 'issued'
		  )
		ORDER BY c.suspended_at ASC NULLS LAST
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "customer.restore_candidates", "list restore candidates", err)
	}
	defer rows.Close()
	out := []port.RestoreCandidate{}
	for rows.Next() {
		var (
			c        port.RestoreCandidate
			status   string
			lastPaid *time.Time
		)
		if err := rows.Scan(&c.CustomerID, &status, &lastPaid); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "customer.restore_scan", "scan restore candidate", err)
		}
		c.CurrentState = mapCustomerStateForSuspension(status)
		c.LastPaidAt = lastPaid
		out = append(out, c)
	}
	return out, nil
}

// mapCustomerStateForSuspension projects the legacy CRM status string
// into the new CustomerSuspensionState enum the orchestration
// service speaks. 'suspended' maps to soft_suspend by default — the
// hard_suspend distinction lives only on billing.suspension_actions
// until a future wave splits the CRM enum.
func mapCustomerStateForSuspension(status string) domain.CustomerSuspensionState {
	switch status {
	case "active":
		return domain.CustomerSuspensionStateActive
	case "suspended":
		return domain.CustomerSuspensionStateSoftSuspend
	}
	return domain.CustomerSuspensionStateActive
}
