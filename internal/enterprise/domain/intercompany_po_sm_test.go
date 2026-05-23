package domain

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	derrors "github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// Wave 104 — IntercompanyPO state-machine contract tests (TC-SM-ICPO-*)
//
//	draft → issued → accepted  (terminal positive)
//	                  ↘ rejected (terminal negative; reason required)
//	draft | issued → cancelled (terminal admin)
//	issued | accepted → superseded (chained via supersedes_id)
// =====================================================================

func newIntercompanyPOAt(t *testing.T, status IntercompanyPOStatus) *IntercompanyPO {
	t.Helper()
	po, err := NewIntercompanyPO(
		uuid.New(), uuid.New(),
		uuid.New(), uuid.New(),
		"ICPO-1",
	)
	if err != nil {
		t.Fatalf("NewIntercompanyPO: %v", err)
	}
	switch status {
	case IntercompanyPOStatusDraft:
	case IntercompanyPOStatusIssued:
		if err := po.Issue(); err != nil {
			t.Fatalf("Issue: %v", err)
		}
	case IntercompanyPOStatusAccepted:
		if err := po.Issue(); err != nil {
			t.Fatalf("Issue: %v", err)
		}
		uid := uuid.New()
		if err := po.Accept(&uid); err != nil {
			t.Fatalf("Accept: %v", err)
		}
	case IntercompanyPOStatusRejected:
		if err := po.Issue(); err != nil {
			t.Fatalf("Issue: %v", err)
		}
		if err := po.Reject("no capacity"); err != nil {
			t.Fatalf("Reject: %v", err)
		}
	case IntercompanyPOStatusCancelled:
		if err := po.Cancel(); err != nil {
			t.Fatalf("Cancel: %v", err)
		}
	case IntercompanyPOStatusSuperseded:
		if err := po.Issue(); err != nil {
			t.Fatalf("Issue: %v", err)
		}
		if err := po.Supersede(uuid.New()); err != nil {
			t.Fatalf("Supersede: %v", err)
		}
	}
	return po
}

func TestIntercompanyPOSM_ValidTransitions(t *testing.T) {
	uid := uuid.New()
	cases := []struct {
		name       string
		from       IntercompanyPOStatus
		action     func(*IntercompanyPO) error
		wantStatus IntercompanyPOStatus
	}{
		{"draft -> issued", IntercompanyPOStatusDraft, func(p *IntercompanyPO) error { return p.Issue() }, IntercompanyPOStatusIssued},
		{"issued -> accepted", IntercompanyPOStatusIssued, func(p *IntercompanyPO) error { return p.Accept(&uid) }, IntercompanyPOStatusAccepted},
		{"issued -> rejected", IntercompanyPOStatusIssued, func(p *IntercompanyPO) error { return p.Reject("decline") }, IntercompanyPOStatusRejected},
		{"draft -> cancelled", IntercompanyPOStatusDraft, func(p *IntercompanyPO) error { return p.Cancel() }, IntercompanyPOStatusCancelled},
		{"issued -> cancelled", IntercompanyPOStatusIssued, func(p *IntercompanyPO) error { return p.Cancel() }, IntercompanyPOStatusCancelled},
		{"issued -> superseded", IntercompanyPOStatusIssued, func(p *IntercompanyPO) error { return p.Supersede(uuid.New()) }, IntercompanyPOStatusSuperseded},
		{"accepted -> superseded", IntercompanyPOStatusAccepted, func(p *IntercompanyPO) error { return p.Supersede(uuid.New()) }, IntercompanyPOStatusSuperseded},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			po := newIntercompanyPOAt(t, tc.from)
			if err := tc.action(po); err != nil {
				t.Fatalf("action: %v", err)
			}
			if po.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q", po.Status, tc.wantStatus)
			}
		})
	}
}

func TestIntercompanyPOSM_InvalidTransitions(t *testing.T) {
	uid := uuid.New()
	cases := []struct {
		name     string
		from     IntercompanyPOStatus
		action   func(*IntercompanyPO) error
		wantCode string
	}{
		{"draft -> accepted (skip issued)", IntercompanyPOStatusDraft, func(p *IntercompanyPO) error { return p.Accept(&uid) }, "intercompany_po.invalid_state_transition"},
		{"draft -> rejected (no issue)", IntercompanyPOStatusDraft, func(p *IntercompanyPO) error { return p.Reject("n/a") }, "intercompany_po.invalid_state_transition"},
		{"accepted -> issued (backward)", IntercompanyPOStatusAccepted, func(p *IntercompanyPO) error { return p.Issue() }, "intercompany_po.invalid_state_transition"},
		{"accepted -> accepted (double)", IntercompanyPOStatusAccepted, func(p *IntercompanyPO) error { return p.Accept(&uid) }, "intercompany_po.invalid_state_transition"},
		{"accepted -> cancelled", IntercompanyPOStatusAccepted, func(p *IntercompanyPO) error { return p.Cancel() }, "intercompany_po.invalid_state_transition"},
		{"rejected -> accepted", IntercompanyPOStatusRejected, func(p *IntercompanyPO) error { return p.Accept(&uid) }, "intercompany_po.invalid_state_transition"},
		{"cancelled -> issued", IntercompanyPOStatusCancelled, func(p *IntercompanyPO) error { return p.Issue() }, "intercompany_po.invalid_state_transition"},
		{"superseded -> accepted", IntercompanyPOStatusSuperseded, func(p *IntercompanyPO) error { return p.Accept(&uid) }, "intercompany_po.invalid_state_transition"},
		{"draft -> superseded (cannot supersede a draft)", IntercompanyPOStatusDraft, func(p *IntercompanyPO) error { return p.Supersede(uuid.New()) }, "intercompany_po.invalid_state_transition"},
		// Validation errors
		{"reject without reason", IntercompanyPOStatusIssued, func(p *IntercompanyPO) error { return p.Reject("") }, "intercompany_po.reject_reason_required"},
		{"supersede with nil new id", IntercompanyPOStatusIssued, func(p *IntercompanyPO) error { return p.Supersede(uuid.Nil) }, "intercompany_po.supersede_id_required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			po := newIntercompanyPOAt(t, tc.from)
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
