package postgres

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/enterprise/domain"
	"github.com/ion-core/backend/internal/enterprise/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// Wave 103 — EWO checklist progress repository
//
// Idempotency story (key insight, shared with caller via the port doc):
//
//   - INSERT ... ON CONFLICT (ewo_id, idempotency_key) DO NOTHING
//   - If RowsAffected == 0, the conflict fired — re-fetch the row by
//     (ewo_id, idempotency_key) and update *p in place so the caller
//     sees the canonical persisted state, not their replay attempt.
//   - When idempotency_key IS NULL the partial unique index doesn't
//     fire, so it falls through to a plain INSERT (Postgres treats
//     NULL != NULL in unique constraints).
// =====================================================================

type EWOChecklistProgressRepository struct {
	pool *pgxpool.Pool
}

func NewEWOChecklistProgressRepository(pool *pgxpool.Pool) *EWOChecklistProgressRepository {
	return &EWOChecklistProgressRepository{pool: pool}
}

var _ port.EWOChecklistProgressRepository = (*EWOChecklistProgressRepository)(nil)

const checklistProgressCols = `
	id, ewo_id, checklist_item_id, COALESCE(item_label, ''),
	status, completed_by, completed_at,
	photo_url, photo_hash, COALESCE(notes, ''),
	idempotency_key, created_at, updated_at
`

func (r *EWOChecklistProgressRepository) ListByEWO(
	ctx context.Context,
	ewoID uuid.UUID,
) ([]domain.EWOChecklistProgress, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+checklistProgressCols+`
		FROM enterprise.ewo_checklist_progress
		WHERE ewo_id = $1
		ORDER BY created_at ASC
		LIMIT 500`, ewoID)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal,
			"db.checklist_progress_list", "list checklist progress", err)
	}
	defer rows.Close()
	out := []domain.EWOChecklistProgress{}
	for rows.Next() {
		p, err := scanChecklistProgress(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

func (r *EWOChecklistProgressRepository) FindByIdempotencyKey(
	ctx context.Context,
	ewoID uuid.UUID,
	idempotencyKey string,
) (*domain.EWOChecklistProgress, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+checklistProgressCols+`
		FROM enterprise.ewo_checklist_progress
		WHERE ewo_id = $1 AND idempotency_key = $2`, ewoID, idempotencyKey)
	p, err := scanChecklistProgress(row)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// Upsert inserts a new row or, on idempotency_key conflict, re-fetches
// the existing row and copies it into *p. Caller sees the canonical
// persisted state regardless of which path fired.
func (r *EWOChecklistProgressRepository) Upsert(
	ctx context.Context,
	p *domain.EWOChecklistProgress,
) error {
	if p == nil {
		return derrors.Validation(
			"checklist_progress.nil",
			"progress row is nil",
		)
	}
	if err := p.Validate(); err != nil {
		return err
	}
	tag, err := r.pool.Exec(ctx, `
		INSERT INTO enterprise.ewo_checklist_progress
			(id, ewo_id, checklist_item_id, item_label,
			 status, completed_by, completed_at,
			 photo_url, photo_hash, notes,
			 idempotency_key, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (ewo_id, idempotency_key) DO NOTHING
	`,
		p.ID, p.EWOID, p.ChecklistItemID, p.ItemLabel,
		string(p.Status), p.CompletedBy, p.CompletedAt,
		p.PhotoURL, p.PhotoHash, p.Notes,
		p.IdempotencyKey, p.CreatedAt, p.UpdatedAt,
	)
	if err != nil {
		return mapDBError(err, "checklist_progress", "insert checklist progress")
	}
	if tag.RowsAffected() == 0 && p.IdempotencyKey != nil {
		// Replay — refetch the existing row.
		existing, ferr := r.FindByIdempotencyKey(ctx, p.EWOID, *p.IdempotencyKey)
		if ferr != nil {
			return ferr
		}
		*p = *existing
	}
	return nil
}

func scanChecklistProgress(row pgx.Row) (domain.EWOChecklistProgress, error) {
	var (
		p      domain.EWOChecklistProgress
		status string
	)
	err := row.Scan(
		&p.ID, &p.EWOID, &p.ChecklistItemID, &p.ItemLabel,
		&status, &p.CompletedBy, &p.CompletedAt,
		&p.PhotoURL, &p.PhotoHash, &p.Notes,
		&p.IdempotencyKey, &p.CreatedAt, &p.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.EWOChecklistProgress{}, derrors.NotFound(
			"checklist_progress.not_found", "checklist progress not found")
	}
	if err != nil {
		return domain.EWOChecklistProgress{}, derrors.Wrap(derrors.KindInternal,
			"db.checklist_progress_scan", "scan checklist progress", err)
	}
	p.Status = domain.ChecklistItemStatus(status)
	return p, nil
}
