// Wave 117 — offline-first tablet sync for stock opname.
package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// OpnameTabletSyncStatus mirrors the CHECK on
// warehouse.opname_tablet_sessions.sync_status.
//
//	in_progress → synced → reconciled
//	in_progress → failed (retryable; goes back to in_progress)
type OpnameTabletSyncStatus string

const (
	OpnameTabletSyncInProgress OpnameTabletSyncStatus = "in_progress"
	OpnameTabletSyncSynced     OpnameTabletSyncStatus = "synced"
	OpnameTabletSyncFailed     OpnameTabletSyncStatus = "failed"
	OpnameTabletSyncReconciled OpnameTabletSyncStatus = "reconciled"
)

func (s OpnameTabletSyncStatus) Valid() bool {
	switch s {
	case OpnameTabletSyncInProgress, OpnameTabletSyncSynced,
		OpnameTabletSyncFailed, OpnameTabletSyncReconciled:
		return true
	}
	return false
}

// OpnameTabletSession ties an offline-collected payload to the central
// opname session. Idempotent sync via offline_payload_hash UNIQUE.
type OpnameTabletSession struct {
	ID                 uuid.UUID
	OpnameSessionID    uuid.UUID
	DeviceID           string
	TechnicianUserID   uuid.UUID
	StartedAt          time.Time
	CompletedAt        *time.Time
	TotalScans         int
	SyncStatus         OpnameTabletSyncStatus
	OfflinePayloadHash string
	LastSyncedAt       *time.Time
	Notes              string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// NewOpnameTabletSession constructs a session at tablet startup. The
// payload hash arrives later on the first sync submission.
func NewOpnameTabletSession(opnameSessionID, techUserID uuid.UUID, deviceID string) (*OpnameTabletSession, error) {
	if opnameSessionID == uuid.Nil {
		return nil, errors.Validation("opname_tablet.opname_session_required", "opname_session_id is required")
	}
	if techUserID == uuid.Nil {
		return nil, errors.Validation("opname_tablet.tech_required", "technician_user_id is required")
	}
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return nil, errors.Validation("opname_tablet.device_required", "device_id is required")
	}
	now := time.Now().UTC()
	return &OpnameTabletSession{
		ID:               uuid.New(),
		OpnameSessionID:  opnameSessionID,
		DeviceID:         deviceID,
		TechnicianUserID: techUserID,
		StartedAt:        now,
		SyncStatus:       OpnameTabletSyncInProgress,
		CreatedAt:        now,
		UpdatedAt:        now,
	}, nil
}

// MarkSynced flips sync_status to synced after a successful payload
// submit. Sets last_synced_at + payload hash.
func (s *OpnameTabletSession) MarkSynced(payloadHash string, totalScans int) error {
	if s.SyncStatus == OpnameTabletSyncReconciled {
		return errors.Conflict("opname_tablet.already_reconciled",
			"session already reconciled — cannot re-sync")
	}
	now := time.Now().UTC()
	s.SyncStatus = OpnameTabletSyncSynced
	s.OfflinePayloadHash = payloadHash
	s.LastSyncedAt = &now
	s.TotalScans = totalScans
	if s.CompletedAt == nil {
		s.CompletedAt = &now
	}
	s.UpdatedAt = now
	return nil
}

// MarkFailed allows a retry — the tablet will reattempt sync.
func (s *OpnameTabletSession) MarkFailed(reason string) {
	now := time.Now().UTC()
	s.SyncStatus = OpnameTabletSyncFailed
	s.Notes = reason
	s.UpdatedAt = now
}

// MarkReconciled — terminal. The variance has been folded into the
// parent opname session's counts.
func (s *OpnameTabletSession) MarkReconciled() error {
	if s.SyncStatus != OpnameTabletSyncSynced {
		return errors.Conflict("opname_tablet.not_synced",
			"only synced sessions can be reconciled")
	}
	s.SyncStatus = OpnameTabletSyncReconciled
	s.UpdatedAt = time.Now().UTC()
	return nil
}
