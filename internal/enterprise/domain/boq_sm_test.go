package domain

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	derrors "github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// Wave 104 — BOQ state-machine contract tests (TC-SM-BOQ-*)
//
// Lifecycle (per boq.go top-of-file diagram):
//
//	draft           → in_approval
//	in_approval     → boq_approved / rejected
//	rejected        → revision_draft
//	revision_draft  → in_approval
//	boq_approved    → superseded
// =====================================================================

// completeLine returns a valid submit-ready BOQLine for the given BOQ.
// The line carries:
//   - assigned provider (company + user)
//   - vendor_unit_cost set
//   - sell_unit_price above the margin floor so ValidateMarginFloor
//     passes
func completeLine(t *testing.T, boqID uuid.UUID) BOQLine {
	t.Helper()
	l, err := NewBOQLine(
		boqID, uuid.New(), uuid.New(),
		"SKU-1", "Patch cord 2m", "ea",
		1000, // base
		20,   // min_margin_pct
		10,   // max_discount_pct
		1,    // qty
	)
	if err != nil {
		t.Fatalf("NewBOQLine: %v", err)
	}
	l.SetProvider(uuid.New(), uuid.New())
	if err := l.SetVendorCost(700); err != nil {
		t.Fatalf("SetVendorCost: %v", err)
	}
	// Sell price 2000 -> margin (2000-700)/2000=65% -> > 20% floor.
	l.SellUnitPrice = 2000
	return *l
}

func newBOQAt(t *testing.T, status BOQStatus) *BOQ {
	t.Helper()
	b, err := NewBOQ(uuid.New(), uuid.New())
	if err != nil {
		t.Fatalf("NewBOQ: %v", err)
	}
	lines := []BOQLine{completeLine(t, b.ID)}
	switch status {
	case BOQStatusDraft:
		// already there
	case BOQStatusInApproval:
		if err := b.Submit(lines, uuid.New(), "hash1"); err != nil {
			t.Fatalf("Submit: %v", err)
		}
	case BOQStatusApproved:
		if err := b.Submit(lines, uuid.New(), "hash1"); err != nil {
			t.Fatalf("Submit: %v", err)
		}
		if err := b.MarkApproved(); err != nil {
			t.Fatalf("MarkApproved: %v", err)
		}
	case BOQStatusRejected:
		if err := b.Submit(lines, uuid.New(), "hash1"); err != nil {
			t.Fatalf("Submit: %v", err)
		}
		if err := b.MarkRejected(RejectionReasonPricing, "too high"); err != nil {
			t.Fatalf("MarkRejected: %v", err)
		}
	case BOQStatusRevisionDraft:
		if err := b.Submit(lines, uuid.New(), "hash1"); err != nil {
			t.Fatalf("Submit: %v", err)
		}
		if err := b.MarkRejected(RejectionReasonPricing, "too high"); err != nil {
			t.Fatalf("MarkRejected: %v", err)
		}
		if err := b.StartRevision(); err != nil {
			t.Fatalf("StartRevision: %v", err)
		}
	case BOQStatusSuperseded:
		if err := b.Submit(lines, uuid.New(), "hash1"); err != nil {
			t.Fatalf("Submit: %v", err)
		}
		if err := b.MarkApproved(); err != nil {
			t.Fatalf("MarkApproved: %v", err)
		}
		if err := b.Supersede(); err != nil {
			t.Fatalf("Supersede: %v", err)
		}
	}
	return b
}

func TestBOQSM_ValidTransitions(t *testing.T) {
	cases := []struct {
		name       string
		from       BOQStatus
		action     func(*BOQ, []BOQLine) error
		wantStatus BOQStatus
	}{
		{"draft -> in_approval (Submit)", BOQStatusDraft, func(b *BOQ, l []BOQLine) error { return b.Submit(l, uuid.New(), "h") }, BOQStatusInApproval},
		{"in_approval -> approved", BOQStatusInApproval, func(b *BOQ, _ []BOQLine) error { return b.MarkApproved() }, BOQStatusApproved},
		{"in_approval -> rejected", BOQStatusInApproval, func(b *BOQ, _ []BOQLine) error { return b.MarkRejected(RejectionReasonScope, "scope mismatch") }, BOQStatusRejected},
		{"rejected -> revision_draft", BOQStatusRejected, func(b *BOQ, _ []BOQLine) error { return b.StartRevision() }, BOQStatusRevisionDraft},
		{"revision_draft -> in_approval (Submit)", BOQStatusRevisionDraft, func(b *BOQ, l []BOQLine) error { return b.Submit(l, uuid.New(), "h2") }, BOQStatusInApproval},
		{"approved -> superseded", BOQStatusApproved, func(b *BOQ, _ []BOQLine) error { return b.Supersede() }, BOQStatusSuperseded},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := newBOQAt(t, tc.from)
			lines := []BOQLine{completeLine(t, b.ID)}
			if err := tc.action(b, lines); err != nil {
				t.Fatalf("action: %v", err)
			}
			if b.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q", b.Status, tc.wantStatus)
			}
		})
	}
}

func TestBOQSM_InvalidTransitions(t *testing.T) {
	cases := []struct {
		name     string
		from     BOQStatus
		action   func(*BOQ, []BOQLine) error
		wantCode string
	}{
		{"in_approval -> in_approval (re-submit)", BOQStatusInApproval, func(b *BOQ, l []BOQLine) error { return b.Submit(l, uuid.New(), "h") }, "boq.invalid_state_transition"},
		{"approved -> rejected", BOQStatusApproved, func(b *BOQ, _ []BOQLine) error { return b.MarkRejected(RejectionReasonOther, "n/a") }, "boq.invalid_state_transition"},
		{"approved -> approved", BOQStatusApproved, func(b *BOQ, _ []BOQLine) error { return b.MarkApproved() }, "boq.invalid_state_transition"},
		{"draft -> approved (skip approval)", BOQStatusDraft, func(b *BOQ, _ []BOQLine) error { return b.MarkApproved() }, "boq.invalid_state_transition"},
		{"draft -> rejected (skip approval)", BOQStatusDraft, func(b *BOQ, _ []BOQLine) error { return b.MarkRejected(RejectionReasonOther, "n/a") }, "boq.invalid_state_transition"},
		{"draft -> revision_draft (no rejection)", BOQStatusDraft, func(b *BOQ, _ []BOQLine) error { return b.StartRevision() }, "boq.invalid_state_transition"},
		{"draft -> superseded", BOQStatusDraft, func(b *BOQ, _ []BOQLine) error { return b.Supersede() }, "boq.invalid_state_transition"},
		{"superseded -> approved", BOQStatusSuperseded, func(b *BOQ, _ []BOQLine) error { return b.MarkApproved() }, "boq.invalid_state_transition"},
		{"rejected without comment", BOQStatusInApproval, func(b *BOQ, _ []BOQLine) error { return b.MarkRejected(RejectionReasonOther, "") }, "boq.rejection_comment_required"},
		{"submit with no lines", BOQStatusDraft, func(b *BOQ, _ []BOQLine) error { return b.Submit(nil, uuid.New(), "h") }, "boq.no_lines"},
		{"submit without template_id", BOQStatusDraft, func(b *BOQ, l []BOQLine) error { return b.Submit(l, uuid.Nil, "h") }, "boq.approval_template_required"},
		{"submit without snapshot_hash", BOQStatusDraft, func(b *BOQ, l []BOQLine) error { return b.Submit(l, uuid.New(), "") }, "boq.snapshot_hash_required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := newBOQAt(t, tc.from)
			lines := []BOQLine{completeLine(t, b.ID)}
			err := tc.action(b, lines)
			if err == nil {
				t.Fatalf("action should have errored; b.Status now %q", b.Status)
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
