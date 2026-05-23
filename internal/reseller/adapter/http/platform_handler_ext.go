package http

import (
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/reseller/port"
	"github.com/ion-core/backend/pkg/errors"
	"github.com/ion-core/backend/pkg/httpserver"
)

// =====================================================================
// Wave 102 — Subscribers
// =====================================================================
//
// Every handler:
//   1. Pulls tenant via TenantFromContext (defensive — TenantScope
//      already guarantees this, but a nil-tenant is fatal so we check).
//   2. Decodes the body / params.
//   3. Calls the corresponding usecase method (which auto-scopes).
//   4. Writes the DTO.
// The handlers don't carry tenant-isolation logic of their own — that
// lives in the usecase + repo, and the cross_tenant_test.go contract
// test exercises it.

func (h *PlatformHandler) requireTenant(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	tenantID := TenantFromContext(r.Context())
	if tenantID == uuid.Nil {
		writeErr(w, errors.Unauthorized("session.missing", "tenant not resolved"))
		return uuid.Nil, false
	}
	return tenantID, true
}

func (h *PlatformHandler) listSubscribers(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := h.requireTenant(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	page := httpserver.ParseIntDefault(q.Get("page"), 1)
	if page < 1 {
		page = 1
	}
	pageSize := httpserver.ParseIntDefault(q.Get("page_size"), 50)
	items, total, err := h.subscribers.ListMySubscribers(r.Context(), port.SubscriberListFilter{
		ResellerAccountID: tenantID,
		Status:            q.Get("status"),
		Limit:             pageSize,
		Offset:            (page - 1) * pageSize,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]subscriberDTO, 0, len(items))
	for _, s := range items {
		out = append(out, toSubscriberDTO(s))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":     out,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

func (h *PlatformHandler) createSubscriber(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireTenant(w, r); !ok {
		return
	}
	var req createSubscriberRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	in := port.CreateSubscriberInput{
		CustomerName:  req.CustomerName,
		CustomerEmail: req.CustomerEmail,
		CustomerPhone: req.CustomerPhone,
		AddressLine:   req.AddressLine,
		MonthlyFee:    req.MonthlyFee,
		Notes:         req.Notes,
	}
	if req.ResellerAccountID != "" {
		u, err := parseUUID(req.ResellerAccountID, "reseller_account_id")
		if err != nil {
			writeErr(w, err)
			return
		}
		in.ResellerAccountID = u
	}
	subID, err := parseOptionalUUIDStr(req.SubAreaID, "sub_area_id")
	if err != nil {
		writeErr(w, err)
		return
	}
	in.SubAreaID = subID
	planID, err := parseOptionalUUIDStr(req.ServicePlanID, "service_plan_id")
	if err != nil {
		writeErr(w, err)
		return
	}
	in.ServicePlanID = planID

	sub, err := h.subscribers.CreateSubscriber(r.Context(), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toSubscriberDTO(*sub))
}

func (h *PlatformHandler) getSubscriber(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireTenant(w, r); !ok {
		return
	}
	id, err := parseUUID(chi.URLParam(r, "id"), "subscriber")
	if err != nil {
		writeErr(w, err)
		return
	}
	sub, err := h.subscribers.GetMySubscriber(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSubscriberDTO(*sub))
}

func (h *PlatformHandler) updateSubscriber(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireTenant(w, r); !ok {
		return
	}
	id, err := parseUUID(chi.URLParam(r, "id"), "subscriber")
	if err != nil {
		writeErr(w, err)
		return
	}
	var req updateSubscriberRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	fields := port.UpdateSubscriberInput{
		CustomerName:  req.CustomerName,
		CustomerEmail: req.CustomerEmail,
		CustomerPhone: req.CustomerPhone,
		AddressLine:   req.AddressLine,
		MonthlyFee:    req.MonthlyFee,
		Notes:         req.Notes,
	}
	subID, err := parseOptionalUUIDStr(req.SubAreaID, "sub_area_id")
	if err != nil {
		writeErr(w, err)
		return
	}
	fields.SubAreaID = subID
	planID, err := parseOptionalUUIDStr(req.ServicePlanID, "service_plan_id")
	if err != nil {
		writeErr(w, err)
		return
	}
	fields.ServicePlanID = planID

	sub, err := h.subscribers.UpdateMySubscriber(r.Context(), id, fields)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSubscriberDTO(*sub))
}

func (h *PlatformHandler) suspendSubscriber(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireTenant(w, r); !ok {
		return
	}
	id, err := parseUUID(chi.URLParam(r, "id"), "subscriber")
	if err != nil {
		writeErr(w, err)
		return
	}
	var req suspendSubscriberRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	sub, err := h.subscribers.SuspendMySubscriber(r.Context(), id, req.Reason)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSubscriberDTO(*sub))
}

func (h *PlatformHandler) reactivateSubscriber(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireTenant(w, r); !ok {
		return
	}
	id, err := parseUUID(chi.URLParam(r, "id"), "subscriber")
	if err != nil {
		writeErr(w, err)
		return
	}
	sub, err := h.subscribers.ReactivateMySubscriber(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSubscriberDTO(*sub))
}

func (h *PlatformHandler) terminateSubscriber(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireTenant(w, r); !ok {
		return
	}
	id, err := parseUUID(chi.URLParam(r, "id"), "subscriber")
	if err != nil {
		writeErr(w, err)
		return
	}
	sub, err := h.subscribers.TerminateMySubscriber(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSubscriberDTO(*sub))
}

// importSubscribers accepts a CSV file as multipart/form-data (field
// name "file") OR as a raw text/csv body. The usecase parses the rows
// and returns an audit-trail SubscriberImport — partial imports are
// a first-class result so we return 200 + import summary regardless of
// row-level errors (clients inspect the status field).
const importMaxSize = 8 << 20 // 8 MiB cap to avoid memory exhaustion

func (h *PlatformHandler) importSubscribers(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireTenant(w, r); !ok {
		return
	}
	body, err := readCSVBody(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	im, err := h.subscribers.ImportSubscribersCSV(r.Context(), body)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSubscriberImportDTO(*im))
}

// readCSVBody pulls the CSV bytes out of either multipart form (field
// "file") or a raw text/csv body. Caps the payload to importMaxSize
// to limit memory blast radius.
func readCSVBody(r *http.Request) ([]byte, error) {
	ct := r.Header.Get("Content-Type")
	if len(ct) >= len("multipart/") && ct[:len("multipart/")] == "multipart/" {
		// 8 MiB form parse limit — same as importMaxSize for parity.
		if err := r.ParseMultipartForm(importMaxSize); err != nil {
			return nil, errors.Validation("subscriber_import.body_invalid", "could not parse multipart body")
		}
		f, _, err := r.FormFile("file")
		if err != nil {
			return nil, errors.Validation("subscriber_import.file_missing", "form field 'file' is required")
		}
		defer f.Close()
		body, err := io.ReadAll(io.LimitReader(f, importMaxSize))
		if err != nil {
			return nil, errors.Validation("subscriber_import.body_invalid", "could not read file")
		}
		return body, nil
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, importMaxSize))
	if err != nil {
		return nil, errors.Validation("subscriber_import.body_invalid", "could not read body")
	}
	if len(body) == 0 {
		return nil, errors.Validation("subscriber_import.empty", "csv body is empty")
	}
	return body, nil
}

// =====================================================================
// Wave 102 — Invoices
// =====================================================================

func (h *PlatformHandler) listInvoices(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := h.requireTenant(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	page := httpserver.ParseIntDefault(q.Get("page"), 1)
	if page < 1 {
		page = 1
	}
	pageSize := httpserver.ParseIntDefault(q.Get("page_size"), 50)
	f := port.InvoiceListFilter{
		ResellerAccountID: tenantID,
		Status:            q.Get("status"),
		Limit:             pageSize,
		Offset:            (page - 1) * pageSize,
	}
	if s := q.Get("subscriber_id"); s != "" {
		u, err := parseUUID(s, "subscriber_id")
		if err != nil {
			writeErr(w, err)
			return
		}
		f.SubscriberID = &u
	}
	if y := q.Get("period_year"); y != "" {
		f.PeriodYear = httpserver.ParseIntDefault(y, 0)
	}
	if m := q.Get("period_month"); m != "" {
		f.PeriodMonth = httpserver.ParseIntDefault(m, 0)
	}
	items, total, err := h.invoices.ListMyInvoices(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]subscriberInvoiceDTO, 0, len(items))
	for _, i := range items {
		out = append(out, toSubscriberInvoiceDTO(i))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":     out,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

func (h *PlatformHandler) getInvoice(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireTenant(w, r); !ok {
		return
	}
	id, err := parseUUID(chi.URLParam(r, "id"), "invoice")
	if err != nil {
		writeErr(w, err)
		return
	}
	inv, err := h.invoices.GetMyInvoice(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSubscriberInvoiceDTO(*inv))
}

func (h *PlatformHandler) markInvoicePaid(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireTenant(w, r); !ok {
		return
	}
	id, err := parseUUID(chi.URLParam(r, "id"), "invoice")
	if err != nil {
		writeErr(w, err)
		return
	}
	inv, err := h.invoices.MarkMyInvoicePaid(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSubscriberInvoiceDTO(*inv))
}

// =====================================================================
// Wave 102 — Dashboard
// =====================================================================

func (h *PlatformHandler) getDashboardMTD(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireTenant(w, r); !ok {
		return
	}
	d, err := h.dashboard.MTD(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}
