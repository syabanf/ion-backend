package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// FirmwareVersion is a catalog entry: "for this kind+model, here's an
// image you can run". A row here doesn't push firmware to anything —
// the FirmwareUpgradeJob does that. is_critical promotes the version
// to "critical_pending" in compliance reports if a device isn't on it.
type FirmwareVersion struct {
	ID             uuid.UUID
	Kind           DeviceKind
	Model          string
	Version        string
	ReleaseNotes   string
	IsRecommended  bool
	IsCritical     bool
	ReleasedAt     *time.Time
	SupportedUntil *time.Time
	CreatedAt      time.Time
}

// NewFirmwareVersion enforces the (kind, model, version) shape contract.
// The DB has a UNIQUE constraint on that triple — the validation here
// is just for friendlier error messages.
func NewFirmwareVersion(kind DeviceKind, model, version string) (*FirmwareVersion, error) {
	model = strings.TrimSpace(model)
	version = strings.TrimSpace(version)
	if !validKinds[kind] {
		return nil, errors.Validation("firmware.kind_invalid", "kind is not a known value")
	}
	if model == "" {
		return nil, errors.Validation("firmware.model_required", "model is required")
	}
	if version == "" {
		return nil, errors.Validation("firmware.version_required", "version is required")
	}
	return &FirmwareVersion{
		ID:        uuid.New(),
		Kind:      kind,
		Model:     model,
		Version:   version,
		CreatedAt: time.Now().UTC(),
	}, nil
}

// UpgradeJobStatus tracks the upgrade workflow.
//
// Legal transitions (enforced below):
//
//	scheduled → staged → in_progress → succeeded
//	                                 ↘ failed → rolled_back (after max_retries)
//	scheduled|staged → cancelled
type UpgradeJobStatus string

const (
	UpgradeJobStatusScheduled  UpgradeJobStatus = "scheduled"
	UpgradeJobStatusStaged     UpgradeJobStatus = "staged"
	UpgradeJobStatusInProgress UpgradeJobStatus = "in_progress"
	UpgradeJobStatusSucceeded  UpgradeJobStatus = "succeeded"
	UpgradeJobStatusFailed     UpgradeJobStatus = "failed"
	UpgradeJobStatusRolledBack UpgradeJobStatus = "rolled_back"
	UpgradeJobStatusCancelled  UpgradeJobStatus = "cancelled"
)

// FirmwareUpgradeJob is one attempted upgrade. retry_count tracks how
// many times Fail() has been called; the usecase auto-rolls back at
// max_retries instead of bouncing back to Scheduled.
type FirmwareUpgradeJob struct {
	ID               uuid.UUID
	DeviceID         uuid.UUID
	TargetFirmwareID *uuid.UUID
	ScheduledAt      *time.Time
	StartedAt        *time.Time
	CompletedAt      *time.Time
	Status           UpgradeJobStatus
	RetryCount       int
	MaxRetries       int
	ErrorMsg         string
	PreviousFirmware string
	CreatedBy        *uuid.UUID
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// NewFirmwareUpgradeJob creates a Scheduled job. scheduledAt may be the
// zero value — the cron picks it up immediately if so.
func NewFirmwareUpgradeJob(deviceID uuid.UUID, targetFirmwareID *uuid.UUID, scheduledAt time.Time, createdBy *uuid.UUID) (*FirmwareUpgradeJob, error) {
	if deviceID == uuid.Nil {
		return nil, errors.Validation("firmware_job.device_required", "device_id is required")
	}
	now := time.Now().UTC()
	job := &FirmwareUpgradeJob{
		ID:               uuid.New(),
		DeviceID:         deviceID,
		TargetFirmwareID: targetFirmwareID,
		Status:           UpgradeJobStatusScheduled,
		RetryCount:       0,
		MaxRetries:       3,
		CreatedBy:        createdBy,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if !scheduledAt.IsZero() {
		t := scheduledAt.UTC()
		job.ScheduledAt = &t
	}
	return job, nil
}

// Stage transitions scheduled → staged: the image has been copied to
// the device but not flashed. Idempotent on already-staged.
func (j *FirmwareUpgradeJob) Stage() error {
	if j.Status == UpgradeJobStatusStaged {
		return nil
	}
	if j.Status != UpgradeJobStatusScheduled {
		return errors.Conflict(
			"firmware_job.invalid_state_transition",
			"only scheduled jobs can be staged (current: "+string(j.Status)+")",
		)
	}
	j.Status = UpgradeJobStatusStaged
	j.UpdatedAt = time.Now().UTC()
	return nil
}

// Start flips staged → in_progress and snapshots the previous firmware
// for the rollback path.
func (j *FirmwareUpgradeJob) Start(previousFirmware string, at time.Time) error {
	if j.Status == UpgradeJobStatusInProgress {
		return nil
	}
	if j.Status != UpgradeJobStatusStaged {
		return errors.Conflict(
			"firmware_job.invalid_state_transition",
			"only staged jobs can be started (current: "+string(j.Status)+")",
		)
	}
	j.Status = UpgradeJobStatusInProgress
	j.PreviousFirmware = strings.TrimSpace(previousFirmware)
	j.StartedAt = &at
	j.UpdatedAt = time.Now().UTC()
	return nil
}

// Succeed marks the upgrade complete.
func (j *FirmwareUpgradeJob) Succeed(at time.Time) error {
	if j.Status == UpgradeJobStatusSucceeded {
		return nil
	}
	if j.Status != UpgradeJobStatusInProgress {
		return errors.Conflict(
			"firmware_job.invalid_state_transition",
			"only in_progress jobs can succeed (current: "+string(j.Status)+")",
		)
	}
	j.Status = UpgradeJobStatusSucceeded
	j.CompletedAt = &at
	j.UpdatedAt = time.Now().UTC()
	return nil
}

// Fail records an attempt failure. Returns true when the caller should
// retry (retry budget left) and false when the job has exhausted its
// retries — the usecase then calls RollBack().
func (j *FirmwareUpgradeJob) Fail(errMsg string) (retryable bool, err error) {
	if j.Status != UpgradeJobStatusInProgress && j.Status != UpgradeJobStatusStaged {
		return false, errors.Conflict(
			"firmware_job.invalid_state_transition",
			"only in_progress or staged jobs can fail (current: "+string(j.Status)+")",
		)
	}
	j.ErrorMsg = strings.TrimSpace(errMsg)
	j.RetryCount++
	j.UpdatedAt = time.Now().UTC()
	if j.RetryCount >= j.MaxRetries {
		j.Status = UpgradeJobStatusFailed
		return false, nil
	}
	// Still room — go back to scheduled so the cron tries again.
	j.Status = UpgradeJobStatusScheduled
	return true, nil
}

// RollBack finalises a fully-failed job by reverting the previous
// firmware snapshot. Only allowed from Failed.
func (j *FirmwareUpgradeJob) RollBack(at time.Time) error {
	if j.Status == UpgradeJobStatusRolledBack {
		return nil
	}
	if j.Status != UpgradeJobStatusFailed {
		return errors.Conflict(
			"firmware_job.invalid_state_transition",
			"only failed jobs can be rolled back (current: "+string(j.Status)+")",
		)
	}
	j.Status = UpgradeJobStatusRolledBack
	j.CompletedAt = &at
	j.UpdatedAt = time.Now().UTC()
	return nil
}

// Cancel aborts a pre-in_progress job. Once we're flashing we can't
// cancel cleanly — the caller must Fail() instead.
func (j *FirmwareUpgradeJob) Cancel() error {
	if j.Status == UpgradeJobStatusCancelled {
		return nil
	}
	if j.Status != UpgradeJobStatusScheduled && j.Status != UpgradeJobStatusStaged {
		return errors.Conflict(
			"firmware_job.invalid_state_transition",
			"only scheduled or staged jobs can be cancelled (current: "+string(j.Status)+")",
		)
	}
	j.Status = UpgradeJobStatusCancelled
	j.UpdatedAt = time.Now().UTC()
	return nil
}
