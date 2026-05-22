package domain

import (
	"testing"

	"github.com/google/uuid"
)

// Wave 75 P1 — tests pinning the lead status-flow fixes from QA TC-CRM-013.
// Prior build let any status mutation through (hot→new and even
// converted→new). Coverage check also auto-flipped to Potential on
// excess_distance — QA correctly flagged this as confusing.

func TestNewLead_DefaultsToNew(t *testing.T) {
	l, err := NewLead("Budi", "0811", "Jakarta")
	if err != nil {
		t.Fatalf("NewLead: %v", err)
	}
	if l.Status != LeadStatusNew {
		t.Fatalf("expected status=new on creation, got %s", l.Status)
	}
}

func TestApplyCoverage_ExcessDoesNotAutoFlipToPotential(t *testing.T) {
	l, err := NewLead("Budi", "0811", "Jakarta")
	if err != nil {
		t.Fatalf("NewLead: %v", err)
	}
	// Coverage check finds excess distance — sales rep hasn't acted yet.
	excess := 250.0
	l.ApplyCoverage(CoverageVerdictExcess, []byte(`{}`), nil, &excess, nil, nil, false)

	// QA TC-CRM-013: status MUST stay at 'new'. The rep marks Potential
	// explicitly via MarkPotential() — coverage alone never moves the
	// pipeline.
	if l.Status != LeadStatusNew {
		t.Fatalf("excess coverage must not auto-flip status; got %s, want new", l.Status)
	}
	if l.CoverageVerdict == nil || *l.CoverageVerdict != CoverageVerdictExcess {
		t.Fatalf("coverage verdict not captured")
	}
}

func TestMarkPotential_ExplicitOnly(t *testing.T) {
	l, _ := NewLead("Budi", "0811", "Jakarta")
	if err := l.MarkPotential(); err != nil {
		t.Fatalf("MarkPotential from new: %v", err)
	}
	if l.Status != LeadStatusPotential {
		t.Fatalf("after MarkPotential, status=%s want potential", l.Status)
	}
}

func TestCanTransitionTo_ForwardOnly(t *testing.T) {
	type tc struct {
		from, to LeadStatus
		wantErr  bool
	}
	cases := []tc{
		// Allowed forward steps from new.
		{LeadStatusNew, LeadStatusActive, false},
		{LeadStatusNew, LeadStatusHot, false},
		{LeadStatusNew, LeadStatusLost, false},
		{LeadStatusNew, LeadStatusPotential, false},

		// Same-status no-op is allowed (idempotent PATCH).
		{LeadStatusNew, LeadStatusNew, false},
		{LeadStatusHot, LeadStatusHot, false},

		// Forward through pipeline.
		{LeadStatusActive, LeadStatusWarm, false},
		{LeadStatusWarm, LeadStatusHot, false},
		{LeadStatusHot, LeadStatusConverted, false},

		// QA-flagged regressions — these MUST fail.
		{LeadStatusConverted, LeadStatusNew, true}, // un-convert
		{LeadStatusConverted, LeadStatusHot, true}, // un-convert sideways
		{LeadStatusHot, LeadStatusNew, true},       // backwards from hot
		{LeadStatusWarm, LeadStatusNew, true},      // backwards from warm
		{LeadStatusLost, LeadStatusHot, true},      // resurrect from lost
		{LeadStatusPotential, LeadStatusNew, true}, // backwards from potential
	}
	for _, c := range cases {
		l := &Lead{ID: uuid.New(), Status: c.from}
		err := l.CanTransitionTo(c.to)
		if (err != nil) != c.wantErr {
			t.Errorf("CanTransitionTo(%s → %s): err=%v, wantErr=%v", c.from, c.to, err, c.wantErr)
		}
	}
}

func TestNewLead_DefaultsToBroadbandType(t *testing.T) {
	l, err := NewLead("Budi", "0811", "Jakarta")
	if err != nil {
		t.Fatalf("NewLead: %v", err)
	}
	// Wave 76 (TC-CRM-002): every lead has a type; broadband by default.
	if l.LeadType != LeadTypeBroadband {
		t.Fatalf("expected LeadType=broadband, got %s", l.LeadType)
	}
}

func TestIsValidLeadSource_FullSet(t *testing.T) {
	// Wave 76 (TC-CRM-006): the broadband path must accept all 14
	// source values, not just the legacy 5.
	cases := []struct {
		src  LeadSource
		want bool
	}{
		{LeadSourceManual, true},
		{LeadSourceSelfOrder, true},
		{LeadSourceSalesApp, true},
		{LeadSourceReferral, true},
		{LeadSourceCSReferral, true},
		{LeadSourceColdCall, true},
		{LeadSourceWebsite, true},
		{LeadSourceWhatsapp, true},
		{LeadSourceSocialMediaDM, true},
		{LeadSourceVoipCall, true},
		{LeadSourceLineCall, true},
		{LeadSourceWalkIn, true},
		{LeadSourceEvent, true},
		{LeadSourcePartner, true},
		{LeadSource("bogus"), false},
		{LeadSource(""), false},
	}
	for _, c := range cases {
		got := IsValidLeadSource(c.src)
		if got != c.want {
			t.Errorf("IsValidLeadSource(%q) = %v, want %v", c.src, got, c.want)
		}
	}
	// Sanity: all listed in AllLeadSources are also IsValid.
	for _, s := range AllLeadSources {
		if !IsValidLeadSource(s) {
			t.Errorf("AllLeadSources lists %q but IsValidLeadSource rejects it", s)
		}
	}
}

func TestCanConvert_ConsistentWithTransitions(t *testing.T) {
	// CanConvert and CanTransitionTo(Converted) should agree on
	// convertibility for in-flight statuses. They diverge for already-
	// converted (CanConvert errs to prevent double-create; CanTransitionTo
	// returns nil because same-status is a no-op PATCH) and for
	// Potential/Qualified (CanConvert short-circuits these per legacy
	// flow; CanTransitionTo requires the rep go via Hot first).
	for _, s := range []LeadStatus{
		LeadStatusNew, LeadStatusActive, LeadStatusWarm,
		LeadStatusHot, LeadStatusLost,
	} {
		l := &Lead{ID: uuid.New(), Status: s}
		ccErr := l.CanConvert()
		ttErr := l.CanTransitionTo(LeadStatusConverted)
		if (ccErr == nil) != (ttErr == nil) {
			t.Errorf("status=%s: CanConvert=%v, CanTransitionTo(converted)=%v", s, ccErr, ttErr)
		}
	}
}
