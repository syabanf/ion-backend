package usecase

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/operations/domain"
	"github.com/ion-core/backend/internal/operations/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// In-memory stubs
// =====================================================================

type fakeJobRepo struct {
	mu     sync.Mutex
	byID   map[uuid.UUID]*domain.BulkJob
}

func newFakeJobRepo() *fakeJobRepo { return &fakeJobRepo{byID: map[uuid.UUID]*domain.BulkJob{}} }

func (r *fakeJobRepo) Create(_ context.Context, j *domain.BulkJob) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	clone := *j
	r.byID[j.ID] = &clone
	return nil
}
func (r *fakeJobRepo) Update(_ context.Context, j *domain.BulkJob) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byID[j.ID]; !ok {
		return derrors.NotFound("not_found", "")
	}
	clone := *j
	r.byID[j.ID] = &clone
	return nil
}
func (r *fakeJobRepo) FindByID(_ context.Context, id uuid.UUID) (*domain.BulkJob, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.byID[id]
	if !ok {
		return nil, nil
	}
	clone := *j
	return &clone, nil
}
func (r *fakeJobRepo) List(_ context.Context, _ port.BulkJobFilter) ([]domain.BulkJob, int, error) {
	return nil, 0, nil
}
func (r *fakeJobRepo) ListRunnable(_ context.Context, limit int) ([]domain.BulkJob, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []domain.BulkJob{}
	for _, j := range r.byID {
		if j.Status == domain.BulkJobStatusPending || j.Status == domain.BulkJobStatusRunning {
			out = append(out, *j)
		}
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// fakeBPC — bulk plan change item repo
type fakeBPC struct {
	mu    sync.Mutex
	items map[uuid.UUID]*domain.BulkPlanChangeItem
}

func newFakeBPC() *fakeBPC { return &fakeBPC{items: map[uuid.UUID]*domain.BulkPlanChangeItem{}} }
func (r *fakeBPC) CreateBatch(_ context.Context, items []domain.BulkPlanChangeItem) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range items {
		clone := items[i]
		r.items[clone.ID] = &clone
	}
	return nil
}
func (r *fakeBPC) Update(_ context.Context, it *domain.BulkPlanChangeItem) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.items[it.ID]; !ok {
		return derrors.NotFound("not_found", "")
	}
	clone := *it
	r.items[it.ID] = &clone
	return nil
}
func (r *fakeBPC) FindByID(_ context.Context, id uuid.UUID) (*domain.BulkPlanChangeItem, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	it, ok := r.items[id]
	if !ok {
		return nil, nil
	}
	clone := *it
	return &clone, nil
}
func (r *fakeBPC) ListByJob(_ context.Context, jobID uuid.UUID, _, _ int) ([]domain.BulkPlanChangeItem, error) {
	return r.matching(jobID, nil), nil
}
func (r *fakeBPC) ListUnprocessedForJob(_ context.Context, jobID uuid.UUID, _ int) ([]domain.BulkPlanChangeItem, error) {
	open := map[domain.BulkPlanChangeItemStatus]bool{
		domain.BPCItemQueued:     true,
		domain.BPCItemValidating: true,
		domain.BPCItemValidated:  true,
		domain.BPCItemProcessing: true,
	}
	return r.matching(jobID, open), nil
}
func (r *fakeBPC) matching(jobID uuid.UUID, statuses map[domain.BulkPlanChangeItemStatus]bool) []domain.BulkPlanChangeItem {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []domain.BulkPlanChangeItem{}
	for _, it := range r.items {
		if it.BulkJobID != jobID {
			continue
		}
		if statuses != nil && !statuses[it.Status] {
			continue
		}
		out = append(out, *it)
	}
	return out
}

// fakePCExec — controllable executor
type fakePCExec struct {
	failFor map[uuid.UUID]error
	called  int
	mu      sync.Mutex
}

func (e *fakePCExec) Apply(_ context.Context, item *domain.BulkPlanChangeItem) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.called++
	if e.failFor != nil {
		if err, ok := e.failFor[item.ID]; ok {
			return err
		}
	}
	return nil
}

// =====================================================================
// Tests
// =====================================================================

func TestRunBulkPlanChange_AllSucceed(t *testing.T) {
	ctx := context.Background()
	jobs := newFakeJobRepo()
	items := newFakeBPC()
	pc := &fakePCExec{}

	svc := NewBulkExecutorService(BulkExecutorDeps{
		Jobs: jobs, BPCItems: items, PCExecutor: pc,
	})

	job, _ := domain.NewBulkJob(domain.BulkJobPlanChange, false, nil)
	job.TotalItems = 3
	_ = jobs.Create(ctx, job)

	for i := 0; i < 3; i++ {
		it := domain.BulkPlanChangeItem{
			ID:           uuid.New(),
			BulkJobID:    job.ID,
			CustomerID:   uuid.New(),
			TargetPlanID: uuid.New(),
			Status:       domain.BPCItemQueued,
		}
		_ = items.CreateBatch(ctx, []domain.BulkPlanChangeItem{it})
	}

	out, err := svc.RunBulkPlanChange(ctx, job.ID)
	if err != nil {
		t.Fatalf("RunBulkPlanChange: %v", err)
	}
	if out.Status != domain.BulkJobStatusCompleted {
		t.Errorf("status: want completed, got %s", out.Status)
	}
	if out.SucceededItems != 3 {
		t.Errorf("succeeded: want 3, got %d", out.SucceededItems)
	}
	if pc.called != 3 {
		t.Errorf("executor calls: want 3, got %d", pc.called)
	}
}

func TestRunBulkPlanChange_MixedOutcomes(t *testing.T) {
	ctx := context.Background()
	jobs := newFakeJobRepo()
	items := newFakeBPC()

	job, _ := domain.NewBulkJob(domain.BulkJobPlanChange, false, nil)
	job.TotalItems = 3
	_ = jobs.Create(ctx, job)

	good1 := newBPC(job.ID)
	good2 := newBPC(job.ID)
	bad := newBPC(job.ID)
	_ = items.CreateBatch(ctx, []domain.BulkPlanChangeItem{good1, good2, bad})

	pc := &fakePCExec{
		failFor: map[uuid.UUID]error{bad.ID: errors.New("downstream rejected")},
	}
	svc := NewBulkExecutorService(BulkExecutorDeps{
		Jobs: jobs, BPCItems: items, PCExecutor: pc,
	})

	out, err := svc.RunBulkPlanChange(ctx, job.ID)
	if err != nil {
		t.Fatalf("RunBulkPlanChange: %v", err)
	}
	if out.Status != domain.BulkJobStatusPartial {
		t.Errorf("status: want partial, got %s", out.Status)
	}
	if out.SucceededItems != 2 || out.FailedItems != 1 {
		t.Errorf("counters: succeeded=%d failed=%d", out.SucceededItems, out.FailedItems)
	}
}

func TestRunBulkPlanChange_DryRun(t *testing.T) {
	ctx := context.Background()
	jobs := newFakeJobRepo()
	items := newFakeBPC()
	pc := &fakePCExec{}

	job, _ := domain.NewBulkJob(domain.BulkJobPlanChange, true, nil) // dry_run=true
	job.TotalItems = 2
	_ = jobs.Create(ctx, job)

	it1 := newBPC(job.ID)
	it2 := newBPC(job.ID)
	_ = items.CreateBatch(ctx, []domain.BulkPlanChangeItem{it1, it2})

	svc := NewBulkExecutorService(BulkExecutorDeps{
		Jobs: jobs, BPCItems: items, PCExecutor: pc,
	})

	out, err := svc.RunBulkPlanChange(ctx, job.ID)
	if err != nil {
		t.Fatalf("RunBulkPlanChange: %v", err)
	}
	if pc.called != 0 {
		t.Errorf("executor must not be called in dry_run; got %d", pc.called)
	}
	if out.SkippedItems != 2 {
		t.Errorf("skipped: want 2, got %d", out.SkippedItems)
	}
}

func TestRunBulkPlanChange_Idempotent(t *testing.T) {
	ctx := context.Background()
	jobs := newFakeJobRepo()
	items := newFakeBPC()
	pc := &fakePCExec{}

	job, _ := domain.NewBulkJob(domain.BulkJobPlanChange, false, nil)
	job.TotalItems = 2
	_ = jobs.Create(ctx, job)

	it1 := newBPC(job.ID)
	it2 := newBPC(job.ID)
	_ = items.CreateBatch(ctx, []domain.BulkPlanChangeItem{it1, it2})

	svc := NewBulkExecutorService(BulkExecutorDeps{
		Jobs: jobs, BPCItems: items, PCExecutor: pc,
	})

	if _, err := svc.RunBulkPlanChange(ctx, job.ID); err != nil {
		t.Fatalf("first run: %v", err)
	}
	firstCalls := pc.called

	out, err := svc.RunBulkPlanChange(ctx, job.ID)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if pc.called != firstCalls {
		t.Errorf("idempotent: extra Apply calls (was %d, now %d)", firstCalls, pc.called)
	}
	if out.Status != domain.BulkJobStatusCompleted {
		t.Errorf("status: want completed, got %s", out.Status)
	}
}

func TestCancelJob(t *testing.T) {
	ctx := context.Background()
	jobs := newFakeJobRepo()
	svc := NewBulkExecutorService(BulkExecutorDeps{Jobs: jobs})

	job, _ := domain.NewBulkJob(domain.BulkJobPlanChange, false, nil)
	_ = jobs.Create(ctx, job)

	out, err := svc.CancelJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("CancelJob: %v", err)
	}
	if out.Status != domain.BulkJobStatusCancelled {
		t.Errorf("status: want cancelled, got %s", out.Status)
	}
}

func TestRunBulkPlanChange_KindMismatch(t *testing.T) {
	ctx := context.Background()
	jobs := newFakeJobRepo()
	svc := NewBulkExecutorService(BulkExecutorDeps{Jobs: jobs})

	job, _ := domain.NewBulkJob(domain.BulkJobODPMigration, false, nil)
	_ = jobs.Create(ctx, job)

	if _, err := svc.RunBulkPlanChange(ctx, job.ID); err == nil {
		t.Fatalf("kind mismatch should error")
	}
}

func TestRunBulkPlanChange_NoExecutor(t *testing.T) {
	ctx := context.Background()
	jobs := newFakeJobRepo()
	items := newFakeBPC()
	svc := NewBulkExecutorService(BulkExecutorDeps{Jobs: jobs, BPCItems: items})

	job, _ := domain.NewBulkJob(domain.BulkJobPlanChange, false, nil)
	job.TotalItems = 1
	_ = jobs.Create(ctx, job)
	it := newBPC(job.ID)
	_ = items.CreateBatch(ctx, []domain.BulkPlanChangeItem{it})

	out, err := svc.RunBulkPlanChange(ctx, job.ID)
	if err != nil {
		t.Fatalf("RunBulkPlanChange: %v", err)
	}
	if out.Status != domain.BulkJobStatusFailed {
		t.Errorf("no executor wired → all fail → status failed, got %s", out.Status)
	}
}

func newBPC(jobID uuid.UUID) domain.BulkPlanChangeItem {
	return domain.BulkPlanChangeItem{
		ID:           uuid.New(),
		BulkJobID:    jobID,
		CustomerID:   uuid.New(),
		TargetPlanID: uuid.New(),
		Status:       domain.BPCItemQueued,
	}
}
