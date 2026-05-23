package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/reseller/domain"
	"github.com/ion-core/backend/internal/reseller/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// SubscriberRepository implements port.SubscriberRepository against
// `reseller.subscribers`. Tenant isolation contract: every
// List/Find/Count REFUSES uuid.Nil for the tenant filter — a missing
// reseller_account_id is a defect, not a 0-row result.
type SubscriberRepository struct {
	pool *pgxpool.Pool
}

func NewSubscriberRepository(pool *pgxpool.Pool) *SubscriberRepository {
	return &SubscriberRepository{pool: pool}
}

var _ port.SubscriberRepository = (*SubscriberRepository)(nil)

const subscriberCols = `
	id, reseller_account_id, customer_name,
	COALESCE(customer_email, ''), COALESCE(customer_phone, ''),
	COALESCE(address_line, ''),
	sub_area_id, service_plan_id,
	COALESCE(monthly_fee, 0),
	status, COALESCE(notes, ''),
	activated_at, suspended_at, terminated_at,
	created_at, updated_at
`

func (r *SubscriberRepository) Create(ctx context.Context, s *domain.Subscriber) error {
	if s.ResellerAccountID == uuid.Nil {
		return derrors.Validation("subscriber.reseller_required", "reseller_account_id is required")
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO reseller.subscribers
			(id, reseller_account_id, customer_name, customer_email, customer_phone,
			 address_line, sub_area_id, service_plan_id, monthly_fee, status, notes,
			 activated_at, suspended_at, terminated_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
	`,
		s.ID, s.ResellerAccountID, s.CustomerName,
		nullableString(s.CustomerEmail), nullableString(s.CustomerPhone),
		nullableString(s.AddressLine), s.SubAreaID, s.ServicePlanID,
		s.MonthlyFee, string(s.Status), nullableString(s.Notes),
		s.ActivatedAt, s.SuspendedAt, s.TerminatedAt,
		s.CreatedAt, s.UpdatedAt,
	)
	if err != nil {
		return mapDBError(err, "subscriber", "insert subscriber")
	}
	return nil
}

func (r *SubscriberRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.Subscriber, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+subscriberCols+` FROM reseller.subscribers WHERE id = $1`, id)
	s, err := scanSubscriber(row)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// FindForReseller is the tenant-scoped lookup. The WHERE clause pins
// both id and reseller_account_id so a cross-tenant id surfaces as
// NotFound rather than leaking the row. This is the load-bearing
// tenant-isolation primitive.
func (r *SubscriberRepository) FindForReseller(ctx context.Context, resellerID, id uuid.UUID) (*domain.Subscriber, error) {
	if resellerID == uuid.Nil {
		return nil, derrors.Validation("subscriber.reseller_required", "reseller_account_id is required")
	}
	row := r.pool.QueryRow(ctx,
		`SELECT `+subscriberCols+` FROM reseller.subscribers
		 WHERE reseller_account_id = $1 AND id = $2`,
		resellerID, id)
	s, err := scanSubscriber(row)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *SubscriberRepository) List(ctx context.Context, f port.SubscriberListFilter) ([]domain.Subscriber, int, error) {
	if f.ResellerAccountID == uuid.Nil {
		return nil, 0, derrors.Validation("subscriber.tenant_filter_required", "reseller_account_id filter is required")
	}
	args := []any{f.ResellerAccountID}
	wh := []string{"reseller_account_id = $1"}
	if f.Status != "" {
		args = append(args, f.Status)
		wh = append(wh, fmt.Sprintf("status = $%d", len(args)))
	}
	where := " WHERE " + strings.Join(wh, " AND ")

	var total int
	if err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM reseller.subscribers`+where, args...,
	).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.subscriber_count", "count subscribers", err)
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
	sql := `SELECT ` + subscriberCols + ` FROM reseller.subscribers` + where +
		` ORDER BY created_at DESC LIMIT $` + fmt.Sprint(len(args)-1) +
		` OFFSET $` + fmt.Sprint(len(args))

	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.subscriber_list", "list subscribers", err)
	}
	defer rows.Close()
	out := []domain.Subscriber{}
	for rows.Next() {
		s, err := scanSubscriber(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, s)
	}
	return out, total, nil
}

// Count is a focused helper for the dashboard tiles — same tenant
// guard as List, but skips the SELECT-and-scan path for a cheap count.
func (r *SubscriberRepository) Count(ctx context.Context, f port.SubscriberListFilter) (int, error) {
	if f.ResellerAccountID == uuid.Nil {
		return 0, derrors.Validation("subscriber.tenant_filter_required", "reseller_account_id filter is required")
	}
	args := []any{f.ResellerAccountID}
	wh := []string{"reseller_account_id = $1"}
	if f.Status != "" {
		args = append(args, f.Status)
		wh = append(wh, fmt.Sprintf("status = $%d", len(args)))
	}
	where := " WHERE " + strings.Join(wh, " AND ")
	var total int
	if err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM reseller.subscribers`+where, args...,
	).Scan(&total); err != nil {
		return 0, derrors.Wrap(derrors.KindInternal, "db.subscriber_count", "count subscribers", err)
	}
	return total, nil
}

// Update persists every mutable field (status timestamps stay on
// UpdateStatus). We deliberately do NOT touch reseller_account_id —
// re-tenanting a row is impossible by design.
func (r *SubscriberRepository) Update(ctx context.Context, s *domain.Subscriber) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE reseller.subscribers
		SET customer_name = $2,
		    customer_email = $3,
		    customer_phone = $4,
		    address_line = $5,
		    sub_area_id = $6,
		    service_plan_id = $7,
		    monthly_fee = $8,
		    notes = $9,
		    updated_at = $10
		WHERE id = $1 AND reseller_account_id = $11
	`,
		s.ID, s.CustomerName,
		nullableString(s.CustomerEmail), nullableString(s.CustomerPhone),
		nullableString(s.AddressLine), s.SubAreaID, s.ServicePlanID,
		s.MonthlyFee, nullableString(s.Notes), s.UpdatedAt,
		s.ResellerAccountID,
	)
	if err != nil {
		return mapDBError(err, "subscriber", "update subscriber")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("subscriber.not_found", "subscriber not found")
	}
	return nil
}

// UpdateStatus persists status + per-status timestamps. Tenant pinned
// in the WHERE — a tampered call with a wrong tenant id surfaces as
// NotFound rather than a cross-tenant mutation.
func (r *SubscriberRepository) UpdateStatus(ctx context.Context, s *domain.Subscriber) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE reseller.subscribers
		SET status = $2,
		    suspended_at = $3,
		    terminated_at = $4,
		    updated_at = $5
		WHERE id = $1 AND reseller_account_id = $6
	`,
		s.ID, string(s.Status), s.SuspendedAt, s.TerminatedAt, s.UpdatedAt, s.ResellerAccountID,
	)
	if err != nil {
		return mapDBError(err, "subscriber", "update subscriber status")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("subscriber.not_found", "subscriber not found")
	}
	return nil
}

func scanSubscriber(row pgx.Row) (domain.Subscriber, error) {
	var s domain.Subscriber
	var status string
	err := row.Scan(
		&s.ID, &s.ResellerAccountID, &s.CustomerName,
		&s.CustomerEmail, &s.CustomerPhone,
		&s.AddressLine,
		&s.SubAreaID, &s.ServicePlanID,
		&s.MonthlyFee,
		&status, &s.Notes,
		&s.ActivatedAt, &s.SuspendedAt, &s.TerminatedAt,
		&s.CreatedAt, &s.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Subscriber{}, derrors.NotFound("subscriber.not_found", "subscriber not found")
	}
	if err != nil {
		return domain.Subscriber{}, derrors.Wrap(derrors.KindInternal, "db.subscriber_scan", "scan subscriber", err)
	}
	s.Status = domain.SubscriberStatus(status)
	return s, nil
}
