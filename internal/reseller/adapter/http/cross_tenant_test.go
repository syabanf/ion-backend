// Package http contract test — load-bearing tenant isolation guarantee.
//
// The full path under test:
//
//	HTTP → TenantScope middleware → SubscriberUseCase / InvoiceInboxUseCase
//	     → SubscriberRepository / SubscriberInvoiceRepository (stub) → ✗ leak
//
// Strategy:
//  1. Stub repos hold an in-memory map of subscribers + invoices.
//  2. A fake TenantScope middleware reads tenant id from a custom
//     header so the test can flip tenants per request.
//  3. The test creates a row under tenant A, then issues a request
//     under tenant B's context for every platform-subscriber route
//     and asserts:
//       a. HTTP status ∈ {403, 404}
//       b. JSON error code is `subscriber.cross_tenant` OR
//          `subscriber.not_found`/`invoice.not_found`
//       c. NO row data leaks in the response body
//
// We test the integration through the actual chi router so middleware
// ordering + route mounting are exercised. The stub repos are the
// only mock: they implement the real port interface and emulate the
// tenant guard the production postgres adapters enforce.
package http

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/reseller/domain"
	"github.com/ion-core/backend/internal/reseller/port"
	"github.com/ion-core/backend/internal/reseller/usecase"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// ---------------------------------------------------------------------
// Stub repos — replicate the production tenant-guard semantics.
// ---------------------------------------------------------------------

type stubSubRepo struct {
	mu    sync.Mutex
	items map[uuid.UUID]domain.Subscriber
}

func newStubSubRepo() *stubSubRepo {
	return &stubSubRepo{items: map[uuid.UUID]domain.Subscriber{}}
}

var _ port.SubscriberRepository = (*stubSubRepo)(nil)

func (r *stubSubRepo) Create(ctx context.Context, s *domain.Subscriber) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items[s.ID] = *s
	return nil
}

func (r *stubSubRepo) FindByID(ctx context.Context, id uuid.UUID) (*domain.Subscriber, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.items[id]
	if !ok {
		return nil, notFoundErr("subscriber.not_found", "subscriber not found")
	}
	return &s, nil
}

// FindForReseller replicates the production guard: returns NotFound
// whenever the (reseller, id) pair isn't a match — even if the id
// exists under a different tenant.
func (r *stubSubRepo) FindForReseller(ctx context.Context, resellerID, id uuid.UUID) (*domain.Subscriber, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if resellerID == uuid.Nil {
		return nil, validationErr("subscriber.reseller_required", "reseller_account_id is required")
	}
	s, ok := r.items[id]
	if !ok || s.ResellerAccountID != resellerID {
		return nil, notFoundErr("subscriber.not_found", "subscriber not found")
	}
	return &s, nil
}

func (r *stubSubRepo) List(ctx context.Context, f port.SubscriberListFilter) ([]domain.Subscriber, int, error) {
	if f.ResellerAccountID == uuid.Nil {
		return nil, 0, validationErr("subscriber.tenant_filter_required", "reseller_account_id filter is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []domain.Subscriber{}
	for _, s := range r.items {
		if s.ResellerAccountID != f.ResellerAccountID {
			continue
		}
		if f.Status != "" && string(s.Status) != f.Status {
			continue
		}
		out = append(out, s)
	}
	return out, len(out), nil
}

func (r *stubSubRepo) Update(ctx context.Context, s *domain.Subscriber) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	existing, ok := r.items[s.ID]
	if !ok || existing.ResellerAccountID != s.ResellerAccountID {
		return notFoundErr("subscriber.not_found", "subscriber not found")
	}
	r.items[s.ID] = *s
	return nil
}

func (r *stubSubRepo) UpdateStatus(ctx context.Context, s *domain.Subscriber) error {
	return r.Update(ctx, s)
}

func (r *stubSubRepo) Count(ctx context.Context, f port.SubscriberListFilter) (int, error) {
	items, total, err := r.List(ctx, f)
	_ = items
	return total, err
}

// stub invoice repo

type stubInvoiceRepo struct {
	mu    sync.Mutex
	items map[uuid.UUID]domain.SubscriberInvoice
}

func newStubInvoiceRepo() *stubInvoiceRepo {
	return &stubInvoiceRepo{items: map[uuid.UUID]domain.SubscriberInvoice{}}
}

var _ port.SubscriberInvoiceRepository = (*stubInvoiceRepo)(nil)

func (r *stubInvoiceRepo) Create(ctx context.Context, i *domain.SubscriberInvoice) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items[i.ID] = *i
	return nil
}

func (r *stubInvoiceRepo) FindByID(ctx context.Context, id uuid.UUID) (*domain.SubscriberInvoice, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	i, ok := r.items[id]
	if !ok {
		return nil, notFoundErr("invoice.not_found", "invoice not found")
	}
	return &i, nil
}

func (r *stubInvoiceRepo) FindForReseller(ctx context.Context, resellerID, id uuid.UUID) (*domain.SubscriberInvoice, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if resellerID == uuid.Nil {
		return nil, validationErr("invoice.reseller_required", "reseller_account_id is required")
	}
	i, ok := r.items[id]
	if !ok || i.ResellerAccountID != resellerID {
		return nil, notFoundErr("invoice.not_found", "invoice not found")
	}
	return &i, nil
}

func (r *stubInvoiceRepo) List(ctx context.Context, f port.InvoiceListFilter) ([]domain.SubscriberInvoice, int, error) {
	if f.ResellerAccountID == uuid.Nil {
		return nil, 0, validationErr("invoice.tenant_filter_required", "reseller_account_id filter is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []domain.SubscriberInvoice{}
	for _, i := range r.items {
		if i.ResellerAccountID != f.ResellerAccountID {
			continue
		}
		if f.Status != "" && string(i.Status) != f.Status {
			continue
		}
		out = append(out, i)
	}
	return out, len(out), nil
}

func (r *stubInvoiceRepo) UpdateStatus(ctx context.Context, i *domain.SubscriberInvoice) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	existing, ok := r.items[i.ID]
	if !ok || existing.ResellerAccountID != i.ResellerAccountID {
		return notFoundErr("invoice.not_found", "invoice not found")
	}
	r.items[i.ID] = *i
	return nil
}

func (r *stubInvoiceRepo) ListOverdueForReseller(ctx context.Context, resellerID uuid.UUID, asOf time.Time) ([]domain.SubscriberInvoice, error) {
	return nil, nil
}

func (r *stubInvoiceRepo) SumPaidMTD(ctx context.Context, resellerID uuid.UUID, monthStart, asOf time.Time) (float64, error) {
	return 0, nil
}

func (r *stubInvoiceRepo) SumOpen(ctx context.Context, resellerID uuid.UUID) (float64, error) {
	return 0, nil
}

// stub import repo

type stubImportRepo struct {
	mu    sync.Mutex
	items map[uuid.UUID]domain.SubscriberImport
}

func newStubImportRepo() *stubImportRepo { return &stubImportRepo{items: map[uuid.UUID]domain.SubscriberImport{}} }

var _ port.SubscriberImportRepository = (*stubImportRepo)(nil)

func (r *stubImportRepo) Create(ctx context.Context, im *domain.SubscriberImport) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items[im.ID] = *im
	return nil
}
func (r *stubImportRepo) FindByID(ctx context.Context, id uuid.UUID) (*domain.SubscriberImport, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	im, ok := r.items[id]
	if !ok {
		return nil, notFoundErr("subscriber_import.not_found", "import not found")
	}
	return &im, nil
}
func (r *stubImportRepo) UpdateStatus(ctx context.Context, im *domain.SubscriberImport) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items[im.ID] = *im
	return nil
}
func (r *stubImportRepo) List(ctx context.Context, f port.SubscriberImportListFilter) ([]domain.SubscriberImport, int, error) {
	return nil, 0, nil
}

// errors helpers — thin wrappers so call sites stay terse.

func notFoundErr(code, msg string) error   { return derrors.NotFound(code, msg) }
func validationErr(code, msg string) error { return derrors.Validation(code, msg) }

// ---------------------------------------------------------------------
// Fake tenant scope — inject tenant from `X-Test-Tenant` header.
// ---------------------------------------------------------------------

// fakeTenantScope is a stand-in for production TenantScope that lets
// the test drive the tenant id directly. It mirrors the production
// behaviour: stash the tenant uuid in the context, fail
// Unauthorized if missing.
func fakeTenantScope(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := r.Header.Get("X-Test-Tenant")
		if raw == "" {
			http.Error(w, `{"error":{"code":"session.missing"}}`, http.StatusUnauthorized)
			return
		}
		u, err := uuid.Parse(raw)
		if err != nil {
			http.Error(w, `{"error":{"code":"session.missing"}}`, http.StatusUnauthorized)
			return
		}
		ctx := context.WithValue(r.Context(), ctxKeyTenant, u)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// buildTestRouter wires the Wave 102 routes against the stub repos
// and the fake tenant scope. The Wave 94 platform stub (issueSession
// etc.) isn't needed because the test only hits the Wave 102 routes.
func buildTestRouter(subRepo *stubSubRepo, invRepo *stubInvoiceRepo) chi.Router {
	importRepo := newStubImportRepo()
	subSvc := usecase.NewSubscriberService(subRepo, importRepo, TenantFromContext)
	invSvc := usecase.NewInvoiceInboxService(invRepo, TenantFromContext)
	// Dashboard not directly under test, but we wire it for parity.
	dashSvc := usecase.NewDashboardService(subRepo, invRepo, nil, TenantFromContext)

	h := &PlatformHandler{
		subscribers: subSvc,
		invoices:    invSvc,
		dashboard:   dashSvc,
	}

	r := chi.NewRouter()
	r.Route("/api/platform", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(fakeTenantScope)
			r.Get("/subscribers", h.listSubscribers)
			r.Post("/subscribers", h.createSubscriber)
			r.Get("/subscribers/{id}", h.getSubscriber)
			r.Patch("/subscribers/{id}", h.updateSubscriber)
			r.Post("/subscribers/{id}/suspend", h.suspendSubscriber)
			r.Post("/subscribers/{id}/reactivate", h.reactivateSubscriber)
			r.Post("/subscribers/{id}/terminate", h.terminateSubscriber)
			r.Post("/subscribers/import", h.importSubscribers)

			r.Get("/invoices", h.listInvoices)
			r.Get("/invoices/{id}", h.getInvoice)
			r.Post("/invoices/{id}/mark-paid", h.markInvoicePaid)
		})
	})
	return r
}

// ---------------------------------------------------------------------
// Cross-tenant block contract test.
// ---------------------------------------------------------------------

func TestCrossTenantBlock(t *testing.T) {
	tenantA := uuid.MustParse("00000000-0000-0000-0000-00000000000a")
	tenantB := uuid.MustParse("00000000-0000-0000-0000-00000000000b")

	subRepo := newStubSubRepo()
	invRepo := newStubInvoiceRepo()
	router := buildTestRouter(subRepo, invRepo)

	// --- seed: create subscriber + invoice under tenant A ---
	subA, err := domain.NewSubscriber(tenantA, "Tenant-A Subscriber", "a@example.com", "+62-811", 350000)
	if err != nil {
		t.Fatalf("seed subscriber: %v", err)
	}
	if err := subRepo.Create(context.Background(), subA); err != nil {
		t.Fatalf("seed subscriber persist: %v", err)
	}
	dueAt := time.Now().Add(7 * 24 * time.Hour)
	invA, err := domain.NewSubscriberInvoice(tenantA, subA.ID, "INV-A-001", 2026, 5, 350000, &dueAt)
	if err != nil {
		t.Fatalf("seed invoice: %v", err)
	}
	if err := invRepo.Create(context.Background(), invA); err != nil {
		t.Fatalf("seed invoice persist: %v", err)
	}

	// --- table-driven assertion: every route refuses cross-tenant access ---
	type tc struct {
		name           string
		method         string
		path           string
		body           string
		acceptStatuses []int // any of these is acceptable
		// acceptCodes is the set of error codes that proves the
		// isolation path triggered. NotFound from FindForReseller is
		// fine (the row's existence is hidden); cross_tenant from a
		// body-tenant-mismatch is fine (Forbidden).
		acceptCodes []string
	}

	subPath := "/api/platform/subscribers/" + subA.ID.String()
	invPath := "/api/platform/invoices/" + invA.ID.String()

	cases := []tc{
		{
			name:           "GET subscriber under tenant B",
			method:         http.MethodGet,
			path:           subPath,
			acceptStatuses: []int{http.StatusForbidden, http.StatusNotFound},
			acceptCodes:    []string{"subscriber.not_found", "subscriber.cross_tenant"},
		},
		{
			name:           "PATCH subscriber under tenant B",
			method:         http.MethodPatch,
			path:           subPath,
			body:           `{"customer_name":"hacked"}`,
			acceptStatuses: []int{http.StatusForbidden, http.StatusNotFound},
			acceptCodes:    []string{"subscriber.not_found", "subscriber.cross_tenant"},
		},
		{
			name:           "POST suspend under tenant B",
			method:         http.MethodPost,
			path:           subPath + "/suspend",
			body:           `{"reason":"hack"}`,
			acceptStatuses: []int{http.StatusForbidden, http.StatusNotFound},
			acceptCodes:    []string{"subscriber.not_found", "subscriber.cross_tenant"},
		},
		{
			name:           "POST reactivate under tenant B",
			method:         http.MethodPost,
			path:           subPath + "/reactivate",
			acceptStatuses: []int{http.StatusForbidden, http.StatusNotFound},
			acceptCodes:    []string{"subscriber.not_found", "subscriber.cross_tenant"},
		},
		{
			name:           "POST terminate under tenant B",
			method:         http.MethodPost,
			path:           subPath + "/terminate",
			acceptStatuses: []int{http.StatusForbidden, http.StatusNotFound},
			acceptCodes:    []string{"subscriber.not_found", "subscriber.cross_tenant"},
		},
		{
			name:           "GET invoice under tenant B",
			method:         http.MethodGet,
			path:           invPath,
			acceptStatuses: []int{http.StatusForbidden, http.StatusNotFound},
			acceptCodes:    []string{"invoice.not_found"},
		},
		{
			name:           "POST mark-paid under tenant B",
			method:         http.MethodPost,
			path:           invPath + "/mark-paid",
			acceptStatuses: []int{http.StatusForbidden, http.StatusNotFound},
			acceptCodes:    []string{"invoice.not_found"},
		},
		{
			name:           "POST create subscriber with tampered reseller_account_id under tenant B",
			method:         http.MethodPost,
			path:           "/api/platform/subscribers",
			body:           fmt.Sprintf(`{"reseller_account_id":%q,"customer_name":"Tampered","monthly_fee":100000}`, tenantA.String()),
			acceptStatuses: []int{http.StatusForbidden},
			acceptCodes:    []string{"subscriber.cross_tenant"},
		},
	}

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			var body *bytes.Reader
			if c.body == "" {
				body = bytes.NewReader(nil)
			} else {
				body = bytes.NewReader([]byte(c.body))
			}
			req := httptest.NewRequest(c.method, c.path, body)
			req.Header.Set("X-Test-Tenant", tenantB.String())
			if c.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, req)

			if !containsInt(c.acceptStatuses, rr.Code) {
				t.Fatalf("status: got %d, want one of %v\nbody=%s", rr.Code, c.acceptStatuses, rr.Body.String())
			}
			code := extractErrorCode(rr.Body.Bytes())
			if !containsStr(c.acceptCodes, code) {
				t.Fatalf("error code: got %q, want one of %v\nbody=%s", code, c.acceptCodes, rr.Body.String())
			}
			// Defense in depth: the response body must not echo
			// tenant A's name. The only way that would leak is if
			// the handler proceeded past the guard.
			if bytes.Contains(rr.Body.Bytes(), []byte("Tenant-A Subscriber")) {
				t.Fatalf("response leaked tenant-A subscriber data: %s", rr.Body.String())
			}
		})
	}

	// --- positive control: list under tenant B starts empty (before
	// the import case below plants a row under B). Run this BEFORE
	// the import scenario so the count is deterministic.
	t.Run("tenant B list is empty before import", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/platform/subscribers", nil)
		req.Header.Set("X-Test-Tenant", tenantB.String())
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("tenant B list: got %d, want 200\nbody=%s", rr.Code, rr.Body.String())
		}
		var resp map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode list: %v", err)
		}
		if total, _ := resp["total"].(float64); total != 0 {
			t.Fatalf("tenant B list: total=%v, want 0", total)
		}
	})

	// --- import route: a CSV upload under tenant B can never plant
	// rows under tenant A because the usecase pins the tenant from
	// context, not from the CSV body. We assert the import succeeds
	// (200) but the new row lands under tenant B, not tenant A. The
	// total under tenant A stays at 1; total under tenant B becomes 1.
	t.Run("CSV import under tenant B lands under tenant B only", func(t *testing.T) {
		csv := "customer_name,customer_email,monthly_fee\nImported-B,b@example.com,100000\n"
		req := httptest.NewRequest(http.MethodPost, "/api/platform/subscribers/import", strings.NewReader(csv))
		req.Header.Set("X-Test-Tenant", tenantB.String())
		req.Header.Set("Content-Type", "text/csv")
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("import: got %d, want 200\nbody=%s", rr.Code, rr.Body.String())
		}
		// Walk the repo directly to assert tenant pinning.
		var foundForA, foundForB int
		for _, s := range subRepo.items {
			switch s.ResellerAccountID {
			case tenantA:
				foundForA++
			case tenantB:
				foundForB++
			}
		}
		if foundForA != 1 {
			t.Fatalf("tenant A subscriber count: got %d, want 1 (seeded row only)", foundForA)
		}
		if foundForB != 1 {
			t.Fatalf("tenant B subscriber count: got %d, want 1 (imported row only)", foundForB)
		}
	})

	// --- positive control: tenant A can still see its own row ---
	t.Run("tenant A still sees own subscriber", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, subPath, nil)
		req.Header.Set("X-Test-Tenant", tenantA.String())
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("tenant A own read: got %d, want 200\nbody=%s", rr.Code, rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), "Tenant-A Subscriber") {
			t.Fatalf("expected tenant A's subscriber in body, got: %s", rr.Body.String())
		}
	})

}

func extractErrorCode(body []byte) string {
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return ""
	}
	return env.Error.Code
}

func containsInt(xs []int, x int) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func containsStr(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
