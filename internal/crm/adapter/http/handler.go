// Package http is the driving adapter for the CRM bounded context.
package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/crm/domain"
	"github.com/ion-core/backend/internal/crm/port"
	"github.com/ion-core/backend/pkg/auth"
	"github.com/ion-core/backend/pkg/errors"
	"github.com/ion-core/backend/pkg/httpserver"
)

// logLeadEvent inserts a row into crm.lead_events. Fire-and-forget —
// errors are swallowed because failing to write an audit row should
// not roll back a successful business action. The handler functions
// call this immediately after the core mutation succeeds.
//
// `data` is marshalled to JSONB; nil → "{}".
func (h *Handler) logLeadEvent(ctx context.Context, leadID uuid.UUID, actorUserID *uuid.UUID, kind, summary string, data map[string]any) {
	if h.eventPool == nil {
		return
	}
	if data == nil {
		data = map[string]any{}
	}
	bs, err := json.Marshal(data)
	if err != nil {
		bs = []byte("{}")
	}
	_, _ = h.eventPool.Exec(ctx, `
		INSERT INTO crm.lead_events
			(lead_id, actor_user_id, kind, summary, data)
		VALUES ($1, $2, $3, $4, $5)
	`, leadID, actorUserID, kind, summary, bs)
}

type Handler struct {
	uc       port.UseCase
	verifier *auth.Verifier
	// ktpRL throttles the KTP OCR endpoint. Optional; nil means no limit.
	// The endpoint is authenticated, so the limit is per-IP-of-the-caller —
	// enough to slow a misbehaving rep or a stolen-credentials replay
	// loop without blocking legitimate batch uploads from the same office.
	ktpRL *httpserver.RateLimit
	// ktpProvider is the KTP OCR backend (stub by default). Switch via
	// WithKTPProvider — typically based on a build tag + env var combo
	// so binaries without Tesseract installed can still serve the
	// stub provider.
	ktpProvider KTPProvider
	// eventPool is an optional pgxpool injected by main.go so the
	// handler can write rows directly into crm.lead_events (audit
	// timeline) without growing the UseCase port. Fire-and-forget —
	// failures here MUST NOT roll back the successful core write.
	eventPool *pgxpool.Pool
}

func NewHandler(uc port.UseCase, verifier *auth.Verifier) *Handler {
	return &Handler{uc: uc, verifier: verifier}
}

// WithEventPool attaches a pgxpool used for direct lead-event
// auto-writes. See logLeadEvent.
func (h *Handler) WithEventPool(p *pgxpool.Pool) *Handler {
	h.eventPool = p
	return h
}

// WithKTPRateLimit attaches a per-IP rate limiter to /ktp-ocr.
// Tuned to absorb a typical onboarding cadence (a few KTP scans per
// minute) while still slowing pathological loops.
func (h *Handler) WithKTPRateLimit(rl *httpserver.RateLimit) *Handler {
	h.ktpRL = rl
	return h
}

// WithKTPProvider swaps the KTP OCR backend (default = deterministic
// stub). Round-4 binaries built with the `tesseract` tag can swap in
// the Tesseract provider; round-5 will add Google Vision.
func (h *Handler) WithKTPProvider(p KTPProvider) *Handler {
	h.ktpProvider = p
	return h
}

// Mount — route map:
//
//	Products
//	  GET   /products                         [crm.product.read]
//	  POST  /products                         [crm.product.manage]
//
//	Leads
//	  GET   /leads                            [crm.lead.read]
//	  GET   /leads/{id}                       [crm.lead.read]
//	  POST  /leads                            [crm.lead.manage]
//	  PATCH /leads/{id}                       [crm.lead.manage]
//	  POST  /leads/{id}/convert               [crm.lead.convert]
//
//	Documents
//	  PATCH /documents/{id}                   [crm.lead.manage]
//
//	Customers / Orders
//	  GET   /customers                        [crm.customer.read]
//	  GET   /customers/{id}                   [crm.customer.read]
//	  GET   /orders                           [crm.order.read]
//	  GET   /orders/{id}                      [crm.order.read]
func (h *Handler) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(httpserver.RequireAuth(h.verifier))

		r.With(httpserver.RequirePermission("crm.product.read")).Get("/products", h.listProducts)
		r.With(httpserver.RequirePermission("crm.product.read")).Get("/products/{id}", h.getProduct)
		r.With(httpserver.RequirePermission("crm.product.manage")).Post("/products", h.createProduct)
		// Wave 77 (TC-PRD-014/016/018/022/024): PATCH for schema slot
		// reassignment. The same `crm.product.manage` permission gates
		// the route — TC-PRD-024's "approval workflow" requirement is
		// the schema-publish workflow (Wave 79), not a separate gate
		// per product update. The slot value still must point at a
		// schema that's `published` (resolver-enforced) — this PATCH
		// doesn't bypass that.
		r.With(httpserver.RequirePermission("crm.product.manage")).Patch("/products/{id}", h.updateProduct)

		r.With(httpserver.RequirePermission("crm.lead.read")).Get("/leads", h.listLeads)
		r.With(httpserver.RequirePermission("crm.lead.read")).Get("/leads/{id}", h.getLead)
		r.With(httpserver.RequirePermission("crm.lead.manage")).Post("/leads", h.createLead)
		r.With(httpserver.RequirePermission("crm.lead.manage")).Patch("/leads/{id}", h.updateLead)
		r.With(httpserver.RequirePermission("crm.lead.convert")).Post("/leads/{id}/convert", h.convertLead)

		r.With(httpserver.RequirePermission("crm.lead.manage")).Patch("/documents/{id}", h.updateDocument)

		r.With(httpserver.RequirePermission("crm.customer.read")).Get("/customers", h.listCustomers)
		r.With(httpserver.RequirePermission("crm.customer.read")).Get("/customers/{id}", h.getCustomer)

		r.With(httpserver.RequirePermission("crm.order.read")).Get("/orders", h.listOrders)
		r.With(httpserver.RequirePermission("crm.order.read")).Get("/orders/{id}", h.getOrder)

		// M4 r2 — Onboarding schemas (read-only in round 2; the publish UI is round 3)
		r.With(httpserver.RequirePermission("crm.schema.read")).Get("/onboarding-schemas", h.listSchemas)
		r.With(httpserver.RequirePermission("crm.schema.read")).Get("/onboarding-schemas/{id}", h.getSchema)

		// M4 r2 — Sales dashboard
		r.With(httpserver.RequirePermission("crm.dashboard.read")).Get("/sales-dashboard", h.salesDashboard)

		// KTP OCR stub — accepts an image upload, returns the parsed
		// fields the lead-create form needs. Round-3 returns deterministic
		// stub data; round-4 will route through Google Vision / Tesseract.
		// Per-IP rate limit is layered on top of the permission check so
		// a compromised account can't burn through OCR budget.
		if h.ktpRL != nil {
			r.With(h.ktpRL.Middleware()).
				With(httpserver.RequirePermission("crm.lead.manage")).
				Post("/ktp-ocr", h.parseKTPImage)
		} else {
			r.With(httpserver.RequirePermission("crm.lead.manage")).
				Post("/ktp-ocr", h.parseKTPImage)
		}
	})
}

// DTOs (productDTO, leadDTO, customerDTO, orderDTO, documentDTO, …) live in dto.go.

// =====================================================================
// Products handlers
// =====================================================================

func (h *Handler) listProducts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	out, err := h.uc.ListProducts(r.Context(), port.ProductListFilter{
		Search:     q.Get("q"),
		ActiveOnly: q.Get("active_only") == "true",
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	items := make([]productDTO, 0, len(out))
	for _, p := range out {
		items = append(items, toProductDTO(p))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) createProduct(w http.ResponseWriter, r *http.Request) {
	var req createProductRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	in := port.CreateProductInput{
		Code:         req.Code,
		Name:         req.Name,
		SpeedMbps:    req.SpeedMbps,
		MonthlyPrice: req.MonthlyPrice,
		OTCPrice:     req.OTCPrice,
	}
	// Wave 77: forward optional schema slot ids; validate UUID format.
	parseSlot := func(s string, code string) (*uuid.UUID, error) {
		if s == "" {
			return nil, nil
		}
		id, err := uuid.Parse(s)
		if err != nil {
			return nil, errors.Validation("product."+code+"_invalid", code+" must be a uuid")
		}
		return &id, nil
	}
	for _, x := range []struct {
		raw  string
		code string
		dst  **uuid.UUID
	}{
		{req.OnboardingSchemaID, "onboarding_schema_id", &in.OnboardingSchemaID},
		{req.BillingSchemaID, "billing_schema_id", &in.BillingSchemaID},
		{req.ServiceSchemaID, "service_schema_id", &in.ServiceSchemaID},
		{req.CommissionSchemaID, "commission_schema_id", &in.CommissionSchemaID},
		{req.SuspensionSchemaID, "suspension_schema_id", &in.SuspensionSchemaID},
	} {
		v, err := parseSlot(x.raw, x.code)
		if err != nil {
			httpserver.WriteError(w, err)
			return
		}
		*x.dst = v
	}
	p, err := h.uc.CreateProduct(r.Context(), in)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toProductDTO(*p))
}

// Wave 77 (TC-PRD-027): single-product GET for the product detail page.
// Surfaces the 5 schema slot FKs so the UI can render the assignment.
func (h *Handler) getProduct(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "product")
	if !ok {
		return
	}
	p, err := h.uc.GetProduct(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toProductDTO(*p))
}

// Wave 77 (TC-PRD-014/016/018/022/024): PATCH for partial product
// updates, primarily for schema slot (re)assignment.
func (h *Handler) updateProduct(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "product")
	if !ok {
		return
	}
	var req updateProductRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	in := port.UpdateProductInput{
		ID:            id,
		Name:          req.Name,
		SpeedMbps:     req.SpeedMbps,
		MonthlyPrice:  req.MonthlyPrice,
		OTCPrice:      req.OTCPrice,
		TempWindowHrs: req.TempWindowHrs,
		Active:        req.Active,
		ClearOnboarding:  req.ClearOnboarding,
		ClearBilling:     req.ClearBilling,
		ClearService:     req.ClearService,
		ClearCommission:  req.ClearCommission,
		ClearSuspension:  req.ClearSuspension,
	}
	parseSlot := func(s *string, code string) (*uuid.UUID, error) {
		if s == nil || *s == "" {
			return nil, nil
		}
		v, err := uuid.Parse(*s)
		if err != nil {
			return nil, errors.Validation("product."+code+"_invalid", code+" must be a uuid")
		}
		return &v, nil
	}
	for _, x := range []struct {
		raw  *string
		code string
		dst  **uuid.UUID
	}{
		{req.OnboardingSchemaID, "onboarding_schema_id", &in.OnboardingSchemaID},
		{req.BillingSchemaID, "billing_schema_id", &in.BillingSchemaID},
		{req.ServiceSchemaID, "service_schema_id", &in.ServiceSchemaID},
		{req.CommissionSchemaID, "commission_schema_id", &in.CommissionSchemaID},
		{req.SuspensionSchemaID, "suspension_schema_id", &in.SuspensionSchemaID},
	} {
		v, err := parseSlot(x.raw, x.code)
		if err != nil {
			httpserver.WriteError(w, err)
			return
		}
		*x.dst = v
	}
	p, err := h.uc.UpdateProduct(r.Context(), in)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toProductDTO(*p))
}

// =====================================================================
// Leads handlers
// =====================================================================

func (h *Handler) listLeads(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := httpserver.ParseIntDefault(q.Get("page_size"), 50)
	page := httpserver.ParseIntDefault(q.Get("page"), 1)
	f := port.LeadListFilter{
		Status: q.Get("status"),
		Search: q.Get("q"),
		Limit:  limit,
		Offset: (page - 1) * limit,
	}
	if v := q.Get("branch_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpserver.WriteError(w, errors.Validation("lead.branch_invalid", "branch_id is not a uuid"))
			return
		}
		f.BranchID = &id
	}
	if v := q.Get("sales_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpserver.WriteError(w, errors.Validation("lead.sales_invalid", "sales_id is not a uuid"))
			return
		}
		f.SalesID = &id
	}
	out, total, err := h.uc.ListLeads(r.Context(), f)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	items := make([]leadDTO, 0, len(out))
	for _, lw := range out {
		items = append(items, toLeadDTO(lw))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"items": items, "total": total, "page": page, "page_size": limit,
	})
}

func (h *Handler) getLead(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "lead")
	if !ok {
		return
	}
	lw, err := h.uc.GetLead(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toLeadDTO(*lw))
}

func (h *Handler) createLead(w http.ResponseWriter, r *http.Request) {
	var req createLeadRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	c := httpserver.ClaimsFromContext(r.Context())
	in := port.CreateLeadInput{
		FullName:          req.FullName,
		Phone:             req.Phone,
		Email:             req.Email,
		NIK:               req.NIK,
		Address:           req.Address,
		GPSLat:            req.GPSLat,
		GPSLng:            req.GPSLng,
		Source:            req.Source,
		Notes:             req.Notes,
		AcceptExcessCable: req.AcceptExcessCable,
		LeadType:          req.LeadType, // Wave 76
	}
	if req.ProductID != "" {
		id, err := uuid.Parse(req.ProductID)
		if err != nil {
			httpserver.WriteError(w, errors.Validation("lead.product_invalid", "product_id is not a uuid"))
			return
		}
		in.ProductID = &id
	}
	if req.SalesID != "" {
		id, err := uuid.Parse(req.SalesID)
		if err != nil {
			httpserver.WriteError(w, errors.Validation("lead.sales_invalid", "sales_id is not a uuid"))
			return
		}
		in.SalesID = &id
	}
	// Wave 76 (TC-CRM-007): referrer customer id when source=referral.
	if req.ReferrerCustomerID != "" {
		id, err := uuid.Parse(req.ReferrerCustomerID)
		if err != nil {
			httpserver.WriteError(w, errors.Validation("lead.referrer_invalid",
				"referrer_customer_id is not a uuid"))
			return
		}
		in.ReferrerCustomerID = &id
	}
	if c != nil {
		uid := c.UserID
		in.CreatedBy = &uid
	}
	out, err := h.uc.CreateLead(r.Context(), in)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toLeadDTO(*out))
}

func (h *Handler) updateLead(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "lead")
	if !ok {
		return
	}
	var req updateLeadRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	in := port.UpdateLeadInput{
		ID:                id,
		FullName:          req.FullName,
		Phone:             req.Phone,
		Email:             req.Email,
		NIK:               req.NIK,
		Address:           req.Address,
		GPSLat:            req.GPSLat,
		GPSLng:            req.GPSLng,
		ClearGPS:          req.ClearGPS,
		ClearProduct:      req.ClearProduct,
		ClearSales:        req.ClearSales,
		Notes:             req.Notes,
		AcceptExcessCable: req.AcceptExcessCable,
	}
	if req.ProductID != nil && *req.ProductID != "" {
		pid, err := uuid.Parse(*req.ProductID)
		if err != nil {
			httpserver.WriteError(w, errors.Validation("lead.product_invalid", "product_id is not a uuid"))
			return
		}
		in.ProductID = &pid
	}
	if req.SalesID != nil && *req.SalesID != "" {
		sid, err := uuid.Parse(*req.SalesID)
		if err != nil {
			httpserver.WriteError(w, errors.Validation("lead.sales_invalid", "sales_id is not a uuid"))
			return
		}
		in.SalesID = &sid
	}
	if req.Status != nil {
		st := domain.LeadStatus(*req.Status)
		in.Status = &st
	}
	out, err := h.uc.UpdateLead(r.Context(), in)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	// Audit: every meaningful field change writes a dedicated event.
	// We diff the pre/post lead so the timeline is rich (status →
	// status_change, sales_id → sales_reassigned, etc.). One event
	// per logical change, all attributed to the same actor.
	var actor *uuid.UUID
	if c := httpserver.ClaimsFromContext(r.Context()); c != nil {
		uid := c.UserID
		actor = &uid
	}
	if req.Status != nil {
		h.logLeadEvent(r.Context(), id, actor, "status_change",
			"Status changed to "+*req.Status,
			map[string]any{"to": *req.Status})
	}
	if req.SalesID != nil {
		h.logLeadEvent(r.Context(), id, actor, "sales_reassigned",
			"Sales rep reassigned",
			map[string]any{"to": *req.SalesID})
	}
	if req.ProductID != nil {
		h.logLeadEvent(r.Context(), id, actor, "product_changed",
			"Product changed",
			map[string]any{"to": *req.ProductID})
	}
	if req.AcceptExcessCable != nil {
		h.logLeadEvent(r.Context(), id, actor, "accept_excess_changed",
			fmt.Sprintf("Accept-excess set to %v", *req.AcceptExcessCable),
			map[string]any{"to": *req.AcceptExcessCable})
	}
	if req.Notes != nil && strings.TrimSpace(*req.Notes) != "" {
		summary := *req.Notes
		if len(summary) > 80 {
			summary = summary[:77] + "…"
		}
		h.logLeadEvent(r.Context(), id, actor, "note",
			"Note: "+summary,
			map[string]any{"length": len(*req.Notes)})
	}
	httpserver.WriteJSON(w, http.StatusOK, toLeadDTO(*out))
}

func (h *Handler) convertLead(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "lead")
	if !ok {
		return
	}
	c := httpserver.ClaimsFromContext(r.Context())
	if c == nil {
		httpserver.WriteError(w, errors.Unauthorized("auth.missing", "authentication required"))
		return
	}
	out, err := h.uc.ConvertLead(r.Context(), port.ConvertLeadInput{
		LeadID:      id,
		PerformedBy: c.UserID,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	actor := c.UserID
	h.logLeadEvent(r.Context(), id, &actor, "converted",
		"Lead converted to customer + order",
		map[string]any{
			"customer_id": out.Customer.ID.String(),
			"order_id":    out.Order.ID.String(),
		})
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"customer": toCustomerDTO(out.Customer),
		"order":    toOrderDTO(out.Order),
	})
}

// =====================================================================
// Documents handler
// =====================================================================

func (h *Handler) updateDocument(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "doc")
	if !ok {
		return
	}
	var req updateDocumentRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out, err := h.uc.UpdateDocument(r.Context(), port.UpdateDocumentInput{
		ID:        id,
		Submitted: req.Submitted,
		FileURL:   req.FileURL,
		Notes:     req.Notes,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	// Log to the lead timeline only when the doc transitions to
	// submitted=true. Notes-only edits don't warrant a timeline row.
	if req.Submitted != nil && *req.Submitted {
		var actor *uuid.UUID
		if c := httpserver.ClaimsFromContext(r.Context()); c != nil {
			uid := c.UserID
			actor = &uid
		}
		h.logLeadEvent(r.Context(), out.LeadID, actor, "doc_uploaded",
			"Document submitted: "+out.Label,
			map[string]any{"doc_key": out.DocKey, "doc_id": out.ID.String()})
	}
	httpserver.WriteJSON(w, http.StatusOK, toDocumentDTO(*out))
}

// =====================================================================
// Customers / Orders read-only handlers
// =====================================================================

func (h *Handler) listCustomers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := httpserver.ParseIntDefault(q.Get("page_size"), 50)
	page := httpserver.ParseIntDefault(q.Get("page"), 1)
	out, total, err := h.uc.ListCustomers(r.Context(), q.Get("status"), limit, (page-1)*limit)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	items := make([]customerDTO, 0, len(out))
	for _, c := range out {
		items = append(items, toCustomerDTO(c))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"items": items, "total": total, "page": page, "page_size": limit,
	})
}

func (h *Handler) getCustomer(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "customer")
	if !ok {
		return
	}
	c, err := h.uc.GetCustomer(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toCustomerDTO(*c))
}

func (h *Handler) listOrders(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := httpserver.ParseIntDefault(q.Get("page_size"), 50)
	page := httpserver.ParseIntDefault(q.Get("page"), 1)

	var (
		out   []domain.Order
		total int
		err   error
	)
	if cid := q.Get("customer_id"); cid != "" {
		// Customer-scoped listing — used by /crm/customers/[id] to render
		// the customer's order(s) (and the new OTC type pill, Gap B).
		customerID, parseErr := uuid.Parse(cid)
		if parseErr != nil {
			httpserver.WriteError(w, errors.Validation("order.customer_id_invalid", "customer_id is not a uuid"))
			return
		}
		out, total, err = h.uc.ListOrdersForCustomer(r.Context(), customerID, limit, (page-1)*limit)
	} else {
		out, total, err = h.uc.ListOrders(r.Context(), q.Get("status"), limit, (page-1)*limit)
	}
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	items := make([]orderDTO, 0, len(out))
	for _, o := range out {
		items = append(items, toOrderDTO(o))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"items": items, "total": total, "page": page, "page_size": limit,
	})
}

func (h *Handler) getOrder(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "order")
	if !ok {
		return
	}
	o, err := h.uc.GetOrder(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toOrderDTO(*o))
}

// =====================================================================
// helpers
// =====================================================================

