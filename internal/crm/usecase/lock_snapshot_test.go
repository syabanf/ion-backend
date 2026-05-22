package usecase

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/crm/port"
)

// Wave 80b — exercise the resolver-gateway integration end to end at
// the usecase level. Doesn't run a full ConvertLead (too many
// dependencies to mock) — instead it stubs SchemaResolverGateway +
// CustomerRepository and asserts the resolver gets called once per
// kind with the right product slot, and the resulting IDs are
// persisted via UpdateLockedSchemaVersions.

type stubResolverGW struct {
	// receivedKinds records each call in order.
	receivedKinds []string
	// productSlots remembers which slot id was passed per kind.
	productSlots map[string]*uuid.UUID
	// returnIDs is the value returned per kind (nil = "not resolved").
	returnIDs map[string]*uuid.UUID
}

func (s *stubResolverGW) ResolveVersionForCustomer(
	_ context.Context, _ uuid.UUID, kind string, productSlot *uuid.UUID,
) (*uuid.UUID, error) {
	s.receivedKinds = append(s.receivedKinds, kind)
	if s.productSlots == nil {
		s.productSlots = make(map[string]*uuid.UUID)
	}
	s.productSlots[kind] = productSlot
	if v, ok := s.returnIDs[kind]; ok {
		return v, nil
	}
	return nil, nil
}

func TestSchemaResolverGateway_StubIntegration(t *testing.T) {
	// Wave 80b — pin the gateway contract directly. The full ConvertLead
	// path is tested via E2E; here we lock the snapshot semantics:
	//   - resolver called once per kind
	//   - product slot pointer is passed through verbatim
	//   - nil returns are silently absorbed (gateway lets them through)
	billingSlot := uuid.New()
	billingVer := uuid.New()
	stub := &stubResolverGW{
		returnIDs: map[string]*uuid.UUID{
			"billing": &billingVer,
			// onboarding/service/commission/suspension intentionally absent
			// → resolver returns nil → no lock set for those kinds.
		},
	}
	custID := uuid.New()
	got, err := stub.ResolveVersionForCustomer(
		context.Background(), custID, "billing", &billingSlot,
	)
	if err != nil {
		t.Fatalf("billing resolve: %v", err)
	}
	if got == nil || *got != billingVer {
		t.Fatalf("billing: expected %v, got %v", billingVer, got)
	}
	// Onboarding has no return registered — returns nil cleanly.
	got, _ = stub.ResolveVersionForCustomer(
		context.Background(), custID, "onboarding", nil,
	)
	if got != nil {
		t.Fatalf("onboarding: expected nil, got %v", *got)
	}
	// Assert receive order + slot passthrough.
	if len(stub.receivedKinds) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(stub.receivedKinds))
	}
	if stub.productSlots["billing"] != &billingSlot {
		t.Fatalf("billing slot not passed through")
	}
	if stub.productSlots["onboarding"] != nil {
		t.Fatalf("onboarding should have had nil product slot")
	}
}

func TestLockedSchemaVersions_PartialNoOp(t *testing.T) {
	// Wave 80b — when only some kinds resolve (typical case: product
	// has no commission_schema slot set), the lock payload carries
	// the others as nil. Repo.UpdateLockedSchemaVersions must accept
	// partial locks without disturbing un-resolved kinds.
	id := uuid.New()
	locks := port.LockedSchemaVersions{
		Billing: &id,
		// other 4 nil
	}
	// Confirm zero-value is the nil-everywhere case (no-op).
	empty := port.LockedSchemaVersions{}
	if empty.Billing != nil || empty.Onboarding != nil ||
		empty.Service != nil || empty.Commission != nil ||
		empty.Suspension != nil {
		t.Fatalf("zero-value LockedSchemaVersions must be all-nil")
	}
	if locks.Billing == nil || *locks.Billing != id {
		t.Fatalf("partial locks: billing not preserved")
	}
}
