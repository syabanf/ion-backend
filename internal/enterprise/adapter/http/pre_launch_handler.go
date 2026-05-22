package http

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/enterprise/domain"
	"github.com/ion-core/backend/internal/enterprise/port"
	"github.com/ion-core/backend/pkg/auth"
	"github.com/ion-core/backend/pkg/errors"
	"github.com/ion-core/backend/pkg/httpserver"
)

// PreLaunchHandler bundles the pre-launch endpoints (E5/E7/E8/E9/E11/E12).
// Kept in one file so the surface is easy to scan; each section is
// self-contained.
type PreLaunchHandler struct {
	plans       port.InvoicePlanUseCase
	verifier    *auth.Verifier
	svc         PreLaunchUseCase
}

// PreLaunchUseCase is the union the handler depends on. The concrete
// Service implements all of these.
type PreLaunchUseCase interface {
	// E7 — PO documents
	ListPODocuments(ctx context.Context, opportunityID uuid.UUID) ([]domain.PODocument, error)
	UploadPODocument(ctx context.Context, in port.UploadPODocumentInput) (*domain.PODocument, error)
	// E8 — payment proofs
	ListPaymentProofs(ctx context.Context, paymentID uuid.UUID) ([]domain.PaymentProof, error)
	UploadPaymentProof(ctx context.Context, in port.UploadPaymentProofInput) (*domain.PaymentProof, error)
	// E9 — checklist
	ListEWOChecklistItems(ctx context.Context, ewoID uuid.UUID) ([]domain.EWOChecklistItem, error)
	ReplaceEWOChecklist(ctx context.Context, in port.ReplaceEWOChecklistInput) ([]domain.EWOChecklistItem, error)
	UpdateEWOChecklistItem(ctx context.Context, in port.UpdateEWOChecklistItemInput) (*domain.EWOChecklistItem, error)
	// E11 — projects/sites/services
	ListProjects(ctx context.Context, status string, opportunityID *uuid.UUID, limit, offset int) ([]domain.Project, int, error)
	GetProject(ctx context.Context, id uuid.UUID) (*domain.Project, []domain.ProjectSite, error)
	CreateProject(ctx context.Context, in port.CreateProjectInput) (*domain.Project, error)
	ListProjectSites(ctx context.Context, projectID uuid.UUID) ([]domain.ProjectSite, error)
	CreateProjectSite(ctx context.Context, in port.CreateProjectSiteInput) (*domain.ProjectSite, error)
	ActivateProjectSite(ctx context.Context, siteID uuid.UUID) (*domain.ProjectSite, error)
	ListEnterpriseServices(ctx context.Context, siteID uuid.UUID) ([]domain.EnterpriseService, error)
	CreateEnterpriseService(ctx context.Context, in port.CreateEnterpriseServiceInput) (*domain.EnterpriseService, error)
	// E12 — RFQs
	ListRFQs(ctx context.Context, status string, opportunityID, assignedTo *uuid.UUID, limit, offset int) ([]domain.RFQ, int, error)
	GetRFQ(ctx context.Context, id uuid.UUID) (*domain.RFQ, error)
	CreateRFQ(ctx context.Context, in port.CreateRFQInput) (*domain.RFQ, error)
	AssignRFQ(ctx context.Context, in port.AssignRFQInput) (*domain.RFQ, error)
	FulfillRFQ(ctx context.Context, in port.FulfillRFQInput) (*domain.RFQ, error)
	CancelRFQ(ctx context.Context, in port.CancelRFQInput) (*domain.RFQ, error)

	// Polish — EWO checklist templates (admin) + seed-from-template + bulk PO.
	ListEWOChecklistTemplates(ctx context.Context, activeOnly bool) ([]port.EWOChecklistTemplate, error)
	GetEWOChecklistTemplate(ctx context.Context, id uuid.UUID) (*port.EWOChecklistTemplate, error)
	SaveEWOChecklistTemplate(ctx context.Context, in port.EWOChecklistTemplate) (*port.EWOChecklistTemplate, error)
	DeleteEWOChecklistTemplate(ctx context.Context, id uuid.UUID) error
	SeedEWOChecklistFromTemplate(ctx context.Context, ewoID uuid.UUID, templateCode string) ([]domain.EWOChecklistItem, error)
	BulkUploadPODocuments(ctx context.Context, batch []port.UploadPODocumentInput) ([]domain.PODocument, error)

	// Sub-company revenue ledger — scoped read for the BOQ detail page.
	ListInternalTransactionsByBOQ(ctx context.Context, boqVersionID uuid.UUID) ([]domain.InternalTransaction, error)
}

func NewPreLaunchHandler(svc PreLaunchUseCase, plans port.InvoicePlanUseCase, verifier *auth.Verifier) *PreLaunchHandler {
	return &PreLaunchHandler{svc: svc, plans: plans, verifier: verifier}
}

func (h *PreLaunchHandler) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(httpserver.RequireAuth(h.verifier))

		// E5 — invoice plans
		r.With(httpserver.RequirePermission("enterprise.invoice_plan.read")).
			Get("/invoice-plans/{id}", h.getInvoicePlan)
		r.With(httpserver.RequirePermission("enterprise.invoice_plan.read")).
			Get("/invoice-plans/by-quotation/{quotation_id}", h.getInvoicePlanByQuotation)
		r.With(httpserver.RequirePermission("enterprise.invoice_plan.manage")).
			Post("/invoice-plans/from-quotation/{quotation_id}", h.createInvoicePlan)
		r.With(httpserver.RequirePermission("enterprise.invoice_plan.manage")).
			Put("/invoice-plans/{id}/items", h.replaceInvoicePlanItems)
		r.With(httpserver.RequirePermission("enterprise.invoice_plan.manage")).
			Post("/invoice-plans/{id}/activate", h.activateInvoicePlan)
		r.With(httpserver.RequirePermission("enterprise.invoice_plan.manage")).
			Post("/invoice-plan-items/{id}/issue", h.issueTerminItem)
		r.With(httpserver.RequirePermission("enterprise.invoice_plan.manage")).
			Post("/invoice-plans/{id}/cancel", h.cancelInvoicePlan)

		// E7 — PO documents (scoped to opportunity)
		r.With(httpserver.RequirePermission("enterprise.po_document.read")).
			Get("/opportunities/{opportunity_id}/po-documents", h.listPODocuments)
		r.With(httpserver.RequirePermission("enterprise.po_document.manage")).
			Post("/opportunities/{opportunity_id}/po-documents", h.uploadPODocument)

		// E8 — payment proofs (scoped to invoice payment)
		r.With(httpserver.RequirePermission("enterprise.payment_proof.read")).
			Get("/invoice-payments/{payment_id}/proofs", h.listPaymentProofs)
		r.With(httpserver.RequirePermission("enterprise.payment_proof.manage")).
			Post("/invoice-payments/{payment_id}/proofs", h.uploadPaymentProof)

		// E9 — EWO checklist
		r.With(httpserver.RequirePermission("enterprise.ewo.read")).
			Get("/ewos/{ewo_id}/checklist", h.listEWOChecklist)
		r.With(httpserver.RequirePermission("enterprise.ewo_checklist.manage")).
			Put("/ewos/{ewo_id}/checklist", h.replaceEWOChecklist)
		r.With(httpserver.RequirePermission("enterprise.ewo_checklist.manage")).
			Patch("/ewo-checklist-items/{id}", h.updateEWOChecklistItem)

		// E11 — projects / sites / services
		r.With(httpserver.RequirePermission("enterprise.project.read")).
			Get("/projects", h.listProjects)
		r.With(httpserver.RequirePermission("enterprise.project.read")).
			Get("/projects/{id}", h.getProject)
		r.With(httpserver.RequirePermission("enterprise.project.manage")).
			Post("/projects/from-quotation/{quotation_id}", h.createProject)
		r.With(httpserver.RequirePermission("enterprise.project.read")).
			Get("/projects/{id}/sites", h.listProjectSites)
		r.With(httpserver.RequirePermission("enterprise.project.manage")).
			Post("/projects/{id}/sites", h.createProjectSite)
		r.With(httpserver.RequirePermission("enterprise.project.manage")).
			Post("/project-sites/{id}/activate", h.activateProjectSite)
		r.With(httpserver.RequirePermission("enterprise.project.read")).
			Get("/project-sites/{id}/services", h.listEnterpriseServices)
		r.With(httpserver.RequirePermission("enterprise.project.manage")).
			Post("/project-sites/{id}/services", h.createEnterpriseService)

		// Polish — bulk PO upload (one-shot multi-revision uploads).
		r.With(httpserver.RequirePermission("enterprise.po_document.manage")).
			Post("/opportunities/{opportunity_id}/po-documents/bulk", h.bulkUploadPODocuments)

		// Polish — EWO checklist templates (admin catalog).
		r.With(httpserver.RequirePermission("enterprise.ewo_checklist_template.read")).
			Get("/ewo-checklist-templates", h.listEWOChecklistTemplates)
		r.With(httpserver.RequirePermission("enterprise.ewo_checklist_template.read")).
			Get("/ewo-checklist-templates/{id}", h.getEWOChecklistTemplate)
		r.With(httpserver.RequirePermission("enterprise.ewo_checklist_template.manage")).
			Put("/ewo-checklist-templates", h.saveEWOChecklistTemplate)
		r.With(httpserver.RequirePermission("enterprise.ewo_checklist_template.manage")).
			Delete("/ewo-checklist-templates/{id}", h.deleteEWOChecklistTemplate)
		// Polish — seed an EWO's checklist from a named template (gated
		// on the manage-checklist perm, not template.read, because it
		// mutates the EWO's items rather than browsing the catalog).
		r.With(httpserver.RequirePermission("enterprise.ewo_checklist.manage")).
			Post("/ewos/{ewo_id}/checklist/from-template/{code}", h.seedEWOChecklistFromTemplate)

		// E12 — RFQs
		r.With(httpserver.RequirePermission("enterprise.rfq.read")).
			Get("/rfqs", h.listRFQs)
		r.With(httpserver.RequirePermission("enterprise.rfq.read")).
			Get("/rfqs/{id}", h.getRFQ)
		r.With(httpserver.RequirePermission("enterprise.rfq.manage")).
			Post("/rfqs", h.createRFQ)
		r.With(httpserver.RequirePermission("enterprise.rfq.manage")).
			Post("/rfqs/{id}/assign", h.assignRFQ)
		r.With(httpserver.RequirePermission("enterprise.rfq.manage")).
			Post("/rfqs/{id}/fulfill", h.fulfillRFQ)
		r.With(httpserver.RequirePermission("enterprise.rfq.manage")).
			Post("/rfqs/{id}/cancel", h.cancelRFQ)

		// Sub-company revenue ledger (read-only). Scoped by
		// boq_version_id; the BOQ detail page surfaces this beneath the
		// approval chain once a BOQ is approved.
		r.With(httpserver.RequirePermission("enterprise.internal_transaction.read")).
			Get("/internal-transactions", h.listInternalTransactions)
	})
}

// =====================================================================
// E5 — invoice plan handlers
// =====================================================================

func (h *PreLaunchHandler) getInvoicePlan(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "invoice_plan")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	p, items, err := h.plans.GetInvoicePlan(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"plan":  toInvoicePlanDTO(*p),
		"items": toInvoicePlanItemDTOs(items),
	})
}

func (h *PreLaunchHandler) getInvoicePlanByQuotation(w http.ResponseWriter, r *http.Request) {
	qid, err := parseUUIDLocal(chi.URLParam(r, "quotation_id"), "quotation")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	p, items, err := h.plans.GetInvoicePlanByQuotation(r.Context(), qid)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"plan":  toInvoicePlanDTO(*p),
		"items": toInvoicePlanItemDTOs(items),
	})
}

type createInvoicePlanRequest struct {
	Notes string `json:"notes"`
}

func (h *PreLaunchHandler) createInvoicePlan(w http.ResponseWriter, r *http.Request) {
	qid, err := parseUUIDLocal(chi.URLParam(r, "quotation_id"), "quotation")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var req createInvoicePlanRequest
	_ = httpserver.DecodeJSON(r, &req)
	p, err := h.plans.CreateInvoicePlan(r.Context(), port.CreateInvoicePlanInput{
		QuotationID: qid,
		Notes:       req.Notes,
		CreatedBy:   actorUserIDLocal(r.Context()),
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toInvoicePlanDTO(*p))
}

type replaceInvoicePlanItemsRequest struct {
	Items []port.InvoicePlanItemInput `json:"items"`
}

func (h *PreLaunchHandler) replaceInvoicePlanItems(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "invoice_plan")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var req replaceInvoicePlanItemsRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	items, err := h.plans.ReplaceInvoicePlanItems(r.Context(), port.ReplaceInvoicePlanItemsInput{
		PlanID: id, Items: req.Items,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"items": toInvoicePlanItemDTOs(items),
	})
}

func (h *PreLaunchHandler) activateInvoicePlan(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "invoice_plan")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	p, err := h.plans.ActivateInvoicePlan(r.Context(), port.ActivateInvoicePlanInput{PlanID: id})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toInvoicePlanDTO(*p))
}

func (h *PreLaunchHandler) issueTerminItem(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "invoice_plan_item")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	inv, err := h.plans.IssueTerminItem(r.Context(), port.IssueTerminItemInput{
		ItemID:   id,
		IssuedBy: actorUserIDLocal(r.Context()),
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toInvoiceDTO(*inv))
}

type cancelReasonRequest struct {
	Reason string `json:"reason"`
}

func (h *PreLaunchHandler) cancelInvoicePlan(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "invoice_plan")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var req cancelReasonRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	if req.Reason == "" {
		httpserver.WriteError(w, errors.Validation("invoice_plan.reason_required", "reason is required"))
		return
	}
	p, err := h.plans.CancelInvoicePlan(r.Context(), port.CancelInvoicePlanInput{PlanID: id, Reason: req.Reason})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toInvoicePlanDTO(*p))
}

// =====================================================================
// E7 — PO docs
// =====================================================================

type uploadPORequest struct {
	PONumber      string `json:"po_number"`
	FileURL       string `json:"file_url"`
	FileName      string `json:"file_name"`
	FileSizeBytes int64  `json:"file_size_bytes"`
	ContentType   string `json:"content_type"`
	IssuedByPIC   string `json:"issued_by_pic"`
	Notes         string `json:"notes"`
}

func (h *PreLaunchHandler) listPODocuments(w http.ResponseWriter, r *http.Request) {
	opp, err := parseUUIDLocal(chi.URLParam(r, "opportunity_id"), "opportunity")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	docs, err := h.svc.ListPODocuments(r.Context(), opp)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]poDocumentDTO, 0, len(docs))
	for _, d := range docs {
		out = append(out, toPODocumentDTO(d))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (h *PreLaunchHandler) uploadPODocument(w http.ResponseWriter, r *http.Request) {
	opp, err := parseUUIDLocal(chi.URLParam(r, "opportunity_id"), "opportunity")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var req uploadPORequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	d, err := h.svc.UploadPODocument(r.Context(), port.UploadPODocumentInput{
		OpportunityID: opp,
		PONumber:      req.PONumber,
		FileURL:       req.FileURL,
		FileName:      req.FileName,
		FileSizeBytes: req.FileSizeBytes,
		ContentType:   req.ContentType,
		IssuedByPIC:   req.IssuedByPIC,
		Notes:         req.Notes,
		UploadedBy:    actorUserIDLocal(r.Context()),
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toPODocumentDTO(*d))
}

// =====================================================================
// E8 — payment proofs
// =====================================================================

type uploadPaymentProofRequest struct {
	FileURL       string `json:"file_url"`
	FileName      string `json:"file_name"`
	FileSizeBytes int64  `json:"file_size_bytes"`
	ContentType   string `json:"content_type"`
	Notes         string `json:"notes"`
}

func (h *PreLaunchHandler) listPaymentProofs(w http.ResponseWriter, r *http.Request) {
	pid, err := parseUUIDLocal(chi.URLParam(r, "payment_id"), "invoice_payment")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	proofs, err := h.svc.ListPaymentProofs(r.Context(), pid)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]paymentProofDTO, 0, len(proofs))
	for _, p := range proofs {
		out = append(out, toPaymentProofDTO(p))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (h *PreLaunchHandler) uploadPaymentProof(w http.ResponseWriter, r *http.Request) {
	pid, err := parseUUIDLocal(chi.URLParam(r, "payment_id"), "invoice_payment")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var req uploadPaymentProofRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	p, err := h.svc.UploadPaymentProof(r.Context(), port.UploadPaymentProofInput{
		InvoicePaymentID: pid,
		FileURL:          req.FileURL,
		FileName:         req.FileName,
		FileSizeBytes:    req.FileSizeBytes,
		ContentType:      req.ContentType,
		Notes:            req.Notes,
		UploadedBy:       actorUserIDLocal(r.Context()),
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toPaymentProofDTO(*p))
}

// =====================================================================
// E9 — EWO checklist
// =====================================================================

type replaceChecklistRequest struct {
	Items []port.EWOChecklistItemInput `json:"items"`
}

type updateChecklistItemRequest struct {
	Status string `json:"status"`
	Notes  string `json:"notes"`
}

func (h *PreLaunchHandler) listEWOChecklist(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDLocal(chi.URLParam(r, "ewo_id"), "ewo")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	items, err := h.svc.ListEWOChecklistItems(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]ewoChecklistItemDTO, 0, len(items))
	for _, it := range items {
		out = append(out, toEWOChecklistItemDTO(it))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"items":        out,
		"progress_pct": domain.EWOProgress(items),
	})
}

func (h *PreLaunchHandler) replaceEWOChecklist(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDLocal(chi.URLParam(r, "ewo_id"), "ewo")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var req replaceChecklistRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	items, err := h.svc.ReplaceEWOChecklist(r.Context(), port.ReplaceEWOChecklistInput{
		EWOID: id, Items: req.Items,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]ewoChecklistItemDTO, 0, len(items))
	for _, it := range items {
		out = append(out, toEWOChecklistItemDTO(it))
	}
	httpserver.WriteJSON(w, http.StatusCreated, map[string]any{"items": out})
}

func (h *PreLaunchHandler) updateEWOChecklistItem(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "ewo_checklist_item")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var req updateChecklistItemRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	it, err := h.svc.UpdateEWOChecklistItem(r.Context(), port.UpdateEWOChecklistItemInput{
		ItemID:      id,
		Status:      req.Status,
		Notes:       req.Notes,
		CompletedBy: actorUserIDLocal(r.Context()),
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toEWOChecklistItemDTO(*it))
}

// =====================================================================
// E11 — projects / sites / services
// =====================================================================

func (h *PreLaunchHandler) listProjects(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page := parseIntDefaultLocal(q.Get("page"), 1)
	pageSize := parseIntDefaultLocal(q.Get("page_size"), 50)
	if page < 1 {
		page = 1
	}
	var oppID *uuid.UUID
	if s := q.Get("opportunity_id"); s != "" {
		u, err := parseUUIDLocal(s, "opportunity_id")
		if err != nil {
			httpserver.WriteError(w, err)
			return
		}
		oppID = &u
	}
	items, total, err := h.svc.ListProjects(r.Context(), q.Get("status"), oppID, pageSize, (page-1)*pageSize)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]projectDTO, 0, len(items))
	for _, p := range items {
		out = append(out, toProjectDTO(p))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"items": out, "total": total, "page": page, "page_size": pageSize,
	})
}

func (h *PreLaunchHandler) getProject(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "project")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	p, sites, err := h.svc.GetProject(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	siteDTOs := make([]projectSiteDTO, 0, len(sites))
	for _, s := range sites {
		siteDTOs = append(siteDTOs, toProjectSiteDTO(s))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"project": toProjectDTO(*p),
		"sites":   siteDTOs,
	})
}

type createProjectRequest struct {
	Notes string `json:"notes"`
}

func (h *PreLaunchHandler) createProject(w http.ResponseWriter, r *http.Request) {
	qid, err := parseUUIDLocal(chi.URLParam(r, "quotation_id"), "quotation")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var req createProjectRequest
	_ = httpserver.DecodeJSON(r, &req)
	p, err := h.svc.CreateProject(r.Context(), port.CreateProjectInput{
		QuotationID:          qid,
		ProjectManagerUserID: actorUserIDLocal(r.Context()),
		Notes:                req.Notes,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toProjectDTO(*p))
}

func (h *PreLaunchHandler) listProjectSites(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "project")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	sites, err := h.svc.ListProjectSites(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]projectSiteDTO, 0, len(sites))
	for _, s := range sites {
		out = append(out, toProjectSiteDTO(s))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}

type createProjectSiteRequest struct {
	SiteCode string   `json:"site_code"`
	SiteName string   `json:"site_name"`
	Address  string   `json:"address"`
	Lat      *float64 `json:"lat"`
	Lng      *float64 `json:"lng"`
	PICName  string   `json:"pic_name"`
	PICPhone string   `json:"pic_phone"`
}

func (h *PreLaunchHandler) createProjectSite(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "project")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var req createProjectSiteRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	site, err := h.svc.CreateProjectSite(r.Context(), port.CreateProjectSiteInput{
		ProjectID: id,
		SiteCode:  req.SiteCode, SiteName: req.SiteName, Address: req.Address,
		Lat: req.Lat, Lng: req.Lng,
		PICName: req.PICName, PICPhone: req.PICPhone,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toProjectSiteDTO(*site))
}

// activateProjectSite — E11 polish. Flips a site to active; the
// usecase auto-completes the parent project when every site is active.
func (h *PreLaunchHandler) activateProjectSite(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "project_site")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	site, err := h.svc.ActivateProjectSite(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toProjectSiteDTO(*site))
}

func (h *PreLaunchHandler) listEnterpriseServices(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "project_site")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	svcs, err := h.svc.ListEnterpriseServices(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]enterpriseServiceDTO, 0, len(svcs))
	for _, s := range svcs {
		out = append(out, toEnterpriseServiceDTO(s))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}

type createEnterpriseServiceRequest struct {
	BOQLineID   string `json:"boq_line_id"`
	ServiceCode string `json:"service_code"`
	ServiceName string `json:"service_name"`
	Notes       string `json:"notes"`
}

func (h *PreLaunchHandler) createEnterpriseService(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "project_site")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var req createEnterpriseServiceRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var boqLineID *uuid.UUID
	if req.BOQLineID != "" {
		u, err := parseUUIDLocal(req.BOQLineID, "boq_line_id")
		if err != nil {
			httpserver.WriteError(w, err)
			return
		}
		boqLineID = &u
	}
	svc, err := h.svc.CreateEnterpriseService(r.Context(), port.CreateEnterpriseServiceInput{
		ProjectSiteID: id, BOQLineID: boqLineID,
		ServiceCode: req.ServiceCode, ServiceName: req.ServiceName, Notes: req.Notes,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toEnterpriseServiceDTO(*svc))
}

// =====================================================================
// E12 — RFQs
// =====================================================================

func (h *PreLaunchHandler) listRFQs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page := parseIntDefaultLocal(q.Get("page"), 1)
	pageSize := parseIntDefaultLocal(q.Get("page_size"), 50)
	if page < 1 {
		page = 1
	}
	var oppID, assignedTo *uuid.UUID
	if s := q.Get("opportunity_id"); s != "" {
		u, err := parseUUIDLocal(s, "opportunity_id")
		if err != nil {
			httpserver.WriteError(w, err)
			return
		}
		oppID = &u
	}
	if s := q.Get("assigned_to"); s != "" {
		u, err := parseUUIDLocal(s, "assigned_to")
		if err != nil {
			httpserver.WriteError(w, err)
			return
		}
		assignedTo = &u
	}
	items, total, err := h.svc.ListRFQs(r.Context(), q.Get("status"), oppID, assignedTo, pageSize, (page-1)*pageSize)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]rfqDTO, 0, len(items))
	for _, r := range items {
		out = append(out, toRFQDTO(r))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"items": out, "total": total, "page": page, "page_size": pageSize,
	})
}

func (h *PreLaunchHandler) getRFQ(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "rfq")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	rfq, err := h.svc.GetRFQ(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toRFQDTO(*rfq))
}

type createRFQRequest struct {
	OpportunityID string `json:"opportunity_id"`
	Requirements  string `json:"requirements"`
	Constraints   string `json:"constraints"`
	DeadlineDays  int    `json:"deadline_days"`
}

func (h *PreLaunchHandler) createRFQ(w http.ResponseWriter, r *http.Request) {
	var req createRFQRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	opp, err := parseUUIDLocal(req.OpportunityID, "opportunity_id")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	rfq, err := h.svc.CreateRFQ(r.Context(), port.CreateRFQInput{
		OpportunityID: opp,
		Requirements:  req.Requirements,
		Constraints:   req.Constraints,
		DeadlineDays:  req.DeadlineDays,
		RequestedBy:   actorUserIDLocal(r.Context()),
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toRFQDTO(*rfq))
}

type assignRFQRequest struct {
	AssignedTo string `json:"assigned_to"`
}

func (h *PreLaunchHandler) assignRFQ(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "rfq")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var req assignRFQRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	to, err := parseUUIDLocal(req.AssignedTo, "assigned_to")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	rfq, err := h.svc.AssignRFQ(r.Context(), port.AssignRFQInput{RFQID: id, AssignedTo: to})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toRFQDTO(*rfq))
}

type fulfillRFQRequest struct {
	FulfilledBOQID string `json:"fulfilled_boq_id"`
}

func (h *PreLaunchHandler) fulfillRFQ(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "rfq")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var req fulfillRFQRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	bqid, err := parseUUIDLocal(req.FulfilledBOQID, "fulfilled_boq_id")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	rfq, err := h.svc.FulfillRFQ(r.Context(), port.FulfillRFQInput{RFQID: id, FulfilledBOQID: bqid})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toRFQDTO(*rfq))
}

func (h *PreLaunchHandler) cancelRFQ(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "rfq")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var req cancelReasonRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	rfq, err := h.svc.CancelRFQ(r.Context(), port.CancelRFQInput{RFQID: id, Reason: req.Reason})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toRFQDTO(*rfq))
}

// =====================================================================
// DTOs
// =====================================================================

type invoicePlanDTO struct {
	ID             string  `json:"id"`
	PlanNumber     string  `json:"plan_number"`
	QuotationID    string  `json:"quotation_id"`
	OpportunityID  string  `json:"opportunity_id"`
	BOQVersionID   string  `json:"boq_version_id"`
	Status         string  `json:"status"`
	TotalAmount    float64 `json:"total_amount"`
	SubtotalAmount float64 `json:"subtotal_amount"`
	TaxPct         float64 `json:"tax_pct"`
	TaxAmount      float64 `json:"tax_amount"`
	PlannedAmount  float64 `json:"planned_amount"`
	Currency       string  `json:"currency"`
	TolerancePct   float64 `json:"tolerance_pct"`
	Notes          string  `json:"notes"`
	Revision       int     `json:"revision"`
	CreatedAt      string  `json:"created_at"`
	UpdatedAt      string  `json:"updated_at"`
}

func toInvoicePlanDTO(p domain.InvoicePlan) invoicePlanDTO {
	return invoicePlanDTO{
		ID: p.ID.String(), PlanNumber: p.PlanNumber,
		QuotationID: p.QuotationID.String(), OpportunityID: p.OpportunityID.String(), BOQVersionID: p.BOQVersionID.String(),
		Status: string(p.Status), TotalAmount: p.TotalAmount,
		SubtotalAmount: p.SubtotalAmount, TaxPct: p.TaxPct, TaxAmount: p.TaxAmount,
		PlannedAmount: p.PlannedAmount, Currency: p.Currency, TolerancePct: p.TolerancePct,
		Notes: p.Notes, Revision: p.Revision,
		CreatedAt: rfc3339(p.CreatedAt), UpdatedAt: rfc3339(p.UpdatedAt),
	}
}

type invoicePlanItemDTO struct {
	ID            string  `json:"id"`
	PlanID        string  `json:"plan_id"`
	SeqNo         int     `json:"seq_no"`
	Label         string  `json:"label"`
	Amount        float64 `json:"amount"`
	DueOffsetDays int     `json:"due_offset_days"`
	InvoiceID     *string `json:"invoice_id,omitempty"`
	IssuedAt      *string `json:"issued_at,omitempty"`
	Notes         string  `json:"notes"`
	CreatedAt     string  `json:"created_at"`
}

func toInvoicePlanItemDTOs(items []domain.InvoicePlanItem) []invoicePlanItemDTO {
	out := make([]invoicePlanItemDTO, 0, len(items))
	for _, it := range items {
		var invID *string
		if it.InvoiceID != nil {
			s := it.InvoiceID.String()
			invID = &s
		}
		out = append(out, invoicePlanItemDTO{
			ID: it.ID.String(), PlanID: it.PlanID.String(),
			SeqNo: it.SeqNo, Label: it.Label, Amount: it.Amount,
			DueOffsetDays: it.DueOffsetDays,
			InvoiceID:     invID,
			IssuedAt:      rfc3339Ptr(it.IssuedAt),
			Notes:         it.Notes,
			CreatedAt:     rfc3339(it.CreatedAt),
		})
	}
	return out
}

type poDocumentDTO struct {
	ID            string  `json:"id"`
	OpportunityID string  `json:"opportunity_id"`
	PONumber      string  `json:"po_number"`
	PORevision    int     `json:"po_revision"`
	FileURL       string  `json:"file_url"`
	FileName      string  `json:"file_name"`
	FileSizeBytes int64   `json:"file_size_bytes"`
	ContentType   string  `json:"content_type"`
	IssuedByPIC   string  `json:"issued_by_pic"`
	ReceivedAt    string  `json:"received_at"`
	UploadedBy    *string `json:"uploaded_by,omitempty"`
	Notes         string  `json:"notes"`
	CreatedAt     string  `json:"created_at"`
}

func toPODocumentDTO(d domain.PODocument) poDocumentDTO {
	var by *string
	if d.UploadedBy != nil {
		s := d.UploadedBy.String()
		by = &s
	}
	return poDocumentDTO{
		ID: d.ID.String(), OpportunityID: d.OpportunityID.String(),
		PONumber: d.PONumber, PORevision: d.PORevision,
		FileURL: d.FileURL, FileName: d.FileName, FileSizeBytes: d.FileSizeBytes,
		ContentType: d.ContentType, IssuedByPIC: d.IssuedByPIC,
		ReceivedAt: rfc3339(d.ReceivedAt), UploadedBy: by,
		Notes: d.Notes, CreatedAt: rfc3339(d.CreatedAt),
	}
}

type paymentProofDTO struct {
	ID               string  `json:"id"`
	InvoicePaymentID string  `json:"invoice_payment_id"`
	FileURL          string  `json:"file_url"`
	FileName         string  `json:"file_name"`
	FileSizeBytes    int64   `json:"file_size_bytes"`
	ContentType      string  `json:"content_type"`
	UploadedBy       *string `json:"uploaded_by,omitempty"`
	Notes            string  `json:"notes"`
	CreatedAt        string  `json:"created_at"`
}

func toPaymentProofDTO(p domain.PaymentProof) paymentProofDTO {
	var by *string
	if p.UploadedBy != nil {
		s := p.UploadedBy.String()
		by = &s
	}
	return paymentProofDTO{
		ID: p.ID.String(), InvoicePaymentID: p.InvoicePaymentID.String(),
		FileURL: p.FileURL, FileName: p.FileName, FileSizeBytes: p.FileSizeBytes,
		ContentType: p.ContentType, UploadedBy: by,
		Notes: p.Notes, CreatedAt: rfc3339(p.CreatedAt),
	}
}

type ewoChecklistItemDTO struct {
	ID          string  `json:"id"`
	EWOID       string  `json:"ewo_id"`
	SeqNo       int     `json:"seq_no"`
	Label       string  `json:"label"`
	Description string  `json:"description"`
	Status      string  `json:"status"`
	CompletedAt *string `json:"completed_at,omitempty"`
	CompletedBy *string `json:"completed_by,omitempty"`
	Notes       string  `json:"notes"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

func toEWOChecklistItemDTO(it domain.EWOChecklistItem) ewoChecklistItemDTO {
	var by *string
	if it.CompletedBy != nil {
		s := it.CompletedBy.String()
		by = &s
	}
	return ewoChecklistItemDTO{
		ID: it.ID.String(), EWOID: it.EWOID.String(),
		SeqNo: it.SeqNo, Label: it.Label, Description: it.Description,
		Status: string(it.Status), CompletedAt: rfc3339Ptr(it.CompletedAt),
		CompletedBy: by, Notes: it.Notes,
		CreatedAt: rfc3339(it.CreatedAt), UpdatedAt: rfc3339(it.UpdatedAt),
	}
}

type projectDTO struct {
	ID                   string  `json:"id"`
	ProjectNumber        string  `json:"project_number"`
	QuotationID          string  `json:"quotation_id"`
	OpportunityID        string  `json:"opportunity_id"`
	BOQVersionID         string  `json:"boq_version_id"`
	Status               string  `json:"status"`
	StartedAt            *string `json:"started_at,omitempty"`
	CompletedAt          *string `json:"completed_at,omitempty"`
	CancelledAt          *string `json:"cancelled_at,omitempty"`
	CancelReason         string  `json:"cancel_reason"`
	ProjectManagerUserID *string `json:"project_manager_user_id,omitempty"`
	Notes                string  `json:"notes"`
	Revision             int     `json:"revision"`
	CreatedAt            string  `json:"created_at"`
	UpdatedAt            string  `json:"updated_at"`
}

func toProjectDTO(p domain.Project) projectDTO {
	var pm *string
	if p.ProjectManagerUserID != nil {
		s := p.ProjectManagerUserID.String()
		pm = &s
	}
	return projectDTO{
		ID: p.ID.String(), ProjectNumber: p.ProjectNumber,
		QuotationID: p.QuotationID.String(), OpportunityID: p.OpportunityID.String(), BOQVersionID: p.BOQVersionID.String(),
		Status: string(p.Status),
		StartedAt: rfc3339Ptr(p.StartedAt), CompletedAt: rfc3339Ptr(p.CompletedAt), CancelledAt: rfc3339Ptr(p.CancelledAt),
		CancelReason: p.CancelReason, ProjectManagerUserID: pm,
		Notes: p.Notes, Revision: p.Revision,
		CreatedAt: rfc3339(p.CreatedAt), UpdatedAt: rfc3339(p.UpdatedAt),
	}
}

type projectSiteDTO struct {
	ID          string   `json:"id"`
	ProjectID   string   `json:"project_id"`
	SiteCode    string   `json:"site_code"`
	SiteName    string   `json:"site_name"`
	Address     string   `json:"address"`
	Lat         *float64 `json:"lat,omitempty"`
	Lng         *float64 `json:"lng,omitempty"`
	PICName     string   `json:"pic_name"`
	PICPhone    string   `json:"pic_phone"`
	Status      string   `json:"status"`
	ActivatedAt *string  `json:"activated_at,omitempty"`
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`
}

func toProjectSiteDTO(s domain.ProjectSite) projectSiteDTO {
	return projectSiteDTO{
		ID: s.ID.String(), ProjectID: s.ProjectID.String(),
		SiteCode: s.SiteCode, SiteName: s.SiteName, Address: s.Address,
		Lat: s.Lat, Lng: s.Lng,
		PICName: s.PICName, PICPhone: s.PICPhone,
		Status: string(s.Status), ActivatedAt: rfc3339Ptr(s.ActivatedAt),
		CreatedAt: rfc3339(s.CreatedAt), UpdatedAt: rfc3339(s.UpdatedAt),
	}
}

type enterpriseServiceDTO struct {
	ID            string  `json:"id"`
	ProjectSiteID string  `json:"project_site_id"`
	BOQLineID     *string `json:"boq_line_id,omitempty"`
	ServiceCode   string  `json:"service_code"`
	ServiceName   string  `json:"service_name"`
	Status        string  `json:"status"`
	ActivatedAt   *string `json:"activated_at,omitempty"`
	TerminatedAt  *string `json:"terminated_at,omitempty"`
	Notes         string  `json:"notes"`
	CreatedAt     string  `json:"created_at"`
	UpdatedAt     string  `json:"updated_at"`
}

func toEnterpriseServiceDTO(s domain.EnterpriseService) enterpriseServiceDTO {
	var bq *string
	if s.BOQLineID != nil {
		x := s.BOQLineID.String()
		bq = &x
	}
	return enterpriseServiceDTO{
		ID: s.ID.String(), ProjectSiteID: s.ProjectSiteID.String(), BOQLineID: bq,
		ServiceCode: s.ServiceCode, ServiceName: s.ServiceName,
		Status: string(s.Status), ActivatedAt: rfc3339Ptr(s.ActivatedAt),
		TerminatedAt: rfc3339Ptr(s.TerminatedAt), Notes: s.Notes,
		CreatedAt: rfc3339(s.CreatedAt), UpdatedAt: rfc3339(s.UpdatedAt),
	}
}

type rfqDTO struct {
	ID             string  `json:"id"`
	RFQNumber      string  `json:"rfq_number"`
	OpportunityID  string  `json:"opportunity_id"`
	Status         string  `json:"status"`
	RequestedBy    *string `json:"requested_by,omitempty"`
	AssignedTo     *string `json:"assigned_to,omitempty"`
	Requirements   string  `json:"requirements"`
	Constraints    string  `json:"constraints"`
	DeadlineAt     *string `json:"deadline_at,omitempty"`
	FulfilledAt    *string `json:"fulfilled_at,omitempty"`
	FulfilledBOQID *string `json:"fulfilled_boq_id,omitempty"`
	CancelledAt    *string `json:"cancelled_at,omitempty"`
	CancelReason   string  `json:"cancel_reason"`
	Notes          string  `json:"notes"`
	Revision       int     `json:"revision"`
	CreatedAt      string  `json:"created_at"`
	UpdatedAt      string  `json:"updated_at"`
}

func toRFQDTO(r domain.RFQ) rfqDTO {
	var by, to, boq *string
	if r.RequestedBy != nil {
		s := r.RequestedBy.String()
		by = &s
	}
	if r.AssignedTo != nil {
		s := r.AssignedTo.String()
		to = &s
	}
	if r.FulfilledBOQID != nil {
		s := r.FulfilledBOQID.String()
		boq = &s
	}
	return rfqDTO{
		ID: r.ID.String(), RFQNumber: r.RFQNumber, OpportunityID: r.OpportunityID.String(),
		Status: string(r.Status),
		RequestedBy: by, AssignedTo: to,
		Requirements: r.Requirements, Constraints: r.Constraints,
		DeadlineAt: rfc3339Ptr(r.DeadlineAt),
		FulfilledAt: rfc3339Ptr(r.FulfilledAt), FulfilledBOQID: boq,
		CancelledAt: rfc3339Ptr(r.CancelledAt), CancelReason: r.CancelReason,
		Notes: r.Notes, Revision: r.Revision,
		CreatedAt: rfc3339(r.CreatedAt), UpdatedAt: rfc3339(r.UpdatedAt),
	}
}

// =====================================================================
// Internal transactions (sub-company revenue ledger)
// =====================================================================

func (h *PreLaunchHandler) listInternalTransactions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	// Currently we only support the boq_version_id scope — the BOQ
	// detail page is the only consumer. Vendor-aggregated reports
	// (Phase post-launch) will widen this filter.
	bv := q.Get("boq_version_id")
	if bv == "" {
		httpserver.WriteError(w, errors.Validation(
			"internal_transaction.boq_version_id_required",
			"boq_version_id query parameter is required",
		))
		return
	}
	id, err := parseUUIDLocal(bv, "boq_version_id")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	items, err := h.svc.ListInternalTransactionsByBOQ(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]internalTransactionDTO, 0, len(items))
	for _, it := range items {
		out = append(out, toInternalTransactionDTO(it))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}

type internalTransactionDTO struct {
	ID              string  `json:"id"`
	BOQVersionID    string  `json:"boq_version_id"`
	BOQLineID       string  `json:"boq_line_id"`
	QuotationID     *string `json:"quotation_id,omitempty"`
	VendorCompanyID *string `json:"vendor_company_id,omitempty"`
	SellAmount      float64 `json:"sell_amount"`
	CostAmount      float64 `json:"cost_amount"`
	MarginAmount    float64 `json:"margin_amount"`
	Currency        string  `json:"currency"`
	RecognizedAt    string  `json:"recognized_at"`
	Notes           string  `json:"notes"`
	CreatedAt       string  `json:"created_at"`
}

func toInternalTransactionDTO(it domain.InternalTransaction) internalTransactionDTO {
	var q, v *string
	if it.QuotationID != nil {
		s := it.QuotationID.String()
		q = &s
	}
	if it.VendorCompanyID != nil {
		s := it.VendorCompanyID.String()
		v = &s
	}
	return internalTransactionDTO{
		ID:              it.ID.String(),
		BOQVersionID:    it.BOQVersionID.String(),
		BOQLineID:       it.BOQLineID.String(),
		QuotationID:     q,
		VendorCompanyID: v,
		SellAmount:      it.SellAmount,
		CostAmount:      it.CostAmount,
		MarginAmount:    it.MarginAmount,
		Currency:        it.Currency,
		RecognizedAt:    rfc3339(it.RecognizedAt),
		Notes:           it.Notes,
		CreatedAt:       rfc3339(it.CreatedAt),
	}
}

// =====================================================================
// Polish — EWO checklist templates (admin) + bulk PO + seed-from-template
// =====================================================================

type ewoChecklistTemplateItem struct {
	SeqNo       int    `json:"seq_no"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

type ewoChecklistTemplateDTO struct {
	ID          string                     `json:"id"`
	Code        string                     `json:"code"`
	Name        string                     `json:"name"`
	Description string                     `json:"description"`
	Active      bool                       `json:"active"`
	Items       []ewoChecklistTemplateItem `json:"items"`
	ItemCount   int                        `json:"item_count"`
	CreatedBy   *string                    `json:"created_by,omitempty"`
	CreatedAt   string                     `json:"created_at"`
	UpdatedAt   string                     `json:"updated_at"`
}

// toEWOChecklistTemplateDTO — the repo stores items as a raw jsonb
// blob; on the way out we decode so the FE doesn't have to parse a
// string-inside-JSON. Decode failures don't blow up the response —
// callers can still see the metadata and re-save a valid body.
func toEWOChecklistTemplateDTO(t port.EWOChecklistTemplate) ewoChecklistTemplateDTO {
	var by *string
	if t.CreatedBy != nil {
		s := t.CreatedBy.String()
		by = &s
	}
	items := []ewoChecklistTemplateItem{}
	if len(t.ItemsJSON) > 0 {
		_ = json.Unmarshal(t.ItemsJSON, &items)
	}
	return ewoChecklistTemplateDTO{
		ID:          t.ID.String(),
		Code:        t.Code,
		Name:        t.Name,
		Description: t.Description,
		Active:      t.Active,
		Items:       items,
		ItemCount:   len(items),
		CreatedBy:   by,
		CreatedAt:   t.CreatedAt,
		UpdatedAt:   t.UpdatedAt,
	}
}

func (h *PreLaunchHandler) listEWOChecklistTemplates(w http.ResponseWriter, r *http.Request) {
	activeOnly := r.URL.Query().Get("active_only") == "true"
	items, err := h.svc.ListEWOChecklistTemplates(r.Context(), activeOnly)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]ewoChecklistTemplateDTO, 0, len(items))
	for _, t := range items {
		out = append(out, toEWOChecklistTemplateDTO(t))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (h *PreLaunchHandler) getEWOChecklistTemplate(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "ewo_checklist_template")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	t, err := h.svc.GetEWOChecklistTemplate(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toEWOChecklistTemplateDTO(*t))
}

type saveEWOChecklistTemplateRequest struct {
	ID          string                     `json:"id"`
	Code        string                     `json:"code"`
	Name        string                     `json:"name"`
	Description string                     `json:"description"`
	Active      bool                       `json:"active"`
	Items       []ewoChecklistTemplateItem `json:"items"`
}

func (h *PreLaunchHandler) saveEWOChecklistTemplate(w http.ResponseWriter, r *http.Request) {
	var req saveEWOChecklistTemplateRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	if req.Code == "" {
		httpserver.WriteError(w, errors.Validation("ewo_template.code_required", "code is required"))
		return
	}
	// We persist via a raw jsonb body; encode the array shape here so
	// callers don't have to.
	body, err := json.Marshal(req.Items)
	if err != nil {
		httpserver.WriteError(w, errors.Validation("ewo_template.invalid_items", "items must be a valid array"))
		return
	}
	in := port.EWOChecklistTemplate{
		Code: req.Code, Name: req.Name, Description: req.Description,
		Active: req.Active, ItemsJSON: body,
		CreatedBy: actorUserIDLocal(r.Context()),
	}
	if req.ID != "" {
		u, err := uuid.Parse(req.ID)
		if err == nil {
			in.ID = u
		}
	}
	out, err := h.svc.SaveEWOChecklistTemplate(r.Context(), in)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toEWOChecklistTemplateDTO(*out))
}

func (h *PreLaunchHandler) deleteEWOChecklistTemplate(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "ewo_checklist_template")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	if err := h.svc.DeleteEWOChecklistTemplate(r.Context(), id); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *PreLaunchHandler) seedEWOChecklistFromTemplate(w http.ResponseWriter, r *http.Request) {
	ewoID, err := parseUUIDLocal(chi.URLParam(r, "ewo_id"), "ewo")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	code := chi.URLParam(r, "code")
	if code == "" {
		httpserver.WriteError(w, errors.Validation("ewo_template.code_required", "template code is required"))
		return
	}
	items, err := h.svc.SeedEWOChecklistFromTemplate(r.Context(), ewoID, code)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]ewoChecklistItemDTO, 0, len(items))
	for _, it := range items {
		out = append(out, toEWOChecklistItemDTO(it))
	}
	httpserver.WriteJSON(w, http.StatusCreated, map[string]any{"items": out})
}

type bulkUploadPORequest struct {
	Items []uploadPORequest `json:"items"`
}

func (h *PreLaunchHandler) bulkUploadPODocuments(w http.ResponseWriter, r *http.Request) {
	opp, err := parseUUIDLocal(chi.URLParam(r, "opportunity_id"), "opportunity")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var req bulkUploadPORequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	if len(req.Items) == 0 {
		httpserver.WriteError(w, errors.Validation("po_document.empty_batch", "items is required"))
		return
	}
	actor := actorUserIDLocal(r.Context())
	batch := make([]port.UploadPODocumentInput, 0, len(req.Items))
	for _, it := range req.Items {
		batch = append(batch, port.UploadPODocumentInput{
			OpportunityID: opp,
			PONumber:      it.PONumber,
			FileURL:       it.FileURL,
			FileName:      it.FileName,
			FileSizeBytes: it.FileSizeBytes,
			ContentType:   it.ContentType,
			IssuedByPIC:   it.IssuedByPIC,
			Notes:         it.Notes,
			UploadedBy:    actor,
		})
	}
	docs, err := h.svc.BulkUploadPODocuments(r.Context(), batch)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]poDocumentDTO, 0, len(docs))
	for _, d := range docs {
		out = append(out, toPODocumentDTO(d))
	}
	httpserver.WriteJSON(w, http.StatusCreated, map[string]any{"items": out})
}
