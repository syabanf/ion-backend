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

// ResellerAccountRepository implements port.ResellerAccountRepository
// against `reseller.reseller_accounts`.
type ResellerAccountRepository struct {
	pool *pgxpool.Pool
}

func NewResellerAccountRepository(pool *pgxpool.Pool) *ResellerAccountRepository {
	return &ResellerAccountRepository{pool: pool}
}

var _ port.ResellerAccountRepository = (*ResellerAccountRepository)(nil)

const accountCols = `
	id, parent_subsidiary_id, name,
	COALESCE(npwp, ''), COALESCE(contact_email, ''), COALESCE(contact_phone, ''),
	status, margin_pct, credit_limit, balance,
	created_at, updated_at,
	approved_at, approved_by
`

func (r *ResellerAccountRepository) Create(ctx context.Context, a *domain.ResellerAccount) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO reseller.reseller_accounts
			(id, parent_subsidiary_id, name, npwp, contact_email, contact_phone,
			 status, margin_pct, credit_limit, balance,
			 created_at, updated_at, approved_at, approved_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
	`,
		a.ID, a.ParentSubsidiaryID, a.Name,
		nullableString(a.NPWP), nullableString(a.ContactEmail), nullableString(a.ContactPhone),
		string(a.Status), a.MarginPct, a.CreditLimit, a.Balance,
		a.CreatedAt, a.UpdatedAt, a.ApprovedAt, a.ApprovedBy,
	)
	if err != nil {
		return mapDBError(err, "reseller_account", "insert reseller account")
	}
	return nil
}

func (r *ResellerAccountRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.ResellerAccount, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+accountCols+` FROM reseller.reseller_accounts WHERE id = $1`, id)
	a, err := scanAccount(row)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func (r *ResellerAccountRepository) List(ctx context.Context, f port.ResellerListFilter) ([]domain.ResellerAccount, int, error) {
	var wh []string
	var args []any
	if f.Status != "" {
		args = append(args, f.Status)
		wh = append(wh, fmt.Sprintf("status = $%d", len(args)))
	}
	if f.ParentSubsidiaryID != nil {
		args = append(args, *f.ParentSubsidiaryID)
		wh = append(wh, fmt.Sprintf("parent_subsidiary_id = $%d", len(args)))
	}
	where := ""
	if len(wh) > 0 {
		where = " WHERE " + strings.Join(wh, " AND ")
	}

	var total int
	if err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM reseller.reseller_accounts`+where, args...,
	).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.reseller_account_count", "count accounts", err)
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
	sql := `SELECT ` + accountCols + ` FROM reseller.reseller_accounts` + where +
		` ORDER BY created_at DESC LIMIT $` + fmt.Sprint(len(args)-1) +
		` OFFSET $` + fmt.Sprint(len(args))

	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.reseller_account_list", "list accounts", err)
	}
	defer rows.Close()

	out := []domain.ResellerAccount{}
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, a)
	}
	return out, total, nil
}

func (r *ResellerAccountRepository) UpdateStatus(ctx context.Context, a *domain.ResellerAccount) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE reseller.reseller_accounts
		SET status = $2,
		    approved_at = $3,
		    approved_by = $4,
		    updated_at = NOW()
		WHERE id = $1
	`,
		a.ID, string(a.Status), a.ApprovedAt, a.ApprovedBy,
	)
	if err != nil {
		return mapDBError(err, "reseller_account", "update reseller account status")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("reseller_account.not_found", "reseller account not found")
	}
	return nil
}

func scanAccount(row pgx.Row) (domain.ResellerAccount, error) {
	var a domain.ResellerAccount
	var status string
	err := row.Scan(
		&a.ID, &a.ParentSubsidiaryID, &a.Name,
		&a.NPWP, &a.ContactEmail, &a.ContactPhone,
		&status, &a.MarginPct, &a.CreditLimit, &a.Balance,
		&a.CreatedAt, &a.UpdatedAt,
		&a.ApprovedAt, &a.ApprovedBy,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ResellerAccount{}, derrors.NotFound("reseller_account.not_found", "reseller account not found")
	}
	if err != nil {
		return domain.ResellerAccount{}, derrors.Wrap(derrors.KindInternal, "db.reseller_account_scan", "scan account", err)
	}
	a.Status = domain.ResellerStatus(status)
	return a, nil
}

// nullableString returns nil for the empty string so we don't write
// empty strings into nullable TEXT columns. The scan side uses
// COALESCE(..., '') to read them back as empty strings — keeps the
// domain free of string-pointer juggling for free-form contact fields.
func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
