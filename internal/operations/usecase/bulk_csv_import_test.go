package usecase

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// stubLookup implements port.CSVLookupPort.
type stubLookup struct {
	customers map[string]uuid.UUID
	plans     map[string]uuid.UUID
	ports     map[string]uuid.UUID
	templates map[string]uuid.UUID
}

func (s *stubLookup) CustomerIDByNumber(_ context.Context, no string) (*uuid.UUID, error) {
	if id, ok := s.customers[no]; ok {
		return &id, nil
	}
	return nil, nil
}
func (s *stubLookup) PlanIDByCode(_ context.Context, c string) (*uuid.UUID, error) {
	if id, ok := s.plans[c]; ok {
		return &id, nil
	}
	return nil, nil
}
func (s *stubLookup) PortIDByCode(_ context.Context, c string) (*uuid.UUID, error) {
	if id, ok := s.ports[c]; ok {
		return &id, nil
	}
	return nil, nil
}
func (s *stubLookup) WOTemplateIDByCode(_ context.Context, c string) (*uuid.UUID, error) {
	if id, ok := s.templates[c]; ok {
		return &id, nil
	}
	return nil, nil
}

func TestImportPlanChangeCSV_Happy(t *testing.T) {
	ctx := context.Background()
	jobs := newFakeJobRepo()
	items := newFakeBPC()
	lookup := &stubLookup{
		customers: map[string]uuid.UUID{"C0001": uuid.New(), "C0002": uuid.New()},
		plans:     map[string]uuid.UUID{"P-100M": uuid.New()},
	}
	im := NewBulkCSVImporter(jobs, items, nil, nil, lookup, nil)

	csv := `customer_no,target_plan_code,effective_at
C0001,P-100M,2026-06-01T00:00:00Z
C0002,P-100M,
`
	sum, err := im.ImportPlanChangeCSV(ctx, strings.NewReader(csv), false, nil)
	if err != nil {
		t.Fatalf("ImportPlanChangeCSV: %v", err)
	}
	if sum.Total != 2 || sum.OK != 2 {
		t.Errorf("want 2 ok / 2 total, got %d/%d", sum.OK, sum.Total)
	}
	if len(sum.Errors) != 0 {
		t.Errorf("want no errors, got %d", len(sum.Errors))
	}
}

func TestImportPlanChangeCSV_BadRows(t *testing.T) {
	ctx := context.Background()
	jobs := newFakeJobRepo()
	items := newFakeBPC()
	lookup := &stubLookup{
		customers: map[string]uuid.UUID{"C0001": uuid.New()},
		plans:     map[string]uuid.UUID{"P-100M": uuid.New()},
	}
	im := NewBulkCSVImporter(jobs, items, nil, nil, lookup, nil)

	csv := `customer_no,target_plan_code,effective_at
C0001,P-100M,2026-06-01T00:00:00Z
,P-100M,
C9999,P-100M,
C0001,P-NOPE,
C0001,P-100M,not-a-date
`
	sum, err := im.ImportPlanChangeCSV(ctx, strings.NewReader(csv), false, nil)
	if err != nil {
		t.Fatalf("ImportPlanChangeCSV: %v", err)
	}
	if sum.OK != 1 {
		t.Errorf("ok rows: want 1, got %d", sum.OK)
	}
	if len(sum.Errors) != 4 {
		t.Errorf("want 4 row errors, got %d: %+v", len(sum.Errors), sum.Errors)
	}
}

func TestImportODPMigrationCSV_Happy(t *testing.T) {
	ctx := context.Background()
	jobs := newFakeJobRepo()
	items := &fakeBOM{items: map[uuid.UUID]*fakeBOMItem{}}
	lookup := &stubLookup{
		customers: map[string]uuid.UUID{"C0001": uuid.New()},
		ports:     map[string]uuid.UUID{"OLT-JKT:5": uuid.New()},
	}
	im := NewBulkCSVImporter(jobs, nil, items, nil, lookup, nil)

	csv := `customer_no,to_olt_port_code,scheduled_window_start,scheduled_window_end
C0001,OLT-JKT:5,2026-06-01T00:00:00Z,2026-06-01T02:00:00Z
`
	sum, err := im.ImportODPMigrationCSV(ctx, strings.NewReader(csv), false, nil)
	if err != nil {
		t.Fatalf("ImportODPMigrationCSV: %v", err)
	}
	if sum.OK != 1 {
		t.Errorf("ok: want 1, got %d", sum.OK)
	}
}

func TestImportWOCreationCSV_Happy(t *testing.T) {
	ctx := context.Background()
	jobs := newFakeJobRepo()
	items := &fakeBWO{items: map[uuid.UUID]*fakeBWOItem{}}
	lookup := &stubLookup{
		customers: map[string]uuid.UUID{"C0001": uuid.New()},
		templates: map[string]uuid.UUID{"WO-MAINT": uuid.New()},
	}
	im := NewBulkCSVImporter(jobs, nil, nil, items, lookup, nil)

	csv := `customer_no,wo_template_code,wo_type,scheduled_at
C0001,WO-MAINT,maintenance,2026-06-01T08:00:00Z
C0001,,maintenance,
`
	sum, err := im.ImportWOCreationCSV(ctx, strings.NewReader(csv), false, nil)
	if err != nil {
		t.Fatalf("ImportWOCreationCSV: %v", err)
	}
	if sum.OK != 2 {
		t.Errorf("ok: want 2 (template code is optional), got %d / errors %+v", sum.OK, sum.Errors)
	}
}

func TestImportCSV_HeaderRequired(t *testing.T) {
	ctx := context.Background()
	jobs := newFakeJobRepo()
	items := newFakeBPC()
	im := NewBulkCSVImporter(jobs, items, nil, nil, &stubLookup{}, nil)

	// Empty input → header missing
	_, err := im.ImportPlanChangeCSV(ctx, strings.NewReader(""), false, nil)
	if err == nil {
		t.Fatalf("empty CSV should error on header")
	}
}
