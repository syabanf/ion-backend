package postgres

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/crm/domain"
	"github.com/ion-core/backend/internal/crm/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type ProductRepository struct {
	pool *pgxpool.Pool
}

func NewProductRepository(pool *pgxpool.Pool) *ProductRepository {
	return &ProductRepository{pool: pool}
}

var _ port.ProductRepository = (*ProductRepository)(nil)

// Wave 77 (TC-PRD-014/016/018/022): productSelect now reads the 5
// schema slot FKs. All nullable, so scan into *uuid.UUID pointers.
const productSelect = `
SELECT id, code, name, speed_mbps, monthly_price, otc_price,
       temp_activation_window_hours, active, created_at,
       onboarding_schema_id, billing_schema_id, service_schema_id,
       commission_schema_id, suspension_schema_id
FROM crm.products
`

func (r *ProductRepository) Create(ctx context.Context, p *domain.Product) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO crm.products (id, code, name, speed_mbps, monthly_price, otc_price,
		                          temp_activation_window_hours, active, created_at,
		                          onboarding_schema_id, billing_schema_id, service_schema_id,
		                          commission_schema_id, suspension_schema_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
	`, p.ID, p.Code, p.Name, p.SpeedMbps, p.MonthlyPrice, p.OTCPrice,
		p.TempActivationWindowHrs, p.Active, p.CreatedAt,
		p.OnboardingSchemaID, p.BillingSchemaID, p.ServiceSchemaID,
		p.CommissionSchemaID, p.SuspensionSchemaID)
	return mapDBError(err, "product.create", "create product")
}

func (r *ProductRepository) List(ctx context.Context, f port.ProductListFilter) ([]domain.Product, error) {
	var (
		args  []any
		conds []string
	)
	if f.ActiveOnly {
		conds = append(conds, "active = TRUE")
	}
	if s := strings.TrimSpace(f.Search); s != "" {
		args = append(args, "%"+s+"%")
		conds = append(conds, "(code ILIKE $"+itoa(len(args))+" OR name ILIKE $"+itoa(len(args))+")")
	}
	sql := productSelect
	if len(conds) > 0 {
		sql += " WHERE " + strings.Join(conds, " AND ")
	}
	sql += " ORDER BY speed_mbps ASC"
	if f.Limit > 0 {
		args = append(args, f.Limit)
		sql += " LIMIT $" + itoa(len(args))
	}
	if f.Offset > 0 {
		args = append(args, f.Offset)
		sql += " OFFSET $" + itoa(len(args))
	}

	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.product_list", "list products", err)
	}
	defer rows.Close()
	out := []domain.Product{}
	for rows.Next() {
		var p domain.Product
		if err := rows.Scan(&p.ID, &p.Code, &p.Name, &p.SpeedMbps, &p.MonthlyPrice,
			&p.OTCPrice, &p.TempActivationWindowHrs, &p.Active, &p.CreatedAt,
			&p.OnboardingSchemaID, &p.BillingSchemaID, &p.ServiceSchemaID,
			&p.CommissionSchemaID, &p.SuspensionSchemaID); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "db.product_scan", "scan product", err)
		}
		out = append(out, p)
	}
	return out, nil
}

func (r *ProductRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.Product, error) {
	row := r.pool.QueryRow(ctx, productSelect+" WHERE id = $1", id)
	var p domain.Product
	err := row.Scan(&p.ID, &p.Code, &p.Name, &p.SpeedMbps, &p.MonthlyPrice,
		&p.OTCPrice, &p.TempActivationWindowHrs, &p.Active, &p.CreatedAt,
		&p.OnboardingSchemaID, &p.BillingSchemaID, &p.ServiceSchemaID,
		&p.CommissionSchemaID, &p.SuspensionSchemaID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, derrors.NotFound("product.not_found", "product not found")
	}
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.product_get", "read product", err)
	}
	return &p, nil
}

func (r *ProductRepository) Update(ctx context.Context, p *domain.Product) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE crm.products
		   SET name = $2, speed_mbps = $3, monthly_price = $4, otc_price = $5,
		       temp_activation_window_hours = $6, active = $7,
		       onboarding_schema_id = $8, billing_schema_id = $9, service_schema_id = $10,
		       commission_schema_id = $11, suspension_schema_id = $12
		 WHERE id = $1
	`, p.ID, p.Name, p.SpeedMbps, p.MonthlyPrice, p.OTCPrice,
		p.TempActivationWindowHrs, p.Active,
		p.OnboardingSchemaID, p.BillingSchemaID, p.ServiceSchemaID,
		p.CommissionSchemaID, p.SuspensionSchemaID)
	if err != nil {
		return mapDBError(err, "product.update", "update product")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("product.not_found", "product not found")
	}
	return nil
}

// itoa keeps positional-arg-building tidy without pulling strconv just for this.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
