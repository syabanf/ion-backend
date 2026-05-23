package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/enterprise/domain"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type InternalTransactionRepository struct {
	pool *pgxpool.Pool
}

func NewInternalTransactionRepository(pool *pgxpool.Pool) *InternalTransactionRepository {
	return &InternalTransactionRepository{pool: pool}
}

const internalTxCols = `
	id, boq_version_id, boq_line_id, quotation_id, vendor_company_id,
	sell_amount, cost_amount, margin_amount, currency,
	recognized_at, COALESCE(notes, ''), created_at,
	COALESCE(source_event, ''), superseded_at
`

// CreateBatch inserts all rows in one tx with ON CONFLICT DO NOTHING so
// re-firing the BOQ approval hook (e.g. due to a retry) is idempotent
// via the unique index on boq_line_id.
func (r *InternalTransactionRepository) CreateBatch(ctx context.Context, txs []domain.InternalTransaction) error {
	if len(txs) == 0 {
		return nil
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "db.internal_tx_begin", "begin tx", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, t := range txs {
		// Default source_event when caller didn't set one — keeps
		// pre-Wave-101 call sites compatible without forcing every
		// place to thread the constant through.
		source := t.SourceEvent
		if source == "" {
			source = domain.InternalTransactionSourceBOQApproval
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO enterprise.internal_transactions
				(id, boq_version_id, boq_line_id, quotation_id, vendor_company_id,
				 sell_amount, cost_amount, currency, recognized_at, notes, created_at,
				 source_event)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
			ON CONFLICT (boq_line_id) DO NOTHING
		`,
			t.ID, t.BOQVersionID, t.BOQLineID, t.QuotationID, t.VendorCompanyID,
			t.SellAmount, t.CostAmount, t.Currency, t.RecognizedAt, t.Notes, t.CreatedAt,
			source,
		); err != nil {
			return mapDBError(err, "internal_transaction", "insert")
		}
	}
	return tx.Commit(ctx)
}

// ListByBOQ returns all rows recognized against a BOQ version. Powers
// the "internal revenue" surface on the BOQ detail page.
func (r *InternalTransactionRepository) ListByBOQ(ctx context.Context, boqVersionID uuid.UUID) ([]domain.InternalTransaction, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+internalTxCols+` FROM enterprise.internal_transactions WHERE boq_version_id = $1 ORDER BY recognized_at DESC`,
		boqVersionID,
	)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.internal_tx_list_boq", "list", err)
	}
	defer rows.Close()
	out := []domain.InternalTransaction{}
	for rows.Next() {
		t, err := scanInternalTx(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// ListByVendor returns rows for a given vendor company, optionally
// filtered by date range. Powers the per-vendor ledger view.
func (r *InternalTransactionRepository) ListByVendor(
	ctx context.Context,
	vendorCompanyID uuid.UUID,
	from, to *string,
	limit, offset int,
) ([]domain.InternalTransaction, int, float64, float64, error) {
	if limit <= 0 {
		limit = 100
	}
	wh := []string{"vendor_company_id = $1"}
	args := []any{vendorCompanyID}
	if from != nil && *from != "" {
		args = append(args, *from)
		wh = append(wh, fmt.Sprintf("recognized_at >= $%d", len(args)))
	}
	if to != nil && *to != "" {
		args = append(args, *to)
		wh = append(wh, fmt.Sprintf("recognized_at <= $%d", len(args)))
	}
	where := " WHERE " + strings.Join(wh, " AND ")

	var (
		total       int
		sumSell, sumCost float64
	)
	if err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*), COALESCE(SUM(sell_amount), 0), COALESCE(SUM(cost_amount), 0)
		 FROM enterprise.internal_transactions`+where, args...,
	).Scan(&total, &sumSell, &sumCost); err != nil {
		return nil, 0, 0, 0, derrors.Wrap(derrors.KindInternal, "db.internal_tx_aggregate", "agg", err)
	}

	args = append(args, limit, offset)
	q := `SELECT ` + internalTxCols +
		` FROM enterprise.internal_transactions` + where +
		` ORDER BY recognized_at DESC LIMIT $` + fmt.Sprint(len(args)-1) +
		` OFFSET $` + fmt.Sprint(len(args))
	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, 0, 0, 0, derrors.Wrap(derrors.KindInternal, "db.internal_tx_list_vendor", "list", err)
	}
	defer rows.Close()
	out := []domain.InternalTransaction{}
	for rows.Next() {
		t, err := scanInternalTx(rows)
		if err != nil {
			return nil, 0, 0, 0, err
		}
		out = append(out, t)
	}
	return out, total, sumSell, sumCost, nil
}

func scanInternalTx(row pgx.Row) (domain.InternalTransaction, error) {
	var t domain.InternalTransaction
	err := row.Scan(
		&t.ID, &t.BOQVersionID, &t.BOQLineID, &t.QuotationID, &t.VendorCompanyID,
		&t.SellAmount, &t.CostAmount, &t.MarginAmount, &t.Currency,
		&t.RecognizedAt, &t.Notes, &t.CreatedAt,
		&t.SourceEvent, &t.SupersededAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.InternalTransaction{}, derrors.NotFound("internal_transaction.not_found", "not found")
	}
	if err != nil {
		return domain.InternalTransaction{}, derrors.Wrap(derrors.KindInternal, "db.internal_tx_scan", "scan", err)
	}
	return t, nil
}
