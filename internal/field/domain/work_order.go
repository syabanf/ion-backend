// Package domain holds the Field bounded context's entities + invariants.
package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// WOType — Phase 1 supports new_installation + termination from the order
// flow, plus maintenance (rare in P1; appears once tickets exist).
type WOType string

const (
	WOTypeNewInstallation WOType = "new_installation"
	WOTypeMaintenance     WOType = "maintenance"
	WOTypeTermination     WOType = "termination"
)

// WOStatus — lifecycle states. The state machine is enforced by the service
// layer; transitions outside the allowed graph are rejected.
//
//	created            — record exists; nothing scheduled
//	unassigned         — routed to a team/leader; awaiting tech pair
//	assigned           — tech pair set
//	dispatched         — materials picked up from warehouse (M3 cross-cut, R2)
//	in_progress        — technician on site
//	pending_noc_verification — BAST submitted; awaiting NOC + payment gate
//	completed          — NOC approved; M2 promotes RADIUS to permanent
//	rescheduled        — moved to a new date (audit row in wo_reschedules)
//	cancelled          — terminal; no further work
type WOStatus string

const (
	WOStatusCreated                WOStatus = "created"
	WOStatusUnassigned             WOStatus = "unassigned"
	WOStatusAssigned               WOStatus = "assigned"
	WOStatusDispatched             WOStatus = "dispatched"
	WOStatusInProgress             WOStatus = "in_progress"
	WOStatusPendingNOCVerification WOStatus = "pending_noc_verification"
	WOStatusCompleted              WOStatus = "completed"
	WOStatusRescheduled            WOStatus = "rescheduled"
	WOStatusCancelled              WOStatus = "cancelled"
)

// Valid mirrors the DB CHECK constraint on field.work_orders.status so
// HTTP handlers can return a clean 400 instead of letting an invalid
// string flow through to the DB and surface as a 500.
func (s WOStatus) Valid() bool {
	switch s {
	case WOStatusCreated, WOStatusUnassigned, WOStatusAssigned,
		WOStatusDispatched, WOStatusInProgress,
		WOStatusPendingNOCVerification, WOStatusCompleted,
		WOStatusRescheduled, WOStatusCancelled:
		return true
	}
	return false
}

type Priority string

const (
	PriorityHigh   Priority = "high"
	PriorityMedium Priority = "medium"
	PriorityLow    Priority = "low"
)

// Valid mirrors the DB CHECK constraint on field.work_orders.priority.
// Empty string is treated as "not provided" (caller defaults it); use
// Valid() only when the caller explicitly passes a value.
func (p Priority) Valid() bool {
	switch p {
	case PriorityHigh, PriorityMedium, PriorityLow:
		return true
	}
	return false
}

// Valid mirrors the DB CHECK on field.work_orders.wo_type.
func (t WOType) Valid() bool {
	switch t {
	case WOTypeNewInstallation, WOTypeMaintenance, WOTypeTermination:
		return true
	}
	return false
}

// WorkOrder is the operational anchor. Other bounded contexts touch it:
//   - M6 invoice references order_id; pays before NOC verifies BAST
//   - M2 promotes RADIUS to permanent on NOC approval
//   - M3 dispatches assets at warehouse intake (round-2)
type WorkOrder struct {
	ID                 uuid.UUID
	WONumber           string
	OrderID            *uuid.UUID
	CustomerID         uuid.UUID
	WOType             WOType
	ProductType        string
	MaintenanceSubtype string
	Address            string
	BranchID           *uuid.UUID
	Priority           Priority
	Status             WOStatus
	ScheduledDate      *time.Time
	SLADueAt           *time.Time // M5 r2 — drives the SLA-breach queue
	TeamID             *uuid.UUID
	TeamLeaderID       *uuid.UUID
	IsEmergency        bool
	IsCrossArea        bool
	Notes              string
	CreatedBy          *uuid.UUID
	CreatedAt          time.Time
	UpdatedAt          time.Time

	// Wave 84 (TC-WO-011) — per-customer product + pinned service
	// schema. Both nullable so legacy WOs continue to load; new WOs
	// created from an order stamp both via the CRM projection.
	// ProductID is the customer's product at install time; if the
	// product later mutates its slot, the WO keeps the original
	// snapshot via ServiceSchemaID.
	ProductID       *uuid.UUID
	ServiceSchemaID *uuid.UUID

	// Wave 132 — customer-segment snapshot driving the tech-app
	// broadband/enterprise badge. Stamped at WO creation from the
	// linked customer's customer_type and never mutated after, so a
	// segment change on the customer doesn't retroactively re-label
	// in-flight WOs. Must be 'broadband' or 'enterprise'; the DB
	// CHECK constraint enforces.
	Category WOCategory
}

// WOCategory is the customer-segment classification stamped on each WO
// at creation time. Two values:
//
//	broadband  — residential / SMB broadband installs + maintenance
//	enterprise — business / enterprise / corporate per CRM customer_type
//
// Anything that doesn't map cleanly defaults to broadband.
type WOCategory string

const (
	WOCategoryBroadband  WOCategory = "broadband"
	WOCategoryEnterprise WOCategory = "enterprise"
)

func (c WOCategory) Valid() bool {
	return c == WOCategoryBroadband || c == WOCategoryEnterprise
}

// NormalizeCategory accepts a free-form category string (typically from
// crm.customers.customer_type) and returns the canonical WOCategory.
// Unknown values fall back to broadband — the Phase 1 default.
func NormalizeCategory(raw string) WOCategory {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "enterprise", "business", "corporate":
		return WOCategoryEnterprise
	default:
		return WOCategoryBroadband
	}
}

// NewInstallationWO builds a WO from an order. The service supplies
// customer_id + address + branch_id from the order projection.
//
// Wave 132 — the caller passes the customer's category (derived from
// customer_type via NormalizeCategory) so the WO row carries its own
// broadband/enterprise snapshot. Defaults to broadband if zero-value.
func NewInstallationWO(orderID *uuid.UUID, customerID uuid.UUID, address string, category WOCategory) (*WorkOrder, error) {
	address = strings.TrimSpace(address)
	if customerID == uuid.Nil {
		return nil, errors.Validation("wo.customer_required", "customer_id is required")
	}
	if address == "" {
		return nil, errors.Validation("wo.address_required", "address is required")
	}
	if !category.Valid() {
		category = WOCategoryBroadband
	}
	return &WorkOrder{
		ID:          uuid.New(),
		WONumber:    GenerateWONumber(time.Now()),
		OrderID:     orderID,
		CustomerID:  customerID,
		WOType:      WOTypeNewInstallation,
		ProductType: "broadband",
		Category:    category,
		Address:     address,
		Priority:    PriorityMedium,
		Status:      WOStatusCreated,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}, nil
}

// NewTerminationWO builds a termination WO. Used by both voluntary and
// auto-suspension-driven flows. Same shape as installation; the checklist
// template lookup uses (termination, broadband, "") to find the right
// checklist (device-retrieval BAST).
//
// Wave 132 — category snapshot like NewInstallationWO. Termination WOs
// inherit the segment that the customer had at termination time.
func NewTerminationWO(orderID *uuid.UUID, customerID uuid.UUID, address string, category WOCategory) (*WorkOrder, error) {
	address = strings.TrimSpace(address)
	if customerID == uuid.Nil {
		return nil, errors.Validation("wo.customer_required", "customer_id is required")
	}
	if address == "" {
		return nil, errors.Validation("wo.address_required", "address is required")
	}
	if !category.Valid() {
		category = WOCategoryBroadband
	}
	return &WorkOrder{
		ID:          uuid.New(),
		WONumber:    GenerateWONumber(time.Now()),
		OrderID:     orderID,
		CustomerID:  customerID,
		WOType:      WOTypeTermination,
		ProductType: "broadband",
		Category:    category,
		Address:     address,
		Priority:    PriorityMedium,
		Status:      WOStatusCreated,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}, nil
}

func GenerateWONumber(t time.Time) string {
	return "WO-" + t.UTC().Format("20060102") + "-" + uuid.New().String()[:8]
}

// validTransitions maps every allowed status edge. Anything not here is
// rejected by AssertCanTransition.
//
// Edges intentionally NOT included:
//   - completed → anything (terminal)
//   - cancelled → anything (terminal)
//   - any → created (created is only the constructor's initial state)
var validTransitions = map[WOStatus]map[WOStatus]bool{
	WOStatusCreated:    {WOStatusUnassigned: true, WOStatusCancelled: true},
	WOStatusUnassigned: {WOStatusAssigned: true, WOStatusCancelled: true, WOStatusRescheduled: true},
	WOStatusAssigned:   {WOStatusDispatched: true, WOStatusInProgress: true, WOStatusUnassigned: true, WOStatusCancelled: true, WOStatusRescheduled: true},
	WOStatusDispatched: {WOStatusInProgress: true, WOStatusCancelled: true, WOStatusRescheduled: true},
	WOStatusInProgress: {WOStatusPendingNOCVerification: true, WOStatusRescheduled: true, WOStatusCancelled: true},
	WOStatusPendingNOCVerification: {
		WOStatusCompleted:  true, // NOC approved
		WOStatusInProgress: true, // NOC rejected → tech retries
	},
	WOStatusRescheduled: {WOStatusAssigned: true, WOStatusUnassigned: true, WOStatusCancelled: true},
}

// AssertCanTransition checks the state machine. The service uses this
// before any status-mutating action so callers see a clean conflict
// error instead of a half-applied update.
func (w *WorkOrder) AssertCanTransition(to WOStatus) error {
	if w.Status == to {
		return errors.Conflict("wo.status_unchanged", "wo already in status "+string(to))
	}
	if validTransitions[w.Status] == nil || !validTransitions[w.Status][to] {
		return errors.Conflict("wo.invalid_transition",
			"cannot move wo from "+string(w.Status)+" to "+string(to))
	}
	return nil
}
