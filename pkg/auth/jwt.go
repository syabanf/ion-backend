// Package auth provides JWT issuance and verification.
//
// The Issuer is held by identity-svc (the only service that mints tokens).
// All other services use a Verifier to validate incoming tokens — they
// don't need the signing secret to be shareable as long as we move to
// asymmetric keys later.
package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// Claims is the canonical JWT payload for ION Core.
//
// Permissions are denormalized into the token at issuance time so downstream
// services can authorize requests by checking claims locally — no callback
// to identity-svc. The short access-token TTL (default 15 minutes) bounds
// the privilege-revocation lag.
//
// Wave 97 added the company-scope + suspended fields:
//   - SubsidiaryID + HoldingCompanyID feed the enterprise CompanyScope
//     predicate so reads can be filtered to the actor's allowed
//     commercial-owner set without a per-request DB lookup.
//   - SuspendedAt short-circuits requests for users that identity-svc has
//     marked inactive between token issuance and expiry. Existing tokens
//     issued before Wave 97 simply omit these fields — the verifier
//     treats their zero values as "no scope restriction / not suspended",
//     which means OLD tokens see the SAME data they always did. New
//     enterprise endpoints relying on CompanyScope.AppliesTo() will
//     return empty result sets for tokens whose SubsidiaryID is zero,
//     which is the safe-by-default behaviour we want for company-owned
//     resources.
type Claims struct {
	UserID           uuid.UUID  `json:"uid"`
	Email            string     `json:"email"`
	Roles            []string   `json:"roles"`
	Permissions      []string   `json:"perms"`
	BranchID         *uuid.UUID `json:"branch_id,omitempty"`
	BranchLevel      string     `json:"branch_level,omitempty"`
	SubsidiaryID     *uuid.UUID `json:"subsidiary_id,omitempty"`
	HoldingCompanyID *uuid.UUID `json:"holding_company_id,omitempty"`
	SuspendedAt      *time.Time `json:"suspended_at,omitempty"`
	jwt.RegisteredClaims
}

// HasPermission reports whether the canonical "module.action" key is in
// the claims. Used by RequirePermission middleware in pkg/httpserver.
func (c *Claims) HasPermission(key string) bool {
	for _, p := range c.Permissions {
		if p == key {
			return true
		}
	}
	return false
}

// HasAnyPermission reports whether the claims include at least one of keys.
func (c *Claims) HasAnyPermission(keys ...string) bool {
	for _, k := range keys {
		if c.HasPermission(k) {
			return true
		}
	}
	return false
}

// Issuer creates signed JWTs.
type Issuer struct {
	secret    []byte
	issuer    string
	accessTTL time.Duration
}

func NewIssuer(secret, issuer string, accessTTL time.Duration) *Issuer {
	return &Issuer{secret: []byte(secret), issuer: issuer, accessTTL: accessTTL}
}

// Issue mints an access token for the given subject claims.
func (i *Issuer) Issue(c Claims) (string, error) {
	now := time.Now()
	c.RegisteredClaims = jwt.RegisteredClaims{
		Issuer:    i.issuer,
		Subject:   c.UserID.String(),
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(i.accessTTL)),
		NotBefore: jwt.NewNumericDate(now),
		ID:        uuid.NewString(),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, &c)
	return tok.SignedString(i.secret)
}

// Verifier validates JWTs. Used by every downstream service.
type Verifier struct {
	secret []byte
	issuer string
}

func NewVerifier(secret, issuer string) *Verifier {
	return &Verifier{secret: []byte(secret), issuer: issuer}
}

var (
	ErrInvalidToken = errors.New("auth: invalid token")
	ErrExpiredToken = errors.New("auth: expired token")
)

// Verify parses and validates a token string. Returns *Claims on success.
func (v *Verifier) Verify(tokenString string) (*Claims, error) {
	tok, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return v.secret, nil
	}, jwt.WithIssuer(v.issuer), jwt.WithValidMethods([]string{"HS256"}))

	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrExpiredToken
		}
		return nil, ErrInvalidToken
	}

	claims, ok := tok.Claims.(*Claims)
	if !ok || !tok.Valid {
		return nil, ErrInvalidToken
	}
	return claims, nil
}
