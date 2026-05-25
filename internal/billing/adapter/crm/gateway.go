// Package crm adapts CRM reads/writes to the billing port.CRMGateway.
// Round-2 uses direct SQL against the shared DB; round-4 will swap to
// HTTP when CRM ships as its own deployment.
package crm

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/billing/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type Gateway struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Gateway {
	return &Gateway{pool: pool}
}

var _ port.CRMGateway = (*Gateway)(nil)

// ActiveOrdersForRecurring returns the projection the scheduler needs:
// one row per order belonging to an 'active' customer. We include
// customer status so the caller can skip suspended/terminated cleanly.
//
// activated_at is approximated as the max NOC-verified-at across the
// order's BAST records; that's when the customer flipped to active.
// We use the customer's created_at as a fallback so the first cycle
// still fires for legacy customers without a BAST record.
func (g *Gateway) ActiveOrdersForRecurring(ctx context.Context) ([]port.RecurringOrder, error) {
	rows, err := g.pool.Query(ctx, `
		SELECT
		  o.id, o.customer_id, c.status,
		  o.monthly_price, o.sales_id, o.branch_id,
		  o.nearest_node_id,
		  COALESCE(
		    (SELECT MAX(noc_verified_at) FROM field.bast_records b WHERE b.wo_id IN (
		       SELECT id FROM field.work_orders wo WHERE wo.order_id = o.id
		     )),
		    c.created_at
		  ) AS activated_at
		FROM crm.orders o
		JOIN crm.customers c ON c.id = o.customer_id
		WHERE c.status = 'active'
	`)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "crm.recurring", "list recurring", err)
	}
	defer rows.Close()
	out := []port.RecurringOrder{}
	for rows.Next() {
		var r port.RecurringOrder
		if err := rows.Scan(
			&r.OrderID, &r.CustomerID, &r.CustomerStatus,
			&r.MonthlyPrice, &r.SalesID, &r.OrderBranchID,
			&r.InfrastructureNode, &r.ActivatedAt,
		); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "crm.recurring_scan", "scan order", err)
		}
		out = append(out, r)
	}
	return out, nil
}

func (g *Gateway) OrderWithCustomer(ctx context.Context, orderID uuid.UUID) (*port.RecurringOrder, error) {
	row := g.pool.QueryRow(ctx, `
		SELECT
		  o.id, o.customer_id, c.status,
		  o.monthly_price, o.sales_id, o.branch_id,
		  o.nearest_node_id,
		  c.created_at AS activated_at
		FROM crm.orders o
		JOIN crm.customers c ON c.id = o.customer_id
		WHERE o.id = $1
	`, orderID)
	var r port.RecurringOrder
	err := row.Scan(&r.OrderID, &r.CustomerID, &r.CustomerStatus,
		&r.MonthlyPrice, &r.SalesID, &r.OrderBranchID,
		&r.InfrastructureNode, &r.ActivatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, derrors.NotFound("order.not_found", "order not found")
	}
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "crm.order_scan", "scan order", err)
	}
	return &r, nil
}

// SetCustomerStatus drives suspension / restore / termination from the
// scheduler. We stamp audit columns based on the target status.
func (g *Gateway) SetCustomerStatus(ctx context.Context, customerID uuid.UUID, status string, reason string) error {
	var stampSQL string
	switch status {
	case "suspended":
		stampSQL = ", suspended_at = NOW(), suspend_reason = $3"
	case "active":
		// Clear suspension audit on restore.
		stampSQL = ", suspended_at = NULL, suspend_reason = NULL"
	case "terminated":
		stampSQL = ", terminated_at = NOW(), suspend_reason = $3"
	}
	sql := `UPDATE crm.customers SET status = $2, updated_at = NOW()` + stampSQL + ` WHERE id = $1`
	args := []any{customerID, status}
	if status == "suspended" || status == "terminated" {
		args = append(args, reason)
	}
	tag, err := g.pool.Exec(ctx, sql, args...)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "crm.status_set", "set customer status", err)
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("customer.not_found", "customer not found")
	}
	return nil
}

// ManagerOfSales walks up users.reports_to_user_id until it lands on a
// user with role 'sales_manager'. Returns nil when the chain ends
// without finding one. We cap depth at 10 to avoid pathological loops
// in malformed data.
func (g *Gateway) ManagerOfSales(ctx context.Context, salesUserID uuid.UUID) (*uuid.UUID, error) {
	row := g.pool.QueryRow(ctx, `
		WITH RECURSIVE chain(user_id, reports_to, depth) AS (
		    SELECT id, reports_to_user_id, 0 FROM identity.users WHERE id = $1
		    UNION ALL
		    SELECT u.id, u.reports_to_user_id, c.depth + 1
		    FROM identity.users u
		    JOIN chain c ON u.id = c.reports_to
		    WHERE c.depth < 10
		)
		SELECT c.user_id
		FROM chain c
		JOIN identity.user_roles ur ON ur.user_id = c.user_id
		JOIN identity.roles r       ON r.id = ur.role_id AND r.name = 'sales_manager'
		WHERE c.depth > 0
		ORDER BY c.depth
		LIMIT 1
	`, salesUserID)
	var id uuid.UUID
	err := row.Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "crm.manager_walk", "walk reports_to", err)
	}
	return &id, nil
}

func (g *Gateway) SalesBranchOf(ctx context.Context, salesUserID uuid.UUID) (*uuid.UUID, error) {
	row := g.pool.QueryRow(ctx, `SELECT branch_id FROM identity.users WHERE id = $1`, salesUserID)
	var id *uuid.UUID
	if err := row.Scan(&id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, derrors.Wrap(derrors.KindInternal, "crm.sales_branch", "read sales branch", err)
	}
	return id, nil
}

// =====================================================================
// M6 r3 — suspension scan, customer summary, referral I/O
// =====================================================================

// SuspendedCustomers returns one row per customer currently in 'suspended'
// status. We join the active order so the scheduler has address +
// branch handy when minting termination work-orders.
func (g *Gateway) SuspendedCustomers(ctx context.Context) ([]port.SuspendedCustomer, error) {
	rows, err := g.pool.Query(ctx, `
		SELECT
		  c.id, c.suspended_at, c.lock_in_until,
		  o.id, COALESCE(c.address, ''), c.branch_id
		FROM crm.customers c
		LEFT JOIN LATERAL (
		     SELECT id FROM crm.orders WHERE customer_id = c.id ORDER BY created_at DESC LIMIT 1
		) o ON TRUE
		WHERE c.status = 'suspended'
	`)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "crm.suspended", "list suspended", err)
	}
	defer rows.Close()
	out := []port.SuspendedCustomer{}
	for rows.Next() {
		var r port.SuspendedCustomer
		if err := rows.Scan(&r.CustomerID, &r.SuspendedAt, &r.LockInUntil,
			&r.OrderID, &r.Address, &r.BranchID); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "crm.suspended_scan", "scan suspended", err)
		}
		out = append(out, r)
	}
	return out, nil
}

// CustomerSummary pulls a small projection used by the termination flow.
//
// Wave 132 — also includes customer_type so the termination WO inherits
// the broadband/enterprise badge from the customer record. COALESCE'd
// to '' for any legacy rows that don't carry a customer_type.
func (g *Gateway) CustomerSummary(ctx context.Context, customerID uuid.UUID) (*port.CustomerSummary, error) {
	row := g.pool.QueryRow(ctx, `
		SELECT
		  c.id, o.id, c.status, COALESCE(c.address, ''), c.branch_id,
		  c.activated_at, c.lock_in_until,
		  COALESCE(o.monthly_price, 0), COALESCE(o.otc_price, 0),
		  COALESCE(c.customer_type, '')
		FROM crm.customers c
		LEFT JOIN LATERAL (
		     SELECT id, monthly_price, otc_price FROM crm.orders
		      WHERE customer_id = c.id ORDER BY created_at DESC LIMIT 1
		) o ON TRUE
		WHERE c.id = $1
	`, customerID)
	var s port.CustomerSummary
	if err := row.Scan(&s.CustomerID, &s.OrderID, &s.Status, &s.Address, &s.BranchID,
		&s.ActivatedAt, &s.LockInUntil, &s.MonthlyPrice, &s.OTCPrice,
		&s.CustomerType); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, derrors.NotFound("customer.not_found", "customer not found")
		}
		return nil, derrors.Wrap(derrors.KindInternal, "crm.cust_summary", "read summary", err)
	}
	return &s, nil
}

// RecordReferral attaches a referee to a referrer. Resolution order:
//   1. explicit referrer_customer_id (if any)
//   2. referrer_code lookup in crm.customers.referral_code
// At least one of the two must resolve. We never throw if the referee
// already has a referral (idempotent), but we don't overwrite either.
func (g *Gateway) RecordReferral(ctx context.Context, refereeID uuid.UUID, code string, referrerID *uuid.UUID) (*port.ReferralRow, error) {
	// Already attached?
	if r, _ := g.ReferralForReferee(ctx, refereeID); r != nil {
		return r, nil
	}
	var resolved *uuid.UUID
	if referrerID != nil {
		resolved = referrerID
	} else if code != "" {
		row := g.pool.QueryRow(ctx, `SELECT id FROM crm.customers WHERE referral_code = $1 LIMIT 1`, code)
		var id uuid.UUID
		if err := row.Scan(&id); err == nil {
			resolved = &id
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return nil, derrors.Wrap(derrors.KindInternal, "crm.referral_lookup", "lookup referrer", err)
		}
	}
	if resolved == nil && code == "" {
		return nil, derrors.Validation("referral.required", "referrer_code or referrer_id required")
	}
	row := g.pool.QueryRow(ctx, `
		INSERT INTO crm.referrals (referrer_customer_id, referee_customer_id, referrer_code, status)
		VALUES ($1, $2, $3, 'pending')
		RETURNING id, referrer_customer_id, referee_customer_id, referrer_code, status, rewarded_at, created_at
	`, resolved, refereeID, code)
	var r port.ReferralRow
	if err := row.Scan(&r.ID, &r.ReferrerCustomerID, &r.RefereeCustomerID, &r.ReferrerCode,
		&r.Status, &r.RewardedAt, &r.CreatedAt); err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "crm.referral_insert", "insert referral", err)
	}
	return &r, nil
}

// FindCustomerByNumber resolves a customer by `customer_number` alone.
// Called from the OTP confirm leg, where phone possession was already
// proved during the request leg.
func (g *Gateway) FindCustomerByNumber(ctx context.Context, customerNumber string) (uuid.UUID, error) {
	row := g.pool.QueryRow(ctx,
		`SELECT id FROM crm.customers WHERE customer_number = $1 LIMIT 1`,
		customerNumber)
	var id uuid.UUID
	if err := row.Scan(&id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, derrors.NotFound("customer.not_found",
				"customer_number not found")
		}
		return uuid.Nil, derrors.Wrap(derrors.KindInternal, "crm.cust_lookup_n",
			"customer lookup by number failed", err)
	}
	return id, nil
}

// FindCustomerByNumberAndPhone resolves the customer-portal sign-in pair
// to the customer's UUID. Phone is normalised by stripping whitespace
// and trailing/leading non-digits — Indonesia phone numbers often arrive
// in mixed formats (+62…, 0…, 62…), and the customer typing the form
// won't always match what's on file. The customer_number is matched
// exactly because it's a known machine-issued string.
func (g *Gateway) FindCustomerByNumberAndPhone(ctx context.Context, customerNumber, phone string) (uuid.UUID, error) {
	// regexp_replace strips every char that isn't a digit, so the stored
	// phone "+62 812 3456 7890" and the typed "081234567890" both reduce
	// to "6281234567890" / "081234567890". We compare the trailing 9
	// digits to be tolerant of the country-code variants.
	row := g.pool.QueryRow(ctx, `
		SELECT id FROM crm.customers
		 WHERE customer_number = $1
		   AND right(regexp_replace(phone, '\D', '', 'g'), 9)
		     = right(regexp_replace($2,    '\D', '', 'g'), 9)
		LIMIT 1
	`, customerNumber, phone)
	var id uuid.UUID
	if err := row.Scan(&id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, derrors.NotFound("customer.not_found",
				"no customer matches the supplied number + phone")
		}
		return uuid.Nil, derrors.Wrap(derrors.KindInternal, "crm.cust_lookup",
			"customer lookup failed", err)
	}
	return id, nil
}

func (g *Gateway) ReferralForReferee(ctx context.Context, refereeID uuid.UUID) (*port.ReferralRow, error) {
	row := g.pool.QueryRow(ctx, `
		SELECT id, referrer_customer_id, referee_customer_id, referrer_code, status, rewarded_at, created_at
		FROM crm.referrals WHERE referee_customer_id = $1
	`, refereeID)
	var r port.ReferralRow
	if err := row.Scan(&r.ID, &r.ReferrerCustomerID, &r.RefereeCustomerID, &r.ReferrerCode,
		&r.Status, &r.RewardedAt, &r.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, derrors.Wrap(derrors.KindInternal, "crm.referral_lookup", "read referral", err)
	}
	return &r, nil
}

// ActiveAddonsForCustomer (Phase 1B — TC-billing-addon-merge) returns
// the customer's currently active add-ons so the recurring scheduler
// can fold them into the monthly invoice. We filter on status='active'
// — addons in 'pending_install' / 'cancelled' don't bill yet.
//
// LEFT JOIN against products lets us populate a human-readable name
// even when the addon's product row no longer exists (the addon row's
// monthly_fee snapshot is authoritative regardless).
func (g *Gateway) ActiveAddonsForCustomer(
	ctx context.Context, customerID uuid.UUID,
) ([]port.CustomerAddon, error) {
	rows, err := g.pool.Query(ctx, `
		SELECT ca.addon_id, COALESCE(p.name, ''), ca.quantity, ca.monthly_fee
		FROM crm.customer_addons ca
		LEFT JOIN crm.products p ON p.id = ca.addon_id
		WHERE ca.customer_id = $1 AND ca.status = 'active'
		ORDER BY ca.created_at
	`, customerID)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal,
			"crm.active_addons_lookup", "load active addons", err)
	}
	defer rows.Close()
	out := []port.CustomerAddon{}
	for rows.Next() {
		var a port.CustomerAddon
		if err := rows.Scan(&a.AddonID, &a.Name, &a.Quantity, &a.MonthlyFee); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal,
				"crm.active_addons_scan", "scan addon row", err)
		}
		out = append(out, a)
	}
	return out, nil
}
