package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/nocmon/domain"
	"github.com/ion-core/backend/internal/nocmon/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// ServiceProbeRepository implements port.ServiceProbeRepository
// against `nocmon.service_probes`.
type ServiceProbeRepository struct {
	pool *pgxpool.Pool
}

func NewServiceProbeRepository(pool *pgxpool.Pool) *ServiceProbeRepository {
	return &ServiceProbeRepository{pool: pool}
}

var _ port.ServiceProbeRepository = (*ServiceProbeRepository)(nil)

const probeCols = `
	id, customer_id, plan_id, probe_kind,
	COALESCE(probe_target, ''),
	interval_seconds, threshold_warn, threshold_critical,
	is_active, last_probed_at, last_status,
	created_at, updated_at
`

func (r *ServiceProbeRepository) Create(ctx context.Context, p *domain.ServiceProbe) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO nocmon.service_probes
			(id, customer_id, plan_id, probe_kind, probe_target,
			 interval_seconds, threshold_warn, threshold_critical,
			 is_active, last_probed_at, last_status,
			 created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
	`,
		p.ID, p.CustomerID, p.PlanID, string(p.ProbeKind), nullableString(p.ProbeTarget),
		p.IntervalSeconds, p.ThresholdWarn, p.ThresholdCritical,
		p.IsActive, p.LastProbedAt, string(p.LastStatus),
		p.CreatedAt, p.UpdatedAt,
	)
	if err != nil {
		return mapDBError(err, "probe", "insert service_probe")
	}
	return nil
}

func (r *ServiceProbeRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.ServiceProbe, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+probeCols+` FROM nocmon.service_probes WHERE id = $1`, id)
	p, err := scanProbe(row)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (r *ServiceProbeRepository) List(ctx context.Context, f port.ProbeListFilter) ([]domain.ServiceProbe, int, error) {
	args := []any{}
	wh := []string{}
	if f.CustomerID != nil {
		args = append(args, *f.CustomerID)
		wh = append(wh, fmt.Sprintf("customer_id = $%d", len(args)))
	}
	if f.Kind != "" {
		args = append(args, string(f.Kind))
		wh = append(wh, fmt.Sprintf("probe_kind = $%d", len(args)))
	}
	if f.OnlyActive {
		wh = append(wh, "is_active = TRUE")
	}
	where := ""
	if len(wh) > 0 {
		where = " WHERE " + strings.Join(wh, " AND ")
	}

	var total int
	if err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM nocmon.service_probes`+where, args...).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.probe_count", "count probes", err)
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
	sql := `SELECT ` + probeCols + ` FROM nocmon.service_probes` + where +
		` ORDER BY created_at DESC LIMIT $` + fmt.Sprint(len(args)-1) +
		` OFFSET $` + fmt.Sprint(len(args))

	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.probe_list", "list probes", err)
	}
	defer rows.Close()
	out := []domain.ServiceProbe{}
	for rows.Next() {
		p, err := scanProbe(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, p)
	}
	return out, total, nil
}

// ListDue picks the probes the cron tick should run on this round.
// The is_active filter is mandatory; the time-arithmetic uses
// make_interval so a deployed binary that drifts in Go's
// time.Duration string formatting can't break it.
func (r *ServiceProbeRepository) ListDue(ctx context.Context, asOf time.Time, limit int) ([]domain.ServiceProbe, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+probeCols+`
		FROM nocmon.service_probes
		WHERE is_active = TRUE
		  AND (
		      last_probed_at IS NULL
		      OR last_probed_at + make_interval(secs => interval_seconds) <= $1
		  )
		ORDER BY COALESCE(last_probed_at, 'epoch'::timestamptz) ASC
		LIMIT $2
	`, asOf, limit)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.probe_due", "list due probes", err)
	}
	defer rows.Close()
	out := []domain.ServiceProbe{}
	for rows.Next() {
		p, err := scanProbe(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

func (r *ServiceProbeRepository) ListUnhealthy(ctx context.Context, limit int) ([]domain.ServiceProbe, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+probeCols+`
		FROM nocmon.service_probes
		WHERE is_active = TRUE
		  AND last_status IN ('warn','critical','unreachable')
		ORDER BY last_probed_at DESC NULLS LAST
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.probe_unhealthy", "list unhealthy probes", err)
	}
	defer rows.Close()
	out := []domain.ServiceProbe{}
	for rows.Next() {
		p, err := scanProbe(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

func (r *ServiceProbeRepository) Update(ctx context.Context, p *domain.ServiceProbe) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE nocmon.service_probes
		SET probe_target = $2,
		    interval_seconds = $3,
		    threshold_warn = $4,
		    threshold_critical = $5,
		    updated_at = $6
		WHERE id = $1
	`, p.ID, nullableString(p.ProbeTarget), p.IntervalSeconds,
		p.ThresholdWarn, p.ThresholdCritical, p.UpdatedAt,
	)
	if err != nil {
		return mapDBError(err, "probe", "update probe")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("probe.not_found", "probe not found")
	}
	return nil
}

func (r *ServiceProbeRepository) UpdateLastSample(ctx context.Context, id uuid.UUID, status domain.SampleStatus, at time.Time) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE nocmon.service_probes
		SET last_probed_at = $2,
		    last_status = $3,
		    updated_at = $2
		WHERE id = $1
	`, id, at, string(status))
	if err != nil {
		return mapDBError(err, "probe", "update probe last sample")
	}
	return nil
}

func (r *ServiceProbeRepository) UpdateActive(ctx context.Context, p *domain.ServiceProbe) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE nocmon.service_probes
		SET is_active = $2, updated_at = $3
		WHERE id = $1
	`, p.ID, p.IsActive, p.UpdatedAt)
	if err != nil {
		return mapDBError(err, "probe", "update probe active")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("probe.not_found", "probe not found")
	}
	return nil
}

func scanProbe(row pgx.Row) (domain.ServiceProbe, error) {
	var p domain.ServiceProbe
	var kind, status string
	err := row.Scan(
		&p.ID, &p.CustomerID, &p.PlanID, &kind,
		&p.ProbeTarget,
		&p.IntervalSeconds, &p.ThresholdWarn, &p.ThresholdCritical,
		&p.IsActive, &p.LastProbedAt, &status,
		&p.CreatedAt, &p.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ServiceProbe{}, derrors.NotFound("probe.not_found", "probe not found")
	}
	if err != nil {
		return domain.ServiceProbe{}, derrors.Wrap(derrors.KindInternal, "db.probe_scan", "scan probe", err)
	}
	p.ProbeKind = domain.ProbeKind(kind)
	p.LastStatus = domain.SampleStatus(status)
	return p, nil
}
