// Package e2e is the black-box integration test for the Phase 1 broadband
// happy path. It walks the entire customer journey against a live stack:
//
//	identity-svc + network-svc + warehouse-svc + crm-svc + field-svc +
//	billing-svc + api-gateway
//
// All HTTP calls go through the gateway on http://localhost:8080. The test
// is meant to be re-runnable on the same database (it uses random suffixes
// for codes/emails/numbers) and bails out cleanly if the stack isn't up.
//
// Run with:
//
//	make test-e2e
//
// or directly (assumes services already running + admin seeded):
//
//	cd backend && go test ./test/e2e -v -tags=e2e
//
// The build tag keeps this out of the default `go test ./...` run so unit
// tests stay independent of a live stack.
//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// =====================================================================
// Harness
// =====================================================================

const baseURL = "http://localhost:8080"

// client wraps http.Client with a bearer token + helpers that decode JSON
// and surface server-side error envelopes as test failures.
type client struct {
	t     *testing.T
	http  *http.Client
	token string
}

func newClient(t *testing.T) *client {
	t.Helper()
	return &client{t: t, http: &http.Client{Timeout: 15 * time.Second}}
}

// login authenticates as the seeded super-admin. We read SEED_ADMIN_* env
// vars to match the dev .env; defaults match the bootstrap docs.
func (c *client) login() {
	c.t.Helper()
	email := envOr("SEED_ADMIN_EMAIL", "admin@ion.local")
	pwd := envOr("SEED_ADMIN_PASSWORD", "IonAdmin#2026!ChangeMe")
	body := map[string]string{"email": email, "password": pwd}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	c.do("POST", "/api/identity/auth/login", body, &out, 200)
	if out.AccessToken == "" {
		c.t.Fatalf("login returned empty access_token (is identity-svc seeded?)")
	}
	c.token = out.AccessToken
}

// do executes an HTTP call with optional JSON body + JSON-decoded result.
// If wantStatus != 0, fails the test on mismatch and shows the error body.
func (c *client) do(method, path string, in, out any, wantStatus int) {
	c.t.Helper()
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			c.t.Fatalf("marshal body: %v", err)
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, baseURL+path, body)
	if err != nil {
		c.t.Fatalf("new request %s %s: %v", method, path, err)
	}
	if c.token != "" {
		req.Header.Set("authorization", "Bearer "+c.token)
	}
	if in != nil {
		req.Header.Set("content-type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatalf("request %s %s: %v (is the stack up on :8080?)", method, path, err)
	}
	defer resp.Body.Close()
	buf, _ := io.ReadAll(resp.Body)
	if wantStatus != 0 && resp.StatusCode != wantStatus {
		c.t.Fatalf("%s %s: want status %d, got %d — body: %s",
			method, path, wantStatus, resp.StatusCode, string(buf))
	}
	if out != nil && len(buf) > 0 {
		if err := json.Unmarshal(buf, out); err != nil {
			c.t.Fatalf("decode %s %s: %v — body: %s", method, path, err, string(buf))
		}
	}
}

// doExpectError calls an endpoint that should fail. Returns the error code
// surfaced by the platform error envelope so the test can assert on it.
func (c *client) doExpectError(method, path string, in any, wantStatus int) string {
	c.t.Helper()
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, baseURL+path, body)
	if c.token != "" {
		req.Header.Set("authorization", "Bearer "+c.token)
	}
	if in != nil {
		req.Header.Set("content-type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatalf("request %s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		body, _ := io.ReadAll(resp.Body)
		c.t.Fatalf("%s %s: want status %d, got %d — body: %s",
			method, path, wantStatus, resp.StatusCode, string(body))
	}
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Kind    string `json:"kind"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&env)
	return env.Error.Code
}

// suffix returns a short unique string for uniqueness on re-runs.
func suffix() string { return fmt.Sprintf("%d", time.Now().UnixNano()%1_000_000) }

func envOr(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

// =====================================================================
// The test
// =====================================================================

// TestBroadbandHappyPath walks the whole Phase-1 broadband path end-to-end.
// Each subtest is one logical step; failing fast at any step bails the test
// (the next step depends on prior state).
func TestBroadbandHappyPath(t *testing.T) {
	c := newClient(t)
	c.login()

	sx := suffix() // unique-per-run tag

	// Jitter the GPS by a few meters per run so the lead's nearest ODP is
	// the one we just created here, not a leftover from a prior run. Use
	// a per-run rand so consecutive runs sit in non-overlapping coverage
	// neighborhoods. We don't seed deterministically — the goal is just
	// to not collide with stale data; reproducibility isn't a goal.
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	odpLat := -6.0 - rng.Float64()*0.5      // -6.5 .. -6.0
	odpLng := 106.5 + rng.Float64()*0.5     // 106.5 .. 107.0
	leadLat := odpLat - 0.0001              // ~11m south
	leadLng := odpLng + 0.0001

	// -----------------------------------------------------------------
	// 1. Branches — create regional + area if our test branch doesn't exist
	// -----------------------------------------------------------------
	var areaID string
	t.Run("01_branch_setup", func(t *testing.T) {
		var regional struct {
			ID string `json:"id"`
		}
		c.do("POST", "/api/identity/branches", map[string]any{
			"name":  "E2E Regional " + sx,
			"code":  "E2E-REG-" + sx,
			"level": "regional",
		}, &regional, 201)

		var area struct {
			ID string `json:"id"`
		}
		c.do("POST", "/api/identity/branches", map[string]any{
			"name":      "E2E Area " + sx,
			"code":      "E2E-AREA-" + sx,
			"level":     "area",
			"parent_id": regional.ID,
		}, &area, 201)
		areaID = area.ID
		if areaID == "" {
			t.Fatal("area id empty")
		}
	})

	// -----------------------------------------------------------------
	// 2. ODP — needed so coverage_check returns 'covered' for our lead's GPS
	// -----------------------------------------------------------------
	var odpID string
	t.Run("02_odp_for_coverage", func(t *testing.T) {
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
		if odpTypeID == "" {
			t.Fatal("no odp node type seeded")
		}
		var node struct {
			ID string `json:"id"`
		}
		c.do("POST", "/api/network/nodes", map[string]any{
			"node_type_id": odpTypeID,
			"name":         "E2E ODP " + sx,
			"code":         "E2E-ODP-" + sx,
			"branch_id":    areaID,
			"address":      "E2E Test Address",
			"gps_lat":      odpLat,
			"gps_lng":      odpLng,
			"total_ports":  8,
			"port_role":    "customer_drop",
		}, &node, 201)
		odpID = node.ID
		_ = odpID // not referenced again, but useful when this fails
	})

	// -----------------------------------------------------------------
	// 3. CRM lead — GPS near ODP; coverage should auto-stamp 'qualified'
	// -----------------------------------------------------------------
	var leadID, productID string
	t.Run("03_lead_with_coverage", func(t *testing.T) {
		var products struct {
			Items []struct {
				ID   string `json:"id"`
				Code string `json:"code"`
			} `json:"items"`
		}
		c.do("GET", "/api/crm/products", nil, &products, 200)
		for _, p := range products.Items {
			if p.Code == "BB-30" {
				productID = p.ID
			}
		}
		if productID == "" {
			t.Fatal("seeded BB-30 product not found")
		}

		var lead struct {
			ID              string `json:"id"`
			Status          string `json:"status"`
			CoverageVerdict string `json:"coverage_verdict"`
			Documents       []struct {
				ID       string `json:"id"`
				Required bool   `json:"required"`
			} `json:"documents"`
		}
		c.do("POST", "/api/crm/leads", map[string]any{
			"full_name":  "E2E Customer " + sx,
			"phone":      "+62811" + sx,
			"nik":        "31740" + sx + "0000",
			"address":    "Jl. E2E Test " + sx,
			"gps_lat":    leadLat,
			"gps_lng":    leadLng,
			"product_id": productID,
		}, &lead, 201)
		leadID = lead.ID
		if lead.Status != "qualified" {
			t.Fatalf("expected status qualified, got %q (verdict=%s)", lead.Status, lead.CoverageVerdict)
		}
		if lead.CoverageVerdict != "covered" {
			t.Fatalf("expected coverage covered, got %q", lead.CoverageVerdict)
		}

		// -----------------------------------------------------------------
		// 4. Submit all required documents
		// -----------------------------------------------------------------
		for _, d := range lead.Documents {
			if !d.Required {
				continue
			}
			c.do("PATCH", "/api/crm/documents/"+d.ID, map[string]any{
				"submitted": true,
			}, nil, 200)
		}
	})

	// -----------------------------------------------------------------
	// 5. Convert lead — this auto-creates the OTC invoice via the CRM→billing gateway
	// -----------------------------------------------------------------
	var orderID, customerID string
	t.Run("05_convert_lead_auto_otc", func(t *testing.T) {
		var conv struct {
			Customer struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"customer"`
			Order struct {
				ID           string  `json:"id"`
				Status       string  `json:"status"`
				MonthlyPrice float64 `json:"monthly_price"`
			} `json:"order"`
		}
		c.do("POST", "/api/crm/leads/"+leadID+"/convert", map[string]any{}, &conv, 200)
		orderID = conv.Order.ID
		customerID = conv.Customer.ID
		if conv.Customer.Status != "pending_install" {
			t.Fatalf("customer status: want pending_install, got %q", conv.Customer.Status)
		}
		if conv.Order.Status != "created" {
			t.Fatalf("order status: want created, got %q", conv.Order.Status)
		}

		// Auto-OTC should be there.
		var invs struct {
			Items []struct {
				ID            string  `json:"id"`
				InvoiceType   string  `json:"invoice_type"`
				Status        string  `json:"status"`
				Total         float64 `json:"total"`
			} `json:"items"`
		}
		c.do("GET", "/api/billing/invoices?order_id="+orderID, nil, &invs, 200)
		if len(invs.Items) == 0 {
			t.Fatal("no OTC invoice auto-created on convert")
		}
		got := invs.Items[0]
		if got.InvoiceType != "otc" {
			t.Fatalf("invoice type: want otc, got %q", got.InvoiceType)
		}
		if got.Status != "issued" {
			t.Fatalf("invoice status: want issued, got %q", got.Status)
		}
		if got.Total <= 0 {
			t.Fatalf("invoice total <= 0: %v", got.Total)
		}
	})

	// -----------------------------------------------------------------
	// 6. Field setup — team leader + 2 techs + team + WO
	// -----------------------------------------------------------------
	var tlID, seniorID, juniorID, teamID, woID string
	t.Run("06_field_setup_team_and_wo", func(t *testing.T) {
		mk := func(empID, name, email string, roles []string, grade *string, reports *string) string {
			body := map[string]any{
				"employee_id": empID,
				"full_name":   name,
				"email":       email,
				"phone":       "+62811" + sx + empID,
				"password":    "Pass1234!" + sx,
				"branch_id":   areaID,
				"roles":       roles,
			}
			if grade != nil {
				body["technician_grade"] = *grade
			}
			if reports != nil {
				body["reports_to_id"] = *reports
			}
			var u struct {
				ID string `json:"id"`
			}
			c.do("POST", "/api/identity/users", body, &u, 201)
			return u.ID
		}
		tlID = mk("TL"+sx, "E2E Leader "+sx, "tl"+sx+"@ion.local",
			[]string{"team_leader"}, nil, nil)
		seniorGrade := "senior"
		juniorGrade := "junior"
		seniorID = mk("SR"+sx, "E2E Senior "+sx, "sr"+sx+"@ion.local",
			[]string{"technician"}, &seniorGrade, &tlID)
		juniorID = mk("JR"+sx, "E2E Junior "+sx, "jr"+sx+"@ion.local",
			[]string{"technician"}, &juniorGrade, &tlID)

		var team struct {
			ID string `json:"id"`
		}
		c.do("POST", "/api/field/teams", map[string]any{
			"code":           "E2E-TM-" + sx,
			"name":           "E2E Team " + sx,
			"branch_id":      areaID,
			"team_leader_id": tlID,
		}, &team, 201)
		teamID = team.ID
		c.do("POST", "/api/field/teams/"+teamID+"/members", map[string]any{
			"user_id": seniorID, "grade": "senior",
		}, nil, 201)
		c.do("POST", "/api/field/teams/"+teamID+"/members", map[string]any{
			"user_id": juniorID, "grade": "junior",
		}, nil, 201)

		var wo struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		}
		c.do("POST", "/api/field/work-orders", map[string]any{
			"order_id": orderID,
			"priority": "medium",
		}, &wo, 201)
		woID = wo.ID
		if wo.Status != "unassigned" {
			t.Fatalf("wo status: want unassigned, got %q", wo.Status)
		}
	})

	// -----------------------------------------------------------------
	// 7. Route → assign → in_progress
	// -----------------------------------------------------------------
	t.Run("07_route_assign_start", func(t *testing.T) {
		c.do("POST", "/api/field/work-orders/"+woID+"/route", map[string]any{
			"team_id": teamID,
		}, nil, 200)
		var assigned struct {
			Status string `json:"status"`
		}
		c.do("POST", "/api/field/work-orders/"+woID+"/assign", map[string]any{
			"lead_id":        seniorID,
			"lead_grade":     "senior",
			"observer_id":    juniorID,
			"observer_grade": "junior",
		}, &assigned, 200)
		if assigned.Status != "assigned" {
			t.Fatalf("after assign want assigned, got %q", assigned.Status)
		}
		c.do("POST", "/api/field/work-orders/"+woID+"/status", map[string]any{
			"status": "in_progress",
		}, nil, 200)
	})

	// -----------------------------------------------------------------
	// 8. Submit all checklist responses
	// -----------------------------------------------------------------
	t.Run("08_checklist_responses", func(t *testing.T) {
		var wo struct {
			ChecklistItems []struct {
				ID          string `json:"id"`
				Label       string `json:"label"`
				ItemType    string `json:"item_type"`
				GPSRequired bool   `json:"gps_required"`
			} `json:"checklist_items"`
		}
		c.do("GET", "/api/field/work-orders/"+woID, nil, &wo, 200)
		if len(wo.ChecklistItems) == 0 {
			t.Fatal("no checklist items — template loader broken")
		}
		for i, it := range wo.ChecklistItems {
			body := map[string]any{
				"template_item_id": it.ID,
				"response_text":    fmt.Sprintf("item %d done (%s)", i+1, it.Label),
			}
			// M5 r3 — gps_required items need GPS on the response (or
			// an upload's GPS, which we don't simulate here). The
			// service accepts inline GPS when no file is attached.
			if it.GPSRequired {
				body["gps_lat"] = leadLat
				body["gps_lng"] = leadLng
			}
			c.do("POST", "/api/field/work-orders/"+woID+"/checklist", body, nil, 200)
		}
	})

	// -----------------------------------------------------------------
	// 9. Submit BAST → pending NOC
	// -----------------------------------------------------------------
	var bastID string
	t.Run("09_submit_bast", func(t *testing.T) {
		var b struct {
			ID        string `json:"id"`
			NOCStatus string `json:"noc_status"`
		}
		c.do("POST", "/api/field/work-orders/"+woID+"/bast", map[string]any{
			"sign_off_mode": "on_site",
		}, &b, 200)
		bastID = b.ID
		if b.NOCStatus != "pending" {
			t.Fatalf("BAST noc_status: want pending, got %q", b.NOCStatus)
		}
	})

	// -----------------------------------------------------------------
	// 10. NOC tries to approve — the payment gate MUST block
	// -----------------------------------------------------------------
	t.Run("10_payment_gate_blocks_noc", func(t *testing.T) {
		code := c.doExpectError("POST", "/api/field/basts/"+bastID+"/verify",
			map[string]any{"decision": "approved", "notes": "ready"}, 409)
		if code != "bast.payment_gate" {
			t.Fatalf("want error code bast.payment_gate, got %q", code)
		}
	})

	// -----------------------------------------------------------------
	// 11. Record full payment → invoice flips to paid
	// -----------------------------------------------------------------
	t.Run("11_record_payment", func(t *testing.T) {
		var invs struct {
			Items []struct {
				ID    string  `json:"id"`
				Total float64 `json:"total"`
			} `json:"items"`
		}
		c.do("GET", "/api/billing/invoices?order_id="+orderID, nil, &invs, 200)
		if len(invs.Items) == 0 {
			t.Fatal("OTC missing during payment step — should still exist")
		}
		inv := invs.Items[0]
		var after struct {
			Status            string  `json:"status"`
			PaidAmount        float64 `json:"paid_amount"`
			OutstandingAmount float64 `json:"outstanding_amount"`
		}
		c.do("POST", "/api/billing/invoices/"+inv.ID+"/payments", map[string]any{
			"amount":         inv.Total,
			"payment_method": "bank_transfer",
			"notes":          "E2E bulk pay",
		}, &after, 200)
		if after.Status != "paid" {
			t.Fatalf("after payment want status paid, got %q", after.Status)
		}
		if after.OutstandingAmount != 0 {
			t.Fatalf("outstanding should be 0, got %v", after.OutstandingAmount)
		}

		// And the cross-context probe should now say true.
		var probe struct {
			OTCPaid bool `json:"otc_paid"`
		}
		c.do("GET", "/api/billing/orders/"+orderID+"/otc-status", nil, &probe, 200)
		if !probe.OTCPaid {
			t.Fatal("/orders/{id}/otc-status still says false after payment")
		}
	})

	// -----------------------------------------------------------------
	// 12. NOC approves — should succeed; WO → completed
	// -----------------------------------------------------------------
	t.Run("12_noc_approves", func(t *testing.T) {
		var b struct {
			NOCStatus string `json:"noc_status"`
		}
		c.do("POST", "/api/field/basts/"+bastID+"/verify", map[string]any{
			"decision": "approved",
			"notes":    "E2E approved after payment",
		}, &b, 200)
		if b.NOCStatus != "approved" {
			t.Fatalf("want noc_status approved, got %q", b.NOCStatus)
		}
		var wo struct {
			Status string `json:"status"`
		}
		c.do("GET", "/api/field/work-orders/"+woID, nil, &wo, 200)
		if wo.Status != "completed" {
			t.Fatalf("want WO status completed, got %q", wo.Status)
		}
	})

	// NOC approval fires the activation hook: customer flips to 'active'
	// and the RADIUS account moves from absent → TEMPORARY → PERMANENT.
	t.Run("13_customer_active_and_radius_permanent", func(t *testing.T) {
		var cu struct {
			ID       string `json:"id"`
			Status   string `json:"status"`
			BranchID string `json:"branch_id"`
		}
		c.do("GET", "/api/crm/customers/"+customerID, nil, &cu, 200)
		if cu.ID == "" {
			t.Fatal("customer disappeared")
		}
		if cu.BranchID != areaID {
			t.Fatalf("customer branch want %s got %s", areaID, cu.BranchID)
		}
		if cu.Status != "active" {
			t.Fatalf("customer status: want active (activation hook should fire on NOC approve), got %q", cu.Status)
		}

		// RADIUS account state isn't exposed via HTTP — check it
		// directly. The activation hook calls Provision (idempotent) →
		// PromoteToPermanent, so we expect a single row at
		// 'permanent_active'.
		dbURL := envOr("DATABASE_URL", "postgres://syabanf@localhost:5432/ion_core?sslmode=disable")
		pool, err := pgxpool.New(context.Background(), dbURL)
		if err != nil {
			t.Fatalf("pgx pool: %v", err)
		}
		defer pool.Close()
		var radStatus, radUsername string
		if err := pool.QueryRow(context.Background(),
			`SELECT status, username FROM network.radius_accounts WHERE customer_id = $1`,
			uuid.MustParse(customerID),
		).Scan(&radStatus, &radUsername); err != nil {
			t.Fatalf("read radius account: %v", err)
		}
		if strings.ToLower(radStatus) != "permanent_active" {
			t.Fatalf("radius status: want permanent_active, got %q", radStatus)
		}
		if radUsername == "" {
			t.Fatal("radius username unexpectedly empty")
		}
	})
}
