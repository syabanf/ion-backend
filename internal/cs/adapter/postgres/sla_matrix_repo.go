package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/cs/domain"
	"github.com/ion-core/backend/internal/cs/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// SLAMatrixRepository implements port.SLAMatrixRepository.
type SLAMatrixRepository struct {
	pool *pgxpool.Pool
}

func NewSLAMatrixRepository(pool *pgxpool.Pool) *SLAMatrixRepository {
	return &SLAMatrixRepository{pool: pool}
}

var _ port.SLAMatrixRepository = (*SLAMatrixRepository)(nil)

const slaMatrixCols = `
	id, customer_type, ticket_type, priority,
	first_response_minutes, resolve_minutes,
	breach_warn_pct,
	COALESCE(escalation_levels, '[]'::jsonb),
	is_active, effective_from, effective_to,
	created_at, updated_at
`

// FindByKey returns the most recent active row for (customer_type,
// ticket_type, priority) whose effective_from <= at.
func (r *SLAMatrixRepository) FindByKey(ctx context.Context, ct domain.CustomerType, tt domain.TicketType, p domain.Priority, at time.Time) (*domain.SLAMatrixEntry, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT `+slaMatrixCols+`
		  FROM cs.sla_matrix
		 WHERE customer_type = $1
		   AND ticket_type   = $2
		   AND priority      = $3
		   AND is_active     = TRUE
		   AND effective_from <= $4
		   AND (effective_to IS NULL OR effective_to >= $4)
		 ORDER BY effective_from DESC
		 LIMIT 1
	`, string(ct), string(tt), string(p), at.UTC())
	e, err := scanSLAMatrixRow(row)
	if err != nil {
		return nil, err
	}
	return &e, nil
}

func (r *SLAMatrixRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.SLAMatrixEntry, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+slaMatrixCols+` FROM cs.sla_matrix WHERE id = $1`, id)
	e, err := scanSLAMatrixRow(row)
	if err != nil {
		return nil, err
	}
	return &e, nil
}

func (r *SLAMatrixRepository) List(ctx context.Context, f port.SLAMatrixFilter) ([]domain.SLAMatrixEntry, error) {
	var wh []string
	var args []any
	add := func(cond string, val any) {
		args = append(args, val)
		wh = append(wh, fmt.Sprintf(cond, len(args)))
	}
	if f.CustomerType != "" {
		add("customer_type = $%d", f.CustomerType)
	}
	if f.TicketType != "" {
		add("ticket_type = $%d", f.TicketType)
	}
	if f.Priority != "" {
		add("priority = $%d", f.Priority)
	}
	if f.OnlyActive {
		wh = append(wh, "is_active = TRUE")
	}
	where := ""
	if len(wh) > 0 {
		where = " WHERE " + strings.Join(wh, " AND ")
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 500
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}
	args = append(args, limit, offset)
	sql := `SELECT ` + slaMatrixCols + ` FROM cs.sla_matrix` + where +
		` ORDER BY customer_type, ticket_type, priority, effective_from DESC` +
		` LIMIT $` + fmt.Sprint(len(args)-1) + ` OFFSET $` + fmt.Sprint(len(args))
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "cs.sla.list", "list sla matrix", err)
	}
	defer rows.Close()
	out := []domain.SLAMatrixEntry{}
	for rows.Next() {
		e, err := scanSLAMatrixRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

// Upsert performs an INSERT … ON CONFLICT on the natural unique key
// (customer_type, ticket_type, priority, effective_from). Updates the
// resolve/first_response minutes + active flag when present.
func (r *SLAMatrixRepository) Upsert(ctx context.Context, e *domain.SLAMatrixEntry) error {
	levels, err := json.Marshal(e.EscalationLevels)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "cs.sla.marshal", "marshal escalation_levels", err)
	}
	if len(levels) == 0 || string(levels) == "null" {
		levels = []byte("[]")
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO cs.sla_matrix
			(id, customer_type, ticket_type, priority,
			 first_response_minutes, resolve_minutes,
			 breach_warn_pct, escalation_levels,
			 is_active, effective_from, effective_to,
			 created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		ON CONFLICT (customer_type, ticket_type, priority, effective_from)
		DO UPDATE SET
			first_response_minutes = EXCLUDED.first_response_minutes,
			resolve_minutes        = EXCLUDED.resolve_minutes,
			breach_warn_pct        = EXCLUDED.breach_warn_pct,
			escalation_levels      = EXCLUDED.escalation_levels,
			is_active              = EXCLUDED.is_active,
			effective_to           = EXCLUDED.effective_to,
			updated_at             = NOW()
	`,
		e.ID, string(e.CustomerType), string(e.TicketType), string(e.Priority),
		e.FirstResponseMinutes, e.ResolveMinutes,
		e.BreachWarnPct, levels,
		e.IsActive, e.EffectiveFrom, e.EffectiveTo,
		e.CreatedAt, e.UpdatedAt,
	)
	if err != nil {
		return mapDBError(err, "cs.sla", "upsert sla matrix")
	}
	return nil
}

func scanSLAMatrixRow(row pgx.Row) (domain.SLAMatrixEntry, error) {
	var e domain.SLAMatrixEntry
	var ct, tt, prio string
	var levels []byte
	err := row.Scan(
		&e.ID, &ct, &tt, &prio,
		&e.FirstResponseMinutes, &e.ResolveMinutes,
		&e.BreachWarnPct, &levels,
		&e.IsActive, &e.EffectiveFrom, &e.EffectiveTo,
		&e.CreatedAt, &e.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.SLAMatrixEntry{}, derrors.NotFound("cs.sla.not_found", "sla matrix entry not found")
	}
	if err != nil {
		return domain.SLAMatrixEntry{}, derrors.Wrap(derrors.KindInternal, "cs.sla.scan", "scan sla matrix", err)
	}
	e.CustomerType = domain.CustomerType(ct)
	e.TicketType = domain.TicketType(tt)
	e.Priority = domain.Priority(prio)
	if len(levels) > 0 {
		_ = json.Unmarshal(levels, &e.EscalationLevels)
	}
	return e, nil
}
