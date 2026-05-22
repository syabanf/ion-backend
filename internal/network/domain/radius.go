package domain

import (
	"time"

	"github.com/google/uuid"
)

// RadiusStatus mirrors the CHECK on network.radius_accounts.status.
//
// The lifecycle per PRD:
//
//   TEMPORARY → PERMANENT_ACTIVE   (NOC verifies BAST after invoices paid)
//   PERMANENT_ACTIVE → SUSPENDED   (suspension schema fires)
//   SUSPENDED → PERMANENT_ACTIVE   (restoration after payment)
//   any → DEACTIVATED              (termination)
type RadiusStatus string

const (
	RadiusStatusTemporary       RadiusStatus = "temporary"
	RadiusStatusPermanentActive RadiusStatus = "permanent_active"
	RadiusStatusSuspended       RadiusStatus = "suspended"
	RadiusStatusDeactivated     RadiusStatus = "deactivated"
)

func (s RadiusStatus) Valid() bool {
	switch s {
	case RadiusStatusTemporary, RadiusStatusPermanentActive,
		RadiusStatusSuspended, RadiusStatusDeactivated:
		return true
	}
	return false
}

// RadiusAccount mirrors network.radius_accounts.
//
// Security note (M7 audit, May 2026):
//
//   - `PasswordHash` is a bcrypt hash of the plaintext password — one-
//     way, not reversible. Migration 0019 renamed the column from
//     `password_encrypted` (which was a misnomer) to `password_hash`
//     to reflect reality.
//
//   - The plaintext password is generated client-side (random in
//     `field/adapter/network/activation.go`), hashed at the adapter
//     boundary, and never persisted. No HTTP handler returns this
//     field, and no log statement in pkg/network includes it.
//
//   - We tag PasswordHash with `json:"-"` so any future surface
//     that JSON-encodes the struct can't accidentally serialise it.
//     Round-4 will move the column to a dedicated `radius_credentials`
//     table behind a permission-gated mTLS read endpoint when ION
//     RADIUS deployments need to mint device PPPoE configs at scale.
type RadiusAccount struct {
	ID         uuid.UUID
	CustomerID uuid.UUID
	Username   string
	// PasswordHash holds bcrypt(plaintext). One-way, never returned
	// over the wire (json:"-").
	PasswordHash string `json:"-"`
	VLANID              *int
	BandwidthProfileID  string
	IPAddress           string
	Status              RadiusStatus
	TempActivatedAt     *time.Time
	TempExpiresAt       *time.Time
	PermActivatedAt     *time.Time
	SuspendedAt         *time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// ProvisionInput is what callers pass to RadiusClient.Provision to mint
// a new RADIUS account for a customer at WO-creation time.
//
// WindowHours is the PRD §13 `temporary_activation_window_hours` — how
// long the account stays in TEMPORARY before the janitor sweep
// auto-deactivates it. Zero = system default (72h).
type ProvisionInput struct {
	CustomerID         uuid.UUID
	Username           string
	PasswordPlain      string
	BandwidthProfileID string
	VLANID             *int
	WindowHours        int
}
