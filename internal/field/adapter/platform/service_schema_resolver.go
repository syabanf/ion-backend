// Package platform adapts the platform's service-schema resolver to
// field's ServiceSchemaResolver port (Wave 84b).
//
// The customer's pinned service-schema body is fetched via the
// existing platform.Resolver. We extract the "checklist_items" array
// (v1 PROVISIONAL shape — see CreateDerivedTemplateInput in
// internal/field/port for the contract) and translate to the
// field-side DerivedChecklistItem shape.
//
// When PRD signs off on a different JSON shape, only this translator
// changes — the materializer in the postgres adapter consumes
// field-internal types regardless.
package platform

import (
	"context"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/field/port"
	platformusecase "github.com/ion-core/backend/internal/platform/usecase"
)

type ServiceSchemaResolver struct {
	resolver *platformusecase.Resolver
}

func NewServiceSchemaResolver(r *platformusecase.Resolver) *ServiceSchemaResolver {
	return &ServiceSchemaResolver{resolver: r}
}

var _ port.ServiceSchemaResolver = (*ServiceSchemaResolver)(nil)

// ResolveChecklistForCustomer reads the service-kind schema for the
// customer, extracts the checklist_items array, and translates each
// entry to the field-internal DerivedChecklistItem shape.
//
// Returns (nil, nil, nil) for:
//   - no resolver wired (defensive)
//   - no published service schema for this customer (legacy product)
//   - schema body has no "checklist_items" key
//   - "checklist_items" exists but is empty
//
// The second return is the schema_definitions.id of the resolved
// schema — the materializer stamps it on the derived template's
// `derived_from_schema_id` column so we can trace items back to
// their source schema.
//
// We always return nil for the schema_id today because the platform
// Resolver doesn't expose it via the simple Resolve() entrypoint —
// upgrading to ResolveSchemaForCustomerWith would surface it, but
// that requires re-plumbing the resolver type. Tracked separately.
func (g *ServiceSchemaResolver) ResolveChecklistForCustomer(
	ctx context.Context, customerID uuid.UUID,
) ([]port.DerivedChecklistItem, *uuid.UUID, error) {
	if g.resolver == nil {
		return nil, nil, nil
	}
	body, err := g.resolver.Resolve(ctx, customerID, "service")
	if err != nil {
		// No schema published is a legitimate non-error case at this
		// boundary — the materializer falls through to legacy templates.
		// We could distinguish from infra errors via derrors.IsNotFound,
		// but the caller treats both as "skip materialization" so the
		// extra branching adds no value.
		return nil, nil, nil
	}
	raw, ok := body["checklist_items"].([]any)
	if !ok || len(raw) == 0 {
		return nil, nil, nil
	}
	items := make([]port.DerivedChecklistItem, 0, len(raw))
	for i, entry := range raw {
		obj, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		items = append(items, port.DerivedChecklistItem{
			ItemOrder:         readIntDefault(obj, "item_order", i+1),
			ItemType:          readStringDefault(obj, "item_type", "photo"),
			Label:             readStringDefault(obj, "label", ""),
			Required:          readBoolDefault(obj, "required", true),
			PhotoTag:          readStringDefault(obj, "photo_tag", ""),
			GPSRequired:       readBoolDefault(obj, "gps_required", false),
			MinAccuracyMeters: readIntPtr(obj, "min_accuracy_meters"),
		})
	}
	if len(items) == 0 {
		return nil, nil, nil
	}
	return items, nil, nil
}

// readStringDefault returns the value at key from body as a string,
// or fallback if the key is missing / not a string.
func readStringDefault(body map[string]any, key, fallback string) string {
	if v, ok := body[key].(string); ok {
		return v
	}
	return fallback
}

// readIntDefault — JSON numbers land as float64 by default.
func readIntDefault(body map[string]any, key string, fallback int) int {
	switch n := body[key].(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return fallback
}

func readBoolDefault(body map[string]any, key string, fallback bool) bool {
	if v, ok := body[key].(bool); ok {
		return v
	}
	return fallback
}

// readIntPtr returns nil when the key is missing or not numeric — the
// DB column min_accuracy_meters is nullable, so we want to preserve
// "unspecified" semantics rather than defaulting to 0.
func readIntPtr(body map[string]any, key string) *int {
	switch n := body[key].(type) {
	case float64:
		v := int(n)
		return &v
	case int:
		v := n
		return &v
	}
	return nil
}
