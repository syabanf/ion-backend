package postgres

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/billing/domain"
	"github.com/ion-core/backend/internal/billing/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type PaymentRepository struct {
	pool *pgxpool.Pool
}

func NewPaymentRepository(pool *pgxpool.Pool) *PaymentRepository {
	return &PaymentRepository{pool: pool}
}

var _ port.PaymentRepository = (*PaymentRepository)(nil)

func (r *PaymentRepository) Create(ctx context.Context, p *domain.Payment) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO billing.payments (
			id, invoice_id, customer_id, amount, payment_method,
			gateway_transaction_id, payment_date, confirmed_by, status,
			notes, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$11)
	`,
		p.ID, p.InvoiceID, p.CustomerID, p.Amount, p.PaymentMethod,
		nullableString(p.GatewayTransactionID), p.PaymentDate, p.ConfirmedBy,
		string(p.Status), nullableString(p.Notes), p.CreatedAt,
	)
	return mapDBError(err, "payment.create", "create payment")
}

func (r *PaymentRepository) SumConfirmedForInvoice(ctx context.Context, invoiceID uuid.UUID) (float64, error) {
	var sum *float64
	if err := r.pool.QueryRow(ctx, `
		SELECT SUM(amount) FROM billing.payments
		 WHERE invoice_id = $1 AND status = 'confirmed'
	`, invoiceID).Scan(&sum); err != nil {
		return 0, derrors.Wrap(derrors.KindInternal, "db.payment_sum", "sum payments", err)
	}
	if sum == nil {
		return 0, nil
	}
	return *sum, nil
}
