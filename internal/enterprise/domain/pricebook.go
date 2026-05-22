// Package domain holds the enterprise context's entities and value objects.
//
// Rules (same as identity / crm / warehouse domains):
//   - No imports of pkg/database, pkg/httpserver, or any other framework.
//   - Constructors enforce invariants — callers can't construct an
//     invalid value.
//   - Errors are typed pkg/errors values so the HTTP layer can map them
//     to the right HTTP status without inspecting strings.
//
// Phase 2 entities: Pricebook, PricebookLine, Opportunity. Other CPQ
// entities (BOQ, Quotation, Negotiation, EWO, etc.) land in later
// phases — keeping this package small now keeps the bounded context
// easy to extract into its own service later.
package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// PricebookStatus tracks the published-vs-draft lifecycle. Only one
// row per `code` may be in `published` state at a time — enforced both
// by the postgres partial unique index and by the usecase's Publish
// transition.
type PricebookStatus string

const (
	PricebookStatusDraft      PricebookStatus = "draft"
	PricebookStatusPublished  PricebookStatus = "published"
	PricebookStatusSuperseded PricebookStatus = "superseded"
)

// Pricebook is a versioned catalog of services + materials with
// enterprise-grade pricing rules (margin floor, discount ceiling).
//
// Per CPQ §4.1 + TC-PB-001/002/007/008: every published pricebook is
// immutable from the operator's perspective. Editing a published row
// means publishing a new version — the previous row stays in place
// with status='superseded' so already-pinned Opportunities keep
// showing the prices they were quoted at.
//
// `HoldingCompanyID` is a free-form string for now — we'll formalize
// it to an FK once the holding-company entity lands (Phase 3+).
type Pricebook struct {
	ID                uuid.UUID
	Code              string
	Name              string
	Currency          string // 3-letter ISO (IDR / USD / etc.)
	EffectiveFrom     time.Time
	EffectiveTo       *time.Time
	HoldingCompanyID  string
	VersionNo         int
	Status            PricebookStatus
	PublishedAt       *time.Time
	SupersededAt      *time.Time
	Notes             string
	CreatedBy         *uuid.UUID
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// NewPricebook constructs a new draft pricebook. The version number
// starts at 1; subsequent publishes-after-edit increment it via the
// usecase. Effective dates are validated as a closed window when
// `effectiveTo` is set.
func NewPricebook(code, name string, effectiveFrom time.Time, effectiveTo *time.Time) (*Pricebook, error) {
	code = strings.TrimSpace(code)
	name = strings.TrimSpace(name)
	if code == "" {
		return nil, errors.Validation("pricebook.code_required", "code is required")
	}
	if name == "" {
		return nil, errors.Validation("pricebook.name_required", "name is required")
	}
	if effectiveTo != nil && !effectiveTo.After(effectiveFrom) {
		return nil, errors.Validation(
			"pricebook.effective_window_invalid",
			"effective_to must be strictly after effective_from",
		)
	}
	now := time.Now().UTC()
	return &Pricebook{
		ID:            uuid.New(),
		Code:          code,
		Name:          name,
		Currency:      "IDR",
		EffectiveFrom: effectiveFrom,
		EffectiveTo:   effectiveTo,
		VersionNo:     1,
		Status:        PricebookStatusDraft,
		CreatedAt:     now,
		UpdatedAt:     now,
	}, nil
}

// Publish transitions a draft pricebook to published. Idempotent on
// already-published; rejects superseded (you don't un-supersede a
// pricebook — you create a new one).
func (p *Pricebook) Publish() error {
	switch p.Status {
	case PricebookStatusPublished:
		return nil
	case PricebookStatusSuperseded:
		return errors.Conflict(
			"pricebook.cannot_publish_superseded",
			"superseded pricebooks cannot be republished — create a new version",
		)
	}
	now := time.Now().UTC()
	p.Status = PricebookStatusPublished
	p.PublishedAt = &now
	p.UpdatedAt = now
	return nil
}

// Supersede flips a published pricebook to superseded — used when a
// newer version takes over. The published_at timestamp is preserved
// so audit trails can show "this version was live from X to Y".
func (p *Pricebook) Supersede() error {
	if p.Status != PricebookStatusPublished {
		return errors.Conflict(
			"pricebook.cannot_supersede_unpublished",
			"only published pricebooks can be superseded",
		)
	}
	now := time.Now().UTC()
	p.Status = PricebookStatusSuperseded
	p.SupersededAt = &now
	p.UpdatedAt = now
	return nil
}

// Overlaps reports whether two pricebooks (presumably with the same
// `Code`) have overlapping effective windows. NULL `EffectiveTo`
// represents an open-ended window — treated as "infinity" in the
// comparison.
//
// Used by the usecase's pre-insert overlap check (CPQ TC-PB-002 →
// HTTP 409 pricebook_overlap).
func (p *Pricebook) Overlaps(other *Pricebook) bool {
	// Two ranges [a1, a2] and [b1, b2] overlap iff a1 <= b2 AND b1 <= a2.
	// With nil = +∞, the comparisons become straightforward.
	a1 := p.EffectiveFrom
	b1 := other.EffectiveFrom
	// p.EffectiveTo nil → p extends forever → overlap iff b1 <= +∞ (always true)
	// other.EffectiveTo nil → same shape on the other side.
	if p.EffectiveTo == nil && other.EffectiveTo == nil {
		return true
	}
	if p.EffectiveTo == nil {
		// p forever ⇒ overlap iff a1 <= other.EffectiveTo
		return !a1.After(*other.EffectiveTo)
	}
	if other.EffectiveTo == nil {
		return !b1.After(*p.EffectiveTo)
	}
	// Both bounded.
	return !a1.After(*other.EffectiveTo) && !b1.After(*p.EffectiveTo)
}
