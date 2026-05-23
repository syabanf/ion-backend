package usecase

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/netdevices/domain"
	"github.com/ion-core/backend/internal/netdevices/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// In-memory fakes for the swap-orchestrator test. Each fake stores the
// aggregate in a map keyed by id so we can verify cross-aggregate side
// effects after a swap flow runs.
// =====================================================================

type fakeDeviceRepo struct {
	mu       sync.Mutex
	store    map[uuid.UUID]*domain.Device
	bySerial map[string]uuid.UUID
}

func newFakeDeviceRepo() *fakeDeviceRepo {
	return &fakeDeviceRepo{
		store:    map[uuid.UUID]*domain.Device{},
		bySerial: map[string]uuid.UUID{},
	}
}

func (r *fakeDeviceRepo) Create(_ context.Context, d *domain.Device) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.bySerial[d.SerialNo]; ok {
		return derrors.Conflict("device.duplicate", "serial exists")
	}
	cp := *d
	r.store[d.ID] = &cp
	r.bySerial[d.SerialNo] = d.ID
	return nil
}
func (r *fakeDeviceRepo) FindByID(_ context.Context, id uuid.UUID) (*domain.Device, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.store[id]
	if !ok {
		return nil, derrors.NotFound("device.not_found", "device not found")
	}
	cp := *d
	return &cp, nil
}
func (r *fakeDeviceRepo) FindBySerial(_ context.Context, serialNo string) (*domain.Device, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.bySerial[serialNo]
	if !ok {
		return nil, derrors.NotFound("device.not_found", "device not found")
	}
	cp := *r.store[id]
	return &cp, nil
}
func (r *fakeDeviceRepo) UpdateLifecycle(_ context.Context, d *domain.Device) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.store[d.ID]; !ok {
		return derrors.NotFound("device.not_found", "device not found")
	}
	cp := *d
	r.store[d.ID] = &cp
	return nil
}
func (r *fakeDeviceRepo) List(_ context.Context, _ port.DeviceListFilter) ([]domain.Device, int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []domain.Device{}
	for _, d := range r.store {
		out = append(out, *d)
	}
	return out, len(out), nil
}
func (r *fakeDeviceRepo) FindFirstAvailable(_ context.Context, _ domain.DeviceKind, _ string, _ *uuid.UUID) (*domain.Device, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, d := range r.store {
		if d.Status == domain.DeviceStatusInStock {
			cp := *d
			return &cp, nil
		}
	}
	return nil, derrors.NotFound("device.not_found", "no in-stock device")
}

type fakeSwapRepo struct {
	mu    sync.Mutex
	store map[uuid.UUID]*domain.DeviceSwap
}

func newFakeSwapRepo() *fakeSwapRepo {
	return &fakeSwapRepo{store: map[uuid.UUID]*domain.DeviceSwap{}}
}

func (r *fakeSwapRepo) Create(_ context.Context, s *domain.DeviceSwap) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *s
	r.store[s.ID] = &cp
	return nil
}
func (r *fakeSwapRepo) FindByID(_ context.Context, id uuid.UUID) (*domain.DeviceSwap, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.store[id]
	if !ok {
		return nil, derrors.NotFound("swap.not_found", "swap not found")
	}
	cp := *s
	return &cp, nil
}
func (r *fakeSwapRepo) UpdateLifecycle(_ context.Context, s *domain.DeviceSwap) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *s
	r.store[s.ID] = &cp
	return nil
}
func (r *fakeSwapRepo) List(_ context.Context, _ string, _ *uuid.UUID, _, _ int) ([]domain.DeviceSwap, int, error) {
	return nil, 0, nil
}

type fakeWOCreator struct {
	called bool
	failWith error
	lastSwapID uuid.UUID
	lastTechID uuid.UUID
}

func (f *fakeWOCreator) CreateSwapWO(_ context.Context, swapID, customerID, techID uuid.UUID) (uuid.UUID, error) {
	if f.failWith != nil {
		return uuid.Nil, f.failWith
	}
	f.called = true
	f.lastSwapID = swapID
	_ = customerID
	f.lastTechID = techID
	return uuid.New(), nil
}

type fakeRetrofitter struct {
	called bool
	lastOld uuid.UUID
	lastNew uuid.UUID
}

func (f *fakeRetrofitter) CreateRetrofitForSwap(_ context.Context, swapID, oldID, newID uuid.UUID) (uuid.UUID, error) {
	f.called = true
	_ = swapID
	f.lastOld = oldID
	f.lastNew = newID
	return uuid.New(), nil
}

// =====================================================================
// Happy path — full Request → Approve → Stage → Assign → Complete
// =====================================================================

func TestSwapService_FullHappyPath(t *testing.T) {
	ctx := context.Background()

	devRepo := newFakeDeviceRepo()
	swapRepo := newFakeSwapRepo()
	woc := &fakeWOCreator{}
	retro := &fakeRetrofitter{}

	customer := uuid.New()
	approver := uuid.New()
	tech := uuid.New()

	faulty, _ := domain.NewDevice("FAULTY-001", domain.DeviceKindONT, "M", "V")
	if err := faulty.Allocate(customer, uuid.New()); err != nil {
		t.Fatalf("allocate faulty: %v", err)
	}
	if err := faulty.Commission(time.Now().UTC()); err != nil {
		t.Fatalf("commission faulty: %v", err)
	}
	if err := faulty.Activate(); err != nil {
		t.Fatalf("activate faulty: %v", err)
	}
	if err := devRepo.Create(ctx, faulty); err != nil {
		t.Fatalf("save faulty: %v", err)
	}

	replacement, _ := domain.NewDevice("REPL-001", domain.DeviceKindONT, "M", "V")
	if err := devRepo.Create(ctx, replacement); err != nil {
		t.Fatalf("save replacement: %v", err)
	}

	svc := NewSwapService(swapRepo, devRepo, woc, retro, nil)

	// Request.
	swap, err := svc.RequestSwap(ctx, port.RequestSwapInput{
		CustomerID:     customer,
		FaultyDeviceID: faulty.ID,
		Reason:         "ONT not booting",
	})
	if err != nil {
		t.Fatalf("RequestSwap: %v", err)
	}
	if swap.Status != domain.SwapStatusRequested {
		t.Errorf("status = %s, want requested", swap.Status)
	}

	// Approve.
	if _, err := svc.ApproveSwap(ctx, swap.ID, approver); err != nil {
		t.Fatalf("ApproveSwap: %v", err)
	}

	// Stage — must allocate the replacement.
	if _, err := svc.StageSwap(ctx, swap.ID, replacement.ID); err != nil {
		t.Fatalf("StageSwap: %v", err)
	}
	// Verify replacement was allocated.
	upd, _ := devRepo.FindByID(ctx, replacement.ID)
	if upd.Status != domain.DeviceStatusAllocated {
		t.Errorf("replacement status = %s, want allocated", upd.Status)
	}

	// Assign technician — should create WO via bridge.
	if _, err := svc.AssignTechnician(ctx, swap.ID, tech); err != nil {
		t.Fatalf("AssignTechnician: %v", err)
	}
	if !woc.called {
		t.Errorf("expected WorkOrderCreator to be called")
	}
	if woc.lastTechID != tech {
		t.Errorf("WO got tech=%s want %s", woc.lastTechID, tech)
	}

	// Complete — faulty → decommissioned, replacement → active, retrofit called.
	final, err := svc.CompleteSwap(ctx, swap.ID)
	if err != nil {
		t.Fatalf("CompleteSwap: %v", err)
	}
	if final.Status != domain.SwapStatusSwapped {
		t.Errorf("final status = %s, want swapped", final.Status)
	}
	if !retro.called {
		t.Errorf("expected RetrofitTrigger to be called")
	}
	if retro.lastOld != faulty.ID {
		t.Errorf("retrofit old id = %s, want %s", retro.lastOld, faulty.ID)
	}
	if retro.lastNew != replacement.ID {
		t.Errorf("retrofit new id = %s, want %s", retro.lastNew, replacement.ID)
	}
	if final.RetrofitID == nil {
		t.Errorf("retrofit id not stamped on swap")
	}

	updFaulty, _ := devRepo.FindByID(ctx, faulty.ID)
	if updFaulty.Status != domain.DeviceStatusDecommissioned {
		t.Errorf("faulty post-swap = %s, want decommissioned", updFaulty.Status)
	}
	updReplacement, _ := devRepo.FindByID(ctx, replacement.ID)
	if updReplacement.Status != domain.DeviceStatusActive {
		t.Errorf("replacement post-swap = %s, want active", updReplacement.Status)
	}

	// Close.
	closed, err := svc.CloseSwap(ctx, swap.ID)
	if err != nil {
		t.Fatalf("CloseSwap: %v", err)
	}
	if closed.Status != domain.SwapStatusClosed {
		t.Errorf("status after close = %s, want closed", closed.Status)
	}
}

// =====================================================================
// Negative paths
// =====================================================================

func TestSwapService_StageRejectsNonStockReplacement(t *testing.T) {
	ctx := context.Background()
	devRepo := newFakeDeviceRepo()
	swapRepo := newFakeSwapRepo()
	customer := uuid.New()
	approver := uuid.New()

	faulty, _ := domain.NewDevice("F-1", domain.DeviceKindONT, "M", "V")
	_ = devRepo.Create(ctx, faulty)
	// Replacement is already commissioned — can't be staged.
	repl, _ := domain.NewDevice("R-1", domain.DeviceKindONT, "M", "V")
	_ = repl.Allocate(customer, uuid.New())
	_ = repl.Commission(time.Now().UTC())
	_ = devRepo.Create(ctx, repl)

	svc := NewSwapService(swapRepo, devRepo, nil, nil, nil)
	swap, err := svc.RequestSwap(ctx, port.RequestSwapInput{
		CustomerID: customer, FaultyDeviceID: faulty.ID, Reason: "x",
	})
	if err != nil {
		t.Fatalf("RequestSwap: %v", err)
	}
	if _, err := svc.ApproveSwap(ctx, swap.ID, approver); err != nil {
		t.Fatalf("ApproveSwap: %v", err)
	}
	_, err = svc.StageSwap(ctx, swap.ID, repl.ID)
	if err == nil {
		t.Fatalf("StageSwap should fail with non-stock replacement")
	}
	var de *derrors.Error
	if !errors.As(err, &de) || de.Code != "swap.replacement_not_in_stock" {
		t.Errorf("err code = %v want swap.replacement_not_in_stock", err)
	}
}

func TestSwapService_CompleteRequiresReplacement(t *testing.T) {
	ctx := context.Background()
	devRepo := newFakeDeviceRepo()
	swapRepo := newFakeSwapRepo()
	customer := uuid.New()

	faulty, _ := domain.NewDevice("F-1", domain.DeviceKindONT, "M", "V")
	_ = devRepo.Create(ctx, faulty)

	svc := NewSwapService(swapRepo, devRepo, nil, nil, nil)
	swap, _ := svc.RequestSwap(ctx, port.RequestSwapInput{
		CustomerID: customer, FaultyDeviceID: faulty.ID, Reason: "x",
	})
	_, _ = svc.ApproveSwap(ctx, swap.ID, uuid.New())
	// Skip stage — try to complete.
	_, err := svc.CompleteSwap(ctx, swap.ID)
	if err == nil {
		t.Fatalf("CompleteSwap without replacement should fail")
	}
	var de *derrors.Error
	if !errors.As(err, &de) || de.Code != "swap.no_replacement" {
		t.Errorf("err code = %v want swap.no_replacement", err)
	}
}

func TestSwapService_RequestRequiresFaulty(t *testing.T) {
	ctx := context.Background()
	devRepo := newFakeDeviceRepo()
	swapRepo := newFakeSwapRepo()
	svc := NewSwapService(swapRepo, devRepo, nil, nil, nil)

	_, err := svc.RequestSwap(ctx, port.RequestSwapInput{
		CustomerID: uuid.New(), FaultyDeviceID: uuid.New(), Reason: "x",
	})
	if err == nil {
		t.Fatalf("RequestSwap with unknown faulty id should fail")
	}
}
