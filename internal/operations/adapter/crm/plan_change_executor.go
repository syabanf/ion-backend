// Wave 125 — SQL-only bridge from the operations module into the crm
// bounded context. The bridge:
//
//   - Inserts a row into `crm.plan_change_requests` when a Bulk Plan
//     Change item is applied. The CRM module owns the lifecycle from
//     here on (approval gates, RADIUS propagation, etc.).
//   - Resolves customer codes / plan codes during CSV import.
//   - Inspects the customer's current plan + status for the pre-flight
//     validator.
//
// No Go imports from internal/crm — by design. When CRM moves to its
// own binary, swap this for an HTTP adapter without touching the
// operations usecase layer.
package crm

import (
	"context"
	stderrors "errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/operations/domain"
	"github.com/ion-core/backend/internal/operations/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// PlanChangeBridge owns the SQL-only connection into crm.* tables.
type PlanChangeBridge struct {
	pool *pgxpool.Pool
}

func NewPlanChangeBridge(pool *pgxpool.Pool) *PlanChangeBridge {
	return &PlanChangeBridge{pool: pool}
}

// Compile-time interface checks.
var (
	_ port.PlanChangeExecutor          = (*PlanChangeBridge)(nil)
	_ domain.PlanChangeValidatorPort   = (*PlanChangeBridge)(nil)
)

// schemaInstalled returns true if the crm.plan_change_requests table is
// present. Used by Apply to fail fast on a misconfigured cluster.
func (b *PlanChangeBridge) schemaInstalled(ctx context.Context) (bool, error) {
	var n int
	err := b.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM information_schema.tables
		 WHERE table_schema = 'crm' AND table_name = 'plan_change_requests'
	`).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// Apply inserts a crm.plan_change_requests row. Idempotent on
// (customer_id, to_product_id, effective_at) — re-running the executor
// after a crash doesn't create duplicate requests.
func (b *PlanChangeBridge) Apply(ctx context.Context, item *domain.BulkPlanChangeItem) error {
	ok, err := b.schemaInstalled(ctx)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "bulk.crm_schema_probe",
			"probe crm schema", err)
	}
	if !ok {
		return derrors.New(derrors.KindUnavailable, "bulk.target_schema_missing",
			"crm.plan_change_requests not installed in this database")
	}
	// from_product_id: prefer the snapshot, fall back to the customer's
	// latest order product_id, else nil. The CHECK in
	// crm.plan_change_requests requires non-null, so we resolve at
	// apply-time.
	fromID := item.CurrentPlanID
	if fromID == nil {
		latest, err := b.latestProductForCustomer(ctx, item.CustomerID)
		if err == nil {
			fromID = latest
		}
	}
	if fromID == nil {
		return derrors.Validation("bulk.plan_change_no_from",
			"customer has no current product to change from")
	}
	if *fromID == item.TargetPlanID {
		// Caller's validator should have caught this; treat as no-op
		// rather than violate the CHECK.
		return derrors.Validation("bulk.plan_change_noop",
			"customer is already on the target plan")
	}
	changeKind := "upgrade"
	if isDowngrade, err := b.isDowngrade(ctx, *fromID, item.TargetPlanID); err == nil && isDowngrade {
		changeKind = "downgrade"
	}
	effAt := item.EffectiveAt
	if effAt == nil {
		// Default to the start of the next month at UTC midnight.
		now := time.Now().UTC()
		next := time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC)
		effAt = &next
	}
	_, err = b.pool.Exec(ctx, `
		INSERT INTO crm.plan_change_requests
			(customer_id, from_product_id, to_product_id, change_kind,
			 reason, status, effective_at)
		VALUES ($1, $2, $3, $4, $5, 'pending', $6)
	`, item.CustomerID, *fromID, item.TargetPlanID, changeKind,
		"bulk:"+item.BulkJobID.String(), *effAt)
	if err != nil {
		return mapDBError(err, "bulk.plan_change_apply", "insert plan_change_request")
	}
	return nil
}

// PlanExists — domain.PlanChangeValidatorPort.
func (b *PlanChangeBridge) PlanExists(ctx context.Context, planID uuid.UUID) (bool, error) {
	var n int
	err := b.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM crm.products WHERE id = $1
	`, planID).Scan(&n)
	if err != nil {
		return false, mapDBError(err, "bulk.plan_exists", "probe crm.products")
	}
	return n > 0, nil
}

// CustomerCurrentPlan — domain.PlanChangeValidatorPort. Returns the
// customer's current product (from the latest non-cancelled order) and
// the customer.status text. Returns (nil, "", nil) if the customer
// doesn't exist.
func (b *PlanChangeBridge) CustomerCurrentPlan(ctx context.Context, customerID uuid.UUID) (*uuid.UUID, string, error) {
	var status string
	err := b.pool.QueryRow(ctx, `
		SELECT status FROM crm.customers WHERE id = $1
	`, customerID).Scan(&status)
	if stderrors.Is(err, pgx.ErrNoRows) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", mapDBError(err, "bulk.customer_status", "probe crm.customers")
	}
	pid, _ := b.latestProductForCustomer(ctx, customerID)
	return pid, status, nil
}

func (b *PlanChangeBridge) latestProductForCustomer(ctx context.Context, customerID uuid.UUID) (*uuid.UUID, error) {
	var pid uuid.UUID
	err := b.pool.QueryRow(ctx, `
		SELECT product_id
		  FROM crm.orders
		 WHERE customer_id = $1
		   AND product_id IS NOT NULL
		   AND status <> 'cancelled'
		 ORDER BY created_at DESC
		 LIMIT 1
	`, customerID).Scan(&pid)
	if stderrors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, mapDBError(err, "bulk.customer_product", "probe crm.orders")
	}
	return &pid, nil
}

// isDowngrade — compares the monthly price of the two products. Returns
// true if to_product.monthly_price < from_product.monthly_price.
func (b *PlanChangeBridge) isDowngrade(ctx context.Context, fromID, toID uuid.UUID) (bool, error) {
	var fromPrice, toPrice float64
	err := b.pool.QueryRow(ctx, `
		SELECT
		  (SELECT monthly_price FROM crm.products WHERE id = $1),
		  (SELECT monthly_price FROM crm.products WHERE id = $2)
	`, fromID, toID).Scan(&fromPrice, &toPrice)
	if err != nil {
		return false, err
	}
	return toPrice < fromPrice, nil
}

// =====================================================================
// CSV lookup port — partial implementation for plan_code + customer_no.
// =====================================================================

// CustomerIDByNumber — port.CSVLookupPort.
func (b *PlanChangeBridge) CustomerIDByNumber(ctx context.Context, customerNo string) (*uuid.UUID, error) {
	var id uuid.UUID
	err := b.pool.QueryRow(ctx, `
		SELECT id FROM crm.customers WHERE customer_number = $1
	`, customerNo).Scan(&id)
	if stderrors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, mapDBError(err, "bulk.customer_lookup", "probe crm.customers")
	}
	return &id, nil
}

// PlanIDByCode — port.CSVLookupPort.
func (b *PlanChangeBridge) PlanIDByCode(ctx context.Context, code string) (*uuid.UUID, error) {
	var id uuid.UUID
	err := b.pool.QueryRow(ctx, `
		SELECT id FROM crm.products WHERE code = $1 AND active = TRUE
	`, code).Scan(&id)
	if stderrors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, mapDBError(err, "bulk.plan_lookup", "probe crm.products")
	}
	return &id, nil
}
