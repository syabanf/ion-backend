// Wave 115 — AddOnService usecase tests.
//
// Stub repo + catalog reader + CRM gateway exercise the purchase →
// list → cancel happy path plus the cross-customer scope guard.

package usecase

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/billing/domain"
	"github.com/ion-core/backend/internal/billing/port"
)

// =====================================================================
// Stubs
// =====================================================================

type stubAddOnRepo struct {
	rows map[uuid.UUID]domain.AddOnPurchase
}

func newStubAddOnRepo() *stubAddOnRepo {
	return &stubAddOnRepo{rows: map[uuid.UUID]domain.AddOnPurchase{}}
}

func (s *stubAddOnRepo) Create(_ context.Context, p *domain.AddOnPurchase) error {
	s.rows[p.ID] = *p
	return nil
}
func (s *stubAddOnRepo) Update(_ context.Context, p *domain.AddOnPurchase) error {
	s.rows[p.ID] = *p
	return nil
}
func (s *stubAddOnRepo) FindByID(_ context.Context, id uuid.UUID) (*domain.AddOnPurchase, error) {
	if r, ok := s.rows[id]; ok {
		return &r, nil
	}
	return nil, nil
}
func (s *stubAddOnRepo) ListByCustomer(_ context.Context, customerID uuid.UUID, statuses []string) ([]domain.AddOnPurchase, error) {
	out := []domain.AddOnPurchase{}
	statusSet := map[string]struct{}{}
	for _, s := range statuses {
		statusSet[s] = struct{}{}
	}
	for _, r := range s.rows {
		if r.CustomerID != customerID {
			continue
		}
		if len(statusSet) > 0 {
			if _, ok := statusSet[string(r.Status)]; !ok {
				continue
			}
		}
		out = append(out, r)
	}
	return out, nil
}
func (s *stubAddOnRepo) ListExpiring(_ context.Context, before time.Time, _ int) ([]domain.AddOnPurchase, error) {
	out := []domain.AddOnPurchase{}
	for _, r := range s.rows {
		if r.Status != domain.AddOnStatusActive {
			continue
		}
		if r.ValidUntil != nil && !r.ValidUntil.After(before) {
			out = append(out, r)
		}
	}
	return out, nil
}

type stubCatalogReader struct {
	items map[string]port.CatalogItem
}

func newStubCatalogReader() *stubCatalogReader {
	return &stubCatalogReader{items: map[string]port.CatalogItem{}}
}

func (s *stubCatalogReader) ListActive(_ context.Context) ([]port.CatalogItem, error) {
	out := []port.CatalogItem{}
	for _, v := range s.items {
		if v.Active {
			out = append(out, v)
		}
	}
	return out, nil
}
func (s *stubCatalogReader) FindBySKU(_ context.Context, sku string) (*port.CatalogItem, error) {
	if v, ok := s.items[sku]; ok {
		return &v, nil
	}
	return nil, nil
}

type stubCRMSync struct {
	upserts int
	cancels int
}

func (s *stubCRMSync) UpsertCustomerAddon(_ context.Context, _, _ uuid.UUID, _ int, _, _ float64, _ string) error {
	s.upserts++
	return nil
}
func (s *stubCRMSync) MarkCancelled(_ context.Context, _ uuid.UUID, _ string) error {
	s.cancels++
	return nil
}

// =====================================================================
// Tests
// =====================================================================

func TestAddOnService_Purchase_DigitalGoesActive(t *testing.T) {
	repo := newStubAddOnRepo()
	cat := newStubCatalogReader()
	cat.items["speed_50"] = port.CatalogItem{
		ID:         uuid.New(),
		SKU:        "speed_50",
		Name:       "Speed Boost 50",
		Category:   domain.AddOnCategoryDigital,
		MonthlyFee: 50000,
		Active:     true,
	}
	crm := &stubCRMSync{}
	svc := NewAddOnService(repo, cat, crm)
	cust := uuid.New()
	p, err := svc.Purchase(context.Background(), port.PurchaseInput{
		CustomerID: cust,
		SKU:        "speed_50",
		Quantity:   1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != domain.AddOnStatusActive {
		t.Errorf("digital addon should be active, got %s", p.Status)
	}
	if crm.upserts != 1 {
		t.Errorf("expected CRM upsert call, got %d", crm.upserts)
	}
}

func TestAddOnService_Purchase_PhysicalPendingInstall(t *testing.T) {
	repo := newStubAddOnRepo()
	cat := newStubCatalogReader()
	cat.items["router"] = port.CatalogItem{
		ID:              uuid.New(),
		SKU:             "router",
		Name:            "Router",
		Category:        domain.AddOnCategoryPhysical,
		MonthlyFee:      0,
		RequiresInstall: true,
		Active:          true,
	}
	svc := NewAddOnService(repo, cat, nil)
	p, err := svc.Purchase(context.Background(), port.PurchaseInput{
		CustomerID: uuid.New(),
		SKU:        "router",
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != domain.AddOnStatusPendingInstall {
		t.Errorf("physical addon should be pending_install, got %s", p.Status)
	}
}

func TestAddOnService_Purchase_RequiresActiveCatalogItem(t *testing.T) {
	repo := newStubAddOnRepo()
	cat := newStubCatalogReader()
	cat.items["x"] = port.CatalogItem{SKU: "x", Active: false}
	svc := NewAddOnService(repo, cat, nil)
	_, err := svc.Purchase(context.Background(), port.PurchaseInput{
		CustomerID: uuid.New(),
		SKU:        "x",
	})
	if err == nil {
		t.Fatal("expected validation error for inactive sku")
	}
}

func TestAddOnService_Purchase_NotFoundForMissingSKU(t *testing.T) {
	svc := NewAddOnService(newStubAddOnRepo(), newStubCatalogReader(), nil)
	_, err := svc.Purchase(context.Background(), port.PurchaseInput{
		CustomerID: uuid.New(),
		SKU:        "ghost",
	})
	if err == nil {
		t.Fatal("expected not-found for missing sku")
	}
}

func TestAddOnService_ListActive(t *testing.T) {
	repo := newStubAddOnRepo()
	cat := newStubCatalogReader()
	cust := uuid.New()
	cat.items["a"] = port.CatalogItem{SKU: "a", Category: domain.AddOnCategoryDigital, Active: true}
	svc := NewAddOnService(repo, cat, nil)
	_, _ = svc.Purchase(context.Background(), port.PurchaseInput{CustomerID: cust, SKU: "a"})
	rows, err := svc.ListActive(context.Background(), cust)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Errorf("expected 1 active addon, got %d", len(rows))
	}
}

func TestAddOnService_Cancel_RejectsCrossCustomer(t *testing.T) {
	repo := newStubAddOnRepo()
	cat := newStubCatalogReader()
	cat.items["a"] = port.CatalogItem{SKU: "a", Category: domain.AddOnCategoryDigital, Active: true}
	svc := NewAddOnService(repo, cat, nil)
	custA := uuid.New()
	custB := uuid.New()
	p, _ := svc.Purchase(context.Background(), port.PurchaseInput{CustomerID: custA, SKU: "a"})
	if _, err := svc.Cancel(context.Background(), custB, p.ID, "stranger"); err == nil {
		t.Fatal("expected not-found across customers")
	}
}

func TestAddOnService_Cancel_HappyPath(t *testing.T) {
	repo := newStubAddOnRepo()
	cat := newStubCatalogReader()
	cat.items["a"] = port.CatalogItem{SKU: "a", Category: domain.AddOnCategoryDigital, Active: true}
	svc := NewAddOnService(repo, cat, nil)
	cust := uuid.New()
	p, _ := svc.Purchase(context.Background(), port.PurchaseInput{CustomerID: cust, SKU: "a"})
	out, err := svc.Cancel(context.Background(), cust, p.ID, "changed mind")
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != domain.AddOnStatusCancelled {
		t.Errorf("expected cancelled, got %s", out.Status)
	}
}

func TestAddOnService_ListAvailable_FiltersInactive(t *testing.T) {
	cat := newStubCatalogReader()
	cat.items["on"] = port.CatalogItem{SKU: "on", Active: true}
	cat.items["off"] = port.CatalogItem{SKU: "off", Active: false}
	svc := NewAddOnService(newStubAddOnRepo(), cat, nil)
	items, err := svc.ListAvailable(context.Background(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Errorf("expected 1 active catalog item, got %d", len(items))
	}
}
