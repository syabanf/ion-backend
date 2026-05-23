package postgres

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/operations/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// CSDashboardAggregationRepository — persists cs.dashboard_aggregations
// =====================================================================

type CSDashboardAggregationRepository struct {
	pool *pgxpool.Pool
}

func NewCSDashboardAggregationRepository(pool *pgxpool.Pool) *CSDashboardAggregationRepository {
	return &CSDashboardAggregationRepository{pool: pool}
}

var _ port.CSDashboardAggregationRepository = (*CSDashboardAggregationRepository)(nil)

func (r *CSDashboardAggregationRepository) Create(ctx context.Context, kind string, scopeUserID, scopeTeamID *uuid.UUID, periodStart, periodEnd *time.Time, payload map[string]any) error {
	payloadJSON, _ := json.Marshal(payload)
	if len(payloadJSON) == 0 {
		payloadJSON = []byte("{}")
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO cs.dashboard_aggregations
			(kind, scope_user_id, scope_team_id,
			 period_start, period_end, payload)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb)
	`, kind, scopeUserID, scopeTeamID, periodStart, periodEnd, string(payloadJSON))
	return mapDBError(err, "cs_dashboard", "insert aggregation")
}

func (r *CSDashboardAggregationRepository) LatestByKind(ctx context.Context, kind string, scopeUserID, scopeTeamID *uuid.UUID) (*port.DashboardAggregationRow, error) {
	q := `
		SELECT id, kind, scope_user_id, scope_team_id, aggregated_at,
		       period_start, period_end, COALESCE(payload::text, '{}')
		  FROM cs.dashboard_aggregations
		 WHERE kind = $1`
	args := []any{kind}
	if scopeUserID != nil {
		q += ` AND scope_user_id = $2`
		args = append(args, *scopeUserID)
	} else {
		q += ` AND scope_user_id IS NULL`
	}
	if scopeTeamID != nil {
		// Append next positional.
		if scopeUserID != nil {
			q += ` AND scope_team_id = $3`
		} else {
			q += ` AND scope_team_id = $2`
		}
		args = append(args, *scopeTeamID)
	} else {
		q += ` AND scope_team_id IS NULL`
	}
	q += ` ORDER BY aggregated_at DESC LIMIT 1`

	var row port.DashboardAggregationRow
	var payloadJSON string
	err := r.pool.QueryRow(ctx, q, args...).Scan(
		&row.ID, &row.Kind, &row.ScopeUserID, &row.ScopeTeamID,
		&row.AggregatedAt, &row.PeriodStart, &row.PeriodEnd, &payloadJSON,
	)
	if stderrors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, mapDBError(err, "cs_dashboard", "latest by kind")
	}
	if payloadJSON != "" && payloadJSON != "{}" {
		_ = json.Unmarshal([]byte(payloadJSON), &row.Payload)
	}
	return &row, nil
}

// =====================================================================
// CSDashboardLiveReader — SQL-only queries against cs.* tables. Used by
// the CSDashboardService when no precomputed row matches.
//
// Queries are defensive against schema variance: COALESCE around
// nullable counters and best-effort role detection (role on the user
// row, NOT on a per-ticket basis — agents have role='cs_agent').
// =====================================================================

type CSDashboardLiveReader struct {
	pool *pgxpool.Pool
}

func NewCSDashboardLiveReader(pool *pgxpool.Pool) *CSDashboardLiveReader {
	return &CSDashboardLiveReader{pool: pool}
}

var _ port.CSDashboardLiveReader = (*CSDashboardLiveReader)(nil)

func (r *CSDashboardLiveReader) AgentQueue(ctx context.Context, userID uuid.UUID) (port.AgentQueueSnapshot, error) {
	out := port.AgentQueueSnapshot{UserID: userID}
	// Counters in one round-trip via COUNT FILTER.
	err := r.pool.QueryRow(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE assigned_to = $1 AND status NOT IN ('resolved','closed','cancelled')),
			COUNT(*) FILTER (WHERE assigned_to IS NULL AND status = 'open'),
			COUNT(*) FILTER (WHERE assigned_to = $1 AND status = 'pending_internal'),
			COUNT(*) FILTER (WHERE assigned_to = $1 AND status = 'pending_customer'),
			COUNT(*) FILTER (WHERE assigned_to = $1 AND status = 'resolved' AND resolved_at >= NOW() - interval '1 day'),
			COUNT(*) FILTER (WHERE assigned_to = $1
				AND status NOT IN ('resolved','closed','cancelled')
				AND sla_resolve_due_at IS NOT NULL
				AND sla_resolve_due_at < NOW() + interval '30 minutes'
				AND COALESCE(sla_breached_resolve, FALSE) = FALSE),
			COUNT(*) FILTER (WHERE assigned_to = $1
				AND COALESCE(sla_breached_resolve, FALSE) = TRUE
				AND status NOT IN ('resolved','closed','cancelled'))
		  FROM cs.tickets
	`, userID).Scan(
		&out.OpenAssigned, &out.UnassignedAvailable,
		&out.PendingInternal, &out.PendingCustomer,
		&out.ResolvedToday, &out.SLAAtRisk, &out.SLABreached,
	)
	if err != nil {
		// If cs.tickets isn't installed (legacy DB), return zero snapshot.
		return out, nil
	}
	// Open tickets list (top 50)
	rows, qerr := r.pool.Query(ctx, `
		SELECT id, COALESCE(title,''), COALESCE(priority,''), COALESCE(status,''),
		       sla_first_response_due_at, sla_resolve_due_at,
		       COALESCE(sla_breached_resolve, FALSE)
		  FROM cs.tickets
		 WHERE assigned_to = $1
		   AND status NOT IN ('resolved','closed','cancelled')
		 ORDER BY COALESCE(sla_resolve_due_at, NOW() + interval '1 year') ASC
		 LIMIT 50
	`, userID)
	if qerr == nil {
		defer rows.Close()
		now := time.Now().UTC()
		for rows.Next() {
			var t port.AgentQueueTicket
			var frDue, rvDue *time.Time
			if err := rows.Scan(&t.TicketID, &t.Title, &t.Priority, &t.Status,
				&frDue, &rvDue, &t.Breached); err != nil {
				continue
			}
			if frDue != nil {
				t.FirstResponseRemainingS = int64(frDue.Sub(now).Seconds())
			}
			if rvDue != nil {
				t.ResolveRemainingS = int64(rvDue.Sub(now).Seconds())
			}
			out.OpenTickets = append(out.OpenTickets, t)
		}
	}
	return out, nil
}

func (r *CSDashboardLiveReader) SupervisorTeamSLA(ctx context.Context, supervisorUserID uuid.UUID) (port.TeamSLASnapshot, error) {
	out := port.TeamSLASnapshot{SupervisorUserID: supervisorUserID}
	// Resolve the supervisor's team IDs first.
	teamRows, err := r.pool.Query(ctx, `
		SELECT id FROM cs.teams WHERE manager_user_id = $1 AND active = TRUE
	`, supervisorUserID)
	if err != nil {
		return out, nil
	}
	defer teamRows.Close()
	for teamRows.Next() {
		var tid uuid.UUID
		if err := teamRows.Scan(&tid); err == nil {
			out.TeamIDs = append(out.TeamIDs, tid)
		}
	}
	if len(out.TeamIDs) == 0 {
		return out, nil
	}
	// Single COUNT FILTER round-trip for SLA stats.
	var openCount, resolved24h, breachedFR, breachedRV int
	var avgFRMin, avgRVMin *float64
	err = r.pool.QueryRow(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE status NOT IN ('resolved','closed','cancelled')),
			COUNT(*) FILTER (WHERE status = 'resolved' AND resolved_at >= NOW() - interval '1 day'),
			COUNT(*) FILTER (WHERE COALESCE(sla_breached_first_response, FALSE)),
			COUNT(*) FILTER (WHERE COALESCE(sla_breached_resolve, FALSE)),
			AVG(EXTRACT(EPOCH FROM (first_response_at - opened_at))/60)
				FILTER (WHERE first_response_at IS NOT NULL),
			AVG(EXTRACT(EPOCH FROM (resolved_at - opened_at))/60)
				FILTER (WHERE resolved_at IS NOT NULL AND status = 'resolved')
		  FROM cs.tickets
		 WHERE assigned_team_id = ANY($1)
	`, out.TeamIDs).Scan(&openCount, &resolved24h, &breachedFR, &breachedRV, &avgFRMin, &avgRVMin)
	if err != nil {
		return out, nil
	}
	out.OpenCount = openCount
	out.ResolvedLast24h = resolved24h
	out.BreachedFirstResp = breachedFR
	out.BreachedResolve = breachedRV
	if avgFRMin != nil {
		out.AvgFirstResponseMin = int(*avgFRMin)
	}
	if avgRVMin != nil {
		out.AvgResolveMin = int(*avgRVMin)
	}
	totalBreaches := breachedFR + breachedRV
	totalTickets := openCount + resolved24h
	if totalTickets > 0 {
		out.CompliancePct = 1.0 - float64(totalBreaches)/float64(totalTickets+1)
	} else {
		out.CompliancePct = 1.0
	}
	return out, nil
}

func (r *CSDashboardLiveReader) EscalationQueue(ctx context.Context, minLevel int, limit int) ([]port.EscalationRow, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx, `
		SELECT t.id, COALESCE(t.title,''), COALESCE(t.priority,''),
		       COALESCE(t.status,''), COALESCE(t.escalation_level, 0),
		       COALESCE(t.escalated_at, t.opened_at), t.assigned_to
		  FROM cs.tickets t
		 WHERE COALESCE(t.escalation_level, 0) >= $1
		   AND t.status NOT IN ('resolved','closed','cancelled')
		 ORDER BY COALESCE(t.escalation_level, 0) DESC, t.opened_at ASC
		 LIMIT $2
	`, minLevel, limit)
	if err != nil {
		// Schema variance: escalation_level column may not exist; return empty.
		return nil, nil
	}
	defer rows.Close()
	out := []port.EscalationRow{}
	for rows.Next() {
		var r port.EscalationRow
		if err := rows.Scan(&r.TicketID, &r.Title, &r.Priority, &r.Status,
			&r.Level, &r.EscalatedAt, &r.AssignedTo); err != nil {
			continue
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (r *CSDashboardLiveReader) SatisfactionSummary(ctx context.Context, from, to time.Time) (port.SatisfactionSummary, error) {
	out := port.SatisfactionSummary{From: from, To: to}
	var count int
	var avgRating *float64
	var promoters, detractors int
	err := r.pool.QueryRow(ctx, `
		SELECT COUNT(*), AVG(rating)::float8,
		       COUNT(*) FILTER (WHERE rating = 5),
		       COUNT(*) FILTER (WHERE rating IN (1,2))
		  FROM cs.csat_responses
		 WHERE created_at BETWEEN $1 AND $2
	`, from, to).Scan(&count, &avgRating, &promoters, &detractors)
	if err != nil {
		return out, nil
	}
	out.Count = count
	if avgRating != nil {
		out.AvgRating = *avgRating
	}
	if count > 0 {
		pp := float64(promoters) / float64(count)
		dp := float64(detractors) / float64(count)
		out.NPSScore = (pp - dp) * 100
	}
	rows, qerr := r.pool.Query(ctx, `
		SELECT ticket_id, rating, COALESCE(comment, ''), created_at
		  FROM cs.csat_responses
		 WHERE created_at BETWEEN $1 AND $2
		   AND rating <= 2
		 ORDER BY created_at DESC
		 LIMIT 25
	`, from, to)
	if qerr == nil {
		defer rows.Close()
		for rows.Next() {
			var row port.CSATCriticalRow
			if err := rows.Scan(&row.TicketID, &row.Rating, &row.Comment, &row.CreatedAt); err == nil {
				out.CriticalLow = append(out.CriticalLow, row)
			}
		}
	}
	return out, nil
}

func (r *CSDashboardLiveReader) ChannelDistribution(ctx context.Context, from, to time.Time) (map[string]int, error) {
	out := map[string]int{}
	rows, err := r.pool.Query(ctx, `
		SELECT COALESCE(opened_via, 'unknown'), COUNT(*)
		  FROM cs.tickets
		 WHERE opened_at BETWEEN $1 AND $2
		 GROUP BY COALESCE(opened_via, 'unknown')
	`, from, to)
	if err != nil {
		return out, nil
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var count int
		if err := rows.Scan(&key, &count); err != nil {
			continue
		}
		out[key] = count
	}
	return out, nil
}

func (r *CSDashboardLiveReader) ActiveAgentIDs(ctx context.Context, limit int) ([]uuid.UUID, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := r.pool.Query(ctx, `
		SELECT DISTINCT assigned_to
		  FROM cs.tickets
		 WHERE assigned_to IS NOT NULL
		   AND status NOT IN ('resolved','closed','cancelled')
		 LIMIT $1
	`, limit)
	if err != nil {
		return nil, nil
	}
	defer rows.Close()
	out := []uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err == nil {
			out = append(out, id)
		}
	}
	return out, rows.Err()
}

func (r *CSDashboardLiveReader) SupervisorTeamIDs(ctx context.Context, limit int) ([]uuid.UUID, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx, `
		SELECT DISTINCT manager_user_id
		  FROM cs.teams
		 WHERE active = TRUE AND manager_user_id IS NOT NULL
		 LIMIT $1
	`, limit)
	if err != nil {
		return nil, nil
	}
	defer rows.Close()
	out := []uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err == nil {
			out = append(out, id)
		}
	}
	return out, rows.Err()
}

// Silence unused-import warnings while keeping derrors available for
// future error-path additions.
var _ = derrors.Validation
