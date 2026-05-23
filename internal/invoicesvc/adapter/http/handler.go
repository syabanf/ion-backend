// Package http is the driving adapter for the invoicesvc bounded context.
//
// Three surfaces under one handler:
//
//   - /api/invoicesvc/snapshots/...      — issuance-time snapshot CRUD
//   - /api/invoicesvc/credit-notes/...   — credit-note state machine
//   - /api/invoicesvc/bulk-jobs/...      — bulk generation queue
//   - /api/invoicesvc/my/invoices/...    — customer-self portal
//   - /api/invoicesvc/aggregations       — dashboard rollup
//   - /api/invoicesvc/cycles/{id}/health — generation health
//   - /api/invoicesvc/top-overdue        — overdue customer ranking
//
// RBAC: per-route RequirePermission gates against the perms seeded in
// migration 0078. Customer routes additionally enforce self-scope by
// reading claims.UserID (the portal-issued JWT puts customer_id into
// UserID — same convention crm/portal_auth.go uses).
package http

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/invoicesvc/domain"
	"github.com/ion-core/backend/internal/invoicesvc/port"
	"github.com/ion-core/backend/pkg/auth"
	"github.com/ion-core/backend/pkg/errors"
	"github.com/ion-core/backend/pkg/httpserver"
)

type Handler struct {
	snapshots  port.SnapshotUseCase
	creditNotes port.CreditNoteUseCase
	bulk       port.BulkUseCase
	monitoring port.MonitoringUseCase
	verifier   *auth.Verifier
}

func NewHandler(
	snapshots port.SnapshotUseCase,
	creditNotes port.CreditNoteUseCase,
	bulk port.BulkUseCase,
	monitoring port.MonitoringUseCase,
	verifier *auth.Verifier,
) *Handler {
	return &Handler{
		snapshots:   snapshots,
		creditNotes: creditNotes,
		bulk:        bulk,
		monitoring:  monitoring,
		verifier:    verifier,
	}
}

func (h *Handler) Mount(r chi.Router) {
	r.Route("/api/invoicesvc", func(r chi.Router) {
		r.Use(httpserver.RequireAuth(h.verifier))

		// Snapshots ----------------------------------------------------
		r.With(httpserver.RequirePermission("invoicesvc.snapshot.read")).
			Post("/snapshots", h.createSnapshot)
		r.With(httpserver.RequirePermission("invoicesvc.snapshot.read")).
			Get("/snapshots/{id}", h.getSnapshot)
		r.With(httpserver.RequirePermission("invoicesvc.snapshot.read")).
			Get("/invoices/{invoice_id}/snapshots", h.listSnapshotsForInvoice)

		// Credit notes -------------------------------------------------
		r.With(httpserver.RequirePermission("invoicesvc.credit_note.write")).
			Post("/credit-notes", h.createCreditNote)
		r.With(httpserver.RequirePermission("invoicesvc.credit_note.approve")).
			Post("/credit-notes/{id}/issue", h.issueCreditNote)
		r.With(httpserver.RequirePermission("invoicesvc.credit_note.approve")).
			Post("/credit-notes/{id}/apply", h.applyCreditNote)
		r.With(httpserver.RequirePermission("invoicesvc.credit_note.write")).
			Post("/credit-notes/{id}/void", h.voidCreditNote)
		r.With(httpserver.RequirePermission("invoicesvc.credit_note.read")).
			Get("/credit-notes/{id}", h.getCreditNote)
		r.With(httpserver.RequirePermission("invoicesvc.credit_note.read")).
			Get("/credit-notes", h.listCreditNotes)

		// Bulk jobs ---------------------------------------------------
		r.With(httpserver.RequirePermission("invoicesvc.bulk.run")).
			Post("/bulk-jobs", h.startBulkJob)
		r.With(httpserver.RequirePermission("invoicesvc.bulk.read")).
			Get("/bulk-jobs/{id}", h.getBulkJob)
		r.With(httpserver.RequirePermission("invoicesvc.bulk.read")).
			Get("/bulk-jobs", h.listBulkJobs)

		// Customer-self monitoring (scoped via claims.UserID) ---------
		r.With(httpserver.RequirePermission("invoicesvc.monitoring.read.self")).
			Get("/my/invoices", h.myInvoices)
		r.With(httpserver.RequirePermission("invoicesvc.monitoring.read.self")).
			Get("/my/invoices/{id}", h.myInvoice)
		r.With(httpserver.RequirePermission("invoicesvc.monitoring.read.self")).
			Get("/my/payments", h.myPaymentHistory)
		r.With(httpserver.RequirePermission("invoicesvc.monitoring.read.self")).
			Get("/my/reminders", h.myReminderHistory)

		// Dashboard monitoring ---------------------------------------
		r.With(httpserver.RequirePermission("invoicesvc.monitoring.read")).
			Get("/aggregations", h.aggregations)
		r.With(httpserver.RequirePermission("invoicesvc.monitoring.read")).
			Get("/cycles/{cycle_id}/health", h.cycleHealth)
		r.With(httpserver.RequirePermission("invoicesvc.monitoring.read")).
			Get("/top-overdue", h.topOverdue)
	})
}

// ---------------------------------------------------------------------
// Snapshots
// ---------------------------------------------------------------------

func (h *Handler) createSnapshot(w http.ResponseWriter, r *http.Request) {
	var req createSnapshotRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	invID, err := uuid.Parse(req.InvoiceID)
	if err != nil {
		httpserver.WriteError(w, errors.Validation("snapshot.invoice_id_invalid", "invoice_id is not a valid uuid"))
		return
	}
	var schemaID *uuid.UUID
	if req.SchemaSnapshotID != "" {
		if u, err := uuid.Parse(req.SchemaSnapshotID); err == nil {
			schemaID = &u
		}
	}
	lines := make([]domain.SnapshotLineItem, 0, len(req.LineItems))
	for _, l := range req.LineItems {
		lines = append(lines, domain.SnapshotLineItem{
			Description: l.Description,
			ItemType:    l.ItemType,
			Quantity:    l.Quantity,
			UnitPrice:   l.UnitPrice,
			Amount:      l.Amount,
		})
	}
	snap, err := h.snapshots.CreateSnapshot(r.Context(), invID, lines, schemaID)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toSnapshotDTO(*snap))
}

func (h *Handler) getSnapshot(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "snapshot")
	if !ok {
		return
	}
	snap, err := h.snapshots.GetSnapshot(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	if snap == nil {
		httpserver.WriteError(w, errors.NotFound("snapshot.not_found", "snapshot not found"))
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toSnapshotDTO(*snap))
}

func (h *Handler) listSnapshotsForInvoice(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "invoice_id", "invoice")
	if !ok {
		return
	}
	snaps, err := h.snapshots.ListSnapshots(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]snapshotDTO, 0, len(snaps))
	for _, s := range snaps {
		out = append(out, toSnapshotDTO(s))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": out, "total": len(out)})
}

// ---------------------------------------------------------------------
// Credit notes
// ---------------------------------------------------------------------

func (h *Handler) createCreditNote(w http.ResponseWriter, r *http.Request) {
	var req createCreditNoteRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	invID, err := uuid.Parse(req.InvoiceID)
	if err != nil {
		httpserver.WriteError(w, errors.Validation("credit_note.invoice_id_invalid", "invoice_id is not a valid uuid"))
		return
	}
	var custID *uuid.UUID
	if req.CustomerID != "" {
		if u, err := uuid.Parse(req.CustomerID); err == nil {
			custID = &u
		}
	}
	by := claimsUserID(r)
	cn, err := h.creditNotes.Create(r.Context(), invID, custID, req.Amount, req.Reason, by)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toCreditNoteDTO(*cn))
}

func (h *Handler) issueCreditNote(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "credit_note")
	if !ok {
		return
	}
	cn, err := h.creditNotes.Issue(r.Context(), id, claimsUserID(r))
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toCreditNoteDTO(*cn))
}

func (h *Handler) applyCreditNote(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "credit_note")
	if !ok {
		return
	}
	cn, err := h.creditNotes.Apply(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toCreditNoteDTO(*cn))
}

func (h *Handler) voidCreditNote(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "credit_note")
	if !ok {
		return
	}
	var req voidCreditNoteRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	cn, err := h.creditNotes.Void(r.Context(), id, claimsUserID(r), req.Reason)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toCreditNoteDTO(*cn))
}

func (h *Handler) getCreditNote(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "credit_note")
	if !ok {
		return
	}
	cn, err := h.creditNotes.Get(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toCreditNoteDTO(*cn))
}

func (h *Handler) listCreditNotes(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := port.CreditNoteFilter{
		Status: q.Get("status"),
		Limit:  httpserver.ParseIntDefault(q.Get("page_size"), 50),
		Offset: 0,
	}
	page := httpserver.ParseIntDefault(q.Get("page"), 1)
	if page > 1 {
		f.Offset = (page - 1) * f.Limit
	}
	if v := q.Get("invoice_id"); v != "" {
		if u, err := uuid.Parse(v); err == nil {
			f.InvoiceID = &u
		}
	}
	if v := q.Get("customer_id"); v != "" {
		if u, err := uuid.Parse(v); err == nil {
			f.CustomerID = &u
		}
	}
	items, total, err := h.creditNotes.List(r.Context(), f)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]creditNoteDTO, 0, len(items))
	for _, c := range items {
		out = append(out, toCreditNoteDTO(c))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": out, "total": total})
}

// ---------------------------------------------------------------------
// Bulk jobs
// ---------------------------------------------------------------------

func (h *Handler) startBulkJob(w http.ResponseWriter, r *http.Request) {
	var req startBulkJobRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	in := port.StartBulkJobInput{
		Kind:         domain.BulkJobKind(strings.TrimSpace(req.Kind)),
		TargetFilter: req.TargetFilter,
		CreatedBy:    claimsUserID(r),
	}
	job, err := h.bulk.StartJob(r.Context(), in)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toBulkJobDTO(*job))
}

func (h *Handler) getBulkJob(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "bulk_job")
	if !ok {
		return
	}
	job, items, err := h.bulk.JobStatus(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	itemsOut := make([]bulkItemDTO, 0, len(items))
	for _, it := range items {
		itemsOut = append(itemsOut, toBulkItemDTO(it))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"job":   toBulkJobDTO(*job),
		"items": itemsOut,
	})
}

func (h *Handler) listBulkJobs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := port.BulkJobFilter{
		Kind:   q.Get("kind"),
		Status: q.Get("status"),
		Limit:  httpserver.ParseIntDefault(q.Get("page_size"), 50),
		Offset: 0,
	}
	page := httpserver.ParseIntDefault(q.Get("page"), 1)
	if page > 1 {
		f.Offset = (page - 1) * f.Limit
	}
	items, total, err := h.bulk.List(r.Context(), f)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]bulkJobDTO, 0, len(items))
	for _, j := range items {
		out = append(out, toBulkJobDTO(j))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": out, "total": total})
}

// ---------------------------------------------------------------------
// Customer-self monitoring
// ---------------------------------------------------------------------

func (h *Handler) myInvoices(w http.ResponseWriter, r *http.Request) {
	cid := claimsCustomerID(r)
	if cid == uuid.Nil {
		httpserver.WriteError(w, errors.Unauthorized("monitoring.no_customer", "no customer context"))
		return
	}
	q := r.URL.Query()
	f := port.CustomerInvoiceFilter{
		Status: q.Get("status"),
		Limit:  httpserver.ParseIntDefault(q.Get("page_size"), 50),
		Offset: 0,
	}
	page := httpserver.ParseIntDefault(q.Get("page"), 1)
	if page > 1 {
		f.Offset = (page - 1) * f.Limit
	}
	rows, total, err := h.monitoring.MyInvoices(r.Context(), cid, f)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]invoiceProjectionDTO, 0, len(rows))
	for _, p := range rows {
		out = append(out, toInvoiceProjectionDTO(p))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": out, "total": total})
}

func (h *Handler) myInvoice(w http.ResponseWriter, r *http.Request) {
	cid := claimsCustomerID(r)
	if cid == uuid.Nil {
		httpserver.WriteError(w, errors.Unauthorized("monitoring.no_customer", "no customer context"))
		return
	}
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "invoice")
	if !ok {
		return
	}
	proj, err := h.monitoring.MyInvoice(r.Context(), cid, id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toInvoiceProjectionDTO(*proj))
}

func (h *Handler) myPaymentHistory(w http.ResponseWriter, r *http.Request) {
	cid := claimsCustomerID(r)
	if cid == uuid.Nil {
		httpserver.WriteError(w, errors.Unauthorized("monitoring.no_customer", "no customer context"))
		return
	}
	limit := httpserver.ParseIntDefault(r.URL.Query().Get("limit"), 50)
	rows, err := h.monitoring.MyPaymentHistory(r.Context(), cid, limit)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]paymentHistoryDTO, 0, len(rows))
	for _, p := range rows {
		out = append(out, toPaymentHistoryDTO(p))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (h *Handler) myReminderHistory(w http.ResponseWriter, r *http.Request) {
	cid := claimsCustomerID(r)
	if cid == uuid.Nil {
		httpserver.WriteError(w, errors.Unauthorized("monitoring.no_customer", "no customer context"))
		return
	}
	limit := httpserver.ParseIntDefault(r.URL.Query().Get("limit"), 50)
	rows, err := h.monitoring.MyReminderHistory(r.Context(), cid, limit)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]reminderHistoryDTO, 0, len(rows))
	for _, p := range rows {
		out = append(out, toReminderHistoryDTO(p))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}

// ---------------------------------------------------------------------
// Dashboard monitoring
// ---------------------------------------------------------------------

func (h *Handler) aggregations(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := port.InvoiceQueryFilter{Status: q.Get("status")}
	if v := q.Get("from"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.From = &t
		}
	}
	if v := q.Get("to"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.To = &t
		}
	}
	if v := q.Get("plan_id"); v != "" {
		if u, err := uuid.Parse(v); err == nil {
			f.PlanID = &u
		}
	}
	if v := q.Get("branch_id"); v != "" {
		if u, err := uuid.Parse(v); err == nil {
			f.BranchID = &u
		}
	}
	if v := q.Get("customer_id"); v != "" {
		if u, err := uuid.Parse(v); err == nil {
			f.CustomerID = &u
		}
	}
	agg, err := h.monitoring.Aggregations(r.Context(), f)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toAggregationDTO(*agg))
}

func (h *Handler) cycleHealth(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "cycle_id", "cycle")
	if !ok {
		return
	}
	out, err := h.monitoring.CycleHealth(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toCycleHealthDTO(*out))
}

func (h *Handler) topOverdue(w http.ResponseWriter, r *http.Request) {
	limit := httpserver.ParseIntDefault(r.URL.Query().Get("limit"), 10)
	rows, err := h.monitoring.TopOverdueCustomers(r.Context(), limit)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]topOverdueDTO, 0, len(rows))
	for _, r := range rows {
		out = append(out, toTopOverdueDTO(r))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}

// ---------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------

// claimsUserID pulls the authenticated user's id from the JWT claims.
// Returns nil for unauthenticated requests (caller wraps with
// RequireAuth so this is mostly defensive).
func claimsUserID(r *http.Request) *uuid.UUID {
	c := httpserver.ClaimsFromContext(r.Context())
	if c == nil || c.UserID == uuid.Nil {
		return nil
	}
	id := c.UserID
	return &id
}

// claimsCustomerID returns the customer_id baked into a portal-issued
// JWT. Per the crm/portal_auth.go convention, the customer's UUID rides
// in claims.UserID for portal tokens — the same field internal users
// use for user_id. The /portal/me handler trusts this convention; we
// follow suit for symmetry.
func claimsCustomerID(r *http.Request) uuid.UUID {
	c := httpserver.ClaimsFromContext(r.Context())
	if c == nil {
		return uuid.Nil
	}
	return c.UserID
}
