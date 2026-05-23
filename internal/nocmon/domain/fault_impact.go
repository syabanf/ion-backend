package domain

import (
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// ImpactKind mirrors the DB CHECK enum on fault_impact_links.
type ImpactKind string

const (
	ImpactKindFullOutage   ImpactKind = "full_outage"
	ImpactKindDegraded     ImpactKind = "degraded"
	ImpactKindIntermittent ImpactKind = "intermittent"
	ImpactKindUnknown      ImpactKind = "unknown"
)

func (k ImpactKind) Valid() bool {
	switch k {
	case ImpactKindFullOutage, ImpactKindDegraded, ImpactKindIntermittent, ImpactKindUnknown:
		return true
	}
	return false
}

// FaultImpact is one (fault, customer) join row. The cascade
// traversal (TC-FIA-001/002/003) materializes one of these per
// downstream customer; the unique (fault_event_id, customer_id)
// makes the cascade idempotent across reruns.
type FaultImpact struct {
	ID                uuid.UUID
	FaultEventID      uuid.UUID
	CustomerID        uuid.UUID
	ImpactKind        ImpactKind
	ImpactStart       *time.Time
	ImpactEnd         *time.Time
	SLACreditEligible bool
	NotifiedAt        *time.Time
}

// NewFaultImpact validates a link row. The slaCreditEligible flag is
// computed by ComputeSLACreditEligible before this constructor
// returns so the call site is the only place that owns the SLA
// policy.
func NewFaultImpact(faultID, customerID uuid.UUID, kind ImpactKind, start time.Time, slaEligible bool) (*FaultImpact, error) {
	if faultID == uuid.Nil {
		return nil, errors.Validation("impact.fault_required", "fault_event_id is required")
	}
	if customerID == uuid.Nil {
		return nil, errors.Validation("impact.customer_required", "customer_id is required")
	}
	if !kind.Valid() {
		return nil, errors.Validation("impact.kind_invalid", "impact_kind must be full_outage/degraded/intermittent/unknown")
	}
	startUTC := start.UTC()
	return &FaultImpact{
		ID:                uuid.New(),
		FaultEventID:      faultID,
		CustomerID:        customerID,
		ImpactKind:        kind,
		ImpactStart:       &startUTC,
		SLACreditEligible: slaEligible,
	}, nil
}

// ComputeSLACreditEligible decides whether the impact window
// qualifies for an SLA credit. The current policy is "an outage
// that exceeds slaWindow hours is eligible"; the broadband product
// uses slaWindow = 4 hours (PRD §SLA). Pass slaWindow ≤ 0 to disable
// (returns false unconditionally).
//
// Kept as a free function rather than a method so callers can
// compute the flag *before* constructing the FaultImpact — the
// constructor takes it as input and never recomputes (single source
// of truth at the call site).
func ComputeSLACreditEligible(impactDurationHours float64, slaWindowHours float64) bool {
	if slaWindowHours <= 0 {
		return false
	}
	return impactDurationHours >= slaWindowHours
}
