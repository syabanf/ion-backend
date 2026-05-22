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

	// Wave 78 (TC-SCH-011/015/023/026, TC-PRD-025): version lock.
	// At conversion the resolver snapshots the version it picked for
	// each of the 5 kinds and pins them here. The resolver prefers
	// these locked versions over `FindLatestPublished` on subsequent
	// reads, so publishing a new schema version doesn't silently
	// re-rate existing customers.
	//
	// All nullable so legacy customers (created before Wave 78) fall
	// through to the existing resolver path until explicitly migrated.
	LockedOnboardingSchemaVersionID  *uuid.UUID
	LockedBillingSchemaVersionID     *uuid.UUID
	LockedServiceSchemaVersionID     *uuid.UUID
	LockedCommissionSchemaVersionID  *uuid.UUID
	LockedSuspensionSchemaVersionID  *uuid.UUID
}

// LockedSchemaVersionID returns the FK column matching the given
// schema kind (one of: onboarding, billing, service, commission,
// suspension). Returns nil for unknown kinds or unlocked rows.
//
// Used by the resolver — see internal/platform/usecase.Service
// — to short-circuit `FindLatestPublished` when a customer has
// been pinned to a specific version.
func (c *Customer) LockedSchemaVersionID(kind string) *uuid.UUID {
	switch kind {
	case "onboarding":
		return c.LockedOnboardingSchemaVersionID
	case "billing":
		return c.LockedBillingSchemaVersionID
	case "service":
		return c.LockedServiceSchemaVersionID
	case "commission":
		return c.LockedCommissionSchemaVersionID
	case "suspension":
		return c.LockedSuspensionSchemaVersionID
	}
	return nil
}

// SetLockedSchemaVersion pins a specific schema version for this
// customer + kind. Mirror of `Product.SetSchemaSlot` so the conversion
// path can loop over `domain.SchemaSlots` without per-kind switches.
func (c *Customer) SetLockedSchemaVersion(kind string, versionID *uuid.UUID) error {
	switch kind {
	case "onboarding":
		c.LockedOnboardingSchemaVersionID = versionID
	case "billing":
		c.LockedBillingSchemaVersionID = versionID
	case "service":
		c.LockedServiceSchemaVersionID = versionID
	case "commission":
		c.LockedCommissionSchemaVersionID = versionID
	case "suspension":
		c.LockedSuspensionSchemaVersionID = versionID
	default:
		return errors.Validation("customer.schema_kind_invalid",
			"schema kind '"+kind+"' must be one of: onboarding, billing, service, commission, suspension")
	}
	return nil
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
