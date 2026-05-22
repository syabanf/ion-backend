package http

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/warehouse/domain"
	"github.com/ion-core/backend/internal/warehouse/port"
	"github.com/ion-core/backend/pkg/errors"
	"github.com/ion-core/backend/pkg/httpserver"
)

// =====================================================================
// Supplier DTOs (CRM-Sales-Enterprise PRD §5.1)
// =====================================================================

type supplierDTO struct {
	ID            string   `json:"id"`
	Code          string   `json:"code"`
	CompanyName   string   `json:"company_name"`
	ContactPerson string   `json:"contact_person"`
	Phone         string   `json:"phone"`
	Email         string   `json:"email"`
	Address       string   `json:"address"`
	PaymentTerms  string   `json:"payment_terms"`
	NPWP          string   `json:"npwp"`
	NIB           string   `json:"nib"`
	CategoryTags  []string `json:"category_tags"`
	Notes         string   `json:"notes"`
	Active        bool     `json:"active"`
	OnboardedAt   string   `json:"onboarded_at"`
	CreatedAt     string   `json:"created_at"`
	UpdatedAt     string   `json:"updated_at"`
}

func toSupplierDTO(s domain.Supplier) supplierDTO {
	tags := s.CategoryTags
	if tags == nil {
		tags = []string{}
	}
	return supplierDTO{
		ID:            s.ID.String(),
		Code:          s.Code,
		CompanyName:   s.CompanyName,
		ContactPerson: s.ContactPerson,
		Phone:         s.Phone,
		Email:         s.Email,
		Address:       s.Address,
		PaymentTerms:  s.PaymentTerms,
		NPWP:          s.NPWP,
		NIB:           s.NIB,
		CategoryTags:  tags,
		Notes:         s.Notes,
		Active:        s.Active,
		OnboardedAt:   s.OnboardedAt.Format("2006-01-02T15:04:05Z07:00"),
		CreatedAt:     s.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:     s.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

type createSupplierRequest struct {
	Code          string   `json:"code"`
	CompanyName   string   `json:"company_name"`
	ContactPerson string   `json:"contact_person"`
	Phone         string   `json:"phone"`
	Email         string   `json:"email"`
	Address       string   `json:"address"`
	PaymentTerms  string   `json:"payment_terms"`
	NPWP          string   `json:"npwp"`
	NIB           string   `json:"nib"`
	CategoryTags  []string `json:"category_tags"`
	Notes         string   `json:"notes"`
}

type updateSupplierRequest struct {
	CompanyName   *string   `json:"company_name,omitempty"`
	ContactPerson *string   `json:"contact_person,omitempty"`
	Phone         *string   `json:"phone,omitempty"`
	Email         *string   `json:"email,omitempty"`
	Address       *string   `json:"address,omitempty"`
	PaymentTerms  *string   `json:"payment_terms,omitempty"`
	NPWP          *string   `json:"npwp,omitempty"`
	NIB           *string   `json:"nib,omitempty"`
	CategoryTags  *[]string `json:"category_tags,omitempty"`
	Notes         *string   `json:"notes,omitempty"`
	Active        *bool     `json:"active,omitempty"`
}

// =====================================================================
// Handlers
// =====================================================================

func (h *Handler) listSuppliers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := parseIntDefault(q.Get("page_size"), 50)
	page := parseIntDefault(q.Get("page"), 1)
	if page < 1 {
		page = 1
	}
	f := port.SupplierListFilter{
		Search:          q.Get("q"),
		ActiveOnly:      q.Get("active_only") == "true",
		IncludeInactive: q.Get("include_inactive") == "true",
		Limit:           limit,
		Offset:          (page - 1) * limit,
	}
	out, total, err := h.uc.ListSuppliers(r.Context(), f)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	items := make([]supplierDTO, 0, len(out))
	for _, s := range out {
		items = append(items, toSupplierDTO(s))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"items":     items,
		"total":     total,
		"page":      page,
		"page_size": limit,
	})
}

func (h *Handler) getSupplier(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpserver.WriteError(w, errors.Validation("supplier.id_invalid", "id is not a valid uuid"))
		return
	}
	s, err := h.uc.GetSupplier(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toSupplierDTO(*s))
}

func (h *Handler) createSupplier(w http.ResponseWriter, r *http.Request) {
	var req createSupplierRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	s, err := h.uc.CreateSupplier(r.Context(), port.CreateSupplierInput{
		Code:          req.Code,
		CompanyName:   req.CompanyName,
		ContactPerson: req.ContactPerson,
		Phone:         req.Phone,
		Email:         req.Email,
		Address:       req.Address,
		PaymentTerms:  req.PaymentTerms,
		NPWP:          req.NPWP,
		NIB:           req.NIB,
		CategoryTags:  req.CategoryTags,
		Notes:         req.Notes,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toSupplierDTO(*s))
}

func (h *Handler) updateSupplier(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpserver.WriteError(w, errors.Validation("supplier.id_invalid", "id is not a valid uuid"))
		return
	}
	var req updateSupplierRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	s, err := h.uc.UpdateSupplier(r.Context(), port.UpdateSupplierInput{
		ID:            id,
		CompanyName:   req.CompanyName,
		ContactPerson: req.ContactPerson,
		Phone:         req.Phone,
		Email:         req.Email,
		Address:       req.Address,
		PaymentTerms:  req.PaymentTerms,
		NPWP:          req.NPWP,
		NIB:           req.NIB,
		CategoryTags:  req.CategoryTags,
		Notes:         req.Notes,
		Active:        req.Active,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toSupplierDTO(*s))
}
