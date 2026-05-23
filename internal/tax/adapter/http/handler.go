// Package http is the driving adapter for the tax bounded context —
// translates HTTP into UseCase calls. Same conventions as the
// enterprise / warehouse HTTP adapters.
//
// Routes are mounted under `/api/tax` so the routing tree is
// predictable when this lives behind the api-gateway.
package http

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/tax/domain"
	"github.com/ion-core/backend/internal/tax/port"
	"github.com/ion-core/backend/pkg/auth"
	"github.com/ion-core/backend/pkg/errors"
	"github.com/ion-core/backend/pkg/httpserver"
)

// Handler wires the chi router into the tax UseCase. Construct with
// NewHandler + call Mount on the service's top-level chi.Router.
type Handler struct {
	uc       port.UseCase
	verifier *auth.Verifier
}

func NewHandler(uc port.UseCase, verifier *auth.Verifier) *Handler {
	return &Handler{uc: uc, verifier: verifier}
}

// Mount — route map:
//
//	GET    /api/tax/profiles?subsidiary_id=...   [tax.profile.read]
//	POST   /api/tax/profiles                     [tax.profile.write]
//	GET    /api/tax/profiles/{id}                [tax.profile.read]
//	POST   /api/tax/faktur                       [tax.faktur.write]
//	POST   /api/tax/faktur/{id}/submit           [tax.faktur.write]
//
// All routes require auth (RequireAuth) and the listed permission key.
func (h *Handler) Mount(r chi.Router) {
	r.Route("/api/tax", func(r chi.Router) {
		r.Use(httpserver.RequireAuth(h.verifier))

		r.With(httpserver.RequirePermission("tax.profile.read")).
			Get("/profiles", h.listProfiles)
		r.With(httpserver.RequirePermission("tax.profile.write")).
			Post("/profiles", h.createProfile)
		r.With(httpserver.RequirePermission("tax.profile.read")).
			Get("/profiles/{id}", h.getProfile)

		r.With(httpserver.RequirePermission("tax.faktur.write")).
			Post("/faktur", h.issueFaktur)
		r.With(httpserver.RequirePermission("tax.faktur.write")).
			Post("/faktur/{id}/submit", h.submitFaktur)
	})
}

// =====================================================================
// Profile handlers
// =====================================================================

func (h *Handler) listProfiles(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page := httpserver.ParseIntDefault(q.Get("page"), 1)
	if page < 1 {
		page = 1
	}
	pageSize := httpserver.ParseIntDefault(q.Get("page_size"), 50)
	f := port.CompanyTaxProfileFilter{
		Limit:  pageSize,
		Offset: (page - 1) * pageSize,
	}
	if s := q.Get("subsidiary_id"); s != "" {
		u, err := parseUUID(s, "subsidiary_id")
		if err != nil {
			httpserver.WriteError(w, err)
			return
		}
		f.SubsidiaryID = &u
	}

	// "active=true" + subsidiary_id collapses the response to the
	// single active profile (the common consumer pattern — invoice
	// generation needs one row, not a list).
	if q.Get("active") == "true" && f.SubsidiaryID != nil {
		at := time.Now().UTC()
		if s := q.Get("at"); s != "" {
			t, err := time.Parse(time.RFC3339, s)
			if err != nil {
				httpserver.WriteError(w, errors.Validation(
					"tax.at_invalid", "at must be an RFC 3339 timestamp"))
				return
			}
			at = t.UTC()
		}
		p, err := h.uc.GetActiveProfile(r.Context(), *f.SubsidiaryID, at)
		if err != nil {
			httpserver.WriteError(w, err)
			return
		}
		httpserver.WriteJSON(w, http.StatusOK, map[string]any{
			"items":     []companyTaxProfileDTO{toProfileDTO(*p)},
			"total":     1,
			"page":      1,
			"page_size": 1,
		})
		return
	}

	items, total, err := h.uc.ListProfiles(r.Context(), f)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]companyTaxProfileDTO, 0, len(items))
	for _, p := range items {
		out = append(out, toProfileDTO(p))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"items":     out,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

func (h *Handler) getProfile(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "tax_profile")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	p, err := h.uc.GetProfile(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toProfileDTO(*p))
}

func (h *Handler) createProfile(w http.ResponseWriter, r *http.Request) {
	var req createProfileRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	subID, err := parseUUID(req.SubsidiaryID, "subsidiary_id")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	in := port.CreateCompanyTaxProfileInput{
		SubsidiaryID:  subID,
		Name:          req.Name,
		NPWP:          req.NPWP,
		IsPKP:         req.IsPKP,
		PPNRate:       req.PPNRate,
		PPh23Rate:     req.PPh23Rate,
		PPhFinalRate:  req.PPhFinalRate,
		EffectiveFrom: req.EffectiveFrom,
		EffectiveTo:   req.EffectiveTo,
	}
	p, err := h.uc.CreateProfile(r.Context(), in)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toProfileDTO(*p))
}

// =====================================================================
// Faktur handlers
// =====================================================================

func (h *Handler) issueFaktur(w http.ResponseWriter, r *http.Request) {
	var req issueFakturRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	invID, err := parseUUID(req.InvoiceID, "invoice_id")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	subID, err := parseUUID(req.SubsidiaryID, "subsidiary_id")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	in := port.IssueFakturInput{
		InvoiceID:    invID,
		SubsidiaryID: subID,
		JenisFaktur:  req.JenisFaktur,
		NPWPLawan:    req.NPWPLawan,
		DPP:          req.DPP,
		PPN:          req.PPN,
	}
	f, err := h.uc.IssueFakturForInvoice(r.Context(), in)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toFakturDTO(*f))
}

func (h *Handler) submitFaktur(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "faktur")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	f, err := h.uc.SubmitFaktur(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toFakturDTO(*f))
}

// =====================================================================
// DTOs (kept in same package, per the wave brief)
// =====================================================================

type companyTaxProfileDTO struct {
	ID            string  `json:"id"`
	SubsidiaryID  string  `json:"subsidiary_id"`
	Name          string  `json:"name"`
	NPWP          string  `json:"npwp"`
	IsPKP         bool    `json:"is_pkp"`
	PPNRate       float64 `json:"ppn_rate"`
	PPh23Rate     float64 `json:"pph23_rate"`
	PPhFinalRate  float64 `json:"pph_final_rate"`
	EffectiveFrom string  `json:"effective_from"`
	EffectiveTo   *string `json:"effective_to,omitempty"`
	CreatedAt     string  `json:"created_at"`
	UpdatedAt     string  `json:"updated_at"`
}

type createProfileRequest struct {
	SubsidiaryID  string  `json:"subsidiary_id"`
	Name          string  `json:"name"`
	NPWP          string  `json:"npwp"`
	IsPKP         bool    `json:"is_pkp"`
	PPNRate       float64 `json:"ppn_rate"`
	PPh23Rate     float64 `json:"pph23_rate"`
	PPhFinalRate  float64 `json:"pph_final_rate"`
	EffectiveFrom string  `json:"effective_from"`
	EffectiveTo   string  `json:"effective_to"`
}

type fakturPajakDTO struct {
	ID                 string   `json:"id"`
	InvoiceID          string   `json:"invoice_id"`
	SubsidiaryID       *string  `json:"subsidiary_id,omitempty"`
	NomorSeri          string   `json:"nomor_seri"`
	JenisFaktur        string   `json:"jenis_faktur"`
	TanggalFaktur      *string  `json:"tanggal_faktur,omitempty"`
	NPWPLawanTransaksi string   `json:"npwp_lawan_transaksi"`
	DPP                float64  `json:"dpp"`
	PPN                float64  `json:"ppn"`
	Status             string   `json:"status"`
	CreatedAt          string   `json:"created_at"`
	UpdatedAt          string   `json:"updated_at"`
}

type issueFakturRequest struct {
	InvoiceID    string  `json:"invoice_id"`
	SubsidiaryID string  `json:"subsidiary_id"`
	JenisFaktur  string  `json:"jenis_faktur"`
	NPWPLawan    string  `json:"npwp_lawan_transaksi"`
	DPP          float64 `json:"dpp"`
	PPN          float64 `json:"ppn"`
}

// =====================================================================
// Mappers + helpers
// =====================================================================

func toProfileDTO(p domain.CompanyTaxProfile) companyTaxProfileDTO {
	dto := companyTaxProfileDTO{
		ID:            p.ID.String(),
		SubsidiaryID:  p.SubsidiaryID.String(),
		Name:          p.Name,
		NPWP:          p.NPWP,
		IsPKP:         p.IsPKP,
		PPNRate:       p.PPNRate,
		PPh23Rate:     p.PPh23Rate,
		PPhFinalRate:  p.PPhFinalRate,
		EffectiveFrom: p.EffectiveFrom.UTC().Format("2006-01-02"),
		CreatedAt:     httpserver.FormatRFC3339(p.CreatedAt),
		UpdatedAt:     httpserver.FormatRFC3339(p.UpdatedAt),
	}
	if p.EffectiveTo != nil {
		s := p.EffectiveTo.UTC().Format("2006-01-02")
		dto.EffectiveTo = &s
	}
	return dto
}

func toFakturDTO(f domain.FakturPajak) fakturPajakDTO {
	dto := fakturPajakDTO{
		ID:                 f.ID.String(),
		InvoiceID:          f.InvoiceID.String(),
		NomorSeri:          f.NomorSeri,
		JenisFaktur:        string(f.JenisFaktur),
		NPWPLawanTransaksi: f.NPWPLawanTransaksi,
		DPP:                f.DPP,
		PPN:                f.PPN,
		Status:             string(f.Status),
		CreatedAt:          httpserver.FormatRFC3339(f.CreatedAt),
		UpdatedAt:          httpserver.FormatRFC3339(f.UpdatedAt),
	}
	if f.SubsidiaryID != nil {
		s := f.SubsidiaryID.String()
		dto.SubsidiaryID = &s
	}
	if f.TanggalFaktur != nil {
		s := f.TanggalFaktur.UTC().Format("2006-01-02")
		dto.TanggalFaktur = &s
	}
	return dto
}

func parseUUID(s, field string) (uuid.UUID, error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil, errors.Validation(
			field+".id_invalid",
			field+" is not a valid uuid",
		)
	}
	return id, nil
}
