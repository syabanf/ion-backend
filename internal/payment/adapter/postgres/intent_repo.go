package postgres

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/payment/domain"
	"github.com/ion-core/backend/internal/payment/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// PaymentIntentRepository implements port.PaymentIntentRepository
// against `payment.payment_intents`.
type PaymentIntentRepository struct {
	pool *pgxpool.Pool
}

func NewPaymentIntentRepository(pool *pgxpool.Pool) *PaymentIntentRepository {
	return &PaymentIntentRepository{pool: pool}
}

var _ port.PaymentIntentRepository = (*PaymentIntentRepository)(nil)

const intentCols = `
	id, invoice_id, customer_id, gateway_id,
	amount, COALESCE(currency, 'IDR'), status,
	routing_decision::text,
	idempotency_key, external_payment_ref,
	paid_at, expired_at, cancelled_at,
	COALESCE(failure_code, ''), COALESCE(failure_reason, ''),
	COALESCE(refunded_amount, 0),
	created_at, updated_at
`

// CreateOrFetchByIdempotency runs the create + dedup dance. Without an
// idempotency_key the row is created unconditionally (FRESH=true). With
// a key, conflict short-circuits to the existing row.
func (r *PaymentIntentRepository) CreateOrFetchByIdempotency(
	ctx context.Context,
	intent *domain.PaymentIntent,
) (bool, *domain.PaymentIntent, error) {
	if intent == nil {
		return false, nil, derrors.Validation("intent.nil", "intent is nil")
	}
	var routingJSON any
	if intent.RoutingDecision != nil {
		b, _ := json.Marshal(intent.RoutingDecision)
		routingJSON = string(b)
	}
	if intent.IdempotencyKey == nil || *intent.IdempotencyKey == "" {
		// No idempotency_key — plain INSERT.
		_, err := r.pool.Exec(ctx, `
			INSERT INTO payment.payment_intents
				(id, invoice_id, customer_id, gateway_id,
				 amount, currency, status, routing_decision,
				 idempotency_key, external_payment_ref,
				 paid_at, expired_at, cancelled_at,
				 failure_code, failure_reason, refunded_amount,
				 created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)
		`,
			intent.ID, intent.InvoiceID, intent.CustomerID, intent.GatewayID,
			intent.Amount, intent.Currency, string(intent.Status),
			routingJSON,
			nil, intent.ExternalPaymentRef,
			intent.PaidAt, intent.ExpiredAt, intent.CancelledAt,
			nullableString(intent.FailureCode), nullableString(intent.FailureReason),
			intent.RefundedAmount,
			intent.CreatedAt, intent.UpdatedAt,
		)
		if err != nil {
			return false, nil, mapDBError(err, "payment_intent", "insert payment intent")
		}
		return true, intent, nil
	}
	// With idempotency_key — INSERT ... ON CONFLICT DO NOTHING, then
	// re-fetch the canonical row if we collided.
	tag, err := r.pool.Exec(ctx, `
		INSERT INTO payment.payment_intents
			(id, invoice_id, customer_id, gateway_id,
			 amount, currency, status, routing_decision,
			 idempotency_key, external_payment_ref,
			 paid_at, expired_at, cancelled_at,
			 failure_code, failure_reason, refunded_amount,
			 created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)
		ON CONFLICT (idempotency_key) DO NOTHING
	`,
		intent.ID, intent.InvoiceID, intent.CustomerID, intent.GatewayID,
		intent.Amount, intent.Currency, string(intent.Status),
		routingJSON,
		*intent.IdempotencyKey, intent.ExternalPaymentRef,
		intent.PaidAt, intent.ExpiredAt, intent.CancelledAt,
		nullableString(intent.FailureCode), nullableString(intent.FailureReason),
		intent.RefundedAmount,
		intent.CreatedAt, intent.UpdatedAt,
	)
	if err != nil {
		return false, nil, mapDBError(err, "payment_intent", "insert payment intent")
	}
	if tag.RowsAffected() == 1 {
		return true, intent, nil
	}
	// Replay — refetch the canonical row from the idempotency_key.
	row := r.pool.QueryRow(ctx,
		`SELECT `+intentCols+` FROM payment.payment_intents WHERE idempotency_key = $1`,
		*intent.IdempotencyKey,
	)
	persisted, err := scanIntent(row)
	if err != nil {
		return false, nil, err
	}
	return false, &persisted, nil
}

func (r *PaymentIntentRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.PaymentIntent, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+intentCols+` FROM payment.payment_intents WHERE id = $1`, id)
	i, err := scanIntent(row)
	if err != nil {
		return nil, err
	}
	return &i, nil
}

func (r *PaymentIntentRepository) FindByExternalRef(ctx context.Context, ref string) (*domain.PaymentIntent, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+intentCols+` FROM payment.payment_intents WHERE external_payment_ref = $1`, ref)
	i, err := scanIntent(row)
	if err != nil {
		return nil, err
	}
	return &i, nil
}

func (r *PaymentIntentRepository) List(ctx context.Context, f port.IntentListFilter) ([]domain.PaymentIntent, int, error) {
	var wh []string
	var args []any
	if f.InvoiceID != nil {
		args = append(args, *f.InvoiceID)
		wh = append(wh, fmt.Sprintf("invoice_id = $%d", len(args)))
	}
	if f.CustomerID != nil {
		args = append(args, *f.CustomerID)
		wh = append(wh, fmt.Sprintf("customer_id = $%d", len(args)))
	}
	if f.Status != "" {
		args = append(args, f.Status)
		wh = append(wh, fmt.Sprintf("status = $%d", len(args)))
	}
	where := ""
	if len(wh) > 0 {
		where = " WHERE " + strings.Join(wh, " AND ")
	}
	var total int
	if err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM payment.payment_intents`+where, args...,
	).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.payment_intent_count", "count intents", err)
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
	sql := `SELECT ` + intentCols + ` FROM payment.payment_intents` + where +
		` ORDER BY created_at DESC LIMIT $` + fmt.Sprint(len(args)-1) +
		` OFFSET $` + fmt.Sprint(len(args))
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.payment_intent_list", "list intents", err)
	}
	defer rows.Close()
	out := []domain.PaymentIntent{}
	for rows.Next() {
		i, err := scanIntent(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, i)
	}
	return out, total, nil
}

func (r *PaymentIntentRepository) Update(ctx context.Context, intent *domain.PaymentIntent) error {
	var routingJSON any
	if intent.RoutingDecision != nil {
		b, _ := json.Marshal(intent.RoutingDecision)
		routingJSON = string(b)
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE payment.payment_intents
		SET gateway_id = $2,
		    status = $3,
		    routing_decision = COALESCE($4::jsonb, routing_decision),
		    external_payment_ref = $5,
		    paid_at = $6,
		    expired_at = $7,
		    cancelled_at = $8,
		    failure_code = $9,
		    failure_reason = $10,
		    refunded_amount = $11,
		    updated_at = NOW()
		WHERE id = $1
	`,
		intent.ID, intent.GatewayID, string(intent.Status),
		routingJSON,
		intent.ExternalPaymentRef,
		intent.PaidAt, intent.ExpiredAt, intent.CancelledAt,
		nullableString(intent.FailureCode), nullableString(intent.FailureReason),
		intent.RefundedAmount,
	)
	if err != nil {
		return mapDBError(err, "payment_intent", "update payment intent")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("payment_intent.not_found", "payment intent not found")
	}
	return nil
}

func (r *PaymentIntentRepository) ListPendingOlderThan(ctx context.Context, cutoff time.Time, limit int) ([]domain.PaymentIntent, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+intentCols+`
		FROM payment.payment_intents
		WHERE status = 'pending' AND created_at < $1
		ORDER BY created_at ASC
		LIMIT $2
	`, cutoff, limit)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.payment_intent_stale", "list stale intents", err)
	}
	defer rows.Close()
	out := []domain.PaymentIntent{}
	for rows.Next() {
		i, err := scanIntent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, nil
}

func scanIntent(row pgx.Row) (domain.PaymentIntent, error) {
	var i domain.PaymentIntent
	var status string
	var routingJSON *string
	err := row.Scan(
		&i.ID, &i.InvoiceID, &i.CustomerID, &i.GatewayID,
		&i.Amount, &i.Currency, &status,
		&routingJSON,
		&i.IdempotencyKey, &i.ExternalPaymentRef,
		&i.PaidAt, &i.ExpiredAt, &i.CancelledAt,
		&i.FailureCode, &i.FailureReason,
		&i.RefundedAmount,
		&i.CreatedAt, &i.UpdatedAt,
	)
	if stderrors.Is(err, pgx.ErrNoRows) {
		return domain.PaymentIntent{}, derrors.NotFound(
			"payment_intent.not_found", "payment intent not found")
	}
	if err != nil {
		return domain.PaymentIntent{}, derrors.Wrap(
			derrors.KindInternal, "db.payment_intent_scan", "scan intent", err)
	}
	i.Status = domain.PaymentStatus(status)
	if routingJSON != nil && *routingJSON != "" {
		var d domain.RouteDecision
		if jerr := json.Unmarshal([]byte(*routingJSON), &d); jerr == nil {
			i.RoutingDecision = &d
		}
	}
	return i, nil
}
