// Wave 117 — Opname tablet (offline-first) sync service.
package usecase

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/warehouse/domain"
	"github.com/ion-core/backend/internal/warehouse/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

func (s *Service) WithOpnameTablet(r port.OpnameTabletSessionRepository) *Service {
	s.opnameTabletSessions = r
	return s
}

func errOpnameTabletNotConfigured() error {
	return derrors.Wrap(derrors.KindInternal, "opname_tablet.not_configured",
		"opname tablet session repository is not configured for this service", nil)
}

func (s *Service) StartOpnameTabletSession(ctx context.Context, in port.CreateOpnameTabletSessionInput) (*domain.OpnameTabletSession, error) {
	if s.opnameTabletSessions == nil {
		return nil, errOpnameTabletNotConfigured()
	}
	sess, err := domain.NewOpnameTabletSession(in.OpnameSessionID, in.TechnicianUserID, in.DeviceID)
	if err != nil {
		return nil, err
	}
	if err := s.opnameTabletSessions.Create(ctx, sess); err != nil {
		return nil, err
	}
	s.auditf(ctx, "opname_tablet.start",
		"session=%s opname=%s device=%s tech=%s",
		sess.ID, in.OpnameSessionID, in.DeviceID, in.TechnicianUserID)
	return sess, nil
}

// SubmitOfflinePayload is idempotent: if a session for the same
// (opname_session_id, hash) already exists, return it instead of
// re-applying. This is the safety net for flaky-network double-submit.
func (s *Service) SubmitOfflinePayload(ctx context.Context, sessionID uuid.UUID, payloadBytes []byte, totalScans int) (*domain.OpnameTabletSession, error) {
	if s.opnameTabletSessions == nil {
		return nil, errOpnameTabletNotConfigured()
	}
	hash := HashPayload(payloadBytes)
	sess, err := s.opnameTabletSessions.FindByID(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	// Idempotency check — same hash already synced → short-circuit.
	if sess.OfflinePayloadHash == hash && sess.SyncStatus == domain.OpnameTabletSyncSynced {
		return sess, nil
	}
	// Also catch the case where a duplicate hash arrived under a
	// different session id (rare but possible during retry storms).
	dup, _ := s.opnameTabletSessions.FindByPayloadHash(ctx, sess.OpnameSessionID, hash)
	if dup != nil && dup.ID != sess.ID {
		return dup, nil
	}
	if err := sess.MarkSynced(hash, totalScans); err != nil {
		return nil, err
	}
	if err := s.opnameTabletSessions.UpdateStatus(ctx, sess); err != nil {
		// On UNIQUE violation (concurrent submit), re-look up by hash.
		if dup2, e := s.opnameTabletSessions.FindByPayloadHash(ctx, sess.OpnameSessionID, hash); e == nil && dup2 != nil {
			return dup2, nil
		}
		return nil, err
	}
	s.auditf(ctx, "opname_tablet.sync", "session=%s scans=%d hash=%s", sessionID, totalScans, hash)
	return sess, nil
}

// ReconcileSession terminates a synced tablet session — the variance is
// considered folded into the parent opname session.
func (s *Service) ReconcileSession(ctx context.Context, sessionID uuid.UUID) (*domain.OpnameTabletSession, error) {
	if s.opnameTabletSessions == nil {
		return nil, errOpnameTabletNotConfigured()
	}
	sess, err := s.opnameTabletSessions.FindByID(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if err := sess.MarkReconciled(); err != nil {
		return nil, err
	}
	if err := s.opnameTabletSessions.UpdateStatus(ctx, sess); err != nil {
		return nil, err
	}
	s.auditf(ctx, "opname_tablet.reconcile", "session=%s", sessionID)
	return sess, nil
}

func (s *Service) GetOpnameTabletSession(ctx context.Context, id uuid.UUID) (*domain.OpnameTabletSession, error) {
	if s.opnameTabletSessions == nil {
		return nil, errOpnameTabletNotConfigured()
	}
	return s.opnameTabletSessions.FindByID(ctx, id)
}

func (s *Service) ListOpnameTabletSessionsForOpname(ctx context.Context, opnameSessionID uuid.UUID) ([]domain.OpnameTabletSession, error) {
	if s.opnameTabletSessions == nil {
		return nil, errOpnameTabletNotConfigured()
	}
	return s.opnameTabletSessions.ListForOpnameSession(ctx, opnameSessionID)
}

// HashPayload returns the canonical hex sha256 of the payload bytes.
// Exposed so handlers + tests can compute the hash before submit (eg
// for client-side duplicate guard).
func HashPayload(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
