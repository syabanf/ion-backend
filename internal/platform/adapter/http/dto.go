package http

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/platform/domain"
)

// rfc3339 returns t in canonical RFC 3339 UTC.
func rfc3339(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

func rfc3339Ptr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := rfc3339(*t)
	return &s
}

func uuidPtrString(u *uuid.UUID) *string {
	if u == nil {
		return nil
	}
	s := u.String()
	return &s
}

// =====================================================================
// Schema DTOs
// =====================================================================

type schemaDTO struct {
	ID           string          `json:"id"`
	Kind         string          `json:"kind"`
	Code         string          `json:"code"`
	VersionNo    int             `json:"version_no"`
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	Body         json.RawMessage `json:"body"`
	Status       string          `json:"status"`
	PublishedAt  *string         `json:"published_at,omitempty"`
	SupersededAt *string         `json:"superseded_at,omitempty"`
	Notes        string          `json:"notes"`
	CreatedBy    *string         `json:"created_by,omitempty"`
	CreatedAt    string          `json:"created_at"`
	UpdatedAt    string          `json:"updated_at"`
}

func toSchemaDTO(s domain.SchemaDefinition) schemaDTO {
	body := s.Body
	if len(body) == 0 {
		body = []byte("{}")
	}
	return schemaDTO{
		ID:           s.ID.String(),
		Kind:         string(s.Kind),
		Code:         s.Code,
		VersionNo:    s.VersionNo,
		Name:         s.Name,
		Description:  s.Description,
		Body:         json.RawMessage(body),
		Status:       string(s.Status),
		PublishedAt:  rfc3339Ptr(s.PublishedAt),
		SupersededAt: rfc3339Ptr(s.SupersededAt),
		Notes:        s.Notes,
		CreatedBy:    uuidPtrString(s.CreatedBy),
		CreatedAt:    rfc3339(s.CreatedAt),
		UpdatedAt:    rfc3339(s.UpdatedAt),
	}
}

func toSchemaDTOPtr(s *domain.SchemaDefinition) *schemaDTO {
	if s == nil {
		return nil
	}
	d := toSchemaDTO(*s)
	return &d
}

type createSchemaRequest struct {
	Kind        string          `json:"kind"`
	Code        string          `json:"code"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Body        json.RawMessage `json:"body"`
	Notes       string          `json:"notes"`
}

type updateSchemaRequest struct {
	Name        *string         `json:"name,omitempty"`
	Description *string         `json:"description,omitempty"`
	Body        json.RawMessage `json:"body,omitempty"`
	Notes       *string         `json:"notes,omitempty"`
}

// =====================================================================
// Override DTOs
// =====================================================================

type overrideDTO struct {
	ID         string          `json:"id"`
	CustomerID string          `json:"customer_id"`
	SchemaKind string          `json:"schema_kind"`
	SchemaID   *string         `json:"schema_id,omitempty"`
	SchemaCode string          `json:"schema_code"`
	Patch      json.RawMessage `json:"patch"`
	Reason     string          `json:"reason"`
	ValidFrom  string          `json:"valid_from"`
	ValidUntil *string         `json:"valid_until,omitempty"`
	Revision   int             `json:"revision"`
	CreatedBy  *string         `json:"created_by,omitempty"`
	CreatedAt  string          `json:"created_at"`
	UpdatedAt  string          `json:"updated_at"`
}

func toOverrideDTO(o domain.CustomerSchemaOverride) overrideDTO {
	patch := o.Patch
	if len(patch) == 0 {
		patch = []byte("{}")
	}
	return overrideDTO{
		ID:         o.ID.String(),
		CustomerID: o.CustomerID.String(),
		SchemaKind: string(o.SchemaKind),
		SchemaID:   uuidPtrString(o.SchemaID),
		SchemaCode: o.SchemaCode,
		Patch:      json.RawMessage(patch),
		Reason:     o.Reason,
		ValidFrom:  rfc3339(o.ValidFrom),
		ValidUntil: rfc3339Ptr(o.ValidUntil),
		Revision:   o.Revision,
		CreatedBy:  uuidPtrString(o.CreatedBy),
		CreatedAt:  rfc3339(o.CreatedAt),
		UpdatedAt:  rfc3339(o.UpdatedAt),
	}
}

func toOverrideDTOPtr(o *domain.CustomerSchemaOverride) *overrideDTO {
	if o == nil {
		return nil
	}
	d := toOverrideDTO(*o)
	return &d
}

type upsertOverrideRequest struct {
	SchemaCode string          `json:"schema_code"`
	SchemaID   string          `json:"schema_id,omitempty"`
	Patch      json.RawMessage `json:"patch"`
	Reason     string          `json:"reason"`
	ValidFrom  string          `json:"valid_from,omitempty"`
	ValidUntil string          `json:"valid_until,omitempty"`
}

// =====================================================================
// Resolution DTO — what GET /platform/customer-schemas returns.
// =====================================================================

type resolvedDTO struct {
	Kind     string          `json:"kind"`
	Schema   *schemaDTO      `json:"schema,omitempty"`
	Override *overrideDTO    `json:"override,omitempty"`
	Resolved json.RawMessage `json:"resolved,omitempty"`
}
