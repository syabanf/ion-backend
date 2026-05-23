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

// financePolishSurface is the optional Wave 107 polish surface. It
// matches the methods declared on usecase.Service in
// usecase/finance_polish.go. Used via type assertion from the
// FinanceHandler so the FinanceUseCase port stays stable.
type financePolishSurface interface {
	SubmitPaymentProofHTTP(ctx context.Context,
		invoiceID uuid.UUID,
		fileURL, fileHash, fileName, contentType, notes string,
		fileSize int64,
		uploadedBy *uuid.UUID,
	) (*domain.PaymentProof, error)
	VerifyPaymentProofHTTP(ctx context.Context,
		proofID, byUserID uuid.UUID,
		decision, reason string,
		amount float64,
	) error
	SetInvoicePPh23(ctx context.Context, invoiceID uuid.UUID, applicable bool, amount float64) (*domain.Invoice, error)
}

// FinanceHandler is the Phase 5 HTTP surface — Invoices, Payments, EWOs.
//
// Routes:
//
//   GET    /invoices                              [enterprise.invoice.read]
//   GET    /invoices/{id}                         [enterprise.invoice.read]
//   POST   /invoices/from-quotation/{quote_id}    [enterprise.invoice.manage]
//   POST   /invoices/{id}/void                    [enterprise.invoice.manage]
//   POST   /invoices/{id}/payments                [enterprise.payment.record]
//
//   GET    /ewos                                  [enterprise.ewo.read]
//   GET    /ewos/{id}                             [enterprise.ewo.read]
//   POST   /ewos/from-quotation/{quote_id}        [enterprise.ewo.manage]
//   POST   /ewos/{id}/assign                      [enterprise.ewo.manage]
//   POST   /ewos/{id}/start                       [enterprise.ewo.manage]
//   POST   /ewos/{id}/complete                    [enterprise.ewo.manage]
//   POST   /ewos/{id}/cancel                      [enterprise.ewo.manage]
//   POST   /ewos/{id}/link-field-wo               [enterprise.ewo.manage]
type FinanceHandler struct {
	uc       port.FinanceUseCase
	verifier *auth.Verifier
}

func NewFinanceHandler(uc port.FinanceUseCase, verifier *auth.Verifier) *FinanceHandler {
	return &FinanceHandler{uc: uc, verifier: verifier}
}

func (h *FinanceHandler) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(httpserver.RequireAuth(h.verifier))
		// Field-mask covers invoice totals + payment amounts in case a
		// vendor token slips past the RBAC gate. EWO responses carry no
		// commercial fields but the middleware sits at the group level
		// for uniform defense-in-depth.
		r.Use(BOQFieldMaskMiddleware)

		// Invoices
		r.With(httpserver.RequirePermission("enterprise.invoice.read")).
			Get("/invoices", h.listInvoices)
		r.With(httpserver.RequirePermission("enterprise.invoice.read")).
			Get("/invoices/{id}", h.getInvoice)
		r.With(httpserver.RequirePermission("enterprise.invoice.manage")).
			Post("/invoices/from-quotation/{quote_id}", h.issueInvoice)
		r.With(httpserver.RequirePermission("enterprise.invoice.manage")).
			Post("/invoices/{id}/void", h.voidInvoice)
		r.With(httpserver.RequirePermission("enterprise.payment.record")).
			Post("/invoices/{id}/payments", h.recordPayment)

		// Wave 107 — Finance Client AR polish.
		r.With(httpserver.RequirePermission("enterprise.payment.record")).
			Post("/invoices/{id}/payment-proof", h.submitPaymentProof)
		r.With(httpserver.RequirePermission("enterprise.payment.record")).
			Post("/payment-proofs/{id}/verify", h.verifyPaymentProof)
		r.With(httpserver.RequirePermission("enterprise.invoice.manage")).
			Post("/invoices/{id}/pph23", h.setInvoicePPh23)

		// EWOs
		r.With(httpserver.RequirePermission("enterprise.ewo.read")).
			Get("/ewos", h.listEWOs)
		r.With(httpserver.RequirePermission("enterprise.ewo.read")).
			Get("/ewos/{id}", h.getEWO)
		r.With(httpserver.RequirePermission("enterprise.ewo.manage")).
			Post("/ewos/from-quotation/{quote_id}", h.createEWO)
		r.With(httpserver.RequirePermission("enterprise.ewo.manage")).
			Post("/ewos/{id}/assign", h.assignEWO)
		r.With(httpserver.RequirePermission("enterprise.ewo.manage")).
			Post("/ewos/{id}/start", h.startEWO)
		r.With(httpserver.RequirePermission("enterprise.ewo.manage")).
			Post("/ewos/{id}/complete", h.completeEWO)
		r.With(httpserver.RequirePermission("enterprise.ewo.manage")).
			Post("/ewos/{id}/cancel", h.cancelEWO)
		r.With(httpserver.RequirePermission("enterprise.ewo.manage")).
			Post("/ewos/{id}/link-field-wo", h.linkEWOFieldWO)
	})
}

// =====================================================================
// Invoice handlers
// =====================================================================

func (h *FinanceHandler) listInvoices(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page := parseIntDefaultLocal(q.Get("page"), 1)
	if page < 1 {
		page = 1
	}
	pageSize := parseIntDefaultLocal(q.Get("page_size"), 50)
	f := port.InvoiceListFilter{
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
	if s := q.Get("quotation_id"); s != "" {
		u, err := parseUUIDLocal(s, "quotation_id")
		if err != nil {
			httpserver.WriteError(w, err)
			return
		}
		f.QuotationID = &u
	}
	items, total, err := h.uc.ListInvoices(r.Context(), f)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]invoiceDTO, 0, len(items))
	for _, inv := range items {
		out = append(out, toInvoiceDTO(inv))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"items":     out,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

func (h *FinanceHandler) getInvoice(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "invoice")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	inv, payments, err := h.uc.GetInvoice(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"invoice":  toInvoiceDTO(*inv),
		"payments": toPaymentDTOs(payments),
	})
}

func (h *FinanceHandler) issueInvoice(w http.ResponseWriter, r *http.Request) {
	quoteID, err := parseUUIDLocal(chi.URLParam(r, "quote_id"), "quotation")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var req issueInvoiceRequest
	_ = httpserver.DecodeJSON(r, &req)
	in := port.IssueInvoiceInput{
		QuotationID: quoteID,
		DueDays:     req.DueDays,
		Notes:       req.Notes,
		IssuedBy:    actorUserIDLocal(r.Context()),
	}
	inv, err := h.uc.IssueInvoice(r.Context(), in)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toInvoiceDTO(*inv))
}

func (h *FinanceHandler) voidInvoice(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "invoice")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var req voidInvoiceRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	if req.Reason == "" {
		httpserver.WriteError(w, errors.Validation("invoice.void_reason_required", "reason is required"))
		return
	}
	inv, err := h.uc.VoidInvoice(r.Context(), port.VoidInvoiceInput{
		InvoiceID:  id,
		Reason:     req.Reason,
		IfRevision: req.IfRevision,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toInvoiceDTO(*inv))
}

func (h *FinanceHandler) recordPayment(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "invoice")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var req recordPaymentRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	if req.Amount <= 0 {
		httpserver.WriteError(w, errors.Validation("payment.amount_invalid", "amount must be > 0"))
		return
	}
	in := port.RecordPaymentInput{
		InvoiceID:  id,
		Amount:     req.Amount,
		Method:     req.Method,
		Reference:  req.Reference,
		Notes:      req.Notes,
		PaidAt:     req.PaidAt,
		RecordedBy: actorUserIDLocal(r.Context()),
	}
	p, inv, err := h.uc.RecordPayment(r.Context(), in)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, map[string]any{
		"payment": toPaymentDTO(*p),
		"invoice": toInvoiceDTO(*inv),
	})
}

// =====================================================================
// EWO handlers
// =====================================================================

func (h *FinanceHandler) listEWOs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page := parseIntDefaultLocal(q.Get("page"), 1)
	if page < 1 {
		page = 1
	}
	pageSize := parseIntDefaultLocal(q.Get("page_size"), 50)
	f := port.EWOListFilter{
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
	if s := q.Get("quotation_id"); s != "" {
		u, err := parseUUIDLocal(s, "quotation_id")
		if err != nil {
			httpserver.WriteError(w, err)
			return
		}
		f.QuotationID = &u
	}
	if s := q.Get("assigned_to"); s != "" {
		u, err := parseUUIDLocal(s, "assigned_to")
		if err != nil {
			httpserver.WriteError(w, err)
			return
		}
		f.AssignedTo = &u
	}
	items, total, err := h.uc.ListEWOs(r.Context(), f)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]ewoDTO, 0, len(items))
	for _, e := range items {
		out = append(out, toEWODTO(e))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"items":     out,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

func (h *FinanceHandler) getEWO(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "ewo")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	e, err := h.uc.GetEWO(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toEWODTO(*e))
}

func (h *FinanceHandler) createEWO(w http.ResponseWriter, r *http.Request) {
	quoteID, err := parseUUIDLocal(chi.URLParam(r, "quote_id"), "quotation")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var req createEWORequest
	_ = httpserver.DecodeJSON(r, &req)
	e, err := h.uc.CreateEWO(r.Context(), port.CreateEWOInput{
		QuotationID: quoteID,
		Notes:       req.Notes,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toEWODTO(*e))
}

func (h *FinanceHandler) assignEWO(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "ewo")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var req assignEWORequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	userID, err := parseUUIDLocal(req.AssignedTo, "assigned_to")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	e, err := h.uc.AssignEWO(r.Context(), port.AssignEWOInput{
		EWOID:      id,
		AssignedTo: userID,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toEWODTO(*e))
}

func (h *FinanceHandler) startEWO(w http.ResponseWriter, r *http.Request) {
	h.simpleEWOTransition(w, r, "start")
}
func (h *FinanceHandler) completeEWO(w http.ResponseWriter, r *http.Request) {
	h.simpleEWOTransition(w, r, "complete")
}
func (h *FinanceHandler) cancelEWO(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "ewo")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var req cancelEWORequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	if req.Reason == "" {
		httpserver.WriteError(w, errors.Validation("ewo.cancel_reason_required", "reason is required"))
		return
	}
	e, err := h.uc.CancelEWO(r.Context(), port.CancelEWOInput{
		EWOID:  id,
		Reason: req.Reason,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toEWODTO(*e))
}

// linkEWOFieldWO — operator binds an enterprise EWO to a field-module
// work order. The field WO ID is a soft FK: the field module owns its
// own schema in a separate service, so we don't validate existence
// here. Idempotent — re-linking simply overwrites.
func (h *FinanceHandler) linkEWOFieldWO(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "ewo")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var req linkFieldWORequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	fieldWO, err := parseUUIDLocal(req.FieldWorkOrderID, "field_work_order_id")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	e, err := h.uc.LinkEWOToFieldWO(r.Context(), id, fieldWO)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toEWODTO(*e))
}

// simpleEWOTransition handles start/complete (no body required).
func (h *FinanceHandler) simpleEWOTransition(
	w http.ResponseWriter, r *http.Request, action string,
) {
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "ewo")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var (
		e   *domain.EWO
		uce error
	)
	switch action {
	case "start":
		e, uce = h.uc.StartEWO(r.Context(), id)
	case "complete":
		e, uce = h.uc.CompleteEWO(r.Context(), id)
	default:
		httpserver.WriteError(w, errors.Validation("ewo.invalid_action", "unknown action"))
		return
	}
	if uce != nil {
		httpserver.WriteError(w, uce)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toEWODTO(*e))
}

// =====================================================================
// DTOs
// =====================================================================

type invoiceDTO struct {
	ID                string  `json:"id"`
	InvoiceNumber     string  `json:"invoice_number"`
	QuotationID       string  `json:"quotation_id"`
	OpportunityID     string  `json:"opportunity_id"`
	BOQVersionID      string  `json:"boq_version_id"`
	Status            string  `json:"status"`
	TotalAmount       float64 `json:"total_amount"`
	SubtotalAmount    float64 `json:"subtotal_amount"`
	TaxPct            float64 `json:"tax_pct"`
	TaxAmount         float64 `json:"tax_amount"`
	PaidAmount        float64 `json:"paid_amount"`
	Balance           float64 `json:"balance"`
	Currency          string  `json:"currency"`
	IssuedAt          string  `json:"issued_at"`
	DueAt             string  `json:"due_at"`
	PaidAt            *string `json:"paid_at,omitempty"`
	VoidedAt          *string `json:"voided_at,omitempty"`
	VoidReason        string  `json:"void_reason"`
	Notes             string  `json:"notes"`
	IssuedBy          *string `json:"issued_by,omitempty"`
	InvoicePlanID     *string `json:"invoice_plan_id,omitempty"`
	InvoicePlanItemID *string `json:"invoice_plan_item_id,omitempty"`
	Revision          int     `json:"revision"`
	CreatedAt         string  `json:"created_at"`
	UpdatedAt         string  `json:"updated_at"`
}

func toInvoiceDTO(inv domain.Invoice) invoiceDTO {
	var issuedBy *string
	if inv.IssuedBy != nil {
		s := inv.IssuedBy.String()
		issuedBy = &s
	}
	var planID, planItemID *string
	if inv.InvoicePlanID != nil {
		s := inv.InvoicePlanID.String()
		planID = &s
	}
	if inv.InvoicePlanItemID != nil {
		s := inv.InvoicePlanItemID.String()
		planItemID = &s
	}
	return invoiceDTO{
		ID:                inv.ID.String(),
		InvoiceNumber:     inv.InvoiceNumber,
		QuotationID:       inv.QuotationID.String(),
		OpportunityID:     inv.OpportunityID.String(),
		BOQVersionID:      inv.BOQVersionID.String(),
		Status:            string(inv.Status),
		TotalAmount:       inv.TotalAmount,
		SubtotalAmount:    inv.SubtotalAmount,
		TaxPct:            inv.TaxPct,
		TaxAmount:         inv.TaxAmount,
		PaidAmount:        inv.PaidAmount,
		Balance:           inv.Balance(),
		Currency:          inv.Currency,
		IssuedAt:          rfc3339(inv.IssuedAt),
		DueAt:             rfc3339(inv.DueAt),
		PaidAt:            rfc3339Ptr(inv.PaidAt),
		VoidedAt:          rfc3339Ptr(inv.VoidedAt),
		VoidReason:        inv.VoidReason,
		Notes:             inv.Notes,
		IssuedBy:          issuedBy,
		InvoicePlanID:     planID,
		InvoicePlanItemID: planItemID,
		Revision:          inv.Revision,
		CreatedAt:         rfc3339(inv.CreatedAt),
		UpdatedAt:         rfc3339(inv.UpdatedAt),
	}
}

type paymentDTO struct {
	ID         string  `json:"id"`
	InvoiceID  string  `json:"invoice_id"`
	Amount     float64 `json:"amount"`
	Method     string  `json:"method"`
	Reference  string  `json:"reference"`
	PaidAt     string  `json:"paid_at"`
	Notes      string  `json:"notes"`
	RecordedBy *string `json:"recorded_by,omitempty"`
	CreatedAt  string  `json:"created_at"`
}

func toPaymentDTO(p domain.InvoicePayment) paymentDTO {
	var recordedBy *string
	if p.RecordedBy != nil {
		s := p.RecordedBy.String()
		recordedBy = &s
	}
	return paymentDTO{
		ID:         p.ID.String(),
		InvoiceID:  p.InvoiceID.String(),
		Amount:     p.Amount,
		Method:     string(p.Method),
		Reference:  p.Reference,
		PaidAt:     rfc3339(p.PaidAt),
		Notes:      p.Notes,
		RecordedBy: recordedBy,
		CreatedAt:  rfc3339(p.CreatedAt),
	}
}

func toPaymentDTOs(in []domain.InvoicePayment) []paymentDTO {
	out := make([]paymentDTO, 0, len(in))
	for _, p := range in {
		out = append(out, toPaymentDTO(p))
	}
	return out
}

type ewoDTO struct {
	ID               string  `json:"id"`
	EWONumber        string  `json:"ewo_number"`
	QuotationID      string  `json:"quotation_id"`
	OpportunityID    string  `json:"opportunity_id"`
	BOQVersionID     string  `json:"boq_version_id"`
	Status           string  `json:"status"`
	AssignedTo       *string `json:"assigned_to,omitempty"`
	StartedAt        *string `json:"started_at,omitempty"`
	CompletedAt      *string `json:"completed_at,omitempty"`
	CancelledAt      *string `json:"cancelled_at,omitempty"`
	CancelReason     string  `json:"cancel_reason"`
	Notes            string  `json:"notes"`
	FieldWorkOrderID *string `json:"field_work_order_id,omitempty"`
	Revision         int     `json:"revision"`
	CreatedAt        string  `json:"created_at"`
	UpdatedAt        string  `json:"updated_at"`

	// Wave 96 — dual EWO + scheduling. Added fields; pre-existing
	// field names left untouched. side="x" for legacy single-EWO rows.
	Side                     string  `json:"side"`
	ExecutingSubsidiaryID    *string `json:"executing_subsidiary_id,omitempty"`
	IntercompanyPOID         *string `json:"intercompany_po_id,omitempty"`
	PairedEWOID              *string `json:"paired_ewo_id,omitempty"`
	ScheduledStart           *string `json:"scheduled_start_date,omitempty"`
	ScheduledEnd             *string `json:"scheduled_end_date,omitempty"`
	DurationDays             *int    `json:"duration_days,omitempty"`
	AssignedTechnicianUserID *string `json:"assigned_technician_user_id,omitempty"`
	AssignedTeamLeadUserID   *string `json:"assigned_team_lead_user_id,omitempty"`
	ScheduleLocked           bool    `json:"schedule_locked"`
}

func toEWODTO(e domain.EWO) ewoDTO {
	var assignedTo *string
	if e.AssignedTo != nil {
		s := e.AssignedTo.String()
		assignedTo = &s
	}
	var fieldWO *string
	if e.FieldWorkOrderID != nil {
		s := e.FieldWorkOrderID.String()
		fieldWO = &s
	}
	d := ewoDTO{
		ID:               e.ID.String(),
		EWONumber:        e.EWONumber,
		QuotationID:      e.QuotationID.String(),
		OpportunityID:    e.OpportunityID.String(),
		BOQVersionID:     e.BOQVersionID.String(),
		Status:           string(e.Status),
		AssignedTo:       assignedTo,
		StartedAt:        rfc3339Ptr(e.StartedAt),
		CompletedAt:      rfc3339Ptr(e.CompletedAt),
		CancelledAt:      rfc3339Ptr(e.CancelledAt),
		CancelReason:     e.CancelReason,
		Notes:            e.Notes,
		FieldWorkOrderID: fieldWO,
		Revision:         e.Revision,
		CreatedAt:        rfc3339(e.CreatedAt),
		UpdatedAt:        rfc3339(e.UpdatedAt),
		Side:             string(e.Side),
		ScheduleLocked:   e.ScheduleLocked,
		DurationDays:     e.DurationDays,
	}
	if d.Side == "" {
		// Defend against zero-value side from older code paths — the
		// migration backfills 'x' for legacy rows but a freshly
		// constructed EWO without going through NewEWO would have ""
		// here, which the FE shouldn't see.
		d.Side = "x"
	}
	if e.ExecutingSubsidiaryID != nil {
		s := e.ExecutingSubsidiaryID.String()
		d.ExecutingSubsidiaryID = &s
	}
	if e.IntercompanyPOID != nil {
		s := e.IntercompanyPOID.String()
		d.IntercompanyPOID = &s
	}
	if e.PairedEWOID != nil {
		s := e.PairedEWOID.String()
		d.PairedEWOID = &s
	}
	if e.ScheduledStartDate != nil {
		s := rfc3339(*e.ScheduledStartDate)
		d.ScheduledStart = &s
	}
	if e.ScheduledEndDate != nil {
		s := rfc3339(*e.ScheduledEndDate)
		d.ScheduledEnd = &s
	}
	if e.AssignedTechnicianUserID != nil {
		s := e.AssignedTechnicianUserID.String()
		d.AssignedTechnicianUserID = &s
	}
	if e.AssignedTeamLeadUserID != nil {
		s := e.AssignedTeamLeadUserID.String()
		d.AssignedTeamLeadUserID = &s
	}
	return d
}

// =====================================================================
// Requests
// =====================================================================

type issueInvoiceRequest struct {
	DueDays int    `json:"due_days"`
	Notes   string `json:"notes"`
}

type voidInvoiceRequest struct {
	Reason     string `json:"reason"`
	IfRevision *int   `json:"if_revision,omitempty"`
}

type recordPaymentRequest struct {
	Amount    float64 `json:"amount"`
	Method    string  `json:"method"`
	Reference string  `json:"reference"`
	PaidAt    string  `json:"paid_at"`
	Notes     string  `json:"notes"`
}

type createEWORequest struct {
	Notes string `json:"notes"`
}

type assignEWORequest struct {
	AssignedTo string `json:"assigned_to"`
}

type cancelEWORequest struct {
	Reason string `json:"reason"`
}

type linkFieldWORequest struct {
	FieldWorkOrderID string `json:"field_work_order_id"`
}

// =====================================================================
// Wave 107 — Finance polish handlers (payment proof + PPh23).
//
// We dispatch via type-assertion against the underlying Service rather
// than extending the FinanceUseCase port. That keeps the change additive
// — the existing port stays a stable contract; the polish methods are
// optional extensions reachable only when wired via the canonical
// Service.
// =====================================================================

type submitPaymentProofRequest struct {
	FileURL     string `json:"file_url"`
	FileHash    string `json:"file_hash,omitempty"`
	FileName    string `json:"file_name,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	FileSize    int64  `json:"file_size,omitempty"`
	Notes       string `json:"notes,omitempty"`
}

type verifyPaymentProofRequest struct {
	Decision string  `json:"decision"` // "approved" | "rejected"
	Reason   string  `json:"reason,omitempty"`
	Amount   float64 `json:"amount,omitempty"`
}

type setPPh23Request struct {
	Applicable bool    `json:"applicable"`
	Amount     float64 `json:"amount,omitempty"`
}

func (h *FinanceHandler) submitPaymentProof(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "invoice")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var req submitPaymentProofRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	uc, ok := h.uc.(financePolishSurface)
	if !ok {
		httpserver.WriteError(w, errors.Wrap(errors.KindInternal, "finance.polish_not_wired", "polish surface unavailable", nil))
		return
	}
	proof, err := uc.SubmitPaymentProofHTTP(
		r.Context(),
		id,
		req.FileURL, req.FileHash, req.FileName, req.ContentType, req.Notes,
		req.FileSize,
		actorUserIDLocal(r.Context()),
	)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, map[string]any{
		"id":        proof.ID.String(),
		"file_url":  proof.FileURL,
		"file_name": proof.FileName,
		"created":   proof.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	})
}

func (h *FinanceHandler) verifyPaymentProof(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "proof")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var req verifyPaymentProofRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	actor := actorUserIDLocal(r.Context())
	if actor == nil {
		httpserver.WriteError(w, errors.Forbidden("payment_proof.actor_required", "actor required"))
		return
	}
	uc, ok := h.uc.(financePolishSurface)
	if !ok {
		httpserver.WriteError(w, errors.Wrap(errors.KindInternal, "finance.polish_not_wired", "polish surface unavailable", nil))
		return
	}
	if err := uc.VerifyPaymentProofHTTP(
		r.Context(), id, *actor,
		req.Decision, req.Reason, req.Amount,
	); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *FinanceHandler) setInvoicePPh23(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "invoice")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var req setPPh23Request
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	uc, ok := h.uc.(financePolishSurface)
	if !ok {
		httpserver.WriteError(w, errors.Wrap(errors.KindInternal, "finance.polish_not_wired", "polish surface unavailable", nil))
		return
	}
	inv, err := uc.SetInvoicePPh23(r.Context(), id, req.Applicable, req.Amount)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toInvoiceDTO(*inv))
}
