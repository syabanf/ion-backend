// Round-2 HTTP handlers: schema read + sales dashboard.
package http

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/crm/port"
	"github.com/ion-core/backend/pkg/errors"
	"github.com/ion-core/backend/pkg/httpserver"
)

// DTOs (schemaDTO, salesDashboardDTO) live in dto.go.

// =====================================================================
// Schemas — list / get
// =====================================================================

func (h *Handler) listSchemas(w http.ResponseWriter, r *http.Request) {
	out, err := h.uc.ListOnboardingSchemas(r.Context())
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	items := make([]schemaDTO, 0, len(out))
	for _, s := range out {
		items = append(items, toSchemaDTO(s))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) getSchema(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpserver.WriteError(w, errors.Validation("schema.id_invalid", "id is not a uuid"))
		return
	}
	s, err := h.uc.GetOnboardingSchema(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toSchemaDTO(*s))
}

// =====================================================================
// Sales dashboard
// =====================================================================

func (h *Handler) salesDashboard(w http.ResponseWriter, r *http.Request) {
	c := httpserver.ClaimsFromContext(r.Context())
	if c == nil {
		httpserver.WriteError(w, errors.Unauthorized("auth.missing", "authentication required"))
		return
	}
	in := port.SalesDashboardInput{}
	if r.URL.Query().Get("mine") == "true" {
		uid := c.UserID
		in.MineUserID = &uid
	}
	v, err := h.uc.SalesDashboard(r.Context(), in)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	dto := salesDashboardDTO{
		LeadsByStatus:      map[string]int{},
		ConvertedThisMonth: v.ConvertedThisMonth,
		OrdersThisMonth:    v.OrdersThisMonth,
		TotalThisMonth:     v.TotalOTCMonth,
	}
	for st, n := range v.LeadsByStatus {
		dto.LeadsByStatus[string(st)] = n
	}
	for _, lw := range v.RecentLeads {
		dto.RecentLeads = append(dto.RecentLeads, toLeadDTO(lw))
	}
	for _, o := range v.RecentConversions {
		dto.RecentConversions = append(dto.RecentConversions, toOrderDTO(o))
	}
	httpserver.WriteJSON(w, http.StatusOK, dto)
}
