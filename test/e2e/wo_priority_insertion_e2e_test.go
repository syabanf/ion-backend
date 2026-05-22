// Mid-schedule priority insertion end-to-end.
//
// Wave 58 — proves the TC-WO-PRIORITY flow: an emergency installation
// or repair gets dropped into a tech's day mid-schedule. The tech sees
// the insertion in /priority-insertions/mine, accepts via
// /priority-insertions/{id}/respond, and the insertion's accepted flag
// flips.
//
//   1. setupWOReadyForNOC (helper) — gives us an in-progress WO + a
//      seeded senior tech (w.SeniorID)
//   2. Admin POSTs /work-orders/{id}/priority-insert targeting the
//      senior tech with a reason
//   3. Senior tech logs in (we re-login using their seed-demo creds)
//   4. Senior GETs /priority-insertions/mine → sees the insertion
//   5. Senior POSTs /priority-insertions/{id}/respond with accepted=true
//   6. Subsequent /mine call no longer returns it (accepted IS NOT NULL
//      filter)
//
// The handler in phase2_backlog.go is a thin INSERT; this test is the
// primary functional assertion that the table + filter + respond
// endpoint all work together.
//
//go:build e2e

package e2e

import (
	"context"
	"testing"
)

func TestWOPriorityInsertion(t *testing.T) {
	admin := newClient(t)
	admin.login()

	// We need a WO + a tech we can re-authenticate as. setupWOReadyForNOC
	// creates a senior tech (w.SeniorID) but with a temp password we
	// don't easily know. Use the existing seed-demo tech account
	// instead — ensureWOAssignedToTech (p1p2_endpoints_test.go) already
	// wires tech@ion.local as the lead on a real WO.
	woID := ensureWOAssignedToTech(t)

	// Look up tech@ion.local's user ID via the shared pool (p1p2 helper).
	var techID string
	if err := sharedPool.QueryRow(context.Background(), `
		SELECT id::text FROM identity.users WHERE email='tech@ion.local'
	`).Scan(&techID); err != nil {
		t.Fatalf("look up tech user id: %v", err)
	}

	// -----------------------------------------------------------------
	// 1. Admin inserts a priority into the tech's queue.
	// -----------------------------------------------------------------
	const reason = "Wave 58 — VIP customer outage; reroute next visit."
	var insertion struct {
		ID    string `json:"id"`
		WoID  string `json:"wo_id"`
		TechID string `json:"tech_id"`
	}
	admin.do("POST", "/api/field/work-orders/"+woID+"/priority-insert",
		map[string]any{
			"tech_user_id": techID,
			"reason":       reason,
		}, &insertion, 201)
	if insertion.ID == "" {
		t.Fatal("priority-insert returned empty id")
	}

	// -----------------------------------------------------------------
	// 2. Tech logs in (separate client with tech's bearer token).
	// -----------------------------------------------------------------
	tech := newClientAs(t, "tech@ion.local")

	// -----------------------------------------------------------------
	// 3. Tech sees the insertion in their /mine queue.
	// -----------------------------------------------------------------
	var mine struct {
		Items []struct {
			ID       string `json:"id"`
			WoID     string `json:"wo_id"`
			Reason   string `json:"reason"`
			Accepted *bool  `json:"accepted,omitempty"`
		} `json:"items"`
	}
	tech.do("GET", "/api/field/priority-insertions/mine", nil, &mine, 200)
	var found *struct {
		ID       string `json:"id"`
		WoID     string `json:"wo_id"`
		Reason   string `json:"reason"`
		Accepted *bool  `json:"accepted,omitempty"`
	}
	for i := range mine.Items {
		if mine.Items[i].ID == insertion.ID {
			found = &mine.Items[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("priority insertion %s not in tech's /mine queue", insertion.ID)
	}
	if found.Reason != reason {
		t.Errorf("reason round-trip broken: got %q, want %q", found.Reason, reason)
	}
	if found.Accepted != nil {
		t.Errorf("fresh insertion accepted should be null; got %v", *found.Accepted)
	}

	// -----------------------------------------------------------------
	// 4. Tech accepts the insertion.
	// -----------------------------------------------------------------
	tech.do("POST", "/api/field/priority-insertions/"+insertion.ID+"/respond",
		map[string]any{"accepted": true}, nil, 200)

	// -----------------------------------------------------------------
	// 5. /mine filter shows accepted IS NULL — so the accepted
	//    insertion shouldn't appear anymore.
	// -----------------------------------------------------------------
	var mineAfter struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	tech.do("GET", "/api/field/priority-insertions/mine", nil, &mineAfter, 200)
	for _, it := range mineAfter.Items {
		if it.ID == insertion.ID {
			t.Errorf("accepted insertion %s still in /mine — filter broken", insertion.ID)
		}
	}

	// -----------------------------------------------------------------
	// 6. Direct DB read confirms accepted=true persisted.
	// -----------------------------------------------------------------
	var accepted *bool
	if err := sharedPool.QueryRow(context.Background(),
		`SELECT accepted FROM field.priority_insertions WHERE id=$1`,
		insertion.ID).Scan(&accepted); err != nil {
		t.Fatalf("look up insertion in DB: %v", err)
	}
	if accepted == nil || !*accepted {
		t.Errorf("DB accepted: want true, got %v", accepted)
	}
}
