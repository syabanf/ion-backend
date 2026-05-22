// Voluntary termination end-to-end.
//
// Builds on the broadband happy path's primitives: we re-run the
// branch + ODP + lead + convert + WO + payment + BAST + NOC-approve
// chain to land a paid customer, then exercise the full termination
// arc:
//
//	customer self-service portal OTP request
//	→ OTP confirm with reason
//	→ termination_request created (no balance, no lock-in: status=wo_created)
//	→ termination WO assigned + started
//	→ termination BAST submitted + NOC-approved
//	→ termination_request.status = completed
//	→ customer.status = terminated
//	→ network.radius_accounts.status = DEACTIVATED
//
// The two SQL shortcuts previous rounds carried (force-flip the
// customer to 'active' + provision a RADIUS account) are gone — the
// install-complete activation hook on VerifyBAST(approved) does both
// for us, and this test exercises that path end-to-end.
//
// Run with: make test-e2e (alongside the happy path).
//go:build e2e

package e2e

import (
	"context"
	"math/rand"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestVoluntaryTermination(t *testing.T) {
	c := newClient(t)
	c.login()

	sx := suffix()
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	odpLat := -6.0 - rng.Float64()*0.5
	odpLng := 106.5 + rng.Float64()*0.5
	leadLat := odpLat - 0.0001
	leadLng := odpLng + 0.0001

	// -------- Reusable harness state --------
	var (
		areaID, odpID, leadID                  string
		customerID, orderID                    string
		tlID, seniorID, juniorID, teamID, woID string
		bastID                                 string
	)

	// =====================================================================
	// Phase A — drive the happy path to "paid + BAST approved"
	// =====================================================================

	t.Run("A01_branches", func(t *testing.T) {
		var regional struct {
			ID string `json:"id"`
		}
		c.do("POST", "/api/identity/branches", map[string]any{
			"name":  "TERM Regional " + sx,
			"code":  "TERM-REG-" + sx,
			"level": "regional",
		}, &regional, 201)
		var area struct {
			ID string `json:"id"`
		}
		c.do("POST", "/api/identity/branches", map[string]any{
			"name":      "TERM Area " + sx,
			"code":      "TERM-AREA-" + sx,
			"level":     "area",
			"parent_id": regional.ID,
		}, &area, 201)
		areaID = area.ID
	})

	t.Run("A02_odp", func(t *testing.T) {
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
			"name":              "TERM ODP " + sx,
			"code":              "TERM-ODP-" + sx,
			"branch_id":         areaID,
			"address":           "Termination Test Address",
			"gps_lat":           odpLat,
			"gps_lng":           odpLng,
			"total_ports":       8,
			"port_role":         "customer_drop",
			"coverage_radius_m": 200,
		}, &node, 201)
		odpID = node.ID
		if odpID == "" {
			t.Fatal("odp id empty")
		}
	})

	t.Run("A03_lead", func(t *testing.T) {
		var products struct {
			Items []struct{ ID string } `json:"items"`
		}
		c.do("GET", "/api/crm/products?active_only=true", nil, &products, 200)
		if len(products.Items) == 0 {
			// Wave 47's master-data seed (migration 0047) guarantees
			// the BB-10/30/50/100 products exist. If we see 0 here,
			// either that migration regressed or the seed catalog was
			// manually emptied — both are real regressions, not skip
			// conditions.
			t.Fatal("no products available — Wave 47 master-data seed broken?")
		}
		var lead struct {
			ID        string `json:"id"`
			Documents []struct {
				ID       string `json:"id"`
				Required bool   `json:"required"`
			} `json:"documents"`
		}
		c.do("POST", "/api/crm/leads", map[string]any{
			"full_name":  "Termination Test " + sx,
			"phone":      "+62812" + sx + "00",
			"nik":        "31740" + sx + "1234",
			"address":    "Jl. Termination " + sx,
			"gps_lat":    leadLat,
			"gps_lng":    leadLng,
			"product_id": products.Items[0].ID,
		}, &lead, 201)
		leadID = lead.ID

		// Mark all required documents as submitted so the lead clears
		// the conversion gate. This isn't the system under test for the
		// termination scenario — we just need a paid customer.
		for _, d := range lead.Documents {
			if !d.Required {
				continue
			}
			c.do("PATCH", "/api/crm/documents/"+d.ID,
				map[string]any{"submitted": true}, nil, 200)
		}
	})

	t.Run("A04_convert", func(t *testing.T) {
		var conv struct {
			Customer struct {
				ID string `json:"id"`
			} `json:"customer"`
			Order struct {
				ID string `json:"id"`
			} `json:"order"`
		}
		c.do("POST", "/api/crm/leads/"+leadID+"/convert", map[string]any{}, &conv, 200)
		customerID = conv.Customer.ID
		orderID = conv.Order.ID
	})

	t.Run("A05_field_setup", func(t *testing.T) {
		mk := func(empID, name, email string, roles []string, grade *string) string {
			body := map[string]any{
				"employee_id": empID, "full_name": name, "email": email,
				"phone":     "+62811TERM" + empID,
				"password":  "Pass1234!" + sx,
				"roles":     roles,
				"branch_id": areaID,
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
		tlID = mk("TLT"+sx, "TL Term "+sx, "tlterm"+sx+"@ion.local",
			[]string{"team_leader"}, nil)
		senior := "senior"
		junior := "junior"
		seniorID = mk("SRT"+sx, "Senior Term "+sx, "srterm"+sx+"@ion.local",
			[]string{"technician"}, &senior)
		juniorID = mk("JRT"+sx, "Junior Term "+sx, "jrterm"+sx+"@ion.local",
			[]string{"technician"}, &junior)

		var team struct {
			ID string `json:"id"`
		}
		c.do("POST", "/api/field/teams", map[string]any{
			"code": "TEAM-TERM-" + sx, "name": "Team Term " + sx,
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
		c.do("POST", "/api/field/work-orders", map[string]any{
			"order_id": orderID,
		}, &wo, 201)
		woID = wo.ID
	})

	t.Run("A06_run_wo", func(t *testing.T) {
		c.do("POST", "/api/field/work-orders/"+woID+"/route",
			map[string]any{"team_id": teamID}, nil, 200)
		c.do("POST", "/api/field/work-orders/"+woID+"/assign", map[string]any{
			"lead_id": seniorID, "lead_grade": "senior",
			"observer_id": juniorID, "observer_grade": "junior",
		}, nil, 200)
		c.do("POST", "/api/field/work-orders/"+woID+"/status",
			map[string]any{"status": "in_progress"}, nil, 200)

		// Fill the checklist so BAST can submit.
		var detail struct {
			ChecklistItems []struct {
				ID          string `json:"id"`
				GPSRequired bool   `json:"gps_required"`
			} `json:"checklist_items"`
		}
		c.do("GET", "/api/field/work-orders/"+woID, nil, &detail, 200)
		for _, it := range detail.ChecklistItems {
			body := map[string]any{
				"template_item_id": it.ID,
				"response_text":    "ok",
			}
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
	})

	t.Run("A07_pay_and_approve", func(t *testing.T) {
		var invs struct {
			Items []struct {
				ID    string  `json:"id"`
				Total float64 `json:"total"`
			} `json:"items"`
		}
		c.do("GET", "/api/billing/invoices?order_id="+orderID, nil, &invs, 200)
		if len(invs.Items) == 0 {
			t.Fatal("no auto-OTC invoice")
		}
		c.do("POST", "/api/billing/invoices/"+invs.Items[0].ID+"/payments", map[string]any{
			"amount":         invs.Items[0].Total,
			"payment_method": "manual_bank_transfer",
			"notes":          "termination e2e",
		}, nil, 200)
		// NOC approve → install WO completes.
		c.do("POST", "/api/field/basts/"+bastID+"/verify", map[string]any{
			"decision": "approved",
		}, nil, 200)
	})

	// =====================================================================
	// Phase B — sanity check the install activation hook fired.
	//
	// VerifyBAST(approved) in Phase A's last step should have flipped
	// the customer to 'active' and driven the RADIUS account to
	// PERMANENT_ACTIVE. We open a DB pool here mainly for Phase D's
	// terminal RADIUS check, but we also assert the hook landed cleanly
	// before we proceed — otherwise the termination path would silently
	// no-op the RADIUS DEACTIVATE.
	// =====================================================================

	dbURL := envOr("DATABASE_URL", "postgres://syabanf@localhost:5432/ion_core?sslmode=disable")
	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		t.Fatalf("pgx pool: %v", err)
	}
	defer pool.Close()

	t.Run("B01_install_activation_landed", func(t *testing.T) {
		var cu struct {
			Status string `json:"status"`
		}
		c.do("GET", "/api/crm/customers/"+customerID, nil, &cu, 200)
		if cu.Status != "active" {
			t.Fatalf("install activation hook didn't flip customer; status=%q", cu.Status)
		}
		var radStatus string
		if err := pool.QueryRow(context.Background(),
			`SELECT status FROM network.radius_accounts WHERE customer_id = $1`,
			uuid.MustParse(customerID),
		).Scan(&radStatus); err != nil {
			t.Fatalf("read radius account: %v", err)
		}
		if strings.ToLower(radStatus) != "permanent_active" {
			t.Fatalf("install activation hook didn't promote RADIUS; status=%q", radStatus)
		}
	})

	// =====================================================================
	// Phase C — customer self-service termination via portal OTP
	// =====================================================================

	var terminationID, termWoID string

	t.Run("C01_portal_request_otp", func(t *testing.T) {
		// Look up the customer_number + phone we just created.
		ctx := context.Background()
		var customerNumber, phone string
		row := pool.QueryRow(ctx,
			`SELECT customer_number, phone FROM crm.customers WHERE id=$1`,
			uuid.MustParse(customerID))
		if err := row.Scan(&customerNumber, &phone); err != nil {
			t.Fatalf("read customer: %v", err)
		}
		// The portal endpoints are public — do() includes the token,
		// which is harmless on an unauthenticated route.
		var out struct {
			ExpiresAt string `json:"expires_at"`
			DevOTP    string `json:"dev_otp"`
		}
		c.do("POST", "/api/billing/portal/termination/request", map[string]any{
			"customer_number": customerNumber,
			"phone":           phone,
		}, &out, 202)
		if out.DevOTP == "" {
			// PORTAL_DEV_OTP=true is exported by the pr-e2e CI job
			// (Wave 48). An empty dev_otp here means the billing-svc
			// is wired differently than CI expects, which is a real
			// regression — not a skip condition.
			t.Fatal("portal termination request returned no dev_otp — PORTAL_DEV_OTP missing on billing-svc?")
		}

		var confirm struct {
			TerminationID string `json:"termination_id"`
			Status        string `json:"status"`
		}
		c.do("POST", "/api/billing/portal/termination/confirm", map[string]any{
			"customer_number": customerNumber,
			"otp":             out.DevOTP,
			"reason":          "e2e moving overseas",
		}, &confirm, 201)
		terminationID = confirm.TerminationID
		if !strings.Contains(confirm.Status, "wo_") {
			t.Fatalf("expected status wo_pending or wo_created, got %q", confirm.Status)
		}
	})

	t.Run("C02_verify_termination_request", func(t *testing.T) {
		if terminationID == "" {
			// C01 fatals when the portal confirm step doesn't return
			// a terminationID, so this branch should be unreachable on
			// a healthy stack. Keep the guard but make it loud.
			t.Fatal("terminationID empty — C01 should have set it (or fatalled)")
		}
		var got struct {
			ID     string `json:"id"`
			Kind   string `json:"kind"`
			Status string `json:"status"`
			WOID   string `json:"wo_id"`
			Reason string `json:"reason"`
		}
		c.do("GET", "/api/billing/terminations/"+terminationID, nil, &got, 200)
		if got.Kind != "voluntary" {
			t.Fatalf("kind: want voluntary, got %q", got.Kind)
		}
		if got.WOID == "" {
			t.Fatalf("expected wo_id to be set; status=%s", got.Status)
		}
		if !strings.Contains(got.Reason, "self-service") {
			t.Fatalf("expected reason to mention self-service, got %q", got.Reason)
		}
		termWoID = got.WOID
	})

	// =====================================================================
	// Phase D — drive the termination WO through to NOC approval
	// =====================================================================

	t.Run("D01_termination_wo_run", func(t *testing.T) {
		if termWoID == "" {
			// C02 fatals when wo_id is empty, so this is unreachable on
			// a healthy stack. Loud guard.
			t.Fatal("termWoID empty — C02 should have populated it (or fatalled)")
		}
		c.do("POST", "/api/field/work-orders/"+termWoID+"/route",
			map[string]any{"team_id": teamID}, nil, 200)
		c.do("POST", "/api/field/work-orders/"+termWoID+"/assign", map[string]any{
			"lead_id": seniorID, "lead_grade": "senior",
			"observer_id": juniorID, "observer_grade": "junior",
		}, nil, 200)
		c.do("POST", "/api/field/work-orders/"+termWoID+"/status",
			map[string]any{"status": "in_progress"}, nil, 200)

		// Termination has its own checklist template (termination ×
		// broadband). Burn through it the same way.
		var detail struct {
			ChecklistItems []struct {
				ID          string `json:"id"`
				GPSRequired bool   `json:"gps_required"`
			} `json:"checklist_items"`
		}
		c.do("GET", "/api/field/work-orders/"+termWoID, nil, &detail, 200)
		for _, it := range detail.ChecklistItems {
			body := map[string]any{
				"template_item_id": it.ID,
				"response_text":    "device retrieved",
			}
			if it.GPSRequired {
				body["gps_lat"] = leadLat
				body["gps_lng"] = leadLng
			}
			c.do("POST", "/api/field/work-orders/"+termWoID+"/checklist", body, nil, 200)
		}
		var b struct {
			ID string `json:"id"`
		}
		c.do("POST", "/api/field/work-orders/"+termWoID+"/bast",
			map[string]any{"sign_off_mode": "on_site"}, &b, 200)

		// NOC approve. The hook on the field-svc VerifyBAST path fires
		// OnTerminationWOCompleted → billing flips request to completed,
		// customer → terminated, RADIUS → DEACTIVATED.
		c.do("POST", "/api/field/basts/"+b.ID+"/verify", map[string]any{
			"decision": "approved",
		}, nil, 200)
	})

	t.Run("D02_verify_terminal_state", func(t *testing.T) {
		if termWoID == "" {
			// Reachable only if every earlier guard somehow returned
			// rather than fatalled — defence in depth.
			t.Fatal("termWoID empty in D02 — earlier guard missed")
		}

		// Termination request → completed.
		var tr struct {
			Status string `json:"status"`
		}
		c.do("GET", "/api/billing/terminations/"+terminationID, nil, &tr, 200)
		if tr.Status != "completed" {
			t.Fatalf("termination status: want completed, got %q", tr.Status)
		}

		// Customer → terminated.
		var cu struct {
			Status string `json:"status"`
		}
		c.do("GET", "/api/crm/customers/"+customerID, nil, &cu, 200)
		if cu.Status != "terminated" {
			t.Fatalf("customer status: want terminated, got %q", cu.Status)
		}

		// RADIUS account → DEACTIVATED (read straight from the DB —
		// there's no public read endpoint for radius_accounts).
		ctx := context.Background()
		var radStatus string
		err := pool.QueryRow(ctx,
			`SELECT status FROM network.radius_accounts WHERE customer_id=$1`,
			uuid.MustParse(customerID),
		).Scan(&radStatus)
		if err != nil {
			t.Fatalf("read radius account: %v", err)
		}
		// LocalRadiusClient.Deactivate writes status='deactivated' (lowercase).
		if strings.ToLower(radStatus) != "deactivated" {
			t.Fatalf("radius status: want deactivated, got %q", radStatus)
		}
	})

	_ = os.Getenv // keep stdlib import while harness evolves
}
