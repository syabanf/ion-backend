package usecase

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/platform/domain"
	"github.com/ion-core/backend/internal/platform/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// Wave 78 — resolver precedence tests. Lookup order:
//   1. customer override
//   2. customer locked version
//   3. product schema slot
//   4. global DEFAULT
//
// Each test pins one tier of the contract by stubbing the repos
// and asserting the resolver picked the right schema_id.

type stubSchemaRepo struct {
	byID         map[uuid.UUID]*domain.SchemaDefinition
	latestByCode map[string]*domain.SchemaDefinition // key = string(kind)+"|"+code
}

func (s *stubSchemaRepo) Create(_ context.Context, _ *domain.SchemaDefinition) error {
	return nil
}
func (s *stubSchemaRepo) Update(_ context.Context, _ *domain.SchemaDefinition) error {
	return nil
}
func (s *stubSchemaRepo) FindByID(_ context.Context, id uuid.UUID) (*domain.SchemaDefinition, error) {
	if v, ok := s.byID[id]; ok {
		return v, nil
	}
	return nil, derrors.NotFound("schema.not_found", "not found")
}
func (s *stubSchemaRepo) FindLatestPublished(_ context.Context, kind domain.SchemaKind, code string) (*domain.SchemaDefinition, error) {
	if v, ok := s.latestByCode[string(kind)+"|"+code]; ok {
		return v, nil
	}
	return nil, derrors.NotFound("schema.not_found", "not found")
}
func (s *stubSchemaRepo) MaxVersion(_ context.Context, _ domain.SchemaKind, _ string) (int, error) {
	return 0, nil
}
func (s *stubSchemaRepo) List(_ context.Context, _ port.SchemaListFilter) ([]domain.SchemaDefinition, int, error) {
	return nil, 0, nil
}

type stubOverrideRepo struct {
	byCustomerKind map[string]*domain.CustomerSchemaOverride // key = customerID + "|" + string(kind)
}

func (s *stubOverrideRepo) Upsert(_ context.Context, _ *domain.CustomerSchemaOverride) error {
	return nil
}
func (s *stubOverrideRepo) FindByCustomerAndKind(_ context.Context, customerID uuid.UUID, kind domain.SchemaKind) (*domain.CustomerSchemaOverride, error) {
	if v, ok := s.byCustomerKind[customerID.String()+"|"+string(kind)]; ok {
		return v, nil
	}
	return nil, derrors.NotFound("override.not_found", "not found")
}
func (s *stubOverrideRepo) ListByCustomer(_ context.Context, _ uuid.UUID) ([]domain.CustomerSchemaOverride, error) {
	return nil, nil
}
func (s *stubOverrideRepo) Delete(_ context.Context, _ uuid.UUID, _ domain.SchemaKind) error {
	return nil
}

// buildSchema constructs a valid SchemaDefinition for the stubs. body
// is a minimal JSON blob so domain.ResolveForCustomer doesn't panic
// on nil patching.
func buildSchema(id uuid.UUID, kind domain.SchemaKind, code string) *domain.SchemaDefinition {
	return &domain.SchemaDefinition{
		ID:     id,
		Kind:   kind,
		Code:   code,
		Status: domain.SchemaStatusPublished,
		Body:   json.RawMessage(`{"k":"` + code + `"}`),
	}
}

func TestResolver_Tier1_OverridePinnedSchema(t *testing.T) {
	// Setup: customer has an override pinned to a specific schema id.
	cust := uuid.New()
	overrideID := uuid.New()
	defaultID := uuid.New()
	productID := uuid.New()
	lockedID := uuid.New()

	schemas := &stubSchemaRepo{
		byID: map[uuid.UUID]*domain.SchemaDefinition{
			overrideID: buildSchema(overrideID, domain.SchemaKindBilling, "OVERRIDE"),
			defaultID:  buildSchema(defaultID, domain.SchemaKindBilling, "DEFAULT"),
			productID:  buildSchema(productID, domain.SchemaKindBilling, "PRODUCT"),
			lockedID:   buildSchema(lockedID, domain.SchemaKindBilling, "LOCKED"),
		},
		latestByCode: map[string]*domain.SchemaDefinition{
			"billing|DEFAULT": buildSchema(defaultID, domain.SchemaKindBilling, "DEFAULT"),
		},
	}
	pinned := overrideID
	overrides := &stubOverrideRepo{
		byCustomerKind: map[string]*domain.CustomerSchemaOverride{
			cust.String() + "|billing": {
				CustomerID: cust,
				SchemaKind: domain.SchemaKindBilling,
				SchemaID:   &pinned,
			},
		},
	}
	svc := NewService(schemas, overrides)

	_, picked, _, err := svc.ResolveSchemaForCustomerWith(
		context.Background(), cust, domain.SchemaKindBilling,
		ResolveOptions{LockedVersionID: &lockedID, ProductSchemaSlotID: &productID},
	)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if picked == nil || picked.ID != overrideID {
		t.Fatalf("expected override winner, got %v", picked)
	}
}

func TestResolver_Tier2_LockedVersionBeatsProductAndDefault(t *testing.T) {
	cust := uuid.New()
	defaultID := uuid.New()
	productID := uuid.New()
	lockedID := uuid.New()

	schemas := &stubSchemaRepo{
		byID: map[uuid.UUID]*domain.SchemaDefinition{
			defaultID: buildSchema(defaultID, domain.SchemaKindService, "DEFAULT"),
			productID: buildSchema(productID, domain.SchemaKindService, "PRODUCT"),
			lockedID:  buildSchema(lockedID, domain.SchemaKindService, "LOCKED"),
		},
		latestByCode: map[string]*domain.SchemaDefinition{
			"service|DEFAULT": buildSchema(defaultID, domain.SchemaKindService, "DEFAULT"),
		},
	}
	overrides := &stubOverrideRepo{byCustomerKind: map[string]*domain.CustomerSchemaOverride{}}
	svc := NewService(schemas, overrides)

	// QA TC-SCH-011: pinned customer stays on locked version even
	// when newer schemas exist.
	_, picked, _, err := svc.ResolveSchemaForCustomerWith(
		context.Background(), cust, domain.SchemaKindService,
		ResolveOptions{LockedVersionID: &lockedID, ProductSchemaSlotID: &productID},
	)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if picked == nil || picked.ID != lockedID {
		t.Fatalf("expected locked winner, got %v", picked)
	}
}

func TestResolver_Tier3_ProductSlotBeatsDefault(t *testing.T) {
	cust := uuid.New()
	defaultID := uuid.New()
	productID := uuid.New()

	schemas := &stubSchemaRepo{
		byID: map[uuid.UUID]*domain.SchemaDefinition{
			defaultID: buildSchema(defaultID, domain.SchemaKindSuspension, "DEFAULT"),
			productID: buildSchema(productID, domain.SchemaKindSuspension, "PRODUCT"),
		},
		latestByCode: map[string]*domain.SchemaDefinition{
			"suspension|DEFAULT": buildSchema(defaultID, domain.SchemaKindSuspension, "DEFAULT"),
		},
	}
	overrides := &stubOverrideRepo{byCustomerKind: map[string]*domain.CustomerSchemaOverride{}}
	svc := NewService(schemas, overrides)

	_, picked, _, err := svc.ResolveSchemaForCustomerWith(
		context.Background(), cust, domain.SchemaKindSuspension,
		ResolveOptions{ProductSchemaSlotID: &productID},
	)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if picked == nil || picked.ID != productID {
		t.Fatalf("expected product slot winner, got %v", picked)
	}
}

func TestResolver_Tier4_DefaultWhenNothingElse(t *testing.T) {
	cust := uuid.New()
	defaultID := uuid.New()

	schemas := &stubSchemaRepo{
		byID: map[uuid.UUID]*domain.SchemaDefinition{
			defaultID: buildSchema(defaultID, domain.SchemaKindCommission, "DEFAULT"),
		},
		latestByCode: map[string]*domain.SchemaDefinition{
			"commission|DEFAULT": buildSchema(defaultID, domain.SchemaKindCommission, "DEFAULT"),
		},
	}
	overrides := &stubOverrideRepo{byCustomerKind: map[string]*domain.CustomerSchemaOverride{}}
	svc := NewService(schemas, overrides)

	_, picked, _, err := svc.ResolveSchemaForCustomerWith(
		context.Background(), cust, domain.SchemaKindCommission, ResolveOptions{},
	)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if picked == nil || picked.ID != defaultID {
		t.Fatalf("expected DEFAULT winner, got %v", picked)
	}
}

func TestResolver_LockedVersionMissing_FallsThroughToProduct(t *testing.T) {
	// Locked ID points to a deleted row → resolver gracefully falls
	// through. Misconfigured customer doesn't brick the dunning tick.
	cust := uuid.New()
	productID := uuid.New()

	schemas := &stubSchemaRepo{
		byID: map[uuid.UUID]*domain.SchemaDefinition{
			productID: buildSchema(productID, domain.SchemaKindBilling, "PRODUCT"),
		},
		latestByCode: map[string]*domain.SchemaDefinition{
			"billing|DEFAULT": buildSchema(uuid.New(), domain.SchemaKindBilling, "DEFAULT"),
		},
	}
	overrides := &stubOverrideRepo{byCustomerKind: map[string]*domain.CustomerSchemaOverride{}}
	svc := NewService(schemas, overrides)

	deletedLocked := uuid.New() // not in stub
	_, picked, _, err := svc.ResolveSchemaForCustomerWith(
		context.Background(), cust, domain.SchemaKindBilling,
		ResolveOptions{LockedVersionID: &deletedLocked, ProductSchemaSlotID: &productID},
	)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if picked == nil || picked.ID != productID {
		t.Fatalf("expected fallthrough to product, got %v", picked)
	}
}

func TestResolver_LegacyContract_StillWorksWithoutOptions(t *testing.T) {
	// Wave 78 didn't break pre-Wave-78 contract: ResolveSchemaForCustomer
	// (no options) routes through ResolveSchemaForCustomerWith with
	// empty ResolveOptions. Tier 4 (DEFAULT) wins.
	cust := uuid.New()
	defaultID := uuid.New()
	schemas := &stubSchemaRepo{
		byID: map[uuid.UUID]*domain.SchemaDefinition{},
		latestByCode: map[string]*domain.SchemaDefinition{
			"service|DEFAULT": buildSchema(defaultID, domain.SchemaKindService, "DEFAULT"),
		},
	}
	overrides := &stubOverrideRepo{byCustomerKind: map[string]*domain.CustomerSchemaOverride{}}
	svc := NewService(schemas, overrides)
	_, picked, _, err := svc.ResolveSchemaForCustomer(context.Background(), cust, domain.SchemaKindService)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if picked == nil || picked.ID != defaultID {
		t.Fatalf("expected DEFAULT, got %v", picked)
	}
}
