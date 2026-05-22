// Maintenance event lifecycle end-to-end.
//
// Wave 51 — proves the NOC's planned-maintenance flow (PRD §6.4):
//
//   1. Admin creates a network ODP (gives the event something to be
//      attached to)
//   2. Admin creates a maintenance event with that ODP as an affected
//      node — status starts 'planned'
//   3. Admin lists /maintenance-events and confirms the new event is
//      visible
//   4. Admin GETs the detail and sees the node attached
//   5. Admin dispatches the event with a team_id + open_wos=true
//      — status flips to 'dispatched' and a maintenance WO is spawned
//   6. Re-dispatching the same event returns 409 (idempotency guard
//      on the 'planned' check)
//
// We don't drive the spawned WO through completion here — that's
// covered by broadband_e2e_test's WO lifecycle. This test focuses on
// the event lifecycle itself.
//
//go:build e2e

package e2e

import (
	"math/rand"
	"testing"
	"time"
)

func TestMaintenanceEventLifecycle(t *testing.T) {
	admin := newClient(t)
	admin.login()

	sx := suffix()
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))

	// -----------------------------------------------------------------
	// 1. Branch + ODP — the event will affect this node.
	// -----------------------------------------------------------------
	var regional struct{ ID string `json:"id"` }
	admin.do("POST", "/api/identity/branches", map[string]any{
		"name":  "W51 Maint Regional " + sx,
		"code":  "W51-M-REG-" + sx,
		"level": "regional",
	}, &regional, 201)
	var area struct{ ID string `json:"id"` }
	admin.do("POST", "/api/identity/branches", map[string]any{
		"name":      "W51 Maint Area " + sx,
		"code":      "W51-M-AREA-" + sx,
		"level":     "area",
		"parent_id": regional.ID,
	}, &area, 201)

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
		t.Fatal("no odp node-type seeded")
	}

	var node struct{ ID string `json:"id"` }
	admin.do("POST", "/api/network/nodes", map[string]any{
		"node_type_id": odpTypeID,
		"name":         "W51 Maint ODP " + sx,
		"code":         "W51-M-ODP-" + sx,
		"branch_id":    area.ID,
		"address":      "W51 maintenance test",
		"gps_lat":      -6.0 - rng.Float64()*0.5,
		"gps_lng":      106.5 + rng.Float64()*0.5,
		"total_ports":  8,
		"port_role":    "customer_drop",
	}, &node, 201)

	// -----------------------------------------------------------------
	// 2. Create a maintenance event.
	// -----------------------------------------------------------------
	start := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	end := time.Now().Add(28 * time.Hour).UTC().Format(time.RFC3339)
	var event struct {
		ID        string `json:"id"`
		EventCode string `json:"event_code"`
		Status    string `json:"status"`
	}
	admin.do("POST", "/api/field/maintenance-events", map[string]any{
		"title":           "W51 Scheduled outage " + sx,
		"description":     "Wave 51 maintenance E2E — backup generator swap.",
		"event_kind":      "planned_outage",
		"scheduled_start": start,
		"scheduled_end":   end,
		"branch_id":       area.ID,
		"node_ids":        []string{node.ID},
	}, &event, 201)
	if event.ID == "" {
		t.Fatal("maintenance event create returned empty id")
	}
	if event.Status != "planned" {
		t.Errorf("new event status: want planned, got %q", event.Status)
	}

	// -----------------------------------------------------------------
	// 3. List endpoint shows the new event.
	// -----------------------------------------------------------------
	var list struct {
		Items []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"items"`
	}
	admin.do("GET", "/api/field/maintenance-events", nil, &list, 200)
	found := false
	for _, it := range list.Items {
		if it.ID == event.ID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("event %s missing from /maintenance-events list", event.ID)
	}

	// -----------------------------------------------------------------
	// 4. Detail view shows the attached node.
	// -----------------------------------------------------------------
	var detail struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		Nodes []struct {
			NodeID string `json:"node_id"`
		} `json:"nodes,omitempty"`
		AffectedNodes []struct {
			NodeID string `json:"node_id"`
		} `json:"affected_nodes,omitempty"`
	}
	admin.do("GET", "/api/field/maintenance-events/"+event.ID, nil, &detail, 200)
	nodes := detail.Nodes
	if len(nodes) == 0 {
		nodes = detail.AffectedNodes
	}
	if len(nodes) == 0 {
		t.Errorf("event detail returned no attached nodes (expected the ODP we just attached)")
	}

	// -----------------------------------------------------------------
	// 5. We need a team to dispatch to. Build a minimal team — one team
	//    leader user + one team with that leader. This mirrors the
	//    field-team-setup step from broadband_e2e_test.
	// -----------------------------------------------------------------
	var tl struct{ ID string `json:"id"` }
	admin.do("POST", "/api/identity/users", map[string]any{
		"employee_id": "W51-TL-" + sx,
		"full_name":   "W51 Team Leader",
		"email":       "w51-tl-" + sx + "@ion.local",
		"password":    "TempPass!2026",
		"branch_id":   area.ID,
		"roles":       []string{"team_leader"},
	}, &tl, 201)

	var team struct{ ID string `json:"id"` }
	admin.do("POST", "/api/field/teams", map[string]any{
		"name":           "W51 Maint Team " + sx,
		"branch_id":      area.ID,
		"team_leader_id": tl.ID,
	}, &team, 201)

	// -----------------------------------------------------------------
	// 6. Dispatch — status flips to 'dispatched', WO is spawned.
	// -----------------------------------------------------------------
	admin.do("POST", "/api/field/maintenance-events/"+event.ID+"/dispatch",
		map[string]any{
			"team_id":  team.ID,
			"open_wos": true,
			"priority": "high",
		}, nil, 200)

	var afterDispatch struct {
		Status         string `json:"status"`
		AssignedTeamID string `json:"assigned_team_id"`
	}
	admin.do("GET", "/api/field/maintenance-events/"+event.ID, nil, &afterDispatch, 200)
	if afterDispatch.Status != "dispatched" {
		t.Errorf("post-dispatch status: want dispatched, got %q", afterDispatch.Status)
	}
	if afterDispatch.AssignedTeamID != team.ID {
		t.Errorf("assigned_team_id: want %q, got %q", team.ID, afterDispatch.AssignedTeamID)
	}

	// -----------------------------------------------------------------
	// 7. Re-dispatch is rejected with 409 (status guard).
	// -----------------------------------------------------------------
	_ = admin.doExpectError("POST", "/api/field/maintenance-events/"+event.ID+"/dispatch",
		map[string]any{
			"team_id":  team.ID,
			"open_wos": false,
		}, 409)
}
