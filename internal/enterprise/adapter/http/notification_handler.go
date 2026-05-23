package http

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/enterprise/domain"
	"github.com/ion-core/backend/internal/enterprise/port"
	"github.com/ion-core/backend/pkg/auth"
	"github.com/ion-core/backend/pkg/errors"
	"github.com/ion-core/backend/pkg/httpserver"
)

// NotificationHandler is the consumer-side surface for E10. Producer-
// side dispatch is internal to usecase.Service (via Notify()).
//
// Routes (all gated on enterprise.notification.read):
//
//   GET    /notifications              ?unread_only=true&page=&page_size=
//   POST   /notifications/{id}/read
//   POST   /notifications/read-all
//
// The actor is taken from the JWT subject so notifications are
// recipient-scoped without explicit user IDs in URLs.
type NotificationHandler struct {
	uc       port.NotificationUseCase
	verifier *auth.Verifier
}

func NewNotificationHandler(uc port.NotificationUseCase, verifier *auth.Verifier) *NotificationHandler {
	return &NotificationHandler{uc: uc, verifier: verifier}
}

func (h *NotificationHandler) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(httpserver.RequireAuth(h.verifier))

		// Legacy paths — kept for back-compat with Wave 30 clients.
		r.With(httpserver.RequirePermission("enterprise.notification.read")).
			Get("/notifications", h.list)
		r.With(httpserver.RequirePermission("enterprise.notification.read")).
			Post("/notifications/{id}/read", h.markRead)
		r.With(httpserver.RequirePermission("enterprise.notification.read")).
			Post("/notifications/read-all", h.markAllRead)

		// Wave 107 — canonical paths from the audit spec.
		//   GET    /notifications/inbox          ?read=true|false&page=&page_size=
		//   POST   /notifications/mark-all-read  alias of /notifications/read-all
		//
		// `/notifications/{id}/read` already matches the spec.
		r.With(httpserver.RequirePermission("enterprise.notification.read")).
			Get("/notifications/inbox", h.list)
		r.With(httpserver.RequirePermission("enterprise.notification.read")).
			Post("/notifications/mark-all-read", h.markAllRead)

		// Preferences (per-user mute toggles).
		r.With(httpserver.RequirePermission("enterprise.notification_pref.manage")).
			Get("/notification-prefs", h.listPrefs)
		r.With(httpserver.RequirePermission("enterprise.notification_pref.manage")).
			Put("/notification-prefs", h.upsertPref)
		r.With(httpserver.RequirePermission("enterprise.notification_pref.manage")).
			Delete("/notification-prefs", h.deletePref)
	})
}

func (h *NotificationHandler) listPrefs(w http.ResponseWriter, r *http.Request) {
	actor := actorUserIDLocal(r.Context())
	if actor == nil {
		httpserver.WriteError(w, errors.Forbidden("notification_pref.actor_required", "actor required"))
		return
	}
	type prefHandler interface {
		ListMyNotificationPrefs(ctx context.Context, userID uuid.UUID) ([]port.NotificationPref, error)
	}
	uc, ok := h.uc.(prefHandler)
	if !ok {
		httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": []any{}})
		return
	}
	prefs, err := uc.ListMyNotificationPrefs(r.Context(), *actor)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	items := make([]map[string]any, 0, len(prefs))
	for _, p := range prefs {
		items = append(items, map[string]any{
			"kind":    p.Kind,
			"enabled": p.Enabled,
		})
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *NotificationHandler) upsertPref(w http.ResponseWriter, r *http.Request) {
	actor := actorUserIDLocal(r.Context())
	if actor == nil {
		httpserver.WriteError(w, errors.Forbidden("notification_pref.actor_required", "actor required"))
		return
	}
	var req struct {
		Kind    string `json:"kind"`
		Enabled bool   `json:"enabled"`
	}
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	if req.Kind == "" {
		httpserver.WriteError(w, errors.Validation("notification_pref.kind_required", "kind is required"))
		return
	}
	type prefHandler interface {
		UpsertNotificationPref(ctx context.Context, pref port.NotificationPref) error
	}
	uc, ok := h.uc.(prefHandler)
	if !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := uc.UpsertNotificationPref(r.Context(), port.NotificationPref{
		UserID: *actor, Kind: req.Kind, Enabled: req.Enabled,
	}); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *NotificationHandler) deletePref(w http.ResponseWriter, r *http.Request) {
	actor := actorUserIDLocal(r.Context())
	if actor == nil {
		httpserver.WriteError(w, errors.Forbidden("notification_pref.actor_required", "actor required"))
		return
	}
	kind := r.URL.Query().Get("kind")
	if kind == "" {
		httpserver.WriteError(w, errors.Validation("notification_pref.kind_required", "?kind= is required"))
		return
	}
	type prefHandler interface {
		DeleteNotificationPref(ctx context.Context, userID uuid.UUID, kind string) error
	}
	uc, ok := h.uc.(prefHandler)
	if !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := uc.DeleteNotificationPref(r.Context(), *actor, kind); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *NotificationHandler) list(w http.ResponseWriter, r *http.Request) {
	actor := actorUserIDLocal(r.Context())
	if actor == nil {
		httpserver.WriteError(w, errors.Forbidden("notification.actor_required", "actor required"))
		return
	}
	q := r.URL.Query()
	page := parseIntDefaultLocal(q.Get("page"), 1)
	if page < 1 {
		page = 1
	}
	pageSize := parseIntDefaultLocal(q.Get("page_size"), 50)
	// Wave 107 — accept both `unread_only=true` (legacy) and
	// `read=false` (spec). `read=true` is the inverse — return ONLY
	// read items — which the usecase doesn't model directly; for that
	// path we just call the unfiltered list and filter in the DTO loop.
	unreadOnly := q.Get("unread_only") == "true" || q.Get("read") == "false"
	readOnly := q.Get("read") == "true"
	items, total, unread, err := h.uc.ListMyNotifications(r.Context(), port.ListNotificationsInput{
		RecipientUserID: *actor,
		UnreadOnly:      unreadOnly,
		Limit:           pageSize,
		Offset:          (page - 1) * pageSize,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]notificationDTO, 0, len(items))
	for _, n := range items {
		// Wave 107 — when `read=true` was requested, skip unread rows
		// here (the usecase doesn't model "read only" natively).
		if readOnly && n.ReadAt == nil {
			continue
		}
		out = append(out, toNotificationDTO(n))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"items":     out,
		"total":     total,
		"unread":    unread,
		"page":      page,
		"page_size": pageSize,
	})
}

func (h *NotificationHandler) markRead(w http.ResponseWriter, r *http.Request) {
	actor := actorUserIDLocal(r.Context())
	if actor == nil {
		httpserver.WriteError(w, errors.Forbidden("notification.actor_required", "actor required"))
		return
	}
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "notification")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	if err := h.uc.MarkNotificationRead(r.Context(), id, *actor); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *NotificationHandler) markAllRead(w http.ResponseWriter, r *http.Request) {
	actor := actorUserIDLocal(r.Context())
	if actor == nil {
		httpserver.WriteError(w, errors.Forbidden("notification.actor_required", "actor required"))
		return
	}
	if err := h.uc.MarkAllNotificationsRead(r.Context(), *actor); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type notificationDTO struct {
	ID          string  `json:"id"`
	Kind        string  `json:"kind"`
	SubjectType string  `json:"subject_type"`
	SubjectID   string  `json:"subject_id"`
	Title       string  `json:"title"`
	Body        string  `json:"body"`
	Severity    string  `json:"severity"`
	ReadAt      *string `json:"read_at,omitempty"`
	CreatedAt   string  `json:"created_at"`
}

func toNotificationDTO(n domain.Notification) notificationDTO {
	return notificationDTO{
		ID:          n.ID.String(),
		Kind:        n.Kind,
		SubjectType: n.SubjectType,
		SubjectID:   n.SubjectID.String(),
		Title:       n.Title,
		Body:        n.Body,
		Severity:    string(n.Severity),
		ReadAt:      rfc3339Ptr(n.ReadAt),
		CreatedAt:   rfc3339(n.CreatedAt),
	}
}
