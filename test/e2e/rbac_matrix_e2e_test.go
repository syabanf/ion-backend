// RBAC matrix end-to-end — proves Wave 47's seed migration actually
// produced a working permission catalog at runtime, not just rows.
//
// The migration-smoke job in CI confirms the row counts (≥17 roles,
// ≥100 permissions, ≥150 role_permissions, super_admin → all). This
// test confirms the FUNCTIONAL behaviour: when a user logs in as
// $role, the authorization check on each gated endpoint either lets
// them through (200/201/404) or refuses (403). Drift between the
// permission catalog and the handler-side `RequirePermission` strings
// is the silent regression we're catching here.
//
// We don't try to cover every (role, endpoint) cell — that's 17×100
// probes. We test the load-bearing ones: a few read endpoints per
// module + the deny case for a clearly out-of-scope role.
//
//go:build e2e

package e2e

import (
	"strings"
	"testing"
)

// allowedStatusesForAllow — when a role IS supposed to have access, we
// allow any 2xx OR 404 (the resource may not exist; we only care that
// it's not a permission rejection).
func isAllowedResponse(status int) bool {
	if status >= 200 && status < 300 {
		return true
	}
	if status == 404 {
		return true
	}
	return false
}

type probe struct {
	role        string // seed-demo email prefix, e.g. "sales" for sales@ion.local
	email       string
	method      string
	path        string
	wantAllowed bool // true = expect access; false = expect 403
}

func TestRBACMatrix(t *testing.T) {
	// The probe table is intentionally compact — 4 reads per role plus
	// one cross-role deny — to cover the integration without explosion.
	// Add a row when a permission landing in a new module's UI is added.
	probes := []probe{
		// sales_rep → can read CRM, cannot read identity / billing / warehouse
		{role: "sales", email: "sales@ion.local", method: "GET", path: "/api/crm/leads", wantAllowed: true},
		{role: "sales", email: "sales@ion.local", method: "GET", path: "/api/crm/products", wantAllowed: true},
		{role: "sales", email: "sales@ion.local", method: "GET", path: "/api/identity/users", wantAllowed: false},
		{role: "sales", email: "sales@ion.local", method: "GET", path: "/api/billing/invoices", wantAllowed: false},
		{role: "sales", email: "sales@ion.local", method: "GET", path: "/api/warehouse/items", wantAllowed: false},

		// technician → can read field WO + tech location, cannot read CRM or billing
		{role: "tech", email: "tech@ion.local", method: "GET", path: "/api/field/work-orders", wantAllowed: true},
		{role: "tech", email: "tech@ion.local", method: "GET", path: "/api/crm/leads", wantAllowed: false},
		{role: "tech", email: "tech@ion.local", method: "GET", path: "/api/billing/invoices", wantAllowed: false},
		{role: "tech", email: "tech@ion.local", method: "GET", path: "/api/identity/users", wantAllowed: false},

		// noc → can read network topology + WO + field tickets, cannot read billing or warehouse
		{role: "noc", email: "noc@ion.local", method: "GET", path: "/api/network/nodes", wantAllowed: true},
		{role: "noc", email: "noc@ion.local", method: "GET", path: "/api/field/work-orders", wantAllowed: true},
		{role: "noc", email: "noc@ion.local", method: "GET", path: "/api/billing/invoices", wantAllowed: false},
		{role: "noc", email: "noc@ion.local", method: "GET", path: "/api/warehouse/items", wantAllowed: false},

		// warehouse_manager → can read warehouse/stock, cannot read CRM or billing
		{role: "wh-mgr", email: "wh-mgr@ion.local", method: "GET", path: "/api/warehouse/items", wantAllowed: true},
		{role: "wh-mgr", email: "wh-mgr@ion.local", method: "GET", path: "/api/warehouse/stock-dashboard", wantAllowed: true},
		{role: "wh-mgr", email: "wh-mgr@ion.local", method: "GET", path: "/api/crm/leads", wantAllowed: false},
		{role: "wh-mgr", email: "wh-mgr@ion.local", method: "GET", path: "/api/billing/invoices", wantAllowed: false},

		// finance_staff → can read billing, cannot edit identity users or run cycles
		{role: "fin-staff", email: "fin-staff@ion.local", method: "GET", path: "/api/billing/invoices", wantAllowed: true},
		{role: "fin-staff", email: "fin-staff@ion.local", method: "GET", path: "/api/billing/commissions", wantAllowed: true},
		{role: "fin-staff", email: "fin-staff@ion.local", method: "GET", path: "/api/identity/users", wantAllowed: false},
		{role: "fin-staff", email: "fin-staff@ion.local", method: "POST", path: "/api/billing/cycles/run", wantAllowed: false},

		// ops admin → can manage identity + read everything
		{role: "ops", email: "ops@ion.local", method: "GET", path: "/api/identity/users", wantAllowed: true},
		{role: "ops", email: "ops@ion.local", method: "GET", path: "/api/identity/branches", wantAllowed: true},
		{role: "ops", email: "ops@ion.local", method: "GET", path: "/api/admin/config", wantAllowed: true},
	}

	// Pre-authenticate one client per distinct email.
	clients := map[string]*client{}
	for _, p := range probes {
		if _, ok := clients[p.email]; !ok {
			clients[p.email] = newClientAs(t, p.email)
		}
	}

	for _, p := range probes {
		name := p.role + "_" + strings.ReplaceAll(strings.Trim(p.path, "/"), "/", "_")
		if !p.wantAllowed {
			name += "_DENY"
		}
		t.Run(name, func(t *testing.T) {
			c := clients[p.email]
			// We don't use c.do here because we want to inspect the
			// status without auto-failing on non-2xx. Build the request
			// inline.
			req, _ := newRequest(t, p.method, baseURL+p.path, nil)
			req.Header.Set("authorization", "Bearer "+c.token)
			resp, err := c.http.Do(req)
			if err != nil {
				t.Fatalf("%s %s: %v", p.method, p.path, err)
			}
			defer resp.Body.Close()
			if p.wantAllowed && !isAllowedResponse(resp.StatusCode) {
				t.Fatalf("%s %s as %s: want allowed (2xx/404), got %d",
					p.method, p.path, p.role, resp.StatusCode)
			}
			if !p.wantAllowed && resp.StatusCode != 403 {
				t.Fatalf("%s %s as %s: want 403, got %d",
					p.method, p.path, p.role, resp.StatusCode)
			}
		})
	}
}
