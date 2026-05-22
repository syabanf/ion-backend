package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// OnboardingSchema replaces the hardcoded DefaultBroadbandDocs from r1.
// Stored as a row in crm.onboarding_schemas with a jsonb `content`. The
// service decodes `content` into a list of DocBlueprint slots and
// instantiates one OrderDocument per slot at lead creation, same as r1.
type OnboardingSchema struct {
	ID            uuid.UUID
	CustomerType  string
	ProductType   string
	Version       int
	Content       OnboardingContent
	Active        bool
	Notes         string
	CreatedBy     *uuid.UUID
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// OnboardingContent is the typed shape of the jsonb `content` column.
// We keep it permissive on unknown fields so adding new schema attributes
// later doesn't break old rows.
type OnboardingContent struct {
	Version   int                       `json:"version"`
	Documents []OnboardingContentDocument `json:"documents"`
}

// OnboardingContentDocument is one slot in the document checklist.
//
// ShowWhenAcceptExcess controls conditional visibility:
//
//	nil  — always shown
//	true — only when the lead's accept_excess_cable is true
//	false — only when accept_excess_cable is false
type OnboardingContentDocument struct {
	Key                  string `json:"key"`
	Label                string `json:"label"`
	Required             bool   `json:"required"`
	ShowWhenAcceptExcess *bool  `json:"show_when_accept_excess,omitempty"`
}

// MarshalContent serialises a Go value into the jsonb representation
// the DB column expects. Centralised here so the service doesn't
// import encoding/json directly for this.
func (s *OnboardingSchema) MarshalContent() ([]byte, error) {
	return json.Marshal(s.Content)
}

// UnmarshalSchemaContent parses the raw jsonb bytes from the DB.
// Surfaced as a free function so the repo can call it without an
// instance.
func UnmarshalSchemaContent(raw []byte) (OnboardingContent, error) {
	var c OnboardingContent
	if len(raw) == 0 {
		return c, nil
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, errors.Wrap(errors.KindInternal, "schema.content_decode",
			"failed to decode onboarding schema content", err)
	}
	return c, nil
}

// BlueprintsFor returns the document slots that apply to a lead with
// the given accept_excess flag. This is the replacement for the r1
// `DefaultBroadbandDocs(acceptExcess)` helper — same semantics, but
// the source is the schema row instead of hardcoded code.
func (c OnboardingContent) BlueprintsFor(acceptExcess bool) []DocBlueprint {
	out := make([]DocBlueprint, 0, len(c.Documents))
	for _, d := range c.Documents {
		if d.ShowWhenAcceptExcess != nil && *d.ShowWhenAcceptExcess != acceptExcess {
			continue
		}
		out = append(out, DocBlueprint{
			Key:      d.Key,
			Label:    d.Label,
			Required: d.Required,
		})
	}
	return out
}
