package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// ProviderCapability is one row in vendor.provider_capabilities. The
// capability_key is the join key the BOQ picker uses to filter — e.g.
// the BOQ asks for `fiber_drop` and the picker returns every provider
// with a matching capability row.
//
// MaxCapacity is optional; nil = unlimited.
type ProviderCapability struct {
	ID             uuid.UUID
	ProviderID     uuid.UUID
	CapabilityKey  string
	CapabilityName string
	MaxCapacity    *int
	CreatedAt      time.Time
}

// NewProviderCapability constructs a validated capability row. Keys are
// lower-cased + trimmed so the picker's case-insensitive lookup just
// works without each call site re-normalising.
func NewProviderCapability(providerID uuid.UUID, key, name string, maxCapacity *int) (*ProviderCapability, error) {
	if providerID == uuid.Nil {
		return nil, errors.Validation("capability.provider_required", "provider_id is required")
	}
	key = strings.ToLower(strings.TrimSpace(key))
	if key == "" {
		return nil, errors.Validation("capability.key_required", "capability_key is required")
	}
	if maxCapacity != nil && *maxCapacity < 0 {
		return nil, errors.Validation("capability.max_capacity_negative", "max_capacity must be >= 0")
	}
	return &ProviderCapability{
		ID:             uuid.New(),
		ProviderID:     providerID,
		CapabilityKey:  key,
		CapabilityName: strings.TrimSpace(name),
		MaxCapacity:    maxCapacity,
		CreatedAt:      time.Now().UTC(),
	}, nil
}
