// Wave 126 — HTTP routes for maintenance enhancements + internal
// announcement dispatcher + operational calendar + cross-module SLA Ops
// view.
//
// We add a sibling handler (Wave126Handler) rather than enlarging the
// existing flat Handler — keeps the Wave 65 surface untouched and lets
// cmd/field-svc/main.go opt in by wiring the service deps.
package http

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/operations/domain"
	"github.com/ion-core/backend/internal/operations/usecase"
	"github.com/ion-core/backend/pkg/auth"
	"github.com/ion-core/backend/pkg/errors"
	"github.com/ion-core/backend/pkg/httpserver"
)

// Wave126Handler is the new operations surface. Each service is nil-safe
// — the corresponding route group only mounts when wired.
type Wave126Handler struct {
	verifier    *auth.Verifier
	maintenance *usecase.MaintenanceService
	announce    *usecase.AnnouncementService
	calendar    *usecase.CalendarService
	xmodSLA     *usecase.CrossModuleSLAService
}

// NewWave126Handler builds the sibling handler.
func NewWave126Handler(verifier *auth.Verifier) *Wave126Handler {
	return &Wave126Handler{verifier: verifier}
}

// WithMaintenance wires the MaintenanceService.
func (h *Wave126Handler) WithMaintenance(s *usecase.MaintenanceService) *Wave126Handler {
	h.maintenance = s
	return h
}

// WithAnnouncements wires the AnnouncementService.
func (h *Wave126Handler) WithAnnouncements(s *usecase.AnnouncementService) *Wave126Handler {
	h.announce = s
	return h
}

// WithCalendar wires the CalendarService.
func (h *Wave126Handler) WithCalendar(s *usecase.CalendarService) *Wave126Handler {
	h.calendar = s
	return h
}

// WithCrossModuleSLA wires the CrossModuleSLAService.
func (h *Wave126Handler) WithCrossModuleSLA(s *usecase.CrossModuleSLAService) *Wave126Handler {
	h.xmodSLA = s
	return h
}

// Mount attaches the Wave 126 routes to the given chi router. The
// api-gateway strips `/api/operations` (and `/api/ops`) before forwarding
// to field-svc, so we mount at root.
func (h *Wave126Handler) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(httpserver.RequireAuth(h.verifier))

		if h.maintenance != nil {
			r.With(httpserver.RequirePermission("field.maintenance.read")).
				Get("/maintenance/{id}/affected-customers", h.listAffectedCustomers)
			r.With(httpserver.RequirePermission("operations.maintenance.approve")).
				Post("/maintenance/{id}/request-approval", h.requestApproval)
			r.With(httpserver.RequirePermission("operations.maintenance.approve")).
				Post("/maintenance/{id}/approve", h.approveMaintenance)
			r.With(httpserver.RequirePermission("operations.maintenance.escalate")).
				Post("/maintenance/{id}/escalate", h.escalateMaintenanceW126)
			r.With(httpserver.RequirePermission("field.maintenance.read")).
				Get("/maintenance/{id}/escalations", h.listEscalations)
		}
		if h.announce != nil {
			r.With(httpserver.RequirePermission("operations.announcement.dispatch")).
				Post("/announcements/{id}/dispatch", h.dispatchAnnouncement)
			r.With(httpserver.RequirePermission("operations.announcement.read")).
				Get("/announcements/{id}/recipients", h.listAnnouncementRecipients)
			r.With(httpserver.RequirePermission("operations.announcement.read")).
				Get("/announcements/inbox", h.announcementInbox)
			r.With(httpserver.RequirePermission("operations.announcement.read")).
				Post("/announcements/{id}/read", h.markAnnouncementRead)
		}
		if h.calendar != nil {
			r.With(httpserver.RequirePermission("operations.calendar.read")).
				Get("/calendar/events", h.calendarRange)
			r.With(httpserver.RequirePermission("operations.calendar.write")).
				Post("/calendar/events", h.createCalendarEvent)
			r.With(httpserver.RequirePermission("operations.calendar.write")).
				Post("/calendar/sync", h.calendarSync)
		}
		if h.xmodSLA != nil {
			r.With(httpserver.RequirePermission("ops.sla.cross_module_view.read")).
				Get("/sla/cross-module/latest", h.xmodSLALatest)
			r.With(httpserver.RequirePermission("ops.sla.cross_module_view.read")).
				Get("/sla/cross-module/{module}/history", h.xmodSLAHistory)
		}
	})
}

// =====================================================================
// Maintenance handlers
// =====================================================================

func (h *Wave126Handler) listAffectedCustomers(w http.ResponseWriter, r *http.Request) {
	id, err := parseURLUUID(r, "id")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	rows, err := h.maintenance.ListAffected(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, c := range rows {
		out = append(out, map[string]any{
			"id":                   c.ID,
			"customer_id":          c.CustomerID,
			"customer_segment":     string(c.CustomerSegment),
			"notified_at":          c.NotifiedAt,
			"notification_channel": c.NotificationChannel,
			"error_msg":            c.ErrorMsg,
		})
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (h *Wave126Handler) requestApproval(w http.ResponseWriter, r *http.Request) {
	id, err := parseURLUUID(r, "id")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	// Re-materialize affected customers (re-runs are idempotent), then
	// surface the threshold result so the caller knows whether approval
	// is required.
	total, err := h.maintenance.MaterializeAffectedCustomers(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"event_id":          id,
		"affected_customers": total,
	})
}

func (h *Wave126Handler) approveMaintenance(w http.ResponseWriter, r *http.Request) {
	id, err := parseURLUUID(r, "id")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	userID := claimsUserID(r)
	if userID == uuid.Nil {
		httpserver.WriteError(w, errors.Unauthorized("auth.missing", "authentication required"))
		return
	}
	if err := h.maintenance.Approve(r.Context(), id, userID); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"event_id":    id,
		"approved_by": userID,
	})
}

type escalateRequestW126 struct {
	Reason            string `json:"reason"`
	EscalatedToUserID string `json:"escalated_to_user_id,omitempty"`
}

func (h *Wave126Handler) escalateMaintenanceW126(w http.ResponseWriter, r *http.Request) {
	id, err := parseURLUUID(r, "id")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var req escalateRequestW126
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpserver.WriteError(w, errors.Validation("operations.maintenance.bad_body", "invalid body"))
		return
	}
	var assigned *uuid.UUID
	if strings.TrimSpace(req.EscalatedToUserID) != "" {
		uid, perr := uuid.Parse(req.EscalatedToUserID)
		if perr != nil {
			httpserver.WriteError(w, errors.Validation("operations.maintenance.bad_user", "escalated_to_user_id invalid"))
			return
		}
		assigned = &uid
	}
	esc, err := h.maintenance.EscalateOverrun(r.Context(), id, req.Reason, assigned)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"id":                    esc.ID,
		"maintenance_event_id":  esc.MaintenanceEventID,
		"level":                 esc.Level,
		"reason":                esc.Reason,
		"escalated_to_user_id":  esc.EscalatedToUserID,
		"escalated_at":          esc.EscalatedAt,
	})
}

func (h *Wave126Handler) listEscalations(w http.ResponseWriter, r *http.Request) {
	id, err := parseURLUUID(r, "id")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	rows, err := h.maintenance.ListEscalations(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, e := range rows {
		out = append(out, map[string]any{
			"id":                    e.ID,
			"level":                 e.Level,
			"reason":                e.Reason,
			"escalated_to_user_id":  e.EscalatedToUserID,
			"escalated_at":          e.EscalatedAt,
			"acknowledged_at":       e.AcknowledgedAt,
			"resolved_at":           e.ResolvedAt,
		})
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}

// =====================================================================
// Announcement handlers
// =====================================================================

func (h *Wave126Handler) dispatchAnnouncement(w http.ResponseWriter, r *http.Request) {
	id, err := parseURLUUID(r, "id")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	a, err := h.announce.DispatchOne(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"id":               a.ID,
		"dispatch_status":  string(a.DispatchStatus),
		"sent_count":       a.SentCount,
		"dispatched_at":    a.DispatchedAt,
	})
}

func (h *Wave126Handler) listAnnouncementRecipients(w http.ResponseWriter, r *http.Request) {
	id, err := parseURLUUID(r, "id")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	rows, err := h.announce.ListRecipients(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, rec := range rows {
		out = append(out, map[string]any{
			"id":            rec.ID,
			"user_id":       rec.UserID,
			"delivered_at":  rec.DeliveredAt,
			"read_at":       rec.ReadAt,
			"channel":       rec.Channel,
			"error_msg":     rec.ErrorMsg,
		})
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (h *Wave126Handler) announcementInbox(w http.ResponseWriter, r *http.Request) {
	userID := claimsUserID(r)
	if userID == uuid.Nil {
		httpserver.WriteError(w, errors.Unauthorized("auth.missing", "authentication required"))
		return
	}
	q := r.URL.Query()
	unreadOnly := q.Get("unread") == "true"
	limit := httpserver.ParseIntDefault(q.Get("limit"), 100)
	rows, err := h.announce.ListMyInbox(r.Context(), userID, unreadOnly, limit)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, e := range rows {
		out = append(out, map[string]any{
			"recipient_id":    e.RecipientID,
			"announcement_id": e.AnnouncementID,
			"title":           e.Title,
			"body":            e.Body,
			"severity":        string(e.Severity),
			"delivered_at":    e.DeliveredAt,
			"read_at":         e.ReadAt,
			"created_at":      e.CreatedAt,
		})
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (h *Wave126Handler) markAnnouncementRead(w http.ResponseWriter, r *http.Request) {
	id, err := parseURLUUID(r, "id")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	userID := claimsUserID(r)
	if userID == uuid.Nil {
		httpserver.WriteError(w, errors.Unauthorized("auth.missing", "authentication required"))
		return
	}
	if err := h.announce.MarkRead(r.Context(), id, userID); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// =====================================================================
// Calendar handlers
// =====================================================================

type createCalendarEventReq struct {
	EventKind   string         `json:"event_kind"`
	Title       string         `json:"title"`
	Description string         `json:"description,omitempty"`
	Scope       string         `json:"scope,omitempty"`
	ScopeID     string         `json:"scope_id,omitempty"`
	AllDay      bool           `json:"all_day,omitempty"`
	StartsAt    time.Time      `json:"starts_at"`
	EndsAt      *time.Time     `json:"ends_at,omitempty"`
	ColorHex    string         `json:"color_hex,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

func (h *Wave126Handler) createCalendarEvent(w http.ResponseWriter, r *http.Request) {
	userID := claimsUserID(r)
	if userID == uuid.Nil {
		httpserver.WriteError(w, errors.Unauthorized("auth.missing", "authentication required"))
		return
	}
	var req createCalendarEventReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpserver.WriteError(w, errors.Validation("operations.calendar.bad_body", "invalid body"))
		return
	}
	var scopeID *uuid.UUID
	if strings.TrimSpace(req.ScopeID) != "" {
		sid, perr := uuid.Parse(req.ScopeID)
		if perr != nil {
			httpserver.WriteError(w, errors.Validation("operations.calendar.bad_scope_id", "scope_id invalid"))
			return
		}
		scopeID = &sid
	}
	ev, err := h.calendar.CreateEvent(r.Context(), usecase.CreateEventInput{
		EventKind:   domain.EventKind(req.EventKind),
		Title:       req.Title,
		Description: req.Description,
		Scope:       domain.EventScope(req.Scope),
		ScopeID:     scopeID,
		AllDay:      req.AllDay,
		StartsAt:    req.StartsAt,
		EndsAt:      req.EndsAt,
		ColorHex:    req.ColorHex,
		Metadata:    req.Metadata,
		CreatedBy:   userID,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, calendarEventDTO(ev))
}

func (h *Wave126Handler) calendarRange(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from, ferr := parseTimeOrDefault(q.Get("from"), time.Now().AddDate(0, 0, -7))
	if ferr != nil {
		httpserver.WriteError(w, ferr)
		return
	}
	to, terr := parseTimeOrDefault(q.Get("to"), time.Now().AddDate(0, 1, 0))
	if terr != nil {
		httpserver.WriteError(w, terr)
		return
	}
	scope := domain.NormalizeScope(q.Get("scope"))
	var scopeID *uuid.UUID
	if s := q.Get("scope_id"); s != "" {
		sid, perr := uuid.Parse(s)
		if perr != nil {
			httpserver.WriteError(w, errors.Validation("operations.calendar.bad_scope_id", "scope_id invalid"))
			return
		}
		scopeID = &sid
	}
	limit := httpserver.ParseIntDefault(q.Get("limit"), 500)
	events, err := h.calendar.ListInRange(r.Context(), from, to, scope, scopeID, limit)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(events))
	for i := range events {
		out = append(out, calendarEventDTO(&events[i]))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (h *Wave126Handler) calendarSync(w http.ResponseWriter, r *http.Request) {
	n, err := h.calendar.AutoSync(r.Context())
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"synced": n})
}

func calendarEventDTO(e *domain.CalendarEvent) map[string]any {
	return map[string]any{
		"id":           e.ID,
		"event_kind":   string(e.EventKind),
		"event_source": string(e.EventSource),
		"source_id":    e.SourceID,
		"title":        e.Title,
		"description":  e.Description,
		"scope":        string(e.Scope),
		"scope_id":     e.ScopeID,
		"all_day":      e.AllDay,
		"starts_at":    e.StartsAt,
		"ends_at":      e.EndsAt,
		"color_hex":    e.ColorHex,
		"metadata":     e.Metadata,
		"created_by":   e.CreatedBy,
		"created_at":   e.CreatedAt,
		"updated_at":   e.UpdatedAt,
	}
}

// =====================================================================
// Cross-Module SLA handlers
// =====================================================================

func (h *Wave126Handler) xmodSLALatest(w http.ResponseWriter, r *http.Request) {
	view, err := h.xmodSLA.LatestUnified(r.Context())
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, view)
}

func (h *Wave126Handler) xmodSLAHistory(w http.ResponseWriter, r *http.Request) {
	module := domain.SLAModule(strings.ToLower(chi.URLParam(r, "module")))
	q := r.URL.Query()
	from, ferr := parseTimeOrDefault(q.Get("from"), time.Now().AddDate(0, 0, -7))
	if ferr != nil {
		httpserver.WriteError(w, ferr)
		return
	}
	to, terr := parseTimeOrDefault(q.Get("to"), time.Now())
	if terr != nil {
		httpserver.WriteError(w, terr)
		return
	}
	limit := httpserver.ParseIntDefault(q.Get("limit"), 100)
	rows, err := h.xmodSLA.History(r.Context(), module, from, to, limit)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": rows})
}

// =====================================================================
// helpers
// =====================================================================

func parseURLUUID(r *http.Request, key string) (uuid.UUID, error) {
	raw := chi.URLParam(r, key)
	if raw == "" {
		return uuid.Nil, errors.Validation("operations."+key+"_required", key+" is required")
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, errors.Validation("operations."+key+"_invalid", key+" must be a uuid")
	}
	return id, nil
}

func parseTimeOrDefault(raw string, def time.Time) (time.Time, error) {
	if raw == "" {
		return def, nil
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t, nil
	}
	if t, err := time.Parse("2006-01-02", raw); err == nil {
		return t, nil
	}
	return time.Time{}, errors.Validation("operations.bad_time", "time must be RFC3339 or YYYY-MM-DD")
}

func claimsUserID(r *http.Request) uuid.UUID {
	c := httpserver.ClaimsFromContext(r.Context())
	if c == nil {
		return uuid.Nil
	}
	return c.UserID
}
