package http

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ion-core/backend/internal/enterprise/domain"
	"github.com/ion-core/backend/internal/enterprise/port"
	"github.com/ion-core/backend/pkg/auth"
	"github.com/ion-core/backend/pkg/errors"
	"github.com/ion-core/backend/pkg/httpserver"
)

// QuotationHandler is the Phase-4a HTTP surface. Same auth + masking
// chain as BOQHandler — pinned behind RequireAuth and the BOQ field
// mask. (Quotations don't carry vendor-mask-eligible fields directly
// — sell_total is customer-facing — but we still chain the mask so
// the per-route permission gating + actor-context plumbing stays
// uniform.)
type QuotationHandler struct {
	uc       port.QuotationUseCase
	verifier *auth.Verifier
}

func NewQuotationHandler(uc port.QuotationUseCase, verifier *auth.Verifier) *QuotationHandler {
	return &QuotationHandler{uc: uc, verifier: verifier}
}

// Mount — quotation routes:
//
//	GET   /quotations                          [enterprise.quotation.read]
//	GET   /quotations/{id}                     [enterprise.quotation.read]
//	GET   /quotations/{id}/pdf                 [enterprise.quotation.read]
//	POST  /quotations/from-boq/{boq_id}        [enterprise.quotation.manage]
//	POST  /quotations/{id}/accept              [enterprise.quotation.manage]
//	POST  /quotations/{id}/reject              [enterprise.quotation.manage]
//	POST  /quotations/{id}/cancel              [enterprise.quotation.manage]
func (h *QuotationHandler) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(httpserver.RequireAuth(h.verifier))
		// Quotation JSON DTOs carry the same commercial fields as BOQ —
		// sell_total, cost_total, margin_pct. Apply the same vendor mask
		// so a vendor token (if it somehow gets through RBAC) doesn't
		// see them. The /pdf binary route is OUTSIDE this group so it
		// streams as-is; vendors don't have quotation.read perm anyway.
		r.Use(BOQFieldMaskMiddleware)

		r.With(httpserver.RequirePermission("enterprise.quotation.read")).
			Get("/quotations", h.list)
		r.With(httpserver.RequirePermission("enterprise.quotation.read")).
			Get("/quotations/{id}", h.get)
		// Binary endpoint — streams application/pdf. Kept outside the
		// JSON-DTO mask middleware because there's nothing JSON-shaped
		// to strip.
		r.With(httpserver.RequirePermission("enterprise.quotation.read")).
			Get("/quotations/{id}/pdf", h.getPDF)
		r.With(httpserver.RequirePermission("enterprise.quotation.manage")).
			Post("/quotations/from-boq/{boq_id}", h.generate)
		r.With(httpserver.RequirePermission("enterprise.quotation.manage")).
			Post("/quotations/{id}/accept", h.accept)
		r.With(httpserver.RequirePermission("enterprise.quotation.manage")).
			Post("/quotations/{id}/reject", h.reject)
		r.With(httpserver.RequirePermission("enterprise.quotation.manage")).
			Post("/quotations/{id}/cancel", h.cancel)
	})
}

// =====================================================================
// Handlers
// =====================================================================

func (h *QuotationHandler) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page := parseIntDefaultLocal(q.Get("page"), 1)
	if page < 1 {
		page = 1
	}
	pageSize := parseIntDefaultLocal(q.Get("page_size"), 50)
	f := port.QuotationListFilter{
		Status: q.Get("status"),
		Limit:  pageSize,
		Offset: (page - 1) * pageSize,
	}
	if s := q.Get("boq_id"); s != "" {
		u, err := parseUUIDLocal(s, "boq_id")
		if err != nil {
			httpserver.WriteError(w, err)
			return
		}
		f.BOQVersionID = &u
	}
	if s := q.Get("opportunity_id"); s != "" {
		u, err := parseUUIDLocal(s, "opportunity_id")
		if err != nil {
			httpserver.WriteError(w, err)
			return
		}
		f.OpportunityID = &u
	}
	items, total, err := h.uc.ListQuotations(r.Context(), f)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]quotationDTO, 0, len(items))
	for _, q := range items {
		out = append(out, toQuotationDTO(q))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"items":     out,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

func (h *QuotationHandler) get(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "quotation")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	q, err := h.uc.GetQuotation(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toQuotationDTO(*q))
}

// getPDF streams the binary PDF with a strong ETag (the hash) and
// inline disposition so browsers preview rather than auto-download.
func (h *QuotationHandler) getPDF(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "quotation")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	bytes, hash, err := h.uc.GetQuotationPDF(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	// ETag using the strong format (the hash IS the byte-identity).
	// Caches that have the same ETag don't re-download — quotation
	// PDFs are immutable per version so this is cheap and correct.
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("ETag", `"`+hash+`"`)
	w.Header().Set("Content-Disposition", `inline; filename="quotation-`+id.String()[:8]+`.pdf"`)
	// Honor If-None-Match for cache revalidation.
	if r.Header.Get("If-None-Match") == `"`+hash+`"` {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(bytes)
}

func (h *QuotationHandler) generate(w http.ResponseWriter, r *http.Request) {
	boqID, err := parseUUIDLocal(chi.URLParam(r, "boq_id"), "boq")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var req generateQuotationRequest
	// Body optional — empty body means defaults.
	_ = httpserver.DecodeJSON(r, &req)
	in := port.GenerateQuotationInput{
		BOQVersionID: boqID,
		Notes:        req.Notes,
		ValidityDays: req.ValidityDays,
	}
	if uid := actorUserIDLocal(r.Context()); uid != nil {
		in.IssuedBy = uid
	}
	q, err := h.uc.GenerateQuotation(r.Context(), in)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toQuotationDTO(*q))
}

func (h *QuotationHandler) accept(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "quotation")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var req lifecycleRequest
	_ = httpserver.DecodeJSON(r, &req)
	q, err := h.uc.AcceptQuotation(r.Context(), port.AcceptQuotationInput{
		ID:         id,
		IfRevision: req.IfRevision,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toQuotationDTO(*q))
}

func (h *QuotationHandler) reject(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "quotation")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var req lifecycleRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	if req.Reason == "" {
		httpserver.WriteError(w, errors.Validation("quotation.reject_reason_required", "reason is required"))
		return
	}
	q, err := h.uc.RejectQuotation(r.Context(), port.RejectQuotationInput{
		ID:         id,
		Reason:     req.Reason,
		IfRevision: req.IfRevision,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toQuotationDTO(*q))
}

func (h *QuotationHandler) cancel(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "quotation")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var req lifecycleRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	if req.Reason == "" {
		httpserver.WriteError(w, errors.Validation("quotation.cancel_reason_required", "reason is required"))
		return
	}
	q, err := h.uc.CancelQuotation(r.Context(), port.CancelQuotationInput{
		ID:         id,
		Reason:     req.Reason,
		IfRevision: req.IfRevision,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toQuotationDTO(*q))
}

// =====================================================================
// DTOs
// =====================================================================

type quotationDTO struct {
	ID               string  `json:"id"`
	QuotationNumber  string  `json:"quotation_number"`
	VersionNo        int     `json:"version_no"`
	BOQVersionID     string  `json:"boq_version_id"`
	OpportunityID    string  `json:"opportunity_id"`
	Status           string  `json:"status"`
	SellTotal        float64 `json:"sell_total"`
	CostTotal        float64 `json:"cost_total"`
	MarginPct        float64 `json:"margin_pct"`
	Currency         string  `json:"currency"`
	PDFHash          string  `json:"pdf_hash"`
	PDFBytesSize     int     `json:"pdf_bytes_size"`
	ValidFrom        string  `json:"valid_from"`
	ValidUntil       string  `json:"valid_until"`
	IssuedAt         string  `json:"issued_at"`
	AcceptedAt       *string `json:"accepted_at,omitempty"`
	RejectedAt       *string `json:"rejected_at,omitempty"`
	CancelledAt      *string `json:"cancelled_at,omitempty"`
	SupersededAt     *string `json:"superseded_at,omitempty"`
	Notes            string  `json:"notes"`
	Revision         int     `json:"revision"`
	IssuedBy         *string `json:"issued_by,omitempty"`
	// Derived — convenience for the FE so it doesn't need to compare
	// valid_until to client clock.
	IsExpired bool   `json:"is_expired"`
	PDFURL    string `json:"pdf_url"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
}

func toQuotationDTO(q domain.Quotation) quotationDTO {
	issuedBy := (*string)(nil)
	if q.IssuedBy != nil {
		s := q.IssuedBy.String()
		issuedBy = &s
	}
	return quotationDTO{
		ID:              q.ID.String(),
		QuotationNumber: q.QuotationNumber,
		VersionNo:       q.VersionNo,
		BOQVersionID:    q.BOQVersionID.String(),
		OpportunityID:   q.OpportunityID.String(),
		Status:          string(q.Status),
		SellTotal:       q.SellTotal,
		CostTotal:       q.CostTotal,
		MarginPct:       q.MarginPct,
		Currency:        q.Currency,
		PDFHash:         q.PDFHash,
		PDFBytesSize:    q.PDFBytesSize,
		ValidFrom:       rfc3339(q.ValidFrom),
		ValidUntil:      rfc3339(q.ValidUntil),
		IssuedAt:        rfc3339(q.IssuedAt),
		AcceptedAt:      rfc3339Ptr(q.AcceptedAt),
		RejectedAt:      rfc3339Ptr(q.RejectedAt),
		CancelledAt:     rfc3339Ptr(q.CancelledAt),
		SupersededAt:    rfc3339Ptr(q.SupersededAt),
		Notes:           q.Notes,
		Revision:        q.Revision,
		IssuedBy:        issuedBy,
		IsExpired:       q.IsExpired(time.Now()),
		PDFURL:          "/api/enterprise/quotations/" + q.ID.String() + "/pdf",
		CreatedAt:       rfc3339(q.CreatedAt),
		UpdatedAt:       rfc3339(q.UpdatedAt),
	}
}

type generateQuotationRequest struct {
	Notes        string `json:"notes"`
	ValidityDays int    `json:"validity_days"`
}

type lifecycleRequest struct {
	Reason     string `json:"reason"`
	IfRevision *int   `json:"if_revision,omitempty"`
}
