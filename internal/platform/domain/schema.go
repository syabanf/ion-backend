// Package domain holds the Schema System v1 aggregates.
//
// Schemas are versioned, per-tenant rule sets that drive billing,
// commission, suspension, and service behavior. They live in the
// platform namespace (i.e. cross-module) so any service can read /
// resolve a schema for a customer without crossing a bounded-context
// boundary.
//
// Lifecycle mirrors enterprise.boq_versions:
//   - draft     → editable, not yet pickable for customer assignment
//   - published → immutable, the live version
//   - superseded→ retired by a newer published version
//
// Customer-level overrides patch specific fields on a published schema
// for one customer (e.g. "customer X gets 30 grace_days instead of 10")
// without forking the whole schema definition.
package domain

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"

	derrors "github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// Schema kinds — mirror platform.schema_kind enum in DB.
// =====================================================================

type SchemaKind string

const (
	SchemaKindBilling    SchemaKind = "billing"
	SchemaKindCommission SchemaKind = "commission"
	SchemaKindSuspension SchemaKind = "suspension"
	SchemaKindService    SchemaKind = "service"
)

// IsValid reports whether k is one of the four well-known kinds.
func (k SchemaKind) IsValid() bool {
	switch k {
	case SchemaKindBilling, SchemaKindCommission, SchemaKindSuspension, SchemaKindService:
		return true
	}
	return false
}

// ParseSchemaKind is the inbound-validation helper for HTTP / query
// strings. Empty / unknown values return a typed validation error.
func ParseSchemaKind(s string) (SchemaKind, error) {
	k := SchemaKind(strings.ToLower(strings.TrimSpace(s)))
	if !k.IsValid() {
		return "", derrors.Validation(
			"schema.kind_invalid",
			"kind must be one of: billing, commission, suspension, service",
		)
	}
	return k, nil
}

// =====================================================================
// Schema lifecycle status.
// =====================================================================

type SchemaStatus string

const (
	SchemaStatusDraft      SchemaStatus = "draft"
	SchemaStatusSubmitted  SchemaStatus = "submitted" // Wave 79 — awaiting approver votes
	SchemaStatusApproved   SchemaStatus = "approved"  // Wave 79 — all approvals in, ready to publish
	SchemaStatusPublished  SchemaStatus = "published"
	SchemaStatusRejected   SchemaStatus = "rejected" // Wave 79 — transient; auto-flips back to draft with reason
	SchemaStatusSuperseded SchemaStatus = "superseded"
)

// =====================================================================
// SchemaDefinition aggregate.
// =====================================================================

// SchemaDefinition is one (kind, code, version_no) row in
// platform.schema_definitions. Body is the kind-specific JSON payload —
// kept opaque at the domain layer so we don't have to recompile when
// the per-kind shape evolves.
type SchemaDefinition struct {
	ID           uuid.UUID
	Kind         SchemaKind
	Code         string
	VersionNo    int
	Name         string
	Description  string
	Body         json.RawMessage
	Status       SchemaStatus
	PublishedAt  *time.Time
	SupersededAt *time.Time
	// Wave 79 — approval workflow audit timestamps.
	SubmittedAt     *time.Time
	ApprovedAt      *time.Time
	RejectionReason string
	Notes           string
	CreatedBy       *uuid.UUID
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// NewSchema constructs a draft schema. Validation enforces non-empty
// code/name and a valid kind — the body must be a JSON object so the
// override patch's shallow-merge has something well-formed to merge
// over.
func NewSchema(kind SchemaKind, code, name string, body json.RawMessage) (*SchemaDefinition, error) {
	if !kind.IsValid() {
		return nil, derrors.Validation(
			"schema.kind_invalid",
			"kind must be one of: billing, commission, suspension, service",
		)
	}
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, derrors.Validation("schema.code_required", "code is required")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, derrors.Validation("schema.name_required", "name is required")
	}
	if len(body) == 0 {
		body = json.RawMessage("{}")
	}
	// Reject anything that isn't a JSON object — arrays / strings /
	// numbers as the top-level body would break shallow-merge and have
	// no clear semantic for billing/commission rule sets.
	if !isJSONObject(body) {
		return nil, derrors.Validation(
			"schema.body_invalid",
			"body must be a JSON object",
		)
	}
	now := time.Now().UTC()
	return &SchemaDefinition{
		ID:        uuid.New(),
		Kind:      kind,
		Code:      code,
		VersionNo: 1,
		Name:      name,
		Body:      body,
		Status:    SchemaStatusDraft,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

// PublishDirect bypasses the Wave 79 approval gate — used by the
// usecase layer when no approvers are configured for the schema's
// kind (back-compat path so seed scripts and CI keep working).
//
// Callers must own the "is approval required?" decision before
// invoking. Production routes (HTTP handlers) should go through the
// strict Publish() entrypoint and SubmitForApproval/Approve flow.
//
// Idempotent on already-published; rejects superseded.
func (s *SchemaDefinition) PublishDirect() error {
	switch s.Status {
	case SchemaStatusPublished:
		return nil
	case SchemaStatusSuperseded:
		return derrors.Conflict(
			"schema.cannot_publish_superseded",
			"a superseded schema cannot be re-published",
		)
	}
	now := time.Now().UTC()
	s.Status = SchemaStatusPublished
	s.PublishedAt = &now
	s.UpdatedAt = now
	return nil
}

// Publish flips an approved schema to published and stamps PublishedAt.
//
// Wave 79 (TC-SCH-008) gate: only `approved` schemas can be published.
// The pre-Wave-79 contract (draft → published direct) lives in
// PublishDirect; the usecase chooses between them based on whether
// any approvers are configured for the kind.
//
// Idempotent on already-published; rejects superseded.
func (s *SchemaDefinition) Publish() error {
	switch s.Status {
	case SchemaStatusPublished:
		return nil
	case SchemaStatusSuperseded:
		return derrors.Conflict(
			"schema.cannot_publish_superseded",
			"a superseded schema cannot be re-published",
		)
	case SchemaStatusDraft, SchemaStatusSubmitted, SchemaStatusRejected:
		// Wave 79 (TC-SCH-008): publish requires approval first.
		return derrors.Conflict(
			"schema.publish_requires_approval",
			"schema must be approved before publishing — current status: "+string(s.Status),
		)
	}
	now := time.Now().UTC()
	s.Status = SchemaStatusPublished
	s.PublishedAt = &now
	s.UpdatedAt = now
	return nil
}

// SubmitForApproval flips a draft to submitted, stamping SubmittedAt.
// Wave 79 (TC-SCH-007). Caller (usecase layer) is responsible for
// notifying the configured approvers — the domain just owns the state
// transition.
func (s *SchemaDefinition) SubmitForApproval() error {
	if s.Status != SchemaStatusDraft {
		return derrors.Conflict(
			"schema.not_draft",
			"only draft schemas can be submitted for approval — current: "+string(s.Status),
		)
	}
	now := time.Now().UTC()
	s.Status = SchemaStatusSubmitted
	s.SubmittedAt = &now
	s.UpdatedAt = now
	return nil
}

// Approve flips submitted → approved. Caller checks the per-kind
// approver quorum before invoking this. Wave 79 (TC-SCH-008).
func (s *SchemaDefinition) Approve() error {
	if s.Status != SchemaStatusSubmitted {
		return derrors.Conflict(
			"schema.not_submitted",
			"only submitted schemas can be approved — current: "+string(s.Status),
		)
	}
	now := time.Now().UTC()
	s.Status = SchemaStatusApproved
	s.ApprovedAt = &now
	s.UpdatedAt = now
	return nil
}

// Reject sends a submitted schema back to draft with the captured
// reason. Wave 79 (TC-SCH-009): the rejection_reason is preserved on
// the row so the schema builder UI can render it as a callout. Status
// becomes "draft", not "rejected" — the rejected state in the DB
// CHECK is a transient marker the usecase may use during atomic write
// paths; from the caller's view a rejected schema is just a draft
// with a reason.
func (s *SchemaDefinition) Reject(reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return derrors.Validation(
			"schema.reject_reason_required",
			"a reason is required when rejecting a schema",
		)
	}
	if s.Status != SchemaStatusSubmitted {
		return derrors.Conflict(
			"schema.not_submitted",
			"only submitted schemas can be rejected — current: "+string(s.Status),
		)
	}
	now := time.Now().UTC()
	// Per TC-SCH-009: rejection flips back to draft so the operator
	// can edit + resubmit. The reason is captured for the UI to surface.
	s.Status = SchemaStatusDraft
	s.RejectionReason = reason
	s.UpdatedAt = now
	return nil
}

// Supersede is the terminal flip — used when a newer published version
// of the same (kind, code) lands. Only valid from published; calling
// from draft / already-superseded yields a typed conflict.
func (s *SchemaDefinition) Supersede() error {
	if s.Status != SchemaStatusPublished {
		return derrors.Conflict(
			"schema.cannot_supersede",
			"only published schemas can be superseded",
		)
	}
	now := time.Now().UTC()
	s.Status = SchemaStatusSuperseded
	s.SupersededAt = &now
	s.UpdatedAt = now
	return nil
}

// UpdateDraft applies editable fields to a draft schema. Published or
// superseded rows are immutable — the caller must create a new draft
// (next version_no) instead.
func (s *SchemaDefinition) UpdateDraft(name, description *string, body json.RawMessage, notes *string) error {
	if s.Status != SchemaStatusDraft {
		return derrors.Conflict(
			"schema.not_draft",
			"only draft schemas can be edited",
		)
	}
	if name != nil {
		v := strings.TrimSpace(*name)
		if v == "" {
			return derrors.Validation("schema.name_required", "name is required")
		}
		s.Name = v
	}
	if description != nil {
		s.Description = *description
	}
	if len(body) > 0 {
		if !isJSONObject(body) {
			return derrors.Validation(
				"schema.body_invalid",
				"body must be a JSON object",
			)
		}
		s.Body = body
	}
	if notes != nil {
		s.Notes = *notes
	}
	s.UpdatedAt = time.Now().UTC()
	return nil
}

// =====================================================================
// CustomerSchemaOverride aggregate.
// =====================================================================

// CustomerSchemaOverride is a thin patch on a published schema for one
// customer. The (customer_id, schema_kind) pair is unique — one
// override per kind per customer.
type CustomerSchemaOverride struct {
	ID          uuid.UUID
	CustomerID  uuid.UUID
	SchemaKind  SchemaKind
	SchemaID    *uuid.UUID // pinned version; nil = track latest
	SchemaCode  string
	Patch       json.RawMessage
	Reason      string
	ValidFrom   time.Time
	ValidUntil  *time.Time
	Revision    int
	CreatedBy   *uuid.UUID
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// NewOverride constructs a fresh override row. The patch must be a JSON
// object (shallow-merge target shape). A zero-key patch is allowed —
// useful when an operator wants to pin a customer to a specific schema
// version without actually changing any field.
func NewOverride(
	customerID uuid.UUID,
	kind SchemaKind,
	schemaCode string,
	patch json.RawMessage,
) (*CustomerSchemaOverride, error) {
	if !kind.IsValid() {
		return nil, derrors.Validation(
			"schema_override.kind_invalid",
			"kind must be one of: billing, commission, suspension, service",
		)
	}
	schemaCode = strings.TrimSpace(schemaCode)
	if schemaCode == "" {
		return nil, derrors.Validation(
			"schema_override.code_required",
			"schema_code is required",
		)
	}
	if len(patch) == 0 {
		patch = json.RawMessage("{}")
	}
	if !isJSONObject(patch) {
		return nil, derrors.Validation(
			"schema_override.patch_invalid",
			"patch must be a JSON object",
		)
	}
	now := time.Now().UTC()
	return &CustomerSchemaOverride{
		ID:         uuid.New(),
		CustomerID: customerID,
		SchemaKind: kind,
		SchemaCode: schemaCode,
		Patch:      patch,
		ValidFrom:  now,
		Revision:   1,
		CreatedAt:  now,
		UpdatedAt:  now,
	}, nil
}

// =====================================================================
// Resolution — schema body + (optional) override patch.
// =====================================================================

// ResolveForCustomer shallow-merges override.Patch over schema.Body.
//
// Contract:
//   - schema is required; nil = validation error.
//   - override is optional; nil = pass schema.Body through unchanged.
//   - Merge depth is ONE — top-level keys in patch fully replace the
//     same key in body. Nested objects in body are NOT recursively
//     merged. This is the explicit semantic in 0032's migration
//     comment ("shallow-merged over the schema body at evaluation
//     time").
//
// Both inputs are expected to be JSON objects per the constructors
// above; mismatched shapes fall back to the schema body to keep the
// caller honest about validating at write time.
//
// This is the function billing tick / commission calc / suspension
// scheduler will call at runtime. Keep it deterministic and allocation-
// light: one parse per input, one merge map, one re-marshal.
func ResolveForCustomer(schema *SchemaDefinition, override *CustomerSchemaOverride) (json.RawMessage, error) {
	if schema == nil {
		return nil, derrors.Validation(
			"schema.resolve_nil_schema",
			"schema is required to resolve",
		)
	}
	if override == nil || len(override.Patch) == 0 || string(override.Patch) == "{}" {
		// No override → return the body as-is. Copy to avoid the
		// caller mutating our cached pointer.
		out := make(json.RawMessage, len(schema.Body))
		copy(out, schema.Body)
		return out, nil
	}

	var bodyMap map[string]json.RawMessage
	if err := json.Unmarshal(schema.Body, &bodyMap); err != nil {
		// Body isn't a JSON object — return it untouched. Validation
		// at write time should have prevented this.
		out := make(json.RawMessage, len(schema.Body))
		copy(out, schema.Body)
		return out, nil
	}
	var patchMap map[string]json.RawMessage
	if err := json.Unmarshal(override.Patch, &patchMap); err != nil {
		// Patch isn't a JSON object — same fallback as above.
		out := make(json.RawMessage, len(schema.Body))
		copy(out, schema.Body)
		return out, nil
	}

	// Shallow merge: patch keys win.
	for k, v := range patchMap {
		bodyMap[k] = v
	}
	out, err := json.Marshal(bodyMap)
	if err != nil {
		return nil, derrors.Wrap(
			derrors.KindInternal,
			"schema.resolve_marshal",
			"failed to marshal resolved body",
			err,
		)
	}
	return out, nil
}

// =====================================================================
// Helpers
// =====================================================================

// isJSONObject reports whether raw is a top-level JSON object. Used by
// constructors to reject scalars / arrays at write time.
func isJSONObject(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(trimmed, "{") {
		return false
	}
	// Validate it parses — guards against `{garbage`.
	var probe map[string]json.RawMessage
	return json.Unmarshal(raw, &probe) == nil
}
