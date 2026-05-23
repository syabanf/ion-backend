package postgres

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/nocmon/domain"
	"github.com/ion-core/backend/internal/nocmon/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// HealthSampleRepository implements port.HealthSampleRepository
// against the partitioned `nocmon.service_health_samples` table.
type HealthSampleRepository struct {
	pool *pgxpool.Pool
}

func NewHealthSampleRepository(pool *pgxpool.Pool) *HealthSampleRepository {
	return &HealthSampleRepository{pool: pool}
}

var _ port.HealthSampleRepository = (*HealthSampleRepository)(nil)

// Insert is idempotent. The UNIQUE (probe_id, sampled_at) makes
// duplicate inserts (e.g. cron retries after a crash) safe — we
// translate a 23505 on this index back to "ok, no-op" so the caller
// doesn't need a special-case path.
func (r *HealthSampleRepository) Insert(ctx context.Context, s *domain.HealthSample) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO nocmon.service_health_samples
			(id, probe_id, sampled_at, value, status)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (probe_id, sampled_at) DO NOTHING
	`,
		s.ID, s.ProbeID, s.SampledAt, s.Value, string(s.Status),
	)
	if err != nil {
		// Distinct path for the conflict — if we missed it via ON CONFLICT
		// (different unique index, partition-level), translate it to OK.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil
		}
		return mapDBError(err, "sample", "insert health_sample")
	}
	return nil
}

func (r *HealthSampleRepository) ListForProbe(ctx context.Context, f port.SampleListFilter) ([]domain.HealthSample, error) {
	if f.ProbeID == uuid.Nil {
		return nil, derrors.Validation("sample.probe_required", "probe_id is required")
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 500
	}
	args := []any{f.ProbeID}
	where := "probe_id = $1"
	if f.From != nil {
		args = append(args, *f.From)
		where += " AND sampled_at >= $2"
	}
	if f.To != nil {
		args = append(args, *f.To)
		where += " AND sampled_at <= $" + paramIndex(len(args))
	}
	args = append(args, limit)
	sql := `
		SELECT id, probe_id, sampled_at, value, status
		FROM nocmon.service_health_samples
		WHERE ` + where + `
		ORDER BY sampled_at DESC
		LIMIT $` + paramIndex(len(args))
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.sample_list", "list samples", err)
	}
	defer rows.Close()
	out := []domain.HealthSample{}
	for rows.Next() {
		var s domain.HealthSample
		var status string
		if err := rows.Scan(&s.ID, &s.ProbeID, &s.SampledAt, &s.Value, &status); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "db.sample_scan", "scan sample", err)
		}
		s.Status = domain.SampleStatus(status)
		out = append(out, s)
	}
	return out, nil
}

// CountConsecutive checks how many of the latest `lookback` samples
// on the probe match the given status. The anti-flap rule in the
// cron uses this to require 2+ consecutive criticals before opening
// a fault — a single spike doesn't page the NOC.
func (r *HealthSampleRepository) CountConsecutive(ctx context.Context, probeID uuid.UUID, status domain.SampleStatus, lookback int) (int, error) {
	if lookback <= 0 {
		lookback = 3
	}
	rows, err := r.pool.Query(ctx, `
		SELECT status FROM nocmon.service_health_samples
		WHERE probe_id = $1
		ORDER BY sampled_at DESC
		LIMIT $2
	`, probeID, lookback)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil
		}
		return 0, derrors.Wrap(derrors.KindInternal, "db.sample_consecutive", "count consecutive samples", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return 0, derrors.Wrap(derrors.KindInternal, "db.sample_scan", "scan status", err)
		}
		if domain.SampleStatus(s) != status {
			break // streak broken
		}
		count++
	}
	return count, nil
}

// paramIndex turns 3 → "3" without pulling in fmt for every call.
func paramIndex(n int) string {
	// kept tiny to keep this file's only fmt-style dep local
	const digits = "0123456789"
	if n < 10 {
		return string(digits[n])
	}
	// Two-digit fallback — plenty for our parameter counts.
	return string(digits[n/10]) + string(digits[n%10])
}
