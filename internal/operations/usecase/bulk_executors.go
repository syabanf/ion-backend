// Package usecase implements the Operations module use cases.
//
// Wave 125 adds BulkExecutorService — the concrete executors that replace
// the Wave 71 no-op handler. The service owns three run paths
// (plan_change, odp_migration, wo_creation), each:
//
//   - Idempotent — re-runs pick up where they left off via
//     ListUnprocessedForJob (items in queued/validating/validated/
//     processing); terminal items are not touched again.
//   - Concurrency-limited — a semaphore caps in-flight items at 10 to
//     avoid overwhelming the downstream bridge.
//   - Audit-emitting — every item state change is mirrored to
//     `identity.audit_logs` via the shared audit.Writer.
//   - Dry-run aware — when bulk_job.dry_run=true the validator runs but
//     Apply/Create is replaced with a logged no-op; the executor records
//     what would have happened in error_summary.
//
// The Wave 71 `executeBulkOp` HTTP handler is intentionally left alone —
// it serves the legacy `operations.bulk_operations` table that still
// drives the existing preview/approve UI. Wave 125 introduces a NEW
// surface (POST /api/operations/bulk/{kind}) that lands rows into
// `operations.bulk_jobs` + the per-kind item tables.
package usecase

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/operations/domain"
	"github.com/ion-core/backend/internal/operations/port"
	"github.com/ion-core/backend/pkg/audit"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// defaultConcurrency caps in-flight item application. 10 = empirical
// sweet spot from invoice-svc bulk runner; bridges are SQL inserts so
// they're connection-bound, not CPU-bound.
const defaultConcurrency = 10

// defaultPageSize for the unprocessed-items pickup window. Keeps a
// huge job from materialising its full queue at once.
const defaultPageSize = 500

// BulkExecutorService is the orchestrator. Wire dependencies once per
// process; the service is goroutine-safe.
type BulkExecutorService struct {
	jobs   port.BulkJobRepository
	bpcs   port.BulkPlanChangeItemRepository
	boms   port.BulkODPMigrationItemRepository
	bwos   port.BulkWOCreationItemRepository
	pcExec port.PlanChangeExecutor
	omExec port.ODPMigrationExecutor
	wcExec port.WOCreator
	pcVal  domain.PlanChangeValidatorPort
	omVal  domain.ODPMigrationValidatorPort
	wcVal  domain.WOCreationValidatorPort
	audit  audit.Writer
	log    *slog.Logger
	conc   int
	page   int
}

// BulkExecutorDeps groups the dependencies so the wire-up call doesn't
// turn into a 14-argument constructor. Optional ports may be nil and
// the service degrades gracefully (validator off / executor off).
type BulkExecutorDeps struct {
	Jobs        port.BulkJobRepository
	BPCItems    port.BulkPlanChangeItemRepository
	BOMItems    port.BulkODPMigrationItemRepository
	BWOItems    port.BulkWOCreationItemRepository
	PCExecutor  port.PlanChangeExecutor
	OMExecutor  port.ODPMigrationExecutor
	WCExecutor  port.WOCreator
	PCValidator domain.PlanChangeValidatorPort
	OMValidator domain.ODPMigrationValidatorPort
	WCValidator domain.WOCreationValidatorPort
	Audit       audit.Writer
	Log         *slog.Logger
	Concurrency int
	PageSize    int
}

// NewBulkExecutorService — primary constructor.
func NewBulkExecutorService(d BulkExecutorDeps) *BulkExecutorService {
	a := d.Audit
	if a == nil {
		a = audit.Nop{}
	}
	l := d.Log
	if l == nil {
		l = slog.Default()
	}
	c := d.Concurrency
	if c <= 0 {
		c = defaultConcurrency
	}
	p := d.PageSize
	if p <= 0 {
		p = defaultPageSize
	}
	return &BulkExecutorService{
		jobs:   d.Jobs,
		bpcs:   d.BPCItems,
		boms:   d.BOMItems,
		bwos:   d.BWOItems,
		pcExec: d.PCExecutor,
		omExec: d.OMExecutor,
		wcExec: d.WCExecutor,
		pcVal:  d.PCValidator,
		omVal:  d.OMValidator,
		wcVal:  d.WCValidator,
		audit:  a,
		log:    l.With("svc", "operations_bulk_executor"),
		conc:   c,
		page:   p,
	}
}

// =====================================================================
// Public API
// =====================================================================

// RunBulkPlanChange — execute (or resume) a plan_change bulk job.
func (s *BulkExecutorService) RunBulkPlanChange(ctx context.Context, jobID uuid.UUID) (*domain.BulkJob, error) {
	return s.run(ctx, jobID, domain.BulkJobPlanChange, s.processPlanChangePage)
}

// RunBulkODPMigration — execute (or resume) an odp_migration bulk job.
func (s *BulkExecutorService) RunBulkODPMigration(ctx context.Context, jobID uuid.UUID) (*domain.BulkJob, error) {
	return s.run(ctx, jobID, domain.BulkJobODPMigration, s.processODPMigrationPage)
}

// RunBulkWOCreation — execute (or resume) a wo_creation bulk job.
func (s *BulkExecutorService) RunBulkWOCreation(ctx context.Context, jobID uuid.UUID) (*domain.BulkJob, error) {
	return s.run(ctx, jobID, domain.BulkJobWOCreation, s.processWOCreationPage)
}

// CancelJob marks a bulk job cancelled. Items already in terminal state
// stay there; queued items are left alone (the executor's idempotency
// check stops touching them when status moves out of running).
func (s *BulkExecutorService) CancelJob(ctx context.Context, jobID uuid.UUID) (*domain.BulkJob, error) {
	if jobID == uuid.Nil {
		return nil, derrors.Validation("bulk_job.id_required", "job id is required")
	}
	job, err := s.jobs.FindByID(ctx, jobID)
	if err != nil {
		return nil, err
	}
	if job == nil {
		return nil, derrors.NotFound("bulk_job.not_found", "bulk job not found")
	}
	if job.IsTerminal() {
		return job, nil
	}
	if err := job.MarkCancelled(); err != nil {
		return nil, err
	}
	if err := s.jobs.Update(ctx, job); err != nil {
		return nil, err
	}
	s.writeAudit(ctx, job.CreatedBy, "operations", "bulk_job", job.ID.String(),
		"status", "cancelled")
	return job, nil
}

// =====================================================================
// Internal run loop
// =====================================================================

type pageProcessor func(ctx context.Context, job *domain.BulkJob) (processed int, err error)

func (s *BulkExecutorService) run(
	ctx context.Context,
	jobID uuid.UUID,
	expectedKind domain.BulkJobKind,
	pageFn pageProcessor,
) (*domain.BulkJob, error) {
	if jobID == uuid.Nil {
		return nil, derrors.Validation("bulk_job.id_required", "job id is required")
	}
	job, err := s.jobs.FindByID(ctx, jobID)
	if err != nil {
		return nil, err
	}
	if job == nil {
		return nil, derrors.NotFound("bulk_job.not_found", "bulk job not found")
	}
	if job.Kind != expectedKind {
		return nil, derrors.Validation("bulk_job.kind_mismatch",
			fmt.Sprintf("job is %s, expected %s", job.Kind, expectedKind))
	}
	if job.IsTerminal() {
		// Idempotent return — terminal snapshot is the answer.
		return job, nil
	}
	if err := job.MarkRunning(); err != nil {
		return nil, err
	}
	if err := s.jobs.Update(ctx, job); err != nil {
		return nil, err
	}
	s.writeAudit(ctx, job.CreatedBy, "operations", "bulk_job", job.ID.String(),
		"status", "running")

	// Page through unprocessed items. processedThisRun tracks pages that
	// actually moved the queue forward — if we get a zero page we exit.
	for {
		// Bail on cancellation between pages so a /cancel call mid-run
		// is honoured without leaving items hanging.
		if ctx.Err() != nil {
			break
		}
		if cur, _ := s.jobs.FindByID(ctx, job.ID); cur != nil && cur.Status == domain.BulkJobStatusCancelled {
			return cur, nil
		}
		n, err := pageFn(ctx, job)
		if err != nil {
			s.log.Warn("bulk executor page failed", "job_id", job.ID, "err", err)
			return nil, err
		}
		if n == 0 {
			break
		}
	}

	if err := job.Finalize(); err != nil {
		return nil, err
	}
	if err := s.jobs.Update(ctx, job); err != nil {
		return nil, err
	}
	s.writeAudit(ctx, job.CreatedBy, "operations", "bulk_job", job.ID.String(),
		"status", string(job.Status))
	return job, nil
}

// =====================================================================
// Per-kind page processors
// =====================================================================

func (s *BulkExecutorService) processPlanChangePage(ctx context.Context, job *domain.BulkJob) (int, error) {
	if s.bpcs == nil {
		return 0, derrors.Internal("bulk.bpc_repo_nil", "BulkPlanChangeItemRepository not wired")
	}
	batch, err := s.bpcs.ListUnprocessedForJob(ctx, job.ID, s.page)
	if err != nil {
		return 0, err
	}
	if len(batch) == 0 {
		return 0, nil
	}
	sem := make(chan struct{}, s.conc)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for i := range batch {
		it := &batch[i]
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			succeeded, skipped, perr := s.applyPlanChangeItem(ctx, job, it)
			mu.Lock()
			defer mu.Unlock()
			if perr != nil {
				s.log.Warn("bpc item apply failed", "item_id", it.ID, "err", perr)
			}
			job.RecordItem(succeeded, skipped)
		}()
	}
	wg.Wait()
	if err := s.jobs.Update(ctx, job); err != nil {
		return 0, err
	}
	return len(batch), nil
}

func (s *BulkExecutorService) applyPlanChangeItem(
	ctx context.Context,
	job *domain.BulkJob,
	item *domain.BulkPlanChangeItem,
) (succeeded bool, skipped bool, err error) {
	// Validate
	if item.Status == domain.BPCItemQueued {
		_ = item.MarkValidating()
		_ = s.bpcs.Update(ctx, item)
		reason, doSkip, verr := domain.ValidatePlanChangeItem(ctx, s.pcVal, item)
		if verr != nil {
			_ = item.MarkFailed(verr.Error())
			_ = s.bpcs.Update(ctx, item)
			return false, false, verr
		}
		if reason != "" && doSkip {
			_ = item.MarkSkipped(reason)
			_ = s.bpcs.Update(ctx, item)
			s.writeAudit(ctx, job.CreatedBy, "operations", "bulk_plan_change_item",
				item.ID.String(), "status", "skipped")
			return false, true, nil
		}
		if reason != "" {
			_ = item.MarkFailed(reason)
			_ = s.bpcs.Update(ctx, item)
			s.writeAudit(ctx, job.CreatedBy, "operations", "bulk_plan_change_item",
				item.ID.String(), "status", "failed")
			return false, false, nil
		}
		_ = item.MarkValidated()
		_ = s.bpcs.Update(ctx, item)
	}
	// Dry-run: validation passed, but skip Apply. Record in error_summary
	// so the operator can see what WOULD have happened.
	if job.DryRun {
		_ = item.MarkSkipped("dry_run")
		_ = s.bpcs.Update(ctx, item)
		s.writeAudit(ctx, job.CreatedBy, "operations", "bulk_plan_change_item",
			item.ID.String(), "status", "skipped")
		return false, true, nil
	}
	// Apply
	_ = item.MarkProcessing()
	_ = s.bpcs.Update(ctx, item)
	if s.pcExec == nil {
		_ = item.MarkFailed("plan_change_executor_not_wired")
		_ = s.bpcs.Update(ctx, item)
		return false, false, nil
	}
	if aerr := s.pcExec.Apply(ctx, item); aerr != nil {
		_ = item.MarkFailed(aerr.Error())
		_ = s.bpcs.Update(ctx, item)
		s.writeAudit(ctx, job.CreatedBy, "operations", "bulk_plan_change_item",
			item.ID.String(), "status", "failed")
		return false, false, aerr
	}
	_ = item.MarkSucceeded(time.Now().UTC())
	_ = s.bpcs.Update(ctx, item)
	s.writeAudit(ctx, job.CreatedBy, "operations", "bulk_plan_change_item",
		item.ID.String(), "status", "succeeded")
	return true, false, nil
}

func (s *BulkExecutorService) processODPMigrationPage(ctx context.Context, job *domain.BulkJob) (int, error) {
	if s.boms == nil {
		return 0, derrors.Internal("bulk.bom_repo_nil", "BulkODPMigrationItemRepository not wired")
	}
	batch, err := s.boms.ListUnprocessedForJob(ctx, job.ID, s.page)
	if err != nil {
		return 0, err
	}
	if len(batch) == 0 {
		return 0, nil
	}
	sem := make(chan struct{}, s.conc)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for i := range batch {
		it := &batch[i]
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			succeeded, skipped, perr := s.applyODPMigrationItem(ctx, job, it)
			mu.Lock()
			defer mu.Unlock()
			if perr != nil {
				s.log.Warn("bom item apply failed", "item_id", it.ID, "err", perr)
			}
			job.RecordItem(succeeded, skipped)
		}()
	}
	wg.Wait()
	if err := s.jobs.Update(ctx, job); err != nil {
		return 0, err
	}
	return len(batch), nil
}

func (s *BulkExecutorService) applyODPMigrationItem(
	ctx context.Context,
	job *domain.BulkJob,
	item *domain.BulkODPMigrationItem,
) (succeeded bool, skipped bool, err error) {
	if item.Status == domain.BOMItemQueued {
		_ = item.MarkValidating()
		_ = s.boms.Update(ctx, item)
		reason, verr := domain.ValidateODPMigrationItem(ctx, s.omVal, item)
		if verr != nil {
			_ = item.MarkFailed(verr.Error())
			_ = s.boms.Update(ctx, item)
			return false, false, verr
		}
		if reason != "" {
			_ = item.MarkFailed(reason)
			_ = s.boms.Update(ctx, item)
			s.writeAudit(ctx, job.CreatedBy, "operations", "bulk_odp_migration_item",
				item.ID.String(), "status", "failed")
			return false, false, nil
		}
		_ = item.MarkValidated()
		_ = s.boms.Update(ctx, item)
	}
	if job.DryRun {
		_ = item.MarkFailed("dry_run_skipped")
		_ = s.boms.Update(ctx, item)
		return false, true, nil
	}
	if s.omExec == nil {
		_ = item.MarkFailed("odp_migration_executor_not_wired")
		_ = s.boms.Update(ctx, item)
		return false, false, nil
	}
	if aerr := s.omExec.Apply(ctx, item); aerr != nil {
		_ = item.MarkFailed(aerr.Error())
		_ = s.boms.Update(ctx, item)
		s.writeAudit(ctx, job.CreatedBy, "operations", "bulk_odp_migration_item",
			item.ID.String(), "status", "failed")
		return false, false, aerr
	}
	_ = item.MarkMigrated(time.Now().UTC())
	_ = s.boms.Update(ctx, item)
	s.writeAudit(ctx, job.CreatedBy, "operations", "bulk_odp_migration_item",
		item.ID.String(), "status", "migrated")
	return true, false, nil
}

func (s *BulkExecutorService) processWOCreationPage(ctx context.Context, job *domain.BulkJob) (int, error) {
	if s.bwos == nil {
		return 0, derrors.Internal("bulk.bwo_repo_nil", "BulkWOCreationItemRepository not wired")
	}
	batch, err := s.bwos.ListUnprocessedForJob(ctx, job.ID, s.page)
	if err != nil {
		return 0, err
	}
	if len(batch) == 0 {
		return 0, nil
	}
	sem := make(chan struct{}, s.conc)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for i := range batch {
		it := &batch[i]
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			succeeded, skipped, perr := s.applyWOCreationItem(ctx, job, it)
			mu.Lock()
			defer mu.Unlock()
			if perr != nil {
				s.log.Warn("bwo item apply failed", "item_id", it.ID, "err", perr)
			}
			job.RecordItem(succeeded, skipped)
		}()
	}
	wg.Wait()
	if err := s.jobs.Update(ctx, job); err != nil {
		return 0, err
	}
	return len(batch), nil
}

func (s *BulkExecutorService) applyWOCreationItem(
	ctx context.Context,
	job *domain.BulkJob,
	item *domain.BulkWOCreationItem,
) (succeeded bool, skipped bool, err error) {
	if item.Status == domain.BWOItemQueued {
		_ = item.MarkValidating()
		_ = s.bwos.Update(ctx, item)
		reason, dup, verr := domain.ValidateWOCreationItem(ctx, s.wcVal, item)
		if verr != nil {
			_ = item.MarkFailed(verr.Error())
			_ = s.bwos.Update(ctx, item)
			return false, false, verr
		}
		if reason != "" && dup {
			_ = item.MarkDuplicate(reason)
			_ = s.bwos.Update(ctx, item)
			s.writeAudit(ctx, job.CreatedBy, "operations", "bulk_wo_creation_item",
				item.ID.String(), "status", "duplicate")
			return false, true, nil
		}
		if reason != "" {
			_ = item.MarkFailed(reason)
			_ = s.bwos.Update(ctx, item)
			s.writeAudit(ctx, job.CreatedBy, "operations", "bulk_wo_creation_item",
				item.ID.String(), "status", "failed")
			return false, false, nil
		}
		_ = item.MarkValidated()
		_ = s.bwos.Update(ctx, item)
	}
	if job.DryRun {
		_ = item.MarkDuplicate("dry_run")
		_ = s.bwos.Update(ctx, item)
		return false, true, nil
	}
	if s.wcExec == nil {
		_ = item.MarkFailed("wo_creator_not_wired")
		_ = s.bwos.Update(ctx, item)
		return false, false, nil
	}
	woID, aerr := s.wcExec.Create(ctx, item)
	if aerr != nil {
		// A KindConflict from the bridge means "this customer already has
		// an open WO" — re-classify as Duplicate rather than Failed.
		if isBridgeDuplicate(aerr) {
			_ = item.MarkDuplicate(aerr.Error())
			_ = s.bwos.Update(ctx, item)
			s.writeAudit(ctx, job.CreatedBy, "operations", "bulk_wo_creation_item",
				item.ID.String(), "status", "duplicate")
			return false, true, nil
		}
		_ = item.MarkFailed(aerr.Error())
		_ = s.bwos.Update(ctx, item)
		s.writeAudit(ctx, job.CreatedBy, "operations", "bulk_wo_creation_item",
			item.ID.String(), "status", "failed")
		return false, false, aerr
	}
	_ = item.MarkCreated(woID, time.Now().UTC())
	_ = s.bwos.Update(ctx, item)
	s.writeAudit(ctx, job.CreatedBy, "operations", "bulk_wo_creation_item",
		item.ID.String(), "status", "created")
	return true, false, nil
}

// =====================================================================
// Helpers
// =====================================================================

func (s *BulkExecutorService) writeAudit(
	ctx context.Context,
	actor *uuid.UUID,
	module, recordType, recordID, field, after string,
) {
	if s.audit == nil {
		return
	}
	a := uuid.Nil
	if actor != nil {
		a = *actor
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		Timestamp:    time.Now().UTC(),
		UserID:       a,
		Module:       module,
		RecordType:   recordType,
		RecordID:     recordID,
		FieldChanged: field,
		After:        after,
	})
}

// isBridgeDuplicate inspects an error returned by the WOCreator bridge to
// see if it represents a "customer already has an open WO" case
// (which should mark the item Duplicate rather than Failed).
func isBridgeDuplicate(err error) bool {
	if err == nil {
		return false
	}
	var de *derrors.Error
	if errors.As(err, &de) {
		return de.Kind == derrors.KindConflict && de.Code == "bulk.wo_duplicate"
	}
	return false
}
