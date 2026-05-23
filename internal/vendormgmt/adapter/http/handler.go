// Package http is the vendor bounded context's HTTP surface.
//
// Routes (all gated on `vendor.*` permissions from migration 0072):
//
//	GET    /api/vendor/providers                                  [provider.read]
//	POST   /api/vendor/providers                                  [provider.write]
//	GET    /api/vendor/providers/{id}                             [provider.read]
//	PATCH  /api/vendor/providers/{id}                             [provider.write]
//	POST   /api/vendor/providers/{id}/kyc-complete                [provider.write]
//	POST   /api/vendor/providers/{id}/activate                    [provider.write]
//	POST   /api/vendor/providers/{id}/suspend                     [provider.write]
//	POST   /api/vendor/providers/{id}/reactivate                  [provider.write]
//	POST   /api/vendor/providers/{id}/blacklist                   [provider.write]
//	POST   /api/vendor/providers/{id}/capabilities                [provider.write]
//	GET    /api/vendor/providers/{id}/metrics                     [metrics.read]
//	GET    /api/vendor/providers/top-rated                        [provider.read]
//
//	GET    /api/vendor/submissions                                [submission.read]
//	POST   /api/vendor/submissions                                [submission.write]
//	GET    /api/vendor/submissions/{id}                           [submission.read]
//	POST   /api/vendor/submissions/{id}/accept                    [submission.review]
//	POST   /api/vendor/submissions/{id}/reject                    [submission.review]
//	POST   /api/vendor/submissions/{id}/withdraw                  [submission.write]
package http

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/vendormgmt/port"
	"github.com/ion-core/backend/pkg/auth"
	"github.com/ion-core/backend/pkg/errors"
	"github.com/ion-core/backend/pkg/httpserver"
)

type Handler struct {
	providers   port.ProviderUseCase
	submissions port.SubmissionUseCase
	metrics     port.MetricsUseCase
	verifier    *auth.Verifier
}

func NewHandler(
	providers port.ProviderUseCase,
	submissions port.SubmissionUseCase,
	metrics port.MetricsUseCase,
	verifier *auth.Verifier,
) *Handler {
	return &Handler{
		providers:   providers,
		submissions: submissions,
		metrics:     metrics,
		verifier:    verifier,
	}
}

func (h *Handler) Mount(r chi.Router) {
	r.Route("/api/vendor", func(r chi.Router) {
		r.Use(httpserver.RequireAuth(h.verifier))

		// Providers
		r.With(httpserver.RequirePermission("vendor.provider.read")).
			Get("/providers", h.listProviders)
		r.With(httpserver.RequirePermission("vendor.provider.read")).
			Get("/providers/top-rated", h.topRated)
		r.With(httpserver.RequirePermission("vendor.provider.write")).
			Post("/providers", h.createProvider)
		r.With(httpserver.RequirePermission("vendor.provider.read")).
			Get("/providers/{id}", h.getProvider)
		r.With(httpserver.RequirePermission("vendor.provider.write")).
			Patch("/providers/{id}", h.updateProvider)
		r.With(httpserver.RequirePermission("vendor.provider.write")).
			Post("/providers/{id}/kyc-complete", h.completeKYC)
		r.With(httpserver.RequirePermission("vendor.provider.write")).
			Post("/providers/{id}/activate", h.activate)
		r.With(httpserver.RequirePermission("vendor.provider.write")).
			Post("/providers/{id}/suspend", h.suspend)
		r.With(httpserver.RequirePermission("vendor.provider.write")).
			Post("/providers/{id}/reactivate", h.reactivate)
		r.With(httpserver.RequirePermission("vendor.provider.write")).
			Post("/providers/{id}/blacklist", h.blacklist)
		r.With(httpserver.RequirePermission("vendor.provider.write")).
			Post("/providers/{id}/capabilities", h.addCapability)
		r.With(httpserver.RequirePermission("vendor.metrics.read")).
			Get("/providers/{id}/metrics", h.listProviderMetrics)

		// Submissions
		r.With(httpserver.RequirePermission("vendor.submission.read")).
			Get("/submissions", h.listSubmissions)
		r.With(httpserver.RequirePermission("vendor.submission.write")).
			Post("/submissions", h.submit)
		r.With(httpserver.RequirePermission("vendor.submission.read")).
			Get("/submissions/{id}", h.getSubmission)
		r.With(httpserver.RequirePermission("vendor.submission.review")).
			Post("/submissions/{id}/accept", h.accept)
		r.With(httpserver.RequirePermission("vendor.submission.review")).
			Post("/submissions/{id}/reject", h.reject)
		r.With(httpserver.RequirePermission("vendor.submission.write")).
			Post("/submissions/{id}/withdraw", h.withdraw)
	})
}

// ---------------------------------------------------------------------
// Provider handlers
// ---------------------------------------------------------------------

type createProviderRequest struct {
	Name         string   `json:"name"`
	NPWP         string   `json:"npwp,omitempty"`
	ContactEmail string   `json:"contact_email,omitempty"`
	ContactPhone string   `json:"contact_phone,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
}

func (h *Handler) createProvider(w http.ResponseWriter, r *http.Request) {
	var req createProviderRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	p, err := h.providers.Create(r.Context(), port.CreateProviderInput{
		Name:         req.Name,
		NPWP:         req.NPWP,
		ContactEmail: req.ContactEmail,
		ContactPhone: req.ContactPhone,
		Capabilities: req.Capabilities,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toProviderDTO(p, nil))
}

type updateProviderRequest struct {
	Name         *string `json:"name,omitempty"`
	NPWP         *string `json:"npwp,omitempty"`
	ContactEmail *string `json:"contact_email,omitempty"`
	ContactPhone *string `json:"contact_phone,omitempty"`
}

func (h *Handler) updateProvider(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "provider")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var req updateProviderRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	p, err := h.providers.Update(r.Context(), port.UpdateProviderInput{
		ID:           id,
		Name:         req.Name,
		NPWP:         req.NPWP,
		ContactEmail: req.ContactEmail,
		ContactPhone: req.ContactPhone,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toProviderDTO(p, nil))
}

func (h *Handler) getProvider(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "provider")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	p, caps, err := h.providers.Get(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toProviderDTO(p, caps))
}

func (h *Handler) listProviders(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page := parseIntDefault(q.Get("page"), 1)
	if page < 1 {
		page = 1
	}
	pageSize := parseIntDefault(q.Get("page_size"), 50)
	f := port.ProviderListFilter{
		Status:     q.Get("status"),
		OnlyActive: q.Get("only_active") == "true",
		Limit:      pageSize,
		Offset:     (page - 1) * pageSize,
	}
	if cap := q.Get("capability"); cap != "" {
		f.CapabilityIn = []string{cap}
	}
	items, total, err := h.providers.List(r.Context(), f)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(items))
	for i := range items {
		out = append(out, toProviderDTO(&items[i], nil))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"items":     out,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

func (h *Handler) completeKYC(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "provider")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	p, err := h.providers.CompleteKYC(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toProviderDTO(p, nil))
}

func (h *Handler) activate(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "provider")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	p, err := h.providers.Activate(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toProviderDTO(p, nil))
}

type reasonRequest struct {
	Reason string `json:"reason"`
}

func (h *Handler) suspend(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "provider")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var req reasonRequest
	_ = httpserver.DecodeJSON(r, &req)
	p, err := h.providers.Suspend(r.Context(), id, req.Reason)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toProviderDTO(p, nil))
}

func (h *Handler) reactivate(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "provider")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	p, err := h.providers.Reactivate(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toProviderDTO(p, nil))
}

func (h *Handler) blacklist(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "provider")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var req reasonRequest
	_ = httpserver.DecodeJSON(r, &req)
	p, err := h.providers.Blacklist(r.Context(), id, req.Reason)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toProviderDTO(p, nil))
}

type addCapabilityRequest struct {
	CapabilityKey  string `json:"capability_key"`
	CapabilityName string `json:"capability_name,omitempty"`
	MaxCapacity    *int   `json:"max_capacity,omitempty"`
}

func (h *Handler) addCapability(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "provider")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var req addCapabilityRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	c, err := h.providers.AddCapability(r.Context(), port.AddCapabilityInput{
		ProviderID:     id,
		CapabilityKey:  req.CapabilityKey,
		CapabilityName: req.CapabilityName,
		MaxCapacity:    req.MaxCapacity,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, map[string]any{
		"id":              c.ID.String(),
		"provider_id":     c.ProviderID.String(),
		"capability_key":  c.CapabilityKey,
		"capability_name": c.CapabilityName,
		"max_capacity":    c.MaxCapacity,
	})
}

// ---------------------------------------------------------------------
// Submission handlers
// ---------------------------------------------------------------------

type submitRequest struct {
	OpportunityID string   `json:"opportunity_id"`
	ProviderID    string   `json:"provider_id"`
	BOQLineID     *string  `json:"boq_line_id,omitempty"`
	UnitCost      *float64 `json:"unit_cost,omitempty"`
	Notes         string   `json:"notes,omitempty"`
}

func (h *Handler) submit(w http.ResponseWriter, r *http.Request) {
	var req submitRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	oppID, err := parseUUID(req.OpportunityID, "opportunity_id")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	provID, err := parseUUID(req.ProviderID, "provider_id")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var boqLineID *uuid.UUID
	if req.BOQLineID != nil && *req.BOQLineID != "" {
		u, err := parseUUID(*req.BOQLineID, "boq_line_id")
		if err != nil {
			httpserver.WriteError(w, err)
			return
		}
		boqLineID = &u
	}
	actor := actorUserID(r.Context())
	sub, err := h.submissions.Submit(r.Context(), port.SubmitInputInput{
		OpportunityID: oppID,
		ProviderID:    provID,
		BOQLineID:     boqLineID,
		UnitCost:      req.UnitCost,
		Notes:         req.Notes,
		SubmittedBy:   actor,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toSubmissionDTO(sub))
}

func (h *Handler) listSubmissions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page := parseIntDefault(q.Get("page"), 1)
	if page < 1 {
		page = 1
	}
	pageSize := parseIntDefault(q.Get("page_size"), 50)
	f := port.SubmissionListFilter{
		Status: q.Get("status"),
		Limit:  pageSize,
		Offset: (page - 1) * pageSize,
	}
	if s := q.Get("opportunity_id"); s != "" {
		u, err := parseUUID(s, "opportunity_id")
		if err != nil {
			httpserver.WriteError(w, err)
			return
		}
		f.OpportunityID = &u
	}
	if s := q.Get("provider_id"); s != "" {
		u, err := parseUUID(s, "provider_id")
		if err != nil {
			httpserver.WriteError(w, err)
			return
		}
		f.ProviderID = &u
	}
	items, total, err := h.submissions.List(r.Context(), f)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(items))
	for i := range items {
		out = append(out, toSubmissionDTO(&items[i]))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"items":     out,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

func (h *Handler) getSubmission(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "submission")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	sub, err := h.submissions.Get(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toSubmissionDTO(sub))
}

func (h *Handler) accept(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "submission")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	actor := actorUserID(r.Context())
	if actor == nil {
		httpserver.WriteError(w, errors.Forbidden("submission.actor_required", "actor required"))
		return
	}
	sub, err := h.submissions.Accept(r.Context(), port.ReviewSubmissionInput{
		SubmissionID: id,
		Reviewer:     *actor,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toSubmissionDTO(sub))
}

func (h *Handler) reject(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "submission")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	actor := actorUserID(r.Context())
	if actor == nil {
		httpserver.WriteError(w, errors.Forbidden("submission.actor_required", "actor required"))
		return
	}
	var req reasonRequest
	_ = httpserver.DecodeJSON(r, &req)
	sub, err := h.submissions.Reject(r.Context(), port.ReviewSubmissionInput{
		SubmissionID: id,
		Reviewer:     *actor,
		Reason:       req.Reason,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toSubmissionDTO(sub))
}

func (h *Handler) withdraw(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "submission")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	actor := actorUserID(r.Context())
	if actor == nil {
		httpserver.WriteError(w, errors.Forbidden("submission.actor_required", "actor required"))
		return
	}
	sub, err := h.submissions.Withdraw(r.Context(), id, *actor)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toSubmissionDTO(sub))
}

// ---------------------------------------------------------------------
// Metrics handlers
// ---------------------------------------------------------------------

func (h *Handler) listProviderMetrics(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "provider")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	q := r.URL.Query()
	now := time.Now().UTC()
	to := now
	from := now.AddDate(0, 0, -30)
	if s := q.Get("from"); s != "" {
		if t, err := time.Parse("2006-01-02", s); err == nil {
			from = t
		}
	}
	if s := q.Get("to"); s != "" {
		if t, err := time.Parse("2006-01-02", s); err == nil {
			to = t
		}
	}
	items, err := h.metrics.ListDailyMetrics(r.Context(), id, from, to)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	avg, _ := h.metrics.AverageScoreForProvider(r.Context(), id, 30)
	out := make([]map[string]any, 0, len(items))
	for _, m := range items {
		out = append(out, map[string]any{
			"metric_date":             m.MetricDate.Format("2006-01-02"),
			"jobs_completed":          m.JobsCompleted,
			"on_time_completion_pct":  m.OnTimeCompletionPct,
			"avg_response_hours":      m.AvgResponseHours,
			"tickets_resolved":        m.TicketsResolved,
			"customer_satisfaction":   m.CustomerSatisfaction,
		})
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"items":              out,
		"average_score_30d":  avg,
	})
}

func (h *Handler) topRated(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := parseIntDefault(q.Get("limit"), 20)
	minRating, _ := strconv.ParseFloat(q.Get("min_rating"), 64)
	minJobs := parseIntDefault(q.Get("min_jobs"), 0)
	f := port.TopRatedFilter{
		Capability: strings.TrimSpace(q.Get("capability")),
		MinRating:  minRating,
		MinJobs:    minJobs,
		Limit:      limit,
	}
	items, err := h.metrics.TopRatedProviders(r.Context(), f)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]map[string]any, 0, len(items))
	for i := range items {
		out = append(out, toProviderDTO(&items[i], nil))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}
