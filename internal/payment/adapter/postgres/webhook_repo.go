package postgres

import (
	"context"
	stderrors "errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/payment/domain"
	"github.com/ion-core/backend/internal/payment/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// PaymentWebhookRepository implements port.PaymentWebhookRepository
// against `payment.payment_webhooks`. The (gateway_id, external_event_id)
// UNIQUE constraint is the dedup guard — on a redelivery the row stays
// untouched and the usecase flips to Duplicate without replaying side
// effects on the intent.
type PaymentWebhookRepository struct {
	pool *pgxpool.Pool
}

func NewPaymentWebhookRepository(pool *pgxpool.Pool) *PaymentWebhookRepository {
	return &PaymentWebhookRepository{pool: pool}
}

var _ port.PaymentWebhookRepository = (*PaymentWebhookRepository)(nil)

const webhookCols = `
	id, gateway_id, external_event_id, payload::text,
	signature_valid, status, payment_intent_id,
	COALESCE(error_msg, ''),
	received_at, processed_at
`

func (r *PaymentWebhookRepository) CreateOrFetchByDedup(
	ctx context.Context, w *domain.PaymentWebhook,
) (bool, *domain.PaymentWebhook, error) {
	if w == nil {
		return false, nil, derrors.Validation("webhook.nil", "webhook is nil")
	}
	tag, err := r.pool.Exec(ctx, `
		INSERT INTO payment.payment_webhooks
			(id, gateway_id, external_event_id, payload,
			 signature_valid, status, payment_intent_id,
			 error_msg, received_at, processed_at)
		VALUES ($1, $2, $3, $4::jsonb,
		        $5, $6, $7,
		        $8, $9, $10)
		ON CONFLICT (gateway_id, external_event_id) DO NOTHING
	`,
		w.ID, w.GatewayID, w.ExternalEventID, string(w.Payload),
		w.SignatureValid, string(w.Status), w.PaymentIntentID,
		nullableString(w.ErrorMsg), w.ReceivedAt, w.ProcessedAt,
	)
	if err != nil {
		return false, nil, mapDBError(err, "payment_webhook", "insert payment webhook")
	}
	if tag.RowsAffected() == 1 {
		return true, w, nil
	}
	// Conflict — refetch the canonical (already-recorded) row.
	row := r.pool.QueryRow(ctx,
		`SELECT `+webhookCols+`
		 FROM payment.payment_webhooks
		 WHERE gateway_id = $1 AND external_event_id = $2`,
		w.GatewayID, w.ExternalEventID,
	)
	persisted, err := scanWebhook(row)
	if err != nil {
		return false, nil, err
	}
	return false, &persisted, nil
}

func (r *PaymentWebhookRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.PaymentWebhook, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+webhookCols+` FROM payment.payment_webhooks WHERE id = $1`, id)
	w, err := scanWebhook(row)
	if err != nil {
		return nil, err
	}
	return &w, nil
}

func (r *PaymentWebhookRepository) Update(ctx context.Context, w *domain.PaymentWebhook) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE payment.payment_webhooks
		SET signature_valid = $2,
		    status = $3,
		    payment_intent_id = $4,
		    error_msg = $5,
		    processed_at = $6
		WHERE id = $1
	`,
		w.ID, w.SignatureValid, string(w.Status),
		w.PaymentIntentID, nullableString(w.ErrorMsg), w.ProcessedAt,
	)
	if err != nil {
		return mapDBError(err, "payment_webhook", "update payment webhook")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("payment_webhook.not_found", "payment webhook not found")
	}
	return nil
}

func scanWebhook(row pgx.Row) (domain.PaymentWebhook, error) {
	var w domain.PaymentWebhook
	var status string
	var payloadStr string
	err := row.Scan(
		&w.ID, &w.GatewayID, &w.ExternalEventID, &payloadStr,
		&w.SignatureValid, &status, &w.PaymentIntentID,
		&w.ErrorMsg,
		&w.ReceivedAt, &w.ProcessedAt,
	)
	if stderrors.Is(err, pgx.ErrNoRows) {
		return domain.PaymentWebhook{}, derrors.NotFound(
			"payment_webhook.not_found", "payment webhook not found")
	}
	if err != nil {
		return domain.PaymentWebhook{}, derrors.Wrap(
			derrors.KindInternal, "db.payment_webhook_scan", "scan webhook", err)
	}
	w.Status = domain.WebhookStatus(status)
	w.Payload = []byte(payloadStr)
	return w, nil
}
