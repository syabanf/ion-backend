// Package platform adapts the platform usecase to CRM's
// SchemaResolverGateway port (Wave 80b).
//
// At lead conversion, the CRM service calls this gateway 5 times (once
// per schema kind) to ask the platform's resolver "given this new
// customer and the product's schema slot for this kind, what specific
// schema version should I pin?". The returned ID is persisted onto
// crm.customers.locked_<kind>_schema_version_id so subsequent reads
// stay pinned to order-time behavior even after a newer version
// publishes (QA TC-SCH-011/015/023/026, TC-PRD-025).
//
// When platform moves to its own process, swap this in-process adapter
// for an HTTP client to /api/platform/schemas/resolve.
package platform

import (
	"context"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/crm/port"
	platformDomain "github.com/ion-core/backend/internal/platform/domain"
	platformUsecase "github.com/ion-core/backend/internal/platform/usecase"
)

type SchemaResolverGateway struct {
	platform *platformUsecase.Service
}

func NewSchemaResolverGateway(platform *platformUsecase.Service) *SchemaResolverGateway {
	return &SchemaResolverGateway{platform: platform}
}

var _ port.SchemaResolverGateway = (*SchemaResolverGateway)(nil)

// ResolveVersionForCustomer translates the CRM-side string kind into
// the platform's typed `domain.SchemaKind`, calls the new 4-tier
// resolver with the product slot context, and returns the resolved
// SchemaDefinition.ID. nil result means the kind didn't resolve to
// anything (e.g. no DEFAULT seeded for that kind yet) — caller falls
// through to legacy resolver path on subsequent reads.
func (g *SchemaResolverGateway) ResolveVersionForCustomer(
	ctx context.Context,
	customerID uuid.UUID,
	kind string,
	productSchemaSlotID *uuid.UUID,
) (*uuid.UUID, error) {
	platformKind, err := platformDomain.ParseSchemaKind(kind)
	if err != nil {
		// Onboarding lives in a separate table (crm.onboarding_schemas)
		// and isn't part of platform's 4-kind enum. Returning nil keeps
		// ConvertLead happy; the existing onboarding_schemas resolver
		// continues to drive lead-conversion doc checklists.
		return nil, nil
	}
	opts := platformUsecase.ResolveOptions{
		ProductSchemaSlotID: productSchemaSlotID,
	}
	_, picked, _, err := g.platform.ResolveSchemaForCustomerWith(
		ctx, customerID, platformKind, opts,
	)
	if err != nil || picked == nil {
		return nil, err
	}
	id := picked.ID
	return &id, nil
}
