package usecase

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/invoicesvc/domain"
	"github.com/ion-core/backend/internal/invoicesvc/port"
	"github.com/ion-core/backend/pkg/errors"
)

// BulkService implements port.BulkUseCase.
//
// Two-step contract:
//
//   1. StartJob persists the job + queue items (one per customer the
//      filter resolves to). The job sits in 'pending'.
//   2. RunJob picks the job up (either on-demand from HTTP or from the
//      BulkJobRunner cron), iterates queued items, calls
//      InvoiceGenerator per customer, and rolls up the terminal status.
//
// Idempotency: RunJob is safe to retry because per-item state machine
// only accepts queued→{generated,failed,skipped}; a second call sees
// no queued items and finishes the job at its current status.
type BulkService struct {
	jobs      port.BulkJobRepository
	items     port.BulkItemRepository
	reader    port.InvoiceReader
	generator port.InvoiceGenerator
	// maxItemsPerJob caps the queue size so a runaway filter doesn't OOM
	// the runner. Configurable via env when wiring; 0 falls back to
	// defaultMaxItems.
	maxItemsPerJob int
}

const defaultMaxItems = 200000 // 200k — TC-IGE-007 NFR is 100k / 30 min.

func NewBulkService(
	jobs port.BulkJobRepository,
	items port.BulkItemRepository,
	reader port.InvoiceReader,
	generator port.InvoiceGenerator,
) *BulkService {
	return &BulkService{
		jobs:           jobs,
		items:          items,
		reader:         reader,
		generator:      generator,
		maxItemsPerJob: defaultMaxItems,
	}
}

var _ port.BulkUseCase = (*BulkService)(nil)

// StartJob materializes the customer queue + persists the job. If the
// reader resolves zero customers, the job is created and immediately
// finishes as 'completed' (no items to fail).
func (s *BulkService) StartJob(ctx context.Context, in port.StartBulkJobInput) (*domain.BulkGenerationJob, error) {
	job, err := domain.NewBulkGenerationJob(in.Kind, in.TargetFilter, in.CreatedBy)
	if err != nil {
		return nil, err
	}
	customers, err := s.resolveCustomers(ctx, in.TargetFilter)
	if err != nil {
		return nil, err
	}
	if len(customers) > s.maxItemsPerJob {
		return nil, errors.Validation("bulk_job.too_many",
			"target filter resolves to more customers than the per-job cap")
	}
	job.TotalExpected = len(customers)
	if err := s.jobs.Create(ctx, job); err != nil {
		return nil, err
	}
	items := make([]domain.BulkGenerationItem, 0, len(customers))
	for _, cid := range customers {
		c := cid
		items = append(items, domain.BulkGenerationItem{
			ID:         uuid.New(),
			JobID:      job.ID,
			CustomerID: &c,
			Status:     domain.ItemStatusQueued,
		})
	}
	if len(items) > 0 {
		if err := s.items.CreateBatch(ctx, items); err != nil {
			return nil, err
		}
	}
	return job, nil
}

// RunJob walks the queued items, dispatches each through the
// InvoiceGenerator, and finalizes the job.
func (s *BulkService) RunJob(ctx context.Context, id uuid.UUID) (*domain.BulkGenerationJob, error) {
	if id == uuid.Nil {
		return nil, errors.Validation("bulk_job.id_required", "job id is required")
	}
	job, err := s.jobs.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if job == nil {
		return nil, errors.NotFound("bulk_job.not_found", "bulk job not found")
	}
	if job.IsTerminal() {
		// Idempotent return — the terminal snapshot is the answer.
		return job, nil
	}
	if err := job.Start(time.Now().UTC()); err != nil {
		return nil, err
	}
	if err := s.jobs.Update(ctx, job); err != nil {
		return nil, err
	}

	if s.generator == nil {
		// No generator wired — fail every queued item with a typed
		// reason. We still update the per-item rows so the dashboard
		// surfaces the misconfiguration.
		return s.finalizeWithoutGenerator(ctx, job)
	}

	processed, err := s.processQueuedItems(ctx, job)
	if err != nil {
		return nil, err
	}
	if err := job.Finish(processed, time.Now().UTC()); err != nil {
		return nil, err
	}
	if err := s.jobs.Update(ctx, job); err != nil {
		return nil, err
	}
	return job, nil
}

// JobStatus returns the job + its items. Items shouldn't be huge for a
// status-check (we cap reads inside the repo).
func (s *BulkService) JobStatus(ctx context.Context, id uuid.UUID) (*domain.BulkGenerationJob, []domain.BulkGenerationItem, error) {
	if id == uuid.Nil {
		return nil, nil, errors.Validation("bulk_job.id_required", "job id is required")
	}
	job, err := s.jobs.FindByID(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if job == nil {
		return nil, nil, errors.NotFound("bulk_job.not_found", "bulk job not found")
	}
	items, err := s.items.ListByJob(ctx, id)
	if err != nil {
		return job, nil, err
	}
	return job, items, nil
}

func (s *BulkService) List(ctx context.Context, f port.BulkJobFilter) ([]domain.BulkGenerationJob, int, error) {
	if f.Limit <= 0 {
		f.Limit = 50
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	return s.jobs.List(ctx, f)
}

// resolveCustomers is the seam between target_filter (opaque map) and
// the actual customer set. The SQL adapter does the heavy lifting; this
// service only sanity-checks.
func (s *BulkService) resolveCustomers(ctx context.Context, filter map[string]any) ([]uuid.UUID, error) {
	if s.reader == nil {
		return nil, errors.Internal("bulk_job.reader_nil", "invoice reader not configured")
	}
	if filter == nil {
		filter = map[string]any{}
	}
	return s.reader.FindForBulkRun(ctx, filter)
}

func (s *BulkService) processQueuedItems(ctx context.Context, job *domain.BulkGenerationJob) ([]domain.BulkGenerationItem, error) {
	out := []domain.BulkGenerationItem{}
	// Page through queued items so very large jobs don't materialize the
	// whole queue in memory.
	const pageSize = 500
	for {
		batch, err := s.items.ListQueuedForJob(ctx, job.ID, pageSize)
		if err != nil {
			return nil, err
		}
		if len(batch) == 0 {
			// Pick up any non-queued items so Finish can roll them up.
			final, err := s.items.ListByJob(ctx, job.ID)
			if err != nil {
				return nil, err
			}
			out = final
			break
		}
		for i := range batch {
			it := batch[i]
			if it.CustomerID == nil {
				_ = it.MarkSkipped("missing customer id")
				if err := s.items.Update(ctx, &it); err != nil {
					return nil, err
				}
				continue
			}
			gen, err := s.generator.GenerateForCustomer(ctx, *it.CustomerID, uuid.Nil, port.GenerationKind(job.Kind))
			if err != nil {
				_ = it.MarkFailed(err.Error())
				if uerr := s.items.Update(ctx, &it); uerr != nil {
					return nil, uerr
				}
				continue
			}
			if gen == nil {
				_ = it.MarkSkipped("generator returned nil (no invoice needed)")
				if uerr := s.items.Update(ctx, &it); uerr != nil {
					return nil, uerr
				}
				continue
			}
			if err := it.MarkGenerated(gen.InvoiceID, time.Now().UTC()); err != nil {
				_ = it.MarkFailed(err.Error())
			}
			if uerr := s.items.Update(ctx, &it); uerr != nil {
				return nil, uerr
			}
		}
	}
	return out, nil
}

func (s *BulkService) finalizeWithoutGenerator(ctx context.Context, job *domain.BulkGenerationJob) (*domain.BulkGenerationJob, error) {
	queued, err := s.items.ListQueuedForJob(ctx, job.ID, 0)
	if err != nil {
		return nil, err
	}
	for i := range queued {
		_ = queued[i].MarkFailed("invoice generator not configured")
		if err := s.items.Update(ctx, &queued[i]); err != nil {
			return nil, err
		}
	}
	final, err := s.items.ListByJob(ctx, job.ID)
	if err != nil {
		return nil, err
	}
	if err := job.Finish(final, time.Now().UTC()); err != nil {
		return nil, err
	}
	if err := s.jobs.Update(ctx, job); err != nil {
		return nil, err
	}
	return job, nil
}
