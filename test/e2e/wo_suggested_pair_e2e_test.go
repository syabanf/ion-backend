// WO auto-pair suggestion end-to-end (functional).
//
// Wave 59 — the existing p1p2 test for /work-orders/{id}/suggested-pair
// only asserts the response is a non-nil JSON object. It passed even
// when the handler had a typo'd table name (field.assignments vs
// field.wo_assignments) that made it silently return `{}` for every
// request. This test is the functional gate: it builds a WO + a pool
// of available techs in the same branch and asserts the response
// names SOMEONE.
//
//   1. setupCoveredCustomer — branch + ODP + customer + order
//   2. Create 2 senior + 2 junior technicians in that branch (so the
//      handler has candidates to pick from)
//   3. Create a fresh WO against the customer's order (no team yet)
//   4. GET /work-orders/{id}/suggested-pair
//   5. lead_senior + observer_junior both populated with one of our
//      seeded users; their grades match the slot
//
// Co-ships the typo fix in phase2_backlog.go's SQL.
//
//go:build e2e

package e2e

import (
	"testing"
)

func TestWOSuggestedPair(t *testing.T) {
	admin := newClient(t)
	admin.login()

	h := setupCoveredCustomer(t, admin)
	sx := suffix()

	// -----------------------------------------------------------------
	// 1. Seed a pool of senior + junior techs in the same branch.
	//    Without these in the branch, the handler returns {} regardless
	//    of WO state, so the assertion below would falsely "pass".
	// -----------------------------------------------------------------
	mkTech := func(emp, grade string) string {
		var u struct {
			ID string `json:"id"`
		}
		admin.do("POST", "/api/identity/users", map[string]any{
			"employee_id":      "W59-" + emp + "-" + sx,
			"full_name":        "W59 Tech " + emp + " " + sx,
			"email":            "w59-tech-" + emp + "-" + sx + "@ion.local",
			"phone":            "+62811W59" + emp,
			"password":         "TempPass!2026",
			"branch_id":        h.BranchID,
			"roles":            []string{"technician"},
			"technician_grade": grade,
		}, &u, 201)
		return u.ID
	}
	srA := mkTech("SRA", "senior")
	srB := mkTech("SRB", "senior")
	jrA := mkTech("JRA", "junior")
	jrB := mkTech("JRB", "junior")
	expectedSeniors := map[string]bool{srA: true, srB: true}
	expectedJuniors := map[string]bool{jrA: true, jrB: true}

	// -----------------------------------------------------------------
	// 2. Fresh WO — no team assigned. The suggestion engine looks for
	//    available (not currently on another in-progress WO) techs in
	//    the WO's branch.
	// -----------------------------------------------------------------
	var wo struct {
		ID string `json:"id"`
	}
	admin.do("POST", "/api/field/work-orders", map[string]any{
		"order_id": h.OrderID,
	}, &wo, 201)

	// -----------------------------------------------------------------
	// 3. Ask the engine for a pair.
	// -----------------------------------------------------------------
	var sp struct {
		Lead *struct {
			UserID   string `json:"user_id"`
			FullName string `json:"full_name"`
		} `json:"lead_senior,omitempty"`
		Observer *struct {
			UserID   string `json:"user_id"`
			FullName string `json:"full_name"`
		} `json:"observer_junior,omitempty"`
	}
	admin.do("GET", "/api/field/work-orders/"+wo.ID+"/suggested-pair", nil, &sp, 200)

	if sp.Lead == nil {
		t.Fatalf("suggested_pair returned no lead_senior — handler regression or no senior techs in branch?")
	}
	if !expectedSeniors[sp.Lead.UserID] {
		t.Errorf("lead_senior user_id %q not in our seeded pool %v", sp.Lead.UserID, expectedSeniors)
	}
	if sp.Observer == nil {
		t.Fatalf("suggested_pair returned no observer_junior — handler regression or no junior techs in branch?")
	}
	if !expectedJuniors[sp.Observer.UserID] {
		t.Errorf("observer_junior user_id %q not in our seeded pool %v", sp.Observer.UserID, expectedJuniors)
	}

	// -----------------------------------------------------------------
	// 4. Negative — pick the suggested senior, route + assign onto a
	//    NEW WO so they're now busy on an in_progress WO. The next
	//    suggestion call should NOT return them (the busy-check
	//    NOT EXISTS clause kicks in).
	// -----------------------------------------------------------------
	tlID := func() string {
		var u struct {
			ID string `json:"id"`
		}
		admin.do("POST", "/api/identity/users", map[string]any{
			"employee_id": "W59-TL-" + sx,
			"full_name":   "W59 TL " + sx,
			"email":       "w59-tl-" + sx + "@ion.local",
			"phone":       "+62811W59TL",
			"password":    "TempPass!2026",
			"branch_id":   h.BranchID,
			"roles":       []string{"team_leader"},
		}, &u, 201)
		return u.ID
	}()

	var team struct {
		ID string `json:"id"`
	}
	admin.do("POST", "/api/field/teams", map[string]any{
		"name":           "W59 Pair Team " + sx,
		"code":           "W59-PAIR-" + sx,
		"branch_id":      h.BranchID,
		"team_leader_id": tlID,
	}, &team, 201)
	admin.do("POST", "/api/field/teams/"+team.ID+"/members",
		map[string]any{"user_id": sp.Lead.UserID, "grade": "senior"}, nil, 201)
	admin.do("POST", "/api/field/teams/"+team.ID+"/members",
		map[string]any{"user_id": sp.Observer.UserID, "grade": "junior"}, nil, 201)

	// Bind the busy WO + drive to in_progress so the NOT EXISTS clause
	// fires.
	admin.do("POST", "/api/field/work-orders/"+wo.ID+"/route",
		map[string]any{"team_id": team.ID}, nil, 200)
	admin.do("POST", "/api/field/work-orders/"+wo.ID+"/assign", map[string]any{
		"lead_id":        sp.Lead.UserID,
		"lead_grade":     "senior",
		"observer_id":    sp.Observer.UserID,
		"observer_grade": "junior",
	}, nil, 200)
	admin.do("POST", "/api/field/work-orders/"+wo.ID+"/status",
		map[string]any{"status": "in_progress"}, nil, 200)

	// Spin up a fresh WO + ask again — the previously-suggested pair
	// is now busy, so the engine should propose the OTHER senior /
	// junior from our pool.
	var wo2 struct {
		ID string `json:"id"`
	}
	admin.do("POST", "/api/field/work-orders", map[string]any{
		"order_id": h.OrderID,
	}, &wo2, 201)

	var sp2 struct {
		Lead *struct {
			UserID string `json:"user_id"`
		} `json:"lead_senior,omitempty"`
		Observer *struct {
			UserID string `json:"user_id"`
		} `json:"observer_junior,omitempty"`
	}
	admin.do("GET", "/api/field/work-orders/"+wo2.ID+"/suggested-pair", nil, &sp2, 200)
	if sp2.Lead != nil && sp2.Lead.UserID == sp.Lead.UserID {
		t.Errorf("suggestion returned busy senior %s a second time — NOT EXISTS guard broken", sp.Lead.UserID)
	}
	if sp2.Observer != nil && sp2.Observer.UserID == sp.Observer.UserID {
		t.Errorf("suggestion returned busy junior %s a second time — NOT EXISTS guard broken", sp.Observer.UserID)
	}
}
