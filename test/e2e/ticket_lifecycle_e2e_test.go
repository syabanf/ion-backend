// Ticket lifecycle end-to-end — open → message both sides → resolve → CSAT.
//
// Wave 50 — proves the full CS ticket contract:
//
//   1. setupCoveredCustomer (helper) — fresh customer
//   2. Admin (dashboard) opens a ticket via POST /api/field/tickets
//   3. Admin posts an agent message via POST /api/field/tickets/{id}/messages
//   4. Customer (mobile customer_app via portal OTP) sees the ticket in
//      /api/portal/tickets/{id}/messages with the agent message visible
//   5. Customer replies via POST /api/portal/tickets/{id}/messages
//   6. Admin sees the customer's message in /api/field/tickets/{id}/messages
//      (round-trip both directions)
//   7. Admin resolves via PATCH /api/field/tickets/{id} with status=resolved
//   8. Customer submits CSAT via POST /api/portal/tickets/{id}/csat
//      (only allowed after status=resolved)
//
// Asserts the same row is visible from both surfaces and the resolved
// gate on CSAT works.
//
//go:build e2e

package e2e

import (
	"testing"
)

func TestTicketLifecycle(t *testing.T) {
	admin := newClient(t)
	admin.login()

	h := setupCoveredCustomer(t, admin)

	// -----------------------------------------------------------------
	// 1. Admin opens the ticket.
	// -----------------------------------------------------------------
	var ticket struct {
		ID           string `json:"id"`
		TicketNumber string `json:"ticket_number"`
		Status       string `json:"status"`
	}
	admin.do("POST", "/api/field/tickets", map[string]any{
		"customer_id": h.CustomerID,
		"category":    "slow_speed",
		"priority":    "medium",
		"summary":     "Wave 50 ticket lifecycle " + suffix(),
		"description": "Customer reports speed below contracted bandwidth.",
	}, &ticket, 201)
	if ticket.ID == "" {
		t.Fatal("ticket create returned empty id")
	}
	if ticket.Status != "open" {
		t.Errorf("new ticket status: want open, got %q", ticket.Status)
	}

	// -----------------------------------------------------------------
	// 2. Admin posts an agent message (cs_agent surface from dashboard).
	// -----------------------------------------------------------------
	agentMsgBody := "Agent message from Wave 50 " + suffix()
	admin.do("POST", "/api/field/tickets/"+ticket.ID+"/messages", map[string]any{
		"body":             agentMsgBody,
		"is_internal_note": false,
	}, nil, 201)

	// -----------------------------------------------------------------
	// 3. Customer logs in + sees the agent's message in their portal view.
	// -----------------------------------------------------------------
	customer := newCustomerClient(t, h.CustomerNumber, phoneLast4(h.Phone))

	var portalMsgs struct {
		Items []struct {
			ID       string `json:"id"`
			SenderID string `json:"sender_id"`
			Body     string `json:"body"`
			IsAgent  bool   `json:"is_agent"`
		} `json:"items"`
	}
	customer.do("GET", "/api/portal/tickets/"+ticket.ID+"/messages", nil, &portalMsgs, 200)
	sawAgentMsg := false
	for _, m := range portalMsgs.Items {
		if m.Body == agentMsgBody {
			sawAgentMsg = true
			if !m.IsAgent {
				t.Errorf("agent message is_agent flag wrong (false) — surface contract broken")
			}
			break
		}
	}
	if !sawAgentMsg {
		t.Fatalf("customer doesn't see agent message (cross-surface broken). got %d items", len(portalMsgs.Items))
	}

	// -----------------------------------------------------------------
	// 4. Customer replies.
	// -----------------------------------------------------------------
	customerMsgBody := "Customer reply from Wave 50 " + suffix()
	customer.do("POST", "/api/portal/tickets/"+ticket.ID+"/messages", map[string]any{
		"body": customerMsgBody,
	}, nil, 201)

	// -----------------------------------------------------------------
	// 5. Admin sees the customer's reply (the other direction of the
	//    cross-surface handoff).
	// -----------------------------------------------------------------
	var agentView struct {
		Items []struct {
			Body    string `json:"body"`
			IsAgent bool   `json:"is_agent"`
		} `json:"items"`
	}
	admin.do("GET", "/api/field/tickets/"+ticket.ID+"/messages", nil, &agentView, 200)
	sawCustReply := false
	for _, m := range agentView.Items {
		if m.Body == customerMsgBody {
			sawCustReply = true
			if m.IsAgent {
				t.Errorf("customer reply is_agent flag wrong (true) on dashboard view")
			}
			break
		}
	}
	if !sawCustReply {
		t.Fatalf("admin doesn't see customer reply (cross-surface broken). got %d items", len(agentView.Items))
	}

	// -----------------------------------------------------------------
	// 6. CSAT before resolve must fail. Status guard.
	// -----------------------------------------------------------------
	_ = customer.doExpectError("POST", "/api/portal/tickets/"+ticket.ID+"/csat",
		map[string]any{"score": 5, "comment": "premature CSAT"}, 409)

	// -----------------------------------------------------------------
	// 7. Admin resolves the ticket.
	// -----------------------------------------------------------------
	var resolved struct {
		Status string `json:"status"`
	}
	admin.do("PATCH", "/api/field/tickets/"+ticket.ID, map[string]any{
		"status": "resolved",
	}, &resolved, 200)
	if resolved.Status != "resolved" {
		t.Errorf("resolve PATCH didn't flip status: got %q", resolved.Status)
	}

	// -----------------------------------------------------------------
	// 8. CSAT after resolve must succeed.
	// -----------------------------------------------------------------
	customer.do("POST", "/api/portal/tickets/"+ticket.ID+"/csat", map[string]any{
		"score":   5,
		"comment": "Wave 50 — happy customer.",
	}, nil, 201)

	// Round-trip: admin reads the ticket and sees the CSAT.
	var withCSAT struct {
		CSATScore   int    `json:"csat_score"`
		CSATComment string `json:"csat_comment"`
	}
	admin.do("GET", "/api/field/tickets/"+ticket.ID, nil, &withCSAT, 200)
	if withCSAT.CSATScore != 5 {
		t.Errorf("CSAT score not persisted: got %d", withCSAT.CSATScore)
	}
}
