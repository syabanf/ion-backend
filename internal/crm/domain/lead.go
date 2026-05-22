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
type LeadSource string

const (
	LeadSourceManual    LeadSource = "manual"
	LeadSourceSelfOrder LeadSource = "self_order"
	LeadSourceSalesApp  LeadSource = "sales_app"
	LeadSourceReferral  LeadSource = "referral"
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
// the chosen product/branch.
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
// As of PRD §6.3 alignment (migration 0023, QA-flagged via TC-CRM-013):
// the coverage check no longer mutates `Status` directly. Coverage is
// stamped as a separate verdict field — the sales rep then progresses
// the pipeline themselves (New → Active → Warm → Hot → Converted).
//
// Exception — excess_distance (cable > 210m, PRD §6.1 line 915): the
// PRD labels this terminal-ish coverage state "Potential". Per a
// follow-up clarification (broadband happy path Gap A), every
// excess-distance lead transitions to Potential — regardless of
// whether the customer pre-accepted the excess cable charge — because
// the lead is materially blocked on a build/dispatch decision until
// sales explicitly chases or drops it. The `AcceptExcessCable` flag
// still gates conversion, but it does not change the pipeline state.
//
// Uncovered cases leave the status at `New` so sales decides whether
// to chase, drop, or document.
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

	// PRD §6.1 line 915 — excess_distance always maps to Potential.
	if verdict == CoverageVerdictExcess {
		l.Status = LeadStatusPotential
	}
	l.UpdatedAt = time.Now().UTC()
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
