// RADIUS lifecycle end-to-end.
//
// Locks in all four state transitions per PRD §13:
//
//	(1) Provision()         WO created           → TEMPORARY
//	(2) PromoteToPermanent  NOC verified BAST    → PERMANENT_ACTIVE
//	(3) Suspend             suspension scheduler → SUSPENDED
//	(4) Restore             payment cleared      → PERMANENT_ACTIVE
//	(5) Deactivate          termination BAST     → DEACTIVATED
//
// The happy-path e2e covers (1)+(2). The auto-termination e2e covers (3)
// indirectly + the auto-termination path skips (4) entirely. The voluntary
// termination e2e covers (5) — but never on a customer that has been
// suspended-then-restored. This test exercises the **restore** transition
// explicitly because it's the only one without dedicated coverage; we
// also re-assert (1)–(3) and (5) on the same customer so a regression in
// any transition flags here.
//
// All assertions read directly from network.radius_accounts so we catch
// both kinds of regressions: the platform layer firing the wrong call,
// and the RADIUS adapter persisting the wrong state. The test is
// deliberately scoped to RADIUS — billing/field/CRM assertions live in
// the flows that own those signals.
//
//go:build e2e

package e2e

import (
	"context"
	"math/rand"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRadiusLifecycle(t *testing.T) {
	// Wave 65 — re-enabled after Phase 1A closure. The three original
	// blockers are addressed:
	//
	//   (1) RADIUS row missing after WO create — fixed by moving
	//       Provision from VerifyBAST to CreateWOFromOrder per PRD §13.
	//       The TEMPORARY row now exists from the moment the WO is
	//       minted, so this test's `afterProvision` read (line 280)
	//       finds it.
	//   (2) Suspend/restore scheduler reported 0 transitions — the
	//       test now waits for the cycle pass to run after issuing
	//       the recurring invoice and backdating it.
	//   (3) Deactivation endpoint shape — corrected to the actual
	//       route `POST /api/billing/terminations` (handler is the
	//       voluntary termination usecase under the hood).

	c := newClient(t)
	c.login()

	sx := suffix()
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	odpLat := -6.0 - rng.Float64()*0.5
	odpLng := 106.5 + rng.Float64()*0.5
	leadLat := odpLat - 0.0001
	leadLng := odpLng + 0.0001

	dbURL := envOr("DATABASE_URL", "postgres://syabanf@localhost:5432/ion_core?sslmode=disable")
	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		t.Fatalf("pgx pool: %v", err)
	}
	defer pool.Close()

	// radiusRow grabs the single row for this customer. Returning a
	// struct keeps the assertions close to the test rather than fanning
	// out into per-field helpers.
	type radiusRow struct {
		Status            string
		TempActivatedAt   *time.Time
		PermActivatedAt   *time.Time
		SuspendedAt       *time.Time
	}
	readRadius := func(t *testing.T, customerID string) radiusRow {
		t.Helper()
		var r radiusRow
		err := pool.QueryRow(context.Background(), `
			SELECT status, temp_activated_at, perm_activated_at, suspended_at
			  FROM network.radius_accounts
			 WHERE customer_id = $1
		`, uuid.MustParse(customerID)).Scan(
			&r.Status, &r.TempActivatedAt, &r.PermActivatedAt, &r.SuspendedAt,
		)
		if err != nil {
			t.Fatalf("read radius_accounts for %s: %v", customerID, err)
		}
		return r
	}

	var (
		areaID, leadID                         string
		customerID, orderID                    string
		tlID, seniorID, juniorID, teamID, woID string
		bastID                                 string
	)

	// =====================================================================
	// Phase A — land an active customer (same primitives as the happy
	// path, condensed). After this phase the customer should be PERMANENT_
	// ACTIVE and we've crossed transitions (1) and (2).
	// =====================================================================

	t.Run("A_setup_active_customer", func(t *testing.T) {
		var regional struct {
			ID string `json:"id"`
		}
		c.do("POST", "/api/identity/branches", map[string]any{
			"name": "RX Regional " + sx, "code": "RX-REG-" + sx, "level": "regional",
		}, &regional, 201)
		var area struct {
			ID string `json:"id"`
		}
		c.do("POST", "/api/identity/branches", map[string]any{
			"name": "RX Area " + sx, "code": "RX-AREA-" + sx,
			"level": "area", "parent_id": regional.ID,
		}, &area, 201)
		areaID = area.ID

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
		var node struct {
			ID string `json:"id"`
		}
		c.do("POST", "/api/network/nodes", map[string]any{
			"node_type_id":      odpTypeID,
			"name":              "RX ODP " + sx,
			"code":              "RX-ODP-" + sx,
			"branch_id":         areaID,
			"address":           "radius-lifecycle test",
			"gps_lat":           odpLat,
			"gps_lng":           odpLng,
			"total_ports":       8,
			"port_role":         "customer_drop",
			"coverage_radius_m": 200,
		}, &node, 201)

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
			"full_name":  "RX " + sx,
			"phone":      "+62813" + sx + "55",
			"nik":        "31740" + sx + "8888",
			"address":    "Jl. RX " + sx,
			"gps_lat":    leadLat,
			"gps_lng":    leadLng,
			"product_id": products.Items[0].ID,
		}, &lead, 201)
		leadID = lead.ID
		for _, d := range lead.Documents {
			if d.Required {
				c.do("PATCH", "/api/crm/documents/"+d.ID,
					map[string]any{"submitted": true}, nil, 200)
			}
		}

		var conv struct {
			Customer struct{ ID string } `json:"customer"`
			Order    struct{ ID string } `json:"order"`
		}
		c.do("POST", "/api/crm/leads/"+leadID+"/convert", map[string]any{}, &conv, 200)
		customerID = conv.Customer.ID
		orderID = conv.Order.ID

		// Field setup.
		mk := func(empID, name, email string, roles []string, grade *string) string {
			body := map[string]any{
				"employee_id": empID, "full_name": name, "email": email,
				"phone": "+62811RX" + empID, "password": "Pass1234!" + sx,
				"branch_id": areaID, "roles": roles,
			}
			if grade != nil {
				body["technician_grade"] = *grade
			}
			var u struct {
				ID string `json:"id"`
			}
			c.do("POST", "/api/identity/users", body, &u, 201)
			return u.ID
		}
		tlID = mk("TLRX"+sx, "TL RX "+sx, "tlrx"+sx+"@ion.local",
			[]string{"team_leader"}, nil)
		sg := "senior"
		jg := "junior"
		seniorID = mk("SRRX"+sx, "SR RX "+sx, "srrx"+sx+"@ion.local",
			[]string{"technician"}, &sg)
		juniorID = mk("JRRX"+sx, "JR RX "+sx, "jrrx"+sx+"@ion.local",
			[]string{"technician"}, &jg)
		var team struct {
			ID string `json:"id"`
		}
		c.do("POST", "/api/field/teams", map[string]any{
			"code": "TM-RX-" + sx, "name": "Team RX " + sx,
			"branch_id": areaID, "team_leader_id": tlID,
		}, &team, 201)
		teamID = team.ID
		c.do("POST", "/api/field/teams/"+teamID+"/members",
			map[string]any{"user_id": seniorID, "grade": "senior"}, nil, 201)
		c.do("POST", "/api/field/teams/"+teamID+"/members",
			map[string]any{"user_id": juniorID, "grade": "junior"}, nil, 201)

		var wo struct {
			ID string `json:"id"`
		}
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

		var b struct {
			ID string `json:"id"`
		}
		c.do("POST", "/api/field/work-orders/"+woID+"/bast",
			map[string]any{"sign_off_mode": "on_site"}, &b, 200)
		bastID = b.ID

		// === transition (1): WO created drives Provision → TEMPORARY ===
		afterProvision := readRadius(t, customerID)
		if afterProvision.Status != "temporary" {
			t.Fatalf("post-provision status: want 'temporary', got %q", afterProvision.Status)
		}
		if afterProvision.TempActivatedAt == nil {
			t.Fatal("temp_activated_at not stamped on Provision")
		}

		// Pay the OTC + NOC-approve. NOC approval drives the
		// PromoteToPermanent call.
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

		// === transition (2): NOC approval drives PromoteToPermanent ===
		afterPromote := readRadius(t, customerID)
		if afterPromote.Status != "permanent_active" {
			t.Fatalf("post-promote status: want 'permanent_active', got %q", afterPromote.Status)
		}
		if afterPromote.PermActivatedAt == nil {
			t.Fatal("perm_activated_at not stamped on PromoteToPermanent")
		}
	})

	// =====================================================================
	// Phase B — drive Suspend via the scheduler.
	// =====================================================================

	t.Run("B_drive_suspend", func(t *testing.T) {
		// Tighten suspend to fire immediately. Late-fee grace stays high
		// so the late-fee pass doesn't mint a sibling invoice that would
		// confuse our payment-clearance assertion below. terminate_after
		// stays high so the *terminate* pass doesn't fire and run us
		// past Restore territory before we get to test it.
		c.do("PATCH", "/api/billing/policy", map[string]any{
			"late_fee_grace_days":            365,
			"late_fee_amount":                0,
			"suspend_after_days":             0,
			"terminate_after_suspended_days": 36500, // ~100yr; we restore before this matters
			"notify_customer_days_before":    0,
		}, nil, 200)

		// Mint a recurring invoice for this customer (no-op if one is
		// already there), then backdate every issued invoice's due_date.
		c.do("POST", "/api/billing/cycles/run", nil, nil, 200)
		if _, err := pool.Exec(context.Background(), `
			UPDATE billing.invoices
			   SET due_date = NOW() - INTERVAL '7 days'
			 WHERE customer_id = $1
			   AND status = 'issued'
		`, uuid.MustParse(customerID)); err != nil {
			t.Fatalf("backdate invoice: %v", err)
		}

		var rep struct {
			CustomersSuspended int `json:"customers_suspended"`
		}
		c.do("POST", "/api/billing/cycles/run", nil, &rep, 200)
		if rep.CustomersSuspended < 1 {
			t.Fatalf("expected suspend to fire; report: %+v", rep)
		}

		// === transition (3): scheduler drives Suspend → SUSPENDED ===
		afterSuspend := readRadius(t, customerID)
		if afterSuspend.Status != "suspended" {
			t.Fatalf("post-suspend status: want 'suspended', got %q", afterSuspend.Status)
		}
		if afterSuspend.SuspendedAt == nil {
			t.Fatal("suspended_at not stamped on Suspend")
		}
	})

	// =====================================================================
	// Phase C — drive Restore via clearing the overdue balance.
	//
	// This is the transition that has no dedicated coverage elsewhere
	// and the headline reason this test exists.
	// =====================================================================

	t.Run("C_drive_restore", func(t *testing.T) {
		// Pay every overdue issued invoice. The scheduler's
		// suspend/restore pass scans for suspended customers whose
		// outstanding balance is cleared and flips them back.
		var invs struct {
			Items []struct {
				ID     string  `json:"id"`
				Status string  `json:"status"`
				Total  float64 `json:"total"`
			} `json:"items"`
		}
		c.do("GET", "/api/billing/invoices?customer_id="+customerID, nil, &invs, 200)
		paid := 0
		for _, inv := range invs.Items {
			if inv.Status == "issued" || inv.Status == "overdue" {
				c.do("POST", "/api/billing/invoices/"+inv.ID+"/payments", map[string]any{
					"amount":         inv.Total,
					"payment_method": "manual_bank_transfer",
				}, nil, 200)
				paid++
			}
		}
		if paid == 0 {
			t.Fatal("no overdue invoices to pay; restore can't fire")
		}

		var rep struct {
			CustomersRestored int `json:"customers_restored"`
		}
		c.do("POST", "/api/billing/cycles/run", nil, &rep, 200)
		if rep.CustomersRestored < 1 {
			t.Fatalf("expected restore to fire; report: %+v", rep)
		}

		// === transition (4): scheduler drives Restore → PERMANENT_ACTIVE ===
		afterRestore := readRadius(t, customerID)
		if afterRestore.Status != "permanent_active" {
			t.Fatalf("post-restore status: want 'permanent_active', got %q", afterRestore.Status)
		}
		// Restore intentionally does NOT clear suspended_at — PRD
		// treats the prior suspension as historical fact. Verify the
		// audit trail is preserved.
		if afterRestore.SuspendedAt == nil {
			t.Fatal("suspended_at was nulled on Restore (should be retained as history)")
		}
		if afterRestore.PermActivatedAt == nil {
			t.Fatal("perm_activated_at lost on Restore")
		}
	})

	// =====================================================================
	// Phase D — drive Deactivate via voluntary termination.
	// =====================================================================

	t.Run("D_drive_deactivate", func(t *testing.T) {
		// Voluntary termination through the staff API. We don't need
		// the portal OTP flow here — that's covered in voluntary_term.
		var req struct {
			TerminationID string `json:"termination_id"`
		}
		// Wave 65 — `POST /api/billing/terminations` is the staff-side
		// voluntary termination endpoint (the handler internally calls
		// RequestVoluntaryTermination on the billing usecase). The
		// portal OTP flow lives at /portal/* and isn't needed here.
		c.do("POST", "/api/billing/terminations", map[string]any{
			"customer_id": customerID,
			"reason":      "radius lifecycle test cleanup",
		}, &req, 201)

		// Roll the termination WO through the same field flow as the
		// install: route → assign → in_progress → checklist → BAST →
		// NOC approve. We re-use the existing team.
		var twos struct {
			Items []struct {
				ID   string `json:"id"`
				Kind string `json:"kind"`
			} `json:"items"`
		}
		c.do("GET", "/api/field/work-orders?customer_id="+customerID, nil, &twos, 200)
		var termWOID string
		for _, w := range twos.Items {
			if w.Kind == "termination" {
				termWOID = w.ID
			}
		}
		if termWOID == "" {
			t.Fatal("no termination WO found for customer")
		}
		c.do("POST", "/api/field/work-orders/"+termWOID+"/route",
			map[string]any{"team_id": teamID}, nil, 200)
		c.do("POST", "/api/field/work-orders/"+termWOID+"/assign", map[string]any{
			"lead_id": seniorID, "lead_grade": "senior",
			"observer_id": juniorID, "observer_grade": "junior",
		}, nil, 200)
		c.do("POST", "/api/field/work-orders/"+termWOID+"/status",
			map[string]any{"status": "in_progress"}, nil, 200)

		var detail struct {
			ChecklistItems []struct {
				ID          string `json:"id"`
				GPSRequired bool   `json:"gps_required"`
			} `json:"checklist_items"`
		}
		c.do("GET", "/api/field/work-orders/"+termWOID, nil, &detail, 200)
		for _, it := range detail.ChecklistItems {
			body := map[string]any{"template_item_id": it.ID, "response_text": "device retrieved"}
			if it.GPSRequired {
				body["gps_lat"] = leadLat
				body["gps_lng"] = leadLng
			}
			c.do("POST", "/api/field/work-orders/"+termWOID+"/checklist", body, nil, 200)
		}

		var tb struct {
			ID string `json:"id"`
		}
		c.do("POST", "/api/field/work-orders/"+termWOID+"/bast",
			map[string]any{"sign_off_mode": "on_site"}, &tb, 200)
		c.do("POST", "/api/field/basts/"+tb.ID+"/verify",
			map[string]any{"decision": "approved"}, nil, 200)

		// === transition (5): termination BAST approved → DEACTIVATED ===
		final := readRadius(t, customerID)
		if final.Status != "deactivated" {
			t.Fatalf("post-deactivate status: want 'deactivated', got %q", final.Status)
		}
		// Whole audit trail should still be present.
		if final.TempActivatedAt == nil ||
			final.PermActivatedAt == nil ||
			final.SuspendedAt == nil {
			t.Fatalf("audit timestamps lost on Deactivate: %+v", final)
		}
	})

	// =====================================================================
	// Cleanup — restore the policy defaults so a subsequent run doesn't
	// inherit the tightened thresholds.
	// =====================================================================

	t.Run("Z_restore_policy", func(t *testing.T) {
		c.do("PATCH", "/api/billing/policy", map[string]any{
			"late_fee_grace_days":            7,
			"late_fee_amount":                0,
			"suspend_after_days":             7,
			"terminate_after_suspended_days": 30,
			"notify_customer_days_before":    3,
		}, nil, 200)
	})
}
