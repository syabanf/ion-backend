package postgres

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/operations/domain"
	"github.com/ion-core/backend/internal/operations/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// CrossModuleSLASnapshotRepository persists
// operations.cross_module_sla_snapshots.
type CrossModuleSLASnapshotRepository struct {
	pool *pgxpool.Pool
}

func NewCrossModuleSLASnapshotRepository(pool *pgxpool.Pool) *CrossModuleSLASnapshotRepository {
	return &CrossModuleSLASnapshotRepository{pool: pool}
}

var _ port.CrossModuleSLASnapshotRepository = (*CrossModuleSLASnapshotRepository)(nil)

func (r *CrossModuleSLASnapshotRepository) Create(ctx context.Context, s *domain.SLASnapshot) error {
	if s == nil {
		return derrors.Validation("xmod_sla.nil", "snapshot is nil")
	}
	topJSON, _ := json.Marshal(s.TopBreachers)
	if len(topJSON) == 0 {
		topJSON = []byte("[]")
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO operations.cross_module_sla_snapshots
			(id, module, aggregated_at, period_start, period_end,
			 total_at_risk, total_breached,
			 p50_remaining_minutes, p95_remaining_minutes, top_breachers)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::jsonb)
	`,
		s.ID, string(s.Module), s.AggregatedAt, s.PeriodStart, s.PeriodEnd,
		s.TotalAtRisk, s.TotalBreached,
		s.P50RemainingMinutes, s.P95RemainingMinutes, string(topJSON),
	)
	return mapDBError(err, "xmod_sla", "insert snapshot")
}

func (r *CrossModuleSLASnapshotRepository) LatestForModule(ctx context.Context, module domain.SLAModule) (*domain.SLASnapshot, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, module, aggregated_at, period_start, period_end,
		       total_at_risk, total_breached,
		       p50_remaining_minutes, p95_remaining_minutes,
		       COALESCE(top_breachers::text, '[]')
		  FROM operations.cross_module_sla_snapshots
		 WHERE module = $1
		 ORDER BY aggregated_at DESC
		 LIMIT 1
	`, string(module))
	s, err := scanSnapshotRow(row)
	if stderrors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return s, err
}

func (r *CrossModuleSLASnapshotRepository) ListLatest(ctx context.Context) ([]domain.SLASnapshot, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT DISTINCT ON (module)
		       id, module, aggregated_at, period_start, period_end,
		       total_at_risk, total_breached,
		       p50_remaining_minutes, p95_remaining_minutes,
		       COALESCE(top_breachers::text, '[]')
		  FROM operations.cross_module_sla_snapshots
		 ORDER BY module, aggregated_at DESC
	`)
	if err != nil {
		return nil, mapDBError(err, "xmod_sla", "list latest")
	}
	defer rows.Close()
	out := []domain.SLASnapshot{}
	for rows.Next() {
		s, err := scanSnapshotRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

func (r *CrossModuleSLASnapshotRepository) History(ctx context.Context, module domain.SLAModule, from, to time.Time, limit int) ([]domain.SLASnapshot, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id, module, aggregated_at, period_start, period_end,
		       total_at_risk, total_breached,
		       p50_remaining_minutes, p95_remaining_minutes,
		       COALESCE(top_breachers::text, '[]')
		  FROM operations.cross_module_sla_snapshots
		 WHERE module = $1
		   AND aggregated_at BETWEEN $2 AND $3
		 ORDER BY aggregated_at DESC
		 LIMIT $4
	`, string(module), from, to, limit)
	if err != nil {
		return nil, mapDBError(err, "xmod_sla", "history")
	}
	defer rows.Close()
	out := []domain.SLASnapshot{}
	for rows.Next() {
		s, err := scanSnapshotRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

func scanSnapshotRow(row rowScanner) (*domain.SLASnapshot, error) {
	var s domain.SLASnapshot
	var module string
	var topJSON string
	err := row.Scan(
		&s.ID, &module, &s.AggregatedAt, &s.PeriodStart, &s.PeriodEnd,
		&s.TotalAtRisk, &s.TotalBreached,
		&s.P50RemainingMinutes, &s.P95RemainingMinutes, &topJSON,
	)
	if err != nil {
		return nil, err
	}
	s.Module = domain.SLAModule(module)
	if topJSON != "" && topJSON != "[]" {
		_ = json.Unmarshal([]byte(topJSON), &s.TopBreachers)
	}
	return &s, nil
}
