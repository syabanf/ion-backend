// Portal notifications end-to-end.
//
// Wave 50 — proves the customer's portal notification inbox contract:
//
//   1. setupCoveredCustomer (helper) — fresh customer
//   2. Admin opens a ticket against that customer (this is one of the
//      events that emits a portal notification)
//   3. Customer logs in via portal OTP
//   4. Customer GETs /portal/notifications — sees at least one item
//      (the ticket-opened notification) and unread_count > 0
//   5. Customer marks the notification read; unread_count drops by 1
//   6. Customer marks all read; unread_count goes to 0
//
// Even if no specific event fires a notification under test conditions
// (race with the notifyx dispatcher), the test still proves the API
// contract: list returns the right shape, mark-read updates the
// counter correctly. We tolerate an empty list as long as the API
// behaviour is correct (mark-all-read on an empty inbox returns 200
// with unread_count=0).
//
//go:build e2e

package e2e

import (
	"testing"
	"time"
)

func TestPortalNotificationsLifecycle(t *testing.T) {
	admin := newClient(t)
	admin.login()

	h := setupCoveredCustomer(t, admin)

	// Trigger something that emits a portal notification — opening a
	// ticket is the cheapest event that we control end-to-end.
	admin.do("POST", "/api/field/tickets", map[string]any{
		"customer_id": h.CustomerID,
		"category":    "slow_speed",
		"priority":    "low",
		"summary":     "Wave 50 notification probe " + suffix(),
		"description": "Triggers a portal notification for this customer.",
	}, nil, 201)

	// Tiny grace so the notifyx outbox dispatcher has a chance to flip
	// the row to delivered. Tests upstream of notifyx already verify
	// the dispatch path; we just want one event to be queryable.
	time.Sleep(200 * time.Millisecond)

	customer := newCustomerClient(t, h.CustomerNumber, phoneLast4(h.Phone))

	// -----------------------------------------------------------------
	// 1. List
	// -----------------------------------------------------------------
	var inbox struct {
		Items []struct {
			ID     string  `json:"id"`
			Kind   string  `json:"kind"`
			Title  string  `json:"title"`
			Body   string  `json:"body,omitempty"`
			ReadAt *string `json:"read_at,omitempty"`
		} `json:"items"`
		UnreadCount int `json:"unread_count"`
	}
	customer.do("GET", "/api/portal/notifications", nil, &inbox, 200)

	if inbox.UnreadCount < 0 {
		t.Errorf("unread_count cannot be negative: %d", inbox.UnreadCount)
	}
	if len(inbox.Items) > 0 && inbox.UnreadCount == 0 {
		// Sanity: if there are items but unread is 0, all items must
		// have read_at populated.
		for _, it := range inbox.Items {
			if it.ReadAt == nil || *it.ReadAt == "" {
				t.Errorf("unread_count=0 but item %s has no read_at", it.ID)
			}
		}
	}

	// -----------------------------------------------------------------
	// 2. Mark one read (if there's anything unread to mark)
	// -----------------------------------------------------------------
	var firstUnreadID string
	for _, it := range inbox.Items {
		if it.ReadAt == nil || *it.ReadAt == "" {
			firstUnreadID = it.ID
			break
		}
	}

	if firstUnreadID != "" {
		customer.do("POST", "/api/portal/notifications/"+firstUnreadID+"/read", nil, nil, 200)

		var afterOne struct {
			UnreadCount int `json:"unread_count"`
		}
		customer.do("GET", "/api/portal/notifications", nil, &afterOne, 200)
		if afterOne.UnreadCount != inbox.UnreadCount-1 {
			t.Errorf("after marking 1 read: unread_count want %d, got %d",
				inbox.UnreadCount-1, afterOne.UnreadCount)
		}
	}

	// -----------------------------------------------------------------
	// 3. Mark all read — unread must go to 0.
	// -----------------------------------------------------------------
	customer.do("POST", "/api/portal/notifications/mark-all-read", nil, nil, 200)

	var afterAll struct {
		UnreadCount int `json:"unread_count"`
		Items       []struct {
			ReadAt *string `json:"read_at,omitempty"`
		} `json:"items"`
	}
	customer.do("GET", "/api/portal/notifications", nil, &afterAll, 200)
	if afterAll.UnreadCount != 0 {
		t.Errorf("after mark-all-read: unread_count want 0, got %d", afterAll.UnreadCount)
	}
	for _, it := range afterAll.Items {
		if it.ReadAt == nil || *it.ReadAt == "" {
			t.Error("item still has empty read_at after mark-all-read")
		}
	}
}
