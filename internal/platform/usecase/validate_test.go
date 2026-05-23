// Wave 116 — Use-case-level validation tests.
//
// Covers the happy + sad path of ValidateSchemaContent, the publish
// gate (PublishSchemaWithValidation rejects when errors), and the
// nightly-sweep aggregate. Uses in-memory stubs only.

package usecase

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/platform/domain"
	"github.com/ion-core/backend/internal/platform/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// stubValidationResults — minimal port.ValidationResultRepository for
// the use-case tests. Keeps inserts in an in-memory slice so the test
// can assert "was a row written" / "was triggered_by what we expected".
type stubValidationResults struct {
	rows []port.ValidationResultRow
}

func (s *stubValidationResults) Insert(_ context.Context, row *port.ValidationResultRow) error {
	if row == nil {
		return derrors.Validation("validation.row_required", "row required")
	}
	cp := *row
	if cp.ValidatedAt.IsZero() {
		cp.ValidatedAt = time.Now().UTC()
	}
	s.rows = append(s.rows, cp)
	return nil
}

func (s *stubValidationResults) LatestForSchema(_ context.Context, schemaVersionID uuid.UUID) (*port.ValidationResultRow, error) {
	for i := len(s.rows) - 1; i >= 0; i-- {
		if s.rows[i].SchemaVersionID == schemaVersionID {
			cp := s.rows[i]
			return &cp, nil
		}
	}
	return nil, derrors.NotFound("validation.not_found", "no validation result")
}

// schemaUpdates lets the stub schema repo accept updates so the publish
// path can flip status. The existing stubSchemaRepo's Update is a noop;
// we wrap it with a closure-friendly variant here.
type stubSchemaRepoUpdatable struct {
	stubSchemaRepo
	updates []uuid.UUID
}

func (s *stubSchemaRepoUpdatable) Update(_ context.Context, def *domain.SchemaDefinition) error {
	s.byID[def.ID] = def
	s.updates = append(s.updates, def.ID)
	return nil
}

func newValidUsecaseService() (*Service, *stubSchemaRepoUpdatable, *stubValidationResults) {
	sr := &stubSchemaRepoUpdatable{
		stubSchemaRepo: stubSchemaRepo{
			byID:         map[uuid.UUID]*domain.SchemaDefinition{},
			latestByCode: map[string]*domain.SchemaDefinition{},
		},
	}
	or := &stubOverrideRepo{byCustomerKind: map[string]*domain.CustomerSchemaOverride{}}
	vr := &stubValidationResults{}
	svc := NewService(sr, or).
		WithValidatorRegistry(domain.NewValidatorRegistry(), vr)
	return svc, sr, vr
}

// helper — install a valid billing schema (passes validator).
func installValidBillingSchema(t *testing.T, sr *stubSchemaRepoUpdatable, status domain.SchemaStatus) *domain.SchemaDefinition {
	t.Helper()
	body := json.RawMessage(`{
		"cycle_day": 1,
		"currency": "IDR",
		"prorate_policy": "full_period",
		"defer_policy": "first_invoice",
		"tax_mode": "exclusive",
		"tax_pct": 0.11
	}`)
	def := &domain.SchemaDefinition{
		ID:        uuid.New(),
		Kind:      domain.SchemaKindBilling,
		Code:      "TEST_BILLING",
		VersionNo: 1,
		Name:      "Test Billing",
		Body:      body,
		Status:    status,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	sr.byID[def.ID] = def
	return def
}

// helper — install an INVALID billing schema (validator returns errors).
func installInvalidBillingSchema(t *testing.T, sr *stubSchemaRepoUpdatable, status domain.SchemaStatus) *domain.SchemaDefinition {
	t.Helper()
	body := json.RawMessage(`{
		"cycle_day": 31,
		"currency": "XYZ",
		"prorate_policy": "weekly",
		"defer_policy": "never",
		"tax_mode": "none",
		"tax_pct": 0
	}`)
	def := &domain.SchemaDefinition{
		ID:        uuid.New(),
		Kind:      domain.SchemaKindBilling,
		Code:      "TEST_BILLING_BAD",
		VersionNo: 1,
		Name:      "Test Billing (bad)",
		Body:      body,
		Status:    status,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	sr.byID[def.ID] = def
	return def
}

func TestValidateSchemaContent_Valid(t *testing.T) {
	svc, sr, vr := newValidUsecaseService()
	def := installValidBillingSchema(t, sr, domain.SchemaStatusDraft)
	res, err := svc.ValidateSchemaContent(context.Background(), def.ID)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !res.IsValid {
		t.Fatalf("expected valid, got errors=%v", res.Errors)
	}
	if len(vr.rows) != 1 {
		t.Fatalf("expected 1 row persisted, got %d", len(vr.rows))
	}
	if vr.rows[0].TriggeredBy != "manual" {
		t.Errorf("triggered_by mismatch: %s", vr.rows[0].TriggeredBy)
	}
}

func TestValidateSchemaContent_Invalid(t *testing.T) {
	svc, sr, vr := newValidUsecaseService()
	def := installInvalidBillingSchema(t, sr, domain.SchemaStatusDraft)
	res, err := svc.ValidateSchemaContent(context.Background(), def.ID)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if res.IsValid {
		t.Fatal("expected invalid")
	}
	if len(res.Errors) == 0 {
		t.Fatal("expected error list")
	}
	if !vr.rows[0].IsValid && len(vr.rows) != 1 {
		t.Fatal("expected 1 persisted row with is_valid=false")
	}
}

func TestPublishSchemaWithValidation_BlocksOnError(t *testing.T) {
	svc, sr, _ := newValidUsecaseService()
	def := installInvalidBillingSchema(t, sr, domain.SchemaStatusDraft)
	out, err := svc.PublishSchemaWithValidation(context.Background(), def.ID)
	if err == nil {
		t.Fatal("expected validation error blocking publish")
	}
	if out != nil {
		t.Fatal("expected nil schema on error")
	}
	if !derrors.IsConflict(err) && derrors.KindOf(err) != derrors.KindValidation {
		t.Fatalf("expected validation/conflict kind, got %v", derrors.KindOf(err))
	}
	// Ensure schema wasn't flipped.
	if def.Status != domain.SchemaStatusDraft {
		t.Errorf("schema status should remain draft after gate rejection, got %s", def.Status)
	}
}

func TestPublishSchemaWithValidation_PassesValid(t *testing.T) {
	svc, sr, vr := newValidUsecaseService()
	def := installValidBillingSchema(t, sr, domain.SchemaStatusDraft)
	out, err := svc.PublishSchemaWithValidation(context.Background(), def.ID)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if out == nil {
		t.Fatal("expected schema returned")
	}
	if out.Status != domain.SchemaStatusPublished {
		t.Errorf("expected published, got %s", out.Status)
	}
	// Should have a validation row from the publish gate.
	foundPublishGate := false
	for _, r := range vr.rows {
		if r.TriggeredBy == "publish_gate" {
			foundPublishGate = true
		}
	}
	if !foundPublishGate {
		t.Error("expected a publish_gate audit row")
	}
}

func TestLatestValidation_NotFound(t *testing.T) {
	svc, sr, _ := newValidUsecaseService()
	def := installValidBillingSchema(t, sr, domain.SchemaStatusDraft)
	_, err := svc.LatestValidation(context.Background(), def.ID)
	if err == nil || !derrors.IsNotFound(err) {
		t.Fatalf("expected NotFound for un-validated schema, got %v", err)
	}
}

func TestValidateAllPublishedSchemas_CountsInvalid(t *testing.T) {
	svc, sr, _ := newValidUsecaseService()

	// One valid + one invalid, both published. List() in stub returns
	// nothing, so override its behavior:
	good := installValidBillingSchema(t, sr, domain.SchemaStatusPublished)
	bad := installInvalidBillingSchema(t, sr, domain.SchemaStatusPublished)

	// Inject a List implementation by wrapping in a small repo wrapper.
	wrappedSchemas := &listProvidingSchemaRepo{
		stubSchemaRepoUpdatable: sr,
		published: []domain.SchemaDefinition{*good, *bad},
	}
	svc.schemas = wrappedSchemas

	invalid, total, err := svc.ValidateAllPublishedSchemas(context.Background())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if total != 2 {
		t.Errorf("expected total=2, got %d", total)
	}
	if invalid != 1 {
		t.Errorf("expected invalid=1, got %d", invalid)
	}
}

// listProvidingSchemaRepo wraps stubSchemaRepoUpdatable to supply a
// real List response so ValidateAllPublishedSchemas has something to
// iterate. Tests that need the sweep use this; the resolver tests use
// the original stub.
type listProvidingSchemaRepo struct {
	*stubSchemaRepoUpdatable
	published []domain.SchemaDefinition
}

func (l *listProvidingSchemaRepo) List(_ context.Context, f port.SchemaListFilter) ([]domain.SchemaDefinition, int, error) {
	if f.Status != string(domain.SchemaStatusPublished) {
		return nil, 0, nil
	}
	out := []domain.SchemaDefinition{}
	for _, s := range l.published {
		if f.Kind != "" && s.Kind != f.Kind {
			continue
		}
		out = append(out, s)
	}
	return out, len(out), nil
}

func TestListActiveByKind_FiltersOnLatestValidation(t *testing.T) {
	svc, sr, vr := newValidUsecaseService()
	good := installValidBillingSchema(t, sr, domain.SchemaStatusPublished)
	bad := installInvalidBillingSchema(t, sr, domain.SchemaStatusPublished)
	// Insert a passing validation row for `good` and a failing row for `bad`.
	_ = vr.Insert(context.Background(), &port.ValidationResultRow{
		SchemaVersionID:  good.ID,
		IsValid:          true,
		ValidatorVersion: "v1.0",
		TriggeredBy:      "manual",
	})
	_ = vr.Insert(context.Background(), &port.ValidationResultRow{
		SchemaVersionID:  bad.ID,
		IsValid:          false,
		Errors:           []string{"cycle_day_out_of_range"},
		ValidatorVersion: "v1.0",
		TriggeredBy:      "manual",
	})
	svc.schemas = &listProvidingSchemaRepo{
		stubSchemaRepoUpdatable: sr,
		published:               []domain.SchemaDefinition{*good, *bad},
	}
	items, err := svc.ListActiveByKind(context.Background(), domain.SchemaKindBilling)
	if err != nil {
		t.Fatalf("list active: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 active schema (good), got %d", len(items))
	}
	if items[0].ID != good.ID {
		t.Errorf("wrong schema returned: %s", items[0].ID)
	}
}
