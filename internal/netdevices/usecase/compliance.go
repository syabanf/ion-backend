package usecase

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/netdevices/domain"
	"github.com/ion-core/backend/internal/netdevices/port"
	"github.com/ion-core/backend/pkg/audit"
)

// ComplianceService iterates devices, evaluates each against the
// recommended firmware for its (kind, model), and persists a run row.
// Designed to be safe-to-rerun: each invocation creates a new run, so
// the operator gets a history of fleet compliance over time.
type ComplianceService struct {
	devices    port.DeviceRepository
	versions   port.FirmwareVersionRepository
	runs       port.ComplianceRepository
	audit      audit.Writer
	// PageSize controls the scan batch. Default 200 — small enough to
	// keep memory bounded on a large fleet; large enough that the scan
	// completes in a reasonable number of round-trips.
	PageSize int
}

func NewComplianceService(
	devices port.DeviceRepository,
	versions port.FirmwareVersionRepository,
	runs port.ComplianceRepository,
	auditor audit.Writer,
) *ComplianceService {
	if auditor == nil {
		auditor = audit.Nop{}
	}
	return &ComplianceService{
		devices:  devices,
		versions: versions,
		runs:     runs,
		audit:    auditor,
		PageSize: 200,
	}
}

// RunScan iterates active devices, evaluates each, and stores the run.
// scope is "all" (default) or a kind name like "ont"; the latter
// filters the device scan to that kind so a targeted re-scan after a
// critical firmware release doesn't have to walk the whole fleet.
func (s *ComplianceService) RunScan(ctx context.Context, scope string) (*domain.FirmwareComplianceRun, error) {
	run := domain.NewFirmwareComplianceRun(scope)
	if err := s.runs.Create(ctx, run); err != nil {
		return nil, err
	}

	// Cache the (kind, model) → recommended lookup so a 5000-device
	// fleet running on 3 models hits the DB 3 times, not 5000.
	cache := map[string]*domain.FirmwareVersion{}
	getRecommended := func(kind domain.DeviceKind, model string) *domain.FirmwareVersion {
		k := string(kind) + "|" + model
		if v, ok := cache[k]; ok {
			return v
		}
		v, err := s.versions.FindRecommended(ctx, kind, model)
		if err != nil {
			cache[k] = nil
			return nil
		}
		cache[k] = v
		return v
	}

	verdicts := map[string]string{}
	total, compliant, nonCompliant, criticalPending := 0, 0, 0, 0

	filter := port.DeviceListFilter{
		Status: string(domain.DeviceStatusActive),
		Kind:   "",
		Limit:  s.PageSize,
		Offset: 0,
	}
	if scope != "" && scope != "all" {
		filter.Kind = scope
	}

	for {
		devices, _, err := s.devices.List(ctx, filter)
		if err != nil {
			return nil, err
		}
		if len(devices) == 0 {
			break
		}
		for i := range devices {
			d := devices[i]
			total++
			rec := getRecommended(d.Kind, d.Model)
			v := domain.EvaluateDevice(d, rec)
			verdicts[d.ID.String()] = string(v)
			switch v {
			case domain.ComplianceCompliant:
				compliant++
			case domain.ComplianceNonCompliant:
				nonCompliant++
			case domain.ComplianceCriticalPending:
				criticalPending++
			}
		}
		if len(devices) < filter.Limit {
			break
		}
		filter.Offset += filter.Limit
	}

	now := time.Now().UTC()
	run.FinishedAt = &now
	run.TotalDevices = total
	run.Compliant = compliant
	run.NonCompliant = nonCompliant
	run.CriticalPending = criticalPending
	payload, _ := json.Marshal(verdicts)
	run.ReportPayload = payload

	if err := s.runs.UpdateFinish(ctx, run); err != nil {
		return nil, err
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		Module: "netdev", RecordType: "netdev.firmware_compliance_run", RecordID: run.ID.String(),
		FieldChanged: "finished_at", After: now.Format(time.RFC3339),
		Reason: "compliance_scan_completed",
	})
	return run, nil
}

// GetRun returns a stored run.
func (s *ComplianceService) GetRun(ctx context.Context, id uuid.UUID) (*domain.FirmwareComplianceRun, error) {
	return s.runs.FindByID(ctx, id)
}
