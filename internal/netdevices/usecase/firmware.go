package usecase

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/netdevices/domain"
	"github.com/ion-core/backend/internal/netdevices/port"
	"github.com/ion-core/backend/pkg/audit"
)

// FirmwareService owns the firmware catalog + upgrade-job workflow.
//
// The DeviceMgmtClient is the vendor-SDK adapter — in stub mode (default)
// every call is a no-op and the workflow advances purely via the
// usecase. When DEVICE_MGMT_ENABLED=true the wired adapter actually
// pushes images via SNMP/NETCONF.
type FirmwareService struct {
	versions   port.FirmwareVersionRepository
	jobs       port.FirmwareUpgradeJobRepository
	devices    port.DeviceRepository
	mgmtClient port.DeviceMgmtClient
	audit      audit.Writer
}

func NewFirmwareService(
	versions port.FirmwareVersionRepository,
	jobs port.FirmwareUpgradeJobRepository,
	devices port.DeviceRepository,
	mgmtClient port.DeviceMgmtClient,
	auditor audit.Writer,
) *FirmwareService {
	if auditor == nil {
		auditor = audit.Nop{}
	}
	return &FirmwareService{
		versions:   versions,
		jobs:       jobs,
		devices:    devices,
		mgmtClient: mgmtClient,
		audit:      auditor,
	}
}

// RegisterVersion adds a firmware version to the catalog. The DB UNIQUE
// on (kind, model, version) catches duplicates — the postgres adapter
// translates to a conflict for us.
func (s *FirmwareService) RegisterVersion(ctx context.Context, kind domain.DeviceKind, model, version, notes string, isRecommended, isCritical bool) (*domain.FirmwareVersion, error) {
	v, err := domain.NewFirmwareVersion(kind, model, version)
	if err != nil {
		return nil, err
	}
	v.ReleaseNotes = notes
	v.IsRecommended = isRecommended
	v.IsCritical = isCritical
	if err := s.versions.Create(ctx, v); err != nil {
		return nil, err
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		Module: "netdev", RecordType: "netdev.firmware_version", RecordID: v.ID.String(),
		FieldChanged: "version", After: v.Version, Reason: "firmware_version_registered",
	})
	return v, nil
}

// ScheduleUpgrade creates a Scheduled job for a device. The mgmt client
// gets a chance to validate scheduling but failures don't abort the
// job — operator can retry via StageUpgrade.
func (s *FirmwareService) ScheduleUpgrade(ctx context.Context, in port.ScheduleUpgradeInput) (*domain.FirmwareUpgradeJob, error) {
	job, err := domain.NewFirmwareUpgradeJob(in.DeviceID, in.TargetFirmwareID, in.ScheduledAt, in.CreatedBy)
	if err != nil {
		return nil, err
	}
	if err := s.jobs.Create(ctx, job); err != nil {
		return nil, err
	}
	if s.mgmtClient != nil && in.TargetFirmwareID != nil {
		device, derr := s.devices.FindByID(ctx, in.DeviceID)
		if derr == nil {
			fv, ferr := s.versions.FindByID(ctx, *in.TargetFirmwareID)
			if ferr == nil {
				_ = s.mgmtClient.ScheduleFirmwareUpgrade(ctx, device, fv.Version)
			}
		}
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		Module: "netdev", RecordType: "netdev.firmware_upgrade_job", RecordID: job.ID.String(),
		FieldChanged: "status", After: string(job.Status),
		Reason: "firmware_upgrade_scheduled",
	})
	return job, nil
}

// StageUpgrade moves scheduled → staged via the mgmt client (push
// image). Stub-mode mgmt returns nil so the workflow advances cleanly.
func (s *FirmwareService) StageUpgrade(ctx context.Context, jobID uuid.UUID) (*domain.FirmwareUpgradeJob, error) {
	job, err := s.jobs.FindByID(ctx, jobID)
	if err != nil {
		return nil, err
	}
	before := string(job.Status)
	if s.mgmtClient != nil && job.TargetFirmwareID != nil {
		device, derr := s.devices.FindByID(ctx, job.DeviceID)
		if derr == nil {
			fv, ferr := s.versions.FindByID(ctx, *job.TargetFirmwareID)
			if ferr == nil {
				if perr := s.mgmtClient.PushStagedImage(ctx, device, fv.Version); perr != nil {
					return nil, perr
				}
			}
		}
	}
	if err := job.Stage(); err != nil {
		return nil, err
	}
	if err := s.jobs.UpdateLifecycle(ctx, job); err != nil {
		return nil, err
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		Module: "netdev", RecordType: "netdev.firmware_upgrade_job", RecordID: job.ID.String(),
		FieldChanged: "status", Before: before, After: string(job.Status),
		Reason: "firmware_upgrade_staged",
	})
	return job, nil
}

// MarkUpgradeStarted flips staged → in_progress. The caller (vendor
// webhook, cron, manual ops) supplies the previous firmware so the
// rollback path has a snapshot.
func (s *FirmwareService) MarkUpgradeStarted(ctx context.Context, jobID uuid.UUID, previousFirmware string) (*domain.FirmwareUpgradeJob, error) {
	job, err := s.jobs.FindByID(ctx, jobID)
	if err != nil {
		return nil, err
	}
	before := string(job.Status)
	if s.mgmtClient != nil {
		device, derr := s.devices.FindByID(ctx, job.DeviceID)
		if derr == nil {
			if perr := s.mgmtClient.TriggerUpgrade(ctx, device); perr != nil {
				return nil, perr
			}
		}
	}
	if err := job.Start(previousFirmware, time.Now().UTC()); err != nil {
		return nil, err
	}
	if err := s.jobs.UpdateLifecycle(ctx, job); err != nil {
		return nil, err
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		Module: "netdev", RecordType: "netdev.firmware_upgrade_job", RecordID: job.ID.String(),
		FieldChanged: "status", Before: before, After: string(job.Status),
		Reason: "firmware_upgrade_started",
	})
	return job, nil
}

// MarkUpgradeSucceeded is the happy path completion. We also stamp the
// device's firmware_version so the compliance scanner reads the new
// value on its next pass.
func (s *FirmwareService) MarkUpgradeSucceeded(ctx context.Context, jobID uuid.UUID) (*domain.FirmwareUpgradeJob, error) {
	job, err := s.jobs.FindByID(ctx, jobID)
	if err != nil {
		return nil, err
	}
	before := string(job.Status)
	if err := job.Succeed(time.Now().UTC()); err != nil {
		return nil, err
	}
	if err := s.jobs.UpdateLifecycle(ctx, job); err != nil {
		return nil, err
	}
	// Stamp the device's firmware_version with the target version.
	if job.TargetFirmwareID != nil {
		device, derr := s.devices.FindByID(ctx, job.DeviceID)
		if derr == nil {
			fv, ferr := s.versions.FindByID(ctx, *job.TargetFirmwareID)
			if ferr == nil {
				device.FirmwareVersion = fv.Version
				_ = s.devices.UpdateLifecycle(ctx, device)
			}
		}
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		Module: "netdev", RecordType: "netdev.firmware_upgrade_job", RecordID: job.ID.String(),
		FieldChanged: "status", Before: before, After: string(job.Status),
		Reason: "firmware_upgrade_succeeded",
	})
	return job, nil
}

// MarkUpgradeFailed records an attempt failure. When retries remain
// (retryable=true returned by job.Fail) we leave the job at scheduled
// so the cron picks it up again; when exhausted we auto-rollback.
func (s *FirmwareService) MarkUpgradeFailed(ctx context.Context, jobID uuid.UUID, errMsg string) (*domain.FirmwareUpgradeJob, error) {
	job, err := s.jobs.FindByID(ctx, jobID)
	if err != nil {
		return nil, err
	}
	before := string(job.Status)
	retryable, ferr := job.Fail(errMsg)
	if ferr != nil {
		return nil, ferr
	}
	if !retryable {
		// Auto-rollback the firmware. Stub mode mgmt is a no-op.
		if s.mgmtClient != nil && job.PreviousFirmware != "" {
			device, derr := s.devices.FindByID(ctx, job.DeviceID)
			if derr == nil {
				_ = s.mgmtClient.RollbackFirmware(ctx, device, job.PreviousFirmware)
			}
		}
		if rerr := job.RollBack(time.Now().UTC()); rerr != nil {
			return nil, rerr
		}
	}
	if err := s.jobs.UpdateLifecycle(ctx, job); err != nil {
		return nil, err
	}
	reason := "firmware_upgrade_failed"
	if !retryable {
		reason = "firmware_upgrade_failed_rolled_back"
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		Module: "netdev", RecordType: "netdev.firmware_upgrade_job", RecordID: job.ID.String(),
		FieldChanged: "status", Before: before, After: string(job.Status),
		Reason: reason,
	})
	return job, nil
}
