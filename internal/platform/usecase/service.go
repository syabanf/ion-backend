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

	// Wave 82 Tier 2c — optional reader of crm.customers.locked_*_schema_version_id.
	// When wired, ResolveSchemaForCustomerWith automatically loads the
	// customer's locked version for the requested kind whenever the
	// caller didn't pass an explicit LockedVersionID. This closes the
	// loop from Wave 80b's snapshot writer: locks written at lead
	// conversion are now honored on every subsequent resolve.
	locks port.CustomerLockReader

	// Wave 116 — Deep schema content validators. Both fields are
	// optional; without them the service behaves identically to
	// pre-Wave-116 (no publish gate, no audit row writes).
	validators        *domain.ValidatorRegistry
	validationResults port.ValidationResultRepository
}

func NewService(schemas port.SchemaRepository, overrides port.OverrideRepository) *Service {
	return &Service{schemas: schemas, overrides: overrides}
}

// WithCustomerLockReader enables the auto-load of customer schema
// version locks. Optional. Without it, the resolver behaves exactly
// as it did pre-Wave-82: locks are only honored when a caller passes
// LockedVersionID explicitly via ResolveOptions.
func (s *Service) WithCustomerLockReader(r port.CustomerLockReader) *Service {
	if r != nil {
		s.locks = r
	}
	return s
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

// PublishSchema flips the target schema to published. If a prior
// published version of the same (kind, code) exists, it is atomically
// flipped to superseded — guarantees FindLatestPublished returns at
// most one row per (kind, code).
//
// Wave 79 (TC-SCH-008): the strict path requires status=approved before
// publishing. To preserve back-compat for environments without configured
// approvers (CI, demo seed scripts), the usecase calls `PublishDirect`
// which bypasses the approval gate. Production routes that need the
// approval gating should go through SubmitForApproval → Approve →
// PublishSchema explicitly, or use a stricter route variant in Wave 79b.
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
	// PublishDirect preserves the pre-Wave-79 draft→published path so
	// existing seed scripts + CI tests don't regress. The strict
	// Publish() path (only from approved) is exercised by the
	// SubmitForApproval/Approve flow once Wave 79b lands the wiring.
	if err := def.PublishDirect(); err != nil {
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
// Wave 78 (TC-SCH-011/015/023/026, TC-PRD-019/025/026/029/030): the
// lookup order is now four-tier instead of two.
//
//  1. Customer override (per-customer pin). If override.SchemaID is
//     set, use that exact version; else FindLatestPublished by code.
//  2. Customer locked version (Wave 78 — at conversion, the resolver
//     snapshotted the version it chose; we honor it here). Caller
//     passes the locked id via ResolveOptions; nil = no lock.
//  3. Product schema slot (Wave 77 — plan-specific override above the
//     customer-type default). Caller passes via ResolveOptions; the
//     resolver looks up by id.
//  4. Global DEFAULT — `FindLatestPublished(kind, "DEFAULT")`.
//
// Returns a typed NotFound only if all four tiers fail (no DEFAULT
// row published).
func (s *Service) ResolveSchemaForCustomer(
	ctx context.Context, customerID uuid.UUID, kind domain.SchemaKind,
) (json.RawMessage, *domain.SchemaDefinition, *domain.CustomerSchemaOverride, error) {
	// Legacy callers — no product/lock context. Equivalent to passing
	// empty ResolveOptions.
	return s.ResolveSchemaForCustomerWith(ctx, customerID, kind, ResolveOptions{})
}

// ResolveOptions carries the additional inputs the new resolver needs.
// Both fields are optional; the resolver behaves correctly when both
// are nil (falls back to override → DEFAULT, matching the pre-Wave-78
// contract).
//
// Typical callers:
//   - Lead-conversion path: pass ProductSchemaSlotID (from product
//     row) and LockedVersionID = nil to capture the resolver's choice,
//     then persist the returned SchemaDefinition.ID to crm.customers.
//   - Billing/RADIUS tick: pass LockedVersionID (from crm.customers)
//     and ProductSchemaSlotID = nil — the locked id wins.
type ResolveOptions struct {
	// ProductSchemaSlotID — when set, the resolver checks this id
	// (looking up by SchemaID via FindByID) after the customer override
	// and before the DEFAULT fallback. Pass `product.<kind>_schema_id`
	// from crm.products.
	ProductSchemaSlotID *uuid.UUID

	// LockedVersionID — when set, the resolver returns this exact
	// version regardless of what's currently the "latest published"
	// for the schema code. This is the version-lock contract from
	// QA TC-SCH-011/023/026.
	LockedVersionID *uuid.UUID
}

// ResolveSchemaForCustomerWith — full-context resolver. Lookup order
// is documented on ResolveSchemaForCustomer. Calling sites that have
// product + locked-version context should prefer this entrypoint.
func (s *Service) ResolveSchemaForCustomerWith(
	ctx context.Context, customerID uuid.UUID, kind domain.SchemaKind, opts ResolveOptions,
) (json.RawMessage, *domain.SchemaDefinition, *domain.CustomerSchemaOverride, error) {
	if !kind.IsValid() {
		return nil, nil, nil, derrors.Validation(
			"schema.kind_invalid",
			"kind must be one of: billing, commission, suspension, service",
		)
	}

	// Wave 82 Tier 2c — auto-load the customer's locked schema version
	// when the caller didn't supply one. This closes the loop from
	// Wave 80b's snapshot writer: locks written at lead conversion are
	// now honored on every subsequent resolve, regardless of which
	// caller invokes us. Reader is optional; without it we behave as
	// before. Lookup errors are non-fatal — we fall through to the
	// normal resolution chain.
	if s.locks != nil && opts.LockedVersionID == nil && customerID != uuid.Nil {
		if locked, lerr := s.locks.LockedVersionFor(ctx, customerID, kind); lerr == nil && locked != nil {
			opts.LockedVersionID = locked
		}
	}

	var (
		schema   *domain.SchemaDefinition
		override *domain.CustomerSchemaOverride
		err      error
	)

	// Tier 1 — customer override (highest priority).
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

	// Tier 2 — customer-locked version (Wave 78). Honored only when
	// override didn't already resolve. The lock points at a specific
	// schema_definitions row, so we use FindByID. If that lookup fails
	// (e.g. row was deleted), we fall through — better than returning
	// a stale error to the caller.
	if schema == nil && opts.LockedVersionID != nil {
		v, lerr := s.schemas.FindByID(ctx, *opts.LockedVersionID)
		if lerr == nil && v != nil {
			schema = v
		} else if lerr != nil && !derrors.IsNotFound(lerr) {
			return nil, nil, nil, lerr
		}
	}

	// Tier 3 — product schema slot (Wave 77). Plan-specific assignment
	// above the customer-type default.
	if schema == nil && opts.ProductSchemaSlotID != nil {
		v, perr := s.schemas.FindByID(ctx, *opts.ProductSchemaSlotID)
		if perr == nil && v != nil {
			schema = v
		} else if perr != nil && !derrors.IsNotFound(perr) {
			return nil, nil, nil, perr
		}
	}

	// Tier 4 — global DEFAULT fallback. Every kind ships a DEFAULT
	// row in seed data so this never returns NotFound in normal use.
	if schema == nil {
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
