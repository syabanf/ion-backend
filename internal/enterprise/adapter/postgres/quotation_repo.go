package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/enterprise/domain"
	"github.com/ion-core/backend/internal/enterprise/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type QuotationRepository struct {
	pool *pgxpool.Pool
}

func NewQuotationRepository(pool *pgxpool.Pool) *QuotationRepository {
	return &QuotationRepository{pool: pool}
}

var _ port.QuotationRepository = (*QuotationRepository)(nil)

// Two column sets — `quotationListCols` excludes pdf_bytes so listing
// queries don't pay the BYTEA transfer cost; `quotationFullCols`
// includes everything for FindByID. (We never need pdf_bytes in a
// LIST response; the FE only displays them when opening a single
// quotation.)
const quotationListCols = `
	id, quotation_number, version_no, boq_version_id, opportunity_id, status,
	sell_total, cost_total, margin_pct, currency,
	'' as pdf_bytes_placeholder,
	pdf_hash, pdf_bytes_size,
	valid_from, valid_until, issued_at,
	accepted_at, rejected_at, cancelled_at, superseded_at,
	COALESCE(notes,''), revision, issued_by, created_at, updated_at,
	tax_snapshot_hash
`

const quotationFullCols = `
	id, quotation_number, version_no, boq_version_id, opportunity_id, status,
	sell_total, cost_total, margin_pct, currency,
	pdf_bytes, pdf_hash, pdf_bytes_size,
	valid_from, valid_until, issued_at,
	accepted_at, rejected_at, cancelled_at, superseded_at,
	COALESCE(notes,''), revision, issued_by, created_at, updated_at,
	tax_snapshot_hash
`

func (r *QuotationRepository) List(ctx context.Context, f port.QuotationListFilter) ([]domain.Quotation, int, error) {
	var wh []string
	var args []any
	if f.BOQVersionID != nil {
		args = append(args, *f.BOQVersionID)
		wh = append(wh, fmt.Sprintf("boq_version_id = $%d", len(args)))
	}
	if f.OpportunityID != nil {
		args = append(args, *f.OpportunityID)
		wh = append(wh, fmt.Sprintf("opportunity_id = $%d", len(args)))
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
	if err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM enterprise.quotations`+where, args...).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.quotation_count", "count quotations", err)
	}

	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	args = append(args, limit, f.Offset)
	sql := `SELECT ` + quotationListCols + ` FROM enterprise.quotations` + where +
		` ORDER BY issued_at DESC LIMIT $` + fmt.Sprint(len(args)-1) +
		` OFFSET $` + fmt.Sprint(len(args))

	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.quotation_list", "list quotations", err)
	}
	defer rows.Close()
	out := []domain.Quotation{}
	for rows.Next() {
		q, err := scanQuotationListRow(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, q)
	}
	return out, total, nil
}

func (r *QuotationRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.Quotation, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+quotationFullCols+` FROM enterprise.quotations WHERE id = $1`, id)
	q, err := scanQuotation(row)
	if err != nil {
		return nil, err
	}
	return &q, nil
}

// FindPDFBytes is the fast path for the /pdf streaming endpoint —
// avoids hydrating the rest of the row.
func (r *QuotationRepository) FindPDFBytes(ctx context.Context, id uuid.UUID) ([]byte, string, error) {
	var (
		bytes []byte
		hash  string
	)
	err := r.pool.QueryRow(ctx,
		`SELECT pdf_bytes, pdf_hash FROM enterprise.quotations WHERE id = $1`, id,
	).Scan(&bytes, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", derrors.NotFound("quotation.not_found", "quotation not found")
	}
	if err != nil {
		return nil, "", derrors.Wrap(derrors.KindInternal, "db.quotation_pdf_read", "read pdf bytes", err)
	}
	return bytes, hash, nil
}

func (r *QuotationRepository) FindHighestVersion(ctx context.Context, quotationNumber string) (*domain.Quotation, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT `+quotationListCols+`
		FROM enterprise.quotations
		WHERE quotation_number = $1
		ORDER BY version_no DESC
		LIMIT 1
	`, quotationNumber)
	q, err := scanQuotationListRow(row)
	if err != nil {
		return nil, err
	}
	return &q, nil
}

func (r *QuotationRepository) FindLatestForBOQ(ctx context.Context, boqVersionID uuid.UUID) (*domain.Quotation, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT `+quotationListCols+`
		FROM enterprise.quotations
		WHERE boq_version_id = $1
		ORDER BY version_no DESC
		LIMIT 1
	`, boqVersionID)
	q, err := scanQuotationListRow(row)
	if err != nil {
		// NotFound is a valid "no prior quote" signal — let the
		// caller handle it as a non-error.
		return nil, err
	}
	return &q, nil
}

func (r *QuotationRepository) Create(ctx context.Context, q *domain.Quotation) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO enterprise.quotations
			(id, quotation_number, version_no, boq_version_id, opportunity_id, status,
			 sell_total, cost_total, margin_pct, currency,
			 pdf_bytes, pdf_hash, pdf_bytes_size,
			 valid_from, valid_until, issued_at,
			 accepted_at, rejected_at, cancelled_at, superseded_at,
			 notes, revision, issued_by, created_at, updated_at,
			 tax_snapshot_hash)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13,
		        $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25,
		        $26)
	`,
		q.ID, q.QuotationNumber, q.VersionNo, q.BOQVersionID, q.OpportunityID, string(q.Status),
		q.SellTotal, q.CostTotal, q.MarginPct, q.Currency,
		q.PDFBytes, q.PDFHash, q.PDFBytesSize,
		q.ValidFrom, q.ValidUntil, q.IssuedAt,
		q.AcceptedAt, q.RejectedAt, q.CancelledAt, q.SupersededAt,
		q.Notes, q.Revision, q.IssuedBy, q.CreatedAt, q.UpdatedAt,
		q.TaxSnapshotHash,
	)
	if err != nil {
		return mapDBError(err, "quotation", "insert quotation")
	}
	return nil
}

func (r *QuotationRepository) Update(ctx context.Context, q *domain.Quotation, ifRevision *int) error {
	// Status + lifecycle timestamps + notes are the only mutable
	// columns post-create. PDF bytes/hash are immutable — re-renders
	// produce a new row.
	var (
		tag pgconnTag
		err error
	)
	if ifRevision != nil {
		tag, err = r.pool.Exec(ctx, `
			UPDATE enterprise.quotations
			SET status = $2, valid_until = $3,
			    accepted_at = $4, rejected_at = $5, cancelled_at = $6, superseded_at = $7,
			    notes = $8, revision = $9, updated_at = NOW()
			WHERE id = $1 AND revision = $10
		`,
			q.ID, string(q.Status), q.ValidUntil,
			q.AcceptedAt, q.RejectedAt, q.CancelledAt, q.SupersededAt,
			q.Notes, q.Revision, *ifRevision,
		)
	} else {
		tag, err = r.pool.Exec(ctx, `
			UPDATE enterprise.quotations
			SET status = $2, valid_until = $3,
			    accepted_at = $4, rejected_at = $5, cancelled_at = $6, superseded_at = $7,
			    notes = $8, revision = $9, updated_at = NOW()
			WHERE id = $1
		`,
			q.ID, string(q.Status), q.ValidUntil,
			q.AcceptedAt, q.RejectedAt, q.CancelledAt, q.SupersededAt,
			q.Notes, q.Revision,
		)
	}
	if err != nil {
		return mapDBError(err, "quotation", "update quotation")
	}
	if tag.RowsAffected() == 0 {
		if ifRevision != nil {
			if _, e2 := r.FindByID(ctx, q.ID); e2 != nil {
				return e2
			}
			return derrors.Conflict("quotation.stale_version", "quotation has been modified since you loaded it")
		}
		return derrors.NotFound("quotation.not_found", "quotation not found")
	}
	return nil
}

// scanQuotation reads the full column set (including PDF bytes).
func scanQuotation(row pgx.Row) (domain.Quotation, error) {
	var (
		q      domain.Quotation
		status string
	)
	err := row.Scan(
		&q.ID, &q.QuotationNumber, &q.VersionNo, &q.BOQVersionID, &q.OpportunityID, &status,
		&q.SellTotal, &q.CostTotal, &q.MarginPct, &q.Currency,
		&q.PDFBytes, &q.PDFHash, &q.PDFBytesSize,
		&q.ValidFrom, &q.ValidUntil, &q.IssuedAt,
		&q.AcceptedAt, &q.RejectedAt, &q.CancelledAt, &q.SupersededAt,
		&q.Notes, &q.Revision, &q.IssuedBy, &q.CreatedAt, &q.UpdatedAt,
		&q.TaxSnapshotHash,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Quotation{}, derrors.NotFound("quotation.not_found", "quotation not found")
	}
	if err != nil {
		return domain.Quotation{}, derrors.Wrap(derrors.KindInternal, "db.quotation_scan", "scan quotation", err)
	}
	q.Status = domain.QuotationStatus(status)
	return q, nil
}

// scanQuotationListRow reads everything EXCEPT the PDF bytes. The
// list query selects a literal '' placeholder in the bytes column
// position, so the column order matches scanQuotation but the byte
// slice ends up empty.
func scanQuotationListRow(row pgx.Row) (domain.Quotation, error) {
	var (
		q       domain.Quotation
		status  string
		dummy   string // placeholder for the empty-string pdf_bytes column
	)
	err := row.Scan(
		&q.ID, &q.QuotationNumber, &q.VersionNo, &q.BOQVersionID, &q.OpportunityID, &status,
		&q.SellTotal, &q.CostTotal, &q.MarginPct, &q.Currency,
		&dummy, &q.PDFHash, &q.PDFBytesSize,
		&q.ValidFrom, &q.ValidUntil, &q.IssuedAt,
		&q.AcceptedAt, &q.RejectedAt, &q.CancelledAt, &q.SupersededAt,
		&q.Notes, &q.Revision, &q.IssuedBy, &q.CreatedAt, &q.UpdatedAt,
		&q.TaxSnapshotHash,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Quotation{}, derrors.NotFound("quotation.not_found", "quotation not found")
	}
	if err != nil {
		return domain.Quotation{}, derrors.Wrap(derrors.KindInternal, "db.quotation_scan_list", "scan quotation list row", err)
	}
	q.Status = domain.QuotationStatus(status)
	q.PDFBytes = nil
	return q, nil
}
