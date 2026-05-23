package domain

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	derrors "github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// Wave 104 — CustomerPO state-machine contract tests (TC-SM-CPO-*)
//
//	received → validated → accepted (terminal positive)
//	received | validated → rejected (terminal negative; reason required)
//	received | validated → cancelled (terminal admin)
// =====================================================================

func newCustomerPOAt(t *testing.T, status CustomerPOStatus) *CustomerPO {
	t.Helper()
	po, err := NewCustomerPO(uuid.New(), uuid.New(), uuid.New(), "PO-1")
	if err != nil {
		t.Fatalf("NewCustomerPO: %v", err)
	}
	switch status {
	case CustomerPOStatusReceived:
		// already there
	case CustomerPOStatusValidated:
		if err := po.Validate(); err != nil {
			t.Fatalf("Validate: %v", err)
		}
	case CustomerPOStatusAccepted:
		if err := po.Validate(); err != nil {
			t.Fatalf("Validate: %v", err)
		}
		if err := po.Accept(); err != nil {
			t.Fatalf("Accept: %v", err)
		}
	case CustomerPOStatusRejected:
		if err := po.Reject("not approved"); err != nil {
			t.Fatalf("Reject: %v", err)
		}
	case CustomerPOStatusCancelled:
		if err := po.Cancel(); err != nil {
			t.Fatalf("Cancel: %v", err)
		}
	}
	return po
}

func TestCustomerPOSM_ValidTransitions(t *testing.T) {
	cases := []struct {
		name       string
		from       CustomerPOStatus
		action     func(*CustomerPO) error
		wantStatus CustomerPOStatus
	}{
		{"received -> validated", CustomerPOStatusReceived, func(p *CustomerPO) error { return p.Validate() }, CustomerPOStatusValidated},
		{"validated -> accepted", CustomerPOStatusValidated, func(p *CustomerPO) error { return p.Accept() }, CustomerPOStatusAccepted},
		{"received -> rejected", CustomerPOStatusReceived, func(p *CustomerPO) error { return p.Reject("not enough budget") }, CustomerPOStatusRejected},
		{"validated -> rejected", CustomerPOStatusValidated, func(p *CustomerPO) error { return p.Reject("doc mismatch") }, CustomerPOStatusRejected},
		{"received -> cancelled", CustomerPOStatusReceived, func(p *CustomerPO) error { return p.Cancel() }, CustomerPOStatusCancelled},
		{"validated -> cancelled", CustomerPOStatusValidated, func(p *CustomerPO) error { return p.Cancel() }, CustomerPOStatusCancelled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			po := newCustomerPOAt(t, tc.from)
			if err := tc.action(po); err != nil {
				t.Fatalf("action: %v", err)
			}
			if po.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q", po.Status, tc.wantStatus)
			}
		})
	}
}

func TestCustomerPOSM_InvalidTransitions(t *testing.T) {
	cases := []struct {
		name     string
		from     CustomerPOStatus
		action   func(*CustomerPO) error
		wantCode string
	}{
		{"received -> accepted (skip validation)", CustomerPOStatusReceived, func(p *CustomerPO) error { return p.Accept() }, "customer_po.invalid_state_transition"},
		{"accepted -> validated (backward)", CustomerPOStatusAccepted, func(p *CustomerPO) error { return p.Validate() }, "customer_po.invalid_state_transition"},
		{"accepted -> accepted (idempotent NOT allowed)", CustomerPOStatusAccepted, func(p *CustomerPO) error { return p.Accept() }, "customer_po.invalid_state_transition"},
		{"accepted -> rejected", CustomerPOStatusAccepted, func(p *CustomerPO) error { return p.Reject("oops") }, "customer_po.invalid_state_transition"},
		{"accepted -> cancelled", CustomerPOStatusAccepted, func(p *CustomerPO) error { return p.Cancel() }, "customer_po.invalid_state_transition"},
		{"rejected -> validated", CustomerPOStatusRejected, func(p *CustomerPO) error { return p.Validate() }, "customer_po.invalid_state_transition"},
		{"rejected -> accepted", CustomerPOStatusRejected, func(p *CustomerPO) error { return p.Accept() }, "customer_po.invalid_state_transition"},
		{"cancelled -> validated", CustomerPOStatusCancelled, func(p *CustomerPO) error { return p.Validate() }, "customer_po.invalid_state_transition"},
		// Reason validation
		{"reject without reason", CustomerPOStatusReceived, func(p *CustomerPO) error { return p.Reject("") }, "customer_po.reject_reason_required"},
		{"reject with whitespace-only reason", CustomerPOStatusValidated, func(p *CustomerPO) error { return p.Reject("   ") }, "customer_po.reject_reason_required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			po := newCustomerPOAt(t, tc.from)
			err := tc.action(po)
			if err == nil {
				t.Fatalf("action should have errored; po.Status now %q", po.Status)
			}
			var de *derrors.Error
			if !errors.As(err, &de) {
				t.Fatalf("error is not *derrors.Error: %T %v", err, err)
			}
			if de.Code != tc.wantCode {
				t.Errorf("code = %q, want %q", de.Code, tc.wantCode)
			}
		})
	}
}
