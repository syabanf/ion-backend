package domain

import (
	"time"

	"github.com/google/uuid"
)

// ComplianceVerdict is the per-device evaluation outcome.
type ComplianceVerdict string

const (
	ComplianceCompliant       ComplianceVerdict = "compliant"
	ComplianceNonCompliant    ComplianceVerdict = "non_compliant"
	ComplianceCriticalPending ComplianceVerdict = "critical_pending"
	ComplianceUnknown         ComplianceVerdict = "unknown" // no recommended firmware on file
)

// FirmwareComplianceRun is the header of a compliance scan. The
// per-device verdicts go into ReportPayload; the aggregate counters on
// the header power the dashboard tile without cracking the JSON.
type FirmwareComplianceRun struct {
	ID              uuid.UUID
	StartedAt       time.Time
	FinishedAt      *time.Time
	Scope           string
	TotalDevices    int
	Compliant       int
	NonCompliant    int
	CriticalPending int
	ReportPayload   []byte // jsonb: {device_id: verdict}
}

// NewFirmwareComplianceRun mints a fresh in-progress run header. Scope
// defaults to "all"; the cron may pass a kind-specific scope like
// "ont" for fleet-targeted re-scans.
func NewFirmwareComplianceRun(scope string) *FirmwareComplianceRun {
	if scope == "" {
		scope = "all"
	}
	return &FirmwareComplianceRun{
		ID:        uuid.New(),
		StartedAt: time.Now().UTC(),
		Scope:     scope,
	}
}

// EvaluateDevice classifies a single device against the recommended
// firmware for its (kind, model). recommended may be nil — in that case
// we return ComplianceUnknown so the dashboard can surface the gap in
// the catalog rather than mis-reporting the device as compliant.
//
//   - device.firmware_version == recommended.version → compliant.
//   - mismatch + recommended.is_critical            → critical_pending.
//   - mismatch + non-critical                       → non_compliant.
func EvaluateDevice(device Device, recommended *FirmwareVersion) ComplianceVerdict {
	if recommended == nil {
		return ComplianceUnknown
	}
	if device.FirmwareVersion == recommended.Version {
		return ComplianceCompliant
	}
	if recommended.IsCritical {
		return ComplianceCriticalPending
	}
	return ComplianceNonCompliant
}
