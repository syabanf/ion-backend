package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/vendormgmt/domain"
	"github.com/ion-core/backend/internal/vendormgmt/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// ProviderRepository implements port.ProviderRepository against
// vendor.providers.
type ProviderRepository struct {
	pool *pgxpool.Pool
}

func NewProviderRepository(pool *pgxpool.Pool) *ProviderRepository {
	return &ProviderRepository{pool: pool}
}

var _ port.ProviderRepository = (*ProviderRepository)(nil)

const providerCols = `
	id, name,
	COALESCE(npwp, ''), COALESCE(contact_email, ''), COALESCE(contact_phone, ''),
	status, kyc_completed,
	COALESCE(capabilities, '[]'::jsonb),
	rating_score, total_completed_jobs, total_revenue,
	created_at, updated_at,
	suspended_at, COALESCE(suspended_reason, '')
`

func (r *ProviderRepository) Create(ctx context.Context, p *domain.Provider) error {
	caps, _ := json.Marshal(p.Capabilities)
	_, err := r.pool.Exec(ctx, `
		INSERT INTO vendor.providers
			(id, name, npwp, contact_email, contact_phone,
			 status, kyc_completed, capabilities,
			 rating_score, total_completed_jobs, total_revenue,
			 created_at, updated_at, suspended_at, suspended_reason)
		VALUES ($1, $2, NULLIF($3,''), NULLIF($4,''), NULLIF($5,''),
		        $6, $7, $8,
		        $9, $10, $11,
		        $12, $13, $14, NULLIF($15,''))
	`,
		p.ID, p.Name, p.NPWP, p.ContactEmail, p.ContactPhone,
		string(p.Status), p.KYCCompleted, caps,
		p.RatingScore, p.TotalCompletedJobs, p.TotalRevenue,
		p.CreatedAt, p.UpdatedAt, p.SuspendedAt, p.SuspendedReason,
	)
	if err != nil {
		return mapDBError(err, "provider", "insert provider")
	}
	return nil
}

func (r *ProviderRepository) Update(ctx context.Context, p *domain.Provider) error {
	caps, _ := json.Marshal(p.Capabilities)
	tag, err := r.pool.Exec(ctx, `
		UPDATE vendor.providers
		SET name = $2,
		    npwp = NULLIF($3,''),
		    contact_email = NULLIF($4,''),
		    contact_phone = NULLIF($5,''),
		    status = $6,
		    kyc_completed = $7,
		    capabilities = $8,
		    rating_score = $9,
		    total_completed_jobs = $10,
		    total_revenue = $11,
		    suspended_at = $12,
		    suspended_reason = NULLIF($13,''),
		    updated_at = NOW()
		WHERE id = $1
	`,
		p.ID, p.Name, p.NPWP, p.ContactEmail, p.ContactPhone,
		string(p.Status), p.KYCCompleted, caps,
		p.RatingScore, p.TotalCompletedJobs, p.TotalRevenue,
		p.SuspendedAt, p.SuspendedReason,
	)
	if err != nil {
		return mapDBError(err, "provider", "update provider")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("provider.not_found", "provider not found")
	}
	return nil
}

func (r *ProviderRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.Provider, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+providerCols+` FROM vendor.providers WHERE id = $1`, id,
	)
	p, err := scanProvider(row)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (r *ProviderRepository) List(ctx context.Context, f port.ProviderListFilter) ([]domain.Provider, int, error) {
	var wh []string
	var args []any
	if f.Status != "" {
		args = append(args, f.Status)
		wh = append(wh, fmt.Sprintf("status = $%d", len(args)))
	}
	if f.OnlyActive {
		wh = append(wh, "status = 'active'")
	}
	if len(f.CapabilityIn) > 0 {
		args = append(args, f.CapabilityIn)
		wh = append(wh,
			fmt.Sprintf("id IN (SELECT provider_id FROM vendor.provider_capabilities WHERE capability_key = ANY($%d))", len(args)),
		)
	}
	where := ""
	if len(wh) > 0 {
		where = " WHERE " + strings.Join(wh, " AND ")
	}

	var total int
	if err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM vendor.providers`+where, args...,
	).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.provider_count", "count providers", err)
	}

	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}
	args = append(args, limit, offset)
	sql := `SELECT ` + providerCols + ` FROM vendor.providers` + where +
		` ORDER BY rating_score DESC, total_completed_jobs DESC, created_at DESC` +
		fmt.Sprintf(" LIMIT $%d OFFSET $%d", len(args)-1, len(args))

	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.provider_list", "list providers", err)
	}
	defer rows.Close()
	out := []domain.Provider{}
	for rows.Next() {
		p, err := scanProvider(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, p)
	}
	return out, total, nil
}

// IncrementCompletedJob atomically bumps job + revenue counters. Used
// by the cross-context IC-PO-accept hook in enterprise (via the
// MetricsUpdater seam) so concurrent accepts don't race.
func (r *ProviderRepository) IncrementCompletedJob(ctx context.Context, providerID uuid.UUID, revenue float64) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE vendor.providers
		SET total_completed_jobs = total_completed_jobs + 1,
		    total_revenue        = total_revenue + $2,
		    updated_at           = NOW()
		WHERE id = $1
	`, providerID, revenue)
	if err != nil {
		return mapDBError(err, "provider", "increment completed job")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("provider.not_found", "provider not found")
	}
	return nil
}

func scanProvider(row pgx.Row) (domain.Provider, error) {
	var p domain.Provider
	var caps []byte
	var status string
	err := row.Scan(
		&p.ID, &p.Name,
		&p.NPWP, &p.ContactEmail, &p.ContactPhone,
		&status, &p.KYCCompleted, &caps,
		&p.RatingScore, &p.TotalCompletedJobs, &p.TotalRevenue,
		&p.CreatedAt, &p.UpdatedAt,
		&p.SuspendedAt, &p.SuspendedReason,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Provider{}, derrors.NotFound("provider.not_found", "provider not found")
	}
	if err != nil {
		return domain.Provider{}, derrors.Wrap(derrors.KindInternal, "db.provider_scan", "scan provider", err)
	}
	p.Status = domain.ProviderStatus(status)
	if len(caps) > 0 {
		_ = json.Unmarshal(caps, &p.Capabilities)
	}
	if p.Capabilities == nil {
		p.Capabilities = []string{}
	}
	return p, nil
}
