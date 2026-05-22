package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// CustomerType per PRD; Phase 1 only flows broadband, the rest are inert.
type CustomerType string

const (
	CustomerTypeBroadband  CustomerType = "broadband"
	CustomerTypeBusiness   CustomerType = "business"
	CustomerTypeEnterprise CustomerType = "enterprise"
	CustomerTypeCorporate  CustomerType = "corporate"
)

// CustomerStatus lifecycle.
//
//	pending_install — created at conversion; waiting on WO completion
//	active          — installed and on permanent radius
//	suspended       — non-payment or maintenance
//	terminated      — final state; archived
type CustomerStatus string

const (
	CustomerStatusPendingInstall CustomerStatus = "pending_install"
	CustomerStatusActive         CustomerStatus = "active"
	CustomerStatusSuspended      CustomerStatus = "suspended"
	CustomerStatusTerminated     CustomerStatus = "terminated"
)

type Customer struct {
	ID                 uuid.UUID
	CustomerNumber     string
	CustomerType       CustomerType
	FullName           string
	Phone              string
	Email              string
	NIK                string
	Address            string
	GPSLat             *float64
	GPSLng             *float64
	BranchID           *uuid.UUID
	InstallationNodeID *uuid.UUID
	Status             CustomerStatus
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

func GenerateCustomerNumber(t time.Time) string {
	return "CUST-" + t.UTC().Format("20060102") + "-" + uuid.New().String()[:8]
}

// NewBroadbandCustomer is the constructor used during lead conversion.
func NewBroadbandCustomer(fullName, phone, address string) (*Customer, error) {
	fullName = strings.TrimSpace(fullName)
	phone = strings.TrimSpace(phone)
	address = strings.TrimSpace(address)
	if fullName == "" {
		return nil, errors.Validation("customer.name_required", "full_name is required")
	}
	if phone == "" {
		return nil, errors.Validation("customer.phone_required", "phone is required")
	}
	if address == "" {
		return nil, errors.Validation("customer.address_required", "address is required")
	}
	return &Customer{
		ID:             uuid.New(),
		CustomerNumber: GenerateCustomerNumber(time.Now()),
		CustomerType:   CustomerTypeBroadband,
		FullName:       fullName,
		Phone:          phone,
		Address:        address,
		Status:         CustomerStatusPendingInstall,
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}, nil
}
