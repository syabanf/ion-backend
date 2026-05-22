package domain

import (
	"time"

	"github.com/google/uuid"
)

// OrderStatus lifecycle.
//
//	created      — order open; no WO yet
//	wo_assigned  — M5 created a Work Order
//	installed    — technician finished install, BAST submitted
//	active       — NOC verified; customer on permanent radius (M6 ends here)
//	cancelled    — admin/finance cancellation before activation
type OrderStatus string

const (
	OrderStatusCreated    OrderStatus = "created"
	OrderStatusWOAssigned OrderStatus = "wo_assigned"
	OrderStatusInstalled  OrderStatus = "installed"
	OrderStatusActive     OrderStatus = "active"
	OrderStatusCancelled  OrderStatus = "cancelled"
)

// OTCType chooses how the one-time charge is invoiced. PRD §6.4
// (broadband happy-path Gap B) recognises three modes:
//
//	free      — no invoice, contract marked contracted on conversion
//	prepaid   — invoice issued eagerly; activation gated on payment
//	postpaid  — invoice deferred to the activation hook (default)
//
// We keep `postpaid` as the zero-value default so legacy callers
// (and rows persisted before migration 0034) preserve their existing
// behaviour.
type OTCType string

const (
	OTCTypeFree     OTCType = "free"
	OTCTypePrepaid  OTCType = "prepaid"
	OTCTypePostpaid OTCType = "postpaid"
)

// Order represents the contract instance created when a lead converts.
//
// Prices are snapshot at order time: if the product's monthly_price later
// changes, the order continues to bill at the captured value.
type Order struct {
	ID                uuid.UUID
	OrderNumber       string
	LeadID            *uuid.UUID
	CustomerID        uuid.UUID
	ProductID         *uuid.UUID
	MonthlyPrice      float64
	OTCPrice          float64
	ExcessCharge      float64
	AcceptExcessCable bool
	NearestNodeID     *uuid.UUID
	BranchID          *uuid.UUID
	SalesID           *uuid.UUID
	Status            OrderStatus
	OTCType           OTCType
	Notes             string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

func GenerateOrderNumber(t time.Time) string {
	return "ORD-" + t.UTC().Format("20060102") + "-" + uuid.New().String()[:8]
}
