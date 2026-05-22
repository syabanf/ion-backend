package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// LeadStatus is the lifecycle status of a sales lead.
//
// PRD §6.3 specifies the pipeline as:
//
//	new       — captured, no further action yet
//	active    — sales rep is actively engaging the lead
//	warm      — initial interest confirmed
//	hot       — close to closing
//	converted — became a customer + order
//	lost      — customer changed mind / unreachable
//	potential — outside standard coverage, customer signaled willingness to accept excess
//
// Two coverage-driven values (`qualified`, `rejected`) are retained as
// deprecated synonyms for back-compat with rows written before
// migration 0023. New code shouldn't produce them, but conversion and
// status filters still treat them sensibly (qualified ≈ active+; rejected
// ≈ lost).
type LeadStatus string

const (
	LeadStatusNew       LeadStatus = "new"
	LeadStatusActive    LeadStatus = "active"
	LeadStatusWarm      LeadStatus = "warm"
	LeadStatusHot       LeadStatus = "hot"
	LeadStatusPotential LeadStatus = "potential"
	LeadStatusConverted LeadStatus = "converted"
	LeadStatusLost      LeadStatus = "lost"

	// Deprecated — kept for back-compat with pre-0023 rows. Do not
	// write these values from new code; the DB CHECK still permits
	// them so existing rows continue to load.
	LeadStatusQualified LeadStatus = "qualified"
	LeadStatusRejected  LeadStatus = "rejected"
)

// LeadSource records where a lead came from.
//
// Phase-1 broadband (legacy 5):
//
//	manual, self_order, sales_app, referral, cs_referral
//
// Wave 76 (QA TC-CRM-006) adds the rest of PRD §6.3's source enum so
// the broadband path matches the enterprise one. New sources do not
// need any usecase change — they're labels only.
type LeadSource string

const (
	LeadSourceManual        LeadSource = "manual"
	LeadSourceSelfOrder     LeadSource = "self_order"
	LeadSourceSalesApp      LeadSource = "sales_app"
	LeadSourceReferral      LeadSource = "referral"
	LeadSourceCSReferral    LeadSource = "cs_referral"
	LeadSourceColdCall      LeadSource = "cold_call"
	LeadSourceWebsite       LeadSource = "website"
	LeadSourceWhatsapp      LeadSource = "whatsapp"
	LeadSourceSocialMediaDM LeadSource = "social_media_dm"
	LeadSourceVoipCall      LeadSource = "voip_call"
	LeadSourceLineCall      LeadSource = "line_call"
	LeadSourceWalkIn        LeadSource = "walk_in"
	LeadSourceEvent         LeadSource = "event"
	LeadSourcePartner       LeadSource = "partner"
)

// AllLeadSources lists every value accepted by the DB CHECK on
// crm.leads.source. Useful for handler validation + frontend
// dropdown population. Order matches PRD §6.3's enumeration so
// the UI lists sources in a predictable way.
var AllLeadSources = []LeadSource{
	LeadSourceReferral,
	LeadSourceColdCall,
	LeadSourceWebsite,
	LeadSourceWhatsapp,
	LeadSourceSocialMediaDM,
	LeadSourceVoipCall,
	LeadSourceLineCall,
	LeadSourceWalkIn,
	LeadSourceEvent,
	LeadSourcePartner,
	LeadSourceCSReferral,
	// Operational / implicit sources kept at end of the list since
	// they're not user-typed values, they're inferred from where the
	// lead was captured.
	LeadSourceSalesApp,
	LeadSourceSelfOrder,
	LeadSourceManual,
}

// IsValidLeadSource returns true when s matches any allowed source.
func IsValidLeadSource(s LeadSource) bool {
	for _, v := range AllLeadSources {
		if v == s {
			return true
		}
	}
	return false
}

// LeadType partitions leads into broadband vs enterprise pipelines
// (QA TC-CRM-002, TC-CRM-003). The value is fixed at creation and
// must never change after conversion — converting an enterprise lead
// to a broadband customer would corrupt commission attribution.
type LeadType string

const (
	LeadTypeBroadband  LeadType = "broadband"
	LeadTypeEnterprise LeadType = "enterprise"
)

// CoverageVerdict mirrors network.CoverageVerdict; duplicated so this
// package does not import the network bounded context.
type CoverageVerdict string

const (
	CoverageVerdictCovered   CoverageVerdict = "covered"
	CoverageVerdictExcess    CoverageVerdict = "excess_distance"
	CoverageVerdictUncovered CoverageVerdict = "uncovered"
)

// Lead is the top-of-funnel prospect record.
type Lead struct {
	ID         uuid.UUID
	LeadNumber string
	Status     LeadStatus
	LeadType   LeadType // Wave 76 (TC-CRM-002): broadband or enterprise

	// Identity
	FullName string
	Phone    string
	Email    string
	NIK      string

	// Address
	Address string
	GPSLat  *float64
	GPSLng  *float64

	// Coverage snapshot at capture time
	CoverageVerdict   *CoverageVerdict
	CoverageSnapshot  []byte // raw jsonb bytes; opaque to the domain
	AcceptExcessCable bool
	NearestNodeID     *uuid.UUID
	CableDistanceM    *float64
	ExcessCharge      *float64

	// Scoping / ownership
	BranchID  *uuid.UUID
	ProductID *uuid.UUID
	SalesID   *uuid.UUID
	Source    LeadSource
	Notes     string

	// Wave 76 (TC-CRM-007/008): when Source = referral, this is the
	// active customer who referred the prospect. Must point to a
	// customer in `active` status — suspended/blocked/churned are
	// rejected at the usecase level.
	ReferrerCustomerID *uuid.UUID

	// Conversion linkage
	ConvertedCustomerID *uuid.UUID
	ConvertedOrderID    *uuid.UUID
	ConvertedAt         *time.Time

	// M4 r2 audit fields
	OnboardingSchemaID *uuid.UUID
	SalesTypeAtCreate  string // 'broadband' | 'enterprise' | 'both' — captured for audit

	CreatedBy *uuid.UUID
	CreatedAt time.Time
	UpdatedAt time.Time
}

// NewLead constructs a new Lead in 'new' status. Coverage fields and FKs are
// set separately by the usecase as it runs the coverage check and resolves
// the chosen product/branch. LeadType defaults to broadband — the usecase
// can override after construction for enterprise leads.
func NewLead(fullName, phone, address string) (*Lead, error) {
	fullName = strings.TrimSpace(fullName)
	phone = strings.TrimSpace(phone)
	address = strings.TrimSpace(address)
	if fullName == "" {
		return nil, errors.Validation("lead.name_required", "full_name is required")
	}
	if phone == "" {
		return nil, errors.Validation("lead.phone_required", "phone is required")
	}
	if address == "" {
		return nil, errors.Validation("lead.address_required", "address is required")
	}
	return &Lead{
		ID:        uuid.New(),
		Status:    LeadStatusNew,
		LeadType:  LeadTypeBroadband, // Wave 76 default; caller overrides for enterprise
		FullName:  fullName,
		Phone:     phone,
		Address:   address,
		Source:    LeadSourceManual,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}, nil
}

// LeadNumber generator: "LD-YYYYMMDD-XXXX".
func GenerateLeadNumber(t time.Time) string {
	return "LD-" + t.UTC().Format("20060102") + "-" + uuid.New().String()[:8]
}

// ApplyCoverage records a coverage decision against the lead.
//
// Wave 75 (QA TC-CRM-013): coverage NEVER mutates Status. A freshly
// created lead always stays in `new` until the sales rep explicitly
// progresses it. The excess_distance verdict used to auto-flip to
// `potential`, which QA correctly flagged as confusing — the rep
// had no chance to triage. Now the verdict is stored alongside an
// `AcceptExcessCable` flag, and `MarkPotential` is a separate explicit
// transition the rep takes once they've decided to chase the deal.
//
// Uncovered cases also leave the status at `New` so sales decides
// whether to chase, drop, or document.
func (l *Lead) ApplyCoverage(
	verdict CoverageVerdict,
	snapshot []byte,
	nearestNode *uuid.UUID,
	cableDistanceM, excessCharge *float64,
	branch *uuid.UUID,
	acceptExcess bool,
) {
	l.CoverageVerdict = &verdict
	l.CoverageSnapshot = snapshot
	l.NearestNodeID = nearestNode
	l.CableDistanceM = cableDistanceM
	l.ExcessCharge = excessCharge
	l.BranchID = branch
	l.AcceptExcessCable = acceptExcess
	// Note: Status is intentionally NOT changed here. See doc above.
	l.UpdatedAt = time.Now().UTC()
}

// validLeadTransitions encodes the forward-only pipeline + terminal
// branches per PRD §6.3 (and QA TC-CRM-013 acceptance criteria).
//
// Allowed moves out of every status:
//
//	new       → active, warm, hot, lost, potential
//	active    → warm, hot, lost, potential
//	warm      → hot, lost, potential
//	hot       → converted, lost, potential
//	potential → hot (rep got more info), lost
//	converted → (terminal — only conversion side-effects may touch)
//	lost      → (terminal)
//
// Back-compat: the deprecated `qualified` / `rejected` values (from
// pre-0023 rows) map equivalently to `active` / `lost` for transition
// purposes. They cannot be the target of a transition (no new writes).
var validLeadTransitions = map[LeadStatus]map[LeadStatus]bool{
	LeadStatusNew: {
		LeadStatusActive:    true,
		LeadStatusWarm:      true,
		LeadStatusHot:       true,
		LeadStatusLost:      true,
		LeadStatusPotential: true,
	},
	LeadStatusActive: {
		LeadStatusWarm:      true,
		LeadStatusHot:       true,
		LeadStatusLost:      true,
		LeadStatusPotential: true,
	},
	LeadStatusWarm: {
		LeadStatusHot:       true,
		LeadStatusLost:      true,
		LeadStatusPotential: true,
	},
	LeadStatusHot: {
		LeadStatusConverted: true,
		LeadStatusLost:      true,
		LeadStatusPotential: true,
	},
	LeadStatusPotential: {
		LeadStatusHot:  true,
		LeadStatusLost: true,
	},
	LeadStatusConverted: {},
	LeadStatusLost:      {},
	// Pre-0023 back-compat — read-only mapping.
	LeadStatusQualified: {
		LeadStatusHot:       true,
		LeadStatusConverted: true,
		LeadStatusLost:      true,
	},
	LeadStatusRejected: {},
}

// CanTransitionTo returns nil if the requested status is reachable from
// the current one in a single forward step. Returns a Conflict error
// otherwise — same-status writes (idempotent re-set) return nil because
// the caller may legitimately PATCH with the current status.
func (l *Lead) CanTransitionTo(target LeadStatus) error {
	if l.Status == target {
		return nil
	}
	allowed, ok := validLeadTransitions[l.Status]
	if !ok || !allowed[target] {
		return errors.Conflict(
			"lead.invalid_transition",
			"cannot move lead from "+string(l.Status)+" to "+string(target),
		)
	}
	return nil
}

// MarkPotential is the explicit transition the rep takes when the lead
// is outside standard coverage (excess_distance) and the customer has
// signaled willingness to pay the excess cable charge. Per QA
// TC-CRM-013, this is an explicit user action — it does not happen
// automatically from `ApplyCoverage`. The rep must be looking at the
// excess verdict, click Mark Potential, and confirm.
func (l *Lead) MarkPotential() error {
	if err := l.CanTransitionTo(LeadStatusPotential); err != nil {
		return err
	}
	l.Status = LeadStatusPotential
	l.UpdatedAt = time.Now().UTC()
	return nil
}

// CanConvert returns nil if the lead is in a status that allows conversion.
//
// Pipeline-style: `hot` is the canonical "ready to close" state per
// PRD §6.3. `potential` (excess accepted) and the legacy `qualified`
// remain convertible for back-compat. `active` and `warm` aren't yet
// far enough along — the rep should push the lead to `hot` first.
// Already-converted is a no-op error so we don't double-create
// customer records.
func (l *Lead) CanConvert() error {
	if l.Status == LeadStatusConverted {
		return errors.Conflict("lead.already_converted", "lead already converted")
	}
	switch l.Status {
	case LeadStatusHot, LeadStatusPotential, LeadStatusQualified:
		// allowed
	default:
		return errors.Conflict("lead.not_convertible",
			"lead must be hot, potential, or qualified to convert")
	}
	return nil
}
