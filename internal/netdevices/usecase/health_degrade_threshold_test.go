// Wave 120 — health degrade threshold edge.
//
// Pins TC-NDL-* "after THREE consecutive snapshots score below 60
// (default DegradeScore + Threshold), HealthService flips an active
// device → degraded; a single bad snapshot or two-then-recovery must
// NOT degrade".
//
// The auto-degrade watcher lives in usecase/health.go::RecordSnapshot.
// The CountConsecutiveLowScores port returns how many of the last N
// snapshots scored below the threshold; this fake repo lets us drive
// the counter exactly.

package usecase

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/netdevices/domain"
	"github.com/ion-core/backend/internal/netdevices/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// Fake HealthSnapshotRepository
// =====================================================================

type fakeHealthRepo struct {
	mu              sync.Mutex
	snaps           []*domain.HealthSnapshot
	consecutiveLow  int // forced value returned by CountConsecutiveLowScores
}

func (r *fakeHealthRepo) Insert(_ context.Context, s *domain.HealthSnapshot) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.snaps = append(r.snaps, s)
	return nil
}

func (r *fakeHealthRepo) ListRecent(_ context.Context, deviceID uuid.UUID, limit int) ([]domain.HealthSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []domain.HealthSnapshot{}
	for i := len(r.snaps) - 1; i >= 0 && len(out) < limit; i-- {
		if r.snaps[i].DeviceID == deviceID {
			out = append(out, *r.snaps[i])
		}
	}
	return out, nil
}

func (r *fakeHealthRepo) CountConsecutiveLowScores(_ context.Context, _ uuid.UUID, _ int, _ int) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.consecutiveLow, nil
}

// =====================================================================
// Helpers
// =====================================================================

func mkBadSample(deviceID uuid.UUID) port.RecordHealthInput {
	loss := 8.0
	cpu := 96.0
	return port.RecordHealthInput{
		DeviceID:      deviceID,
		SnappedAt:     time.Now().UTC(),
		PacketLossPct: &loss,
		CPUPct:        &cpu,
	}
}

// =====================================================================
// Tests
// =====================================================================

func TestHealthService_OneBadSnapshot_DoesNotDegrade(t *testing.T) {
	ctx := context.Background()
	devRepo := newFakeDeviceRepo()
	customer := uuid.New()
	dev, _ := domain.NewDevice("DEV-DEGRADE-001", domain.DeviceKindONT, "M", "V")
	_ = dev.Allocate(customer, uuid.New())
	_ = dev.Commission(time.Now().UTC())
	_ = dev.Activate()
	_ = devRepo.Create(ctx, dev)

	healthRepo := &fakeHealthRepo{consecutiveLow: 1} // one bad reading only
	svc := NewHealthService(healthRepo, devRepo, nil)
	if _, err := svc.RecordSnapshot(ctx, mkBadSample(dev.ID)); err != nil {
		t.Fatalf("RecordSnapshot: %v", err)
	}
	got, _ := devRepo.FindByID(ctx, dev.ID)
	if got.Status == domain.DeviceStatusDegraded {
		t.Errorf("device degraded after 1 bad sample; want still active")
	}
}

func TestHealthService_ThreeConsecutiveLow_DegradesActiveDevice(t *testing.T) {
	ctx := context.Background()
	devRepo := newFakeDeviceRepo()
	customer := uuid.New()
	dev, _ := domain.NewDevice("DEV-DEGRADE-002", domain.DeviceKindONT, "M", "V")
	_ = dev.Allocate(customer, uuid.New())
	_ = dev.Commission(time.Now().UTC())
	_ = dev.Activate()
	_ = devRepo.Create(ctx, dev)

	healthRepo := &fakeHealthRepo{consecutiveLow: 3} // threshold met
	svc := NewHealthService(healthRepo, devRepo, nil)
	if _, err := svc.RecordSnapshot(ctx, mkBadSample(dev.ID)); err != nil {
		t.Fatalf("RecordSnapshot: %v", err)
	}
	got, _ := devRepo.FindByID(ctx, dev.ID)
	if got.Status != domain.DeviceStatusDegraded {
		t.Errorf("device status = %s, want degraded after 3 consecutive low scores", got.Status)
	}
}

func TestHealthService_TwoLowThenRecovery_DoesNotDegrade(t *testing.T) {
	// Two bad scores followed by a good one — fakeHealthRepo's
	// consecutive counter resets implicitly because we control it.
	ctx := context.Background()
	devRepo := newFakeDeviceRepo()
	customer := uuid.New()
	dev, _ := domain.NewDevice("DEV-DEGRADE-003", domain.DeviceKindONT, "M", "V")
	_ = dev.Allocate(customer, uuid.New())
	_ = dev.Commission(time.Now().UTC())
	_ = dev.Activate()
	_ = devRepo.Create(ctx, dev)

	healthRepo := &fakeHealthRepo{consecutiveLow: 2} // below threshold
	svc := NewHealthService(healthRepo, devRepo, nil)
	if _, err := svc.RecordSnapshot(ctx, mkBadSample(dev.ID)); err != nil {
		t.Fatalf("RecordSnapshot: %v", err)
	}
	got, _ := devRepo.FindByID(ctx, dev.ID)
	if got.Status == domain.DeviceStatusDegraded {
		t.Errorf("degraded with streak=2 (below threshold=3); want still active")
	}
}

func TestHealthService_DegradeBlockedOnNonActive(t *testing.T) {
	// A commissioned-but-not-active device cannot be flipped to degraded
	// directly — the auto-activate path runs first; then the watcher
	// sees Active and acts. With consecutiveLow=3 but the device starting
	// in InStock the activation path doesn't run; the device stays in
	// InStock.
	ctx := context.Background()
	devRepo := newFakeDeviceRepo()
	dev, _ := domain.NewDevice("DEV-DEGRADE-004", domain.DeviceKindONT, "M", "V")
	// Leave dev in InStock — neither auto-active nor degraded should fire.
	_ = devRepo.Create(ctx, dev)

	healthRepo := &fakeHealthRepo{consecutiveLow: 3}
	svc := NewHealthService(healthRepo, devRepo, nil)
	if _, err := svc.RecordSnapshot(ctx, mkBadSample(dev.ID)); err != nil {
		t.Fatalf("RecordSnapshot: %v", err)
	}
	got, _ := devRepo.FindByID(ctx, dev.ID)
	if got.Status != domain.DeviceStatusInStock {
		t.Errorf("status changed unexpectedly: got %s, want in_stock", got.Status)
	}
}

// Direct domain-level pin — MarkDegraded refuses non-active states.
func TestDevice_MarkDegraded_FromInStock_Conflicts(t *testing.T) {
	dev, _ := domain.NewDevice("DEV-DEGRADE-DIRECT", domain.DeviceKindONT, "M", "V")
	if err := dev.MarkDegraded("test"); err == nil {
		t.Fatalf("expected MarkDegraded from in_stock to be refused")
	} else if de := derrors.As(err); de == nil || de.Code != "device.invalid_state_transition" {
		t.Fatalf("err code = %v, want invalid_state_transition", err)
	}
}
