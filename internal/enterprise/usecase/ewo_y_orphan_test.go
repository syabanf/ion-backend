package usecase

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/enterprise/domain"
)

// =====================================================================
// Wave 108 — Edge #24: EWO-Y orphan (no matching EWO-X)
//
// When an IC-PO is accepted but no EWO-X exists for the parent
// opportunity (legacy data; or the quotation-accept hook hasn't fired
// yet), the auto-spawn path STILL creates the EWO-Y — just with
// PairedEWOID == nil. A follow-up cron in Wave 96 backfills the
// pairing once an EWO-X appears.
//
// This contract was set in Wave 96. The test pins it at the domain
// level — what matters is that NewEWOY + the constructor + Validate
// produce a structurally-valid EWO-Y without requiring a pair to be
// present.
//
// The full path (autoSpawnEWOYOnICPOAccept) is exercised by the SM
// tests + the auto-accept idempotency tests; this test isolates the
// orphan/no-pair branch so a future refactor that accidentally makes
// PairedEWOID mandatory would fail here loudly.
// =====================================================================

func TestEWOY_Orphan_HasNoPair(t *testing.T) {
	quotationID := uuid.New()
	opportunityID := uuid.New()
	boqVersionID := uuid.New()
	executingSub := uuid.New()
	icPOID := uuid.New()

	ewoY, err := domain.NewEWOY(
		quotationID, opportunityID, boqVersionID,
		executingSub, icPOID,
		domain.GenerateEWONumber(time.Now()),
		"orphan EWO-Y — no EWO-X exists yet",
	)
	if err != nil {
		t.Fatalf("NewEWOY: %v", err)
	}
	if ewoY.Side != domain.EWOSideY {
		t.Errorf("Side = %q, want EWOSideY (y)", ewoY.Side)
	}
	if ewoY.PairedEWOID != nil {
		t.Errorf("PairedEWOID = %v, want nil (orphan; no EWO-X paired)", ewoY.PairedEWOID)
	}
	if ewoY.IntercompanyPOID == nil || *ewoY.IntercompanyPOID != icPOID {
		t.Errorf("IntercompanyPOID = %v, want %v (load-bearing IC-PO link)",
			ewoY.IntercompanyPOID, icPOID)
	}
	if ewoY.ExecutingSubsidiaryID == nil || *ewoY.ExecutingSubsidiaryID != executingSub {
		t.Errorf("ExecutingSubsidiaryID = %v, want %v",
			ewoY.ExecutingSubsidiaryID, executingSub)
	}

	// The Validate() invariant must still pass — a pair-less EWO-Y is a
	// legitimate row, not a malformed one.
	if err := ewoY.Validate(); err != nil {
		t.Errorf("Validate() on orphan EWO-Y returned error: %v — orphan should be legal", err)
	}
}

// TestEWOY_OrphanThenBackfilledPair — a subsequent LinkPair call after
// the orphan was created must succeed and stamp both sides. This is
// what the Wave 96 backfill cron will do once it lands.
func TestEWOY_OrphanThenBackfilledPair(t *testing.T) {
	icPOID := uuid.New()
	ewoY, err := domain.NewEWOY(
		uuid.New(), uuid.New(), uuid.New(),
		uuid.New(), icPOID,
		domain.GenerateEWONumber(time.Now()),
		"orphan",
	)
	if err != nil {
		t.Fatalf("NewEWOY: %v", err)
	}
	if ewoY.PairedEWOID != nil {
		t.Fatalf("setup: orphan EWO-Y should have nil PairedEWOID")
	}

	ewoX, err := domain.NewEWO(
		uuid.New(), uuid.New(), uuid.New(),
		domain.GenerateEWONumber(time.Now()),
		"commercial side",
	)
	if err != nil {
		t.Fatalf("NewEWO: %v", err)
	}
	if ewoX.Side != domain.EWOSideX {
		t.Fatalf("ewoX.Side = %q, want x", ewoX.Side)
	}

	if err := ewoY.LinkPair(ewoX); err != nil {
		t.Fatalf("LinkPair: %v", err)
	}
	if ewoY.PairedEWOID == nil || *ewoY.PairedEWOID != ewoX.ID {
		t.Errorf("ewoY.PairedEWOID = %v, want %v", ewoY.PairedEWOID, ewoX.ID)
	}
	if ewoX.PairedEWOID == nil || *ewoX.PairedEWOID != ewoY.ID {
		t.Errorf("ewoX.PairedEWOID = %v, want %v (symmetric pair stamp missing)",
			ewoX.PairedEWOID, ewoY.ID)
	}
}
