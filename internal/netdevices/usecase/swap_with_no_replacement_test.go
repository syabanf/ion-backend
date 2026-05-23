// Wave 120 — swap orchestration edge: no replacement available.
//
// Pins TC-NDL-* "if StageSwap cannot find an in_stock replacement, the
// swap must stay in approved state and the caller must receive a
// Conflict with a clear error code — not a 500 or a leaked
// transition". The existing TestSwapService_StageRejectsNonStockReplacement
// (in swap_test.go) covers the case where the caller passes a
// non-in-stock device id; this test complements it by exercising the
// preconditions check (swap must be approved before staging) and the
// kind-mismatch case (which is currently NOT enforced — pinned as a
// future gap below).

package usecase

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/netdevices/domain"
	"github.com/ion-core/backend/internal/netdevices/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

func TestSwapService_StageBeforeApprove_RefusesTransition(t *testing.T) {
	ctx := context.Background()
	devRepo := newFakeDeviceRepo()
	swapRepo := newFakeSwapRepo()
	customer := uuid.New()

	faulty, _ := domain.NewDevice("F-StageEarly", domain.DeviceKindONT, "M", "V")
	_ = devRepo.Create(ctx, faulty)
	replacement, _ := domain.NewDevice("R-StageEarly", domain.DeviceKindONT, "M", "V")
	_ = devRepo.Create(ctx, replacement)

	svc := NewSwapService(swapRepo, devRepo, nil, nil, nil)
	swap, err := svc.RequestSwap(ctx, port.RequestSwapInput{
		CustomerID: customer, FaultyDeviceID: faulty.ID, Reason: "x",
	})
	if err != nil {
		t.Fatalf("RequestSwap: %v", err)
	}
	// Skip approve — try to stage. The domain SM refuses requested → staged
	// (must be approved → staged).
	_, err = svc.StageSwap(ctx, swap.ID, replacement.ID)
	if err == nil {
		t.Fatalf("StageSwap from requested should fail")
	}
	var de *derrors.Error
	if errors.As(err, &de) {
		// Domain transition refuses with `swap.bad_state`; replacement
		// already-allocated check happens before SM transition so we may
		// see either code depending on order. We just require it to be a
		// Conflict.
		if de.Kind != derrors.KindConflict {
			t.Errorf("err kind = %s, want conflict; full = %v", de.Kind, err)
		}
	} else {
		t.Errorf("err = %v, want a derrors.Error", err)
	}
}

// TestSwapService_StageWithMismatchedKind_FutureContract pins the
// future TC-NDL-* "an ONT swap should not be staged with a router
// replacement". StageSwap today does NOT verify replacement.Kind ==
// faulty.Kind; the test is skipped until the domain rule lands. The
// catalog row reference is the swap subsection of TC-NDL.
func TestSwapService_StageWithMismatchedKind_FutureContract(t *testing.T) {
	t.Skip("Wave 120 pin — kind-match enforcement is a future enhancement; " +
		"see docs/wave-120-100pct-broadband-compliance-report.md §3e " +
		"(catalog gap: swap kind-match validator).")
}
