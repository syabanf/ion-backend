package domain

import (
	"errors"
	"testing"

	derrors "github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// Wave 107 — Provider state-machine contract tests (TC-SM-PROV-*).
//
//	pending → active (requires KYC complete)
//	active  ↔ suspended (reason required to suspend)
//	any non-terminal → blacklisted (terminal; reason required)
// =====================================================================

func newProviderAt(t *testing.T, status ProviderStatus, kyc bool) *Provider {
	t.Helper()
	p, err := NewProvider("Test Provider", "01.234.567.8-901.000", "ops@test.id", "")
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	if kyc {
		p.CompleteKYC()
	}
	switch status {
	case ProviderStatusPending:
		// default
	case ProviderStatusActive:
		if !kyc {
			t.Fatal("test setup: active requires kyc")
		}
		if err := p.Activate(); err != nil {
			t.Fatalf("Activate: %v", err)
		}
	case ProviderStatusSuspended:
		if !kyc {
			t.Fatal("test setup: suspended path needs kyc")
		}
		if err := p.Activate(); err != nil {
			t.Fatalf("Activate: %v", err)
		}
		if err := p.Suspend("payment overdue"); err != nil {
			t.Fatalf("Suspend: %v", err)
		}
	case ProviderStatusBlacklisted:
		if err := p.Blacklist("ethics breach"); err != nil {
			t.Fatalf("Blacklist: %v", err)
		}
	}
	return p
}

// TestProviderSM_ActivateRequiresKYC — TC-SM-PROV-001.
func TestProviderSM_ActivateRequiresKYC(t *testing.T) {
	p := newProviderAt(t, ProviderStatusPending, false)
	err := p.Activate()
	if err == nil {
		t.Fatal("Activate without KYC should fail")
	}
	var de *derrors.Error
	if !errors.As(err, &de) {
		t.Fatalf("err type: %T", err)
	}
	if de.Code != "provider.kyc_required" {
		t.Errorf("code = %q, want provider.kyc_required", de.Code)
	}
}

// TestProviderSM_PendingToActive_HappyPath — TC-SM-PROV-002.
func TestProviderSM_PendingToActive_HappyPath(t *testing.T) {
	p := newProviderAt(t, ProviderStatusPending, true)
	if err := p.Activate(); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if p.Status != ProviderStatusActive {
		t.Errorf("status = %q, want active", p.Status)
	}
}

// TestProviderSM_ActiveToSuspended_RequiresReason — TC-SM-PROV-003.
func TestProviderSM_ActiveToSuspended_RequiresReason(t *testing.T) {
	p := newProviderAt(t, ProviderStatusActive, true)
	err := p.Suspend("   ")
	if err == nil {
		t.Fatal("Suspend with empty reason should fail")
	}
	var de *derrors.Error
	if !errors.As(err, &de) {
		t.Fatalf("err type: %T", err)
	}
	if de.Code != "provider.suspend_reason_required" {
		t.Errorf("code = %q, want provider.suspend_reason_required", de.Code)
	}
}

// TestProviderSM_SuspendedReactivate_HappyPath — TC-SM-PROV-004.
func TestProviderSM_SuspendedReactivate_HappyPath(t *testing.T) {
	p := newProviderAt(t, ProviderStatusSuspended, true)
	if err := p.Reactivate(); err != nil {
		t.Fatalf("Reactivate: %v", err)
	}
	if p.Status != ProviderStatusActive {
		t.Errorf("status = %q, want active", p.Status)
	}
	if p.SuspendedAt != nil || p.SuspendedReason != "" {
		t.Errorf("suspension fields not cleared on reactivate: %+v", p)
	}
}

// TestProviderSM_BlacklistTerminal — TC-SM-PROV-005.
func TestProviderSM_BlacklistTerminal(t *testing.T) {
	p := newProviderAt(t, ProviderStatusBlacklisted, false)
	// Cannot Reactivate / Activate / Suspend from terminal.
	if err := p.Activate(); err == nil {
		t.Error("Activate from blacklisted should fail")
	}
	if err := p.Reactivate(); err == nil {
		t.Error("Reactivate from blacklisted should fail")
	}
	if err := p.Suspend("reason"); err == nil {
		t.Error("Suspend from blacklisted should fail")
	}
	// Blacklist itself is idempotent.
	if err := p.Blacklist("further detail"); err != nil {
		t.Errorf("Blacklist idempotent should pass: %v", err)
	}
}

// TestProviderSM_ActivateIdempotent — TC-SM-PROV-006.
func TestProviderSM_ActivateIdempotent(t *testing.T) {
	p := newProviderAt(t, ProviderStatusActive, true)
	if err := p.Activate(); err != nil {
		t.Errorf("Activate idempotent should pass: %v", err)
	}
}

// TestProviderSM_SuspendFromPending — TC-SM-PROV-007.
func TestProviderSM_SuspendFromPending(t *testing.T) {
	p := newProviderAt(t, ProviderStatusPending, true)
	err := p.Suspend("reason")
	if err == nil {
		t.Fatal("Suspend from pending should fail")
	}
	var de *derrors.Error
	if !errors.As(err, &de) {
		t.Fatalf("err type: %T", err)
	}
	if de.Code != "provider.cannot_suspend" {
		t.Errorf("code = %q, want provider.cannot_suspend", de.Code)
	}
}

// TestProviderSM_ReactivateFromActiveIdempotent — TC-SM-PROV-008.
func TestProviderSM_ReactivateFromActiveIdempotent(t *testing.T) {
	p := newProviderAt(t, ProviderStatusActive, true)
	if err := p.Reactivate(); err != nil {
		t.Errorf("Reactivate on active should be idempotent: %v", err)
	}
}
