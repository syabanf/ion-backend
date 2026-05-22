package postgres

import (
	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/identity/port"
	"github.com/ion-core/backend/pkg/auth"
)

// JWTIssuer implements port.TokenIssuer with HS256 via pkg/auth.
// When we move to asymmetric keys (RS256), swap the underlying issuer here
// — the port stays the same.
type JWTIssuer struct {
	issuer *auth.Issuer
}

func NewJWTIssuer(i *auth.Issuer) *JWTIssuer { return &JWTIssuer{issuer: i} }

var _ port.TokenIssuer = (*JWTIssuer)(nil)

func (j *JWTIssuer) Issue(userID uuid.UUID, email string, roles, permissions []string, branchID *uuid.UUID, branchLevel string) (string, error) {
	return j.issuer.Issue(auth.Claims{
		UserID:      userID,
		Email:       email,
		Roles:       roles,
		Permissions: permissions,
		BranchID:    branchID,
		BranchLevel: branchLevel,
	})
}
