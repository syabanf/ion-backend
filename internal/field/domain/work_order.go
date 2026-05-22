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

type Priority string

const (
	PriorityHigh   Priority = "high"
	PriorityMedium Priority = "medium"
	PriorityLow    Priority = "low"
)

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
}

// NewInstallationWO builds a WO from an order. The service supplies
// customer_id + address + branch_id from the order projection.
func NewInstallationWO(orderID *uuid.UUID, customerID uuid.UUID, address string) (*WorkOrder, error) {
	address = strings.TrimSpace(address)
	if customerID == uuid.Nil {
		return nil, errors.Validation("wo.customer_required", "customer_id is required")
	}
	if address == "" {
		return nil, errors.Validation("wo.address_required", "address is required")
	}
	return &WorkOrder{
		ID:          uuid.New(),
		WONumber:    GenerateWONumber(time.Now()),
		OrderID:     orderID,
		CustomerID:  customerID,
		WOType:      WOTypeNewInstallation,
		ProductType: "broadband",
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
func NewTerminationWO(orderID *uuid.UUID, customerID uuid.UUID, address string) (*WorkOrder, error) {
	address = strings.TrimSpace(address)
	if customerID == uuid.Nil {
		return nil, errors.Validation("wo.customer_required", "customer_id is required")
	}
	if address == "" {
		return nil, errors.Validation("wo.address_required", "address is required")
	}
	return &WorkOrder{
		ID:          uuid.New(),
		WONumber:    GenerateWONumber(time.Now()),
		OrderID:     orderID,
		CustomerID:  customerID,
		WOType:      WOTypeTermination,
		ProductType: "broadband",
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
