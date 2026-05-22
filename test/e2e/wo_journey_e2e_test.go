// WO journey timestamps end-to-end.
//
// Wave 59 — proves the PRD §6.1 "Start Journey" + "Arrived" timestamps
// fire correctly. These are the audit signals the dispatcher uses to
// measure travel time vs on-site time.
//
//   1. Build a WO + assign so it's in 'assigned' state
//   2. POST /work-orders/{id}/journey/start → 200, journey_started_at
//      populated on the WO detail
//   3. POST /work-orders/{id}/journey/arrived → 200, arrived_at
//      populated
//   4. Re-POST /journey/start → 200 (idempotent — COALESCE keeps the
//      first timestamp) and the original journey_started_at is
//      preserved
//   5. Negative — call /journey/start on a freshly-created (unassigned)
//      WO → 409 (the handler requires status IN ('assigned','dispatched'))
//
//go:build e2e

package e2e

import (
	"testing"
	"time"
)

func TestWOJourneyTimestamps(t *testing.T) {
	admin := newClient(t)
	admin.login()

	h := setupCoveredCustomer(t, admin)
	sx := suffix()

	// -----------------------------------------------------------------
	// 1. Team + techs + WO routed + assigned (status = 'assigned').
	// -----------------------------------------------------------------
	mkUser := func(prefix, role, grade string) string {
		body := map[string]any{
			"employee_id": prefix + "-" + sx,
			"full_name":   "W59J " + prefix + " " + sx,
			"email":       "w59j-" + prefix + "-" + sx + "@ion.local",
			"phone":       "+62811W59J" + prefix,
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
	tlID := mkUser("JTL", "team_leader", "")
	srID := mkUser("JSR", "technician", "senior")
	jrID := mkUser("JJR", "technician", "junior")

	var team struct {
		ID string `json:"id"`
	}
	admin.do("POST", "/api/field/teams", map[string]any{
		"name":           "W59 Journey Team " + sx,
		"code":           "W59-JNY-" + sx,
		"branch_id":      h.BranchID,
		"team_leader_id": tlID,
	}, &team, 201)
	admin.do("POST", "/api/field/teams/"+team.ID+"/members",
		map[string]any{"user_id": srID, "grade": "senior"}, nil, 201)
	admin.do("POST", "/api/field/teams/"+team.ID+"/members",
		map[string]any{"user_id": jrID, "grade": "junior"}, nil, 201)

	mkWO := func() string {
		var wo struct {
			ID string `json:"id"`
		}
		admin.do("POST", "/api/field/work-orders", map[string]any{
			"order_id": h.OrderID,
		}, &wo, 201)
		return wo.ID
	}

	// Primary WO — assigned state via route + assign.
	woID := mkWO()
	admin.do("POST", "/api/field/work-orders/"+woID+"/route",
		map[string]any{"team_id": team.ID}, nil, 200)
	admin.do("POST", "/api/field/work-orders/"+woID+"/assign", map[string]any{
		"lead_id":        srID,
		"lead_grade":     "senior",
		"observer_id":    jrID,
		"observer_grade": "junior",
	}, nil, 200)

	// -----------------------------------------------------------------
	// 2. journey/start populates journey_started_at.
	// -----------------------------------------------------------------
	admin.do("POST", "/api/field/work-orders/"+woID+"/journey/start", nil, nil, 200)

	var afterStart struct {
		JourneyStartedAt *string `json:"journey_started_at,omitempty"`
		ArrivedAt        *string `json:"arrived_at,omitempty"`
	}
	admin.do("GET", "/api/field/work-orders/"+woID, nil, &afterStart, 200)
	if afterStart.JourneyStartedAt == nil || *afterStart.JourneyStartedAt == "" {
		t.Fatalf("journey_started_at not populated after /journey/start")
	}
	if afterStart.ArrivedAt != nil && *afterStart.ArrivedAt != "" {
		t.Errorf("arrived_at populated prematurely: %q", *afterStart.ArrivedAt)
	}
	originalStart := *afterStart.JourneyStartedAt

	// -----------------------------------------------------------------
	// 3. Re-POST /journey/start is idempotent — COALESCE keeps the
	//    first timestamp. Catches a regression where someone removes
	//    the COALESCE and tech double-tapping the button overwrites
	//    the original departure time.
	// -----------------------------------------------------------------
	time.Sleep(50 * time.Millisecond) // ensure NOW() would differ if not COALESCED
	admin.do("POST", "/api/field/work-orders/"+woID+"/journey/start", nil, nil, 200)
	var afterReStart struct {
		JourneyStartedAt *string `json:"journey_started_at,omitempty"`
	}
	admin.do("GET", "/api/field/work-orders/"+woID, nil, &afterReStart, 200)
	if afterReStart.JourneyStartedAt == nil || *afterReStart.JourneyStartedAt != originalStart {
		t.Errorf("journey_started_at overwritten on re-call: was %q, now %v",
			originalStart, afterReStart.JourneyStartedAt)
	}

	// -----------------------------------------------------------------
	// 4. journey/arrived populates arrived_at.
	// -----------------------------------------------------------------
	admin.do("POST", "/api/field/work-orders/"+woID+"/journey/arrived", nil, nil, 200)

	var afterArrived struct {
		JourneyStartedAt *string `json:"journey_started_at,omitempty"`
		ArrivedAt        *string `json:"arrived_at,omitempty"`
	}
	admin.do("GET", "/api/field/work-orders/"+woID, nil, &afterArrived, 200)
	if afterArrived.ArrivedAt == nil || *afterArrived.ArrivedAt == "" {
		t.Fatalf("arrived_at not populated after /journey/arrived")
	}
	if afterArrived.JourneyStartedAt == nil || *afterArrived.JourneyStartedAt != originalStart {
		t.Errorf("journey_started_at drifted after /arrived: was %q, now %v",
			originalStart, afterArrived.JourneyStartedAt)
	}

	// -----------------------------------------------------------------
	// 5. Negative — /journey/start on an unassigned WO returns 409
	//    (handler requires status IN ('assigned','dispatched')).
	// -----------------------------------------------------------------
	freshWO := mkWO()
	got := admin.statusOnly("POST", "/api/field/work-orders/"+freshWO+"/journey/start")
	if got != 409 {
		t.Errorf("/journey/start on unassigned WO: want 409, got %d", got)
	}
}
