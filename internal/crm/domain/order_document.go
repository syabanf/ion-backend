package domain

import (
	"time"

	"github.com/google/uuid"
)

// OrderDocument is one slot in a lead's onboarding-document checklist.
//
// In Phase 1 we use a hand-coded broadband checklist (DefaultBroadbandDocs)
// instead of a schema-driven checklist. When schema-driven onboarding lands,
// this constructor swaps to "load from schema, instantiate slots per lead".
type OrderDocument struct {
	ID        uuid.UUID
	LeadID    uuid.UUID
	DocKey    string
	Label     string
	Required  bool
	Submitted bool
	FileURL   string
	Notes     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// DocBlueprint describes one slot in the default checklist.
type DocBlueprint struct {
	Key      string
	Label    string
	Required bool
}

// DefaultBroadbandDocs returns the checklist a broadband lead must satisfy
// before it can be converted. acceptExcess controls whether the
// "Signed excess cable consent" slot is required.
func DefaultBroadbandDocs(acceptExcess bool) []DocBlueprint {
	docs := []DocBlueprint{
		{Key: "ktp_id", Label: "KTP / National ID", Required: true},
		{Key: "address_proof", Label: "Address proof", Required: true},
		{Key: "house_photo", Label: "House photo", Required: false},
		{Key: "gps_pin", Label: "GPS pin confirmation", Required: true},
	}
	if acceptExcess {
		docs = append(docs, DocBlueprint{
			Key:      "excess_cable_consent",
			Label:    "Signed excess-cable consent",
			Required: true,
		})
	}
	return docs
}

// NewOrderDocument instantiates one checklist slot for a lead.
func NewOrderDocument(leadID uuid.UUID, b DocBlueprint) *OrderDocument {
	return &OrderDocument{
		ID:        uuid.New(),
		LeadID:    leadID,
		DocKey:    b.Key,
		Label:     b.Label,
		Required:  b.Required,
		Submitted: false,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
}
