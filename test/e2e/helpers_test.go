// Shared setup helpers for the e2e suite.
//
// Wave 50 — the broadband_e2e_test inlines 150+ lines of branch/ODP/
// product/lead/convert setup before the first interesting assertion. As
// new flow-specific tests landed (invoice+payment, plan-change, ticket
// lifecycle, etc.) that boilerplate was being duplicated.
//
// This file factors the common "I need a converted broadband customer"
// setup into one helper so the new tests stay short and focused on the
// flow they're proving.
//
// IMPORTANT: existing tests (broadband_e2e_test.go, voluntary_termination,
// etc.) intentionally keep their inline setup. They were authored as
// isolated walkthroughs; refactoring them in the same wave that adds new
// coverage muddies the blast radius.
//
//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"math/rand"
	"net/http"
	"testing"
	"time"
)

// mustJSON returns the JSON encoding of v as an io.Reader suitable for
// http.Post. We panic on encode failure because the input is always a
// literal map[string]any built by the test — a marshal error is a
// programming bug, not a runtime condition we should recover from.
func mustJSON(v any) io.Reader {
	b, err := json.Marshal(v)
	if err != nil {
		panic("mustJSON: " + err.Error())
	}
	return bytes.NewReader(b)
}

// newRequest builds an *http.Request with optional JSON body. Caller
// is responsible for adding Authorization etc. Returns the request and
// the body io.Reader so the test can wrap or inspect it if needed.
// We use this in the RBAC matrix where we want full control over the
// response — `c.do` auto-fails on non-2xx which is the opposite of
// what a 403-expecting probe wants.
func newRequest(t *testing.T, method, url string, body any) (*http.Request, io.Reader) {
	t.Helper()
	var r io.Reader
	if body != nil {
		r = mustJSON(body)
	}
	req, err := http.NewRequest(method, url, r)
	if err != nil {
		t.Fatalf("newRequest %s %s: %v", method, url, err)
	}
	if body != nil {
		req.Header.Set("content-type", "application/json")
	}
	return req, r
}

// statusOnly fires an HTTP request and returns just the status code.
// Useful for negative authz tests where multiple statuses are
// acceptable (e.g. 403 OR 404 both mean "blocked") and doExpectError's
// exact-match semantics get in the way.
func (c *client) statusOnly(method, path string) int {
	return c.statusOnlyJSON(method, path, nil)
}

// statusOnlyJSON is statusOnly with a JSON body attached.
func (c *client) statusOnlyJSON(method, path string, body any) int {
	c.t.Helper()
	var r io.Reader
	if body != nil {
		r = mustJSON(body)
	}
	req, err := http.NewRequest(method, baseURL+path, r)
	if err != nil {
		c.t.Fatalf("statusOnly newRequest %s %s: %v", method, path, err)
	}
	if c.token != "" {
		req.Header.Set("authorization", "Bearer "+c.token)
	}
	if body != nil {
		req.Header.Set("content-type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatalf("statusOnly request %s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// customerHandle is the bundle returned by setupCoveredCustomer. Every
// downstream flow test we ship in Wave 50 needs at least the customer
// + their portal credentials.
type customerHandle struct {
	BranchID       string
	ODPID          string
	ProductID      string
	LeadID         string
	CustomerID     string
	CustomerNumber string
	Phone          string // raw "+62812…" — last 4 used for portal OTP
	OrderID        string
}

// setupCoveredCustomer walks the canonical lead-→-customer flow on a
// freshly-built branch/ODP. Returns the bundle of IDs that follow-on
// tests need. The admin client is the one passed in, so callers can
// share an authenticated session across multiple setups.
//
// Why not a fixture: we'd need to clean up after each test run, which
// is cheap on a fresh CI DB but flaky on a re-used dev DB. The inline
// "build it from scratch with unique suffixes" pattern dodges that
// entirely — every customer is its own world.
//
// Cost is ~6 HTTP calls + 1 PATCH per required doc + 1 convert. About
// 200-400ms on a warm stack. Acceptable for the dozen tests that need
// a converted customer; not acceptable as a per-subtest cost.
func setupCoveredCustomer(t *testing.T, admin *client) customerHandle {
	t.Helper()
	sx := suffix()
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	odpLat := -6.0 - rng.Float64()*0.5
	odpLng := 106.5 + rng.Float64()*0.5
	leadLat := odpLat - 0.0001
	leadLng := odpLng + 0.0001

	// 1. Branch hierarchy
	var regional struct{ ID string `json:"id"` }
	admin.do("POST", "/api/identity/branches", map[string]any{
		"name":  "W50 Regional " + sx,
		"code":  "W50-REG-" + sx,
		"level": "regional",
	}, &regional, 201)
	var area struct{ ID string `json:"id"` }
	admin.do("POST", "/api/identity/branches", map[string]any{
		"name":      "W50 Area " + sx,
		"code":      "W50-AREA-" + sx,
		"level":     "area",
		"parent_id": regional.ID,
	}, &area, 201)

	// 2. ODP that the lead's GPS will fall under
	var types struct {
		Items []struct {
			ID      string `json:"id"`
			TypeKey string `json:"type_key"`
		} `json:"items"`
	}
	admin.do("GET", "/api/network/node-types", nil, &types, 200)
	var odpTypeID string
	for _, nt := range types.Items {
		if nt.TypeKey == "odp" {
			odpTypeID = nt.ID
		}
	}
	if odpTypeID == "" {
		t.Fatal("setupCoveredCustomer: no odp node-type seeded")
	}
	var node struct{ ID string `json:"id"` }
	admin.do("POST", "/api/network/nodes", map[string]any{
		"node_type_id": odpTypeID,
		"name":         "W50 ODP " + sx,
		"code":         "W50-ODP-" + sx,
		"branch_id":    area.ID,
		"address":      "W50 setup",
		"gps_lat":      odpLat,
		"gps_lng":      odpLng,
		"total_ports":  8,
		"port_role":    "customer_drop",
	}, &node, 201)

	// 3. Find a seeded broadband product (BB-30 is the canonical mid-tier from Wave 47)
	var products struct {
		Items []struct {
			ID   string `json:"id"`
			Code string `json:"code"`
		} `json:"items"`
	}
	admin.do("GET", "/api/crm/products", nil, &products, 200)
	var productID string
	for _, p := range products.Items {
		if p.Code == "BB-30" {
			productID = p.ID
		}
	}
	if productID == "" {
		t.Fatal("setupCoveredCustomer: BB-30 product not seeded (Wave 47 migration missing?)")
	}

	// 4. Lead — GPS sits ~11m south of the ODP we just created
	phone := "+62812" + sx
	var lead struct {
		ID              string `json:"id"`
		Status          string `json:"status"`
		CoverageVerdict string `json:"coverage_verdict"`
		Documents       []struct {
			ID       string `json:"id"`
			Required bool   `json:"required"`
		} `json:"documents"`
	}
	admin.do("POST", "/api/crm/leads", map[string]any{
		"full_name":  "W50 Customer " + sx,
		"phone":      phone,
		"nik":        "31760" + sx + "0000",
		"address":    "Jl. W50 " + sx,
		"gps_lat":    leadLat,
		"gps_lng":    leadLng,
		"product_id": productID,
	}, &lead, 201)
	if lead.Status != "qualified" {
		t.Fatalf("setupCoveredCustomer: lead not qualified (got %q, verdict=%q)",
			lead.Status, lead.CoverageVerdict)
	}

	// 5. Submit required docs
	for _, d := range lead.Documents {
		if d.Required {
			admin.do("PATCH", "/api/crm/documents/"+d.ID, map[string]any{
				"submitted": true,
			}, nil, 200)
		}
	}

	// 6. Convert
	var conv struct {
		Customer struct {
			ID             string `json:"id"`
			CustomerNumber string `json:"customer_number"`
			Phone          string `json:"phone"`
		} `json:"customer"`
		Order struct {
			ID string `json:"id"`
		} `json:"order"`
	}
	admin.do("POST", "/api/crm/leads/"+lead.ID+"/convert", map[string]any{}, &conv, 200)
	if conv.Customer.CustomerNumber == "" {
		t.Fatal("setupCoveredCustomer: convert response missing customer_number")
	}

	custPhone := conv.Customer.Phone
	if custPhone == "" {
		custPhone = phone
	}
	return customerHandle{
		BranchID:       area.ID,
		ODPID:          node.ID,
		ProductID:      productID,
		LeadID:         lead.ID,
		CustomerID:     conv.Customer.ID,
		CustomerNumber: conv.Customer.CustomerNumber,
		Phone:          custPhone,
		OrderID:        conv.Order.ID,
	}
}

// phoneLast4 extracts the last 4 chars of a phone string — what the
// portal OTP request matches against to confirm the customer's identity.
func phoneLast4(phone string) string {
	if len(phone) < 4 {
		return phone
	}
	return phone[len(phone)-4:]
}

// keysOf returns the keys of a string-keyed map. Used in error messages
// where the test wants to surface what the response DID contain when
// the expected key is missing — saves a debugging round-trip.
func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// woReadyForNOC bundles the IDs a NOC-verify test needs:
//   * CustomerID — for asserting status flips post-verification
//   * WOID + BASTID — for the verify call itself
//   * OrderID — for re-paying or re-querying invoices
//   * TechIDs — for any follow-up assignment / message asserts
type woReadyForNOC struct {
	CustomerID string
	OrderID    string
	WOID       string
	BASTID     string
	TeamID     string
	TLID       string
	SeniorID   string
	JuniorID   string
}

// setupWOReadyForNOC builds the full chain a NOC-verify test needs:
//
//   1. Covered customer with auto-OTC invoice (via setupCoveredCustomer)
//   2. team_leader + senior + junior + team
//   3. Install WO routed to the team + assigned with senior lead + junior observer
//   4. WO status → in_progress, all checklist items submitted
//   5. BAST submitted with sign_off_mode=on_site (noc_status='pending')
//   6. Full payment recorded against the OTC (clears the payment-gate
//      so NOC verify can proceed)
//
// After this returns, /api/field/basts/{BASTID}/verify is ready to be
// called with either `decision=approved` (happy path) or
// `decision=rejected` (negative path).
//
// Cost is ~25 HTTP calls. Reusable across the BAST-related tests
// (rejection, redo, payment-gate edge cases, etc.).
func setupWOReadyForNOC(t *testing.T, admin *client) woReadyForNOC {
	t.Helper()
	h := setupCoveredCustomer(t, admin)
	sx := suffix()

	// Team_leader + senior + junior — match broadband_e2e_test's pattern.
	mkUser := func(empPrefix, full, email, role string, grade string) string {
		body := map[string]any{
			"employee_id": empPrefix + "-" + sx,
			"full_name":   full,
			"email":       email,
			"phone":       "+62811BAST" + empPrefix,
			"password":    "TempPass!2026",
			"branch_id":   h.BranchID,
			"roles":       []string{role},
		}
		if grade != "" {
			body["technician_grade"] = grade
		}
		var u struct {
			ID string `json:"id"`
		}
		admin.do("POST", "/api/identity/users", body, &u, 201)
		return u.ID
	}
	tlID := mkUser("BTL", "BAST TL "+sx, "bast-tl-"+sx+"@ion.local", "team_leader", "")
	srID := mkUser("BSR", "BAST Senior "+sx, "bast-sr-"+sx+"@ion.local", "technician", "senior")
	jrID := mkUser("BJR", "BAST Junior "+sx, "bast-jr-"+sx+"@ion.local", "technician", "junior")

	// Team.
	var team struct {
		ID string `json:"id"`
	}
	admin.do("POST", "/api/field/teams", map[string]any{
		"name":           "BAST Team " + sx,
		"code":           "BAST-TEAM-" + sx,
		"branch_id":      h.BranchID,
		"team_leader_id": tlID,
	}, &team, 201)
	admin.do("POST", "/api/field/teams/"+team.ID+"/members",
		map[string]any{"user_id": srID, "grade": "senior"}, nil, 201)
	admin.do("POST", "/api/field/teams/"+team.ID+"/members",
		map[string]any{"user_id": jrID, "grade": "junior"}, nil, 201)

	// Install WO. The order from setupCoveredCustomer doesn't have one
	// attached yet — broadband_e2e_test creates it with just order_id
	// and the service derives the rest from the order.
	var wo struct {
		ID string `json:"id"`
	}
	admin.do("POST", "/api/field/work-orders", map[string]any{
		"order_id": h.OrderID,
	}, &wo, 201)

	// Route + assign + status=in_progress.
	admin.do("POST", "/api/field/work-orders/"+wo.ID+"/route",
		map[string]any{"team_id": team.ID}, nil, 200)
	admin.do("POST", "/api/field/work-orders/"+wo.ID+"/assign", map[string]any{
		"lead_id":        srID,
		"lead_grade":     "senior",
		"observer_id":    jrID,
		"observer_grade": "junior",
	}, nil, 200)
	admin.do("POST", "/api/field/work-orders/"+wo.ID+"/status",
		map[string]any{"status": "in_progress"}, nil, 200)

	// Checklist — submit a response for every item.
	var woDetail struct {
		ChecklistItems []struct {
			ID          string `json:"id"`
			GPSRequired bool   `json:"gps_required"`
		} `json:"checklist_items"`
	}
	admin.do("GET", "/api/field/work-orders/"+wo.ID, nil, &woDetail, 200)
	for _, it := range woDetail.ChecklistItems {
		body := map[string]any{
			"template_item_id": it.ID,
			"response_text":    "Wave 56 — checklist response",
		}
		if it.GPSRequired {
			// setupCoveredCustomer's lead is ~11m south of the ODP; that
			// GPS works for any required-GPS item.
			body["gps_lat"] = -6.0001
			body["gps_lng"] = 106.5001
		}
		admin.do("POST", "/api/field/work-orders/"+wo.ID+"/checklist", body, nil, 200)
	}

	// Submit BAST.
	var bast struct {
		ID        string `json:"id"`
		NOCStatus string `json:"noc_status"`
	}
	admin.do("POST", "/api/field/work-orders/"+wo.ID+"/bast",
		map[string]any{"sign_off_mode": "on_site"}, &bast, 200)
	if bast.NOCStatus != "pending" {
		t.Fatalf("setupWOReadyForNOC: BAST noc_status want pending, got %q", bast.NOCStatus)
	}

	// Pay the auto-OTC so the NOC payment gate clears.
	var invs struct {
		Items []struct {
			ID    string  `json:"id"`
			Total float64 `json:"total"`
		} `json:"items"`
	}
	admin.do("GET", "/api/billing/invoices?order_id="+h.OrderID, nil, &invs, 200)
	if len(invs.Items) == 0 {
		t.Fatal("setupWOReadyForNOC: no OTC invoice — setupCoveredCustomer drift?")
	}
	otc := invs.Items[0]
	admin.do("POST", "/api/billing/invoices/"+otc.ID+"/payments", map[string]any{
		"amount":                 otc.Total,
		"payment_method":         "manual",
		"gateway_transaction_id": "BAST-OTC-PAY-" + sx,
		"notes":                  "Wave 56 — clear payment gate so NOC can verify",
	}, nil, 201)

	return woReadyForNOC{
		CustomerID: h.CustomerID,
		OrderID:    h.OrderID,
		WOID:       wo.ID,
		BASTID:     bast.ID,
		TeamID:     team.ID,
		TLID:       tlID,
		SeniorID:   srID,
		JuniorID:   jrID,
	}
}

// setupActiveCustomer extends setupWOReadyForNOC by walking the chain
// one step further — NOC approves the BAST, which fires the activation
// hook (customer.status → 'active', radius row created). Use this when
// the test needs an active subscriber to act on (suspension, plan
// upgrade with billing impact, addons, etc.).
func setupActiveCustomer(t *testing.T, admin *client) woReadyForNOC {
	t.Helper()
	w := setupWOReadyForNOC(t, admin)
	admin.do("POST", "/api/field/basts/"+w.BASTID+"/verify",
		map[string]any{"decision": "approved", "notes": "Wave 57 — activation"},
		nil, 200)

	// Confirm activation actually took. If the broadband_e2e_test
	// would have caught a regression here, so would we.
	var cust struct {
		Status string `json:"status"`
	}
	admin.do("GET", "/api/crm/customers/"+w.CustomerID, nil, &cust, 200)
	if cust.Status != "active" {
		t.Fatalf("setupActiveCustomer: customer %s want active after NOC approve, got %q",
			w.CustomerID, cust.Status)
	}
	return w
}
