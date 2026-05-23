package domain

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	derrors "github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// Wave 104 — WholesaleOrder state-machine contract tests (TC-SM-WO-*)
//
//	draft → submitted → approved → fulfilled (terminal positive)
//	                  ↘ rejected            (terminal negative)
//	draft|submitted → cancelled              (terminal admin)
// =====================================================================

func newWholesaleOrderAt(t *testing.T, status WholesaleOrderStatus) *WholesaleOrder {
	t.Helper()
	o, err := NewWholesaleOrder(uuid.New(), uuid.New())
	if err != nil {
		t.Fatalf("NewWholesaleOrder: %v", err)
	}
	// Add at least one line so Submit doesn't trip "order.empty".
	if err := o.AddLine(uuid.New(), 1, 100); err != nil {
		t.Fatalf("AddLine: %v", err)
	}
	switch status {
	case WholesaleOrderStatusDraft:
	case WholesaleOrderStatusSubmitted:
		if err := o.Submit(); err != nil {
			t.Fatalf("Submit: %v", err)
		}
	case WholesaleOrderStatusApproved:
		if err := o.Submit(); err != nil {
			t.Fatalf("Submit: %v", err)
		}
		if err := o.Approve(uuid.New()); err != nil {
			t.Fatalf("Approve: %v", err)
		}
	case WholesaleOrderStatusFulfilled:
		if err := o.Submit(); err != nil {
			t.Fatalf("Submit: %v", err)
		}
		if err := o.Approve(uuid.New()); err != nil {
			t.Fatalf("Approve: %v", err)
		}
		if err := o.Fulfill(); err != nil {
			t.Fatalf("Fulfill: %v", err)
		}
	case WholesaleOrderStatusRejected:
		if err := o.Submit(); err != nil {
			t.Fatalf("Submit: %v", err)
		}
		if err := o.Reject(); err != nil {
			t.Fatalf("Reject: %v", err)
		}
	case WholesaleOrderStatusCancelled:
		if err := o.Cancel(); err != nil {
			t.Fatalf("Cancel: %v", err)
		}
	}
	return o
}

func TestWholesaleOrderSM_ValidTransitions(t *testing.T) {
	cases := []struct {
		name       string
		from       WholesaleOrderStatus
		action     func(*WholesaleOrder) error
		wantStatus WholesaleOrderStatus
	}{
		{"draft -> submitted", WholesaleOrderStatusDraft, func(o *WholesaleOrder) error { return o.Submit() }, WholesaleOrderStatusSubmitted},
		{"submitted -> approved", WholesaleOrderStatusSubmitted, func(o *WholesaleOrder) error { return o.Approve(uuid.New()) }, WholesaleOrderStatusApproved},
		{"submitted -> rejected", WholesaleOrderStatusSubmitted, func(o *WholesaleOrder) error { return o.Reject() }, WholesaleOrderStatusRejected},
		{"approved -> fulfilled", WholesaleOrderStatusApproved, func(o *WholesaleOrder) error { return o.Fulfill() }, WholesaleOrderStatusFulfilled},
		{"draft -> cancelled", WholesaleOrderStatusDraft, func(o *WholesaleOrder) error { return o.Cancel() }, WholesaleOrderStatusCancelled},
		{"submitted -> cancelled", WholesaleOrderStatusSubmitted, func(o *WholesaleOrder) error { return o.Cancel() }, WholesaleOrderStatusCancelled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := newWholesaleOrderAt(t, tc.from)
			if err := tc.action(o); err != nil {
				t.Fatalf("action: %v", err)
			}
			if o.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q", o.Status, tc.wantStatus)
			}
		})
	}
}

func TestWholesaleOrderSM_InvalidTransitions(t *testing.T) {
	cases := []struct {
		name     string
		from     WholesaleOrderStatus
		action   func(*WholesaleOrder) error
		wantCode string
	}{
		{"draft -> approved (skip submit)", WholesaleOrderStatusDraft, func(o *WholesaleOrder) error { return o.Approve(uuid.New()) }, "order.cannot_approve"},
		{"draft -> rejected (skip submit)", WholesaleOrderStatusDraft, func(o *WholesaleOrder) error { return o.Reject() }, "order.cannot_reject"},
		{"draft -> fulfilled (skip everything)", WholesaleOrderStatusDraft, func(o *WholesaleOrder) error { return o.Fulfill() }, "order.cannot_fulfill"},
		{"submitted -> fulfilled (skip approve)", WholesaleOrderStatusSubmitted, func(o *WholesaleOrder) error { return o.Fulfill() }, "order.cannot_fulfill"},
		{"approved -> rejected (backward)", WholesaleOrderStatusApproved, func(o *WholesaleOrder) error { return o.Reject() }, "order.cannot_reject"},
		{"approved -> submitted (backward)", WholesaleOrderStatusApproved, func(o *WholesaleOrder) error { return o.Submit() }, "order.cannot_submit"},
		{"approved -> cancelled (post-approve cancel blocked)", WholesaleOrderStatusApproved, func(o *WholesaleOrder) error { return o.Cancel() }, "order.cannot_cancel"},
		{"fulfilled -> fulfilled (double)", WholesaleOrderStatusFulfilled, func(o *WholesaleOrder) error { return o.Fulfill() }, "order.cannot_fulfill"},
		{"fulfilled -> cancelled", WholesaleOrderStatusFulfilled, func(o *WholesaleOrder) error { return o.Cancel() }, "order.cannot_cancel"},
		{"rejected -> approved", WholesaleOrderStatusRejected, func(o *WholesaleOrder) error { return o.Approve(uuid.New()) }, "order.cannot_approve"},
		{"cancelled -> submitted", WholesaleOrderStatusCancelled, func(o *WholesaleOrder) error { return o.Submit() }, "order.cannot_submit"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := newWholesaleOrderAt(t, tc.from)
			err := tc.action(o)
			if err == nil {
				t.Fatalf("action should have errored; status now %q", o.Status)
			}
			var de *derrors.Error
			if !errors.As(err, &de) {
				t.Fatalf("error not *derrors.Error: %T %v", err, err)
			}
			if de.Code != tc.wantCode {
				t.Errorf("code = %q, want %q", de.Code, tc.wantCode)
			}
		})
	}
}

// TestWholesaleOrderSM_SubmitEmpty — Submit() refuses a zero-line order.
func TestWholesaleOrderSM_SubmitEmpty(t *testing.T) {
	o, err := NewWholesaleOrder(uuid.New(), uuid.New())
	if err != nil {
		t.Fatalf("NewWholesaleOrder: %v", err)
	}
	err = o.Submit()
	if err == nil {
		t.Fatal("Submit with no lines should fail")
	}
	var de *derrors.Error
	if !errors.As(err, &de) {
		t.Fatalf("err type: %T", err)
	}
	if de.Code != "order.empty" {
		t.Errorf("code = %q, want order.empty", de.Code)
	}
}
