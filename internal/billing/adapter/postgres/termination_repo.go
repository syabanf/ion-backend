package postgres

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/billing/domain"
	"github.com/ion-core/backend/internal/billing/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type TerminationRepository struct {
	pool *pgxpool.Pool
}

func NewTerminationRepository(pool *pgxpool.Pool) *TerminationRepository {
	return &TerminationRepository{pool: pool}
}

var _ port.TerminationRequestRepository = (*TerminationRepository)(nil)

const termCols = `id, customer_id, order_id, kind, status, COALESCE(reason,''),
       requested_by_user_id, final_invoice_id, penalty_amount,
       outstanding_at_request, wo_id, requested_at, completed_at, COALESCE(notes,'')`

func scanTerm(rows pgx.Row, t *domain.TerminationRequest) error {
	var (
		kind, status, reason, notes string
	)
	if err := rows.Scan(&t.ID, &t.CustomerID, &t.OrderID, &kind, &status, &reason,
		&t.RequestedByUserID, &t.FinalInvoiceID, &t.PenaltyAmount,
		&t.OutstandingAtRequest, &t.WOID, &t.RequestedAt, &t.CompletedAt, &notes); err != nil {
		return err
	}
	t.Kind = domain.TerminationKind(kind)
	t.Status = domain.TerminationStatus(status)
	t.Reason = reason
	t.Notes = notes
	return nil
}

func (r *TerminationRepository) Create(ctx context.Context, t *domain.TerminationRequest) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO billing.termination_requests
		  (id, customer_id, order_id, kind, status, reason,
		   requested_by_user_id, final_invoice_id, penalty_amount,
		   outstanding_at_request, wo_id, requested_at, completed_at, notes)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
	`,
		t.ID, t.CustomerID, t.OrderID, string(t.Kind), string(t.Status),
		nullableString(t.Reason), t.RequestedByUserID, t.FinalInvoiceID,
		t.PenaltyAmount, t.OutstandingAtRequest, t.WOID,
		t.RequestedAt, t.CompletedAt, nullableString(t.Notes),
	)
	if err != nil {
		return mapDBError(err, "termination.create", "create termination request")
	}
	return nil
}

func (r *TerminationRepository) Update(ctx context.Context, t *domain.TerminationRequest) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE billing.termination_requests SET
		  status = $2,
		  final_invoice_id = $3,
		  penalty_amount = $4,
		  outstanding_at_request = $5,
		  wo_id = $6,
		  completed_at = $7,
		  notes = $8
		WHERE id = $1
	`,
		t.ID, string(t.Status), t.FinalInvoiceID, t.PenaltyAmount,
		t.OutstandingAtRequest, t.WOID, t.CompletedAt, nullableString(t.Notes),
	)
	if err != nil {
		return mapDBError(err, "termination.update", "update termination request")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("termination.not_found", "termination request not found")
	}
	return nil
}

func (r *TerminationRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.TerminationRequest, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+termCols+` FROM billing.termination_requests WHERE id = $1`, id)
	var t domain.TerminationRequest
	if err := scanTerm(row, &t); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, derrors.NotFound("termination.not_found", "termination request not found")
		}
		return nil, derrors.Wrap(derrors.KindInternal, "termination.find", "find termination", err)
	}
	return &t, nil
}

func (r *TerminationRepository) FindByWOID(ctx context.Context, woID uuid.UUID) (*domain.TerminationRequest, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+termCols+` FROM billing.termination_requests WHERE wo_id = $1
		 ORDER BY requested_at DESC LIMIT 1`, woID)
	var t domain.TerminationRequest
	if err := scanTerm(row, &t); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, derrors.Wrap(derrors.KindInternal, "termination.by_wo", "find by wo", err)
	}
	return &t, nil
}

func (r *TerminationRepository) FindOpenForCustomer(ctx context.Context, customerID uuid.UUID) (*domain.TerminationRequest, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT `+termCols+`
		FROM billing.termination_requests
		WHERE customer_id = $1
		  AND status NOT IN ('completed','cancelled')
		ORDER BY requested_at DESC LIMIT 1
	`, customerID)
	var t domain.TerminationRequest
	if err := scanTerm(row, &t); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, derrors.Wrap(derrors.KindInternal, "termination.open", "find open termination", err)
	}
	return &t, nil
}

func (r *TerminationRepository) List(ctx context.Context, f port.TerminationRequestFilter) ([]domain.TerminationRequest, int, error) {
	var (
		args  []any
		conds []string
	)
	if f.CustomerID != nil {
		args = append(args, *f.CustomerID)
		conds = append(conds, "customer_id = $"+itoa(len(args)))
	}
	if f.Kind != "" {
		args = append(args, f.Kind)
		conds = append(conds, "kind = $"+itoa(len(args)))
	}
	if f.Status != "" {
		args = append(args, f.Status)
		conds = append(conds, "status = $"+itoa(len(args)))
	}
	if f.FinalInvoiceID != nil {
		args = append(args, *f.FinalInvoiceID)
		conds = append(conds, "final_invoice_id = $"+itoa(len(args)))
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	var total int
	if err := r.pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM billing.termination_requests"+where, args...,
	).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "termination.count", "count terminations", err)
	}
	if f.Limit <= 0 {
		f.Limit = 50
	}
	sql := `SELECT ` + termCols + ` FROM billing.termination_requests` + where +
		" ORDER BY requested_at DESC LIMIT $" + itoa(len(args)+1) + " OFFSET $" + itoa(len(args)+2)
	args = append(args, f.Limit, f.Offset)
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "termination.list", "list terminations", err)
	}
	defer rows.Close()
	out := []domain.TerminationRequest{}
	for rows.Next() {
		var t domain.TerminationRequest
		if err := scanTerm(rows, &t); err != nil {
			return nil, 0, derrors.Wrap(derrors.KindInternal, "termination.scan", "scan termination", err)
		}
		out = append(out, t)
	}
	return out, total, nil
}
