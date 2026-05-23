package domain

import (
	"time"

	"github.com/google/uuid"
)

// HoldingCompany is the top-level tenant in the multi-company model
// (Wave 92). Each holding owns one or more `Subsidiary` entities that
// represent the legal/operational arms doing actual business —
// reseller, supplier, holding ops.
//
// Today the entity is intentionally thin: it carries identity (name +
// NPWP + legal type) and nothing else. Holding-level commercial rules
// (consolidated reporting, inter-co transfer pricing, etc.) land in
// follow-up waves once the FK-to-existing-tables rollout is complete.
//
// NPWP and LegalEntityType are pointers because they are optional
// in the database — a fresh holding can be created and have its tax
// identity filled in later.
type HoldingCompany struct {
	ID              uuid.UUID
	Name            string
	NPWP            *string
	LegalEntityType *string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}
