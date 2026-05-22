package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// SLATemplate is the FK-only catalog of service-level templates a BOQ
// line picks from (CPQ TC-BQ-005: free-text `sla_text` rejected).
// Keep the structure minimal at MVP — `key` is the stable machine
// identifier, `details` is a free-shape JSON blob carrying uptime%,
// response hours, coverage window, etc.
type SLATemplate struct {
	ID          uuid.UUID
	Key         string
	Name        string
	Description string
	Details     []byte // raw JSONB
	Active      bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func NewSLATemplate(key, name string) (*SLATemplate, error) {
	key = strings.TrimSpace(key)
	name = strings.TrimSpace(name)
	if key == "" {
		return nil, errors.Validation("sla_template.key_required", "key is required")
	}
	if name == "" {
		return nil, errors.Validation("sla_template.name_required", "name is required")
	}
	now := time.Now().UTC()
	return &SLATemplate{
		ID:        uuid.New(),
		Key:       key,
		Name:      name,
		Details:   []byte("{}"),
		Active:    true,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}
