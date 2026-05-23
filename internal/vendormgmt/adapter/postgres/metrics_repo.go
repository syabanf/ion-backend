package postgres

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/vendormgmt/domain"
	"github.com/ion-core/backend/internal/vendormgmt/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// MetricsRepository implements port.MetricsRepository.
type MetricsRepository struct {
	pool *pgxpool.Pool
}

func NewMetricsRepository(pool *pgxpool.Pool) *MetricsRepository {
	return &MetricsRepository{pool: pool}
}

var _ port.MetricsRepository = (*MetricsRepository)(nil)

// Upsert writes one row keyed on (provider_id, metric_date). Re-running
// the cron for the same day overwrites the optional metric fields —
// last-write-wins.
func (r *MetricsRepository) Upsert(ctx context.Context, m *domain.DailyMetric) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO vendor.provider_metrics_daily
			(id, provider_id, metric_date,
			 jobs_completed, on_time_completion_pct, avg_response_hours,
			 tickets_resolved, customer_satisfaction, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (provider_id, metric_date) DO UPDATE
			SET jobs_completed          = EXCLUDED.jobs_completed,
			    on_time_completion_pct  = EXCLUDED.on_time_completion_pct,
			    avg_response_hours      = EXCLUDED.avg_response_hours,
			    tickets_resolved        = EXCLUDED.tickets_resolved,
			    customer_satisfaction   = EXCLUDED.customer_satisfaction
	`,
		m.ID, m.ProviderID, m.MetricDate,
		m.JobsCompleted, m.OnTimeCompletionPct, m.AvgResponseHours,
		m.TicketsResolved, m.CustomerSatisfaction, m.CreatedAt,
	)
	if err != nil {
		return mapDBError(err, "provider_metric", "upsert metric")
	}
	return nil
}

func (r *MetricsRepository) ListForProvider(ctx context.Context, providerID uuid.UUID, from, to time.Time) ([]domain.DailyMetric, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, provider_id, metric_date,
		       jobs_completed, on_time_completion_pct, avg_response_hours,
		       tickets_resolved, customer_satisfaction, created_at
		FROM vendor.provider_metrics_daily
		WHERE provider_id = $1
		  AND metric_date BETWEEN $2 AND $3
		ORDER BY metric_date DESC
	`, providerID, from, to)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.metric_list", "list metrics", err)
	}
	defer rows.Close()
	out := []domain.DailyMetric{}
	for rows.Next() {
		var m domain.DailyMetric
		if err := rows.Scan(&m.ID, &m.ProviderID, &m.MetricDate,
			&m.JobsCompleted, &m.OnTimeCompletionPct, &m.AvgResponseHours,
			&m.TicketsResolved, &m.CustomerSatisfaction, &m.CreatedAt); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "db.metric_scan", "scan metric", err)
		}
		out = append(out, m)
	}
	return out, nil
}

// AverageScore returns the avg customer_satisfaction over the lookback
// window. Returns 0 when no rows match — caller treats that as "no
// signal", not as a bad score.
func (r *MetricsRepository) AverageScore(ctx context.Context, providerID uuid.UUID, lookbackDays int) (float64, error) {
	var avg *float64
	err := r.pool.QueryRow(ctx, `
		SELECT AVG(customer_satisfaction)
		FROM vendor.provider_metrics_daily
		WHERE provider_id = $1
		  AND metric_date >= CURRENT_DATE - ($2::int || ' days')::interval
		  AND customer_satisfaction IS NOT NULL
	`, providerID, lookbackDays).Scan(&avg)
	if err != nil {
		return 0, derrors.Wrap(derrors.KindInternal, "db.metric_avg", "compute average score", err)
	}
	if avg == nil {
		return 0, nil
	}
	return *avg, nil
}

// TopRated runs the ranked picker query. Filtering by capability is a
// SQL JOIN against vendor.provider_capabilities so we don't drag entire
// providers across the wire just to discard them.
func (r *MetricsRepository) TopRated(ctx context.Context, f port.TopRatedFilter) ([]domain.Provider, error) {
	var wh []string
	var args []any
	wh = append(wh, "p.status = 'active'")
	if f.MinRating > 0 {
		args = append(args, f.MinRating)
		wh = append(wh, fmt.Sprintf("p.rating_score >= $%d", len(args)))
	}
	if f.MinJobs > 0 {
		args = append(args, f.MinJobs)
		wh = append(wh, fmt.Sprintf("p.total_completed_jobs >= $%d", len(args)))
	}
	join := ""
	if f.Capability != "" {
		args = append(args, f.Capability)
		join = fmt.Sprintf(
			" JOIN vendor.provider_capabilities c ON c.provider_id = p.id AND c.capability_key = $%d",
			len(args),
		)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 20
	}
	args = append(args, limit)
	sql := `
		SELECT ` + providerColsAliased("p") + `
		FROM vendor.providers p` + join + `
		WHERE ` + strings.Join(wh, " AND ") + `
		ORDER BY p.rating_score DESC, p.total_completed_jobs DESC
		LIMIT $` + fmt.Sprint(len(args))
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.top_rated", "rank top providers", err)
	}
	defer rows.Close()
	out := []domain.Provider{}
	for rows.Next() {
		p, err := scanProvider(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// providerColsAliased is the same column list as providerCols but with
// every reference qualified by the supplied alias. The JOIN in TopRated
// would otherwise hit ambiguous column names.
func providerColsAliased(alias string) string {
	cols := []string{
		"id", "name",
		"COALESCE(npwp, '')",
		"COALESCE(contact_email, '')",
		"COALESCE(contact_phone, '')",
		"status", "kyc_completed",
		"COALESCE(capabilities, '[]'::jsonb)",
		"rating_score", "total_completed_jobs", "total_revenue",
		"created_at", "updated_at",
		"suspended_at",
		"COALESCE(suspended_reason, '')",
	}
	for i, c := range cols {
		if strings.HasPrefix(c, "COALESCE") {
			cols[i] = strings.Replace(c, "(", "("+alias+".", 1)
		} else {
			cols[i] = alias + "." + c
		}
	}
	return strings.Join(cols, ", ")
}
