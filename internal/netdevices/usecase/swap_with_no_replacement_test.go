// Wave 120 — swap orchestration edge: no replacement available.
//
// Pins TC-NDL-* "if StageSwap cannot find an in_stock replacement, the
// swap must stay in approved state and the caller must receive a
// Conflict with a clear error code — not a 500 or a leaked
// transition". The existing TestSwapService_StageRejectsNonStockReplacement
// (in swap_test.go) covers the case where the caller passes a
// non-in-stock device id; this test complements it by exercising the
// preconditions check (swap must be approved before staging) and the
// kind-mismatch case (closed in Wave 128B: StageSwap now validates
// replacement.Kind == faulty.Kind).

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

// TestSwapService_StageWithMismatchedKind_FutureContract pins TC-NDL-*
// "an ONT swap must not be staged with a router replacement". Closed
// in Wave 128B: StageSwap now loads the faulty device and refuses with
// `swap.kind_mismatch` when replacement.Kind != faulty.Kind.
func TestSwapService_StageWithMismatchedKind_FutureContract(t *testing.T) {
	ctx := context.Background()
	devRepo := newFakeDeviceRepo()
	swapRepo := newFakeSwapRepo()
	customer := uuid.New()
	approver := uuid.New()

	// Faulty device is an ONT.
	faulty, _ := domain.NewDevice("F-Kind-1", domain.DeviceKindONT, "M", "V")
	_ = devRepo.Create(ctx, faulty)
	// Replacement is a router — wrong kind for the faulty ONT.
	replacement, _ := domain.NewDevice("R-Kind-1", domain.DeviceKindRouter, "M", "V")
	_ = devRepo.Create(ctx, replacement)

	svc := NewSwapService(swapRepo, devRepo, nil, nil, nil)
	swap, err := svc.RequestSwap(ctx, port.RequestSwapInput{
		CustomerID: customer, FaultyDeviceID: faulty.ID, Reason: "ont-down",
	})
	if err != nil {
		t.Fatalf("RequestSwap: %v", err)
	}
	if _, err := svc.ApproveSwap(ctx, swap.ID, approver); err != nil {
		t.Fatalf("ApproveSwap: %v", err)
	}

	_, err = svc.StageSwap(ctx, swap.ID, replacement.ID)
	if err == nil {
		t.Fatalf("StageSwap should fail on kind mismatch (ONT faulty, router replacement)")
	}
	var de *derrors.Error
	if !errors.As(err, &de) || de.Code != "swap.kind_mismatch" {
		t.Errorf("err code = %v, want swap.kind_mismatch", err)
	}
	if errors.As(err, &de) && de.Kind != derrors.KindValidation {
		t.Errorf("err kind = %s, want validation", de.Kind)
	}

	// Replacement must NOT have been allocated — the validator runs before
	// the mutation.
	repl, _ := devRepo.FindByID(ctx, replacement.ID)
	if repl.Status != domain.DeviceStatusInStock {
		t.Errorf("replacement status = %s, want in_stock (validator should not mutate on reject)", repl.Status)
	}
}
