package http

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/enterprise/domain"
	"github.com/ion-core/backend/internal/enterprise/port"
	"github.com/ion-core/backend/pkg/auth"
	"github.com/ion-core/backend/pkg/httpserver"
)

// HoldingHandler exposes the read surface for the Wave 92 multi-company
// holding scaffolding: list/read holding companies, list/read
// subsidiaries. Mutating endpoints come in a follow-up wave once the
// FK rollout to existing enterprise tables is agreed.
//
// The handler depends directly on the two repos (rather than going
// through `port.UseCase`) because Wave 92 doesn't carry domain-level
// business rules yet — they're read-through-from-DB endpoints. When
// rules land (e.g. role-based defaulting on Create), this can lift up
// to a usecase-mediated handler without breaking the route map.
type HoldingHandler struct {
	holdings     port.HoldingCompanyRepository
	subsidiaries port.SubsidiaryRepository
	verifier     *auth.Verifier
}

func NewHoldingHandler(
	holdings port.HoldingCompanyRepository,
	subsidiaries port.SubsidiaryRepository,
	verifier *auth.Verifier,
) *HoldingHandler {
	return &HoldingHandler{
		holdings:     holdings,
		subsidiaries: subsidiaries,
		verifier:     verifier,
	}
}

// Mount wires four read endpoints under the existing enterprise route
// prefix (the api-gateway strips `/api/enterprise/` before the request
// reaches this service):
//
//	GET /holding-companies                          [enterprise.holding_company.read]
//	GET /holding-companies/{id}                     [enterprise.holding_company.read]
//	GET /subsidiaries?holding_company_id=...        [enterprise.holding_company.read]
//	GET /subsidiaries/{id}                          [enterprise.holding_company.read]
func (h *HoldingHandler) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(httpserver.RequireAuth(h.verifier))

		r.With(httpserver.RequirePermission("enterprise.holding_company.read")).
			Get("/holding-companies", h.listHoldingCompanies)
		r.With(httpserver.RequirePermission("enterprise.holding_company.read")).
			Get("/holding-companies/{id}", h.getHoldingCompany)
		r.With(httpserver.RequirePermission("enterprise.holding_company.read")).
			Get("/subsidiaries", h.listSubsidiaries)
		r.With(httpserver.RequirePermission("enterprise.holding_company.read")).
			Get("/subsidiaries/{id}", h.getSubsidiary)
	})
}

// =====================================================================
// Handlers
// =====================================================================

func (h *HoldingHandler) listHoldingCompanies(w http.ResponseWriter, r *http.Request) {
	items, err := h.holdings.List(r.Context())
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]holdingCompanyDTO, 0, len(items))
	for _, hc := range items {
		out = append(out, toHoldingCompanyDTO(hc))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"items": out,
		"total": len(out),
	})
}

func (h *HoldingHandler) getHoldingCompany(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "holding_company")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	hc, err := h.holdings.FindByID(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toHoldingCompanyDTO(*hc))
}

func (h *HoldingHandler) listSubsidiaries(w http.ResponseWriter, r *http.Request) {
	var filter *uuid.UUID
	if s := r.URL.Query().Get("holding_company_id"); s != "" {
		u, err := parseUUID(s, "holding_company_id")
		if err != nil {
			httpserver.WriteError(w, err)
			return
		}
		filter = &u
	}
	items, err := h.subsidiaries.ListByHolding(r.Context(), filter)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]subsidiaryDTO, 0, len(items))
	for _, s := range items {
		out = append(out, toSubsidiaryDTO(s))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"items": out,
		"total": len(out),
	})
}

func (h *HoldingHandler) getSubsidiary(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "subsidiary")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	s, err := h.subsidiaries.FindByID(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toSubsidiaryDTO(*s))
}

// =====================================================================
// DTOs
// =====================================================================

type holdingCompanyDTO struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	NPWP            *string `json:"npwp,omitempty"`
	LegalEntityType *string `json:"legal_entity_type,omitempty"`
	CreatedAt       string  `json:"created_at"`
	UpdatedAt       string  `json:"updated_at"`
}

func toHoldingCompanyDTO(h domain.HoldingCompany) holdingCompanyDTO {
	return holdingCompanyDTO{
		ID:              h.ID.String(),
		Name:            h.Name,
		NPWP:            h.NPWP,
		LegalEntityType: h.LegalEntityType,
		CreatedAt:       httpserver.FormatRFC3339(h.CreatedAt),
		UpdatedAt:       httpserver.FormatRFC3339(h.UpdatedAt),
	}
}

// Wave 94b — `is_pkp` / `ppn_rate` were dropped from this DTO when
// migration 0063 made `tax.company_tax_profiles` the only source of
// truth for tax stance. Callers needing PKP status hit
// `GET /api/tax/profiles?subsidiary_id={id}&active=true&at={ts}`.
type subsidiaryDTO struct {
	ID               string  `json:"id"`
	HoldingCompanyID string  `json:"holding_company_id"`
	Name             string  `json:"name"`
	NPWP             *string `json:"npwp,omitempty"`
	Role             string  `json:"role"`
	CreatedAt        string  `json:"created_at"`
	UpdatedAt        string  `json:"updated_at"`
}

func toSubsidiaryDTO(s domain.Subsidiary) subsidiaryDTO {
	return subsidiaryDTO{
		ID:               s.ID.String(),
		HoldingCompanyID: s.HoldingCompanyID.String(),
		Name:             s.Name,
		NPWP:             s.NPWP,
		Role:             string(s.Role),
		CreatedAt:        httpserver.FormatRFC3339(s.CreatedAt),
		UpdatedAt:        httpserver.FormatRFC3339(s.UpdatedAt),
	}
}
