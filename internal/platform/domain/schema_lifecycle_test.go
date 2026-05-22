package domain

import (
	"encoding/json"
	"testing"
)

// Wave 79 — pin the new schema lifecycle: draft → submitted → approved
// → published, with rejected as a back-edge to draft. The pre-Wave-79
// path (draft → published direct) is now gated.

func newDraftFixture(t *testing.T) *SchemaDefinition {
	t.Helper()
	s, err := NewSchema(SchemaKindBilling, "TEST", "Test schema", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("NewSchema: %v", err)
	}
	return s
}

func TestSubmit_FromDraftSucceeds(t *testing.T) {
	s := newDraftFixture(t)
	if err := s.SubmitForApproval(); err != nil {
		t.Fatalf("SubmitForApproval: %v", err)
	}
	if s.Status != SchemaStatusSubmitted {
		t.Fatalf("status=%s want submitted", s.Status)
	}
	if s.SubmittedAt == nil {
		t.Fatalf("SubmittedAt was not stamped")
	}
}

func TestSubmit_FromNonDraftFails(t *testing.T) {
	s := newDraftFixture(t)
	_ = s.SubmitForApproval()
	// Submitting twice should be rejected.
	if err := s.SubmitForApproval(); err == nil {
		t.Fatalf("expected error on second submit, got nil")
	}
}

func TestPublish_DirectFromDraftBlocked(t *testing.T) {
	// Wave 79 (TC-SCH-008): the pre-Wave-79 contract allowed
	// draft → published. Now domain refuses; usecase handles the
	// "no approvers configured" bypass.
	s := newDraftFixture(t)
	if err := s.Publish(); err == nil {
		t.Fatalf("expected publish-from-draft to be blocked, got nil")
	}
	if s.Status != SchemaStatusDraft {
		t.Fatalf("status leaked to %s after blocked publish", s.Status)
	}
}

func TestApprove_HappyPath(t *testing.T) {
	s := newDraftFixture(t)
	if err := s.SubmitForApproval(); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if err := s.Approve(); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if s.Status != SchemaStatusApproved {
		t.Fatalf("status=%s want approved", s.Status)
	}
	if s.ApprovedAt == nil {
		t.Fatalf("ApprovedAt was not stamped")
	}
}

func TestApprove_FromDraftFails(t *testing.T) {
	// Cannot approve a schema that hasn't been submitted.
	s := newDraftFixture(t)
	if err := s.Approve(); err == nil {
		t.Fatalf("expected error on approve-from-draft, got nil")
	}
}

func TestPublish_FromApprovedSucceeds(t *testing.T) {
	s := newDraftFixture(t)
	_ = s.SubmitForApproval()
	_ = s.Approve()
	if err := s.Publish(); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if s.Status != SchemaStatusPublished {
		t.Fatalf("status=%s want published", s.Status)
	}
	if s.PublishedAt == nil {
		t.Fatalf("PublishedAt was not stamped")
	}
}

func TestReject_FlipsBackToDraftWithReason(t *testing.T) {
	// Wave 79 (TC-SCH-009): rejection captures a reason AND flips back
	// to draft so the operator can edit + resubmit.
	s := newDraftFixture(t)
	_ = s.SubmitForApproval()
	if err := s.Reject("late fee too high"); err != nil {
		t.Fatalf("Reject: %v", err)
	}
	if s.Status != SchemaStatusDraft {
		t.Fatalf("after Reject, status=%s want draft", s.Status)
	}
	if s.RejectionReason != "late fee too high" {
		t.Fatalf("rejection_reason=%q want %q", s.RejectionReason, "late fee too high")
	}
}

func TestReject_RequiresReason(t *testing.T) {
	s := newDraftFixture(t)
	_ = s.SubmitForApproval()
	for _, empty := range []string{"", "   ", "\n\t"} {
		if err := s.Reject(empty); err == nil {
			t.Errorf("expected error for empty reason %q, got nil", empty)
		}
	}
}

func TestReject_OnlyValidFromSubmitted(t *testing.T) {
	s := newDraftFixture(t)
	// Reject from draft state.
	if err := s.Reject("some reason"); err == nil {
		t.Fatalf("expected error on reject-from-draft, got nil")
	}
	// Reject after publish.
	_ = s.SubmitForApproval()
	_ = s.Approve()
	_ = s.Publish()
	if err := s.Reject("some reason"); err == nil {
		t.Fatalf("expected error on reject-from-published, got nil")
	}
}

func TestPublish_StillIdempotentOnPublished(t *testing.T) {
	// Pre-Wave-79 contract: Publish() on an already-published schema
	// is a no-op (idempotent). Preserved.
	s := newDraftFixture(t)
	_ = s.SubmitForApproval()
	_ = s.Approve()
	_ = s.Publish()
	if err := s.Publish(); err != nil {
		t.Fatalf("second Publish should be idempotent, got %v", err)
	}
}

func TestSupersede_StillBlocksDirectFromDraft(t *testing.T) {
	// Pre-Wave-79: Supersede only from published. Preserved.
	s := newDraftFixture(t)
	if err := s.Supersede(); err == nil {
		t.Fatalf("expected error on supersede-from-draft, got nil")
	}
}
