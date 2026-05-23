package postgres

import (
	"context"
	"encoding/json"
	stderrors "errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/payment/domain"
	"github.com/ion-core/backend/internal/payment/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// PaymentGatewayRepository implements port.PaymentGatewayRepository
// against `payment.payment_gateways`.
type PaymentGatewayRepository struct {
	pool *pgxpool.Pool
}

func NewPaymentGatewayRepository(pool *pgxpool.Pool) *PaymentGatewayRepository {
	return &PaymentGatewayRepository{pool: pool}
}

var _ port.PaymentGatewayRepository = (*PaymentGatewayRepository)(nil)

const gatewayCols = `
	id, code, name, kind, is_active, priority,
	COALESCE(supported_methods::text, '[]'),
	min_amount, max_amount,
	config_encrypted, COALESCE(config_key_version, 1),
	created_at, updated_at
`

func (r *PaymentGatewayRepository) Create(ctx context.Context, g *domain.PaymentGateway) error {
	methods, _ := json.Marshal(g.SupportedMethods)
	_, err := r.pool.Exec(ctx, `
		INSERT INTO payment.payment_gateways
			(id, code, name, kind, is_active, priority,
			 supported_methods, min_amount, max_amount,
			 config_encrypted, config_key_version,
			 created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, $8, $9, $10, $11, $12, $13)
	`,
		g.ID, g.Code, g.Name, string(g.Kind), g.IsActive, g.Priority,
		string(methods), g.MinAmount, g.MaxAmount,
		g.ConfigEncrypted, g.ConfigKeyVersion,
		g.CreatedAt, g.UpdatedAt,
	)
	return mapDBError(err, "payment_gateway", "insert payment gateway")
}

func (r *PaymentGatewayRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.PaymentGateway, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+gatewayCols+` FROM payment.payment_gateways WHERE id = $1`, id)
	g, err := scanGateway(row)
	if err != nil {
		return nil, err
	}
	return &g, nil
}

func (r *PaymentGatewayRepository) FindByCode(ctx context.Context, code string) (*domain.PaymentGateway, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+gatewayCols+` FROM payment.payment_gateways WHERE code = $1`, code)
	g, err := scanGateway(row)
	if err != nil {
		return nil, err
	}
	return &g, nil
}

func (r *PaymentGatewayRepository) ListActive(ctx context.Context) ([]domain.PaymentGateway, error) {
	return r.listFiltered(ctx, true)
}

func (r *PaymentGatewayRepository) ListAll(ctx context.Context) ([]domain.PaymentGateway, error) {
	return r.listFiltered(ctx, false)
}

func (r *PaymentGatewayRepository) listFiltered(ctx context.Context, onlyActive bool) ([]domain.PaymentGateway, error) {
	sql := `SELECT ` + gatewayCols + ` FROM payment.payment_gateways`
	if onlyActive {
		sql += ` WHERE is_active = TRUE`
	}
	sql += ` ORDER BY priority ASC, code ASC`
	rows, err := r.pool.Query(ctx, sql)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.payment_gateway_list", "list gateways", err)
	}
	defer rows.Close()
	out := []domain.PaymentGateway{}
	for rows.Next() {
		g, err := scanGateway(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, nil
}

func (r *PaymentGatewayRepository) Update(ctx context.Context, g *domain.PaymentGateway) error {
	methods, _ := json.Marshal(g.SupportedMethods)
	tag, err := r.pool.Exec(ctx, `
		UPDATE payment.payment_gateways
		SET name = $2, kind = $3, is_active = $4, priority = $5,
		    supported_methods = $6::jsonb,
		    min_amount = $7, max_amount = $8,
		    config_encrypted = $9, config_key_version = $10,
		    updated_at = NOW()
		WHERE id = $1
	`,
		g.ID, g.Name, string(g.Kind), g.IsActive, g.Priority,
		string(methods), g.MinAmount, g.MaxAmount,
		g.ConfigEncrypted, g.ConfigKeyVersion,
	)
	if err != nil {
		return mapDBError(err, "payment_gateway", "update payment gateway")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("payment_gateway.not_found", "payment gateway not found")
	}
	return nil
}

func scanGateway(row pgx.Row) (domain.PaymentGateway, error) {
	var g domain.PaymentGateway
	var kind string
	var methodsJSON string
	err := row.Scan(
		&g.ID, &g.Code, &g.Name, &kind, &g.IsActive, &g.Priority,
		&methodsJSON,
		&g.MinAmount, &g.MaxAmount,
		&g.ConfigEncrypted, &g.ConfigKeyVersion,
		&g.CreatedAt, &g.UpdatedAt,
	)
	if stderrors.Is(err, pgx.ErrNoRows) {
		return domain.PaymentGateway{}, derrors.NotFound(
			"payment_gateway.not_found", "payment gateway not found")
	}
	if err != nil {
		return domain.PaymentGateway{}, derrors.Wrap(
			derrors.KindInternal, "db.payment_gateway_scan", "scan gateway", err)
	}
	g.Kind = domain.GatewayKind(kind)
	if methodsJSON != "" {
		_ = json.Unmarshal([]byte(methodsJSON), &g.SupportedMethods)
	}
	if g.SupportedMethods == nil {
		g.SupportedMethods = []string{}
	}
	return g, nil
}
