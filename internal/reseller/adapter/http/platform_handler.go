package http

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/reseller/port"
	"github.com/ion-core/backend/pkg/errors"
	"github.com/ion-core/backend/pkg/httpserver"
)

// ctxKey is the local context key type — keeping it package-private
// prevents accidental cross-package value collisions.
type ctxKey int

const (
	ctxKeyTenant ctxKey = iota
)

// TenantFromContext returns the reseller-tenant id resolved by the
// TenantScope middleware. Returns uuid.Nil if no tenant was attached
// (shouldn't happen behind TenantScope, but defensive).
func TenantFromContext(ctx context.Context) uuid.UUID {
	v, _ := ctx.Value(ctxKeyTenant).(uuid.UUID)
	return v
}

// TenantScope is the reseller-platform middleware. It reads the bearer
// token from `Authorization: Bearer <token>`, resolves it to a
// reseller account via the PlatformResolver, and stashes the
// resulting account id in the request context. Every downstream
// handler MUST use TenantFromContext(ctx) when querying tenant-scoped
// data so a missing WHERE clause becomes a 404 rather than a leak.
//
// The resolver is responsible for:
//   - Unauthorized on missing/invalid/expired token
//   - Forbidden on suspended/terminated reseller
//
// We pass through those errors verbatim via httpserver.WriteError.
func TenantScope(resolver port.PlatformResolver) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := r.Header.Get("Authorization")
			if !strings.HasPrefix(h, "Bearer ") {
				writeErr(w, errors.Unauthorized("session.missing", "missing bearer token"))
				return
			}
			token := strings.TrimPrefix(h, "Bearer ")
			tenantID, err := resolver.ResolveTenant(r.Context(), token)
			if err != nil {
				writeErr(w, err)
				return
			}
			ctx := context.WithValue(r.Context(), ctxKeyTenant, tenantID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// PlatformHandler is the public reseller-platform HTTP surface. Every
// route under `/api/platform/*` (except session issuance) is scoped
// to the resolved tenant; the handlers pull TenantFromContext and
// pass it down to the usecase.
//
// Routes:
//
//	POST  /api/platform/sessions                        (issue token; no tenant)
//	GET   /api/platform/me                              [tenant-scoped]
//	GET   /api/platform/wholesale/skus                  [tenant-scoped]
//	POST  /api/platform/wholesale/orders                [tenant-scoped]
//	GET   /api/platform/wholesale/orders                [tenant-scoped]
//	GET   /api/platform/subscribers                     [tenant-scoped] (Wave 102)
//	POST  /api/platform/subscribers                     [tenant-scoped] (Wave 102)
//	GET   /api/platform/subscribers/{id}                [tenant-scoped] (Wave 102)
//	PATCH /api/platform/subscribers/{id}                [tenant-scoped] (Wave 102)
//	POST  /api/platform/subscribers/{id}/suspend        [tenant-scoped] (Wave 102)
//	POST  /api/platform/subscribers/{id}/reactivate     [tenant-scoped] (Wave 102)
//	POST  /api/platform/subscribers/{id}/terminate      [tenant-scoped] (Wave 102)
//	POST  /api/platform/subscribers/import              [tenant-scoped] (Wave 102, multipart)
//	GET   /api/platform/invoices                        [tenant-scoped] (Wave 102)
//	GET   /api/platform/invoices/{id}                   [tenant-scoped] (Wave 102)
//	POST  /api/platform/invoices/{id}/mark-paid         [tenant-scoped] (Wave 102)
//	GET   /api/platform/dashboard/mtd                   [tenant-scoped] (Wave 102)
type PlatformHandler struct {
	platform    port.PlatformUseCase
	onboarding  port.OnboardingUseCase
	wholesale   port.WholesaleUseCase
	subscribers port.SubscriberUseCase
	invoices    port.InvoiceInboxUseCase
	dashboard   port.DashboardUseCase
}

// NewPlatformHandler constructs the handler with the Wave 94 surface
// only. Wave 102 use cases are optional — wired in via
// WithPlatformExtensions before Mount so existing call sites (and
// tests that only need the Wave 94 routes) keep working.
func NewPlatformHandler(platform port.PlatformUseCase, onboarding port.OnboardingUseCase, wholesale port.WholesaleUseCase) *PlatformHandler {
	return &PlatformHandler{platform: platform, onboarding: onboarding, wholesale: wholesale}
}

// WithPlatformExtensions attaches the Wave 102 services (subscriber
// CRUD, invoice inbox, dashboard). The cmd/reseller-svc bootstrap
// calls this between construction and Mount.
func (h *PlatformHandler) WithPlatformExtensions(
	subs port.SubscriberUseCase,
	invoices port.InvoiceInboxUseCase,
	dashboard port.DashboardUseCase,
) *PlatformHandler {
	h.subscribers = subs
	h.invoices = invoices
	h.dashboard = dashboard
	return h
}

func (h *PlatformHandler) Mount(r chi.Router) {
	r.Route("/api/platform", func(r chi.Router) {
		// Session issuance — NOT tenant-scoped. The caller proves
		// possession of the per-reseller shared secret to get a token.
		r.Post("/sessions", h.issueSession)

		// Everything else is tenant-scoped via TenantScope middleware.
		r.Group(func(r chi.Router) {
			r.Use(TenantScope(h.platform))

			r.Get("/me", h.getMe)
			r.Get("/wholesale/skus", h.listSKUs)
			r.Post("/wholesale/orders", h.createOrder)
			r.Get("/wholesale/orders", h.listOrders)

			// Wave 102 routes are only mounted if the extension hook
			// was called. This keeps NewPlatformHandler-only callers
			// (tests) from breaking with a nil deref.
			if h.subscribers != nil {
				r.Get("/subscribers", h.listSubscribers)
				r.Post("/subscribers", h.createSubscriber)
				r.Get("/subscribers/{id}", h.getSubscriber)
				r.Patch("/subscribers/{id}", h.updateSubscriber)
				r.Post("/subscribers/{id}/suspend", h.suspendSubscriber)
				r.Post("/subscribers/{id}/reactivate", h.reactivateSubscriber)
				r.Post("/subscribers/{id}/terminate", h.terminateSubscriber)
				r.Post("/subscribers/import", h.importSubscribers)
			}
			if h.invoices != nil {
				r.Get("/invoices", h.listInvoices)
				r.Get("/invoices/{id}", h.getInvoice)
				r.Post("/invoices/{id}/mark-paid", h.markInvoicePaid)
			}
			if h.dashboard != nil {
				r.Get("/dashboard/mtd", h.getDashboardMTD)
			}
		})
	})
}

// ---------------------------------------------------------------------
// Session issuance
// ---------------------------------------------------------------------

func (h *PlatformHandler) issueSession(w http.ResponseWriter, r *http.Request) {
	var req issueSessionRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	id, err := parseUUID(req.ResellerID, "reseller_id")
	if err != nil {
		writeErr(w, err)
		return
	}
	ttl := time.Duration(req.TTLSeconds) * time.Second
	sess, err := h.platform.IssueSession(r.Context(), id, req.Secret, ttl)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toPlatformSessionDTO(*sess))
}

// ---------------------------------------------------------------------
// Tenant-scoped handlers
// ---------------------------------------------------------------------

func (h *PlatformHandler) getMe(w http.ResponseWriter, r *http.Request) {
	tenantID := TenantFromContext(r.Context())
	if tenantID == uuid.Nil {
		writeErr(w, errors.Unauthorized("session.missing", "tenant not resolved"))
		return
	}
	a, err := h.onboarding.GetAccount(r.Context(), tenantID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toResellerAccountDTO(*a))
}

func (h *PlatformHandler) listSKUs(w http.ResponseWriter, r *http.Request) {
	tenantID := TenantFromContext(r.Context())
	if tenantID == uuid.Nil {
		writeErr(w, errors.Unauthorized("session.missing", "tenant not resolved"))
		return
	}
	q := r.URL.Query()
	page := httpserver.ParseIntDefault(q.Get("page"), 1)
	if page < 1 {
		page = 1
	}
	pageSize := httpserver.ParseIntDefault(q.Get("page_size"), 50)
	f := port.WholesaleSKUListFilter{
		// Platform always sees only active SKUs.
		OnlyActive: true,
		Limit:      pageSize,
		Offset:     (page - 1) * pageSize,
	}
	if s := q.Get("supplier_subsidiary_id"); s != "" {
		u, err := parseUUID(s, "supplier_subsidiary_id")
		if err != nil {
			writeErr(w, err)
			return
		}
		f.SupplierSubsidiaryID = &u
	}
	items, total, err := h.wholesale.ListSKUs(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]wholesaleSKUDTO, 0, len(items))
	for _, s := range items {
		out = append(out, toWholesaleSKUDTO(s))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":     out,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

func (h *PlatformHandler) createOrder(w http.ResponseWriter, r *http.Request) {
	tenantID := TenantFromContext(r.Context())
	if tenantID == uuid.Nil {
		writeErr(w, errors.Unauthorized("session.missing", "tenant not resolved"))
		return
	}
	var req createOrderRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	if len(req.Lines) == 0 {
		writeErr(w, errors.Validation("order.empty", "lines must be non-empty"))
		return
	}
	lines := make([]port.WholesaleOrderLineInput, 0, len(req.Lines))
	for i, l := range req.Lines {
		skuID, err := parseUUID(l.SKUID, "sku_id")
		if err != nil {
			writeErr(w, err)
			return
		}
		if l.Qty <= 0 {
			writeErr(w, errors.Validation("order.line_qty_invalid",
				"qty must be > 0 at line "+itoa(i)))
			return
		}
		lines = append(lines, port.WholesaleOrderLineInput{SKUID: skuID, Qty: l.Qty})
	}
	o, err := h.wholesale.CreateOrder(r.Context(), port.CreateWholesaleOrderInput{
		ResellerAccountID: tenantID,
		Lines:             lines,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toWholesaleOrderDTO(*o))
}

func (h *PlatformHandler) listOrders(w http.ResponseWriter, r *http.Request) {
	tenantID := TenantFromContext(r.Context())
	if tenantID == uuid.Nil {
		writeErr(w, errors.Unauthorized("session.missing", "tenant not resolved"))
		return
	}
	q := r.URL.Query()
	page := httpserver.ParseIntDefault(q.Get("page"), 1)
	if page < 1 {
		page = 1
	}
	pageSize := httpserver.ParseIntDefault(q.Get("page_size"), 50)
	f := port.WholesaleOrderListFilter{
		// Tenant filter is REQUIRED on the platform surface; passing
		// the resolved tenant id here is what enforces tenant
		// isolation. The repo treats uuid.Nil as "no filter" so we
		// MUST set this.
		ResellerAccountID: tenantID,
		Status:            q.Get("status"),
		Limit:             pageSize,
		Offset:            (page - 1) * pageSize,
	}
	items, total, err := h.wholesale.ListOrders(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]wholesaleOrderDTO, 0, len(items))
	for _, o := range items {
		out = append(out, toWholesaleOrderDTO(o))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":     out,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

// itoa is a tiny helper used in validation messages; we keep it local
// rather than dragging in strconv at every call site.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	n := len(buf)
	for i > 0 {
		n--
		buf[n] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		n--
		buf[n] = '-'
	}
	return string(buf[n:])
}
