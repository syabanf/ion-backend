// Wave 125 — CSV importers for the three bulk-ops kinds.
//
// Each importer:
//   1. Creates the bulk_jobs row (status='pending', dry_run flag).
//   2. Parses the CSV row-by-row and resolves human-readable codes
//      (customer_no, plan_code, port_code, template_code) to UUIDs via
//      the CSVLookupPort.
//   3. Returns an ImportSummary with per-row errors so the operator can
//      see which rows were rejected without opening the DB.
//
// Row errors do NOT abort the import — the goal is to ingest as many
// valid rows as possible, then let the operator decide whether to fix
// the rejected rows + re-import or proceed with the partial batch.
package usecase

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/operations/domain"
	"github.com/ion-core/backend/internal/operations/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// ImportRowError — one row failed to materialise into a queued item.
type ImportRowError struct {
	Row        int    `json:"row"`
	CustomerNo string `json:"customer_no,omitempty"`
	Reason     string `json:"reason"`
}

// ImportSummary — return value from each ImportXxxCSV call.
type ImportSummary struct {
	JobID   uuid.UUID         `json:"job_id"`
	Total   int               `json:"total"`
	OK      int               `json:"ok"`
	Errors  []ImportRowError  `json:"errors"`
}

// BulkCSVImporter materialises CSV rows into queued items.
type BulkCSVImporter struct {
	jobs   port.BulkJobRepository
	bpcs   port.BulkPlanChangeItemRepository
	boms   port.BulkODPMigrationItemRepository
	bwos   port.BulkWOCreationItemRepository
	lookup port.CSVLookupPort
	log    *slog.Logger
}

// NewBulkCSVImporter — primary constructor.
func NewBulkCSVImporter(
	jobs port.BulkJobRepository,
	bpcs port.BulkPlanChangeItemRepository,
	boms port.BulkODPMigrationItemRepository,
	bwos port.BulkWOCreationItemRepository,
	lookup port.CSVLookupPort,
	log *slog.Logger,
) *BulkCSVImporter {
	if log == nil {
		log = slog.Default()
	}
	return &BulkCSVImporter{
		jobs:   jobs,
		bpcs:   bpcs,
		boms:   boms,
		bwos:   bwos,
		lookup: lookup,
		log:    log.With("svc", "operations_bulk_csv_importer"),
	}
}

// ImportPlanChangeCSV ingests rows for a Bulk Plan Change job.
//
//	Columns (header row required): customer_no, target_plan_code, effective_at
//	effective_at is ISO8601 (e.g. 2026-06-01T00:00:00Z) — empty means
//	"next billing cycle" (the CRM bridge picks the default).
func (im *BulkCSVImporter) ImportPlanChangeCSV(
	ctx context.Context,
	r io.Reader,
	dryRun bool,
	createdBy *uuid.UUID,
) (*ImportSummary, error) {
	if im.jobs == nil || im.bpcs == nil {
		return nil, derrors.Internal("bulk.repo_nil", "importer is missing repositories")
	}
	job, err := domain.NewBulkJob(domain.BulkJobPlanChange, dryRun, createdBy)
	if err != nil {
		return nil, err
	}
	if err := im.jobs.Create(ctx, job); err != nil {
		return nil, err
	}
	summary := &ImportSummary{JobID: job.ID, Errors: []ImportRowError{}}
	items := []domain.BulkPlanChangeItem{}

	cr := csv.NewReader(r)
	cr.TrimLeadingSpace = true
	header, err := cr.Read()
	if err != nil {
		return summary, derrors.Validation("bulk.csv_header_missing",
			"first row must be a header")
	}
	idx := headerIndex(header)
	row := 1
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			summary.Errors = append(summary.Errors, ImportRowError{Row: row, Reason: err.Error()})
			row++
			continue
		}
		row++
		summary.Total++
		customerNo := strings.TrimSpace(cell(rec, idx, "customer_no"))
		planCode := strings.TrimSpace(cell(rec, idx, "target_plan_code"))
		effRaw := strings.TrimSpace(cell(rec, idx, "effective_at"))
		if customerNo == "" || planCode == "" {
			summary.Errors = append(summary.Errors, ImportRowError{
				Row: row, CustomerNo: customerNo,
				Reason: "customer_no and target_plan_code are required",
			})
			continue
		}
		customerID, err := im.lookupCustomer(ctx, customerNo)
		if err != nil {
			summary.Errors = append(summary.Errors, ImportRowError{Row: row, CustomerNo: customerNo, Reason: err.Error()})
			continue
		}
		planID, err := im.lookupPlan(ctx, planCode)
		if err != nil {
			summary.Errors = append(summary.Errors, ImportRowError{Row: row, CustomerNo: customerNo, Reason: err.Error()})
			continue
		}
		var effAt *time.Time
		if effRaw != "" {
			t, err := time.Parse(time.RFC3339, effRaw)
			if err != nil {
				summary.Errors = append(summary.Errors, ImportRowError{
					Row: row, CustomerNo: customerNo,
					Reason: fmt.Sprintf("effective_at not ISO8601: %v", err),
				})
				continue
			}
			effAt = &t
		}
		items = append(items, domain.BulkPlanChangeItem{
			ID:           uuid.New(),
			BulkJobID:    job.ID,
			CustomerID:   *customerID,
			TargetPlanID: *planID,
			EffectiveAt:  effAt,
			Status:       domain.BPCItemQueued,
			CreatedAt:    time.Now().UTC(),
		})
		summary.OK++
	}
	if len(items) > 0 {
		if err := im.bpcs.CreateBatch(ctx, items); err != nil {
			return summary, err
		}
	}
	job.TotalItems = summary.OK
	if err := im.jobs.Update(ctx, job); err != nil {
		return summary, err
	}
	return summary, nil
}

// ImportODPMigrationCSV ingests rows for a Bulk ODP Migration job.
//
//	Columns: customer_no, to_olt_port_code, scheduled_window_start, scheduled_window_end
//	scheduled_window_* are ISO8601 (may be empty if no field work expected).
func (im *BulkCSVImporter) ImportODPMigrationCSV(
	ctx context.Context,
	r io.Reader,
	dryRun bool,
	createdBy *uuid.UUID,
) (*ImportSummary, error) {
	if im.jobs == nil || im.boms == nil {
		return nil, derrors.Internal("bulk.repo_nil", "importer is missing repositories")
	}
	job, err := domain.NewBulkJob(domain.BulkJobODPMigration, dryRun, createdBy)
	if err != nil {
		return nil, err
	}
	if err := im.jobs.Create(ctx, job); err != nil {
		return nil, err
	}
	summary := &ImportSummary{JobID: job.ID, Errors: []ImportRowError{}}
	items := []domain.BulkODPMigrationItem{}

	cr := csv.NewReader(r)
	cr.TrimLeadingSpace = true
	header, err := cr.Read()
	if err != nil {
		return summary, derrors.Validation("bulk.csv_header_missing",
			"first row must be a header")
	}
	idx := headerIndex(header)
	row := 1
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			summary.Errors = append(summary.Errors, ImportRowError{Row: row, Reason: err.Error()})
			row++
			continue
		}
		row++
		summary.Total++
		customerNo := strings.TrimSpace(cell(rec, idx, "customer_no"))
		portCode := strings.TrimSpace(cell(rec, idx, "to_olt_port_code"))
		startRaw := strings.TrimSpace(cell(rec, idx, "scheduled_window_start"))
		endRaw := strings.TrimSpace(cell(rec, idx, "scheduled_window_end"))
		if customerNo == "" || portCode == "" {
			summary.Errors = append(summary.Errors, ImportRowError{
				Row: row, CustomerNo: customerNo,
				Reason: "customer_no and to_olt_port_code are required",
			})
			continue
		}
		customerID, err := im.lookupCustomer(ctx, customerNo)
		if err != nil {
			summary.Errors = append(summary.Errors, ImportRowError{Row: row, CustomerNo: customerNo, Reason: err.Error()})
			continue
		}
		portID, err := im.lookupPort(ctx, portCode)
		if err != nil {
			summary.Errors = append(summary.Errors, ImportRowError{Row: row, CustomerNo: customerNo, Reason: err.Error()})
			continue
		}
		var start, end *time.Time
		if startRaw != "" {
			t, err := time.Parse(time.RFC3339, startRaw)
			if err != nil {
				summary.Errors = append(summary.Errors, ImportRowError{
					Row: row, CustomerNo: customerNo,
					Reason: fmt.Sprintf("scheduled_window_start not ISO8601: %v", err),
				})
				continue
			}
			start = &t
		}
		if endRaw != "" {
			t, err := time.Parse(time.RFC3339, endRaw)
			if err != nil {
				summary.Errors = append(summary.Errors, ImportRowError{
					Row: row, CustomerNo: customerNo,
					Reason: fmt.Sprintf("scheduled_window_end not ISO8601: %v", err),
				})
				continue
			}
			end = &t
		}
		items = append(items, domain.BulkODPMigrationItem{
			ID:                   uuid.New(),
			BulkJobID:            job.ID,
			CustomerID:           *customerID,
			ToOLTPortID:          *portID,
			ScheduledWindowStart: start,
			ScheduledWindowEnd:   end,
			Status:               domain.BOMItemQueued,
			CreatedAt:            time.Now().UTC(),
		})
		summary.OK++
	}
	if len(items) > 0 {
		if err := im.boms.CreateBatch(ctx, items); err != nil {
			return summary, err
		}
	}
	job.TotalItems = summary.OK
	if err := im.jobs.Update(ctx, job); err != nil {
		return summary, err
	}
	return summary, nil
}

// ImportWOCreationCSV ingests rows for a Bulk WO Creation job.
//
//	Columns: customer_no, wo_template_code, scheduled_at
//	wo_template_code may be empty (the creator falls back to wo_type
//	defaults). scheduled_at is ISO8601, may be empty.
func (im *BulkCSVImporter) ImportWOCreationCSV(
	ctx context.Context,
	r io.Reader,
	dryRun bool,
	createdBy *uuid.UUID,
) (*ImportSummary, error) {
	if im.jobs == nil || im.bwos == nil {
		return nil, derrors.Internal("bulk.repo_nil", "importer is missing repositories")
	}
	job, err := domain.NewBulkJob(domain.BulkJobWOCreation, dryRun, createdBy)
	if err != nil {
		return nil, err
	}
	if err := im.jobs.Create(ctx, job); err != nil {
		return nil, err
	}
	summary := &ImportSummary{JobID: job.ID, Errors: []ImportRowError{}}
	items := []domain.BulkWOCreationItem{}

	cr := csv.NewReader(r)
	cr.TrimLeadingSpace = true
	cr.FieldsPerRecord = -1 // allow variable column counts (template/scheduled may be omitted)
	header, err := cr.Read()
	if err != nil {
		return summary, derrors.Validation("bulk.csv_header_missing",
			"first row must be a header")
	}
	idx := headerIndex(header)
	row := 1
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			summary.Errors = append(summary.Errors, ImportRowError{Row: row, Reason: err.Error()})
			row++
			continue
		}
		row++
		summary.Total++
		customerNo := strings.TrimSpace(cell(rec, idx, "customer_no"))
		templateCode := strings.TrimSpace(cell(rec, idx, "wo_template_code"))
		woType := strings.TrimSpace(cell(rec, idx, "wo_type"))
		schedRaw := strings.TrimSpace(cell(rec, idx, "scheduled_at"))
		if customerNo == "" {
			summary.Errors = append(summary.Errors, ImportRowError{
				Row: row, Reason: "customer_no is required",
			})
			continue
		}
		customerID, err := im.lookupCustomer(ctx, customerNo)
		if err != nil {
			summary.Errors = append(summary.Errors, ImportRowError{Row: row, CustomerNo: customerNo, Reason: err.Error()})
			continue
		}
		var templateID *uuid.UUID
		if templateCode != "" {
			tid, err := im.lookupTemplate(ctx, templateCode)
			if err != nil {
				summary.Errors = append(summary.Errors, ImportRowError{Row: row, CustomerNo: customerNo, Reason: err.Error()})
				continue
			}
			templateID = tid
		}
		var schedAt *time.Time
		if schedRaw != "" {
			t, err := time.Parse(time.RFC3339, schedRaw)
			if err != nil {
				summary.Errors = append(summary.Errors, ImportRowError{
					Row: row, CustomerNo: customerNo,
					Reason: fmt.Sprintf("scheduled_at not ISO8601: %v", err),
				})
				continue
			}
			schedAt = &t
		}
		items = append(items, domain.BulkWOCreationItem{
			ID:           uuid.New(),
			BulkJobID:    job.ID,
			CustomerID:   *customerID,
			WOTemplateID: templateID,
			WOType:       woType,
			ScheduledAt:  schedAt,
			Status:       domain.BWOItemQueued,
			CreatedAt:    time.Now().UTC(),
		})
		summary.OK++
	}
	if len(items) > 0 {
		if err := im.bwos.CreateBatch(ctx, items); err != nil {
			return summary, err
		}
	}
	job.TotalItems = summary.OK
	if err := im.jobs.Update(ctx, job); err != nil {
		return summary, err
	}
	return summary, nil
}

// =====================================================================
// Helpers
// =====================================================================

// headerIndex maps a column name → its position in the row slice.
func headerIndex(header []string) map[string]int {
	m := make(map[string]int, len(header))
	for i, h := range header {
		m[strings.TrimSpace(strings.ToLower(h))] = i
	}
	return m
}

// cell returns the trimmed value at the named column, or "" if missing.
func cell(rec []string, idx map[string]int, name string) string {
	i, ok := idx[name]
	if !ok || i >= len(rec) {
		return ""
	}
	return rec[i]
}

func (im *BulkCSVImporter) lookupCustomer(ctx context.Context, customerNo string) (*uuid.UUID, error) {
	if im.lookup == nil {
		return nil, derrors.Validation("bulk.lookup_unavailable", "csv lookup port not wired")
	}
	id, err := im.lookup.CustomerIDByNumber(ctx, customerNo)
	if err != nil {
		return nil, err
	}
	if id == nil {
		return nil, derrors.Validation("bulk.customer_not_found",
			fmt.Sprintf("customer %s not found", customerNo))
	}
	return id, nil
}

func (im *BulkCSVImporter) lookupPlan(ctx context.Context, code string) (*uuid.UUID, error) {
	if im.lookup == nil {
		return nil, derrors.Validation("bulk.lookup_unavailable", "csv lookup port not wired")
	}
	id, err := im.lookup.PlanIDByCode(ctx, code)
	if err != nil {
		return nil, err
	}
	if id == nil {
		return nil, derrors.Validation("bulk.plan_not_found",
			fmt.Sprintf("target_plan_code %s not found", code))
	}
	return id, nil
}

func (im *BulkCSVImporter) lookupPort(ctx context.Context, code string) (*uuid.UUID, error) {
	if im.lookup == nil {
		return nil, derrors.Validation("bulk.lookup_unavailable", "csv lookup port not wired")
	}
	id, err := im.lookup.PortIDByCode(ctx, code)
	if err != nil {
		return nil, err
	}
	if id == nil {
		return nil, derrors.Validation("bulk.port_not_found",
			fmt.Sprintf("to_olt_port_code %s not found", code))
	}
	return id, nil
}

func (im *BulkCSVImporter) lookupTemplate(ctx context.Context, code string) (*uuid.UUID, error) {
	if im.lookup == nil {
		return nil, derrors.Validation("bulk.lookup_unavailable", "csv lookup port not wired")
	}
	id, err := im.lookup.WOTemplateIDByCode(ctx, code)
	if err != nil {
		return nil, err
	}
	if id == nil {
		return nil, derrors.Validation("bulk.template_not_found",
			fmt.Sprintf("wo_template_code %s not found", code))
	}
	return id, nil
}
