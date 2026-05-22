// Package usecase implements the platform/schemas UseCase — wires the
// driving HTTP adapter through to the driven postgres repos.
//
// The service is intentionally thin: domain holds the lifecycle rules
// (Publish / Supersede / ResolveForCustomer), the postgres adapters
// handle CRUD, and this layer is the orchestration glue.
package usecase

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/platform/domain"
	"github.com/ion-core/backend/internal/platform/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// Service is the concrete SchemaUseCase.
type Service struct {
	schemas   port.SchemaRepository
	overrides port.OverrideRepository
}

func NewService(schemas port.SchemaRepository, overrides port.OverrideRepository) *Service {
	return &Service{schemas: schemas, overrides: overrides}
}

var _ port.SchemaUseCase = (*Service)(nil)

// =====================================================================
// Schemas
// =====================================================================

func (s *Service) ListSchemas(ctx context.Context, f port.SchemaListFilter) ([]domain.SchemaDefinition, int, error) {
	return s.schemas.List(ctx, f)
}

func (s *Service) GetSchema(ctx context.Context, id uuid.UUID) (*domain.SchemaDefinition, error) {
	return s.schemas.FindByID(ctx, id)
}

// CreateSchema starts a new draft. When the (kind, code) pair already
// exists, the new draft gets the next version_no — matches the
// "drafts iterate, published rows are immutable" idiom from
// enterprise.boq_versions.
func (s *Service) CreateSchema(ctx context.Context, in port.CreateSchemaInput) (*domain.SchemaDefinition, error) {
	def, err := domain.NewSchema(in.Kind, in.Code, in.Name, in.Body)
	if err != nil {
		return nil, err
	}
	def.Description = in.Description
	def.Notes = in.Notes
	def.CreatedBy = in.CreatedBy

	// Assign next version_no if a prior row for (kind, code) exists.
	maxV, err := s.schemas.MaxVersion(ctx, in.Kind, def.Code)
	if err != nil {
		return nil, err
	}
	if maxV > 0 {
		def.VersionNo = maxV + 1
	}

	if err := s.schemas.Create(ctx, def); err != nil {
		return nil, err
	}
	return def, nil
}

func (s *Service) UpdateDraftSchema(ctx context.Context, in port.UpdateSchemaDraftInput) (*domain.SchemaDefinition, error) {
	def, err := s.schemas.FindByID(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if err := def.UpdateDraft(in.Name, in.Description, in.Body, in.Notes); err != nil {
		return nil, err
	}
	if err := s.schemas.Update(ctx, def); err != nil {
		return nil, err
	}
	return def, nil
}

// PublishSchema flips the target draft to published. If a prior
// published version of the same (kind, code) exists, it is atomically
// flipped to superseded — guarantees FindLatestPublished returns at
// most one row per (kind, code).
func (s *Service) PublishSchema(ctx context.Context, id uuid.UUID) (*domain.SchemaDefinition, error) {
	def, err := s.schemas.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	// Look up the prior published peer (if any) BEFORE flipping the
	// target — we want to know whether to supersede it.
	priorRaw, err := s.schemas.FindLatestPublished(ctx, def.Kind, def.Code)
	if err != nil && !derrors.IsNotFound(err) {
		return nil, err
	}
	if err := def.Publish(); err != nil {
		return nil, err
	}
	if priorRaw != nil && priorRaw.ID != def.ID {
		if err := priorRaw.Supersede(); err != nil {
			return nil, err
		}
		if err := s.schemas.Update(ctx, priorRaw); err != nil {
			return nil, err
		}
	}
	if err := s.schemas.Update(ctx, def); err != nil {
		return nil, err
	}
	return def, nil
}

func (s *Service) SupersedeSchema(ctx context.Context, id uuid.UUID) (*domain.SchemaDefinition, error) {
	def, err := s.schemas.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := def.Supersede(); err != nil {
		return nil, err
	}
	if err := s.schemas.Update(ctx, def); err != nil {
		return nil, err
	}
	return def, nil
}

// =====================================================================
// Overrides
// =====================================================================

func (s *Service) ListOverridesForCustomer(ctx context.Context, customerID uuid.UUID) ([]domain.CustomerSchemaOverride, error) {
	return s.overrides.ListByCustomer(ctx, customerID)
}

func (s *Service) GetOverride(ctx context.Context, customerID uuid.UUID, kind domain.SchemaKind) (*domain.CustomerSchemaOverride, error) {
	return s.overrides.FindByCustomerAndKind(ctx, customerID, kind)
}

func (s *Service) UpsertOverride(ctx context.Context, in port.UpsertOverrideInput) (*domain.CustomerSchemaOverride, error) {
	o, err := domain.NewOverride(in.CustomerID, in.Kind, in.SchemaCode, in.Patch)
	if err != nil {
		return nil, err
	}
	o.SchemaID = in.SchemaID
	o.Reason = in.Reason
	if in.ValidFrom != nil {
		o.ValidFrom = *in.ValidFrom
	}
	o.ValidUntil = in.ValidUntil
	o.CreatedBy = in.CreatedBy
	if err := s.overrides.Upsert(ctx, o); err != nil {
		return nil, err
	}
	// Re-read so the caller sees the authoritative revision /
	// timestamps after the ON CONFLICT path bumps revision.
	stored, err := s.overrides.FindByCustomerAndKind(ctx, in.CustomerID, in.Kind)
	if err != nil {
		return nil, err
	}
	return stored, nil
}

func (s *Service) DeleteOverride(ctx context.Context, customerID uuid.UUID, kind domain.SchemaKind) error {
	return s.overrides.Delete(ctx, customerID, kind)
}

// =====================================================================
// Resolution
// =====================================================================

// ResolveSchemaForCustomer returns the resolved JSON body that
// downstream modules (billing tick, commission calc, suspension
// scheduler) should evaluate.
//
// Lookup order:
//  1. Try to find an override for (customer, kind). If found, pick
//     the schema by override.SchemaID if pinned; otherwise pick the
//     latest published by override.SchemaCode.
//  2. No override → pick the latest published schema by kind +
//     **default code** "DEFAULT". This is the convention — every kind
//     has a "DEFAULT" code so the tenant always resolves to something.
//
// Returns a typed NotFound if neither a customer-specific schema nor
// a DEFAULT exists.
func (s *Service) ResolveSchemaForCustomer(
	ctx context.Context, customerID uuid.UUID, kind domain.SchemaKind,
) (json.RawMessage, *domain.SchemaDefinition, *domain.CustomerSchemaOverride, error) {
	if !kind.IsValid() {
		return nil, nil, nil, derrors.Validation(
			"schema.kind_invalid",
			"kind must be one of: billing, commission, suspension, service",
		)
	}

	var (
		schema   *domain.SchemaDefinition
		override *domain.CustomerSchemaOverride
		err      error
	)
	override, err = s.overrides.FindByCustomerAndKind(ctx, customerID, kind)
	if err != nil && !derrors.IsNotFound(err) {
		return nil, nil, nil, err
	}
	if override != nil {
		if override.SchemaID != nil {
			schema, err = s.schemas.FindByID(ctx, *override.SchemaID)
			if err != nil && !derrors.IsNotFound(err) {
				return nil, nil, nil, err
			}
		}
		if schema == nil {
			schema, err = s.schemas.FindLatestPublished(ctx, kind, override.SchemaCode)
			if err != nil && !derrors.IsNotFound(err) {
				return nil, nil, nil, err
			}
		}
	}
	if schema == nil {
		// Fallback path — no override, or override pointed at a
		// missing version. Resolve to the kind's DEFAULT code.
		schema, err = s.schemas.FindLatestPublished(ctx, kind, "DEFAULT")
		if err != nil {
			return nil, nil, nil, err
		}
	}

	resolved, err := domain.ResolveForCustomer(schema, override)
	if err != nil {
		return nil, nil, nil, err
	}
	return resolved, schema, override, nil
}
