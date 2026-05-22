// Suspension → restoration cycle end-to-end ("the cut + uncut").
//
// Wave 57 — the auto_termination test proves overdue customers get
// suspended (and eventually terminated). It does NOT cover the
// restoration leg: customer pays the overdue invoice → next billing
// tick flips them back to active. Without that test, a regression
// where the restore pass stops firing would leave paying customers
// permanently suspended.
//
//   1. setupActiveCustomer (helper) — fresh active subscriber
//   2. Tighten billing.policies (suspend_after_days=0, late_fee 0)
//   3. Mint + backdate a subscription invoice
//   4. POST /cycles/run → customers_suspended >= 1, customer.status
//      flips to 'suspended'
//   5. Customer pays the overdue invoice in full
//   6. POST /cycles/run → customers_restored >= 1, customer.status
//      flips back to 'active'
//   7. RADIUS state: post-restore must be PERMANENT_ACTIVE, not
//      SUSPENDED (defensive — if the restore hook doesn't fire on
//      RADIUS, the customer's billing says "active" but they still
//      can't connect)
//
//go:build e2e

package e2e

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSuspensionRestorationCycle(t *testing.T) {
	admin := newClient(t)
	admin.login()

	w := setupActiveCustomer(t, admin)

	// DB pool — backdating an invoice's due_date is a pgxpool operation;
	// no HTTP endpoint exposes that knob.
	dbURL := envOr("DATABASE_URL",
		"postgres://syabanf@localhost:5432/ion_core?sslmode=disable")
	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		t.Fatalf("pgx pool: %v", err)
	}
	defer pool.Close()
	ctx := context.Background()

	// -----------------------------------------------------------------
	// 1. Tighten policies so suspend fires on day 0. Leave
	//    terminate_after_suspended_days HIGH so we don't auto-terminate
	//    during the test — we only want to exercise suspend + restore.
	// -----------------------------------------------------------------
	admin.do("PATCH", "/api/billing/policy", map[string]any{
		"late_fee_grace_days":            365,
		"late_fee_amount":                0,
		"suspend_after_days":             0,
		"terminate_after_suspended_days": 365,
		"notify_customer_days_before":    0,
	}, nil, 200)

	// -----------------------------------------------------------------
	// 2. Mint a recurring invoice. The customer just activated, so the
	//    cycles/run pass should generate at least one subscription
	//    invoice on this customer's order. recurring_generated may be
	//    0 if the tick already ran during the activation hook — we
	//    only care that there's an issued subscription invoice we can
	//    backdate.
	// -----------------------------------------------------------------
	admin.do("POST", "/api/billing/cycles/run", nil, nil, 200)

	// Backdate every issued invoice on this customer to 7 days past due.
	if _, err := pool.Exec(ctx, `
		UPDATE billing.invoices
		   SET due_date = NOW() - INTERVAL '7 days'
		 WHERE customer_id = $1
		   AND status = 'issued'
	`, uuid.MustParse(w.CustomerID)); err != nil {
		t.Fatalf("backdate invoices: %v", err)
	}

	// -----------------------------------------------------------------
	// 3. SUSPEND — fire the tick.
	// -----------------------------------------------------------------
	var suspend struct {
		CustomersSuspended int `json:"customers_suspended"`
	}
	admin.do("POST", "/api/billing/cycles/run", nil, &suspend, 200)
	if suspend.CustomersSuspended < 1 {
		t.Fatalf("expected ≥1 suspension; report: %+v", suspend)
	}

	var afterSuspend struct {
		Status string `json:"status"`
	}
	admin.do("GET", "/api/crm/customers/"+w.CustomerID, nil, &afterSuspend, 200)
	if afterSuspend.Status != "suspended" {
		t.Fatalf("customer.status: want suspended, got %q", afterSuspend.Status)
	}

	// -----------------------------------------------------------------
	// 4. RESTORE — customer pays the overdue invoices.
	//
	// The suspension test's auto_termination_e2e backdates once and then
	// runs cycles/run twice; the second tick fires terminate. For
	// restoration we instead PAY everything outstanding then run the
	// tick. The restore pass scans for suspended customers with no
	// outstanding past-due balance and flips them back to active.
	// -----------------------------------------------------------------
	var invs struct {
		Items []struct {
			ID                string  `json:"id"`
			Status            string  `json:"status"`
			OutstandingAmount float64 `json:"outstanding_amount"`
		} `json:"items"`
	}
	admin.do("GET", "/api/billing/invoices?customer_id="+w.CustomerID, nil, &invs, 200)
	paidCount := 0
	for _, inv := range invs.Items {
		if inv.Status == "paid" || inv.OutstandingAmount <= 0 {
			continue
		}
		admin.do("POST", "/api/billing/invoices/"+inv.ID+"/payments", map[string]any{
			"amount":                 inv.OutstandingAmount,
			"payment_method":         "manual_bank_transfer",
			"gateway_transaction_id": "W57-RESTORE-" + inv.ID,
			"notes":                  "Wave 57 — clearing overdue to trigger restore",
		}, nil, 201)
		paidCount++
	}
	if paidCount == 0 {
		t.Fatal("expected at least one overdue invoice to pay; none found")
	}

	// -----------------------------------------------------------------
	// 5. Run the tick again. The restore pass should pick up our
	//    now-zero-outstanding customer.
	// -----------------------------------------------------------------
	var restore struct {
		CustomersRestored int `json:"customers_restored"`
	}
	admin.do("POST", "/api/billing/cycles/run", nil, &restore, 200)
	if restore.CustomersRestored < 1 {
		t.Fatalf("expected ≥1 restoration; report: %+v", restore)
	}

	var afterRestore struct {
		Status string `json:"status"`
	}
	admin.do("GET", "/api/crm/customers/"+w.CustomerID, nil, &afterRestore, 200)
	if afterRestore.Status != "active" {
		t.Fatalf("customer.status post-restore: want active, got %q", afterRestore.Status)
	}

	// -----------------------------------------------------------------
	// 6. RADIUS state must follow billing — restored billing-side
	//    customer with a still-SUSPENDED RADIUS row means they're
	//    paying but offline. Catch that with a direct DB read.
	// -----------------------------------------------------------------
	var radiusStatus string
	err = pool.QueryRow(ctx,
		`SELECT status FROM network.radius_accounts WHERE customer_id = $1`,
		uuid.MustParse(w.CustomerID),
	).Scan(&radiusStatus)
	if err != nil {
		// If no row exists, the activation hook never wired it. That's
		// a separate (worse) bug; surface it.
		t.Fatalf("radius row missing for restored customer %s: %v", w.CustomerID, err)
	}
	if radiusStatus == "SUSPENDED" {
		t.Fatalf("RADIUS still SUSPENDED after billing restore — customer paid but offline")
	}
	// PERMANENT_ACTIVE is the expected post-restore state; TEMPORARY
	// also passes (some configs leave it temp until next anniversary).
	if radiusStatus != "PERMANENT_ACTIVE" && radiusStatus != "TEMPORARY" {
		t.Errorf("radius status after restore: want PERMANENT_ACTIVE or TEMPORARY, got %q", radiusStatus)
	}
}
