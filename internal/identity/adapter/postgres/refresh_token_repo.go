package postgres

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/identity/domain"
	"github.com/ion-core/backend/internal/identity/port"
	"github.com/ion-core/backend/pkg/auth"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type RefreshTokenRepository struct {
	pool *pgxpool.Pool
}

func NewRefreshTokenRepository(pool *pgxpool.Pool) *RefreshTokenRepository {
	return &RefreshTokenRepository{pool: pool}
}

var _ port.RefreshTokenRepository = (*RefreshTokenRepository)(nil)

func (r *RefreshTokenRepository) Store(ctx context.Context, t *domain.RefreshToken) error {
	var ipArg any
	if t.IP != "" {
		if parsed := net.ParseIP(t.IP); parsed != nil {
			ipArg = parsed.String()
		}
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO identity.refresh_tokens
			(id, user_id, token_hash, issued_at, expires_at, user_agent, ip)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, t.ID, t.UserID, t.TokenHash, t.IssuedAt, t.ExpiresAt, nullableString(t.UserAgent), ipArg)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "db.refresh_insert", "store refresh token", err)
	}
	return nil
}

// FindActive performs the lookup-then-verify dance: the id locates a row
// in O(1); the bcrypt comparison is what proves the bearer holds the
// correct secret. A missing row or a mismatched secret both return
// NotFound — the caller must NOT distinguish the two (timing risk).
func (r *RefreshTokenRepository) FindActive(ctx context.Context, id uuid.UUID, plaintextSecret string) (*domain.RefreshToken, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, user_id, token_hash, issued_at, expires_at, revoked_at, replaced_by,
		       COALESCE(user_agent, ''), COALESCE(host(ip), '')
		FROM identity.refresh_tokens
		WHERE id = $1
	`, id)

	var (
		t          domain.RefreshToken
		revokedAt  *time.Time
		replacedBy *uuid.UUID
		ipStr      string
	)
	err := row.Scan(
		&t.ID, &t.UserID, &t.TokenHash, &t.IssuedAt, &t.ExpiresAt,
		&revokedAt, &replacedBy, &t.UserAgent, &ipStr,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, derrors.NotFound("refresh.not_found", "refresh token not found")
	}
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.refresh_scan", "scan refresh token", err)
	}
	t.RevokedAt = revokedAt
	t.ReplacedBy = replacedBy
	t.IP = ipStr

	// Verify the secret matches. Constant-time inside bcrypt.
	if err := auth.ComparePassword(t.TokenHash, plaintextSecret); err != nil {
		return nil, derrors.NotFound("refresh.not_found", "refresh token not found")
	}
	return &t, nil
}

func (r *RefreshTokenRepository) Revoke(ctx context.Context, id uuid.UUID, replacedBy *uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE identity.refresh_tokens
		SET revoked_at = NOW(),
		    replaced_by = COALESCE($2, replaced_by)
		WHERE id = $1 AND revoked_at IS NULL
	`, id, replacedBy)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "db.refresh_revoke", "revoke refresh token", err)
	}
	return nil
}

func (r *RefreshTokenRepository) RevokeAllForUser(ctx context.Context, userID uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE identity.refresh_tokens
		SET revoked_at = NOW()
		WHERE user_id = $1 AND revoked_at IS NULL
	`, userID)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "db.refresh_revoke_all", "revoke user tokens", err)
	}
	return nil
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
