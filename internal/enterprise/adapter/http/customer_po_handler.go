package http

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/enterprise/domain"
	"github.com/ion-core/backend/internal/enterprise/port"
	"github.com/ion-core/backend/internal/enterprise/usecase"
	"github.com/ion-core/backend/pkg/auth"
	"github.com/ion-core/backend/pkg/httpserver"
)

// CustomerPOHandler is the Wave 95 HTTP surface for buyer-side POs.
// All routes require auth; per-route permissions follow the wave brief:
//
//	customer_po.read     — list / get
//	customer_po.write    — upload / validate / accept / reject / cancel
//
// We depend on the concrete `*usecase.Service` (not a narrowed
// interface) for now — the Wave 95 surface is small enough that
// introducing yet another typed interface in port/ adds more friction
// than it saves. A consolidated `CustomerPOUseCase` interface lands
// alongside the contract-test sweep in Wave 95.
type CustomerPOHandler struct {
	uc       *usecase.Service
	verifier *auth.Verifier
}

func NewCustomerPOHandler(uc *usecase.Service, verifier *auth.Verifier) *CustomerPOHandler {
	return &CustomerPOHandler{uc: uc, verifier: verifier}
}

// Mount — Customer PO route map:
//
//	POST   /customer-pos                        [enterprise.customer_po.write]
//	GET    /customer-pos                        [enterprise.customer_po.read]
//	GET    /customer-pos/{id}                   [enterprise.customer_po.read]
//	POST   /customer-pos/{id}/validate          [enterprise.customer_po.write]
//	POST   /customer-pos/{id}/accept            [enterprise.customer_po.write]
//	POST   /customer-pos/{id}/reject            [enterprise.customer_po.write]
//	POST   /customer-pos/{id}/cancel            [enterprise.customer_po.write]
func (h *CustomerPOHandler) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(httpserver.RequireAuth(h.verifier))

		r.With(httpserver.RequirePermission("enterprise.customer_po.write")).
			Post("/customer-pos", h.upload)
		r.With(httpserver.RequirePermission("enterprise.customer_po.read")).
			Get("/customer-pos", h.list)
		r.With(httpserver.RequirePermission("enterprise.customer_po.read")).
			Get("/customer-pos/{id}", h.get)
		r.With(httpserver.RequirePermission("enterprise.customer_po.write")).
			Post("/customer-pos/{id}/validate", h.validate)
		r.With(httpserver.RequirePermission("enterprise.customer_po.write")).
			Post("/customer-pos/{id}/accept", h.accept)
		r.With(httpserver.RequirePermission("enterprise.customer_po.write")).
			Post("/customer-pos/{id}/reject", h.reject)
		r.With(httpserver.RequirePermission("enterprise.customer_po.write")).
			Post("/customer-pos/{id}/cancel", h.cancel)
	})
}

// =====================================================================
// Handlers
// =====================================================================

func (h *CustomerPOHandler) upload(w http.ResponseWriter, r *http.Request) {
	var req createCustomerPORequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	oppID, err := parseUUIDLocal(req.OpportunityID, "opportunity_id")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	boqID, err := parseUUIDLocal(req.BOQVersionID, "boq_version_id")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	ownerID, err := parseUUIDLocal(req.CommercialOwnerSubsidiaryID, "commercial_owner_subsidiary_id")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	in := port.UploadCustomerPOInput{
		OpportunityID:               oppID,
		BOQVersionID:                boqID,
		CommercialOwnerSubsidiaryID: ownerID,
		PONumber:                    req.PONumber,
		POValue:                     req.POValue,
		FileURL:                     req.FileURL,
		FileHash:                    req.FileHash,
		Notes:                       req.Notes,
	}
	if s := req.CustomerID; s != "" {
		u, perr := parseUUIDLocal(s, "customer_id")
		if perr != nil {
			httpserver.WriteError(w, perr)
			return
		}
		in.CustomerID = &u
	}
	if uid := actorUserIDLocal(r.Context()); uid != nil {
		in.UploadedBy = uid
	}
	po, err := h.uc.UploadCustomerPO(r.Context(), in)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toCustomerPODTO(*po))
}

func (h *CustomerPOHandler) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page := parseIntDefaultLocal(q.Get("page"), 1)
	if page < 1 {
		page = 1
	}
	pageSize := parseIntDefaultLocal(q.Get("page_size"), 50)
	f := port.CustomerPOListFilter{
		Status: q.Get("status"),
		Limit:  pageSize,
		Offset: (page - 1) * pageSize,
	}
	if s := q.Get("opportunity_id"); s != "" {
		u, err := parseUUIDLocal(s, "opportunity_id")
		if err != nil {
			httpserver.WriteError(w, err)
			return
		}
		f.OpportunityID = &u
	}
	if s := q.Get("commercial_owner_subsidiary_id"); s != "" {
		u, err := parseUUIDLocal(s, "commercial_owner_subsidiary_id")
		if err != nil {
			httpserver.WriteError(w, err)
			return
		}
		f.CommercialOwnerSubsidiaryID = &u
	}
	items, total, err := h.uc.ListCustomerPOs(r.Context(), f)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]customerPODTO, 0, len(items))
	for _, c := range items {
		out = append(out, toCustomerPODTO(c))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"items":     out,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

func (h *CustomerPOHandler) get(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "customer_po")
	if !ok {
		return
	}
	po, err := h.uc.GetCustomerPO(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toCustomerPODTO(*po))
}

func (h *CustomerPOHandler) validate(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "customer_po")
	if !ok {
		return
	}
	po, err := h.uc.ValidateCustomerPO(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toCustomerPODTO(*po))
}

func (h *CustomerPOHandler) accept(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "customer_po")
	if !ok {
		return
	}
	var acceptorID *uuid.UUID
	if uid := actorUserIDLocal(r.Context()); uid != nil {
		acceptorID = uid
	}
	po, icPOs, err := h.uc.AcceptCustomerPO(r.Context(), id, acceptorID)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	icOut := make([]intercompanyPOSummaryDTO, 0, len(icPOs))
	for _, ic := range icPOs {
		icOut = append(icOut, toIntercompanyPOSummaryDTO(ic))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"customer_po":        toCustomerPODTO(*po),
		"intercompany_pos":   icOut,
		"intercompany_count": len(icOut),
	})
}

func (h *CustomerPOHandler) reject(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "customer_po")
	if !ok {
		return
	}
	var req reasonRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	po, err := h.uc.RejectCustomerPO(r.Context(), id, req.Reason)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toCustomerPODTO(*po))
}

func (h *CustomerPOHandler) cancel(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "customer_po")
	if !ok {
		return
	}
	po, err := h.uc.CancelCustomerPO(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toCustomerPODTO(*po))
}

// =====================================================================
// DTOs
// =====================================================================

type customerPODTO struct {
	ID                          string   `json:"id"`
	OpportunityID               string   `json:"opportunity_id"`
	BOQVersionID                string   `json:"boq_version_id"`
	CustomerID                  *string  `json:"customer_id,omitempty"`
	CommercialOwnerSubsidiaryID string   `json:"commercial_owner_subsidiary_id"`
	PONumber                    string   `json:"po_number"`
	POValue                     *float64 `json:"po_value,omitempty"`
	FileURL                     string   `json:"file_url,omitempty"`
	FileHash                    string   `json:"file_hash,omitempty"`
	UploadedBy                  *string  `json:"uploaded_by,omitempty"`
	UploadedAt                  *string  `json:"uploaded_at,omitempty"`
	Status                      string   `json:"status"`
	ValidatedAt                 *string  `json:"validated_at,omitempty"`
	AcceptedAt                  *string  `json:"accepted_at,omitempty"`
	RejectedAt                  *string  `json:"rejected_at,omitempty"`
	CancelledAt                 *string  `json:"cancelled_at,omitempty"`
	RejectionReason             string   `json:"rejection_reason,omitempty"`
	Notes                       string   `json:"notes,omitempty"`
	CreatedAt                   string   `json:"created_at"`
	UpdatedAt                   string   `json:"updated_at"`
}

func toCustomerPODTO(c domain.CustomerPO) customerPODTO {
	return customerPODTO{
		ID:                          c.ID.String(),
		OpportunityID:               c.OpportunityID.String(),
		BOQVersionID:                c.BOQVersionID.String(),
		CustomerID:                  uuidPtrString(c.CustomerID),
		CommercialOwnerSubsidiaryID: c.CommercialOwnerSubsidiaryID.String(),
		PONumber:                    c.PONumber,
		POValue:                     c.POValue,
		FileURL:                     c.FileURL,
		FileHash:                    c.FileHash,
		UploadedBy:                  uuidPtrString(c.UploadedBy),
		UploadedAt:                  rfc3339Ptr(c.UploadedAt),
		Status:                      string(c.Status),
		ValidatedAt:                 rfc3339Ptr(c.ValidatedAt),
		AcceptedAt:                  rfc3339Ptr(c.AcceptedAt),
		RejectedAt:                  rfc3339Ptr(c.RejectedAt),
		CancelledAt:                 rfc3339Ptr(c.CancelledAt),
		RejectionReason:             c.RejectionReason,
		Notes:                       c.Notes,
		CreatedAt:                   rfc3339(c.CreatedAt),
		UpdatedAt:                   rfc3339(c.UpdatedAt),
	}
}

// intercompanyPOSummaryDTO is the slim shape returned alongside the
// AcceptCustomerPO response — just enough to render the FE
// confirmation list ("created N IC-POs"). Full details are fetched via
// GET /intercompany-pos/{id}.
type intercompanyPOSummaryDTO struct {
	ID                          string   `json:"id"`
	ICPONumber                  string   `json:"ic_po_number"`
	CommercialOwnerSubsidiaryID string   `json:"commercial_owner_subsidiary_id"`
	ExecutingSubsidiaryID       string   `json:"executing_subsidiary_id"`
	Status                      string   `json:"status"`
	Total                       *float64 `json:"total,omitempty"`
	IssuedAt                    *string  `json:"issued_at,omitempty"`
	AcceptedAt                  *string  `json:"accepted_at,omitempty"`
}

func toIntercompanyPOSummaryDTO(ic domain.IntercompanyPO) intercompanyPOSummaryDTO {
	return intercompanyPOSummaryDTO{
		ID:                          ic.ID.String(),
		ICPONumber:                  ic.ICPONumber,
		CommercialOwnerSubsidiaryID: ic.CommercialOwnerSubsidiaryID.String(),
		ExecutingSubsidiaryID:       ic.ExecutingSubsidiaryID.String(),
		Status:                      string(ic.Status),
		Total:                       ic.Total,
		IssuedAt:                    rfc3339Ptr(ic.IssuedAt),
		AcceptedAt:                  rfc3339Ptr(ic.AcceptedAt),
	}
}

type createCustomerPORequest struct {
	OpportunityID               string   `json:"opportunity_id"`
	BOQVersionID                string   `json:"boq_version_id"`
	CustomerID                  string   `json:"customer_id"`
	CommercialOwnerSubsidiaryID string   `json:"commercial_owner_subsidiary_id"`
	PONumber                    string   `json:"po_number"`
	POValue                     *float64 `json:"po_value,omitempty"`
	FileURL                     string   `json:"file_url"`
	FileHash                    string   `json:"file_hash"`
	Notes                       string   `json:"notes"`
}

type reasonRequest struct {
	Reason string `json:"reason"`
}
