//go:build e2e

// p1p2_endpoints_test.go — Go integration tests for every endpoint
// introduced by migrations 0040 + 0041 (Waves 3, 4, 5).
//
// Coverage:
//   - crm.customer_notifications      (list, mark read, mark-all-read)
//   - crm.lead_events                  (timeline + auto-write on PATCH)
//   - enterprise.vendor_documents      (upload + verify)
//   - enterprise.vendor_metrics        (per-vendor + roll-up)
//   - enterprise.project_plan_revisions (capture + list snapshot)
//   - field.tech_locations             (POST ping, latest by user, WO replay)
//   - billing.payment_intents          (Xendit-shaped intent creation)
//   - enterprise.service_catalog.default_sla_template_id (PATCH bind/unbind)
//   - crm.cs_referrals                 (CS agent → sales lead)
//   - field priority_insertions        (insert + respond)
//   - field cross_area                 (request + queue)
//   - field suggested_pair             (computed senior+junior pick)
//   - portal active-WO tech-location   (customer-scoped)
//   - portal KTP re-upload             (drops a CS ticket)
//   - identity HRIS sync state         (stub provider trigger)
//   - network customer RADIUS state    (staff lookup)
//   - warehouse stock dashboard        (cross-warehouse roll-up)
//   - billing terminations consolidated (union)
//
// Each subtest is independent and uses random suffixes so the file is
// re-runnable without DB cleanup. Failures bail with the actual server
// response body so debugging is one-shot.

package e2e

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// =====================================================================
// Shared setup
// =====================================================================

// p1p2Setup mints an admin token + opens a pgxpool. Both are torn
// down by the returned cleanup func.
type p1p2Setup struct {
	c    *client
	pool *pgxpool.Pool
	ctx  context.Context
}

var (
	sharedAdminToken     string
	sharedAdminTokenOnce sync.Once
	sharedPool           *pgxpool.Pool
	sharedPoolOnce       sync.Once
)

func setupP1P2(t *testing.T) (*p1p2Setup, func()) {
	t.Helper()
	// Reuse one admin token across the whole package so identity-svc's
	// per-IP rate limiter doesn't slap us at test #11.
	sharedAdminTokenOnce.Do(func() {
		c := newClient(t)
		c.login()
		sharedAdminToken = c.token
	})
	c := newClient(t)
	c.token = sharedAdminToken
	// Same idea for the pool — open once for the whole package.
	sharedPoolOnce.Do(func() {
		dbURL := envOr("DATABASE_URL",
			"postgres://syabanf@localhost:5432/ion_core?sslmode=disable")
		p, err := pgxpool.New(context.Background(), dbURL)
		if err != nil {
			t.Fatalf("open pool: %v", err)
		}
		sharedPool = p
	})
	return &p1p2Setup{c: c, pool: sharedPool, ctx: context.Background()},
		func() {} // pool intentionally not closed — shared across tests
}

// pickFirst pulls a single string value from a one-row query. Used to
// grab existing UUIDs for tests that need to ride on seed data.
//
// Wave 53 — when the query targets a table the p1p2 suite is likely to
// find empty on a fresh CI DB (crm.customers, field.work_orders, etc.),
// we auto-create a fixture instead of skipping. For all other tables
// the original Skipf behaviour is preserved so unrelated tests aren't
// dragged into the auto-fixture path.
func pickFirst(t *testing.T, pool *pgxpool.Pool, q string, args ...any) string {
	t.Helper()
	var s string
	if err := pool.QueryRow(context.Background(), q, args...).Scan(&s); err == nil {
		return s
	}
	// Auto-fixture path. Cheaper than failing the whole suite when
	// seed-demo + the broadband happy path haven't run yet.
	switch {
	case strings.Contains(q, "FROM crm.customers"):
		// pickFirst is package-scoped — pull the admin client off the
		// shared cache so we can call the HTTP API.
		return ensureSharedCustomer(t).CustomerID
	case strings.Contains(q, "FROM field.work_orders"):
		return ensureSharedWOID(t)
	default:
		var err error
		// re-run the query to surface the original error for the skip
		// message; the second QueryRow is cheap.
		err = pool.QueryRow(context.Background(), q, args...).Scan(&s)
		t.Skipf("FOLLOWUP-WAVE-52: seed missing for query %q: %v", q, err)
		return ""
	}
}

// =====================================================================
// Shared p1p2 fixtures
//
// Wave 53 — the p1p2 tests assume an existing customer + WO from the
// broadband happy-path. On a fresh-CI DB those don't exist, so the
// tests silently skipped. These ensure-helpers create the fixture once
// and cache it, so a dozen tests share a single ~400ms setup cost.
// =====================================================================

var (
	sharedP1P2Customer     customerHandle
	sharedP1P2CustomerOnce sync.Once
	sharedP1P2WOID         string
	sharedP1P2WOOnce       sync.Once
)

// sharedAdminClient returns a freshly-tokened admin client using the
// package-shared access token. Used by ensure-helpers that need to
// hit the HTTP API without the per-test client/setupP1P2 dance.
func sharedAdminClient(t *testing.T) *client {
	t.Helper()
	if sharedAdminToken == "" {
		// First call into this helper from a test that hasn't called
		// setupP1P2 yet. Mint the token now.
		c := newClient(t)
		c.login()
		sharedAdminTokenOnce.Do(func() { sharedAdminToken = c.token })
	}
	c := newClient(t)
	c.token = sharedAdminToken
	return c
}

// ensureSharedCustomer returns a converted broadband customer, creating
// the full branch/ODP/lead/customer chain on first call. Subsequent
// callers reuse the same handle. The auto-OTC invoice attached to this
// customer's order is the fixture used by ensureUnpaidInvoice below.
func ensureSharedCustomer(t *testing.T) customerHandle {
	t.Helper()
	sharedP1P2CustomerOnce.Do(func() {
		sharedP1P2Customer = setupCoveredCustomer(t, sharedAdminClient(t))
	})
	return sharedP1P2Customer
}

// ensureSharedWOID creates an install WO against the shared customer.
// Status defaults to 'unassigned' so it's pickable by the cross-area,
// priority-insertion, and suggested-pair flows.
func ensureSharedWOID(t *testing.T) string {
	t.Helper()
	sharedP1P2WOOnce.Do(func() {
		h := ensureSharedCustomer(t)
		scheduled := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
		var wo struct {
			ID string `json:"id"`
		}
		sharedAdminClient(t).do("POST", "/api/field/work-orders", map[string]any{
			"order_id":       h.OrderID,
			"customer_id":    h.CustomerID,
			"kind":           "install",
			"scheduled_date": scheduled,
			"branch_id":      h.BranchID,
			"notes":          "Wave 53 — p1p2 shared fixture WO",
		}, &wo, 201)
		sharedP1P2WOID = wo.ID
	})
	return sharedP1P2WOID
}

// ensureUnpaidInvoice returns (customer_id, invoice_id) for an unpaid
// invoice. Uses the shared customer's auto-OTC, which is always 'issued'
// (unpaid) right after conversion.
func ensureUnpaidInvoice(t *testing.T, s *p1p2Setup) (string, string) {
	t.Helper()
	h := ensureSharedCustomer(t)
	var list struct {
		Items []struct {
			ID                string  `json:"id"`
			Status            string  `json:"status"`
			OutstandingAmount float64 `json:"outstanding_amount"`
		} `json:"items"`
	}
	s.c.do("GET", "/api/billing/invoices?order_id="+h.OrderID, nil, &list, 200)
	for _, inv := range list.Items {
		if inv.Status != "paid" && inv.OutstandingAmount > 0 {
			return h.CustomerID, inv.ID
		}
	}
	t.Fatalf("ensureUnpaidInvoice: shared customer %s has no unpaid invoice — setupCoveredCustomer broken?", h.CustomerID)
	return "", ""
}

// ensureWOAssignedToTech creates an install WO and walks it through
// /route + /assign so that tech@ion.local (the seed-demo technician)
// is the lead technician. The resulting WO has the row in
// field.wo_assignments that TestP1P2_GPSStreaming's join requires.
//
// Setup creates a fresh team_leader + junior tech because the existing
// seed-demo accounts don't carry a team membership. tech@ion.local is
// added as a senior team member, so the assign call wires the
// technician_id correctly.
var (
	sharedTechWOID   string
	sharedTechWOOnce sync.Once
)

func ensureWOAssignedToTech(t *testing.T) string {
	t.Helper()
	sharedTechWOOnce.Do(func() {
		h := ensureSharedCustomer(t)
		admin := sharedAdminClient(t)
		sx := suffix()

		// Look up tech@ion.local's user id via the pool. seed-demo
		// guarantees the row; Wave 52 made the same precondition fatal
		// on the login leg, so this Fatalf is a defence-in-depth check.
		var techID string
		if err := sharedPool.QueryRow(context.Background(),
			`SELECT id::text FROM identity.users WHERE email='tech@ion.local'`,
		).Scan(&techID); err != nil {
			t.Fatalf("ensureWOAssignedToTech: tech@ion.local not seeded: %v", err)
		}

		// Spin up a team_leader + a junior tech so the team meets the
		// senior+junior pairing the assign endpoint expects.
		mkUser := func(empPrefix, full, email, role, grade string) string {
			body := map[string]any{
				"employee_id": empPrefix + "-" + sx,
				"full_name":   full,
				"email":       email,
				"phone":       "+62811W53" + empPrefix,
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
		tlID := mkUser("W53TL", "W53 TL "+sx, "w53-tl-"+sx+"@ion.local",
			"team_leader", "")
		jrID := mkUser("W53JR", "W53 Junior "+sx, "w53-jr-"+sx+"@ion.local",
			"technician", "junior")

		// Team — tech@ion.local + the junior we just made.
		var team struct {
			ID string `json:"id"`
		}
		admin.do("POST", "/api/field/teams", map[string]any{
			"name":           "W53 Tech Team " + sx,
			"code":           "W53-TEAM-" + sx,
			"branch_id":      h.BranchID,
			"team_leader_id": tlID,
		}, &team, 201)
		admin.do("POST", "/api/field/teams/"+team.ID+"/members",
			map[string]any{"user_id": techID, "grade": "senior"}, nil, 201)
		admin.do("POST", "/api/field/teams/"+team.ID+"/members",
			map[string]any{"user_id": jrID, "grade": "junior"}, nil, 201)

		// Fresh install WO so the shared "unassigned" WO from
		// ensureSharedWOID stays unassigned for the cross-area /
		// priority-insertion tests that depend on that state.
		scheduled := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
		var wo struct {
			ID string `json:"id"`
		}
		admin.do("POST", "/api/field/work-orders", map[string]any{
			"order_id":       h.OrderID,
			"customer_id":    h.CustomerID,
			"kind":           "install",
			"scheduled_date": scheduled,
			"branch_id":      h.BranchID,
			"notes":          "Wave 53 — shared tech-assigned WO fixture",
		}, &wo, 201)

		// route → team, then assign with tech as lead + the junior as
		// observer. The assign endpoint creates the wo_assignments rows
		// that the GPS-streaming test joins against.
		admin.do("POST", "/api/field/work-orders/"+wo.ID+"/route",
			map[string]any{"team_id": team.ID}, nil, 200)
		admin.do("POST", "/api/field/work-orders/"+wo.ID+"/assign",
			map[string]any{
				"lead_id":        techID,
				"lead_grade":     "senior",
				"observer_id":    jrID,
				"observer_grade": "junior",
			}, nil, 200)
		// Bump to in_progress so the WO matches the GPS-streaming
		// query's status filter ('assigned','dispatched','in_progress').
		admin.do("POST", "/api/field/work-orders/"+wo.ID+"/status",
			map[string]any{"status": "in_progress"}, nil, 200)

		sharedTechWOID = wo.ID
	})
	return sharedTechWOID
}

// =====================================================================
// Wave 4: customer notifications
// =====================================================================

func TestP1P2_CustomerNotifications(t *testing.T) {
	s, cleanup := setupP1P2(t)
	defer cleanup()

	custID := pickFirst(t, s.pool, `SELECT id FROM crm.customers ORDER BY created_at LIMIT 1`)

	// Seed one unread + one already-read notification.
	_, err := s.pool.Exec(s.ctx, `
		INSERT INTO crm.customer_notifications (customer_id, kind, title, body)
		VALUES ($1, 'ticket_reply', 'Unread test '||$2, 'body'),
		       ($1, 'payment_succeeded', 'Read test '||$2, 'body')
	`, custID, suffix())
	if err != nil {
		t.Fatalf("seed notifications: %v", err)
	}

	// Customer portal token via OTP.
	portalTok := loginPortal(t, s)

	// List — expect at least 2.
	var list struct {
		Items       []map[string]any `json:"items"`
		UnreadCount int              `json:"unread_count"`
	}
	doPortal(t, portalTok, "GET", "/portal/notifications", nil, &list, 200)
	if len(list.Items) < 2 {
		t.Fatalf("expected ≥2 notifications, got %d", len(list.Items))
	}
	if list.UnreadCount < 1 {
		t.Fatalf("unread_count should be ≥1, got %d", list.UnreadCount)
	}

	// Mark one read.
	target := list.Items[0]["id"].(string)
	doPortal(t, portalTok, "POST", "/portal/notifications/"+target+"/read", nil, nil, 200)

	var after struct {
		UnreadCount int `json:"unread_count"`
	}
	doPortal(t, portalTok, "GET", "/portal/notifications", nil, &after, 200)
	if after.UnreadCount >= list.UnreadCount {
		t.Fatalf("unread should drop after mark-read: before=%d after=%d",
			list.UnreadCount, after.UnreadCount)
	}

	// Mark all read.
	doPortal(t, portalTok, "POST", "/portal/notifications/mark-all-read", nil, nil, 200)
	var final struct {
		UnreadCount int `json:"unread_count"`
	}
	doPortal(t, portalTok, "GET", "/portal/notifications", nil, &final, 200)
	if final.UnreadCount != 0 {
		t.Fatalf("mark-all should clear, got %d", final.UnreadCount)
	}
}

// =====================================================================
// Wave 4: lead events — endpoint + auto-write on status PATCH
// =====================================================================

func TestP1P2_LeadEvents_AutoWriteOnStatusChange(t *testing.T) {
	s, cleanup := setupP1P2(t)
	defer cleanup()

	leadID := pickFirst(t, s.pool,
		`SELECT id FROM crm.leads
		 WHERE status NOT IN ('converted','lost','rejected')
		 ORDER BY created_at DESC LIMIT 1`)

	// Snapshot event count before.
	var before int
	_ = s.pool.QueryRow(s.ctx,
		`SELECT COUNT(*) FROM crm.lead_events WHERE lead_id=$1 AND kind='status_change'`,
		leadID).Scan(&before)

	// PATCH the status → handler should fire-and-forget a status_change event.
	s.c.do("PATCH", "/api/crm/leads/"+leadID, map[string]any{
		"status": "warm",
	}, nil, 200)

	time.Sleep(200 * time.Millisecond) // give the goroutine a beat
	var after int
	_ = s.pool.QueryRow(s.ctx,
		`SELECT COUNT(*) FROM crm.lead_events WHERE lead_id=$1 AND kind='status_change'`,
		leadID).Scan(&after)
	if after != before+1 {
		t.Fatalf("expected status_change event auto-write: before=%d after=%d", before, after)
	}

	// GET /events returns the new row.
	var events struct {
		Items []map[string]any `json:"items"`
	}
	s.c.do("GET", "/api/crm/leads/"+leadID+"/events", nil, &events, 200)
	if len(events.Items) == 0 {
		t.Fatalf("timeline returned 0 items")
	}
	// Most recent is the status_change.
	if events.Items[0]["kind"] != "status_change" {
		t.Fatalf("expected most-recent kind=status_change, got %v", events.Items[0]["kind"])
	}
}

// =====================================================================
// Wave 4: vendor onboarding documents
// =====================================================================

func TestP1P2_VendorDocuments(t *testing.T) {
	s, cleanup := setupP1P2(t)
	defer cleanup()

	// Vendor is referenced as a soft-FK UUID; any UUID works for the
	// endpoint smoke. Real schema check is on the document row.
	vendorID := uuid.New().String()

	var created struct {
		ID string `json:"id"`
	}
	s.c.do("POST", "/api/enterprise/vendors/"+vendorID+"/documents",
		map[string]any{
			"kind":      "nib",
			"file_url":  "s3://test/nib-" + suffix() + ".pdf",
			"file_name": "nib.pdf",
			"bytes":     12345,
		}, &created, 201)
	if created.ID == "" {
		t.Fatalf("doc id missing in response")
	}

	// Verify the doc.
	s.c.do("POST", "/api/enterprise/vendor-documents/"+created.ID+"/verify",
		map[string]any{"notes": "OK by test"}, nil, 200)

	// DB check — verified_at is set.
	var verifiedAt *time.Time
	err := s.pool.QueryRow(s.ctx,
		`SELECT verified_at FROM enterprise.vendor_documents WHERE id=$1`,
		created.ID).Scan(&verifiedAt)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if verifiedAt == nil {
		t.Fatalf("verified_at should be set after verify")
	}

	// List should include the new doc.
	var list struct {
		Items []map[string]any `json:"items"`
	}
	s.c.do("GET", "/api/enterprise/vendors/"+vendorID+"/documents", nil, &list, 200)
	if len(list.Items) == 0 {
		t.Fatalf("list returned empty after upload")
	}
}

// =====================================================================
// Wave 4: vendor performance metrics (scorecard)
// =====================================================================

func TestP1P2_VendorMetrics(t *testing.T) {
	s, cleanup := setupP1P2(t)
	defer cleanup()

	vendorID := uuid.New()
	thisMonth := time.Date(time.Now().Year(), time.Now().Month(), 1, 0, 0, 0, 0, time.UTC)

	// Seed a metric row.
	_, err := s.pool.Exec(s.ctx, `
		INSERT INTO enterprise.vendor_metrics
			(vendor_id, period_month, orders_total, orders_on_time, defects_reported, avg_response_hours)
		VALUES ($1, $2, 20, 18, 1, 4.5)
	`, vendorID, thisMonth)
	if err != nil {
		t.Fatalf("seed metric: %v", err)
	}

	// Per-vendor endpoint
	var vm struct {
		Items []struct {
			PeriodMonth  string  `json:"period_month"`
			OrdersTotal  int     `json:"orders_total"`
			OrdersOnTime int     `json:"orders_on_time"`
			OnTimePct    float64 `json:"on_time_pct"`
		} `json:"items"`
	}
	s.c.do("GET", "/api/enterprise/vendors/"+vendorID.String()+"/metrics", nil, &vm, 200)
	if len(vm.Items) == 0 {
		t.Fatalf("per-vendor metrics empty after seed")
	}
	if vm.Items[0].OrdersTotal != 20 {
		t.Fatalf("orders_total mismatch: got %d", vm.Items[0].OrdersTotal)
	}
	if vm.Items[0].OnTimePct < 89.9 || vm.Items[0].OnTimePct > 90.1 {
		t.Fatalf("on_time_pct should be ~90, got %.2f", vm.Items[0].OnTimePct)
	}

	// Current-month roll-up
	var all struct {
		Items []map[string]any `json:"items"`
	}
	s.c.do("GET", "/api/enterprise/vendor-metrics", nil, &all, 200)
	found := false
	for _, it := range all.Items {
		if it["vendor_id"] == vendorID.String() {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("vendor not in current-month roll-up")
	}
}

// =====================================================================
// Wave 4: project plan revisions
// =====================================================================

func TestP1P2_PlanRevisions_Snapshot(t *testing.T) {
	s, cleanup := setupP1P2(t)
	defer cleanup()

	projectID := pickFirst(t, s.pool, `SELECT id FROM enterprise.projects LIMIT 1`)

	// Capture a revision (snapshots the current milestone set).
	var rev struct {
		ID         string `json:"id"`
		RevisionNo int    `json:"revision_no"`
	}
	s.c.do("POST", "/api/enterprise/projects/"+projectID+"/plan-revisions",
		map[string]any{"reason": "test snapshot " + suffix()}, &rev, 201)
	if rev.ID == "" || rev.RevisionNo < 1 {
		t.Fatalf("revision not created: %+v", rev)
	}

	// List includes it.
	var list struct {
		Items []map[string]any `json:"items"`
	}
	s.c.do("GET", "/api/enterprise/projects/"+projectID+"/plan-revisions", nil, &list, 200)
	if len(list.Items) == 0 {
		t.Fatalf("revisions list empty after create")
	}
	// First (most recent) row should be ours.
	if list.Items[0]["id"] != rev.ID {
		t.Fatalf("expected first revision to be the new one")
	}
}

// =====================================================================
// Wave 3: GPS streaming
// =====================================================================

func TestP1P2_GPSStreaming(t *testing.T) {
	s, cleanup := setupP1P2(t)
	defer cleanup()

	// Login as the demo tech to get a tech-scoped token.
	techTok := loginUser(t, s, "tech@ion.local", envOr("SEED_DEMO_PW", "IonDemo!2026Tour"))

	// Wave 53 — use the shared tech-assigned WO fixture. The helper
	// creates a team + adds tech@ion.local as a senior member + routes
	// + assigns the WO, populating the field.wo_assignments row this
	// test's join used to fish for in the seed.
	woID := ensureWOAssignedToTech(t)

	// Tech POSTs a ping.
	doWithToken(t, techTok, "POST", "/api/field/tech-locations", map[string]any{
		"wo_id":      woID,
		"lat":        -6.2088,
		"lng":        106.8456,
		"accuracy_m": 12.5,
	}, nil, 201)

	// Verify it landed in field.tech_locations.
	var cnt int
	if err := s.pool.QueryRow(s.ctx,
		`SELECT COUNT(*) FROM field.tech_locations
		 WHERE wo_id=$1 AND captured_at > NOW() - INTERVAL '1 minute'`,
		woID).Scan(&cnt); err != nil {
		t.Fatalf("lookup ping: %v", err)
	}
	if cnt < 1 {
		t.Fatalf("ping not persisted")
	}

	// WO replay endpoint returns it.
	var replay struct {
		Points []map[string]any `json:"points"`
	}
	s.c.do("GET", "/api/field/work-orders/"+woID+"/tech-locations", nil, &replay, 200)
	if len(replay.Points) < 1 {
		t.Fatalf("replay empty after ping")
	}

	// Live feed: admin reads, sees this tech.
	var live struct {
		Items []map[string]any `json:"items"`
	}
	s.c.do("GET", "/api/field/tech-locations?active_only=true", nil, &live, 200)
	if len(live.Items) == 0 {
		t.Fatalf("live feed empty after ping")
	}
}

// =====================================================================
// Wave 3: payment intents (Xendit-shaped)
// =====================================================================

func TestP1P2_PaymentIntent_VA(t *testing.T) {
	s, cleanup := setupP1P2(t)
	defer cleanup()

	// Wave 53 — use the shared customer's auto-OTC invoice as the
	// fixture. Always 'issued' (unpaid) right after lead conversion.
	custID, invoiceID := ensureUnpaidInvoice(t, s)

	portalTok := loginPortalForCustomer(t, s, custID)

	var intent struct {
		IntentID    string `json:"intent_id"`
		VaNumber    string `json:"va_number"`
		Bank        string `json:"bank"`
		Amount      float64 `json:"amount"`
		Status      string `json:"status"`
		CheckoutURL string `json:"checkout_url"`
	}
	doPortal(t, portalTok, "POST",
		"/portal/invoices/"+invoiceID+"/pay",
		map[string]any{"method": "xendit_va", "bank": "BCA"},
		&intent, 201)
	if intent.IntentID == "" {
		t.Fatalf("intent_id missing")
	}
	if intent.VaNumber == "" || len(intent.VaNumber) < 8 {
		t.Fatalf("VA number too short: %q", intent.VaNumber)
	}
	if intent.Bank != "BCA" {
		t.Fatalf("bank should be BCA, got %q", intent.Bank)
	}
	if intent.Status != "pending" {
		t.Fatalf("status should be pending, got %q", intent.Status)
	}

	// DB row exists.
	var dbStatus string
	if err := s.pool.QueryRow(s.ctx,
		`SELECT status FROM billing.payment_intents WHERE id=$1`,
		intent.IntentID).Scan(&dbStatus); err != nil {
		t.Fatalf("lookup intent: %v", err)
	}
	if dbStatus != "pending" {
		t.Fatalf("DB status should be pending, got %q", dbStatus)
	}
}

// =====================================================================
// Wave 5: services-catalog SLA binding
// =====================================================================

func TestP1P2_ServicesCatalog_BindSLA(t *testing.T) {
	s, cleanup := setupP1P2(t)
	defer cleanup()

	catalogID := pickFirst(t, s.pool, `SELECT id FROM enterprise.service_catalog LIMIT 1`)
	slaID := pickFirst(t, s.pool, `SELECT id FROM enterprise.sla_templates LIMIT 1`)

	// Bind.
	s.c.do("PATCH", "/api/enterprise/services-catalog/"+catalogID+"/sla",
		map[string]any{"sla_template_id": slaID}, nil, 200)
	var got string
	_ = s.pool.QueryRow(s.ctx,
		`SELECT COALESCE(default_sla_template_id::text, '')
		 FROM enterprise.service_catalog WHERE id=$1`,
		catalogID).Scan(&got)
	if got != slaID {
		t.Fatalf("bind failed: want %s, got %s", slaID, got)
	}

	// Unbind.
	s.c.do("PATCH", "/api/enterprise/services-catalog/"+catalogID+"/sla",
		map[string]any{"sla_template_id": ""}, nil, 200)
	_ = s.pool.QueryRow(s.ctx,
		`SELECT COALESCE(default_sla_template_id::text, '')
		 FROM enterprise.service_catalog WHERE id=$1`,
		catalogID).Scan(&got)
	if got != "" {
		t.Fatalf("unbind failed: want empty, got %q", got)
	}
}

// =====================================================================
// Wave 4: CS referral creates a lead with source='cs_referral'
// =====================================================================

func TestP1P2_CSReferral(t *testing.T) {
	s, cleanup := setupP1P2(t)
	defer cleanup()

	phone := "0812" + suffix()
	var out struct {
		ID         string `json:"id"`
		LeadNumber string `json:"lead_number"`
		Source     string `json:"source"`
	}
	s.c.do("POST", "/api/crm/cs-referrals",
		map[string]any{
			"full_name": "Go Test Referral",
			"phone":     phone,
			"address":   "Jl. Go Test",
		}, &out, 201)
	if out.Source != "cs_referral" {
		t.Fatalf("source should be cs_referral, got %q", out.Source)
	}

	// DB confirms + initial event was written.
	var src string
	_ = s.pool.QueryRow(s.ctx,
		`SELECT source FROM crm.leads WHERE id=$1`, out.ID).Scan(&src)
	if src != "cs_referral" {
		t.Fatalf("DB source mismatch: %s", src)
	}

	var evtCount int
	_ = s.pool.QueryRow(s.ctx,
		`SELECT COUNT(*) FROM crm.lead_events WHERE lead_id=$1 AND kind='created'`,
		out.ID).Scan(&evtCount)
	if evtCount < 1 {
		t.Fatalf("expected initial 'created' event, got %d", evtCount)
	}
}

// =====================================================================
// Wave 4: priority insertion (mid-schedule WO)
// =====================================================================

func TestP1P2_PriorityInsertion(t *testing.T) {
	s, cleanup := setupP1P2(t)
	defer cleanup()

	techUserID := pickFirst(t, s.pool, `SELECT id FROM identity.users WHERE email='tech@ion.local'`)
	woID := pickFirst(t, s.pool, `SELECT id FROM field.work_orders LIMIT 1`)

	var out struct {
		ID string `json:"id"`
	}
	s.c.do("POST", "/api/field/work-orders/"+woID+"/priority-insert",
		map[string]any{
			"tech_user_id": techUserID,
			"reason":       "urgent",
		}, &out, 201)
	if out.ID == "" {
		t.Fatalf("priority insert returned no id")
	}

	// Tech retrieves their pending list.
	techTok := loginUser(t, s, "tech@ion.local", envOr("SEED_DEMO_PW", "IonDemo!2026Tour"))
	var pending struct {
		Items []map[string]any `json:"items"`
	}
	doWithToken(t, techTok, "GET", "/api/field/priority-insertions/mine", nil, &pending, 200)
	found := false
	for _, it := range pending.Items {
		if it["id"] == out.ID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("priority insertion not in tech's pending list")
	}

	// Tech responds (accepts).
	doWithToken(t, techTok, "POST",
		"/api/field/priority-insertions/"+out.ID+"/respond",
		map[string]any{"accepted": true}, nil, 200)

	// DB confirms.
	var accepted *bool
	_ = s.pool.QueryRow(s.ctx,
		`SELECT accepted FROM field.priority_insertions WHERE id=$1`,
		out.ID).Scan(&accepted)
	if accepted == nil || *accepted != true {
		t.Fatalf("accepted not persisted")
	}
}

// =====================================================================
// Wave 4: cross-area request
// =====================================================================

func TestP1P2_CrossAreaRequest(t *testing.T) {
	s, cleanup := setupP1P2(t)
	defer cleanup()

	// Wave 53 — use the shared WO fixture. Status defaults to
	// 'unassigned' + is_cross_area = FALSE, matching the original
	// query predicates.
	woID := ensureSharedWOID(t)
	branchID := pickFirst(t, s.pool, `SELECT id FROM identity.branches LIMIT 1`)

	s.c.do("POST", "/api/field/work-orders/"+woID+"/cross-area",
		map[string]any{
			"target_branch_id": branchID,
			"reason":           "test cross-area " + suffix(),
		}, nil, 200)

	var crossArea bool
	_ = s.pool.QueryRow(s.ctx,
		`SELECT is_cross_area FROM field.work_orders WHERE id=$1`,
		woID).Scan(&crossArea)
	if !crossArea {
		t.Fatalf("is_cross_area not flipped")
	}
}

// =====================================================================
// Wave 4: suggested pair
// =====================================================================

func TestP1P2_SuggestedPair(t *testing.T) {
	s, cleanup := setupP1P2(t)
	defer cleanup()

	woID := pickFirst(t, s.pool,
		`SELECT id FROM field.work_orders WHERE branch_id IS NOT NULL LIMIT 1`)

	// Reach the endpoint — it returns whatever's available, possibly
	// empty. We just want a 200 response shape.
	var out map[string]any
	s.c.do("GET", "/api/field/work-orders/"+woID+"/suggested-pair", nil, &out, 200)
	// Shape is {lead_senior?, observer_junior?} — both optional. We
	// just assert the response is a JSON object.
	if out == nil {
		t.Fatalf("suggested-pair returned nil object")
	}
}

// =====================================================================
// Wave 4: portal active-WO tech location
// =====================================================================

func TestP1P2_PortalActiveWOTechLocation(t *testing.T) {
	s, cleanup := setupP1P2(t)
	defer cleanup()

	custID := pickFirst(t, s.pool, `SELECT id FROM crm.customers ORDER BY created_at LIMIT 1`)
	portalTok := loginPortalForCustomer(t, s, custID)

	var out struct {
		HasActiveWO bool   `json:"has_active_wo"`
		WoID        string `json:"wo_id,omitempty"`
	}
	doPortal(t, portalTok, "GET", "/portal/active-wo/tech-location", nil, &out, 200)
	// Either has an active WO or doesn't — both are valid. We just
	// want the shape and a 200.
}

// =====================================================================
// Wave 4: portal KTP re-upload
// =====================================================================

func TestP1P2_PortalKTPReupload(t *testing.T) {
	s, cleanup := setupP1P2(t)
	defer cleanup()

	custID := pickFirst(t, s.pool, `SELECT id FROM crm.customers ORDER BY created_at LIMIT 1`)
	portalTok := loginPortalForCustomer(t, s, custID)

	var out struct {
		TicketNumber string `json:"ticket_number"`
	}
	doPortal(t, portalTok, "POST", "/portal/ktp",
		map[string]any{
			"object_url": "s3://test/ktp-" + suffix() + ".jpg",
			"notes":      "go test",
		}, &out, 201)
	if !strings.HasPrefix(out.TicketNumber, "KTP-") {
		t.Fatalf("ticket_number should start with KTP-: %q", out.TicketNumber)
	}
}

// =====================================================================
// Wave 3: HRIS sync state (stub provider)
// =====================================================================

func TestP1P2_HRISSyncState(t *testing.T) {
	s, cleanup := setupP1P2(t)
	defer cleanup()

	// Fire stub sync.
	s.c.do("POST", "/api/identity/hris/sync-now", map[string]any{}, nil, 200)

	// State row is present + recently-touched.
	var state struct {
		Items []struct {
			Provider    string `json:"provider"`
			RowsSynced  int    `json:"rows_synced"`
		} `json:"items"`
	}
	s.c.do("GET", "/api/identity/hris/sync-state", nil, &state, 200)
	if len(state.Items) == 0 {
		t.Fatalf("HRIS state empty after sync-now")
	}
}

// =====================================================================
// Wave 4: network RADIUS-by-customer
// =====================================================================

func TestP1P2_RADIUSByCustomer(t *testing.T) {
	s, cleanup := setupP1P2(t)
	defer cleanup()

	custID := pickFirst(t, s.pool, `SELECT id FROM crm.customers ORDER BY created_at LIMIT 1`)
	var out struct {
		State string `json:"state"`
	}
	s.c.do("GET", "/api/network/customers/"+custID+"/radius-state", nil, &out, 200)
	if out.State == "" {
		t.Fatalf("state should never be empty (defaults to 'unknown')")
	}
}

// =====================================================================
// Wave 4: warehouse stock dashboard
// =====================================================================

func TestP1P2_StockDashboard(t *testing.T) {
	s, cleanup := setupP1P2(t)
	defer cleanup()

	var summary struct {
		Items []map[string]any `json:"items"`
	}
	s.c.do("GET", "/api/warehouse/stock-dashboard", nil, &summary, 200)
	// Don't insist on rows — fresh DBs are valid — but if there are
	// rows, every row must carry the expected keys.
	for i, row := range summary.Items {
		for _, k := range []string{"id", "name", "code", "total_on_hand", "skus_in_stock", "skus_below_threshold"} {
			if _, ok := row[k]; !ok {
				t.Fatalf("row[%d] missing key %q: %+v", i, k, row)
			}
		}
	}

	var alerts struct {
		Items []map[string]any `json:"items"`
	}
	s.c.do("GET", "/api/warehouse/stock-dashboard/alerts", nil, &alerts, 200)
	for i, row := range alerts.Items {
		if _, ok := row["severity"]; !ok {
			t.Fatalf("alert[%d] missing severity: %+v", i, row)
		}
	}
}

// =====================================================================
// Wave 4: billing terminations consolidated
// =====================================================================

func TestP1P2_TerminationsConsolidated(t *testing.T) {
	s, cleanup := setupP1P2(t)
	defer cleanup()

	var out struct {
		Items []struct {
			Source string `json:"source"`
		} `json:"items"`
	}
	s.c.do("GET", "/api/billing/terminations/consolidated", nil, &out, 200)
	for i, r := range out.Items {
		if r.Source != "billing" && r.Source != "portal" {
			t.Fatalf("row[%d] unexpected source %q", i, r.Source)
		}
	}
}

// =====================================================================
// Wave 4: vendor benchmarks (per-SKU)
// =====================================================================

func TestP1P2_VendorBenchmarks(t *testing.T) {
	s, cleanup := setupP1P2(t)
	defer cleanup()

	var out struct {
		Items []map[string]any `json:"items"`
	}
	s.c.do("GET", "/api/enterprise/vendor-benchmarks", nil, &out, 200)
	// Each row must have sku + quotes + at least min or max once
	// quotes >= 1.
	for i, row := range out.Items {
		if _, ok := row["sku"]; !ok {
			t.Fatalf("row[%d] missing sku: %+v", i, row)
		}
	}
}

// =====================================================================
// Helpers — portal OTP login + per-customer-id token mint
// =====================================================================

func loginPortal(t *testing.T, s *p1p2Setup) string {
	t.Helper()
	custID := pickFirst(t, s.pool, `SELECT id FROM crm.customers ORDER BY created_at LIMIT 1`)
	return loginPortalForCustomer(t, s, custID)
}

func loginPortalForCustomer(t *testing.T, s *p1p2Setup, customerID string) string {
	t.Helper()
	var custNumber, phone string
	if err := s.pool.QueryRow(s.ctx,
		`SELECT customer_number, phone FROM crm.customers WHERE id=$1`,
		customerID).Scan(&custNumber, &phone); err != nil {
		t.Fatalf("lookup customer: %v", err)
	}
	last4 := phone
	if len(phone) > 4 {
		last4 = phone[len(phone)-4:]
	}
	// Demo mode returns the OTP in the response.
	var otpResp struct {
		DebugOTP string `json:"debug_otp"`
	}
	s.c.do("POST", "/portal/auth/otp-request",
		map[string]any{"customer_number": custNumber, "phone_last4": last4},
		&otpResp, 200)
	if otpResp.DebugOTP == "" {
		// CRM_PORTAL_OTP_DEMO=true is exported by the pr-e2e CI job
		// (Wave 48). An empty debug_otp here means crm-svc is wired
		// differently than CI expects — a real regression, not a skip
		// condition.
		t.Fatalf("portal OTP request returned no debug_otp — CRM_PORTAL_OTP_DEMO missing on crm-svc?")
	}
	var verify struct {
		AccessToken string `json:"access_token"`
	}
	s.c.do("POST", "/portal/auth/otp-verify",
		map[string]any{"customer_number": custNumber, "otp": otpResp.DebugOTP},
		&verify, 200)
	if verify.AccessToken == "" {
		t.Fatalf("portal token empty")
	}
	return verify.AccessToken
}

var (
	staffTokenCache   = make(map[string]string)
	staffTokenCacheMu sync.Mutex
)

// loginUser mints a staff token by email/password. Cached per-email so
// /auth/login isn't re-hit across tests (kicks rate-limit at ~10/30s).
func loginUser(t *testing.T, s *p1p2Setup, email, password string) string {
	t.Helper()
	staffTokenCacheMu.Lock()
	defer staffTokenCacheMu.Unlock()
	if tok, ok := staffTokenCache[email]; ok {
		return tok
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	s.c.do("POST", "/api/identity/auth/login",
		map[string]string{"email": email, "password": password},
		&out, 200)
	if out.AccessToken == "" {
		// seed-demo (run by the pr-e2e CI job) creates the 12 demo
		// users. An empty access_token means either the user wasn't
		// seeded or the shared demo password drifted — both are real
		// regressions.
		t.Fatalf("login %s returned empty access_token — seed-demo broken or password drifted", email)
	}
	staffTokenCache[email] = out.AccessToken
	return out.AccessToken
}

// doPortal + doWithToken use a fresh http.Client bound to the supplied
// token rather than the test's admin one.
func doPortal(t *testing.T, token, method, path string, in, out any, wantStatus int) {
	t.Helper()
	doWithToken(t, token, method, path, in, out, wantStatus)
}

func doWithToken(t *testing.T, token, method, path string, in, out any, wantStatus int) {
	t.Helper()
	// Reuse the existing client by swapping the token.
	c := newClient(t)
	c.token = token
	c.do(method, path, in, out, wantStatus)
}

// envOr fallback — duplicates the one in broadband_e2e_test.go to keep
// this file self-contained when run alone.
func init() {
	// Suppress unused-import linter complaint when only some tests run.
	_ = os.Getenv
	_ = fmt.Sprintf
}
