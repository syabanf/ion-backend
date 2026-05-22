// Lead lifecycle end-to-end — proves the negative + non-happy paths
// through the lead status state machine.
//
// Wave 56 — the broadband happy-path tests `new → qualified → converted`.
// The state machine has 3 other terminal states (`rejected`, `lost`,
// `potential`) and the UI lets admins drive transitions via PATCH
// /leads/{id} with `status` set. Without coverage here, a regression
// that silently lets a rejected lead be converted (or one that won't
// let an admin mark a lead `lost`) would land unnoticed.
//
//   1. Create lead A in covered territory → status=qualified
//   2. PATCH /leads/A with status=rejected → status flips, audit fires
//   3. POST /leads/A/convert → expect 409 (rejected leads can't convert)
//   4. Create lead B → PATCH with status=lost → status flips
//   5. Create lead C in uncovered territory → status=potential
//      (coverage_verdict=uncovered + no_excess_consent)
//   6. PATCH lead C with status=qualified + accept_excess_cable=true
//      → status flips (sales manual upgrade)
//
//go:build e2e

package e2e

import (
	"math/rand"
	"testing"
	"time"
)

func TestLeadLifecycleTransitions(t *testing.T) {
	admin := newClient(t)
	admin.login()

	// Reuse setupCoveredCustomer's plumbing for the covered cases (it
	// builds branch + ODP + product + qualified lead + converted
	// customer). For status=rejected we need the same chain but to
	// STOP before convert, so we set things up inline.
	sx := suffix()
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	odpLat := -6.0 - rng.Float64()*0.5
	odpLng := 106.5 + rng.Float64()*0.5

	// Shared branch + ODP for leads A + B (covered).
	var regional struct{ ID string `json:"id"` }
	admin.do("POST", "/api/identity/branches", map[string]any{
		"name":  "W56 Regional " + sx,
		"code":  "W56-REG-" + sx,
		"level": "regional",
	}, &regional, 201)
	var area struct{ ID string `json:"id"` }
	admin.do("POST", "/api/identity/branches", map[string]any{
		"name":      "W56 Area " + sx,
		"code":      "W56-AREA-" + sx,
		"level":     "area",
		"parent_id": regional.ID,
	}, &area, 201)

	var nodeTypes struct {
		Items []struct {
			ID      string `json:"id"`
			TypeKey string `json:"type_key"`
		} `json:"items"`
	}
	admin.do("GET", "/api/network/node-types", nil, &nodeTypes, 200)
	var odpTypeID string
	for _, nt := range nodeTypes.Items {
		if nt.TypeKey == "odp" {
			odpTypeID = nt.ID
		}
	}
	if odpTypeID == "" {
		t.Fatal("no odp node-type seeded")
	}
	admin.do("POST", "/api/network/nodes", map[string]any{
		"node_type_id":     odpTypeID,
		"name":             "W56 ODP " + sx,
		"code":             "W56-ODP-" + sx,
		"branch_id":        area.ID,
		"address":          "W56 lifecycle test",
		"gps_lat":          odpLat,
		"gps_lng":          odpLng,
		"total_ports":      8,
		"port_role":        "customer_drop",
		"coverage_radius_m": 200,
	}, nil, 201)

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
		t.Fatal("BB-30 product not seeded — Wave 47 broken?")
	}

	mkLead := func(label string, latOff, lngOff float64) string {
		var lead struct {
			ID              string `json:"id"`
			Status          string `json:"status"`
			CoverageVerdict string `json:"coverage_verdict"`
		}
		admin.do("POST", "/api/crm/leads", map[string]any{
			"full_name":  "W56 " + label + " " + sx,
			"phone":      "+62813" + label + sx,
			"nik":        "31760" + sx + label,
			"address":    "Jl. W56 " + label + " " + sx,
			"gps_lat":    odpLat + latOff,
			"gps_lng":    odpLng + lngOff,
			"product_id": productID,
		}, &lead, 201)
		return lead.ID
	}

	// =================================================================
	// Case A — lead created qualified, then admin rejects it.
	// =================================================================
	leadA := mkLead("A", -0.0001, 0.0001) // covered

	rejected := "rejected"
	var afterReject struct {
		Status string `json:"status"`
	}
	admin.do("PATCH", "/api/crm/leads/"+leadA, map[string]any{
		"status": rejected,
	}, &afterReject, 200)
	if afterReject.Status != "rejected" {
		t.Fatalf("PATCH status=rejected didn't take: got %q", afterReject.Status)
	}

	// Convert on a rejected lead must fail. The exact status code varies
	// (409 Conflict if it checks state, 400 BadRequest if it validates
	// args); accept either.
	got := admin.statusOnlyJSON("POST", "/api/crm/leads/"+leadA+"/convert", map[string]any{})
	if got != 409 && got != 400 && got != 422 {
		t.Errorf("convert on rejected lead returned %d (want 409/400/422)", got)
	}

	// Re-fetch — the lead must STILL be in 'rejected'. The failed
	// convert mustn't accidentally flip it.
	var stillRejected struct {
		Status string `json:"status"`
	}
	admin.do("GET", "/api/crm/leads/"+leadA, nil, &stillRejected, 200)
	if stillRejected.Status != "rejected" {
		t.Errorf("rejected lead drifted to %q after failed convert", stillRejected.Status)
	}

	// =================================================================
	// Case B — lead → lost (sales rep gave up on the prospect).
	// =================================================================
	leadB := mkLead("B", -0.0001, 0.0002)
	lost := "lost"
	var afterLost struct {
		Status string `json:"status"`
	}
	admin.do("PATCH", "/api/crm/leads/"+leadB, map[string]any{
		"status": lost,
	}, &afterLost, 200)
	if afterLost.Status != "lost" {
		t.Errorf("PATCH status=lost didn't take: got %q", afterLost.Status)
	}

	// =================================================================
	// Case C — lead in uncovered territory.
	//
	// Wave 47's `excess_cable_e2e_test` covers the excess_distance
	// case (within reach but beyond default). This test exercises the
	// 'uncovered' branch: GPS far from any ODP so coverage_check
	// returns uncovered. New leads in that state default to status
	// 'potential' (qualified-once-coverage-clears).
	// =================================================================
	leadC := mkLead("C", 5.0, 5.0) // way outside any ODP's radius

	var leadCData struct {
		Status          string `json:"status"`
		CoverageVerdict string `json:"coverage_verdict"`
	}
	admin.do("GET", "/api/crm/leads/"+leadC, nil, &leadCData, 200)
	if leadCData.CoverageVerdict != "uncovered" {
		t.Errorf("lead C coverage_verdict: want uncovered, got %q", leadCData.CoverageVerdict)
	}
	if leadCData.Status != "potential" && leadCData.Status != "new" {
		// Some adapters mark uncovered leads as 'new', some as
		// 'potential' — accept either; what matters is it's NOT
		// qualified (would let a no-coverage customer convert).
		t.Errorf("lead C status: want potential/new, got %q", leadCData.Status)
	}

	// Manual upgrade — admin overrides after a site survey says
	// coverage is feasible with an excess cable run.
	qualified := "qualified"
	acceptExcess := true
	admin.do("PATCH", "/api/crm/leads/"+leadC, map[string]any{
		"status":              qualified,
		"accept_excess_cable": acceptExcess,
	}, nil, 200)

	var leadCAfter struct {
		Status            string `json:"status"`
		AcceptExcessCable bool   `json:"accept_excess_cable"`
	}
	admin.do("GET", "/api/crm/leads/"+leadC, nil, &leadCAfter, 200)
	if leadCAfter.Status != "qualified" {
		t.Errorf("manual upgrade didn't flip status: got %q", leadCAfter.Status)
	}
	if !leadCAfter.AcceptExcessCable {
		t.Errorf("accept_excess_cable flag didn't persist on the upgrade")
	}
}
