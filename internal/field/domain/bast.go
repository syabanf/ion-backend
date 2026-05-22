package domain

import (
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

type SignOffMode string

const (
	SignOffOnSite SignOffMode = "on_site"
	SignOffRemote SignOffMode = "remote"
)

type NOCStatus string

const (
	NOCStatusPending  NOCStatus = "pending"
	NOCStatusApproved NOCStatus = "approved"
	NOCStatusRejected NOCStatus = "rejected"
)

// BAST (Berita Acara Serah Terima) — immutable on submit per PRD:
// rejection creates a NEW row; the old row stays forever as history.
//
// The `compiled_data` jsonb is the snapshot of everything that fed the
// signoff: customer info, checklist items, resolution log, technician.
// We don't compute it from live tables at read time — once signed, the
// content is what was on screen at sign-off.
type BAST struct {
	ID               uuid.UUID
	WOID             uuid.UUID
	CustomerID       uuid.UUID
	CompiledData     []byte // jsonb opaque to domain
	SignOffMode      SignOffMode
	CustomerSigURL   string
	OTPUsed          bool
	// M5 r2 — hashed OTP for remote sign-off + verification stamp.
	// OTPCode holds the bcrypt hash (empty for on_site BASTs); the
	// plain 6-digit code is returned exactly once from SubmitBAST and
	// delivered to the customer out-of-band (round-3: WhatsApp/SMS).
	OTPCode          string
	OTPVerifiedAt    *time.Time
	// OTPPlaintextOnce is not persisted — set transiently by SubmitBAST
	// so the HTTP layer can surface the 6-digit code in the create
	// response. Read once, then drop.
	OTPPlaintextOnce string `json:"-"`
	SignOffAt        time.Time
	SignOffGPSLat    *float64
	SignOffGPSLng    *float64
	SubmittedBy      *uuid.UUID
	SubmittedAt      time.Time
	NOCStatus        NOCStatus
	NOCVerifiedBy    *uuid.UUID
	NOCVerifiedAt    *time.Time
	NOCNotes         string
}

// NewBAST creates a pending BAST. The actual write is in the service —
// we just stamp defaults and enforce the on_site/remote check.
func NewBAST(woID, customerID uuid.UUID, mode SignOffMode, compiled []byte) (*BAST, error) {
	if woID == uuid.Nil {
		return nil, errors.Validation("bast.wo_required", "wo_id is required")
	}
	if customerID == uuid.Nil {
		return nil, errors.Validation("bast.customer_required", "customer_id is required")
	}
	if mode != SignOffOnSite && mode != SignOffRemote {
		return nil, errors.Validation("bast.signoff_invalid", "sign_off_mode must be on_site or remote")
	}
	if len(compiled) == 0 {
		compiled = []byte("{}")
	}
	now := time.Now().UTC()
	return &BAST{
		ID:           uuid.New(),
		WOID:         woID,
		CustomerID:   customerID,
		CompiledData: compiled,
		SignOffMode:  mode,
		SignOffAt:    now,
		SubmittedAt:  now,
		NOCStatus:    NOCStatusPending,
	}, nil
}

// CanVerify ensures only pending BASTs can be acted on by NOC.
func (b *BAST) CanVerify() error {
	if b.NOCStatus != NOCStatusPending {
		return errors.Conflict("bast.not_pending",
			"BAST already "+string(b.NOCStatus)+"; cannot re-verify")
	}
	return nil
}
