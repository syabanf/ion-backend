package port

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// =====================================================================
// M6 r3 — customer-portal OTP for self-service termination.
//
// Authentication model: no JWT, no customer account. The customer
// supplies (customer_number, phone), we mint a 6-digit OTP keyed to the
// customer_id, hash + persist, and dispatch out-of-band. The customer
// then submits (customer_number, otp, reason) and we verify against
// the hash before invoking the standard RequestVoluntaryTermination.
//
// This is intentionally narrow — only termination today. Other portal
// surfaces (address change, plan switch) can grow off the same primitive.
// =====================================================================

// CustomerOTPRecord mirrors crm.customer_portal_otp.
type CustomerOTPRecord struct {
	ID         uuid.UUID
	CustomerID uuid.UUID
	Purpose    string
	OTPHash    string
	Attempts   int
	VerifiedAt *time.Time
	ExpiresAt  time.Time
	CreatedAt  time.Time
}

// CustomerOTPRepository is the driven port for the OTP table.
type CustomerOTPRepository interface {
	Create(ctx context.Context, rec *CustomerOTPRecord) error
	FindActive(ctx context.Context, customerID uuid.UUID, purpose string) (*CustomerOTPRecord, error)
	MarkVerified(ctx context.Context, id uuid.UUID) error
	IncrementAttempts(ctx context.Context, id uuid.UUID) error
	DeleteExpired(ctx context.Context, before time.Time) (int, error)
	// CountRequestsSince counts OTP rows created at or after the given
	// instant for (customer, purpose). Drives the per-customer
	// rate-limit predicate.
	CountRequestsSince(ctx context.Context, customerID uuid.UUID, purpose string, since time.Time) (int, error)
}

// CustomerLookupGateway lets the portal flow resolve a customer by the
// pair the user types on the request leg, and by number alone on the
// confirm leg (after the OTP gate has already proved phone possession).
type CustomerLookupGateway interface {
	FindCustomerByNumberAndPhone(ctx context.Context, customerNumber, phone string) (uuid.UUID, error)
	FindCustomerByNumber(ctx context.Context, customerNumber string) (uuid.UUID, error)
}

// =====================================================================
// Portal-facing UseCase inputs.
// =====================================================================

type PortalRequestTerminationOTPInput struct {
	CustomerNumber string
	Phone          string
}

// PortalRequestTerminationOTPOutput intentionally has no OTP plaintext
// in the production response — the OTP is delivered out-of-band. The
// dev/testing knob `IncludeDevOTP` (controlled via cmd-wiring) sets
// `DevOTP` to the plaintext so local dev + e2e tests don't need a real
// WhatsApp gateway.
type PortalRequestTerminationOTPOutput struct {
	CustomerID uuid.UUID
	ExpiresAt  time.Time
	// DevOTP is populated only when the service is started with
	// IncludeDevOTP=true. Empty in production.
	DevOTP string
}

type PortalConfirmTerminationInput struct {
	CustomerNumber string
	OTP            string
	Reason         string
}