// Wave 126 — CS Dashboard backend aggregation routes.
//
// The CS Web (Agent) and Web (Supervisor) dashboards used to compute
// everything client-side from flat ticket lists. This file gives them
// dedicated aggregation routes (per the audit's TC-CSD- gaps) so the
// numbers are canonical and the p95<2s NFR is achievable via the cron
// precompute path.
//
// Routes are mounted in NewHandlerWave126.MountWave126() so existing
// cs-svc deployments aren't disturbed — wiring is opt-in.
package http

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	csusecase "github.com/ion-core/backend/internal/cs/usecase"
	"github.com/ion-core/backend/pkg/errors"
	"github.com/ion-core/backend/pkg/httpserver"
)

// WithCSDashboards wires the dashboard service.
func (h *Handler) WithCSDashboards(d *csusecase.CSDashboardService) *Handler {
	h.dashboard = d
	return h
}

// dashboard is the Wave 126 add-on field; declared on Handler in
// dashboard_field.go so the existing constructor stays untouched.

// MountWave126 attaches the CS dashboard routes. Caller mounts after
// the main Mount() so the parent /api/cs route group is in place.
func (h *Handler) MountWave126(r chi.Router) {
	if h.dashboard == nil {
		return
	}
	r.Route("/api/cs/dashboards", func(r chi.Router) {
		r.Use(httpserver.RequireAuth(h.verifier))
		r.With(httpserver.RequirePermission("cs.dashboard.agent_queue.read")).
			Get("/agent-queue", h.dashAgentQueue)
		r.With(httpserver.RequirePermission("cs.dashboard.team_sla.read")).
			Get("/team-sla/{team_id}", h.dashTeamSLA)
		r.With(httpserver.RequirePermission("cs.dashboard.escalation.read")).
			Get("/escalation-queue", h.dashEscalations)
		r.With(httpserver.RequirePermission("cs.dashboard.team_sla.read")).
			Get("/satisfaction-summary", h.dashSatisfaction)
		r.With(httpserver.RequirePermission("cs.dashboard.team_sla.read")).
			Get("/channel-distribution", h.dashChannels)
	})
}

func (h *Handler) dashAgentQueue(w http.ResponseWriter, r *http.Request) {
	userID := actorUserID(r.Context())
	if userID == uuid.Nil {
		writeErr(w, errors.Unauthorized("auth.missing", "authentication required"))
		return
	}
	// Optional ?user_id= for supervisor drill-in (RBAC-gated separately).
	if alt := r.URL.Query().Get("user_id"); alt != "" {
		if uid, perr := uuid.Parse(alt); perr == nil {
			userID = uid
		}
	}
	snap, err := h.dashboard.AgentQueue(r.Context(), userID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

func (h *Handler) dashTeamSLA(w http.ResponseWriter, r *http.Request) {
	raw := chi.URLParam(r, "team_id")
	teamID, perr := uuid.Parse(raw)
	if perr != nil {
		writeErr(w, errors.Validation("cs.dashboard.team_id_invalid", "team_id must be a uuid"))
		return
	}
	// The TeamSLASnapshot is supervisor-keyed; the supervisor lookup
	// happens inside the live reader. For now, treat the team_id path
	// param as the supervisor's user id when team_id == supervisor's
	// effective user id; the caller will use the supervisor's own user
	// id via /api/cs/dashboards/team-sla/{my-user-id}.
	snap, err := h.dashboard.SupervisorTeamSLA(r.Context(), teamID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

func (h *Handler) dashEscalations(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	minLevel := httpserver.ParseIntDefault(q.Get("min_level"), 2)
	limit := httpserver.ParseIntDefault(q.Get("limit"), 100)
	rows, err := h.dashboard.EscalationQueue(r.Context(), minLevel, limit)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": rows,
		"count": len(rows),
	})
}

func (h *Handler) dashSatisfaction(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from, ferr := parseTimeRangeCS(q.Get("from"), time.Now().AddDate(0, 0, -7))
	if ferr != nil {
		writeErr(w, ferr)
		return
	}
	to, terr := parseTimeRangeCS(q.Get("to"), time.Now())
	if terr != nil {
		writeErr(w, terr)
		return
	}
	sum, err := h.dashboard.SatisfactionSummary(r.Context(), from, to)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sum)
}

func (h *Handler) dashChannels(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from, ferr := parseTimeRangeCS(q.Get("from"), time.Now().AddDate(0, 0, -7))
	if ferr != nil {
		writeErr(w, ferr)
		return
	}
	to, terr := parseTimeRangeCS(q.Get("to"), time.Now())
	if terr != nil {
		writeErr(w, terr)
		return
	}
	dist, err := h.dashboard.ChannelDistribution(r.Context(), from, to)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"distribution": dist})
}

func parseTimeRangeCS(raw string, def time.Time) (time.Time, error) {
	if strings.TrimSpace(raw) == "" {
		return def, nil
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t, nil
	}
	if t, err := time.Parse("2006-01-02", raw); err == nil {
		return t, nil
	}
	return time.Time{}, errors.Validation("cs.dashboard.bad_time", "time must be RFC3339 or YYYY-MM-DD")
}
