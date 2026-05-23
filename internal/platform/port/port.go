// Package port defines the driving (UseCase) and driven (Repository)
// contracts for the platform/schemas bounded context.
//
// Same hexagonal pattern as enterprise / identity / crm: HTTP handlers
// depend on SchemaUseCase; the use case depends on repository
// interfaces; postgres adapters implement those interfaces. Domain
// types (SchemaDefinition, CustomerSchemaOverride) flow through.
package port

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/platform/domain"
)

// =====================================================================
// Schema inputs / filters
// =====================================================================

type CreateSchemaInput struct {
	Kind        domain.SchemaKind
	Code        string
	Name        string
	Description string
	Body        json.RawMessage
	Notes       string
	CreatedBy   *uuid.UUID
}

type UpdateSchemaDraftInput struct {
	ID          uuid.UUID
	Name        *string
	Description *string
	Body        json.RawMessage // omit / nil = no change
	Notes       *string
}

// SchemaListFilter is the read-side filter used by both /platform/schemas
// (operator surface) and module-level lookups (e.g. billing tick picking
// the live published schema for a customer).
type SchemaListFilter struct {
	Kind   domain.SchemaKind // empty = all kinds
	Status string            // "draft" | "published" | "superseded" | "" (all)
	Code   string            // exact match — empty = ignore
	Limit  int
	Offset int
}

// =====================================================================
// Override inputs
// =====================================================================

type UpsertOverrideInput struct {
	CustomerID uuid.UUID
	Kind       domain.SchemaKind
	SchemaCode string
	SchemaID   *uuid.UUID // optional: pin to a specific version
	Patch      json.RawMessage
	Reason     string
	ValidFrom  *time.Time
	ValidUntil *time.Time
	CreatedBy  *uuid.UUID
}

// =====================================================================
// SchemaUseCase
// =====================================================================

// SchemaUseCase is the single inbound port for the schema system.
// Routes flow through here — both the operator HTTP surface and any
// in-process consumer (billing tick, commission calc) eventually
// resolve through ResolveSchemaForCustomer.
type SchemaUseCase interface {
	// --- Schemas ---
	ListSchemas(ctx context.Context, f SchemaListFilter) ([]domain.SchemaDefinition, int, error)
	GetSchema(ctx context.Context, id uuid.UUID) (*domain.SchemaDefinition, error)
	CreateSchema(ctx context.Context, in CreateSchemaInput) (*domain.SchemaDefinition, error)
	UpdateDraftSchema(ctx context.Context, in UpdateSchemaDraftInput) (*domain.SchemaDefinition, error)
	PublishSchema(ctx context.Context, id uuid.UUID) (*domain.SchemaDefinition, error)
	SupersedeSchema(ctx context.Context, id uuid.UUID) (*domain.SchemaDefinition, error)

	// --- Overrides ---
	ListOverridesForCustomer(ctx context.Context, customerID uuid.UUID) ([]domain.CustomerSchemaOverride, error)
	GetOverride(ctx context.Context, customerID uuid.UUID, kind domain.SchemaKind) (*domain.CustomerSchemaOverride, error)
	UpsertOverride(ctx context.Context, in UpsertOverrideInput) (*domain.CustomerSchemaOverride, error)
	DeleteOverride(ctx context.Context, customerID uuid.UUID, kind domain.SchemaKind) error

	// --- Resolution ---
	// ResolveSchemaForCustomer returns the schema, optional override,
	// and the resolved jsonb body that downstream modules should
	// evaluate. The body is what billing/commission code will consume.
	ResolveSchemaForCustomer(
		ctx context.Context, customerID uuid.UUID, kind domain.SchemaKind,
	) (resolved json.RawMessage, schema *domain.SchemaDefinition, override *domain.CustomerSchemaOverride, err error)

	// --- Wave 116 — Validation ---
	// ValidateSchemaContent runs the registered content validator against
	// the schema's body, persists a row to platform.schema_validation_results,
	// and returns the typed result. Returns NotFound if schemaVersionID
	// doesn't exist.
	ValidateSchemaContent(ctx context.Context, schemaVersionID uuid.UUID) (*domain.ValidationResult, error)

	// ValidateAllPublishedSchemas sweeps every published schema, runs the
	// registered validator for each, and writes results. Returns the
	// number invalid + the total scanned. Designed for the nightly
	// cron + an admin "validate now" button.
	ValidateAllPublishedSchemas(ctx context.Context) (invalid int, total int, err error)

	// LatestValidation reads the most recent validation result for the
	// schema, or nil + NotFound if it has never been validated.
	LatestValidation(ctx context.Context, schemaVersionID uuid.UUID) (*ValidationResultRow, error)

	// ListActiveByKind returns published schemas of the given kind that
	// passed validation (latest result is_valid=true). Used by the admin
	// surface to render "what's live right now for kind X?"
	ListActiveByKind(ctx context.Context, kind domain.SchemaKind) ([]domain.SchemaDefinition, error)
}

// =====================================================================
// Repositories
// =====================================================================

type SchemaRepository interface {
	Create(ctx context.Context, s *domain.SchemaDefinition) error
	Update(ctx context.Context, s *domain.SchemaDefinition) error
	FindByID(ctx context.Context, id uuid.UUID) (*domain.SchemaDefinition, error)
	// FindLatestPublished returns the active published schema for a
	// (kind, code) pair, or NotFound. Used both by ResolveForCustomer
	// (when the override pin is nil) and by Publish to find the prior
	// row to supersede.
	FindLatestPublished(ctx context.Context, kind domain.SchemaKind, code string) (*domain.SchemaDefinition, error)
	// MaxVersion returns the highest version_no for (kind, code). Used
	// to assign the next draft's version when an operator creates a
	// schema with a code that already exists.
	MaxVersion(ctx context.Context, kind domain.SchemaKind, code string) (int, error)
	List(ctx context.Context, f SchemaListFilter) ([]domain.SchemaDefinition, int, error)
}

type OverrideRepository interface {
	Upsert(ctx context.Context, o *domain.CustomerSchemaOverride) error
	FindByCustomerAndKind(ctx context.Context, customerID uuid.UUID, kind domain.SchemaKind) (*domain.CustomerSchemaOverride, error)
	ListByCustomer(ctx context.Context, customerID uuid.UUID) ([]domain.CustomerSchemaOverride, error)
	Delete(ctx context.Context, customerID uuid.UUID, kind domain.SchemaKind) error
}

// CustomerLockReader (Wave 82 Tier 2c) — read crm.customers.locked_<kind>_schema_version_id
// for a customer. Returns (nil, nil) when no lock is set; (nil, err)
// only on infrastructure failure.
//
// Lives in the platform port because the platform resolver consumes it,
// but the implementation reads from crm.customers (cross-context). The
// adapter lives in internal/platform/adapter/crm/ — sibling to other
// driven adapters.
type CustomerLockReader interface {
	LockedVersionFor(ctx context.Context, customerID uuid.UUID, kind domain.SchemaKind) (*uuid.UUID, error)
}

// =====================================================================
// Wave 116 — Validation result storage.
// =====================================================================

// ValidationResultRow is the persisted shape of a single validator run.
// Mirrors platform.schema_validation_results 1:1.
type ValidationResultRow struct {
	ID                uuid.UUID
	SchemaVersionID   uuid.UUID
	ValidatedAt       time.Time
	IsValid           bool
	Errors            []string
	Warnings          []string
	ValidatorVersion  string
	TriggeredBy       string // 'manual' | 'publish_gate' | 'nightly_sweep'
}

// ValidationResultRepository persists validator outcomes for audit /
// admin surfaces. Writes are insert-only — the latest row by
// validated_at is the canonical "is this schema currently valid?"
// answer.
type ValidationResultRepository interface {
	Insert(ctx context.Context, row *ValidationResultRow) error
	// LatestForSchema returns the most recent row for schema_version_id,
	// or NotFound if none yet.
	LatestForSchema(ctx context.Context, schemaVersionID uuid.UUID) (*ValidationResultRow, error)
}
