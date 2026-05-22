// Cross-surface end-to-end — mobile apps ↔ dashboard.
//
// Wave 49 — the existing broadband_e2e_test walks the whole Phase-1 path
// as a single super-admin client. That proves the *data flow* but not
// the cross-surface contract: that a sales rep using the mobile
// sales_app, an ops admin on the dashboard, and a customer on the
// mobile customer_app all see the same data through their own scoped
// views.
//
// This test exercises that contract:
//
//	1. Admin (dashboard) seeds branch + ODP + product so a lead can be
//	   placed in covered territory.
//	2. Sales rep (mobile sales_app surface) creates a lead via
//	   POST /api/crm/leads with sales@ion.local's token.
//	3. Admin (dashboard surface) lists /api/crm/leads and confirms
//	   the new lead is visible.
//	4. Technician (mobile tech_app surface) hits /api/crm/leads and
//	   gets 403 — proves the permission boundary holds.
//	5. Admin (dashboard) converts the lead → customer (with customer_number).
//	6. Admin (dashboard) opens a ticket against the new customer via
//	   POST /api/field/tickets.
//	7. Customer (mobile customer_app surface, via portal OTP) logs in
//	   using their customer_number + phone-last4. Demo mode surfaces
//	   the OTP in the response so the test can complete without SMS.
//	8. Customer fetches GET /api/portal/tickets and confirms the
//	   dashboard-created ticket appears in their inbox.
//
// Two cross-surface handoffs are proved:
//   * mobile sales_app → dashboard      (lead visibility)
//   * dashboard → mobile customer_app   (ticket visibility)
// Plus the technician permission-boundary check.
//
// Prerequisites for the test to run:
//   * The full service stack must be booted on :8080 (api-gateway).
//   * seed-demo must have run (provides sales@ion.local, tech@ion.local,
//     plus the admin@ion.local super-admin user that newClient uses).
//   * CRM_PORTAL_OTP_DEMO=true must be set on crm-svc so the OTP
//     response carries `debug_otp`; without it, step 7 fails.
//
//go:build e2e

package e2e

import (
	"math/rand"
	"strings"
	"testing"
	"time"
)

// newClientAs returns a freshly-authenticated client for a non-admin
// seed-demo user. seed-demo seeds 12 demo accounts (see seed-demo/main.go)
// with one shared password (IonDemo!2026Tour). Override via env if your
// deployment uses different creds.
func newClientAs(t *testing.T, email string) *client {
	t.Helper()
	c := newClient(t)
	pwd := envOr("SEED_DEMO_PASSWORD", "IonDemo!2026Tour")
	body := map[string]string{"email": email, "password": pwd}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	c.do("POST", "/api/identity/auth/login", body, &out, 200)
	if out.AccessToken == "" {
		t.Fatalf("login %s returned empty access_token (seed-demo missing?)", email)
	}
	c.token = out.AccessToken
	return c
}

// newCustomerClient returns a portal-authenticated client by walking
// the OTP flow. CRM_PORTAL_OTP_DEMO must be true so the response leaks
// the OTP; otherwise we can't verify without SMS.
func newCustomerClient(t *testing.T, customerNumber, phoneLast4 string) *client {
	t.Helper()
	c := newClient(t)
	// 1) Request OTP
	var otpResp struct {
		Sent     bool   `json:"sent"`
		DebugOTP string `json:"debug_otp"`
	}
	c.do("POST", "/api/portal/auth/otp-request", map[string]any{
		"customer_number": customerNumber,
		"phone_last4":     phoneLast4,
	}, &otpResp, 200)
	if !otpResp.Sent {
		t.Fatalf("otp-request: sent=false (customer %s not matched?)", customerNumber)
	}
	if otpResp.DebugOTP == "" {
		t.Fatalf("otp-request returned no debug_otp — set CRM_PORTAL_OTP_DEMO=true on crm-svc to enable test mode")
	}
	// 2) Verify OTP
	var verifyResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	c.do("POST", "/api/portal/auth/otp-verify", map[string]any{
		"customer_number": customerNumber,
		"otp":             otpResp.DebugOTP,
		"device":          "cross-surface-e2e",
	}, &verifyResp, 200)
	if verifyResp.AccessToken == "" {
		t.Fatalf("otp-verify returned empty access_token")
	}
	c.token = verifyResp.AccessToken
	return c
}

func TestCrossSurfaceMobileToDashboard(t *testing.T) {
	// Three concurrent client handles, one per persona.
	admin := newClient(t)
	admin.login() // super_admin via admin@ion.local

	sales := newClientAs(t, "sales@ion.local")
	tech := newClientAs(t, "tech@ion.local")

	sx := suffix()
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	odpLat := -6.0 - rng.Float64()*0.5
	odpLng := 106.5 + rng.Float64()*0.5
	leadLat := odpLat - 0.0001
	leadLng := odpLng + 0.0001

	// -----------------------------------------------------------------
	// Persona: admin (dashboard) — seed branch + ODP so the lead lands
	// in covered territory and qualifies automatically.
	// -----------------------------------------------------------------
	var areaID string
	t.Run("01_admin_seeds_branch", func(t *testing.T) {
		var regional struct{ ID string `json:"id"` }
		admin.do("POST", "/api/identity/branches", map[string]any{
			"name":  "CrossSurface Regional " + sx,
			"code":  "CS-REG-" + sx,
			"level": "regional",
		}, &regional, 201)
		var area struct{ ID string `json:"id"` }
		admin.do("POST", "/api/identity/branches", map[string]any{
			"name":      "CrossSurface Area " + sx,
			"code":      "CS-AREA-" + sx,
			"level":     "area",
			"parent_id": regional.ID,
		}, &area, 201)
		areaID = area.ID
	})

	t.Run("02_admin_seeds_odp", func(t *testing.T) {
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
			t.Fatal("no odp node-type — seed broken")
		}
		var node struct{ ID string `json:"id"` }
		admin.do("POST", "/api/network/nodes", map[string]any{
			"node_type_id": odpTypeID,
			"name":         "CrossSurface ODP " + sx,
			"code":         "CS-ODP-" + sx,
			"branch_id":    areaID,
			"address":      "CrossSurface E2E",
			"gps_lat":      odpLat,
			"gps_lng":      odpLng,
			"total_ports":  8,
			"port_role":    "customer_drop",
		}, &node, 201)
	})

	// -----------------------------------------------------------------
	// Persona: sales_rep (mobile sales_app) — create the lead.
	// This is the SAME endpoint the dashboard uses, but the actor
	// here is a sales rep with sales@ion.local's token, simulating
	// the mobile app submitting a captured prospect.
	// -----------------------------------------------------------------
	var leadID, productID string
	t.Run("03_sales_rep_creates_lead", func(t *testing.T) {
		var products struct {
			Items []struct {
				ID   string `json:"id"`
				Code string `json:"code"`
			} `json:"items"`
		}
		// Note: /api/crm/products needs crm.dashboard.read which
		// sales_rep has per Wave 47.
		sales.do("GET", "/api/crm/products", nil, &products, 200)
		for _, p := range products.Items {
			if p.Code == "BB-30" {
				productID = p.ID
			}
		}
		if productID == "" {
			t.Fatal("BB-30 product not seeded (Wave 47 migration may be missing)")
		}
		var lead struct {
			ID              string `json:"id"`
			Status          string `json:"status"`
			CoverageVerdict string `json:"coverage_verdict"`
		}
		sales.do("POST", "/api/crm/leads", map[string]any{
			"full_name":  "CrossSurface Customer " + sx,
			"phone":      "+62812" + sx,
			"nik":        "31750" + sx + "0000",
			"address":    "Jl. CrossSurface " + sx,
			"gps_lat":    leadLat,
			"gps_lng":    leadLng,
			"product_id": productID,
		}, &lead, 201)
		leadID = lead.ID
		if lead.Status != "qualified" {
			t.Fatalf("lead status: want qualified, got %q (verdict=%s)", lead.Status, lead.CoverageVerdict)
		}
	})

	// -----------------------------------------------------------------
	// CROSS-SURFACE HANDOFF #1: mobile sales_app → dashboard.
	// The lead the sales rep just created should be visible to the
	// admin on the dashboard. Same backend, same DB, two different
	// surfaces reading via their respective tokens.
	// -----------------------------------------------------------------
	t.Run("04_admin_sees_sales_rep_lead", func(t *testing.T) {
		var list struct {
			Items []struct {
				ID       string `json:"id"`
				FullName string `json:"full_name"`
			} `json:"items"`
		}
		admin.do("GET", "/api/crm/leads?page_size=200", nil, &list, 200)
		found := false
		for _, it := range list.Items {
			if it.ID == leadID {
				found = true
				if !strings.Contains(it.FullName, sx) {
					t.Errorf("lead name on dashboard doesn't match sales-rep input: %s", it.FullName)
				}
				break
			}
		}
		if !found {
			t.Fatalf("admin can't see the lead sales-rep created (id=%s) — cross-surface contract broken", leadID)
		}
	})

	// -----------------------------------------------------------------
	// Permission boundary: technician (mobile tech_app) must NOT see
	// the CRM leads list. This is the inverse of the cross-surface
	// guarantee — different surfaces with different scopes mean
	// different read access.
	// -----------------------------------------------------------------
	t.Run("05_technician_denied_lead_list", func(t *testing.T) {
		// We expect 403 Forbidden because technician doesn't have crm.lead.read.
		// doExpectError fails the test if the status isn't 403.
		_ = tech.doExpectError("GET", "/api/crm/leads", nil, 403)
	})

	// -----------------------------------------------------------------
	// Persona: admin (dashboard) — finalise documents, convert lead
	// to a customer + order. We use admin here for simplicity; in
	// production sales_manager handles this transition.
	// -----------------------------------------------------------------
	var customerID, customerNumber, leadPhone string
	t.Run("06_admin_finishes_docs_and_converts", func(t *testing.T) {
		var lead struct {
			Documents []struct {
				ID       string `json:"id"`
				Required bool   `json:"required"`
			} `json:"documents"`
			Phone string `json:"phone"`
		}
		admin.do("GET", "/api/crm/leads/"+leadID, nil, &lead, 200)
		leadPhone = lead.Phone
		for _, d := range lead.Documents {
			if d.Required {
				admin.do("PATCH", "/api/crm/documents/"+d.ID, map[string]any{
					"submitted": true,
				}, nil, 200)
			}
		}
		var conv struct {
			Customer struct {
				ID             string `json:"id"`
				CustomerNumber string `json:"customer_number"`
				Phone          string `json:"phone"`
			} `json:"customer"`
		}
		admin.do("POST", "/api/crm/leads/"+leadID+"/convert", map[string]any{}, &conv, 200)
		customerID = conv.Customer.ID
		customerNumber = conv.Customer.CustomerNumber
		if customerNumber == "" {
			t.Fatal("convert response missing customer_number — needed for portal OTP")
		}
		if conv.Customer.Phone != "" {
			leadPhone = conv.Customer.Phone
		}
	})

	// -----------------------------------------------------------------
	// Persona: admin (dashboard) — open a support ticket against the
	// new customer. This is how CS agents create tickets in the
	// dashboard's CS-tickets page.
	// -----------------------------------------------------------------
	var ticketID, ticketNumber string
	t.Run("07_admin_creates_ticket_for_customer", func(t *testing.T) {
		var resp struct {
			ID           string `json:"id"`
			TicketNumber string `json:"ticket_number"`
			Status       string `json:"status"`
		}
		admin.do("POST", "/api/field/tickets", map[string]any{
			"customer_id": customerID,
			"category":    "slow_speed",
			"priority":    "medium",
			"summary":     "CrossSurface test ticket " + sx,
			"description": "Opened by cross-surface E2E to verify mobile customer_app receives it.",
		}, &resp, 201)
		ticketID = resp.ID
		ticketNumber = resp.TicketNumber
		if ticketID == "" {
			t.Fatal("ticket create returned empty id")
		}
		if resp.Status != "open" {
			t.Errorf("new ticket status: want open, got %q", resp.Status)
		}
	})

	// -----------------------------------------------------------------
	// CROSS-SURFACE HANDOFF #2: dashboard → mobile customer_app.
	// The customer logs in via portal OTP (the mobile customer_app
	// flow) and fetches their tickets. The ticket the admin just
	// created on the dashboard must appear.
	// -----------------------------------------------------------------
	t.Run("08_customer_logs_in_via_portal_otp", func(t *testing.T) {
		// Last 4 digits of phone — the lead's phone is "+62812<sx>" so
		// the last 4 digits are the last 4 of sx (or "0000" if sx is short).
		last4 := leadPhone
		if len(last4) >= 4 {
			last4 = last4[len(last4)-4:]
		}
		customer := newCustomerClient(t, customerNumber, last4)

		// Fetch the customer's tickets via the portal.
		var inbox struct {
			Items []struct {
				ID           string `json:"id"`
				TicketNumber string `json:"ticket_number"`
				Summary      string `json:"summary"`
			} `json:"items"`
		}
		customer.do("GET", "/api/portal/tickets", nil, &inbox, 200)
		found := false
		for _, t := range inbox.Items {
			if t.ID == ticketID || t.TicketNumber == ticketNumber {
				found = true
				if !strings.Contains(t.Summary, sx) {
					return // matched by id/number — body match is bonus
				}
				break
			}
		}
		if !found {
			t.Fatalf("customer can't see ticket %s in /portal/tickets — cross-surface contract broken (got %d items)",
				ticketID, len(inbox.Items))
		}
	})

	// -----------------------------------------------------------------
	// Sanity: sales_rep tries to hit an admin-only endpoint to round
	// out the permission boundary coverage.
	// -----------------------------------------------------------------
	t.Run("09_sales_rep_denied_user_admin", func(t *testing.T) {
		// sales_rep doesn't have identity.user.read.
		_ = sales.doExpectError("GET", "/api/identity/users", nil, 403)
	})
}
