package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/reseller/domain"
	"github.com/ion-core/backend/internal/reseller/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// PlatformSessionRepository implements port.PlatformSessionRepository
// against `reseller.platform_sessions`.
type PlatformSessionRepository struct {
	pool *pgxpool.Pool
}

func NewPlatformSessionRepository(pool *pgxpool.Pool) *PlatformSessionRepository {
	return &PlatformSessionRepository{pool: pool}
}

var _ port.PlatformSessionRepository = (*PlatformSessionRepository)(nil)

const sessionCols = `
	id, reseller_account_id,
	COALESCE(session_token, ''),
	expires_at, created_at, last_used_at
`

func (r *PlatformSessionRepository) Create(ctx context.Context, s *domain.PlatformSession) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO reseller.platform_sessions
			(id, reseller_account_id, session_token, expires_at, created_at, last_used_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`,
		s.ID, s.ResellerAccountID, s.SessionToken, s.ExpiresAt, s.CreatedAt, s.LastUsedAt,
	)
	if err != nil {
		return mapDBError(err, "platform_session", "insert platform session")
	}
	return nil
}

func (r *PlatformSessionRepository) FindByToken(ctx context.Context, token string) (*domain.PlatformSession, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+sessionCols+` FROM reseller.platform_sessions WHERE session_token = $1`, token)
	var s domain.PlatformSession
	err := row.Scan(
		&s.ID, &s.ResellerAccountID,
		&s.SessionToken,
		&s.ExpiresAt, &s.CreatedAt, &s.LastUsedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, derrors.NotFound("platform_session.not_found", "session not found")
	}
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.platform_session_scan", "scan session", err)
	}
	return &s, nil
}

func (r *PlatformSessionRepository) MarkUsed(ctx context.Context, id uuid.UUID, at time.Time) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE reseller.platform_sessions SET last_used_at = $2 WHERE id = $1`, id, at)
	if err != nil {
		return mapDBError(err, "platform_session", "update last_used_at")
	}
	return nil
}
