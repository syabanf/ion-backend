package domain

import (
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// PortRole — what this port does in the topology.
type PortRole string

const (
	PortRolePONDownlink         PortRole = "pon_downlink"        // OLT
	PortRoleDistributionInput   PortRole = "distribution_input"  // ODC upstream
	PortRoleDistributionOutput  PortRole = "distribution_output" // ODC fan-out
	PortRoleCustomerDrop        PortRole = "customer_drop"       // ODP customer port
	PortRoleUplink              PortRole = "uplink"              // generic uplink
	PortRoleGeneric             PortRole = "generic"
)

func (r PortRole) Valid() bool {
	switch r {
	case PortRolePONDownlink, PortRoleDistributionInput, PortRoleDistributionOutput,
		PortRoleCustomerDrop, PortRoleUplink, PortRoleGeneric:
		return true
	}
	return false
}

// PortStatus mirrors the CHECK on network.ports.status.
type PortStatus string

const (
	PortStatusAvailable PortStatus = "available"
	PortStatusReserved  PortStatus = "reserved"
	PortStatusActive    PortStatus = "active"
	PortStatusFaulty    PortStatus = "faulty"
)

func (s PortStatus) Valid() bool {
	switch s {
	case PortStatusAvailable, PortStatusReserved, PortStatusActive, PortStatusFaulty:
		return true
	}
	return false
}

// Port — one port on a node. Reservation is a state machine:
//
//   Available → Reserved (at onboarding)
//   Reserved  → Active   (after NOC verifies installation)
//   Active    → Available (after termination)
//   any       → Faulty   (NOC/technician report)
//   Faulty    → Available (after repair)
//
// Reservation has a timeout (reserved_until). If installation doesn't
// complete in time, a background sweeper releases the port back to Available.
type Port struct {
	ID                uuid.UUID
	NodeID            uuid.UUID
	PortNumber        int
	Role              PortRole
	MaxCapacity       int
	ActiveConnections int
	Status            PortStatus
	CustomerID        *uuid.UUID
	ReservedFor       *uuid.UUID
	ReservedUntil     *time.Time
	ActivatedAt       *time.Time
	CreatedAt         time.Time
}

// NewPort constructs a port. Defaults to Available with capacity=1.
func NewPort(nodeID uuid.UUID, portNumber int, role PortRole) (*Port, error) {
	if nodeID == uuid.Nil {
		return nil, errors.Validation("port.node_required", "node_id is required")
	}
	if portNumber < 1 {
		return nil, errors.Validation("port.number_invalid", "port_number must be >= 1")
	}
	if !role.Valid() {
		return nil, errors.Validation("port.role_invalid", "invalid port_role")
	}
	return &Port{
		ID:          uuid.New(),
		NodeID:      nodeID,
		PortNumber:  portNumber,
		Role:        role,
		MaxCapacity: 1,
		Status:      PortStatusAvailable,
		CreatedAt:   time.Now().UTC(),
	}, nil
}

// Reserve transitions Available → Reserved with a hold expiry.
func (p *Port) Reserve(forCustomer uuid.UUID, until time.Time) error {
	if p.Status != PortStatusAvailable {
		return errors.Conflict("port.not_available", "port is not available")
	}
	p.Status = PortStatusReserved
	p.ReservedFor = &forCustomer
	p.ReservedUntil = &until
	return nil
}

// Activate transitions Reserved (or Available, for emergency direct-attach)
// → Active and links the port to a customer.
func (p *Port) Activate(customerID uuid.UUID) error {
	if p.Status != PortStatusReserved && p.Status != PortStatusAvailable {
		return errors.Conflict("port.cannot_activate", "port is not reservable")
	}
	now := time.Now().UTC()
	p.Status = PortStatusActive
	p.CustomerID = &customerID
	p.ReservedFor = nil
	p.ReservedUntil = nil
	p.ActivatedAt = &now
	p.ActiveConnections = 1
	return nil
}

// Release returns a port to Available (after termination, expired reservation,
// or repair). Caller decides when this is appropriate.
func (p *Port) Release() {
	p.Status = PortStatusAvailable
	p.CustomerID = nil
	p.ReservedFor = nil
	p.ReservedUntil = nil
	p.ActivatedAt = nil
	p.ActiveConnections = 0
}
