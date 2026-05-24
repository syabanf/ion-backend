package postgres

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/invoicesvc/domain"
	"github.com/ion-core/backend/internal/invoicesvc/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// Bulk job repo
// =====================================================================

type BulkJobRepository struct {
	pool *pgxpool.Pool
}

func NewBulkJobRepository(pool *pgxpool.Pool) *BulkJobRepository {
	return &BulkJobRepository{pool: pool}
}

var _ port.BulkJobRepository = (*BulkJobRepository)(nil)

// Wave 128 fix — wrap nullable jsonb in COALESCE so the Scan into the
// `errRaw string` / `targetRaw string` locals doesn't fail with
// "cannot scan NULL into *string". Pre-Wave-128 callers writing
// without error_summary tripped this and the e2e test for partial
// bulk-jobs had to t.Skip.
const bulkJobCols = `
	id, kind, COALESCE(target_filter::text, ''), status,
	COALESCE(total_expected, 0), COALESCE(total_generated, 0), COALESCE(total_failed, 0),
	started_at, completed_at, COALESCE(error_summary::text, ''),
	created_by, created_at, updated_at
`

func (r *BulkJobRepository) Create(ctx context.Context, j *domain.BulkGenerationJob) error {
	if j == nil {
		return derrors.Validation("bulk_job.nil", "job is nil")
	}
	targetJSON, err := marshalAnyMap(j.TargetFilter)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "bulk_job.marshal", "marshal target_filter", err)
	}
	errJSON, err := marshalAnyMap(j.ErrorSummary)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "bulk_job.marshal", "marshal error_summary", err)
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO invoicesvc.bulk_generation_jobs
			(id, kind, target_filter, status,
			 total_expected, total_generated, total_failed,
			 started_at, completed_at, error_summary,
			 created_by, created_at, updated_at)
		VALUES ($1, $2, $3::jsonb, $4, $5, $6, $7, $8, $9, $10::jsonb, $11, $12, $13)
	`,
		j.ID, string(j.Kind), targetJSON, string(j.Status),
		j.TotalExpected, j.TotalGenerated, j.TotalFailed,
		j.StartedAt, j.CompletedAt, errJSON,
		j.CreatedBy, j.CreatedAt, j.UpdatedAt,
	)
	if err != nil {
		return mapDBError(err, "bulk_job", "insert bulk job")
	}
	return nil
}

func (r *BulkJobRepository) Update(ctx context.Context, j *domain.BulkGenerationJob) error {
	errJSON, err := marshalAnyMap(j.ErrorSummary)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "bulk_job.marshal", "marshal error_summary", err)
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE invoicesvc.bulk_generation_jobs
		SET status         = $2,
		    total_expected = $3,
		    total_generated= $4,
		    total_failed   = $5,
		    started_at     = $6,
		    completed_at   = $7,
		    error_summary  = COALESCE($8::jsonb, error_summary),
		    updated_at     = NOW()
		WHERE id = $1
	`,
		j.ID, string(j.Status),
		j.TotalExpected, j.TotalGenerated, j.TotalFailed,
		j.StartedAt, j.CompletedAt, errJSON,
	)
	if err != nil {
		return mapDBError(err, "bulk_job", "update bulk job")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("bulk_job.not_found", "bulk job not found")
	}
	return nil
}

func (r *BulkJobRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.BulkGenerationJob, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+bulkJobCols+` FROM invoicesvc.bulk_generation_jobs WHERE id = $1`, id)
	j, err := scanBulkJob(row)
	if stderrors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &j, nil
}

func (r *BulkJobRepository) List(ctx context.Context, f port.BulkJobFilter) ([]domain.BulkGenerationJob, int, error) {
	var wh []string
	var args []any
	if f.Kind != "" {
		args = append(args, f.Kind)
		wh = append(wh, fmt.Sprintf("kind = $%d", len(args)))
	}
	if f.Status != "" {
		args = append(args, f.Status)
		wh = append(wh, fmt.Sprintf("status = $%d", len(args)))
	}
	where := ""
	if len(wh) > 0 {
		where = " WHERE " + strings.Join(wh, " AND ")
	}
	var total int
	if err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM invoicesvc.bulk_generation_jobs`+where, args...,
	).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "bulk_job.count", "count bulk jobs", err)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}
	args = append(args, limit, offset)
	sql := `SELECT ` + bulkJobCols + ` FROM invoicesvc.bulk_generation_jobs` + where +
		` ORDER BY created_at DESC LIMIT $` + fmt.Sprint(len(args)-1) +
		` OFFSET $` + fmt.Sprint(len(args))
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "bulk_job.list", "list bulk jobs", err)
	}
	defer rows.Close()
	out := []domain.BulkGenerationJob{}
	for rows.Next() {
		j, err := scanBulkJob(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, j)
	}
	return out, total, nil
}

func (r *BulkJobRepository) ListPending(ctx context.Context, limit int) ([]domain.BulkGenerationJob, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+bulkJobCols+`
		FROM invoicesvc.bulk_generation_jobs
		WHERE status = 'pending'
		ORDER BY created_at ASC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "bulk_job.pending", "list pending bulk jobs", err)
	}
	defer rows.Close()
	out := []domain.BulkGenerationJob{}
	for rows.Next() {
		j, err := scanBulkJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, nil
}

func scanBulkJob(row pgx.Row) (domain.BulkGenerationJob, error) {
	var (
		j           domain.BulkGenerationJob
		kindStr     string
		statusStr   string
		targetRaw   string
		errRaw      string
	)
	err := row.Scan(
		&j.ID, &kindStr, &targetRaw, &statusStr,
		&j.TotalExpected, &j.TotalGenerated, &j.TotalFailed,
		&j.StartedAt, &j.CompletedAt, &errRaw,
		&j.CreatedBy, &j.CreatedAt, &j.UpdatedAt,
	)
	if stderrors.Is(err, pgx.ErrNoRows) {
		return domain.BulkGenerationJob{}, derrors.NotFound("bulk_job.not_found", "bulk job not found")
	}
	if err != nil {
		return domain.BulkGenerationJob{}, derrors.Wrap(derrors.KindInternal, "bulk_job.scan", "scan bulk job", err)
	}
	j.Kind = domain.BulkJobKind(kindStr)
	j.Status = domain.JobStatus(statusStr)
	if targetRaw != "" {
		_ = json.Unmarshal([]byte(targetRaw), &j.TargetFilter)
	}
	if errRaw != "" {
		_ = json.Unmarshal([]byte(errRaw), &j.ErrorSummary)
	}
	return j, nil
}

func marshalAnyMap(m map[string]any) (any, error) {
	if m == nil {
		return nil, nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

// =====================================================================
// Bulk item repo
// =====================================================================

type BulkItemRepository struct {
	pool *pgxpool.Pool
}

func NewBulkItemRepository(pool *pgxpool.Pool) *BulkItemRepository {
	return &BulkItemRepository{pool: pool}
}

var _ port.BulkItemRepository = (*BulkItemRepository)(nil)

const bulkItemCols = `
	id, job_id, customer_id, invoice_id, status,
	COALESCE(error_msg, ''), generated_at
`

func (r *BulkItemRepository) CreateBatch(ctx context.Context, items []domain.BulkGenerationItem) error {
	if len(items) == 0 {
		return nil
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "bulk_item.tx", "begin tx", err)
	}
	defer tx.Rollback(ctx)
	for i := range items {
		it := items[i]
		if it.Status == "" {
			it.Status = domain.ItemStatusQueued
		}
		if it.Status != domain.ItemStatusQueued {
			return derrors.Validation("bulk_item.bad_initial_state",
				"new items must arrive in 'queued' status")
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO invoicesvc.bulk_generation_items
				(id, job_id, customer_id, invoice_id, status, error_msg, generated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
		`,
			it.ID, it.JobID, it.CustomerID, it.InvoiceID, string(it.Status),
			nullableString(it.ErrorMsg), it.GeneratedAt,
		); err != nil {
			return mapDBError(err, "bulk_item", "insert bulk item")
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return derrors.Wrap(derrors.KindInternal, "bulk_item.commit", "commit batch", err)
	}
	return nil
}

func (r *BulkItemRepository) Update(ctx context.Context, item *domain.BulkGenerationItem) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE invoicesvc.bulk_generation_items
		SET status       = $2,
		    invoice_id   = $3,
		    error_msg    = $4,
		    generated_at = $5
		WHERE id = $1
	`, item.ID, string(item.Status), item.InvoiceID, nullableString(item.ErrorMsg), item.GeneratedAt)
	if err != nil {
		return mapDBError(err, "bulk_item", "update bulk item")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("bulk_item.not_found", "bulk item not found")
	}
	return nil
}

func (r *BulkItemRepository) ListByJob(ctx context.Context, jobID uuid.UUID) ([]domain.BulkGenerationItem, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+bulkItemCols+` FROM invoicesvc.bulk_generation_items WHERE job_id = $1 ORDER BY id`,
		jobID)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "bulk_item.list", "list bulk items", err)
	}
	defer rows.Close()
	out := []domain.BulkGenerationItem{}
	for rows.Next() {
		it, err := scanBulkItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, nil
}

func (r *BulkItemRepository) ListQueuedForJob(ctx context.Context, jobID uuid.UUID, limit int) ([]domain.BulkGenerationItem, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+bulkItemCols+` FROM invoicesvc.bulk_generation_items
		WHERE job_id = $1 AND status = 'queued'
		ORDER BY id
		LIMIT $2
	`, jobID, limit)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "bulk_item.queued", "list queued items", err)
	}
	defer rows.Close()
	out := []domain.BulkGenerationItem{}
	for rows.Next() {
		it, err := scanBulkItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, nil
}

func scanBulkItem(row pgx.Row) (domain.BulkGenerationItem, error) {
	var (
		it        domain.BulkGenerationItem
		statusStr string
	)
	err := row.Scan(
		&it.ID, &it.JobID, &it.CustomerID, &it.InvoiceID, &statusStr,
		&it.ErrorMsg, &it.GeneratedAt,
	)
	if stderrors.Is(err, pgx.ErrNoRows) {
		return domain.BulkGenerationItem{}, derrors.NotFound("bulk_item.not_found", "bulk item not found")
	}
	if err != nil {
		return domain.BulkGenerationItem{}, derrors.Wrap(derrors.KindInternal, "bulk_item.scan", "scan bulk item", err)
	}
	it.Status = domain.ItemStatus(statusStr)
	return it, nil
}
