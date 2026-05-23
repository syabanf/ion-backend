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

// IntercompanyPOHandler is the Wave 95 HTTP surface for IC-PO
// lifecycle on the executing-subsidiary side.
type IntercompanyPOHandler struct {
	uc       *usecase.Service
	verifier *auth.Verifier
}

func NewIntercompanyPOHandler(uc *usecase.Service, verifier *auth.Verifier) *IntercompanyPOHandler {
	return &IntercompanyPOHandler{uc: uc, verifier: verifier}
}

// Mount — Intercompany PO route map:
//
//	GET    /intercompany-pos                       [enterprise.intercompany_po.read]
//	GET    /intercompany-pos/{id}                  [enterprise.intercompany_po.read]
//	POST   /intercompany-pos/{id}/issue            [enterprise.intercompany_po.write]
//	POST   /intercompany-pos/{id}/accept           [enterprise.intercompany_po.accept]
//	POST   /intercompany-pos/{id}/reject           [enterprise.intercompany_po.reject]
//	POST   /intercompany-pos/{id}/cancel           [enterprise.intercompany_po.cancel]
//
// Note: IC-POs are not created via this surface — they're auto-spawned
// by AcceptCustomerPO. A future wave may add a manual-create endpoint
// behind `intercompany_po.write` for edge cases (e.g. re-issue after
// a rejection).
func (h *IntercompanyPOHandler) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(httpserver.RequireAuth(h.verifier))

		r.With(httpserver.RequirePermission("enterprise.intercompany_po.read")).
			Get("/intercompany-pos", h.list)
		r.With(httpserver.RequirePermission("enterprise.intercompany_po.read")).
			Get("/intercompany-pos/{id}", h.get)
		r.With(httpserver.RequirePermission("enterprise.intercompany_po.write")).
			Post("/intercompany-pos/{id}/issue", h.issue)
		r.With(httpserver.RequirePermission("enterprise.intercompany_po.accept")).
			Post("/intercompany-pos/{id}/accept", h.accept)
		r.With(httpserver.RequirePermission("enterprise.intercompany_po.reject")).
			Post("/intercompany-pos/{id}/reject", h.reject)
		r.With(httpserver.RequirePermission("enterprise.intercompany_po.cancel")).
			Post("/intercompany-pos/{id}/cancel", h.cancel)
	})
}

// =====================================================================
// Handlers
// =====================================================================

func (h *IntercompanyPOHandler) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page := parseIntDefaultLocal(q.Get("page"), 1)
	if page < 1 {
		page = 1
	}
	pageSize := parseIntDefaultLocal(q.Get("page_size"), 50)
	f := port.IntercompanyPOListFilter{
		Status: q.Get("status"),
		Limit:  pageSize,
		Offset: (page - 1) * pageSize,
	}
	if s := q.Get("customer_po_id"); s != "" {
		u, err := parseUUIDLocal(s, "customer_po_id")
		if err != nil {
			httpserver.WriteError(w, err)
			return
		}
		f.CustomerPOID = &u
	}
	if s := q.Get("commercial_owner_subsidiary_id"); s != "" {
		u, err := parseUUIDLocal(s, "commercial_owner_subsidiary_id")
		if err != nil {
			httpserver.WriteError(w, err)
			return
		}
		f.CommercialOwnerSubsidiaryID = &u
	}
	if s := q.Get("executing_subsidiary_id"); s != "" {
		u, err := parseUUIDLocal(s, "executing_subsidiary_id")
		if err != nil {
			httpserver.WriteError(w, err)
			return
		}
		f.ExecutingSubsidiaryID = &u
	}
	if s := q.Get("boq_version_id"); s != "" {
		u, err := parseUUIDLocal(s, "boq_version_id")
		if err != nil {
			httpserver.WriteError(w, err)
			return
		}
		f.BOQVersionID = &u
	}
	items, total, err := h.uc.ListIntercompanyPOs(r.Context(), f)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]intercompanyPODTO, 0, len(items))
	for _, ic := range items {
		out = append(out, toIntercompanyPODTO(ic))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"items":     out,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

func (h *IntercompanyPOHandler) get(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "intercompany_po")
	if !ok {
		return
	}
	po, lines, err := h.uc.GetIntercompanyPO(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	dto := toIntercompanyPODTO(*po)
	dto.Lines = make([]intercompanyPOLineDTO, 0, len(lines))
	for _, l := range lines {
		dto.Lines = append(dto.Lines, toIntercompanyPOLineDTO(l))
	}
	httpserver.WriteJSON(w, http.StatusOK, dto)
}

func (h *IntercompanyPOHandler) issue(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "intercompany_po")
	if !ok {
		return
	}
	po, err := h.uc.IssueIntercompanyPO(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toIntercompanyPODTO(*po))
}

func (h *IntercompanyPOHandler) accept(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "intercompany_po")
	if !ok {
		return
	}
	var byUserID *uuid.UUID
	if uid := actorUserIDLocal(r.Context()); uid != nil {
		byUserID = uid
	}
	po, err := h.uc.AcceptIntercompanyPO(r.Context(), id, byUserID)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toIntercompanyPODTO(*po))
}

func (h *IntercompanyPOHandler) reject(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "intercompany_po")
	if !ok {
		return
	}
	var req reasonRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	po, err := h.uc.RejectIntercompanyPO(r.Context(), id, req.Reason)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toIntercompanyPODTO(*po))
}

func (h *IntercompanyPOHandler) cancel(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "intercompany_po")
	if !ok {
		return
	}
	po, err := h.uc.CancelIntercompanyPO(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toIntercompanyPODTO(*po))
}

// =====================================================================
// DTOs
// =====================================================================

type intercompanyPODTO struct {
	ID                          string                  `json:"id"`
	CustomerPOID                string                  `json:"customer_po_id"`
	BOQVersionID                string                  `json:"boq_version_id"`
	CommercialOwnerSubsidiaryID string                  `json:"commercial_owner_subsidiary_id"`
	ExecutingSubsidiaryID       string                  `json:"executing_subsidiary_id"`
	ICPONumber                  string                  `json:"ic_po_number"`
	Status                      string                  `json:"status"`
	Total                       *float64                `json:"total,omitempty"`
	TaxSnapshotHash             string                  `json:"tax_snapshot_hash,omitempty"`
	IssuedAt                    *string                 `json:"issued_at,omitempty"`
	AcceptedAt                  *string                 `json:"accepted_at,omitempty"`
	AcceptedBy                  *string                 `json:"accepted_by,omitempty"`
	RejectedAt                  *string                 `json:"rejected_at,omitempty"`
	RejectionReason             string                  `json:"rejection_reason,omitempty"`
	CancelledAt                 *string                 `json:"cancelled_at,omitempty"`
	SupersededAt                *string                 `json:"superseded_at,omitempty"`
	SupersedesID                *string                 `json:"supersedes_id,omitempty"`
	Notes                       string                  `json:"notes,omitempty"`
	CreatedAt                   string                  `json:"created_at"`
	UpdatedAt                   string                  `json:"updated_at"`
	Lines                       []intercompanyPOLineDTO `json:"lines,omitempty"`
}

func toIntercompanyPODTO(ic domain.IntercompanyPO) intercompanyPODTO {
	return intercompanyPODTO{
		ID:                          ic.ID.String(),
		CustomerPOID:                ic.CustomerPOID.String(),
		BOQVersionID:                ic.BOQVersionID.String(),
		CommercialOwnerSubsidiaryID: ic.CommercialOwnerSubsidiaryID.String(),
		ExecutingSubsidiaryID:       ic.ExecutingSubsidiaryID.String(),
		ICPONumber:                  ic.ICPONumber,
		Status:                      string(ic.Status),
		Total:                       ic.Total,
		TaxSnapshotHash:             ic.TaxSnapshotHash,
		IssuedAt:                    rfc3339Ptr(ic.IssuedAt),
		AcceptedAt:                  rfc3339Ptr(ic.AcceptedAt),
		AcceptedBy:                  uuidPtrString(ic.AcceptedBy),
		RejectedAt:                  rfc3339Ptr(ic.RejectedAt),
		RejectionReason:             ic.RejectionReason,
		CancelledAt:                 rfc3339Ptr(ic.CancelledAt),
		SupersededAt:                rfc3339Ptr(ic.SupersededAt),
		SupersedesID:                uuidPtrString(ic.SupersedesID),
		Notes:                       ic.Notes,
		CreatedAt:                   rfc3339(ic.CreatedAt),
		UpdatedAt:                   rfc3339(ic.UpdatedAt),
	}
}

type intercompanyPOLineDTO struct {
	ID             string  `json:"id"`
	ICPOID         string  `json:"ic_po_id"`
	BOQLineID      *string `json:"boq_line_id,omitempty"`
	SKUOrServiceID *string `json:"sku_or_service_id,omitempty"`
	Description    string  `json:"description"`
	Qty            float64 `json:"qty"`
	UnitPrice      float64 `json:"unit_price"`
	LineTotal      float64 `json:"line_total"`
	TaxAmount      float64 `json:"tax_amount"`
	CreatedAt      string  `json:"created_at"`
}

func toIntercompanyPOLineDTO(l domain.IntercompanyPOLine) intercompanyPOLineDTO {
	return intercompanyPOLineDTO{
		ID:             l.ID.String(),
		ICPOID:         l.ICPOID.String(),
		BOQLineID:      uuidPtrString(l.BOQLineID),
		SKUOrServiceID: uuidPtrString(l.SKUOrServiceID),
		Description:    l.Description,
		Qty:            l.Qty,
		UnitPrice:      l.UnitPrice,
		LineTotal:      l.LineTotal,
		TaxAmount:      l.TaxAmount,
		CreatedAt:      rfc3339(l.CreatedAt),
	}
}
