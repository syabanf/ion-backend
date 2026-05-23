// Package port defines the contracts between the operations usecase layer
// and the world outside it.
//
// Wave 125 adds the BulkExecutorService ports:
//
//   - Driven repositories (BulkJobRepository + 3 per-kind item repos) that
//     persist the aggregate against `operations.bulk_jobs` +
//     `operations.bulk_<kind>_items`.
//   - Cross-context executor ports (PlanChangeExecutor, ODPMigrationExecutor,
//     WOCreator) that bridge into CRM, Network, and Field WITHOUT importing
//     their Go packages. Implementations live in
//     `internal/operations/adapter/{crm,network,field}/` as SQL-only
//     adapters.
//   - Pre-flight inspection ports for the domain validators. These are
//     defined in `internal/operations/domain/bulk_validators.go` and
//     implemented by the same SQL-only bridges.
package port

import (
	"context"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/operations/domain"
)

// =====================================================================
// 1. Bulk job + item repos
// =====================================================================

// BulkJobFilter — list parameters for the GET endpoints.
type BulkJobFilter struct {
	Kind   string
	Status string
	Limit  int
	Offset int
}

// BulkJobRepository persists the bulk_jobs aggregate.
type BulkJobRepository interface {
	Create(ctx context.Context, j *domain.BulkJob) error
	Update(ctx context.Context, j *domain.BulkJob) error
	FindByID(ctx context.Context, id uuid.UUID) (*domain.BulkJob, error)
	List(ctx context.Context, f BulkJobFilter) ([]domain.BulkJob, int, error)
	// ListRunnable returns jobs in 'pending' or 'running' status that
	// still have queued items. Used by the cron pickup loop. Limit caps
	// the number of jobs returned per tick.
	ListRunnable(ctx context.Context, limit int) ([]domain.BulkJob, error)
}

// BulkPlanChangeItemRepository persists rows of operations.bulk_plan_change_items.
type BulkPlanChangeItemRepository interface {
	CreateBatch(ctx context.Context, items []domain.BulkPlanChangeItem) error
	Update(ctx context.Context, item *domain.BulkPlanChangeItem) error
	FindByID(ctx context.Context, id uuid.UUID) (*domain.BulkPlanChangeItem, error)
	ListByJob(ctx context.Context, jobID uuid.UUID, limit, offset int) ([]domain.BulkPlanChangeItem, error)
	// ListUnprocessedForJob returns items still in queued/validating/
	// validated/processing state — i.e. anything the executor still has
	// work to do on. Idempotent re-runs use this.
	ListUnprocessedForJob(ctx context.Context, jobID uuid.UUID, limit int) ([]domain.BulkPlanChangeItem, error)
}

// BulkODPMigrationItemRepository persists rows of operations.bulk_odp_migration_items.
type BulkODPMigrationItemRepository interface {
	CreateBatch(ctx context.Context, items []domain.BulkODPMigrationItem) error
	Update(ctx context.Context, item *domain.BulkODPMigrationItem) error
	FindByID(ctx context.Context, id uuid.UUID) (*domain.BulkODPMigrationItem, error)
	ListByJob(ctx context.Context, jobID uuid.UUID, limit, offset int) ([]domain.BulkODPMigrationItem, error)
	ListUnprocessedForJob(ctx context.Context, jobID uuid.UUID, limit int) ([]domain.BulkODPMigrationItem, error)
}

// BulkWOCreationItemRepository persists rows of operations.bulk_wo_creation_items.
type BulkWOCreationItemRepository interface {
	CreateBatch(ctx context.Context, items []domain.BulkWOCreationItem) error
	Update(ctx context.Context, item *domain.BulkWOCreationItem) error
	FindByID(ctx context.Context, id uuid.UUID) (*domain.BulkWOCreationItem, error)
	ListByJob(ctx context.Context, jobID uuid.UUID, limit, offset int) ([]domain.BulkWOCreationItem, error)
	ListUnprocessedForJob(ctx context.Context, jobID uuid.UUID, limit int) ([]domain.BulkWOCreationItem, error)
}

// =====================================================================
// 2. Cross-context executor ports
// =====================================================================

// PlanChangeExecutor bridges into the CRM bounded context. The
// implementation inserts a row into `crm.plan_change_requests` with the
// item's parameters (customer + from/to product + effective_at). No
// state-machine traversal in the bridge; CRM owns the lifecycle.
type PlanChangeExecutor interface {
	// Apply persists the change request. Returns nil on success, or a
	// typed error (KindUnavailable if crm schema not installed,
	// KindValidation if the input violates a domain constraint,
	// KindInternal otherwise).
	Apply(ctx context.Context, item *domain.BulkPlanChangeItem) error
}

// ODPMigrationExecutor bridges into the Network + Field contexts. The
// implementation:
//
//	1. Asserts the destination port still has capacity (re-checks
//	   capacity at apply-time, since validation may have raced)
//	2. Creates a "maintenance" WO via the WOCreator port for field crews
//	   to physically re-splice (if scheduled_window is present)
//	3. Updates network.ports.customer_id to the new port
type ODPMigrationExecutor interface {
	Apply(ctx context.Context, item *domain.BulkODPMigrationItem) error
}

// WOCreator bridges into the Field bounded context. The implementation
// inserts a row into `field.work_orders` with status='created' and the
// item's parameters.
type WOCreator interface {
	// Create returns the new WO id on success, or:
	//   - KindConflict + code 'bulk.wo_duplicate' if the customer already
	//     has an open WO of the same type (the executor should mark the
	//     item Duplicate, not Failed)
	//   - KindUnavailable if field schema not installed
	Create(ctx context.Context, item *domain.BulkWOCreationItem) (uuid.UUID, error)
}

// =====================================================================
// 3. CSV lookup ports — used by the importer to resolve human-readable
//    codes (customer_no, plan_code, port_code, template_code) to UUIDs.
//    Implementations live in the cross-context bridges.
// =====================================================================

// CSVLookupPort resolves human-readable codes to UUIDs at import time.
// All methods return (nil, nil) when the code can't be resolved, so the
// importer can collect a per-row error without bailing the whole batch.
type CSVLookupPort interface {
	CustomerIDByNumber(ctx context.Context, customerNo string) (*uuid.UUID, error)
	PlanIDByCode(ctx context.Context, planCode string) (*uuid.UUID, error)
	PortIDByCode(ctx context.Context, portCode string) (*uuid.UUID, error)
	WOTemplateIDByCode(ctx context.Context, templateCode string) (*uuid.UUID, error)
}
