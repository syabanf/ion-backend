package postgres

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/vendormgmt/domain"
	"github.com/ion-core/backend/internal/vendormgmt/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// ProviderCapabilityRepository implements port.ProviderCapabilityRepository.
type ProviderCapabilityRepository struct {
	pool *pgxpool.Pool
}

func NewProviderCapabilityRepository(pool *pgxpool.Pool) *ProviderCapabilityRepository {
	return &ProviderCapabilityRepository{pool: pool}
}

var _ port.ProviderCapabilityRepository = (*ProviderCapabilityRepository)(nil)

func (r *ProviderCapabilityRepository) Create(ctx context.Context, c *domain.ProviderCapability) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO vendor.provider_capabilities
			(id, provider_id, capability_key, capability_name, max_capacity, created_at)
		VALUES ($1, $2, $3, NULLIF($4,''), $5, $6)
	`,
		c.ID, c.ProviderID, c.CapabilityKey, c.CapabilityName, c.MaxCapacity, c.CreatedAt,
	)
	if err != nil {
		return mapDBError(err, "provider_capability", "insert capability")
	}
	return nil
}

func (r *ProviderCapabilityRepository) ListByProvider(ctx context.Context, providerID uuid.UUID) ([]domain.ProviderCapability, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, provider_id, capability_key,
		       COALESCE(capability_name, ''), max_capacity, created_at
		FROM vendor.provider_capabilities
		WHERE provider_id = $1
		ORDER BY capability_key
	`, providerID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return []domain.ProviderCapability{}, nil
		}
		return nil, derrors.Wrap(derrors.KindInternal, "db.capability_list", "list capabilities", err)
	}
	defer rows.Close()
	out := []domain.ProviderCapability{}
	for rows.Next() {
		var c domain.ProviderCapability
		if err := rows.Scan(&c.ID, &c.ProviderID, &c.CapabilityKey,
			&c.CapabilityName, &c.MaxCapacity, &c.CreatedAt); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "db.capability_scan", "scan capability", err)
		}
		out = append(out, c)
	}
	return out, nil
}

func (r *ProviderCapabilityRepository) ListProvidersByCapability(ctx context.Context, capabilityKey string) ([]uuid.UUID, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT provider_id FROM vendor.provider_capabilities
		WHERE capability_key = $1
	`, capabilityKey)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.capability_by_key", "find providers by capability", err)
	}
	defer rows.Close()
	out := []uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "db.capability_by_key_scan", "scan provider id", err)
		}
		out = append(out, id)
	}
	return out, nil
}
