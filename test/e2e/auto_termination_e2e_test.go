// Auto-termination end-to-end.
//
// Drives the scheduler-triggered termination path: an active customer
// with an overdue invoice that ages past suspend_after_days gets
// suspended (RADIUS SUSPENDED, customer status 'suspended'); after
// terminate_after_suspended_days, the next tick mints a
// termination_request (kind=auto) and a termination work-order.
//
// We compress the time axis by setting the policy thresholds to 0
// days and forcing one invoice's due_date into the past via SQL.
// In a single /cycles/run call the scheduler walks all four passes in
// order — recurring (no-op, already generated), late-fees (gated off
// here), suspend (fires), terminate (fires).
//
//go:build e2e

package e2e

import (
	"context"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAutoTermination(t *testing.T) {
	c := newClient(t)
	c.login()

	sx := suffix()
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	odpLat := -6.0 - rng.Float64()*0.5
	odpLng := 106.5 + rng.Float64()*0.5
	leadLat := odpLat - 0.0001
	leadLng := odpLng + 0.0001

	var (
		areaID, leadID                         string
		customerID, orderID                    string
		tlID, seniorID, juniorID, teamID, woID string
		bastID                                 string
	)

	// =====================================================================
	// Phase A — same primitives as the happy path, condensed.
	// We need an active customer with a paid OTC so the install
	// activation hook drives RADIUS to PERMANENT_ACTIVE.
	// =====================================================================

	t.Run("A_setup_active_customer", func(t *testing.T) {
		var regional struct{ ID string `json:"id"` }
		c.do("POST", "/api/identity/branches", map[string]any{
			"name":  "AT Regional " + sx,
			"code":  "AT-REG-" + sx,
			"level": "regional",
		}, &regional, 201)
		var area struct{ ID string `json:"id"` }
		c.do("POST", "/api/identity/branches", map[string]any{
			"name": "AT Area " + sx, "code": "AT-AREA-" + sx,
			"level": "area", "parent_id": regional.ID,
		}, &area, 201)
		areaID = area.ID

		// ODP for coverage.
		var types struct {
			Items []struct {
				ID      string `json:"id"`
				TypeKey string `json:"type_key"`
			} `json:"items"`
		}
		c.do("GET", "/api/network/node-types", nil, &types, 200)
		var odpTypeID string
		for _, nt := range types.Items {
			if nt.TypeKey == "odp" {
				odpTypeID = nt.ID
			}
		}
		var node struct{ ID string `json:"id"` }
		c.do("POST", "/api/network/nodes", map[string]any{
			"node_type_id":      odpTypeID,
			"name":              "AT ODP " + sx,
			"code":              "AT-ODP-" + sx,
			"branch_id":         areaID,
			"address":           "auto-term test",
			"gps_lat":           odpLat,
			"gps_lng":           odpLng,
			"total_ports":       8,
			"port_role":         "customer_drop",
			"coverage_radius_m": 200,
		}, &node, 201)

		// Lead + convert.
		var products struct {
			Items []struct{ ID string } `json:"items"`
		}
		c.do("GET", "/api/crm/products?active_only=true", nil, &products, 200)
		var lead struct {
			ID        string `json:"id"`
			Documents []struct {
				ID       string `json:"id"`
				Required bool   `json:"required"`
			} `json:"documents"`
		}
		c.do("POST", "/api/crm/leads", map[string]any{
			"full_name":  "AutoTerm " + sx,
			"phone":      "+62813" + sx + "00",
			"nik":        "31740" + sx + "9999",
			"address":    "Jl. AutoTerm " + sx,
			"gps_lat":    leadLat,
			"gps_lng":    leadLng,
			"product_id": products.Items[0].ID,
		}, &lead, 201)
		leadID = lead.ID
		for _, d := range lead.Documents {
			if d.Required {
				c.do("PATCH", "/api/crm/documents/"+d.ID, map[string]any{"submitted": true}, nil, 200)
			}
		}
		var conv struct {
			Customer struct{ ID string } `json:"customer"`
			Order    struct{ ID string } `json:"order"`
		}
		c.do("POST", "/api/crm/leads/"+leadID+"/convert", map[string]any{}, &conv, 200)
		customerID = conv.Customer.ID
		orderID = conv.Order.ID

		// Field setup + WO + checklist + BAST + payment + NOC.
		mk := func(empID, name, email string, roles []string, grade *string) string {
			body := map[string]any{
				"employee_id": empID, "full_name": name, "email": email,
				"phone": "+62811AT" + empID, "password": "Pass1234!" + sx,
				"branch_id": areaID, "roles": roles,
			}
			if grade != nil {
				body["technician_grade"] = *grade
			}
			var u struct{ ID string `json:"id"` }
			c.do("POST", "/api/identity/users", body, &u, 201)
			return u.ID
		}
		tlID = mk("TLAT"+sx, "TL AT "+sx, "tlat"+sx+"@ion.local",
			[]string{"team_leader"}, nil)
		sg := "senior"
		jg := "junior"
		seniorID = mk("SRAT"+sx, "SR AT "+sx, "srat"+sx+"@ion.local",
			[]string{"technician"}, &sg)
		juniorID = mk("JRAT"+sx, "JR AT "+sx, "jrat"+sx+"@ion.local",
			[]string{"technician"}, &jg)
		var team struct{ ID string `json:"id"` }
		c.do("POST", "/api/field/teams", map[string]any{
			"code": "TM-AT-" + sx, "name": "Team AT " + sx,
			"branch_id": areaID, "team_leader_id": tlID,
		}, &team, 201)
		teamID = team.ID
		c.do("POST", "/api/field/teams/"+teamID+"/members",
			map[string]any{"user_id": seniorID, "grade": "senior"}, nil, 201)
		c.do("POST", "/api/field/teams/"+teamID+"/members",
			map[string]any{"user_id": juniorID, "grade": "junior"}, nil, 201)
		var wo struct{ ID string `json:"id"` }
		c.do("POST", "/api/field/work-orders",
			map[string]any{"order_id": orderID}, &wo, 201)
		woID = wo.ID

		c.do("POST", "/api/field/work-orders/"+woID+"/route",
			map[string]any{"team_id": teamID}, nil, 200)
		c.do("POST", "/api/field/work-orders/"+woID+"/assign", map[string]any{
			"lead_id": seniorID, "lead_grade": "senior",
			"observer_id": juniorID, "observer_grade": "junior",
		}, nil, 200)
		c.do("POST", "/api/field/work-orders/"+woID+"/status",
			map[string]any{"status": "in_progress"}, nil, 200)

		var detail struct {
			ChecklistItems []struct {
				ID          string `json:"id"`
				GPSRequired bool   `json:"gps_required"`
			} `json:"checklist_items"`
		}
		c.do("GET", "/api/field/work-orders/"+woID, nil, &detail, 200)
		for _, it := range detail.ChecklistItems {
			body := map[string]any{"template_item_id": it.ID, "response_text": "ok"}
			if it.GPSRequired {
				body["gps_lat"] = leadLat
				body["gps_lng"] = leadLng
			}
			c.do("POST", "/api/field/work-orders/"+woID+"/checklist", body, nil, 200)
		}
		var b struct{ ID string `json:"id"` }
		c.do("POST", "/api/field/work-orders/"+woID+"/bast",
			map[string]any{"sign_off_mode": "on_site"}, &b, 200)
		bastID = b.ID

		// Pay the auto-OTC then NOC-approve — install hook drives
		// activation + RADIUS PERMANENT_ACTIVE + customer 'active'.
		var invs struct {
			Items []struct {
				ID    string  `json:"id"`
				Total float64 `json:"total"`
			} `json:"items"`
		}
		c.do("GET", "/api/billing/invoices?order_id="+orderID, nil, &invs, 200)
		c.do("POST", "/api/billing/invoices/"+invs.Items[0].ID+"/payments", map[string]any{
			"amount":         invs.Items[0].Total,
			"payment_method": "manual_bank_transfer",
		}, nil, 200)
		c.do("POST", "/api/field/basts/"+bastID+"/verify",
			map[string]any{"decision": "approved"}, nil, 200)
	})

	// =====================================================================
	// Phase B — load a DB pool. We use it to (a) mint a fresh recurring
	// invoice via /cycles/run, then (b) backdate the due_date so the
	// scheduler treats it as past due. Both are real product knobs —
	// the manual tick is a /api/billing/cycles/run endpoint and the
	// due_date is a real column on the issued invoice.
	// =====================================================================

	dbURL := envOr("DATABASE_URL", "postgres://syabanf@localhost:5432/ion_core?sslmode=disable")
	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		t.Fatalf("pgx pool: %v", err)
	}
	defer pool.Close()

	// =====================================================================
	// Phase C — squeeze the policy + age the invoice + tick.
	// =====================================================================

	t.Run("C01_tighten_policy_and_age_invoice", func(t *testing.T) {
		// Tighten suspend + terminate to fire on day 0. Bump late-fee
		// grace high enough that it doesn't mint an extra invoice
		// during our test.
		c.do("PATCH", "/api/billing/policy", map[string]any{
			"late_fee_grace_days":             365,
			"late_fee_amount":                 0,
			"suspend_after_days":              0,
			"terminate_after_suspended_days":  0,
			"notify_customer_days_before":     0,
		}, nil, 200)

		// Force a recurring invoice cycle. The customer just activated
		// today, so anniversaryPeriod gives us a period that contains
		// "now". RunBillingTick creates the invoice + cycle row.
		var rep struct {
			RecurringGenerated int `json:"recurring_generated"`
		}
		c.do("POST", "/api/billing/cycles/run", nil, &rep, 200)
		if rep.RecurringGenerated < 1 {
			// The recurring may have already been generated in a prior
			// tick (e.g. the startup tick). That's fine — we just need
			// at least one issued invoice on this customer.
		}

		// Backdate every issued invoice on this customer to a past due
		// date. The scheduler's suspend pass scans for due_date <
		// now - suspend_after_days, so with the threshold at 0 days,
		// any past-dated invoice qualifies.
		if _, err := pool.Exec(context.Background(), `
			UPDATE billing.invoices
			   SET due_date = NOW() - INTERVAL '7 days'
			 WHERE customer_id = $1
			   AND status = 'issued'
		`, uuid.MustParse(customerID)); err != nil {
			t.Fatalf("backdate invoice: %v", err)
		}
	})

	t.Run("C02_tick_suspend", func(t *testing.T) {
		// Tick 1 fires the suspend pass: customer flips to 'suspended'
		// and suspended_at is stamped to DB NOW(). The same tick's
		// termination pass *won't* fire on the customer because
		// suspended_at is fractionally after the tick's captured `now`.
		var rep struct {
			CustomersSuspended int `json:"customers_suspended"`
		}
		c.do("POST", "/api/billing/cycles/run", nil, &rep, 200)
		if rep.CustomersSuspended < 1 {
			t.Fatalf("expected suspend to fire; report: %+v", rep)
		}
	})

	t.Run("C03_tick_terminate", func(t *testing.T) {
		// Backdate suspended_at by one minute so the next tick's
		// termination-after-0-days check sees the customer as having
		// been suspended in the past. This is the test analogue of
		// "wait terminate_after_suspended_days" — in production it's
		// real wall time, here we collapse it.
		if _, err := pool.Exec(context.Background(),
			`UPDATE crm.customers SET suspended_at = NOW() - INTERVAL '1 minute' WHERE id = $1`,
			uuid.MustParse(customerID),
		); err != nil {
			t.Fatalf("backdate suspended_at: %v", err)
		}
		var rep struct {
			TerminationsTriggered int `json:"terminations_triggered"`
		}
		c.do("POST", "/api/billing/cycles/run", nil, &rep, 200)
		if rep.TerminationsTriggered < 1 {
			t.Fatalf("expected auto-termination to fire; report: %+v", rep)
		}
	})

	t.Run("C04_verify_termination_chain", func(t *testing.T) {
		// Termination request: kind=auto, status=wo_created, with a wo_id.
		var list struct {
			Items []struct {
				ID     string `json:"id"`
				Kind   string `json:"kind"`
				Status string `json:"status"`
				WOID   string `json:"wo_id"`
				Reason string `json:"reason"`
			} `json:"items"`
		}
		c.do("GET", "/api/billing/terminations?customer_id="+customerID, nil, &list, 200)
		if len(list.Items) == 0 {
			t.Fatal("no termination_request rows for this customer")
		}
		var got *struct {
			ID     string `json:"id"`
			Kind   string `json:"kind"`
			Status string `json:"status"`
			WOID   string `json:"wo_id"`
			Reason string `json:"reason"`
		}
		for i := range list.Items {
			if list.Items[i].Kind == "auto" {
				got = &list.Items[i]
				break
			}
		}
		if got == nil {
			t.Fatalf("no kind=auto request found; got: %+v", list.Items)
		}
		if got.WOID == "" {
			t.Fatalf("auto-termination request has no wo_id; status=%q", got.Status)
		}
		if !strings.Contains(got.Reason, "auto-termination") {
			t.Fatalf("expected reason to mention auto-termination, got %q", got.Reason)
		}

		// And the WO itself exists, with wo_type=termination.
		var wo struct {
			ID     string `json:"id"`
			WOType string `json:"wo_type"`
			Status string `json:"status"`
		}
		c.do("GET", "/api/field/work-orders/"+got.WOID, nil, &wo, 200)
		if wo.WOType != "termination" {
			t.Fatalf("wo_type: want termination, got %q", wo.WOType)
		}
		if wo.Status == "" {
			t.Fatal("wo status missing")
		}

		// Customer status should be 'suspended' (not yet terminated —
		// that requires the termination BAST to be approved, which is
		// exercised in TestVoluntaryTermination). RADIUS should be
		// SUSPENDED for the same reason.
		var cu struct {
			Status string `json:"status"`
		}
		c.do("GET", "/api/crm/customers/"+customerID, nil, &cu, 200)
		if cu.Status != "suspended" {
			t.Fatalf("customer status after auto-termination: want suspended, got %q", cu.Status)
		}
		var radStatus string
		if err := pool.QueryRow(context.Background(),
			`SELECT status FROM network.radius_accounts WHERE customer_id = $1`,
			uuid.MustParse(customerID),
		).Scan(&radStatus); err != nil {
			t.Fatalf("read radius: %v", err)
		}
		if strings.ToLower(radStatus) != "suspended" {
			t.Fatalf("radius status: want suspended, got %q", radStatus)
		}
	})

	// =====================================================================
	// Phase D — leave the policy thresholds non-default for other tests
	// would be unfriendly; restore them. We don't actually care which
	// values pre-existed (each binary starts from the migration default),
	// so we put them back to the migration defaults explicitly.
	// =====================================================================

	t.Run("D_restore_default_policy", func(t *testing.T) {
		c.do("PATCH", "/api/billing/policy", map[string]any{
			"late_fee_grace_days":             7,
			"late_fee_amount":                 25000,
			"suspend_after_days":              30,
			"terminate_after_suspended_days":  60,
			"notify_customer_days_before":     3,
		}, nil, 200)
	})
}
