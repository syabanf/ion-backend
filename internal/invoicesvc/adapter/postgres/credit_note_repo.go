package postgres

import (
	"context"
	stderrors "errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/invoicesvc/domain"
	"github.com/ion-core/backend/internal/invoicesvc/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// CreditNoteRepository implements port.CreditNoteRepository.
type CreditNoteRepository struct {
	pool *pgxpool.Pool
}

func NewCreditNoteRepository(pool *pgxpool.Pool) *CreditNoteRepository {
	return &CreditNoteRepository{pool: pool}
}

var _ port.CreditNoteRepository = (*CreditNoteRepository)(nil)

const cnCols = `
	id, invoice_id, customer_id, COALESCE(credit_no, ''),
	amount, COALESCE(reason, ''), status,
	issued_at, applied_at, voided_at,
	created_by, approved_by,
	created_at, updated_at
`

func (r *CreditNoteRepository) Create(ctx context.Context, cn *domain.CreditNote) error {
	if cn == nil {
		return derrors.Validation("credit_note.nil", "credit note is nil")
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO invoicesvc.credit_notes
			(id, invoice_id, customer_id, credit_no, amount, reason, status,
			 issued_at, applied_at, voided_at, created_by, approved_by,
			 created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
	`,
		cn.ID, cn.InvoiceID, cn.CustomerID, nullableString(cn.CreditNo),
		cn.Amount, nullableString(cn.Reason), string(cn.Status),
		cn.IssuedAt, cn.AppliedAt, cn.VoidedAt,
		cn.CreatedBy, cn.ApprovedBy,
		cn.CreatedAt, cn.UpdatedAt,
	)
	if err != nil {
		return mapDBError(err, "credit_note", "insert credit note")
	}
	return nil
}

func (r *CreditNoteRepository) Update(ctx context.Context, cn *domain.CreditNote) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE invoicesvc.credit_notes
		SET credit_no   = COALESCE(NULLIF($2,''), credit_no),
		    amount      = $3,
		    reason      = $4,
		    status      = $5,
		    issued_at   = $6,
		    applied_at  = $7,
		    voided_at   = $8,
		    approved_by = $9,
		    updated_at  = NOW()
		WHERE id = $1
	`,
		cn.ID, cn.CreditNo, cn.Amount, nullableString(cn.Reason), string(cn.Status),
		cn.IssuedAt, cn.AppliedAt, cn.VoidedAt, cn.ApprovedBy,
	)
	if err != nil {
		return mapDBError(err, "credit_note", "update credit note")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("credit_note.not_found", "credit note not found")
	}
	return nil
}

func (r *CreditNoteRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.CreditNote, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+cnCols+` FROM invoicesvc.credit_notes WHERE id = $1`, id)
	cn, err := scanCreditNote(row)
	if stderrors.Is(err, pgx.ErrNoRows) {
		return nil, derrors.NotFound("credit_note.not_found", "credit note not found")
	}
	if err != nil {
		return nil, err
	}
	return &cn, nil
}

func (r *CreditNoteRepository) List(ctx context.Context, f port.CreditNoteFilter) ([]domain.CreditNote, int, error) {
	var (
		wh   []string
		args []any
	)
	if f.InvoiceID != nil {
		args = append(args, *f.InvoiceID)
		wh = append(wh, fmt.Sprintf("invoice_id = $%d", len(args)))
	}
	if f.CustomerID != nil {
		args = append(args, *f.CustomerID)
		wh = append(wh, fmt.Sprintf("customer_id = $%d", len(args)))
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
		`SELECT COUNT(*) FROM invoicesvc.credit_notes`+where, args...,
	).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "credit_note.count", "count credit notes", err)
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
	sql := `SELECT ` + cnCols + ` FROM invoicesvc.credit_notes` + where +
		` ORDER BY created_at DESC LIMIT $` + fmt.Sprint(len(args)-1) +
		` OFFSET $` + fmt.Sprint(len(args))
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "credit_note.list", "list credit notes", err)
	}
	defer rows.Close()
	out := []domain.CreditNote{}
	for rows.Next() {
		cn, err := scanCreditNote(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, cn)
	}
	return out, total, nil
}

// NextCreditNumber issues a fresh CN id using a date-prefixed format and
// a per-day count. Format: CN-YYYYMMDD-NNNN. NOT cryptographically
// monotonic across days (resets nightly), but unique-per-day + globally
// unique via the UNIQUE constraint on credit_no.
func (r *CreditNoteRepository) NextCreditNumber(ctx context.Context) (string, error) {
	day := time.Now().UTC().Format("20060102")
	prefix := "CN-" + day + "-"
	// Use COUNT + retry on conflict. For Wave 115 scale (low-volume CN
	// issuance), this is sufficient — a sequence is overkill.
	var n int
	err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM invoicesvc.credit_notes WHERE credit_no LIKE $1`,
		prefix+"%",
	).Scan(&n)
	if err != nil {
		return "", derrors.Wrap(derrors.KindInternal, "credit_note.next_no", "compute next credit number", err)
	}
	return fmt.Sprintf("%s%04d", prefix, n+1), nil
}

func scanCreditNote(row pgx.Row) (domain.CreditNote, error) {
	var (
		cn        domain.CreditNote
		statusStr string
	)
	err := row.Scan(
		&cn.ID, &cn.InvoiceID, &cn.CustomerID, &cn.CreditNo,
		&cn.Amount, &cn.Reason, &statusStr,
		&cn.IssuedAt, &cn.AppliedAt, &cn.VoidedAt,
		&cn.CreatedBy, &cn.ApprovedBy,
		&cn.CreatedAt, &cn.UpdatedAt,
	)
	if stderrors.Is(err, pgx.ErrNoRows) {
		return domain.CreditNote{}, derrors.NotFound("credit_note.not_found", "credit note not found")
	}
	if err != nil {
		return domain.CreditNote{}, derrors.Wrap(derrors.KindInternal, "credit_note.scan", "scan credit note", err)
	}
	cn.Status = domain.CreditNoteStatus(statusStr)
	return cn, nil
}
