package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// Supplier is a vendor/distributor that supplies stock or services to
// ION. Fields mirror CRM-Sales-Enterprise PRD §5.1 "Vendor record fields"
// verbatim — Phase 2 enterprise procurement will reuse this same
// registry for RFQ + project-vendor linkage, so the surface lives in
// the warehouse context (current source of intake records) rather than
// behind a separate enterprise module that doesn't exist yet.
//
// CategoryTags is intentionally a flat string slice — admin tags
// suppliers with whichever catalog categories they cover (CCTV, fiber,
// rack-and-power, …). We don't validate the tag values here because
// the catalog of categories is itself admin-configurable; enforcing a
// closed enum at the domain level would couple suppliers to a
// table that's not in this bounded context.
type Supplier struct {
	ID            uuid.UUID
	Code          string
	CompanyName   string
	ContactPerson string
	Phone         string
	Email         string
	Address       string
	PaymentTerms  string // e.g. "net_30", "net_45", "cod"
	NPWP          string // Indonesian tax ID
	NIB           string // Business identification number
	CategoryTags  []string
	Notes         string
	Active        bool
	OnboardedAt   time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// NewSupplier constructs a supplier with the required-field invariants.
// Optional fields (contact info, tags, docs) are set by the caller
// after construction — they're not part of "what makes a valid
// supplier", they're metadata enriched over time.
func NewSupplier(code, companyName string) (*Supplier, error) {
	code = strings.TrimSpace(code)
	companyName = strings.TrimSpace(companyName)
	if code == "" {
		return nil, errors.Validation("supplier.code_required", "code is required")
	}
	if companyName == "" {
		return nil, errors.Validation("supplier.company_name_required", "company_name is required")
	}
	now := time.Now().UTC()
	return &Supplier{
		ID:           uuid.New(),
		Code:         code,
		CompanyName:  companyName,
		CategoryTags: []string{},
		Active:       true,
		OnboardedAt:  now,
		CreatedAt:    now,
		UpdatedAt:    now,
	}, nil
}
