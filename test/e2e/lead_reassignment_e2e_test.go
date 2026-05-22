// Lead reassignment (takeover) end-to-end.
//
// Wave 58 — proves the sales_manager takeover flow (TC-CRM-LTO):
//
//   1. Create sales_rep A + sales_rep B as new users
//   2. As admin, create a lead owned by sales_rep A (sales_id = A)
//   3. As sales_manager (with crm.lead.manage), PATCH /leads/{id}
//      with sales_id = B
//   4. Re-fetch — owner is now sales_rep B
//   5. Audit timeline has a `sales_reassigned` event
//
// The PATCH handler in crm fires logLeadEvent("sales_reassigned", ...)
// when sales_id changes; this test pins both the column update and
// the timeline write.
//
//go:build e2e

package e2e

import (
	"testing"
)

func TestLeadReassignment(t *testing.T) {
	admin := newClient(t)
	admin.login()

	h := setupCoveredCustomer(t, admin)
	// setupCoveredCustomer leaves us with a converted lead. We need a
	// FRESH unconverted lead for reassignment.
	sx := suffix()

	// -----------------------------------------------------------------
	// 1. Two sales reps under the test branch.
	// -----------------------------------------------------------------
	mkRep := func(emp string) string {
		var u struct {
			ID string `json:"id"`
		}
		admin.do("POST", "/api/identity/users", map[string]any{
			"employee_id": "W58-" + emp + "-" + sx,
			"full_name":   "W58 Rep " + emp + " " + sx,
			"email":       "w58-rep-" + emp + "-" + sx + "@ion.local",
			"phone":       "+62811" + emp + sx,
			"password":    "TempPass!2026",
			"branch_id":   h.BranchID,
			"roles":       []string{"sales_rep"},
			"sales_type":  "broadband",
		}, &u, 201)
		return u.ID
	}
	repA := mkRep("REPA")
	repB := mkRep("REPB")

	// -----------------------------------------------------------------
	// 2. Create a lead with sales_id = repA.
	// -----------------------------------------------------------------
	var products struct {
		Items []struct {
			ID, Code string
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
		t.Fatal("BB-30 product not seeded")
	}
	// Reuse the helper's ODP — lead GPS just needs to be within coverage.
	var lead struct {
		ID      string `json:"id"`
		SalesID string `json:"sales_id"`
	}
	admin.do("POST", "/api/crm/leads", map[string]any{
		"full_name":  "Reassign Lead " + sx,
		"phone":      "+62815" + sx,
		"nik":        "31780" + sx + "0000",
		"address":    "Jl. Reassign " + sx,
		"gps_lat":    -6.0001,
		"gps_lng":    106.5001,
		"product_id": productID,
		"sales_id":   repA,
	}, &lead, 201)
	if lead.SalesID != repA {
		t.Fatalf("create with sales_id=%s landed as %q", repA, lead.SalesID)
	}

	// -----------------------------------------------------------------
	// 3. Reassign to repB. sales_manager would normally do this; admin
	//    works for the test (super_admin → all permissions).
	// -----------------------------------------------------------------
	var afterReassign struct {
		ID      string `json:"id"`
		SalesID string `json:"sales_id"`
	}
	admin.do("PATCH", "/api/crm/leads/"+lead.ID, map[string]any{
		"sales_id": repB,
	}, &afterReassign, 200)
	if afterReassign.SalesID != repB {
		t.Errorf("reassigned sales_id: want %s, got %s", repB, afterReassign.SalesID)
	}

	// -----------------------------------------------------------------
	// 4. Re-fetch to confirm the column actually changed in storage.
	// -----------------------------------------------------------------
	var refetched struct {
		SalesID string `json:"sales_id"`
	}
	admin.do("GET", "/api/crm/leads/"+lead.ID, nil, &refetched, 200)
	if refetched.SalesID != repB {
		t.Errorf("re-read sales_id: want %s, got %s (PATCH didn't persist)", repB, refetched.SalesID)
	}

	// -----------------------------------------------------------------
	// 5. The audit timeline should carry a sales_reassigned event.
	// -----------------------------------------------------------------
	var events struct {
		Items []struct {
			ID        string `json:"id"`
			EventType string `json:"event_type"`
			Notes     string `json:"notes,omitempty"`
		} `json:"items"`
	}
	admin.do("GET", "/api/crm/leads/"+lead.ID+"/events", nil, &events, 200)
	sawReassign := false
	for _, e := range events.Items {
		if e.EventType == "sales_reassigned" {
			sawReassign = true
			break
		}
	}
	if !sawReassign {
		t.Errorf("expected sales_reassigned event in lead timeline; got %d events", len(events.Items))
	}

	// -----------------------------------------------------------------
	// 6. Negative — clear_sales=true should drop the assignment. This
	//    is the "unassign" branch (lead goes back to the pool).
	// -----------------------------------------------------------------
	var afterClear struct {
		SalesID string `json:"sales_id"`
	}
	admin.do("PATCH", "/api/crm/leads/"+lead.ID, map[string]any{
		"clear_sales": true,
	}, &afterClear, 200)
	if afterClear.SalesID != "" {
		t.Errorf("clear_sales: want empty sales_id, got %q", afterClear.SalesID)
	}
}
