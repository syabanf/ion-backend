// Per-customer schema override end-to-end.
//
// Wave 50 — the platform's schema_definitions table holds DEFAULT
// schemas for billing/commission/suspension/service (Wave 47 seeds).
// Operators can override a specific customer's effective schema via
// PUT /api/admin/customer-schemas/{customer_id}/{kind}. This test
// proves:
//
//   1. setupCoveredCustomer creates the target customer
//   2. GET resolved schema initially returns the DEFAULT for each kind
//   3. PUT a billing override with a custom grace_days value
//   4. GET resolved schema now reflects the override
//   5. DELETE the override → GET returns the DEFAULT again
//
// Catches drift between the schema-builder UI's PUT shape and what
// the backend stores. Without this, an admin save could silently
// no-op and the customer would still get the default policy.
//
//go:build e2e

package e2e

import (
	"testing"
)

func TestCustomerSchemaOverride(t *testing.T) {
	admin := newClient(t)
	admin.login()

	h := setupCoveredCustomer(t, admin)

	// -----------------------------------------------------------------
	// 1. Read the resolved view BEFORE any override. Expect DEFAULT for
	//    the billing kind.
	// -----------------------------------------------------------------
	var before struct {
		Kinds map[string]struct {
			Source string                 `json:"source"` // "default" or "override"
			Body   map[string]any         `json:"body"`
		} `json:"kinds"`
	}
	admin.do("GET", "/api/admin/customer-schemas/"+h.CustomerID, nil, &before, 200)
	billingBefore, ok := before.Kinds["billing"]
	if !ok {
		// If this fires, either the customer-schemas adapter shape
		// has drifted from `kinds.<code>` to something else, or the
		// 0035 seed migration didn't land a 'billing' default. Both
		// are real regressions, hence Fatal rather than Skip.
		t.Fatalf("customer-schemas/{id} returned no 'billing' kind — adapter shape or seed broken (got kinds=%v)",
			keysOf(before.Kinds))
	}
	if billingBefore.Source != "default" {
		t.Errorf("before override: billing source want 'default', got %q", billingBefore.Source)
	}

	// -----------------------------------------------------------------
	// 2. Write an override that bumps grace_days. The override body is
	//    a partial — only fields set here win over the default. The
	//    backend handles the merge.
	// -----------------------------------------------------------------
	const overrideGraceDays = 7
	admin.do("PUT", "/api/admin/customer-schemas/"+h.CustomerID+"/billing",
		map[string]any{
			"body": map[string]any{
				"grace_days": overrideGraceDays,
				"note":       "Wave 50 — promo customer, extended grace.",
			},
		}, nil, 200)

	// -----------------------------------------------------------------
	// 3. GET resolved view again. billing.source flips to "override"
	//    and the grace_days reflects our value.
	// -----------------------------------------------------------------
	var after struct {
		Kinds map[string]struct {
			Source string         `json:"source"`
			Body   map[string]any `json:"body"`
		} `json:"kinds"`
	}
	admin.do("GET", "/api/admin/customer-schemas/"+h.CustomerID, nil, &after, 200)
	billingAfter := after.Kinds["billing"]
	if billingAfter.Source != "override" {
		t.Errorf("after PUT: billing source want 'override', got %q", billingAfter.Source)
	}
	got := billingAfter.Body["grace_days"]
	// JSON numbers decode to float64; compare via type assertion.
	if g, ok := got.(float64); !ok || int(g) != overrideGraceDays {
		t.Errorf("grace_days override didn't land: got %v (type %T), want %d",
			got, got, overrideGraceDays)
	}

	// -----------------------------------------------------------------
	// 4. DELETE the override → source returns to "default".
	// -----------------------------------------------------------------
	admin.do("DELETE", "/api/admin/customer-schemas/"+h.CustomerID+"/billing", nil, nil, 204)

	var afterDel struct {
		Kinds map[string]struct {
			Source string `json:"source"`
		} `json:"kinds"`
	}
	admin.do("GET", "/api/admin/customer-schemas/"+h.CustomerID, nil, &afterDel, 200)
	if afterDel.Kinds["billing"].Source != "default" {
		t.Errorf("after DELETE: billing source want 'default', got %q",
			afterDel.Kinds["billing"].Source)
	}
}
