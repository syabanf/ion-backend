package usecase

import (
	"context"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/netdevices/domain"
	"github.com/ion-core/backend/internal/netdevices/port"
	"github.com/ion-core/backend/pkg/audit"
)

// HealthService ingests telemetry snapshots and drives the auto-active +
// auto-degraded watchers.
//
// On first sample for a commissioned device we flip it to active. On
// three consecutive low scores (<60) we flip active → degraded.
type HealthService struct {
	snapshots port.HealthSnapshotRepository
	devices   port.DeviceRepository
	audit     audit.Writer

	// Degradation threshold knobs — public so a test can override them.
	DegradeScore     int
	DegradeLookback  int
	DegradeThreshold int // # of consecutive low scores needed to flip
}

func NewHealthService(snapshots port.HealthSnapshotRepository, devices port.DeviceRepository, auditor audit.Writer) *HealthService {
	if auditor == nil {
		auditor = audit.Nop{}
	}
	return &HealthService{
		snapshots:        snapshots,
		devices:          devices,
		audit:            auditor,
		DegradeScore:     60,
		DegradeLookback:  3,
		DegradeThreshold: 3,
	}
}

// RecordSnapshot persists a telemetry sample and runs the auto-state
// watchers. Returns the persisted snapshot.
func (s *HealthService) RecordSnapshot(ctx context.Context, in port.RecordHealthInput) (*domain.HealthSnapshot, error) {
	snap := domain.NewHealthSnapshot(in.DeviceID, in.SnappedAt)
	snap.UptimeSeconds = in.UptimeSeconds
	snap.SignalDBM = in.SignalDBM
	snap.PacketLossPct = in.PacketLossPct
	snap.CPUPct = in.CPUPct
	snap.MemoryPct = in.MemoryPct
	snap.RawPayload = in.RawPayload
	if err := s.snapshots.Insert(ctx, snap); err != nil {
		return nil, err
	}

	// Auto-activate on first sample after commissioning.
	device, derr := s.devices.FindByID(ctx, in.DeviceID)
	if derr == nil {
		if device.Status == domain.DeviceStatusCommissioned {
			if err := device.Activate(); err == nil {
				if uerr := s.devices.UpdateLifecycle(ctx, device); uerr == nil {
					audit.SafeWrite(ctx, s.audit, audit.Entry{
						Module: "netdev", RecordType: "netdev.device", RecordID: device.ID.String(),
						FieldChanged: "status", After: string(device.Status),
						Reason: "device_auto_activated_first_sample",
					})
				}
			}
		}
		// Auto-degrade watcher.
		if device.Status == domain.DeviceStatusActive {
			score := domain.ComputeHealthScore(*snap)
			if score < s.DegradeScore {
				lowN, err := s.snapshots.CountConsecutiveLowScores(ctx, in.DeviceID, s.DegradeScore, s.DegradeLookback)
				if err == nil && lowN >= s.DegradeThreshold {
					if err := device.MarkDegraded("health score below threshold for consecutive samples"); err == nil {
						if uerr := s.devices.UpdateLifecycle(ctx, device); uerr == nil {
							audit.SafeWrite(ctx, s.audit, audit.Entry{
								Module: "netdev", RecordType: "netdev.device", RecordID: device.ID.String(),
								FieldChanged: "status", After: string(device.Status),
								Reason: "device_auto_degraded",
							})
						}
					}
				}
			}
		}
	}
	return snap, nil
}

// History returns the most-recent N snapshots for a device.
func (s *HealthService) History(ctx context.Context, deviceID uuid.UUID, limit int) ([]domain.HealthSnapshot, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	return s.snapshots.ListRecent(ctx, deviceID, limit)
}
