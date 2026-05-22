package domain

import (
	"crypto/rand"
	"encoding/base64"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// RefreshToken represents a refresh grant stored server-side. The plaintext
// is returned to the client exactly once at issuance; the database stores a
// bcrypt hash so a DB leak can't be used to mint access tokens.
//
// Rotation rule: every successful /refresh REVOKES the presented refresh
// token and issues a new one. The old row links to the new via ReplacedBy
// — useful for detecting replay attacks (an attempt to reuse a revoked
// token is a strong signal something is wrong; we revoke the whole chain).
type RefreshToken struct {
	ID         uuid.UUID
	UserID     uuid.UUID
	TokenHash  string
	IssuedAt   time.Time
	ExpiresAt  time.Time
	RevokedAt  *time.Time
	ReplacedBy *uuid.UUID
	UserAgent  string
	IP         string
}

// IsActive reports whether the token can still be redeemed.
func (r *RefreshToken) IsActive(now time.Time) bool {
	return r.RevokedAt == nil && now.Before(r.ExpiresAt)
}

// GenerateRefreshTokenSecret produces a cryptographically strong random
// token string suitable for transport. The caller hashes it before storage.
//
// 32 bytes → 43 chars unpadded base64url; well above brute-force budget.
func GenerateRefreshTokenSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", errors.Wrap(errors.KindInternal, "refresh.random", "could not generate refresh token", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
