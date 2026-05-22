// Portal data isolation end-to-end — proves the customer portal's
// authz boundary holds when one customer attempts to access another
// customer's data by guessing or substituting IDs in URLs.
//
// Wave 55 — the customer portal endpoints filter by the customer_id
// embedded in the session JWT (claims.UserID). A malicious customer
// could try:
//
//   * GET  /api/portal/tickets/{id-of-other-customer's-ticket}
//   * POST /api/portal/tickets/{id-of-other-customer's-ticket}/messages
//   * GET  /api/portal/notifications + read a notification id that
//     belongs to someone else
//
// All of these must return 403 or 404 — never the other customer's data.
// Without this gate, a portal regression that drops the customer_id
// filter would silently leak data.
//
//go:build e2e

package e2e

import (
	"testing"
)

func TestPortalDataIsolation(t *testing.T) {
	admin := newClient(t)
	admin.login()

	// Two independent customers, each in their own branch/ODP.
	alice := setupCoveredCustomer(t, admin)
	bob := setupCoveredCustomer(t, admin)

	// Open a ticket against each so we have a concrete forbidden target.
	openTicket := func(customerID, label string) string {
		var resp struct{ ID string `json:"id"` }
		admin.do("POST", "/api/field/tickets", map[string]any{
			"customer_id": customerID,
			"category":    "slow_speed",
			"priority":    "medium",
			"summary":     label,
			"description": "Wave 55 isolation probe — " + label,
		}, &resp, 201)
		return resp.ID
	}
	aliceTicketID := openTicket(alice.CustomerID, "alice")
	bobTicketID := openTicket(bob.CustomerID, "bob")

	// Alice logs in via portal.
	aliceClient := newCustomerClient(t, alice.CustomerNumber, phoneLast4(alice.Phone))

	// -----------------------------------------------------------------
	// 1. Alice can see her OWN ticket.
	// -----------------------------------------------------------------
	var aliceOwn struct {
		ID         string `json:"id"`
		CustomerID string `json:"customer_id"`
	}
	aliceClient.do("GET", "/api/portal/tickets/"+aliceTicketID, nil, &aliceOwn, 200)
	if aliceOwn.ID != aliceTicketID {
		t.Errorf("alice can't see her own ticket: got id %q want %q", aliceOwn.ID, aliceTicketID)
	}

	// -----------------------------------------------------------------
	// 2. Alice cannot see Bob's ticket. Acceptable: 403 (forbidden) or
	//    404 (not found — leaks less but functionally equivalent).
	// -----------------------------------------------------------------
	got := aliceClient.statusOnly("GET", "/api/portal/tickets/"+bobTicketID)
	if got != 403 && got != 404 {
		t.Fatalf("DATA LEAK: alice reading bob's ticket %s returned status %d (want 403 or 404)",
			bobTicketID, got)
	}

	// -----------------------------------------------------------------
	// 3. Alice cannot post a message to Bob's ticket.
	// -----------------------------------------------------------------
	got = aliceClient.statusOnlyJSON("POST", "/api/portal/tickets/"+bobTicketID+"/messages",
		map[string]any{"body": "Wave 55 — alice trying to post to bob's ticket"})
	if got != 403 && got != 404 {
		t.Fatalf("AUTHZ FAIL: alice POSTing to bob's ticket %s returned status %d (want 403 or 404)",
			bobTicketID, got)
	}

	// -----------------------------------------------------------------
	// 4. Alice cannot submit CSAT on Bob's ticket.
	// -----------------------------------------------------------------
	got = aliceClient.statusOnlyJSON("POST", "/api/portal/tickets/"+bobTicketID+"/csat",
		map[string]any{"score": 1, "comment": "saboteur attempt"})
	if got != 403 && got != 404 && got != 409 {
		// 409 is also acceptable if the ticket isn't resolved yet —
		// what we want to confirm is alice ISN'T 201ing.
		t.Fatalf("AUTHZ FAIL: alice CSATing bob's ticket %s returned status %d", bobTicketID, got)
	}

	// -----------------------------------------------------------------
	// 5. Alice's notifications list must only contain her own rows.
	// -----------------------------------------------------------------
	var inbox struct {
		Items []struct {
			ID         string `json:"id"`
			CustomerID string `json:"customer_id"`
		} `json:"items"`
	}
	aliceClient.do("GET", "/api/portal/notifications", nil, &inbox, 200)
	for _, it := range inbox.Items {
		if it.CustomerID != "" && it.CustomerID != alice.CustomerID {
			t.Fatalf("DATA LEAK: alice's notifications include row %s belonging to customer %q",
				it.ID, it.CustomerID)
		}
	}
}
