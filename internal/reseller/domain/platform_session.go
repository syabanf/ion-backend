package domain

import (
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// PlatformSession is the opaque auth token the reseller platform uses
// to scope every request to a single tenant. The real auth flow
// (OAuth / SSO) lands later — for now we accept a `reseller_id +
// shared secret` exchange and mint an opaque random token. The token
// is stored alongside its tenant id so the middleware can do a single
// lookup per request.
type PlatformSession struct {
	ID                uuid.UUID
	ResellerAccountID uuid.UUID
	SessionToken      string
	ExpiresAt         *time.Time
	CreatedAt         time.Time
	LastUsedAt        *time.Time
}

// NewPlatformSession mints a fresh session with a 256-bit random
// token and a 7-day TTL. Both are conservative defaults — production
// will likely shorten the TTL and add refresh tokens once the real
// auth flow lands.
func NewPlatformSession(resellerID uuid.UUID, ttl time.Duration) (*PlatformSession, error) {
	if resellerID == uuid.Nil {
		return nil, errors.Validation("session.reseller_required", "reseller_account_id is required")
	}
	if ttl <= 0 {
		ttl = 7 * 24 * time.Hour
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return nil, errors.Wrap(errors.KindInternal, "session.rand", "generate session token", err)
	}
	now := time.Now().UTC()
	exp := now.Add(ttl)
	return &PlatformSession{
		ID:                uuid.New(),
		ResellerAccountID: resellerID,
		SessionToken:      hex.EncodeToString(buf),
		ExpiresAt:         &exp,
		CreatedAt:         now,
	}, nil
}

// IsExpired reports whether the session's expiry has elapsed at the
// caller-supplied clock value. We accept the clock as an argument so
// the platform middleware can use a single `time.Now()` per request.
func (s *PlatformSession) IsExpired(now time.Time) bool {
	if s.ExpiresAt == nil {
		return false
	}
	return !now.Before(*s.ExpiresAt)
}
