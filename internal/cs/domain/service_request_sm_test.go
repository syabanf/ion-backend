package domain

import (
	"testing"
	"time"

	"github.com/google/uuid"

	derrors "github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// Wave 124 — Service Request state-machine tests (TC-SR-*).
//
//   submitted → approved | rejected
//   approved → in_progress → fulfilled | cancelled
//   rejected, cancelled, fulfilled are terminal.
// =====================================================================

func newSR(t *testing.T, typ ServiceRequestType) *ServiceRequest {
	t.Helper()
	by := uuid.New()
	sr, err := NewServiceRequest(uuid.New(), uuid.New(), typ, &by, nil)
	if err != nil {
		t.Fatalf("NewServiceRequest: %v", err)
	}
	return sr
}

// TC-SR-009 — happy-path lifecycle for an approval-required type.
func TestSR_HappyPath_RequiresApproval(t *testing.T) {
	sr := newSR(t, SRTypeSpeedDowngrade)
	if sr.Status != SRStatusSubmitted {
		t.Fatalf("expected submitted, got %s", sr.Status)
	}
	approver := uuid.New()
	if err := sr.Approve(approver); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if sr.Status != SRStatusApproved {
		t.Fatalf("expected approved, got %s", sr.Status)
	}
	if sr.ApprovedBy == nil || *sr.ApprovedBy != approver {
		t.Fatalf("ApprovedBy not set")
	}
	if err := sr.StartFulfillment(); err != nil {
		t.Fatalf("StartFulfillment: %v", err)
	}
	if sr.Status != SRStatusInProgress {
		t.Fatalf("expected in_progress, got %s", sr.Status)
	}
	now := time.Now().UTC()
	if err := sr.MarkFulfilled(now); err != nil {
		t.Fatalf("MarkFulfilled: %v", err)
	}
	if sr.Status != SRStatusFulfilled {
		t.Fatalf("expected fulfilled, got %s", sr.Status)
	}
	if sr.FulfilledAt == nil {
		t.Fatalf("FulfilledAt not stamped")
	}
	if !sr.IsTerminal() {
		t.Fatalf("expected terminal after fulfilment")
	}
}

// Non-approval types auto-flow to approved on create.
func TestSR_NoApprovalAutoApproved(t *testing.T) {
	sr := newSR(t, SRTypeAddOn)
	if sr.Status != SRStatusApproved {
		t.Fatalf("expected auto-approved, got %s", sr.Status)
	}
	// Approve on already-approved should be a conflict.
	if err := sr.Approve(uuid.New()); err == nil {
		t.Fatalf("expected conflict re-approving an auto-approved SR")
	}
}

func TestSR_Reject(t *testing.T) {
	sr := newSR(t, SRTypeSpeedDowngrade)
	by := uuid.New()
	if err := sr.Reject(by, "not eligible"); err != nil {
		t.Fatalf("Reject: %v", err)
	}
	if sr.Status != SRStatusRejected {
		t.Fatalf("expected rejected, got %s", sr.Status)
	}
	if sr.RejectionReason != "not eligible" {
		t.Fatalf("rejection reason not stored: %q", sr.RejectionReason)
	}
	if !sr.IsTerminal() {
		t.Fatalf("rejected should be terminal")
	}
}

func TestSR_RejectRequiresReason(t *testing.T) {
	sr := newSR(t, SRTypeSpeedDowngrade)
	if err := sr.Reject(uuid.New(), "   "); err == nil {
		t.Fatalf("expected validation error for empty reason")
	} else if derrors.KindOf(err) != derrors.KindValidation {
		t.Fatalf("expected validation kind, got %v", err)
	}
}

func TestSR_CannotApproveAfterReject(t *testing.T) {
	sr := newSR(t, SRTypeSpeedDowngrade)
	if err := sr.Reject(uuid.New(), "no go"); err != nil {
		t.Fatalf("Reject: %v", err)
	}
	if err := sr.Approve(uuid.New()); err == nil {
		t.Fatalf("expected conflict approving a rejected SR")
	}
}

// TC-SR-009 — invalid transitions.
func TestSR_StartFulfillmentRequiresApproved(t *testing.T) {
	sr := newSR(t, SRTypeSpeedDowngrade) // submitted
	if err := sr.StartFulfillment(); err == nil {
		t.Fatalf("expected conflict starting fulfilment on submitted SR")
	}
}

func TestSR_MarkFulfilledRequiresInProgress(t *testing.T) {
	sr := newSR(t, SRTypeAddOn) // auto-approved
	if err := sr.MarkFulfilled(time.Now().UTC()); err == nil {
		t.Fatalf("expected conflict marking approved SR as fulfilled directly")
	}
}

func TestSR_CancelOnlyFromApprovedOrInProgress(t *testing.T) {
	sr := newSR(t, SRTypeSpeedDowngrade) // submitted
	if err := sr.Cancel(uuid.New(), "changed mind"); err == nil {
		t.Fatalf("expected conflict cancelling submitted SR (should reject instead)")
	}

	sr2 := newSR(t, SRTypeAddOn) // approved
	if err := sr2.Cancel(uuid.New(), "no longer needed"); err != nil {
		t.Fatalf("Cancel from approved: %v", err)
	}
	if sr2.Status != SRStatusCancelled {
		t.Fatalf("expected cancelled, got %s", sr2.Status)
	}
}

// =====================================================================
// Service-request type approval policy
// =====================================================================
func TestSR_RequiresApproval_PerType(t *testing.T) {
	cases := []struct {
		typ      ServiceRequestType
		requires bool
	}{
		{SRTypePlanChange, true},
		{SRTypeAddressRelocation, true},
		{SRTypeSpeedDowngrade, true},
		{SRTypeSuspendPause, true},
		{SRTypeVacationHold, true},
		{SRTypeAddOn, false},
		{SRTypeSpeedUpgrade, false},
		{SRTypeEquipmentSwap, false},
		{SRTypeData, false},
		{SRTypeOther, false},
	}
	for _, c := range cases {
		if got := c.typ.RequiresApproval(); got != c.requires {
			t.Fatalf("%s RequiresApproval = %v, want %v", c.typ, got, c.requires)
		}
	}
}
