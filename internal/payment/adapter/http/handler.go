// Package http is the driving adapter for the payment bounded
// context. Same conventions as the other contexts:
//   - One handler struct per surface (here: a single Handler since the
//     payment surface is internal-only + the public webhook ingest).
//   - DTOs live alongside the handler.
//   - Per-route permission gating; the webhook endpoint is public
//     (signature-verified, no bearer-token gate).
package http

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/payment/domain"
	"github.com/ion-core/backend/internal/payment/port"
	"github.com/ion-core/backend/pkg/auth"
	"github.com/ion-core/backend/pkg/errors"
	"github.com/ion-core/backend/pkg/httpserver"
)

// Handler is the payment HTTP surface. Routes are mounted under
// /api/payment and split between admin (auth-gated, permission-gated)
// and public (webhook ingest — signature-verified inside the usecase).
//
// Routes:
//
//	POST   /api/payment/intents                                [payment.intent.write]
//	GET    /api/payment/intents/{id}                           [payment.intent.read]
//	GET    /api/payment/intents                                [payment.intent.read]
//	POST   /api/payment/intents/{id}/cancel                    [payment.intent.write]
//	POST   /api/payment/intents/{id}/route                     [payment.intent.write]
//	POST   /api/payment/webhooks/{gateway_code}                (PUBLIC; signature-verified)
//	POST   /api/payment/refunds                                [payment.refund.write]
//	POST   /api/payment/refunds/{id}/approve                   [payment.refund.approve]
//	POST   /api/payment/refunds/{id}/reject                    [payment.refund.approve]
//	POST   /api/payment/refunds/{id}/process                   [payment.refund.approve]
//	GET    /api/payment/refunds                                [payment.refund.read]
//	GET    /api/payment/refunds/{id}                           [payment.refund.read]
//	POST   /api/payment/h2h/statements                         [payment.h2h.upload]
//	POST   /api/payment/h2h/statements/{id}/match              [payment.h2h.match]
//	GET    /api/payment/h2h/statements                         [payment.h2h.read]
//	GET    /api/payment/h2h/statements/{id}                    [payment.h2h.read]
//	GET    /api/payment/gateways                               [payment.gateway.read]
type Handler struct {
	intents  port.IntentUseCase
	webhooks port.WebhookUseCase
	refunds  port.RefundUseCase
	h2h      port.H2HUseCase
	gateways port.GatewayUseCase

	// h2hUploadParser exposes the UploadAndParse helper (defined on the
	// concrete H2HService) without forcing it onto the port interface.
	// nil-tolerant: when nil we fall back to the port's UploadStatement
	// + ParseStatement two-step which only works for very small files.
	h2hUploadAndParse H2HUploadAndParser

	verifier *auth.Verifier
}

// H2HUploadAndParser exposes the upload-and-parse one-shot for HTTP
// callers. The concrete H2HService implements this; mocked tests may
// supply a stub.
type H2HUploadAndParser interface {
	UploadAndParse(ctx context.Context, in port.UploadH2HStatementInput) (*domain.H2HBankStatement, error)
}

func NewHandler(
	intents port.IntentUseCase,
	webhooks port.WebhookUseCase,
	refunds port.RefundUseCase,
	h2h port.H2HUseCase,
	gateways port.GatewayUseCase,
	uploader H2HUploadAndParser,
	verifier *auth.Verifier,
) *Handler {
	return &Handler{
		intents:           intents,
		webhooks:          webhooks,
		refunds:           refunds,
		h2h:               h2h,
		gateways:          gateways,
		h2hUploadAndParse: uploader,
		verifier:          verifier,
	}
}

// Mount registers every route. The webhook endpoint sits OUTSIDE the
// auth subgroup so it can be reached without a bearer token; signature
// verification happens inside the usecase via the GatewayClient.
func (h *Handler) Mount(r chi.Router) {
	// PUBLIC — webhook ingest.
	r.Post("/api/payment/webhooks/{gateway_code}", h.ingestWebhook)

	r.Route("/api/payment", func(r chi.Router) {
		r.Use(httpserver.RequireAuth(h.verifier))

		// Intents
		r.With(httpserver.RequirePermission("payment.intent.write")).
			Post("/intents", h.createIntent)
		r.With(httpserver.RequirePermission("payment.intent.read")).
			Get("/intents", h.listIntents)
		r.With(httpserver.RequirePermission("payment.intent.read")).
			Get("/intents/{id}", h.getIntent)
		r.With(httpserver.RequirePermission("payment.intent.write")).
			Post("/intents/{id}/cancel", h.cancelIntent)
		r.With(httpserver.RequirePermission("payment.intent.write")).
			Post("/intents/{id}/route", h.routeIntent)

		// Refunds
		r.With(httpserver.RequirePermission("payment.refund.write")).
			Post("/refunds", h.requestRefund)
		r.With(httpserver.RequirePermission("payment.refund.approve")).
			Post("/refunds/{id}/approve", h.approveRefund)
		r.With(httpserver.RequirePermission("payment.refund.approve")).
			Post("/refunds/{id}/reject", h.rejectRefund)
		r.With(httpserver.RequirePermission("payment.refund.approve")).
			Post("/refunds/{id}/process", h.processRefund)
		r.With(httpserver.RequirePermission("payment.refund.read")).
			Get("/refunds", h.listRefunds)
		r.With(httpserver.RequirePermission("payment.refund.read")).
			Get("/refunds/{id}", h.getRefund)

		// H2H bank statements
		r.With(httpserver.RequirePermission("payment.h2h.upload")).
			Post("/h2h/statements", h.uploadStatement)
		r.With(httpserver.RequirePermission("payment.h2h.match")).
			Post("/h2h/statements/{id}/match", h.matchStatement)
		r.With(httpserver.RequirePermission("payment.h2h.read")).
			Get("/h2h/statements", h.listStatements)
		r.With(httpserver.RequirePermission("payment.h2h.read")).
			Get("/h2h/statements/{id}", h.getStatement)

		// Gateways
		r.With(httpserver.RequirePermission("payment.gateway.read")).
			Get("/gateways", h.listGateways)
	})
}

// ---------------------------------------------------------------------
// Intents
// ---------------------------------------------------------------------

func (h *Handler) createIntent(w http.ResponseWriter, r *http.Request) {
	var req createIntentRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	invoiceID, err := parseUUID(req.InvoiceID, "invoice")
	if err != nil {
		writeErr(w, err)
		return
	}
	in := port.CreateIntentInput{
		InvoiceID:       invoiceID,
		Amount:          req.Amount,
		Currency:        req.Currency,
		IdempotencyKey:  strings.TrimSpace(r.Header.Get("Idempotency-Key")),
		PreferredMethod: req.PreferredMethod,
	}
	if in.IdempotencyKey == "" {
		in.IdempotencyKey = strings.TrimSpace(req.IdempotencyKey)
	}
	if req.CustomerID != "" {
		cid, err := parseUUID(req.CustomerID, "customer")
		if err != nil {
			writeErr(w, err)
			return
		}
		in.CustomerID = &cid
	}
	intent, err := h.intents.CreateIntent(r.Context(), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toIntentDTO(*intent))
}

func (h *Handler) listIntents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page := httpserver.ParseIntDefault(q.Get("page"), 1)
	if page < 1 {
		page = 1
	}
	pageSize := httpserver.ParseIntDefault(q.Get("page_size"), 50)
	f := port.IntentListFilter{
		Status: q.Get("status"),
		Limit:  pageSize,
		Offset: (page - 1) * pageSize,
	}
	if s := q.Get("invoice_id"); s != "" {
		u, err := parseUUID(s, "invoice")
		if err != nil {
			writeErr(w, err)
			return
		}
		f.InvoiceID = &u
	}
	if s := q.Get("customer_id"); s != "" {
		u, err := parseUUID(s, "customer")
		if err != nil {
			writeErr(w, err)
			return
		}
		f.CustomerID = &u
	}
	items, total, err := h.intents.ListIntents(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]intentDTO, 0, len(items))
	for _, i := range items {
		out = append(out, toIntentDTO(i))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":     out,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

func (h *Handler) getIntent(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "intent")
	if !ok {
		return
	}
	i, err := h.intents.GetIntent(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toIntentDTO(*i))
}

func (h *Handler) cancelIntent(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "intent")
	if !ok {
		return
	}
	i, err := h.intents.CancelIntent(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toIntentDTO(*i))
}

func (h *Handler) routeIntent(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "intent")
	if !ok {
		return
	}
	i, err := h.intents.RouteAndPay(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toIntentDTO(*i))
}

// ---------------------------------------------------------------------
// Webhooks (public)
// ---------------------------------------------------------------------

func (h *Handler) ingestWebhook(w http.ResponseWriter, r *http.Request) {
	code := strings.TrimSpace(chi.URLParam(r, "gateway_code"))
	if code == "" {
		writeErr(w, errors.Validation("webhook.gateway_code_required", "gateway_code path parameter is required"))
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 5*1024*1024))
	if err != nil {
		writeErr(w, errors.Validation("webhook.body_read_failed", "could not read webhook body"))
		return
	}
	in := port.WebhookIngestInput{
		GatewayCode: code,
		Signature:   firstHeaderValue(r, "X-Callback-Token", "X-Signature", "X-Callback-Signature", "X-Hub-Signature-256"),
		Payload:     body,
		EventID:     firstHeaderValue(r, "X-Event-Id", "X-Callback-Id"),
	}
	wh, err := h.webhooks.Ingest(r.Context(), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	// Webhooks always reply 200 (even on duplicate / suspect) so the
	// gateway stops retrying. The body carries the outcome for debugging.
	writeJSON(w, http.StatusOK, map[string]any{
		"id":     wh.ID.String(),
		"status": string(wh.Status),
	})
}

// ---------------------------------------------------------------------
// Refunds
// ---------------------------------------------------------------------

func (h *Handler) requestRefund(w http.ResponseWriter, r *http.Request) {
	var req requestRefundRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	intentID, err := parseUUID(req.PaymentIntentID, "intent")
	if err != nil {
		writeErr(w, err)
		return
	}
	in := port.RequestRefundInput{
		PaymentIntentID: intentID,
		Amount:          req.Amount,
		Reason:          req.Reason,
		RequestedBy:     actorUserID(r.Context()),
	}
	rf, err := h.refunds.RequestRefund(r.Context(), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toRefundDTO(*rf))
}

func (h *Handler) approveRefund(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "refund")
	if !ok {
		return
	}
	by := actorUserID(r.Context())
	if by == nil {
		writeErr(w, errors.Unauthorized("auth.missing", "authentication required"))
		return
	}
	rf, err := h.refunds.ApproveRefund(r.Context(), id, *by)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toRefundDTO(*rf))
}

func (h *Handler) rejectRefund(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "refund")
	if !ok {
		return
	}
	var req rejectRefundRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	rf, err := h.refunds.RejectRefund(r.Context(), id, req.Reason)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toRefundDTO(*rf))
}

func (h *Handler) processRefund(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "refund")
	if !ok {
		return
	}
	rf, err := h.refunds.ProcessRefund(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toRefundDTO(*rf))
}

func (h *Handler) listRefunds(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page := httpserver.ParseIntDefault(q.Get("page"), 1)
	if page < 1 {
		page = 1
	}
	pageSize := httpserver.ParseIntDefault(q.Get("page_size"), 50)
	f := port.RefundListFilter{
		Status: q.Get("status"),
		Limit:  pageSize,
		Offset: (page - 1) * pageSize,
	}
	if s := q.Get("intent_id"); s != "" {
		u, err := parseUUID(s, "intent")
		if err != nil {
			writeErr(w, err)
			return
		}
		f.PaymentIntentID = &u
	}
	items, total, err := h.refunds.ListRefunds(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]refundDTO, 0, len(items))
	for _, rf := range items {
		out = append(out, toRefundDTO(rf))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":     out,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

func (h *Handler) getRefund(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "refund")
	if !ok {
		return
	}
	rf, err := h.refunds.GetRefund(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toRefundDTO(*rf))
}

// ---------------------------------------------------------------------
// H2H
// ---------------------------------------------------------------------

func (h *Handler) uploadStatement(w http.ResponseWriter, r *http.Request) {
	// Accept multipart/form-data with a "file" part plus a
	// gateway_code form field; or a JSON body with base64 content for
	// CLI scripts. The multipart path is the primary surface.
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeErr(w, errors.Validation("h2h.upload_invalid", "expected multipart/form-data"))
		return
	}
	gatewayCode := strings.TrimSpace(r.FormValue("gateway_code"))
	if gatewayCode == "" {
		writeErr(w, errors.Validation("h2h.gateway_code_required", "gateway_code form field is required"))
		return
	}
	file, fh, err := r.FormFile("file")
	if err != nil {
		writeErr(w, errors.Validation("h2h.file_required", "file form field is required"))
		return
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, 32*1024*1024))
	if err != nil {
		writeErr(w, errors.Validation("h2h.file_read_failed", "could not read uploaded file"))
		return
	}
	in := port.UploadH2HStatementInput{
		GatewayCode: gatewayCode,
		Filename:    fh.Filename,
		Content:     content,
	}
	var stmt *domain.H2HBankStatement
	if h.h2hUploadAndParse != nil {
		stmt, err = h.h2hUploadAndParse.UploadAndParse(r.Context(), in)
	} else {
		stmt, err = h.h2h.UploadStatement(r.Context(), in)
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toH2HStatementDTO(*stmt))
}

func (h *Handler) matchStatement(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "statement")
	if !ok {
		return
	}
	stmt, err := h.h2h.MatchStatement(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toH2HStatementDTO(*stmt))
}

func (h *Handler) listStatements(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page := httpserver.ParseIntDefault(q.Get("page"), 1)
	if page < 1 {
		page = 1
	}
	pageSize := httpserver.ParseIntDefault(q.Get("page_size"), 50)
	items, total, err := h.h2h.ListStatements(r.Context(), pageSize, (page-1)*pageSize)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]h2hStatementDTO, 0, len(items))
	for _, s := range items {
		out = append(out, toH2HStatementDTO(s))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":     out,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

func (h *Handler) getStatement(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "statement")
	if !ok {
		return
	}
	s, err := h.h2h.GetStatement(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toH2HStatementDTO(*s))
}

// ---------------------------------------------------------------------
// Gateways
// ---------------------------------------------------------------------

func (h *Handler) listGateways(w http.ResponseWriter, r *http.Request) {
	onlyActive := r.URL.Query().Get("only_active") == "true"
	items, err := h.gateways.ListGateways(r.Context(), onlyActive)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]gatewayDTO, 0, len(items))
	for _, g := range items {
		out = append(out, toGatewayDTO(g))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// ---------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------

func actorUserID(ctx context.Context) *uuid.UUID {
	c := httpserver.ClaimsFromContext(ctx)
	if c == nil {
		return nil
	}
	id := c.UserID
	return &id
}

func parseUUID(s, field string) (uuid.UUID, error) {
	id, err := uuid.Parse(strings.TrimSpace(s))
	if err != nil {
		return uuid.Nil, errors.Validation(
			field+".id_invalid",
			field+" is not a valid uuid",
		)
	}
	return id, nil
}

func firstHeaderValue(r *http.Request, names ...string) string {
	for _, n := range names {
		if v := strings.TrimSpace(r.Header.Get(n)); v != "" {
			return v
		}
	}
	return ""
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	httpserver.WriteJSON(w, status, body)
}

func writeErr(w http.ResponseWriter, err error) {
	httpserver.WriteError(w, err)
}

func rfc3339Ptr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}

func uuidPtrString(u *uuid.UUID) *string {
	if u == nil {
		return nil
	}
	s := u.String()
	return &s
}
