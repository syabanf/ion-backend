package domain

import (
	"errors"
	"testing"

	derrors "github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// Wave 104 — Opportunity state-machine contract tests (TC-SM-OPP-*)
//
// The state machine (per opportunity.go top-of-file diagram):
//
//	cold → warm → hot → won
//	      ↓     ↓
//	     lost  lost  (terminal; reason+code required)
//
// Tests are table-driven: each row drives a single transition method
// against a freshly-constructed opportunity in a known stage and
// asserts the resulting stage + error contract.
// =====================================================================

// newOppAt constructs an Opportunity and forces it into a target stage
// by replaying the canonical state machine. Avoids reaching into the
// struct fields directly so tests exercise the same code path users hit.
func newOppAt(t *testing.T, stage OpportunityStage) *Opportunity {
	t.Helper()
	op, err := NewOpportunity("Test Co")
	if err != nil {
		t.Fatalf("NewOpportunity: %v", err)
	}
	switch stage {
	case OpportunityStageCold:
		// already there
	case OpportunityStageWarm:
		if err := op.AdvanceToWarm(); err != nil {
			t.Fatalf("AdvanceToWarm: %v", err)
		}
	case OpportunityStageHot:
		if err := op.AdvanceToWarm(); err != nil {
			t.Fatalf("AdvanceToWarm: %v", err)
		}
		if err := op.AdvanceToHot(); err != nil {
			t.Fatalf("AdvanceToHot: %v", err)
		}
	case OpportunityStageWon:
		if err := op.AdvanceToWarm(); err != nil {
			t.Fatalf("AdvanceToWarm: %v", err)
		}
		if err := op.AdvanceToHot(); err != nil {
			t.Fatalf("AdvanceToHot: %v", err)
		}
		if err := op.MarkWon("PO-123"); err != nil {
			t.Fatalf("MarkWon: %v", err)
		}
	case OpportunityStageLost:
		if err := op.MarkLost(LostReasonOther, "no budget", false); err != nil {
			t.Fatalf("MarkLost: %v", err)
		}
	}
	return op
}

func TestOpportunitySM_ValidTransitions(t *testing.T) {
	cases := []struct {
		name      string
		from      OpportunityStage
		action    func(*Opportunity) error
		wantStage OpportunityStage
	}{
		{"cold -> warm", OpportunityStageCold, func(o *Opportunity) error { return o.AdvanceToWarm() }, OpportunityStageWarm},
		{"warm -> hot", OpportunityStageWarm, func(o *Opportunity) error { return o.AdvanceToHot() }, OpportunityStageHot},
		{"hot -> won", OpportunityStageHot, func(o *Opportunity) error { return o.MarkWon("PO-1") }, OpportunityStageWon},
		{"cold -> lost", OpportunityStageCold, func(o *Opportunity) error { return o.MarkLost(LostReasonPrice, "too expensive", false) }, OpportunityStageLost},
		{"warm -> lost", OpportunityStageWarm, func(o *Opportunity) error { return o.MarkLost(LostReasonCompetitor, "lost to incumbent", false) }, OpportunityStageLost},
		{"hot -> lost (auto)", OpportunityStageHot, func(o *Opportunity) error { return o.MarkLost(LostReasonStageTimeout, "SLA expired", true) }, OpportunityStageLost},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			op := newOppAt(t, tc.from)
			if err := tc.action(op); err != nil {
				t.Fatalf("action: %v", err)
			}
			if op.Stage != tc.wantStage {
				t.Errorf("stage = %q, want %q", op.Stage, tc.wantStage)
			}
		})
	}
}

func TestOpportunitySM_InvalidTransitions(t *testing.T) {
	cases := []struct {
		name     string
		from     OpportunityStage
		action   func(*Opportunity) error
		wantCode string
	}{
		// Backward / skip-ahead transitions
		{"warm -> warm (already advanced)", OpportunityStageWarm, func(o *Opportunity) error { return o.AdvanceToWarm() }, "opportunity.invalid_state_transition"},
		{"cold -> hot (skip warm)", OpportunityStageCold, func(o *Opportunity) error { return o.AdvanceToHot() }, "opportunity.invalid_state_transition"},
		{"cold -> won (skip everything)", OpportunityStageCold, func(o *Opportunity) error { return o.MarkWon("PO-1") }, "opportunity.invalid_state_transition"},
		{"warm -> won (skip hot)", OpportunityStageWarm, func(o *Opportunity) error { return o.MarkWon("PO-1") }, "opportunity.invalid_state_transition"},
		// Terminal-stage immutability
		{"won -> lost", OpportunityStageWon, func(o *Opportunity) error { return o.MarkLost(LostReasonOther, "n/a", false) }, "opportunity.invalid_state_transition"},
		{"lost -> warm", OpportunityStageLost, func(o *Opportunity) error { return o.AdvanceToWarm() }, "opportunity.invalid_state_transition"},
		{"lost -> lost", OpportunityStageLost, func(o *Opportunity) error { return o.MarkLost(LostReasonOther, "n/a", false) }, "opportunity.invalid_state_transition"},
		// Action-validation failures
		{"hot -> won without poRef", OpportunityStageHot, func(o *Opportunity) error { return o.MarkWon("") }, "opportunity.po_reference_required"},
		{"cold -> lost missing code", OpportunityStageCold, func(o *Opportunity) error { return o.MarkLost(LostReasonNone, "no reason given", false) }, "opportunity.lost_reason_code_invalid"},
		{"cold -> lost missing free-text reason", OpportunityStageCold, func(o *Opportunity) error { return o.MarkLost(LostReasonPrice, "", false) }, "opportunity.lost_reason_required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			op := newOppAt(t, tc.from)
			err := tc.action(op)
			if err == nil {
				t.Fatalf("action should have returned error; opp stage now %q", op.Stage)
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
