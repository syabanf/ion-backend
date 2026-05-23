package postgres

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/enterprise/domain"
	"github.com/ion-core/backend/internal/enterprise/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// IntercompanyPairRepository implements `port.IntercompanyPairRepository`
// against `enterprise.intercompany_pairs` (Wave 95 / migration 0064).
type IntercompanyPairRepository struct {
	pool *pgxpool.Pool
}

func NewIntercompanyPairRepository(pool *pgxpool.Pool) *IntercompanyPairRepository {
	return &IntercompanyPairRepository{pool: pool}
}

var _ port.IntercompanyPairRepository = (*IntercompanyPairRepository)(nil)

const intercompanyPairCols = `
	id, commercial_owner_subsidiary_id, executing_subsidiary_id,
	auto_accept, auto_accept_threshold,
	created_at, updated_at
`

func (r *IntercompanyPairRepository) FindByPair(ctx context.Context, commercialOwner, executing uuid.UUID) (*domain.IntercompanyPair, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT `+intercompanyPairCols+`
		FROM enterprise.intercompany_pairs
		WHERE commercial_owner_subsidiary_id = $1
		  AND executing_subsidiary_id = $2
	`, commercialOwner, executing)
	p, err := scanIntercompanyPair(row)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (r *IntercompanyPairRepository) List(ctx context.Context) ([]domain.IntercompanyPair, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+intercompanyPairCols+` FROM enterprise.intercompany_pairs ORDER BY created_at ASC`,
	)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.intercompany_pair_list", "list intercompany_pairs", err)
	}
	defer rows.Close()
	out := []domain.IntercompanyPair{}
	for rows.Next() {
		p, err := scanIntercompanyPair(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// Upsert inserts the pair when missing, updates auto_accept fields
// when the unique key matches. Idempotent — safe to call repeatedly
// with the same configuration.
func (r *IntercompanyPairRepository) Upsert(ctx context.Context, pair *domain.IntercompanyPair) error {
	if pair.ID == uuid.Nil {
		pair.ID = uuid.New()
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO enterprise.intercompany_pairs
			(id, commercial_owner_subsidiary_id, executing_subsidiary_id,
			 auto_accept, auto_accept_threshold,
			 created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, NOW(), NOW())
		ON CONFLICT (commercial_owner_subsidiary_id, executing_subsidiary_id)
		DO UPDATE SET
		    auto_accept = EXCLUDED.auto_accept,
		    auto_accept_threshold = EXCLUDED.auto_accept_threshold,
		    updated_at = NOW()
	`,
		pair.ID, pair.CommercialOwnerSubsidiaryID, pair.ExecutingSubsidiaryID,
		pair.AutoAccept, pair.AutoAcceptThreshold,
	)
	if err != nil {
		return mapDBError(err, "intercompany_pair", "upsert intercompany_pair")
	}
	return nil
}

func scanIntercompanyPair(row pgx.Row) (domain.IntercompanyPair, error) {
	var p domain.IntercompanyPair
	err := row.Scan(
		&p.ID, &p.CommercialOwnerSubsidiaryID, &p.ExecutingSubsidiaryID,
		&p.AutoAccept, &p.AutoAcceptThreshold,
		&p.CreatedAt, &p.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.IntercompanyPair{}, derrors.NotFound("intercompany_pair.not_found", "intercompany_pair not found")
	}
	if err != nil {
		return domain.IntercompanyPair{}, derrors.Wrap(derrors.KindInternal, "db.intercompany_pair_scan", "scan intercompany_pair", err)
	}
	return p, nil
}
