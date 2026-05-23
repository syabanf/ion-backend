// Wave 116 — Validation use-case methods.
//
// The validation flow lives in its own file (not service.go) so the
// pre-existing schema lifecycle code stays untouched. Three entrypoints:
//
//   - ValidateSchemaContent       — one-off, triggered manually or by
//                                   the publish gate.
//   - ValidateAllPublishedSchemas — sweep, triggered nightly + by an
//                                   admin button.
//   - LatestValidation            — read.
//
// Also wires the publish gate: when the validator returns errors the
// PublishSchema call refuses to flip the schema to published. Drafts
// can still be validated, but publish is contingent on a clean run.

package usecase

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/platform/domain"
	"github.com/ion-core/backend/internal/platform/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// WithValidatorRegistry wires the per-kind content validator registry
// + results repository. Both are optional — without them, the service
// runs in pre-Wave-116 mode (publish skips validation, ValidateXxx
// methods return a typed NotConfigured error).
func (s *Service) WithValidatorRegistry(
	registry *domain.ValidatorRegistry,
	results port.ValidationResultRepository,
) *Service {
	if registry != nil {
		s.validators = registry
	}
	if results != nil {
		s.validationResults = results
	}
	return s
}

// ValidateSchemaContent runs the registered validator against the
// schema's body, writes a row to platform.schema_validation_results,
// and returns the typed result.
//
// Trigger source defaults to "manual" — the caller can override via the
// internal validateAndPersist when invoked from the publish gate or the
// nightly sweep.
func (s *Service) ValidateSchemaContent(ctx context.Context, schemaVersionID uuid.UUID) (*domain.ValidationResult, error) {
	return s.validateAndPersist(ctx, schemaVersionID, "manual")
}

func (s *Service) validateAndPersist(
	ctx context.Context, schemaVersionID uuid.UUID, triggeredBy string,
) (*domain.ValidationResult, error) {
	if s.validators == nil {
		return nil, derrors.Internal(
			"schema.validator_not_configured",
			"validator registry not wired",
		)
	}
	def, err := s.schemas.FindByID(ctx, schemaVersionID)
	if err != nil {
		return nil, err
	}

	result := s.validators.Run(string(def.Kind), def.Body)

	// Persist when a results repo is wired; otherwise just return the
	// in-memory result (still useful for the publish gate).
	if s.validationResults != nil {
		row := &port.ValidationResultRow{
			SchemaVersionID:  def.ID,
			IsValid:          result.IsValid,
			Errors:           result.Errors,
			Warnings:         result.Warnings,
			ValidatorVersion: result.ValidatorVersion,
			TriggeredBy:      triggeredBy,
		}
		if err := s.validationResults.Insert(ctx, row); err != nil {
			return nil, err
		}
	}
	return &result, nil
}

// ValidateAllPublishedSchemas sweeps every published row across all
// kinds. Returns (invalid, total, error). The sweep is best-effort per
// row — one row's failure to persist does not halt the sweep; the
// caller logs (invalid, total) for the audit signal.
func (s *Service) ValidateAllPublishedSchemas(ctx context.Context) (int, int, error) {
	if s.validators == nil {
		return 0, 0, derrors.Internal(
			"schema.validator_not_configured",
			"validator registry not wired",
		)
	}
	// Sweep one kind at a time so we can keep memory bounded and pagination
	// per-kind. In practice each (kind, code) has at most a handful of
	// published rows; this loop is O(N).
	kinds := []domain.SchemaKind{
		domain.SchemaKindBilling,
		domain.SchemaKindCommission,
		domain.SchemaKindSuspension,
		domain.SchemaKindService,
	}
	var total, invalid int
	for _, k := range kinds {
		items, _, err := s.schemas.List(ctx, port.SchemaListFilter{
			Kind:   k,
			Status: string(domain.SchemaStatusPublished),
			Limit:  500, // upper bound; production sees ≪ 100
		})
		if err != nil {
			return invalid, total, err
		}
		for _, def := range items {
			total++
			res, verr := s.validateAndPersist(ctx, def.ID, "nightly_sweep")
			if verr != nil {
				// Don't abort the sweep on a single row failure — log
				// via the caller (we don't have a logger here; the cron
				// wrapper logs).
				continue
			}
			if !res.IsValid {
				invalid++
			}
		}
	}
	return invalid, total, nil
}

// LatestValidation returns the most recent validation row for a schema,
// or nil + NotFound when none exists.
func (s *Service) LatestValidation(ctx context.Context, schemaVersionID uuid.UUID) (*port.ValidationResultRow, error) {
	if s.validationResults == nil {
		return nil, derrors.Internal(
			"schema.validation_results_not_configured",
			"validation results repository not wired",
		)
	}
	return s.validationResults.LatestForSchema(ctx, schemaVersionID)
}

// ListActiveByKind returns published schemas of the given kind that
// have a latest validation result with is_valid=true.
//
// Implementation: list all published rows; for each, ask the results
// repo for its latest validation; include only those where the latest
// is_valid=true. A schema that has NEVER been validated is excluded
// (conservative — admin should validate before relying on it).
func (s *Service) ListActiveByKind(ctx context.Context, kind domain.SchemaKind) ([]domain.SchemaDefinition, error) {
	if !kind.IsValid() {
		return nil, derrors.Validation(
			"schema.kind_invalid",
			"kind must be one of: billing, commission, suspension, service",
		)
	}
	items, _, err := s.schemas.List(ctx, port.SchemaListFilter{
		Kind:   kind,
		Status: string(domain.SchemaStatusPublished),
		Limit:  500,
	})
	if err != nil {
		return nil, err
	}
	if s.validationResults == nil {
		// No validation tracking → return everything that's published.
		return items, nil
	}
	out := make([]domain.SchemaDefinition, 0, len(items))
	for _, def := range items {
		latest, lerr := s.validationResults.LatestForSchema(ctx, def.ID)
		if lerr != nil {
			if derrors.IsNotFound(lerr) {
				continue
			}
			return nil, lerr
		}
		if latest.IsValid {
			out = append(out, def)
		}
	}
	return out, nil
}

// PublishSchemaWithValidation is the Wave-116 publish gate: it runs the
// validator before flipping to published, refusing on error. Warnings
// are advisory and don't block.
//
// Distinct method (rather than mutating PublishSchema) so seed scripts
// and CI keep working without a validator wired. HTTP handler routes
// the publish call through this path when validators are configured.
func (s *Service) PublishSchemaWithValidation(ctx context.Context, id uuid.UUID) (*domain.SchemaDefinition, error) {
	// No registry → fall through to the legacy publish path.
	if s.validators == nil {
		return s.PublishSchema(ctx, id)
	}
	result, err := s.validateAndPersist(ctx, id, "publish_gate")
	if err != nil {
		return nil, err
	}
	if !result.IsValid {
		// Surface the first few error codes in the message so the caller's
		// audit log + admin UI can render actionable text. The full list
		// is persisted via validation_results — the HTTP adapter pulls it
		// down via the latest-validation endpoint.
		summary := strings.Join(result.Errors, "; ")
		if len(summary) > 400 {
			summary = summary[:400] + "…"
		}
		return nil, derrors.Validation(
			"schema.publish_validation_failed",
			"schema content failed validation — fix errors before publishing: "+summary,
		)
	}
	return s.PublishSchema(ctx, id)
}
