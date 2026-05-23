package usecase

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/netdevices/domain"
	"github.com/ion-core/backend/internal/netdevices/port"
	"github.com/ion-core/backend/pkg/audit"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// SwapService orchestrates the device-swap workflow.
//
// Pattern mirrors enterprise.AcceptCustomerPO's IC-PO fan-out: each
// cross-context side effect happens through a narrow port (WorkOrder
// Creator, RetrofitTrigger) so this package never imports from
// internal/field or internal/warehouse. Audit rows are written on every
// state transition.
type SwapService struct {
	swaps        port.DeviceSwapRepository
	devices      port.DeviceRepository
	woCreator    port.WorkOrderCreator    // optional bridge
	retrofitter  port.RetrofitTrigger     // optional bridge
	audit        audit.Writer
}

func NewSwapService(
	swaps port.DeviceSwapRepository,
	devices port.DeviceRepository,
	wo port.WorkOrderCreator,
	retro port.RetrofitTrigger,
	auditor audit.Writer,
) *SwapService {
	if auditor == nil {
		auditor = audit.Nop{}
	}
	return &SwapService{
		swaps:       swaps,
		devices:     devices,
		woCreator:   wo,
		retrofitter: retro,
		audit:       auditor,
	}
}

// RequestSwap creates the swap header in Requested state. The faulty
// device must exist; we don't change its state yet — the device stays
// operational until the technician physically swaps it.
func (s *SwapService) RequestSwap(ctx context.Context, in port.RequestSwapInput) (*domain.DeviceSwap, error) {
	if _, err := s.devices.FindByID(ctx, in.FaultyDeviceID); err != nil {
		return nil, err
	}
	swap, err := domain.NewDeviceSwap(in.CustomerID, in.FaultyDeviceID, in.Reason, in.FaultEventID, in.RequestedBy)
	if err != nil {
		return nil, err
	}
	if err := s.swaps.Create(ctx, swap); err != nil {
		return nil, err
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		Module: "netdev", RecordType: "netdev.device_swap", RecordID: swap.ID.String(),
		FieldChanged: "status", After: string(swap.Status),
		Reason: "swap_requested",
	})
	return swap, nil
}

// ApproveSwap is the manager gate (per the catalog's RBAC matrix —
// noc_engineer + warehouse_manager hold swap.approve).
func (s *SwapService) ApproveSwap(ctx context.Context, id, by uuid.UUID) (*domain.DeviceSwap, error) {
	swap, err := s.swaps.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	before := string(swap.Status)
	if err := swap.Approve(by); err != nil {
		return nil, err
	}
	if err := s.swaps.UpdateLifecycle(ctx, swap); err != nil {
		return nil, err
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		UserID:       by,
		Module:       "netdev",
		RecordType:   "netdev.device_swap",
		RecordID:     swap.ID.String(),
		FieldChanged: "status",
		Before:       before,
		After:        string(swap.Status),
		Reason:       "swap_approved",
	})
	return swap, nil
}

// StageSwap binds a replacement device from stock. The replacement gets
// allocated to the customer here so a concurrent allocation can't grab
// the same unit.
func (s *SwapService) StageSwap(ctx context.Context, id, replacementDeviceID uuid.UUID) (*domain.DeviceSwap, error) {
	swap, err := s.swaps.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	replacement, err := s.devices.FindByID(ctx, replacementDeviceID)
	if err != nil {
		return nil, err
	}
	if replacement.Status != domain.DeviceStatusInStock {
		return nil, derrors.Conflict(
			"swap.replacement_not_in_stock",
			"replacement device is not in_stock (current: "+string(replacement.Status)+")",
		)
	}
	// Allocate replacement to the swap's customer atomically with the
	// swap-state transition (best-effort — the repo writes both rows in
	// the same caller-side sequence; cross-row atomicity lives in the
	// pgx adapter via a transaction).
	if err := replacement.Allocate(swap.CustomerID, uuid.Nil); err != nil {
		return nil, err
	}
	if err := s.devices.UpdateLifecycle(ctx, replacement); err != nil {
		return nil, err
	}
	before := string(swap.Status)
	if err := swap.Stage(replacementDeviceID); err != nil {
		return nil, err
	}
	if err := s.swaps.UpdateLifecycle(ctx, swap); err != nil {
		return nil, err
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		Module: "netdev", RecordType: "netdev.device_swap", RecordID: swap.ID.String(),
		FieldChanged: "status", Before: before, After: string(swap.Status),
		Reason: "swap_staged",
	})
	return swap, nil
}

// AssignTechnician creates the WO (via the WorkOrderCreator bridge) and
// flips staged → technician_assigned. When the bridge isn't wired (test
// or pre-Wave-113 deployments) we still flip state and stamp a placeholder
// nil WOID — the operator can manually link the WO later.
func (s *SwapService) AssignTechnician(ctx context.Context, swapID, technicianUserID uuid.UUID) (*domain.DeviceSwap, error) {
	swap, err := s.swaps.FindByID(ctx, swapID)
	if err != nil {
		return nil, err
	}
	var woID *uuid.UUID
	if s.woCreator != nil {
		w, werr := s.woCreator.CreateSwapWO(ctx, swap.ID, swap.CustomerID, technicianUserID)
		if werr != nil {
			return nil, werr
		}
		woID = &w
	}
	before := string(swap.Status)
	if err := swap.AssignTechnician(technicianUserID, woID); err != nil {
		return nil, err
	}
	if err := s.swaps.UpdateLifecycle(ctx, swap); err != nil {
		return nil, err
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		Module: "netdev", RecordType: "netdev.device_swap", RecordID: swap.ID.String(),
		FieldChanged: "status", Before: before, After: string(swap.Status),
		Reason: "swap_technician_assigned",
	})
	return swap, nil
}

// CompleteSwap is the linchpin: it flips the swap to swapped, the
// faulty device to decommissioned, the replacement to active, and
// records the warehouse retrofit pair via the bridge. We don't make
// the retrofit atomic with the swap — same forward-only semantics as
// enterprise.AcceptCustomerPO's IC-PO fan-out: if the retrofit fails
// the swap stays at swapped and the operator can retry the retrofit
// via a follow-up endpoint.
func (s *SwapService) CompleteSwap(ctx context.Context, swapID uuid.UUID) (*domain.DeviceSwap, error) {
	swap, err := s.swaps.FindByID(ctx, swapID)
	if err != nil {
		return nil, err
	}
	if swap.ReplacementDeviceID == nil {
		return nil, derrors.Conflict(
			"swap.no_replacement",
			"swap has no replacement device bound; stage it first",
		)
	}

	now := time.Now().UTC()

	// Faulty → Decommissioned.
	faulty, err := s.devices.FindByID(ctx, swap.FaultyDeviceID)
	if err != nil {
		return nil, err
	}
	if err := faulty.Decommission(now); err != nil {
		return nil, err
	}
	if err := s.devices.UpdateLifecycle(ctx, faulty); err != nil {
		return nil, err
	}

	// Replacement → Commissioned → Active.
	replacement, err := s.devices.FindByID(ctx, *swap.ReplacementDeviceID)
	if err != nil {
		return nil, err
	}
	// If still allocated (the normal staged state), advance it.
	if replacement.Status == domain.DeviceStatusAllocated {
		if err := replacement.Commission(now); err != nil {
			return nil, err
		}
	}
	if replacement.Status == domain.DeviceStatusCommissioned {
		if err := replacement.Activate(); err != nil {
			return nil, err
		}
	}
	if err := s.devices.UpdateLifecycle(ctx, replacement); err != nil {
		return nil, err
	}

	// Warehouse retrofit bridge — non-fatal on failure.
	var retrofitID *uuid.UUID
	if s.retrofitter != nil {
		rid, rerr := s.retrofitter.CreateRetrofitForSwap(ctx, swap.ID, faulty.ID, replacement.ID)
		if rerr == nil {
			retrofitID = &rid
		}
		// errors swallowed — the swap completes regardless, the operator
		// can re-trigger the retrofit via the warehouse admin surface.
	}

	before := string(swap.Status)
	if err := swap.Complete(retrofitID, now); err != nil {
		return nil, err
	}
	if err := s.swaps.UpdateLifecycle(ctx, swap); err != nil {
		return nil, err
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		Module: "netdev", RecordType: "netdev.device_swap", RecordID: swap.ID.String(),
		FieldChanged: "status", Before: before, After: string(swap.Status),
		Reason: "swap_completed",
	})
	return swap, nil
}

// CloseSwap finalises a successful swap into the closed terminal state.
func (s *SwapService) CloseSwap(ctx context.Context, swapID uuid.UUID) (*domain.DeviceSwap, error) {
	swap, err := s.swaps.FindByID(ctx, swapID)
	if err != nil {
		return nil, err
	}
	before := string(swap.Status)
	if err := swap.Close(); err != nil {
		return nil, err
	}
	if err := s.swaps.UpdateLifecycle(ctx, swap); err != nil {
		return nil, err
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		Module: "netdev", RecordType: "netdev.device_swap", RecordID: swap.ID.String(),
		FieldChanged: "status", Before: before, After: string(swap.Status),
		Reason: "swap_closed",
	})
	return swap, nil
}

// GetSwap is the read-through.
func (s *SwapService) GetSwap(ctx context.Context, id uuid.UUID) (*domain.DeviceSwap, error) {
	return s.swaps.FindByID(ctx, id)
}

// ListSwaps returns swaps filtered by status / customer.
func (s *SwapService) ListSwaps(ctx context.Context, status string, customerID *uuid.UUID, limit, offset int) ([]domain.DeviceSwap, int, error) {
	return s.swaps.List(ctx, status, customerID, limit, offset)
}
