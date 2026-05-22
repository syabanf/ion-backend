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

// PricebookRepository implements `port.PricebookRepository` against
// `enterprise.pricebooks`.
type PricebookRepository struct {
	pool *pgxpool.Pool
}

func NewPricebookRepository(pool *pgxpool.Pool) *PricebookRepository {
	return &PricebookRepository{pool: pool}
}

var _ port.PricebookRepository = (*PricebookRepository)(nil)

const pricebookCols = `
	id, code, name, currency,
	effective_from, effective_to,
	COALESCE(holding_company_id,''),
	version_no, status,
	published_at, superseded_at,
	COALESCE(notes,''),
	created_by, created_at, updated_at
`

func (r *PricebookRepository) List(ctx context.Context, f port.PricebookListFilter) ([]domain.Pricebook, int, error) {
	var wh []string
	var args []any
	if f.Status != "" {
		args = append(args, f.Status)
		wh = append(wh, fmt.Sprintf("status = $%d", len(args)))
	}
	if f.HoldingCompanyID != "" {
		args = append(args, f.HoldingCompanyID)
		wh = append(wh, fmt.Sprintf("holding_company_id = $%d", len(args)))
	}
	if f.Code != "" {
		args = append(args, f.Code)
		wh = append(wh, fmt.Sprintf("code = $%d", len(args)))
	}
	where := ""
	if len(wh) > 0 {
		where = " WHERE " + strings.Join(wh, " AND ")
	}

	var total int
	if err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM enterprise.pricebooks`+where, args...,
	).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.pricebook_count", "count pricebooks", err)
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
	sql := `SELECT ` + pricebookCols + ` FROM enterprise.pricebooks` + where +
		` ORDER BY code, version_no DESC LIMIT $` + fmt.Sprint(len(args)-1) +
		` OFFSET $` + fmt.Sprint(len(args))

	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.pricebook_list", "list pricebooks", err)
	}
	defer rows.Close()

	out := []domain.Pricebook{}
	for rows.Next() {
		p, err := scanPricebook(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, p)
	}
	return out, total, nil
}

func (r *PricebookRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.Pricebook, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+pricebookCols+` FROM enterprise.pricebooks WHERE id = $1`, id)
	p, err := scanPricebook(row)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// FindOverlapping returns existing pricebooks sharing the same `code`
// whose effective range overlaps the candidate. The check considers
// nil `effective_to` as +∞ on both sides — matches domain.Overlaps.
//
// We restrict to `status != 'superseded'` because superseded windows
// are historical record only; overlap with them is harmless.
func (r *PricebookRepository) FindOverlapping(ctx context.Context, candidate *domain.Pricebook) ([]domain.Pricebook, error) {
	// SQL overlap logic for two ranges [a1,a2] vs [b1,b2]:
	//   not (a2 < b1 or b2 < a1)
	// With NULLs representing +∞ we use COALESCE to 'infinity'.
	const sql = `
		SELECT ` + pricebookCols + `
		FROM enterprise.pricebooks
		WHERE code = $1
		  AND id <> $2
		  AND status <> 'superseded'
		  AND NOT (
		      COALESCE(effective_to, DATE 'infinity') < $3
		      OR $4 < effective_from
		  )
	`
	// $3 = candidate.EffectiveFrom (a1) → check `b2 < a1`
	// $4 = candidate.EffectiveTo (a2)   → check `a2 < b1`
	// When candidate.EffectiveTo is nil, treat as 'infinity' so the
	// `$4 < effective_from` half can never fire.
	endArg := any(nil)
	if candidate.EffectiveTo != nil {
		endArg = *candidate.EffectiveTo
	} else {
		// Use the Postgres date infinity sentinel.
		endArg = "infinity"
	}
	rows, err := r.pool.Query(ctx, sql,
		candidate.Code, candidate.ID, candidate.EffectiveFrom, endArg,
	)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.pricebook_overlap_query", "query overlapping pricebooks", err)
	}
	defer rows.Close()

	out := []domain.Pricebook{}
	for rows.Next() {
		p, err := scanPricebook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

func (r *PricebookRepository) Create(ctx context.Context, p *domain.Pricebook) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO enterprise.pricebooks
			(id, code, name, currency, effective_from, effective_to,
			 holding_company_id, version_no, status, published_at, superseded_at,
			 notes, created_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
	`,
		p.ID, p.Code, p.Name, p.Currency, p.EffectiveFrom, p.EffectiveTo,
		p.HoldingCompanyID, p.VersionNo, string(p.Status),
		p.PublishedAt, p.SupersededAt, p.Notes,
		p.CreatedBy, p.CreatedAt, p.UpdatedAt,
	)
	if err != nil {
		return mapDBError(err, "pricebook", "insert pricebook")
	}
	return nil
}

func (r *PricebookRepository) Update(ctx context.Context, p *domain.Pricebook) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE enterprise.pricebooks
		SET name = $2,
		    effective_from = $3,
		    effective_to = $4,
		    holding_company_id = $5,
		    notes = $6,
		    status = $7,
		    published_at = $8,
		    superseded_at = $9,
		    updated_at = NOW()
		WHERE id = $1
	`,
		p.ID, p.Name, p.EffectiveFrom, p.EffectiveTo,
		p.HoldingCompanyID, p.Notes,
		string(p.Status), p.PublishedAt, p.SupersededAt,
	)
	if err != nil {
		return mapDBError(err, "pricebook", "update pricebook")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("pricebook.not_found", "pricebook not found")
	}
	return nil
}

func scanPricebook(row pgx.Row) (domain.Pricebook, error) {
	var p domain.Pricebook
	var status string
	err := row.Scan(
		&p.ID, &p.Code, &p.Name, &p.Currency,
		&p.EffectiveFrom, &p.EffectiveTo,
		&p.HoldingCompanyID,
		&p.VersionNo, &status,
		&p.PublishedAt, &p.SupersededAt,
		&p.Notes,
		&p.CreatedBy, &p.CreatedAt, &p.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Pricebook{}, derrors.NotFound("pricebook.not_found", "pricebook not found")
	}
	if err != nil {
		return domain.Pricebook{}, derrors.Wrap(derrors.KindInternal, "db.pricebook_scan", "scan pricebook", err)
	}
	p.Status = domain.PricebookStatus(status)
	return p, nil
}
