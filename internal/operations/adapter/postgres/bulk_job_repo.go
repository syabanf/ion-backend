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

	"github.com/ion-core/backend/internal/operations/domain"
	"github.com/ion-core/backend/internal/operations/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// BulkJobRepository persists operations.bulk_jobs rows.
type BulkJobRepository struct {
	pool *pgxpool.Pool
}

func NewBulkJobRepository(pool *pgxpool.Pool) *BulkJobRepository {
	return &BulkJobRepository{pool: pool}
}

var _ port.BulkJobRepository = (*BulkJobRepository)(nil)

const bulkJobCols = `
	id, kind, status,
	COALESCE(total_items, 0), COALESCE(processed_items, 0),
	COALESCE(succeeded_items, 0), COALESCE(failed_items, 0),
	COALESCE(skipped_items, 0),
	started_at, completed_at, COALESCE(error_summary::text, '{}'),
	dry_run, created_by, created_at, updated_at
`

func (r *BulkJobRepository) Create(ctx context.Context, j *domain.BulkJob) error {
	if j == nil {
		return derrors.Validation("bulk_job.nil", "job is nil")
	}
	summaryJSON, err := marshalAnyMap(j.ErrorSummary)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "bulk_job.marshal", "marshal error_summary", err)
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO operations.bulk_jobs
			(id, kind, status,
			 total_items, processed_items, succeeded_items, failed_items, skipped_items,
			 started_at, completed_at, error_summary,
			 dry_run, created_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11::jsonb, $12, $13, $14, $15)
	`,
		j.ID, string(j.Kind), string(j.Status),
		j.TotalItems, j.ProcessedItems, j.SucceededItems, j.FailedItems, j.SkippedItems,
		j.StartedAt, j.CompletedAt, summaryJSON,
		j.DryRun, j.CreatedBy, j.CreatedAt, j.UpdatedAt,
	)
	return mapDBError(err, "bulk_job", "insert bulk job")
}

func (r *BulkJobRepository) Update(ctx context.Context, j *domain.BulkJob) error {
	summaryJSON, err := marshalAnyMap(j.ErrorSummary)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "bulk_job.marshal", "marshal error_summary", err)
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE operations.bulk_jobs
		   SET status          = $2,
		       total_items     = $3,
		       processed_items = $4,
		       succeeded_items = $5,
		       failed_items    = $6,
		       skipped_items   = $7,
		       started_at      = $8,
		       completed_at    = $9,
		       error_summary   = $10::jsonb,
		       dry_run         = $11,
		       updated_at      = NOW()
		 WHERE id = $1
	`,
		j.ID, string(j.Status),
		j.TotalItems, j.ProcessedItems, j.SucceededItems, j.FailedItems, j.SkippedItems,
		j.StartedAt, j.CompletedAt, summaryJSON, j.DryRun,
	)
	if err != nil {
		return mapDBError(err, "bulk_job", "update bulk job")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("bulk_job.not_found", "bulk job not found")
	}
	return nil
}

func (r *BulkJobRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.BulkJob, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT `+bulkJobCols+`
		  FROM operations.bulk_jobs
		 WHERE id = $1
	`, id)
	j, err := scanBulkJob(row)
	if stderrors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, mapDBError(err, "bulk_job", "find bulk job")
	}
	return j, nil
}

func (r *BulkJobRepository) List(ctx context.Context, f port.BulkJobFilter) ([]domain.BulkJob, int, error) {
	if f.Limit <= 0 {
		f.Limit = 50
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	conds := []string{}
	args := []any{}
	if f.Kind != "" {
		args = append(args, f.Kind)
		conds = append(conds, fmt.Sprintf("kind = $%d", len(args)))
	}
	if f.Status != "" {
		args = append(args, f.Status)
		conds = append(conds, fmt.Sprintf("status = $%d", len(args)))
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	var total int
	if err := r.pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM operations.bulk_jobs"+where, args...,
	).Scan(&total); err != nil {
		return nil, 0, mapDBError(err, "bulk_job", "count bulk jobs")
	}
	args = append(args, f.Limit, f.Offset)
	q := "SELECT " + bulkJobCols + " FROM operations.bulk_jobs" + where +
		fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d OFFSET $%d",
			len(args)-1, len(args))
	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, 0, mapDBError(err, "bulk_job", "list bulk jobs")
	}
	defer rows.Close()
	out := []domain.BulkJob{}
	for rows.Next() {
		j, err := scanBulkJob(rows)
		if err != nil {
			return nil, 0, mapDBError(err, "bulk_job", "scan bulk job")
		}
		out = append(out, *j)
	}
	return out, total, nil
}

func (r *BulkJobRepository) ListRunnable(ctx context.Context, limit int) ([]domain.BulkJob, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+bulkJobCols+`
		  FROM operations.bulk_jobs
		 WHERE status IN ('pending','running')
		 ORDER BY created_at ASC
		 LIMIT $1
	`, limit)
	if err != nil {
		return nil, mapDBError(err, "bulk_job", "list runnable bulk jobs")
	}
	defer rows.Close()
	out := []domain.BulkJob{}
	for rows.Next() {
		j, err := scanBulkJob(rows)
		if err != nil {
			return nil, mapDBError(err, "bulk_job", "scan bulk job")
		}
		out = append(out, *j)
	}
	return out, nil
}

// scanBulkJob — single-row scanner shared by Find/List/ListRunnable. The
// row parameter accepts either pgx.Row (for QueryRow) or pgx.Rows (for
// Query).
func scanBulkJob(row pgx.Row) (*domain.BulkJob, error) {
	var (
		j               domain.BulkJob
		kind, status    string
		summaryRaw      string
		createdByPtr    *uuid.UUID
	)
	if err := row.Scan(
		&j.ID, &kind, &status,
		&j.TotalItems, &j.ProcessedItems,
		&j.SucceededItems, &j.FailedItems, &j.SkippedItems,
		&j.StartedAt, &j.CompletedAt, &summaryRaw,
		&j.DryRun, &createdByPtr, &j.CreatedAt, &j.UpdatedAt,
	); err != nil {
		return nil, err
	}
	j.Kind = domain.BulkJobKind(kind)
	j.Status = domain.BulkJobStatus(status)
	j.CreatedBy = createdByPtr
	j.ErrorSummary = unmarshalAnyMap(summaryRaw)
	return &j, nil
}

func unmarshalAnyMap(s string) map[string]any {
	if s == "" || s == "{}" {
		return nil
	}
	m := map[string]any{}
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil
	}
	if len(m) == 0 {
		return nil
	}
	return m
}
