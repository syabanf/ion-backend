package postgres

import (
	"context"
	stderrors "errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/payment/domain"
	"github.com/ion-core/backend/internal/payment/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// PaymentMethodRepository implements port.PaymentMethodRepository
// against `payment.payment_methods`.
type PaymentMethodRepository struct {
	pool *pgxpool.Pool
}

func NewPaymentMethodRepository(pool *pgxpool.Pool) *PaymentMethodRepository {
	return &PaymentMethodRepository{pool: pool}
}

var _ port.PaymentMethodRepository = (*PaymentMethodRepository)(nil)

const methodCols = `
	id, customer_id, kind, gateway_id,
	COALESCE(masked_account, ''), expires_at,
	is_default, last_used_at,
	created_at, updated_at
`

func (r *PaymentMethodRepository) Create(ctx context.Context, m *domain.PaymentMethod) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO payment.payment_methods
			(id, customer_id, kind, gateway_id, masked_account,
			 expires_at, is_default, last_used_at,
			 created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`,
		m.ID, m.CustomerID, m.Kind, m.GatewayID,
		nullableString(m.MaskedAccount), m.ExpiresAt,
		m.IsDefault, m.LastUsedAt, m.CreatedAt, m.UpdatedAt,
	)
	return mapDBError(err, "payment_method", "insert payment method")
}

func (r *PaymentMethodRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.PaymentMethod, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+methodCols+` FROM payment.payment_methods WHERE id = $1`, id)
	m, err := scanMethod(row)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func (r *PaymentMethodRepository) ListForCustomer(ctx context.Context, customerID uuid.UUID) ([]domain.PaymentMethod, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+methodCols+`
		FROM payment.payment_methods
		WHERE customer_id = $1
		ORDER BY is_default DESC, last_used_at DESC NULLS LAST, created_at DESC
	`, customerID)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.payment_method_list", "list methods", err)
	}
	defer rows.Close()
	out := []domain.PaymentMethod{}
	for rows.Next() {
		m, err := scanMethod(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

func (r *PaymentMethodRepository) MarkUsed(ctx context.Context, id uuid.UUID, at time.Time) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE payment.payment_methods
		SET last_used_at = $2, updated_at = NOW()
		WHERE id = $1
	`, id, at)
	if err != nil {
		return mapDBError(err, "payment_method", "update last_used_at")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("payment_method.not_found", "payment method not found")
	}
	return nil
}

func scanMethod(row pgx.Row) (domain.PaymentMethod, error) {
	var m domain.PaymentMethod
	err := row.Scan(
		&m.ID, &m.CustomerID, &m.Kind, &m.GatewayID,
		&m.MaskedAccount, &m.ExpiresAt,
		&m.IsDefault, &m.LastUsedAt,
		&m.CreatedAt, &m.UpdatedAt,
	)
	if stderrors.Is(err, pgx.ErrNoRows) {
		return domain.PaymentMethod{}, derrors.NotFound(
			"payment_method.not_found", "payment method not found")
	}
	if err != nil {
		return domain.PaymentMethod{}, derrors.Wrap(
			derrors.KindInternal, "db.payment_method_scan", "scan method", err)
	}
	return m, nil
}
