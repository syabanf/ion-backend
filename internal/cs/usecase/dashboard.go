// Wave 126 — CSDashboardService: backend aggregations for the CS agent
// + supervisor dashboards.
//
// Previously the Web (Agent) and Web (Supervisor) dashboards pulled
// flat ticket lists and computed everything client-side. This service
// gives each dashboard a single canonical aggregation route so the
// numbers match across browsers, and a cron precomputes them every 15
// minutes for the p95<2s NFR.
//
// The repository + live-reader interfaces are defined in
// internal/operations/port/calendar.go (they share the postgres adapter
// with the rest of Wave 126; co-locating the SQL keeps the cross-cut
// queries together). The usecase here is in cs/usecase so the cs-svc
// can wire it independently.
package usecase

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	opsport "github.com/ion-core/backend/internal/operations/port"
)

// CSDashboardService computes the agent/team/escalation/CSAT/channel
// dashboards. Each method tries the precomputed row first (if any
// matches), then falls back to a live read.
type CSDashboardService struct {
	repo opsport.CSDashboardAggregationRepository
	live opsport.CSDashboardLiveReader
	log  *slog.Logger
	// staleAfter is how old a precomputed row may be before we fall
	// back to the live reader. Default 5 min.
	staleAfter time.Duration
}

// CSDashboardDeps groups the dependencies.
type CSDashboardDeps struct {
	Repo       opsport.CSDashboardAggregationRepository
	Live       opsport.CSDashboardLiveReader
	StaleAfter time.Duration
	Log        *slog.Logger
}

// NewCSDashboardService builds the service.
func NewCSDashboardService(deps CSDashboardDeps) *CSDashboardService {
	stale := deps.StaleAfter
	if stale <= 0 {
		stale = 5 * time.Minute
	}
	log := deps.Log
	if log == nil {
		log = slog.Default()
	}
	return &CSDashboardService{
		repo:       deps.Repo,
		live:       deps.Live,
		staleAfter: stale,
		log:        log.With("service", "cs.dashboard"),
	}
}

// AgentQueue returns the per-agent dashboard payload. Tries cached row
// first; on miss or stale, computes live.
func (s *CSDashboardService) AgentQueue(ctx context.Context, userID uuid.UUID) (opsport.AgentQueueSnapshot, error) {
	if s == nil || s.live == nil {
		return opsport.AgentQueueSnapshot{}, nil
	}
	if s.repo != nil {
		row, err := s.repo.LatestByKind(ctx, "agent_queue", &userID, nil)
		if err == nil && row != nil && time.Since(row.AggregatedAt) < s.staleAfter {
			if snap, ok := snapshotFromPayload(row.Payload, userID); ok {
				return snap, nil
			}
		}
	}
	return s.live.AgentQueue(ctx, userID)
}

// SupervisorTeamSLA returns the supervisor's per-team SLA payload.
func (s *CSDashboardService) SupervisorTeamSLA(ctx context.Context, supervisorUserID uuid.UUID) (opsport.TeamSLASnapshot, error) {
	if s == nil || s.live == nil {
		return opsport.TeamSLASnapshot{}, nil
	}
	if s.repo != nil {
		row, err := s.repo.LatestByKind(ctx, "supervisor_team_sla", &supervisorUserID, nil)
		if err == nil && row != nil && time.Since(row.AggregatedAt) < s.staleAfter {
			if snap, ok := teamSnapshotFromPayload(row.Payload, supervisorUserID); ok {
				return snap, nil
			}
		}
	}
	return s.live.SupervisorTeamSLA(ctx, supervisorUserID)
}

// EscalationQueue returns the escalations visible to the requesting role.
func (s *CSDashboardService) EscalationQueue(ctx context.Context, minLevel int, limit int) ([]opsport.EscalationRow, error) {
	if s == nil || s.live == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 100
	}
	if minLevel <= 0 {
		minLevel = 2
	}
	return s.live.EscalationQueue(ctx, minLevel, limit)
}

// SatisfactionSummary returns CSAT aggregations for the requested window.
func (s *CSDashboardService) SatisfactionSummary(ctx context.Context, from, to time.Time) (opsport.SatisfactionSummary, error) {
	if s == nil || s.live == nil {
		return opsport.SatisfactionSummary{}, nil
	}
	return s.live.SatisfactionSummary(ctx, from, to)
}

// ChannelDistribution returns ticket counts by opened_via channel.
func (s *CSDashboardService) ChannelDistribution(ctx context.Context, from, to time.Time) (map[string]int, error) {
	if s == nil || s.live == nil {
		return nil, nil
	}
	return s.live.ChannelDistribution(ctx, from, to)
}

// PrecomputeTick is the cron entry point — computes the agent queue for
// each active agent, the supervisor team SLA per supervisor, plus the
// global escalation + satisfaction + channel distribution snapshots.
// Returns the count of rows precomputed.
func (s *CSDashboardService) PrecomputeTick(ctx context.Context) (int, error) {
	if s == nil || s.live == nil || s.repo == nil {
		return 0, nil
	}
	count := 0
	now := time.Now().UTC()
	from := now.Add(-24 * time.Hour)

	// Agents
	agents, err := s.live.ActiveAgentIDs(ctx, 200)
	if err == nil {
		for _, uid := range agents {
			snap, err := s.live.AgentQueue(ctx, uid)
			if err != nil {
				continue
			}
			payload := agentSnapshotToPayload(snap)
			scope := uid
			if err := s.repo.Create(ctx, "agent_queue", &scope, nil, &from, &now, payload); err == nil {
				count++
			}
		}
	}

	// Supervisors
	supervisors, err := s.live.SupervisorTeamIDs(ctx, 100)
	if err == nil {
		for _, sid := range supervisors {
			snap, err := s.live.SupervisorTeamSLA(ctx, sid)
			if err != nil {
				continue
			}
			payload := teamSnapshotToPayload(snap)
			scope := sid
			if err := s.repo.Create(ctx, "supervisor_team_sla", &scope, nil, &from, &now, payload); err == nil {
				count++
			}
		}
	}

	// Escalation queue (global)
	if escs, err := s.live.EscalationQueue(ctx, 2, 200); err == nil {
		payload := map[string]any{
			"items": escs,
			"count": len(escs),
		}
		if err := s.repo.Create(ctx, "escalation_queue", nil, nil, &from, &now, payload); err == nil {
			count++
		}
	}

	// Satisfaction summary (last 24h)
	if sat, err := s.live.SatisfactionSummary(ctx, from, now); err == nil {
		payload := map[string]any{
			"count":         sat.Count,
			"avg_rating":    sat.AvgRating,
			"nps_score":     sat.NPSScore,
			"critical_low":  sat.CriticalLow,
		}
		if err := s.repo.Create(ctx, "satisfaction_summary", nil, nil, &from, &now, payload); err == nil {
			count++
		}
	}

	// Channel distribution (last 24h)
	if dist, err := s.live.ChannelDistribution(ctx, from, now); err == nil {
		payload := map[string]any{
			"distribution": dist,
		}
		if err := s.repo.Create(ctx, "channel_distribution", nil, nil, &from, &now, payload); err == nil {
			count++
		}
	}

	if count > 0 {
		s.log.Info("cs dashboard precompute tick", "rows", count)
	}
	return count, nil
}

// snapshotFromPayload best-effort hydrates a precomputed payload back
// into the snapshot shape. Returns ok=false if the payload schema has
// drifted; the caller falls back to a live read.
func snapshotFromPayload(payload map[string]any, userID uuid.UUID) (opsport.AgentQueueSnapshot, bool) {
	if payload == nil {
		return opsport.AgentQueueSnapshot{}, false
	}
	snap := opsport.AgentQueueSnapshot{UserID: userID}
	snap.OpenAssigned = intFrom(payload["open_assigned"])
	snap.UnassignedAvailable = intFrom(payload["unassigned_available"])
	snap.PendingInternal = intFrom(payload["pending_internal"])
	snap.PendingCustomer = intFrom(payload["pending_customer"])
	snap.ResolvedToday = intFrom(payload["resolved_today"])
	snap.SLAAtRisk = intFrom(payload["sla_at_risk"])
	snap.SLABreached = intFrom(payload["sla_breached"])
	return snap, true
}

func teamSnapshotFromPayload(payload map[string]any, supervisorUserID uuid.UUID) (opsport.TeamSLASnapshot, bool) {
	if payload == nil {
		return opsport.TeamSLASnapshot{}, false
	}
	snap := opsport.TeamSLASnapshot{SupervisorUserID: supervisorUserID}
	snap.OpenCount = intFrom(payload["open_count"])
	snap.ResolvedLast24h = intFrom(payload["resolved_last_24h"])
	snap.BreachedFirstResp = intFrom(payload["breached_first_resp"])
	snap.BreachedResolve = intFrom(payload["breached_resolve"])
	snap.AvgFirstResponseMin = intFrom(payload["avg_first_response_min"])
	snap.AvgResolveMin = intFrom(payload["avg_resolve_min"])
	if v, ok := payload["compliance_pct"].(float64); ok {
		snap.CompliancePct = v
	}
	return snap, true
}

func agentSnapshotToPayload(s opsport.AgentQueueSnapshot) map[string]any {
	return map[string]any{
		"open_assigned":        s.OpenAssigned,
		"unassigned_available": s.UnassignedAvailable,
		"pending_internal":     s.PendingInternal,
		"pending_customer":     s.PendingCustomer,
		"resolved_today":       s.ResolvedToday,
		"sla_at_risk":          s.SLAAtRisk,
		"sla_breached":         s.SLABreached,
	}
}

func teamSnapshotToPayload(s opsport.TeamSLASnapshot) map[string]any {
	return map[string]any{
		"open_count":             s.OpenCount,
		"resolved_last_24h":      s.ResolvedLast24h,
		"breached_first_resp":    s.BreachedFirstResp,
		"breached_resolve":       s.BreachedResolve,
		"avg_first_response_min": s.AvgFirstResponseMin,
		"avg_resolve_min":        s.AvgResolveMin,
		"compliance_pct":         s.CompliancePct,
	}
}

func intFrom(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	default:
		return 0
	}
}
