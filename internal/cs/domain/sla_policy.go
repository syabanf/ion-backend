package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// CustomerType — Wave 124's per-customer-type SLA dimension.
//
// Distinct from crm.customers.customer_type (which is broadband / business
// / enterprise / corporate per migration 0007). The bridge in
// cmd/cs-svc/main.go::customerTypeResolverBridge maps the CRM value
// into this domain enum so the matrix can stay generic.
// =====================================================================

type CustomerType string

const (
	CustomerTypeResidential CustomerType = "residential"
	CustomerTypeBusiness    CustomerType = "business"
	CustomerTypeEnterprise  CustomerType = "enterprise"
	CustomerTypeReseller    CustomerType = "reseller"
	CustomerTypeInternal    CustomerType = "internal"
)

func (c CustomerType) Valid() bool {
	switch c {
	case CustomerTypeResidential, CustomerTypeBusiness, CustomerTypeEnterprise,
		CustomerTypeReseller, CustomerTypeInternal:
		return true
	}
	return false
}

// =====================================================================
// SLAMatrixEntry — one row of cs.sla_matrix.
// =====================================================================

type SLAMatrixEntry struct {
	ID                   uuid.UUID
	CustomerType         CustomerType
	TicketType           TicketType
	Priority             Priority
	FirstResponseMinutes int
	ResolveMinutes       int
	BreachWarnPct        float64
	EscalationLevels     []map[string]any
	IsActive             bool
	EffectiveFrom        time.Time
	EffectiveTo          *time.Time
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// NewSLAMatrixEntry validates the input and stamps timestamps.
func NewSLAMatrixEntry(
	customerType CustomerType,
	ticketType TicketType,
	priority Priority,
	firstResponseMinutes, resolveMinutes int,
	breachWarnPct float64,
	effectiveFrom time.Time,
) (*SLAMatrixEntry, error) {
	if !customerType.Valid() {
		return nil, errors.Validation("cs.sla.customer_type_invalid", "customer_type is not recognized")
	}
	if !ticketType.Valid() {
		return nil, errors.Validation("cs.sla.ticket_type_invalid", "ticket_type is not recognized")
	}
	if !priority.Valid() {
		return nil, errors.Validation("cs.sla.priority_invalid", "priority is not recognized")
	}
	if firstResponseMinutes <= 0 {
		return nil, errors.Validation("cs.sla.first_response_invalid", "first_response_minutes must be > 0")
	}
	if resolveMinutes <= 0 {
		return nil, errors.Validation("cs.sla.resolve_invalid", "resolve_minutes must be > 0")
	}
	if breachWarnPct <= 0 || breachWarnPct >= 1 {
		breachWarnPct = 0.80
	}
	now := time.Now().UTC()
	return &SLAMatrixEntry{
		ID:                   uuid.New(),
		CustomerType:         customerType,
		TicketType:           ticketType,
		Priority:             priority,
		FirstResponseMinutes: firstResponseMinutes,
		ResolveMinutes:       resolveMinutes,
		BreachWarnPct:        breachWarnPct,
		IsActive:             true,
		EffectiveFrom:        effectiveFrom.UTC(),
		CreatedAt:            now,
		UpdatedAt:            now,
	}, nil
}

// ResolveDueDates computes (first_response_due_at, resolve_due_at) for
// a fresh ticket using the matrix entry. now is typically the ticket's
// CreatedAt — Wave 124's evaluator uses the snapshot at open time.
func (e *SLAMatrixEntry) ResolveDueDates(now time.Time) (time.Time, time.Time) {
	fr := now.Add(time.Duration(e.FirstResponseMinutes) * time.Minute)
	rv := now.Add(time.Duration(e.ResolveMinutes) * time.Minute)
	return fr.UTC(), rv.UTC()
}

// IsBreachedFirstResponse reports whether the ticket's effective age
// exceeds the first-response budget AND no first-response has been
// recorded yet.
func (e *SLAMatrixEntry) IsBreachedFirstResponse(now time.Time, t *Ticket) bool {
	if t == nil || t.FirstResponseAt != nil {
		return false
	}
	budget := time.Duration(e.FirstResponseMinutes) * time.Minute
	return t.EffectiveAge(now) > budget
}

// IsBreachedResolve reports whether the ticket's effective age
// exceeds the resolve budget AND the ticket is not yet resolved.
func (e *SLAMatrixEntry) IsBreachedResolve(now time.Time, t *Ticket) bool {
	if t == nil {
		return false
	}
	if t.Status == TicketStatusResolved || t.Status == TicketStatusClosed {
		return false
	}
	budget := time.Duration(e.ResolveMinutes) * time.Minute
	return t.EffectiveAge(now) > budget
}

// IsInWarnWindow reports whether the ticket has crossed the breach-warn
// threshold (e.g. 80% of resolve budget) but is not yet breached.
// Used by the cron evaluator to dispatch the "approaching breach"
// warning notification exactly once.
func (e *SLAMatrixEntry) IsInWarnWindow(now time.Time, t *Ticket) bool {
	if t == nil {
		return false
	}
	if t.Status == TicketStatusResolved || t.Status == TicketStatusClosed {
		return false
	}
	if t.SLAWarnedAt != nil {
		return false
	}
	budget := time.Duration(e.ResolveMinutes) * time.Minute
	threshold := time.Duration(float64(budget) * e.BreachWarnPct)
	age := t.EffectiveAge(now)
	return age >= threshold && age < budget
}

// MapCRMCustomerType normalizes the CRM-side customer_type enum
// (broadband / business / enterprise / corporate) into the CS-side
// CustomerType (residential / business / enterprise / reseller /
// internal). Unknown values default to residential — the bridge stays
// best-effort so a single bad row doesn't break SLA assignment.
func MapCRMCustomerType(raw string) CustomerType {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "broadband", "":
		return CustomerTypeResidential
	case "business":
		return CustomerTypeBusiness
	case "enterprise", "corporate":
		return CustomerTypeEnterprise
	case "reseller":
		return CustomerTypeReseller
	case "internal":
		return CustomerTypeInternal
	default:
		return CustomerTypeResidential
	}
}
