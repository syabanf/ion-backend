package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/billing/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type CustomerOTPRepository struct {
	pool *pgxpool.Pool
}

func NewCustomerOTPRepository(pool *pgxpool.Pool) *CustomerOTPRepository {
	return &CustomerOTPRepository{pool: pool}
}

var _ port.CustomerOTPRepository = (*CustomerOTPRepository)(nil)

func (r *CustomerOTPRepository) Create(ctx context.Context, rec *port.CustomerOTPRecord) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO crm.customer_portal_otp
		  (id, customer_id, purpose, otp_hash, attempts,
		   verified_at, expires_at, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
	`,
		rec.ID, rec.CustomerID, rec.Purpose, rec.OTPHash, rec.Attempts,
		rec.VerifiedAt, rec.ExpiresAt, rec.CreatedAt,
	)
	if err != nil {
		return mapDBError(err, "portal_otp.create", "create OTP")
	}
	return nil
}

// FindActive returns the most-recent non-expired, non-verified row for
// (customer, purpose). When several have been minted in the TTL window
// we trust the latest one — earlier rows stay in the table as audit and
// the janitor eventually drops them.
func (r *CustomerOTPRepository) FindActive(ctx context.Context, customerID uuid.UUID, purpose string) (*port.CustomerOTPRecord, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, customer_id, purpose, otp_hash, attempts,
		       verified_at, expires_at, created_at
		FROM crm.customer_portal_otp
		WHERE customer_id = $1 AND purpose = $2
		  AND verified_at IS NULL
		  AND expires_at > NOW()
		ORDER BY created_at DESC LIMIT 1
	`, customerID, purpose)
	var rec port.CustomerOTPRecord
	err := row.Scan(&rec.ID, &rec.CustomerID, &rec.Purpose, &rec.OTPHash,
		&rec.Attempts, &rec.VerifiedAt, &rec.ExpiresAt, &rec.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "portal_otp.find", "find OTP", err)
	}
	return &rec, nil
}

func (r *CustomerOTPRepository) MarkVerified(ctx context.Context, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE crm.customer_portal_otp SET verified_at = NOW() WHERE id = $1`, id)
	if err != nil {
		return mapDBError(err, "portal_otp.verify", "mark verified")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("portal_otp.not_found", "otp not found")
	}
	return nil
}

func (r *CustomerOTPRepository) IncrementAttempts(ctx context.Context, id uuid.UUID) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE crm.customer_portal_otp SET attempts = attempts + 1 WHERE id = $1`, id)
	if err != nil {
		return mapDBError(err, "portal_otp.attempts", "increment attempts")
	}
	return nil
}

func (r *CustomerOTPRepository) DeleteExpired(ctx context.Context, before time.Time) (int, error) {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM crm.customer_portal_otp WHERE expires_at < $1`, before)
	if err != nil {
		return 0, mapDBError(err, "portal_otp.cleanup", "delete expired")
	}
	return int(tag.RowsAffected()), nil
}

// CountRequestsSince returns how many OTP rows have been created for
// this (customer, purpose) at or after `since`. Used by the per-
// customer rate limit on the request leg: even if a single IP can't
// brute-force, a distributed attacker could otherwise mint OTPs for a
// known customer_number indefinitely.
func (r *CustomerOTPRepository) CountRequestsSince(ctx context.Context, customerID uuid.UUID, purpose string, since time.Time) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM crm.customer_portal_otp
		 WHERE customer_id = $1 AND purpose = $2 AND created_at >= $3
	`, customerID, purpose, since).Scan(&n)
	if err != nil {
		return 0, derrors.Wrap(derrors.KindInternal, "portal_otp.count",
			"count recent OTPs", err)
	}
	return n, nil
}
