package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/vendormgmt/domain"
	"github.com/ion-core/backend/internal/vendormgmt/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// SubmissionRepository implements port.SubmissionRepository.
type SubmissionRepository struct {
	pool *pgxpool.Pool
}

func NewSubmissionRepository(pool *pgxpool.Pool) *SubmissionRepository {
	return &SubmissionRepository{pool: pool}
}

var _ port.SubmissionRepository = (*SubmissionRepository)(nil)

const submissionCols = `
	id, opportunity_id, provider_id,
	boq_line_id, unit_cost, COALESCE(notes, ''),
	status, submitted_by, submitted_at,
	reviewed_by, reviewed_at, COALESCE(rejection_reason, '')
`

func (r *SubmissionRepository) Create(ctx context.Context, s *domain.InputSubmission) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO vendor.provider_input_submissions
			(id, opportunity_id, provider_id, boq_line_id, unit_cost,
			 notes, status, submitted_by, submitted_at,
			 reviewed_by, reviewed_at, rejection_reason)
		VALUES ($1, $2, $3, $4, $5,
		        NULLIF($6,''), $7, $8, $9,
		        $10, $11, NULLIF($12,''))
	`,
		s.ID, s.OpportunityID, s.ProviderID, s.BOQLineID, s.UnitCost,
		s.Notes, string(s.Status), s.SubmittedBy, s.SubmittedAt,
		s.ReviewedBy, s.ReviewedAt, s.RejectionReason,
	)
	if err != nil {
		return mapDBError(err, "submission", "insert submission")
	}
	return nil
}

func (r *SubmissionRepository) Update(ctx context.Context, s *domain.InputSubmission) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE vendor.provider_input_submissions
		SET status = $2,
		    unit_cost = $3,
		    notes = NULLIF($4,''),
		    reviewed_by = $5,
		    reviewed_at = $6,
		    rejection_reason = NULLIF($7,'')
		WHERE id = $1
	`,
		s.ID, string(s.Status), s.UnitCost, s.Notes,
		s.ReviewedBy, s.ReviewedAt, s.RejectionReason,
	)
	if err != nil {
		return mapDBError(err, "submission", "update submission")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("submission.not_found", "submission not found")
	}
	return nil
}

func (r *SubmissionRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.InputSubmission, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+submissionCols+` FROM vendor.provider_input_submissions WHERE id = $1`, id,
	)
	s, err := scanSubmission(row)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *SubmissionRepository) List(ctx context.Context, f port.SubmissionListFilter) ([]domain.InputSubmission, int, error) {
	var wh []string
	var args []any
	if f.OpportunityID != nil {
		args = append(args, *f.OpportunityID)
		wh = append(wh, fmt.Sprintf("opportunity_id = $%d", len(args)))
	}
	if f.ProviderID != nil {
		args = append(args, *f.ProviderID)
		wh = append(wh, fmt.Sprintf("provider_id = $%d", len(args)))
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
		`SELECT COUNT(*) FROM vendor.provider_input_submissions`+where, args...,
	).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.submission_count", "count submissions", err)
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
	sql := `SELECT ` + submissionCols +
		` FROM vendor.provider_input_submissions` + where +
		` ORDER BY submitted_at DESC` +
		fmt.Sprintf(" LIMIT $%d OFFSET $%d", len(args)-1, len(args))

	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.submission_list", "list submissions", err)
	}
	defer rows.Close()
	out := []domain.InputSubmission{}
	for rows.Next() {
		s, err := scanSubmission(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, s)
	}
	return out, total, nil
}

func scanSubmission(row pgx.Row) (domain.InputSubmission, error) {
	var s domain.InputSubmission
	var status string
	err := row.Scan(
		&s.ID, &s.OpportunityID, &s.ProviderID,
		&s.BOQLineID, &s.UnitCost, &s.Notes,
		&status, &s.SubmittedBy, &s.SubmittedAt,
		&s.ReviewedBy, &s.ReviewedAt, &s.RejectionReason,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.InputSubmission{}, derrors.NotFound("submission.not_found", "submission not found")
	}
	if err != nil {
		return domain.InputSubmission{}, derrors.Wrap(derrors.KindInternal, "db.submission_scan", "scan submission", err)
	}
	s.Status = domain.SubmissionStatus(status)
	return s, nil
}
