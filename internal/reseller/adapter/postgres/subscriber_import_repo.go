package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/reseller/domain"
	"github.com/ion-core/backend/internal/reseller/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// SubscriberImportRepository implements port.SubscriberImportRepository
// against `reseller.subscriber_imports`. Tenant guard: every read
// requires a non-nil reseller_account_id filter.
type SubscriberImportRepository struct {
	pool *pgxpool.Pool
}

func NewSubscriberImportRepository(pool *pgxpool.Pool) *SubscriberImportRepository {
	return &SubscriberImportRepository{pool: pool}
}

var _ port.SubscriberImportRepository = (*SubscriberImportRepository)(nil)

const importCols = `
	id, reseller_account_id, COALESCE(source, ''),
	total_rows, ok_rows, error_rows,
	COALESCE(raw_uploaded_url, ''),
	status,
	error_summary,
	created_by, created_at, completed_at
`

func (r *SubscriberImportRepository) Create(ctx context.Context, im *domain.SubscriberImport) error {
	if im.ResellerAccountID == uuid.Nil {
		return derrors.Validation("subscriber_import.reseller_required", "reseller_account_id is required")
	}
	var summaryJSON any
	if len(im.ErrorSummary) > 0 {
		b, err := json.Marshal(im.ErrorSummary)
		if err != nil {
			return derrors.Wrap(derrors.KindInternal, "db.import_summary_marshal", "marshal error summary", err)
		}
		summaryJSON = string(b)
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO reseller.subscriber_imports
			(id, reseller_account_id, source,
			 total_rows, ok_rows, error_rows,
			 raw_uploaded_url, status, error_summary,
			 created_by, created_at, completed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
	`,
		im.ID, im.ResellerAccountID, nullableString(im.Source),
		im.TotalRows, im.OKRows, im.ErrorRows,
		nullableString(im.RawUploadedURL), string(im.Status), summaryJSON,
		im.CreatedBy, im.CreatedAt, im.CompletedAt,
	)
	if err != nil {
		return mapDBError(err, "subscriber_import", "insert subscriber import")
	}
	return nil
}

func (r *SubscriberImportRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.SubscriberImport, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+importCols+` FROM reseller.subscriber_imports WHERE id = $1`, id)
	im, err := scanImport(row)
	if err != nil {
		return nil, err
	}
	return &im, nil
}

// UpdateStatus is the only mutation path — the import row is otherwise
// immutable. We update the counts + error_summary + status +
// completed_at together so the row never observes a partial finalize.
func (r *SubscriberImportRepository) UpdateStatus(ctx context.Context, im *domain.SubscriberImport) error {
	var summaryJSON any
	if len(im.ErrorSummary) > 0 {
		b, err := json.Marshal(im.ErrorSummary)
		if err != nil {
			return derrors.Wrap(derrors.KindInternal, "db.import_summary_marshal", "marshal error summary", err)
		}
		summaryJSON = string(b)
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE reseller.subscriber_imports
		SET total_rows = $2,
		    ok_rows = $3,
		    error_rows = $4,
		    status = $5,
		    error_summary = $6,
		    completed_at = $7
		WHERE id = $1 AND reseller_account_id = $8
	`,
		im.ID, im.TotalRows, im.OKRows, im.ErrorRows,
		string(im.Status), summaryJSON, im.CompletedAt, im.ResellerAccountID,
	)
	if err != nil {
		return mapDBError(err, "subscriber_import", "update subscriber import")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("subscriber_import.not_found", "subscriber import not found")
	}
	return nil
}

func (r *SubscriberImportRepository) List(ctx context.Context, f port.SubscriberImportListFilter) ([]domain.SubscriberImport, int, error) {
	if f.ResellerAccountID == uuid.Nil {
		return nil, 0, derrors.Validation("subscriber_import.tenant_filter_required", "reseller_account_id filter is required")
	}
	args := []any{f.ResellerAccountID}
	wh := []string{"reseller_account_id = $1"}
	if f.Status != "" {
		args = append(args, f.Status)
		wh = append(wh, fmt.Sprintf("status = $%d", len(args)))
	}
	where := " WHERE " + strings.Join(wh, " AND ")

	var total int
	if err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM reseller.subscriber_imports`+where, args...,
	).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.import_count", "count imports", err)
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
	sql := `SELECT ` + importCols + ` FROM reseller.subscriber_imports` + where +
		` ORDER BY created_at DESC LIMIT $` + fmt.Sprint(len(args)-1) +
		` OFFSET $` + fmt.Sprint(len(args))

	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.import_list", "list imports", err)
	}
	defer rows.Close()
	out := []domain.SubscriberImport{}
	for rows.Next() {
		im, err := scanImport(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, im)
	}
	return out, total, nil
}

func scanImport(row pgx.Row) (domain.SubscriberImport, error) {
	var im domain.SubscriberImport
	var status string
	var summaryRaw []byte
	err := row.Scan(
		&im.ID, &im.ResellerAccountID, &im.Source,
		&im.TotalRows, &im.OKRows, &im.ErrorRows,
		&im.RawUploadedURL,
		&status, &summaryRaw,
		&im.CreatedBy, &im.CreatedAt, &im.CompletedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.SubscriberImport{}, derrors.NotFound("subscriber_import.not_found", "subscriber import not found")
	}
	if err != nil {
		return domain.SubscriberImport{}, derrors.Wrap(derrors.KindInternal, "db.import_scan", "scan subscriber import", err)
	}
	im.Status = domain.ImportStatus(status)
	if len(summaryRaw) > 0 {
		_ = json.Unmarshal(summaryRaw, &im.ErrorSummary)
	}
	return im, nil
}
