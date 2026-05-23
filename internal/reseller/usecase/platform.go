package usecase

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/reseller/domain"
	"github.com/ion-core/backend/internal/reseller/port"
	"github.com/ion-core/backend/pkg/errors"
)

// PlatformService implements port.PlatformUseCase. It mints opaque
// session tokens and resolves them back to the owning reseller
// account on every platform request.
//
// The current secret model is a deliberate stub: each reseller has
// a shared secret derived from `sha256(reseller_id || global_pepper)`.
// The real OAuth / SSO flow lands later — keeping this trivially
// replaceable is why ResolveTenant only takes the token, not the
// secret.
type PlatformService struct {
	sessions port.PlatformSessionRepository
	accounts port.ResellerAccountRepository
	pepper   []byte
}

// NewPlatformService takes the session + account repos and a pepper
// that scopes the stub secret derivation. The pepper SHOULD come from
// the deployment's config (cfg.JWTSecret is fine — it's already a
// per-deployment secret).
func NewPlatformService(sessions port.PlatformSessionRepository, accounts port.ResellerAccountRepository, pepper string) *PlatformService {
	return &PlatformService{
		sessions: sessions,
		accounts: accounts,
		pepper:   []byte(pepper),
	}
}

var _ port.PlatformUseCase = (*PlatformService)(nil)

// expectedSecret derives the per-reseller secret. Constant-time
// comparison happens in IssueSession; this helper just produces the
// expected value.
func (s *PlatformService) expectedSecret(resellerID uuid.UUID) string {
	h := sha256.New()
	h.Write(resellerID[:])
	h.Write(s.pepper)
	return hex.EncodeToString(h.Sum(nil))
}

// IssueSession verifies the shared secret and mints a fresh session.
// On a wrong secret we return Unauthorized with no detail — clients
// shouldn't be able to enumerate valid reseller ids.
func (s *PlatformService) IssueSession(ctx context.Context, resellerID uuid.UUID, secret string, ttl time.Duration) (*domain.PlatformSession, error) {
	if resellerID == uuid.Nil {
		return nil, errors.Validation("session.reseller_required", "reseller_account_id is required")
	}
	acc, err := s.accounts.FindByID(ctx, resellerID)
	if err != nil {
		// Map NotFound → Unauthorized to avoid enumeration.
		if errors.IsNotFound(err) {
			return nil, errors.Unauthorized("session.invalid_credentials", "invalid credentials")
		}
		return nil, err
	}
	if !acc.IsOperational() {
		return nil, errors.Forbidden(
			"session.reseller_not_operational",
			"reseller is not in an operational status",
		)
	}
	expected := s.expectedSecret(resellerID)
	if subtle.ConstantTimeCompare([]byte(expected), []byte(strings.TrimSpace(secret))) != 1 {
		return nil, errors.Unauthorized("session.invalid_credentials", "invalid credentials")
	}
	sess, err := domain.NewPlatformSession(resellerID, ttl)
	if err != nil {
		return nil, err
	}
	if err := s.sessions.Create(ctx, sess); err != nil {
		return nil, err
	}
	return sess, nil
}

// ResolveTenant is the platform middleware's entry point. It does:
//  1. token lookup (Unauthorized on miss)
//  2. expiry check (Unauthorized on expired)
//  3. account operational check (Forbidden on suspended/terminated)
//  4. last_used_at bookkeeping
//
// The middleware caches nothing — a single DB round-trip per request
// is acceptable while the platform is low-traffic, and lets revocation
// propagate immediately.
func (s *PlatformService) ResolveTenant(ctx context.Context, sessionToken string) (uuid.UUID, error) {
	token := strings.TrimSpace(sessionToken)
	if token == "" {
		return uuid.Nil, errors.Unauthorized("session.missing", "session token is required")
	}
	sess, err := s.sessions.FindByToken(ctx, token)
	if err != nil {
		if errors.IsNotFound(err) {
			return uuid.Nil, errors.Unauthorized("session.invalid", "invalid session token")
		}
		return uuid.Nil, err
	}
	now := time.Now().UTC()
	if sess.IsExpired(now) {
		return uuid.Nil, errors.Unauthorized("session.expired", "session token has expired")
	}
	acc, err := s.accounts.FindByID(ctx, sess.ResellerAccountID)
	if err != nil {
		return uuid.Nil, err
	}
	if !acc.IsOperational() {
		return uuid.Nil, errors.Forbidden(
			"session.reseller_not_operational",
			"reseller is not in an operational status",
		)
	}
	// best-effort bookkeeping — a failed update shouldn't break the
	// request (the token is still valid)
	_ = s.sessions.MarkUsed(ctx, sess.ID, now)
	return sess.ResellerAccountID, nil
}

// ExpectedSecretForReseller exposes the derived secret to admin
// tooling so an operator can hand it to a reseller during onboarding.
// Not part of the port — only the admin handler uses it directly.
func (s *PlatformService) ExpectedSecretForReseller(resellerID uuid.UUID) string {
	return s.expectedSecret(resellerID)
}
