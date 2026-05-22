package postgres

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/identity/domain"
	"github.com/ion-core/backend/internal/identity/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type PlatformConfigRepository struct {
	pool *pgxpool.Pool
}

func NewPlatformConfigRepository(pool *pgxpool.Pool) *PlatformConfigRepository {
	return &PlatformConfigRepository{pool: pool}
}

var _ port.PlatformConfigRepository = (*PlatformConfigRepository)(nil)

func (r *PlatformConfigRepository) List(ctx context.Context) ([]domain.PlatformConfig, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, config_key, config_value, updated_by, updated_at
		FROM identity.platform_config
		ORDER BY config_key
	`)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.config_list", "list config", err)
	}
	defer rows.Close()

	out := []domain.PlatformConfig{}
	for rows.Next() {
		var c domain.PlatformConfig
		if err := rows.Scan(&c.ID, &c.Key, &c.Value, &c.UpdatedBy, &c.UpdatedAt); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "db.config_scan", "scan config", err)
		}
		out = append(out, c)
	}
	return out, nil
}

func (r *PlatformConfigRepository) Upsert(ctx context.Context, key, value string, updatedBy uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO identity.platform_config (config_key, config_value, updated_by, updated_at)
		VALUES ($1, $2, $3, NOW())
		ON CONFLICT (config_key) DO UPDATE
		   SET config_value = EXCLUDED.config_value,
		       updated_by   = EXCLUDED.updated_by,
		       updated_at   = NOW()
	`, key, value, updatedBy)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "db.config_upsert", "upsert config", err)
	}
	return nil
}
