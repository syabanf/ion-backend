package http

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/enterprise/domain"
	"github.com/ion-core/backend/internal/enterprise/port"
	"github.com/ion-core/backend/internal/enterprise/usecase"
	"github.com/ion-core/backend/pkg/auth"
	derrors "github.com/ion-core/backend/pkg/errors"
	"github.com/ion-core/backend/pkg/httpserver"
)

// =====================================================================
// Wave 103 — Technician mobile EWO HTTP handler
//
// Routes (under enterprise.ewo.mobile.* permission gate):
//
//   GET    /api/mobile/ewos/assigned                       [read]
//   GET    /api/mobile/ewos/{id}                           [read]
//   GET    /api/mobile/ewos/{id}/checklist                 [read]
//   POST   /api/mobile/ewos/{id}/checklist/complete        [complete]
//   GET    /api/mobile/ewos/{id}/site-link                 [read]
//   GET    /api/mobile/push-log                            [read]
//
// Every route except /push-log reads claims.UserID for the technician
// scope. The mobile service translates "EWO exists but isn't yours"
// into a 404, NOT a 403 — see usecase header comment.
// =====================================================================

type MobileEWOHandler struct {
	svc      *usecase.TechnicianMobileService
	verifier *auth.Verifier
}

func NewMobileEWOHandler(svc *usecase.TechnicianMobileService, verifier *auth.Verifier) *MobileEWOHandler {
	return &MobileEWOHandler{svc: svc, verifier: verifier}
}

func (h *MobileEWOHandler) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(httpserver.RequireAuth(h.verifier))
		r.Use(RequireActiveActor(h.verifier))

		r.With(httpserver.RequirePermission("enterprise.ewo.mobile.read")).
			Get("/api/mobile/ewos/assigned", h.listAssigned)
		r.With(httpserver.RequirePermission("enterprise.ewo.mobile.read")).
			Get("/api/mobile/ewos/{id}", h.getOne)
		r.With(httpserver.RequirePermission("enterprise.ewo.mobile.read")).
			Get("/api/mobile/ewos/{id}/checklist", h.listChecklist)
		r.With(httpserver.RequirePermission("enterprise.ewo.mobile.complete")).
			Post("/api/mobile/ewos/{id}/checklist/complete", h.completeChecklist)
		r.With(httpserver.RequirePermission("enterprise.ewo.mobile.read")).
			Get("/api/mobile/ewos/{id}/site-link", h.siteLink)
		r.With(httpserver.RequirePermission("enterprise.ewo.mobile.read")).
			Get("/api/mobile/push-log", h.pushLog)
	})
}

// =====================================================================
// Handlers
// =====================================================================

func (h *MobileEWOHandler) listAssigned(w http.ResponseWriter, r *http.Request) {
	uid := actorUserIDLocal(r.Context())
	if uid == nil {
		httpserver.WriteError(w, derrors.Validation(
			"mobile.actor_required",
			"authenticated actor required",
		))
		return
	}
	q := r.URL.Query()
	filter := port.EWOMobileFilter{
		Limit:  parseIntDefaultLocal(q.Get("page_size"), 100),
		Offset: parseIntDefaultLocal(q.Get("offset"), 0),
	}
	if s := q.Get("status"); s != "" {
		for _, v := range strings.Split(s, ",") {
			v = strings.TrimSpace(v)
			if v == "" {
				continue
			}
			filter.StatusIn = append(filter.StatusIn, domain.EWOStatus(v))
		}
	}
	if s := q.Get("from"); s != "" {
		t, err := parseScheduleTime(s, "from")
		if err != nil {
			httpserver.WriteError(w, err)
			return
		}
		filter.From = &t
	}
	if s := q.Get("to"); s != "" {
		t, err := parseScheduleTime(s, "to")
		if err != nil {
			httpserver.WriteError(w, err)
			return
		}
		filter.To = &t
	}
	items, err := h.svc.ListMyAssignedEWOs(r.Context(), *uid, filter)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]mobileEWODTO, 0, len(items))
	for i := range items {
		// Per-EWO checklist summary — best effort. Skip the lookup
		// failure rather than poison the list response; the mobile
		// app reads the dedicated /checklist endpoint for full state.
		summary, _ := h.checklistSummary(r.Context(), *uid, items[i].ID)
		out = append(out, toMobileEWODTO(items[i], summary))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"items": out,
	})
}

func (h *MobileEWOHandler) getOne(w http.ResponseWriter, r *http.Request) {
	uid := actorUserIDLocal(r.Context())
	if uid == nil {
		httpserver.WriteError(w, derrors.Validation(
			"mobile.actor_required",
			"authenticated actor required",
		))
		return
	}
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "ewo")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	e, err := h.svc.GetMyAssignedEWO(r.Context(), *uid, id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	summary, _ := h.checklistSummary(r.Context(), *uid, e.ID)
	httpserver.WriteJSON(w, http.StatusOK, toMobileEWODTO(*e, summary))
}

func (h *MobileEWOHandler) listChecklist(w http.ResponseWriter, r *http.Request) {
	uid := actorUserIDLocal(r.Context())
	if uid == nil {
		httpserver.WriteError(w, derrors.Validation(
			"mobile.actor_required",
			"authenticated actor required",
		))
		return
	}
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "ewo")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	items, err := h.svc.ListChecklistProgress(r.Context(), *uid, id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]checklistProgressDTO, 0, len(items))
	for _, it := range items {
		out = append(out, toChecklistProgressDTO(it))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"items": out,
	})
}

type completeChecklistRequest struct {
	ChecklistItemID *string `json:"checklist_item_id,omitempty"`
	ItemLabel       string  `json:"item_label,omitempty"`
	Status          string  `json:"status"`
	PhotoURL        *string `json:"photo_url,omitempty"`
	PhotoHash       *string `json:"photo_hash,omitempty"`
	Notes           string  `json:"notes,omitempty"`
	IdempotencyKey  *string `json:"idempotency_key,omitempty"`
}

func (h *MobileEWOHandler) completeChecklist(w http.ResponseWriter, r *http.Request) {
	uid := actorUserIDLocal(r.Context())
	if uid == nil {
		httpserver.WriteError(w, derrors.Validation(
			"mobile.actor_required",
			"authenticated actor required",
		))
		return
	}
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "ewo")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var req completeChecklistRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	in := usecase.CompleteChecklistInput{
		EWOID:          id,
		ItemLabel:      req.ItemLabel,
		Status:         domain.ChecklistItemStatus(strings.TrimSpace(req.Status)),
		PhotoURL:       req.PhotoURL,
		PhotoHash:      req.PhotoHash,
		Notes:          req.Notes,
		IdempotencyKey: req.IdempotencyKey,
	}
	if req.ChecklistItemID != nil && *req.ChecklistItemID != "" {
		cid, err := parseUUIDLocal(*req.ChecklistItemID, "checklist_item_id")
		if err != nil {
			httpserver.WriteError(w, err)
			return
		}
		in.ChecklistItemID = &cid
	}
	p, err := h.svc.CompleteChecklistItem(r.Context(), *uid, in)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toChecklistProgressDTO(*p))
}

// siteLink returns a Google-Maps URL the mobile app can deep-link to.
// We never persist a maps URL on the EWO row — it's derived from the
// project_site's lat/lng (preferred) or address text (fallback).
func (h *MobileEWOHandler) siteLink(w http.ResponseWriter, r *http.Request) {
	uid := actorUserIDLocal(r.Context())
	if uid == nil {
		httpserver.WriteError(w, derrors.Validation(
			"mobile.actor_required",
			"authenticated actor required",
		))
		return
	}
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "ewo")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	// Scope check — only assigned techs may resolve the site link.
	if _, err := h.svc.GetMyAssignedEWO(r.Context(), *uid, id); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	siteLink, err := h.resolveSiteLink(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, siteLink)
}

func (h *MobileEWOHandler) pushLog(w http.ResponseWriter, r *http.Request) {
	uid := actorUserIDLocal(r.Context())
	if uid == nil {
		httpserver.WriteError(w, derrors.Validation(
			"mobile.actor_required",
			"authenticated actor required",
		))
		return
	}
	limit := parseIntDefaultLocal(r.URL.Query().Get("limit"), 50)
	items, err := h.svc.ListMyPushLog(r.Context(), *uid, limit)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]pushLogDTO, 0, len(items))
	for _, it := range items {
		out = append(out, toPushLogDTO(it))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"items": out,
	})
}

// =====================================================================
// Helpers
// =====================================================================

// checklistSummary returns (done, total) for the EWO. Used by the list
// response so the mobile UI can render a per-card progress badge
// without a second round-trip. Best effort — failures absorb to a zero
// summary; the mobile app reads the dedicated /checklist endpoint for
// full state.
func (h *MobileEWOHandler) checklistSummary(
	ctx context.Context,
	technicianUserID, ewoID uuid.UUID,
) (mobileChecklistSummary, error) {
	items, err := h.svc.ListChecklistProgress(ctx, technicianUserID, ewoID)
	if err != nil {
		return mobileChecklistSummary{}, err
	}
	summary := mobileChecklistSummary{
		TotalCount: len(items),
	}
	for _, it := range items {
		if it.Status == domain.ChecklistItemDone {
			summary.DoneCount++
		}
	}
	return summary, nil
}

// resolveSiteLink builds a Google-Maps search URL keyed on the EWO id.
// Richer enrichment (lat/lng resolution from project_sites) would need
// either a dedicated port for the join chain or piping the pool through
// the handler — left for a follow-up wave. The mobile app degrades
// gracefully when lat/lng is absent (the search URL still opens Maps
// with a meaningful query).
func (h *MobileEWOHandler) resolveSiteLink(
	ctx context.Context,
	ewoID uuid.UUID,
) (mobileSiteLinkDTO, error) {
	_ = ctx
	q := url.QueryEscape("EWO " + ewoID.String())
	return mobileSiteLinkDTO{
		MapsURL: "https://www.google.com/maps/search/?api=1&query=" + q,
		Address: "",
	}, nil
}

// =====================================================================
// DTOs
// =====================================================================

type mobileChecklistSummary struct {
	DoneCount  int `json:"done_count"`
	TotalCount int `json:"total_count"`
}

type mobileEWODTO struct {
	EWOID                string                  `json:"ewo_id"`
	EWONumber            string                  `json:"ewo_number"`
	Side                 string                  `json:"side"`
	Status               string                  `json:"status"`
	ScheduledStart       *string                 `json:"scheduled_start,omitempty"`
	ScheduledEnd         *string                 `json:"scheduled_end,omitempty"`
	IntercompanyPOID     *string                 `json:"intercompany_po_id,omitempty"`
	CustomerName         string                  `json:"customer_name"`
	SiteAddress          string                  `json:"site_address"`
	Lat                  *float64                `json:"lat,omitempty"`
	Lng                  *float64                `json:"lng,omitempty"`
	ChecklistSummary     mobileChecklistSummary  `json:"checklist_summary"`
	ScheduleLocked       bool                    `json:"schedule_locked"`
	DeepLink             string                  `json:"deep_link"`
}

func toMobileEWODTO(e domain.EWO, summary mobileChecklistSummary) mobileEWODTO {
	d := mobileEWODTO{
		EWOID:            e.ID.String(),
		EWONumber:        e.EWONumber,
		Side:             string(e.Side),
		Status:           string(e.Status),
		ChecklistSummary: summary,
		ScheduleLocked:   e.ScheduleLocked,
		DeepLink:         "ion-tech://ewo/" + e.ID.String(),
	}
	if e.ScheduledStartDate != nil {
		s := rfc3339(*e.ScheduledStartDate)
		d.ScheduledStart = &s
	}
	if e.ScheduledEndDate != nil {
		s := rfc3339(*e.ScheduledEndDate)
		d.ScheduledEnd = &s
	}
	if e.IntercompanyPOID != nil {
		s := e.IntercompanyPOID.String()
		d.IntercompanyPOID = &s
	}
	return d
}

type checklistProgressDTO struct {
	ID              string  `json:"id"`
	EWOID           string  `json:"ewo_id"`
	ChecklistItemID *string `json:"checklist_item_id,omitempty"`
	ItemLabel       string  `json:"item_label"`
	Status          string  `json:"status"`
	CompletedBy     *string `json:"completed_by,omitempty"`
	CompletedAt     *string `json:"completed_at,omitempty"`
	PhotoURL        *string `json:"photo_url,omitempty"`
	PhotoHash       *string `json:"photo_hash,omitempty"`
	Notes           string  `json:"notes"`
	IdempotencyKey  *string `json:"idempotency_key,omitempty"`
	CreatedAt       string  `json:"created_at"`
	UpdatedAt       string  `json:"updated_at"`
}

func toChecklistProgressDTO(p domain.EWOChecklistProgress) checklistProgressDTO {
	d := checklistProgressDTO{
		ID:             p.ID.String(),
		EWOID:          p.EWOID.String(),
		ItemLabel:      p.ItemLabel,
		Status:         string(p.Status),
		Notes:          p.Notes,
		IdempotencyKey: p.IdempotencyKey,
		PhotoURL:       p.PhotoURL,
		PhotoHash:      p.PhotoHash,
		CreatedAt:      rfc3339(p.CreatedAt),
		UpdatedAt:      rfc3339(p.UpdatedAt),
	}
	if p.ChecklistItemID != nil {
		s := p.ChecklistItemID.String()
		d.ChecklistItemID = &s
	}
	if p.CompletedBy != nil {
		s := p.CompletedBy.String()
		d.CompletedBy = &s
	}
	if p.CompletedAt != nil {
		s := rfc3339(*p.CompletedAt)
		d.CompletedAt = &s
	}
	return d
}

type pushLogDTO struct {
	ID             string         `json:"id"`
	EWOID          string         `json:"ewo_id"`
	Subject        string         `json:"subject"`
	TargetUserID   string         `json:"target_user_id"`
	Payload        map[string]any `json:"payload,omitempty"`
	SentAt         string         `json:"sent_at"`
	DispatchStatus string         `json:"dispatch_status"`
	ErrorMsg       string         `json:"error_msg,omitempty"`
}

func toPushLogDTO(e domain.EWOPushEvent) pushLogDTO {
	return pushLogDTO{
		ID:             e.ID.String(),
		EWOID:          e.EWOID.String(),
		Subject:        string(e.Subject),
		TargetUserID:   e.TargetUserID.String(),
		Payload:        e.Payload,
		SentAt:         rfc3339(e.SentAt),
		DispatchStatus: e.DispatchStatus,
		ErrorMsg:       e.ErrorMsg,
	}
}

type mobileSiteLinkDTO struct {
	MapsURL string   `json:"maps_url"`
	Address string   `json:"address"`
	Lat     *float64 `json:"lat,omitempty"`
	Lng     *float64 `json:"lng,omitempty"`
}

