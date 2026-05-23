package usecase

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/netdevices/domain"
	"github.com/ion-core/backend/internal/netdevices/port"
	"github.com/ion-core/backend/pkg/audit"
)

// RMAService manages the vendor-RMA workflow. The accompanying device
// state transitions (device.OpenRMA / device.MarkRMAReturned) happen in
// the same flow so the netdev device and the RMA record stay in sync.
type RMAService struct {
	rma     port.RMARepository
	devices port.DeviceRepository
	audit   audit.Writer
}

func NewRMAService(rma port.RMARepository, devices port.DeviceRepository, auditor audit.Writer) *RMAService {
	if auditor == nil {
		auditor = audit.Nop{}
	}
	return &RMAService{rma: rma, devices: devices, audit: auditor}
}

// OpenRMA creates a new RMA + flips the device into rma_open.
func (s *RMAService) OpenRMA(ctx context.Context, in port.OpenRMAInput) (*domain.RMARecord, error) {
	device, err := s.devices.FindByID(ctx, in.DeviceID)
	if err != nil {
		return nil, err
	}
	rec, err := domain.NewRMARecord(in.DeviceID, in.Vendor, in.Reason, in.CreatedBy)
	if err != nil {
		return nil, err
	}
	beforeDevice := string(device.Status)
	if err := device.OpenRMA(); err != nil {
		return nil, err
	}
	if err := s.rma.Create(ctx, rec); err != nil {
		return nil, err
	}
	if err := s.devices.UpdateLifecycle(ctx, device); err != nil {
		return nil, err
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		Module: "netdev", RecordType: "netdev.rma", RecordID: rec.ID.String(),
		FieldChanged: "status", After: string(rec.Status),
		Reason: "rma_opened",
	})
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		Module: "netdev", RecordType: "netdev.device", RecordID: device.ID.String(),
		FieldChanged: "status", Before: beforeDevice, After: string(device.Status),
		Reason: "device_rma_opened",
	})
	return rec, nil
}

// MarkShipped flips open → shipped.
func (s *RMAService) MarkShipped(ctx context.Context, id uuid.UUID, vendorRMANo string, at time.Time) (*domain.RMARecord, error) {
	rec, err := s.rma.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	before := string(rec.Status)
	if at.IsZero() {
		at = time.Now().UTC()
	}
	if err := rec.MarkShipped(vendorRMANo, at); err != nil {
		return nil, err
	}
	if err := s.rma.UpdateLifecycle(ctx, rec); err != nil {
		return nil, err
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		Module: "netdev", RecordType: "netdev.rma", RecordID: rec.ID.String(),
		FieldChanged: "status", Before: before, After: string(rec.Status),
		Reason: "rma_shipped",
	})
	return rec, nil
}

// MarkReceived records arrival at the vendor; optionally captures the
// replacement serial they've pre-allocated.
func (s *RMAService) MarkReceived(ctx context.Context, id uuid.UUID, replacementSerial string, at time.Time) (*domain.RMARecord, error) {
	rec, err := s.rma.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	before := string(rec.Status)
	if at.IsZero() {
		at = time.Now().UTC()
	}
	if err := rec.MarkReceived(replacementSerial, at); err != nil {
		return nil, err
	}
	if err := s.rma.UpdateLifecycle(ctx, rec); err != nil {
		return nil, err
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		Module: "netdev", RecordType: "netdev.rma", RecordID: rec.ID.String(),
		FieldChanged: "status", Before: before, After: string(rec.Status),
		Reason: "rma_received",
	})
	return rec, nil
}

// MarkReplaced finalises the success path and flips the underlying
// device from rma_open to rma_returned.
func (s *RMAService) MarkReplaced(ctx context.Context, id uuid.UUID) (*domain.RMARecord, error) {
	rec, err := s.rma.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	before := string(rec.Status)
	if err := rec.MarkReplaced(); err != nil {
		return nil, err
	}
	if err := s.rma.UpdateLifecycle(ctx, rec); err != nil {
		return nil, err
	}
	// Cascade onto the device: rma_open → rma_returned.
	device, derr := s.devices.FindByID(ctx, rec.DeviceID)
	if derr == nil {
		if derr := device.MarkRMAReturned(rec.ReplacementSerial); derr == nil {
			_ = s.devices.UpdateLifecycle(ctx, device)
		}
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		Module: "netdev", RecordType: "netdev.rma", RecordID: rec.ID.String(),
		FieldChanged: "status", Before: before, After: string(rec.Status),
		Reason: "rma_replaced",
	})
	return rec, nil
}

// MarkRejected is the failure path.
func (s *RMAService) MarkRejected(ctx context.Context, id uuid.UUID, reason string) (*domain.RMARecord, error) {
	rec, err := s.rma.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	before := string(rec.Status)
	if err := rec.MarkRejected(reason); err != nil {
		return nil, err
	}
	if err := s.rma.UpdateLifecycle(ctx, rec); err != nil {
		return nil, err
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		Module: "netdev", RecordType: "netdev.rma", RecordID: rec.ID.String(),
		FieldChanged: "status", Before: before, After: string(rec.Status),
		Reason: "rma_rejected",
	})
	return rec, nil
}

// CloseRMA terminates the workflow.
func (s *RMAService) CloseRMA(ctx context.Context, id uuid.UUID) (*domain.RMARecord, error) {
	rec, err := s.rma.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	before := string(rec.Status)
	if err := rec.Close(); err != nil {
		return nil, err
	}
	if err := s.rma.UpdateLifecycle(ctx, rec); err != nil {
		return nil, err
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		Module: "netdev", RecordType: "netdev.rma", RecordID: rec.ID.String(),
		FieldChanged: "status", Before: before, After: string(rec.Status),
		Reason: "rma_closed",
	})
	return rec, nil
}

// GetRMA / ListRMA pass-throughs.
func (s *RMAService) GetRMA(ctx context.Context, id uuid.UUID) (*domain.RMARecord, error) {
	return s.rma.FindByID(ctx, id)
}

func (s *RMAService) ListRMA(ctx context.Context, status string, limit, offset int) ([]domain.RMARecord, int, error) {
	return s.rma.ListByStatus(ctx, status, limit, offset)
}

// ExpireOld flips records past the 90d window into expired. Returns
// the number of rows touched so the cron can log a meaningful summary.
func (s *RMAService) ExpireOld(ctx context.Context, now time.Time) (int, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	candidates, err := s.rma.ListExpirable(ctx, now)
	if err != nil {
		return 0, err
	}
	expired := 0
	for i := range candidates {
		r := &candidates[i]
		changed, eerr := r.Expire(now)
		if eerr != nil || !changed {
			continue
		}
		if uerr := s.rma.UpdateLifecycle(ctx, r); uerr != nil {
			continue
		}
		audit.SafeWrite(ctx, s.audit, audit.Entry{
			Module: "netdev", RecordType: "netdev.rma", RecordID: r.ID.String(),
			FieldChanged: "status", After: string(r.Status),
			Reason: "rma_auto_expired",
		})
		expired++
	}
	return expired, nil
}
