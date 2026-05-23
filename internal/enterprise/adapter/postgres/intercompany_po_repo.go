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
	"github.com/ion-core/backend/internal/enterprise/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// IntercompanyPORepository implements `port.IntercompanyPORepository`
// against `enterprise.intercompany_pos` + `enterprise.intercompany_po_lines`
// (Wave 95 / migration 0064).
type IntercompanyPORepository struct {
	pool *pgxpool.Pool
}

func NewIntercompanyPORepository(pool *pgxpool.Pool) *IntercompanyPORepository {
	return &IntercompanyPORepository{pool: pool}
}

var _ port.IntercompanyPORepository = (*IntercompanyPORepository)(nil)

const intercompanyPOCols = `
	id, customer_po_id, boq_version_id,
	commercial_owner_subsidiary_id, executing_subsidiary_id,
	ic_po_number, status,
	total, COALESCE(tax_snapshot_hash, ''),
	issued_at, accepted_at, accepted_by,
	rejected_at, COALESCE(rejection_reason, ''),
	cancelled_at, superseded_at, supersedes_id,
	COALESCE(notes, ''),
	created_at, updated_at
`

const intercompanyPOLineCols = `
	id, ic_po_id, boq_line_id, sku_or_service_id,
	COALESCE(description, ''),
	COALESCE(qty, 0), COALESCE(unit_price, 0),
	COALESCE(line_total, 0), COALESCE(tax_amount, 0),
	created_at
`

// Create writes the header + every line in one transaction. The unique
// index on ic_po_number surfaces as a `.duplicate` conflict via
// mapDBError; lines are inserted only after the header succeeds.
func (r *IntercompanyPORepository) Create(
	ctx context.Context,
	header *domain.IntercompanyPO,
	lines []domain.IntercompanyPOLine,
) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "db.intercompany_po_begin", "begin tx", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		INSERT INTO enterprise.intercompany_pos
			(id, customer_po_id, boq_version_id,
			 commercial_owner_subsidiary_id, executing_subsidiary_id,
			 ic_po_number, status,
			 total, tax_snapshot_hash,
			 issued_at, accepted_at, accepted_by,
			 rejected_at, rejection_reason,
			 cancelled_at, superseded_at, supersedes_id,
			 notes, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12,
		        $13, $14, $15, $16, $17, $18, $19, $20)
	`,
		header.ID, header.CustomerPOID, header.BOQVersionID,
		header.CommercialOwnerSubsidiaryID, header.ExecutingSubsidiaryID,
		header.ICPONumber, string(header.Status),
		header.Total, header.TaxSnapshotHash,
		header.IssuedAt, header.AcceptedAt, header.AcceptedBy,
		header.RejectedAt, header.RejectionReason,
		header.CancelledAt, header.SupersededAt, header.SupersedesID,
		header.Notes, header.CreatedAt, header.UpdatedAt,
	); err != nil {
		return mapDBError(err, "intercompany_po", "insert intercompany_po")
	}

	for i := range lines {
		l := &lines[i]
		if _, err := tx.Exec(ctx, `
			INSERT INTO enterprise.intercompany_po_lines
				(id, ic_po_id, boq_line_id, sku_or_service_id,
				 description, qty, unit_price, line_total, tax_amount, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		`,
			l.ID, l.ICPOID, l.BOQLineID, l.SKUOrServiceID,
			l.Description, l.Qty, l.UnitPrice, l.LineTotal, l.TaxAmount,
			l.CreatedAt,
		); err != nil {
			return mapDBError(err, "intercompany_po_line", "insert intercompany_po_line")
		}
	}

	return tx.Commit(ctx)
}

func (r *IntercompanyPORepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.IntercompanyPO, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+intercompanyPOCols+` FROM enterprise.intercompany_pos WHERE id = $1`,
		id,
	)
	po, err := scanIntercompanyPO(row)
	if err != nil {
		return nil, err
	}
	return &po, nil
}

func (r *IntercompanyPORepository) List(ctx context.Context, f port.IntercompanyPOListFilter) ([]domain.IntercompanyPO, int, error) {
	var wh []string
	var args []any
	if f.CustomerPOID != nil {
		args = append(args, *f.CustomerPOID)
		wh = append(wh, fmt.Sprintf("customer_po_id = $%d", len(args)))
	}
	if f.CommercialOwnerSubsidiaryID != nil {
		args = append(args, *f.CommercialOwnerSubsidiaryID)
		wh = append(wh, fmt.Sprintf("commercial_owner_subsidiary_id = $%d", len(args)))
	}
	if f.ExecutingSubsidiaryID != nil {
		args = append(args, *f.ExecutingSubsidiaryID)
		wh = append(wh, fmt.Sprintf("executing_subsidiary_id = $%d", len(args)))
	}
	if f.BOQVersionID != nil {
		args = append(args, *f.BOQVersionID)
		wh = append(wh, fmt.Sprintf("boq_version_id = $%d", len(args)))
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
		`SELECT COUNT(*) FROM enterprise.intercompany_pos`+where, args...,
	).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.intercompany_po_count", "count intercompany_pos", err)
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
	sql := `SELECT ` + intercompanyPOCols + ` FROM enterprise.intercompany_pos` + where +
		` ORDER BY created_at DESC LIMIT $` + fmt.Sprint(len(args)-1) +
		` OFFSET $` + fmt.Sprint(len(args))

	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.intercompany_po_list", "list intercompany_pos", err)
	}
	defer rows.Close()

	out := []domain.IntercompanyPO{}
	for rows.Next() {
		po, err := scanIntercompanyPO(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, po)
	}
	return out, total, nil
}

func (r *IntercompanyPORepository) UpdateStatus(ctx context.Context, po *domain.IntercompanyPO) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE enterprise.intercompany_pos
		SET status = $2,
		    total = $3,
		    tax_snapshot_hash = $4,
		    issued_at = $5,
		    accepted_at = $6,
		    accepted_by = $7,
		    rejected_at = $8,
		    rejection_reason = $9,
		    cancelled_at = $10,
		    superseded_at = $11,
		    supersedes_id = $12,
		    notes = $13,
		    updated_at = NOW()
		WHERE id = $1
	`,
		po.ID, string(po.Status),
		po.Total, po.TaxSnapshotHash,
		po.IssuedAt, po.AcceptedAt, po.AcceptedBy,
		po.RejectedAt, po.RejectionReason,
		po.CancelledAt, po.SupersededAt, po.SupersedesID,
		po.Notes,
	)
	if err != nil {
		return mapDBError(err, "intercompany_po", "update intercompany_po")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("intercompany_po.not_found", "intercompany_po not found")
	}
	return nil
}

func (r *IntercompanyPORepository) FindLines(ctx context.Context, icPoID uuid.UUID) ([]domain.IntercompanyPOLine, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+intercompanyPOLineCols+` FROM enterprise.intercompany_po_lines WHERE ic_po_id = $1 ORDER BY created_at ASC`,
		icPoID,
	)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.intercompany_po_lines", "list ic_po lines", err)
	}
	defer rows.Close()
	out := []domain.IntercompanyPOLine{}
	for rows.Next() {
		l, err := scanIntercompanyPOLine(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, nil
}

func scanIntercompanyPO(row pgx.Row) (domain.IntercompanyPO, error) {
	var (
		po     domain.IntercompanyPO
		status string
	)
	err := row.Scan(
		&po.ID, &po.CustomerPOID, &po.BOQVersionID,
		&po.CommercialOwnerSubsidiaryID, &po.ExecutingSubsidiaryID,
		&po.ICPONumber, &status,
		&po.Total, &po.TaxSnapshotHash,
		&po.IssuedAt, &po.AcceptedAt, &po.AcceptedBy,
		&po.RejectedAt, &po.RejectionReason,
		&po.CancelledAt, &po.SupersededAt, &po.SupersedesID,
		&po.Notes,
		&po.CreatedAt, &po.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.IntercompanyPO{}, derrors.NotFound("intercompany_po.not_found", "intercompany_po not found")
	}
	if err != nil {
		return domain.IntercompanyPO{}, derrors.Wrap(derrors.KindInternal, "db.intercompany_po_scan", "scan intercompany_po", err)
	}
	po.Status = domain.IntercompanyPOStatus(status)
	return po, nil
}

func scanIntercompanyPOLine(row pgx.Row) (domain.IntercompanyPOLine, error) {
	var l domain.IntercompanyPOLine
	err := row.Scan(
		&l.ID, &l.ICPOID, &l.BOQLineID, &l.SKUOrServiceID,
		&l.Description, &l.Qty, &l.UnitPrice, &l.LineTotal, &l.TaxAmount,
		&l.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.IntercompanyPOLine{}, derrors.NotFound("intercompany_po_line.not_found", "intercompany_po_line not found")
	}
	if err != nil {
		return domain.IntercompanyPOLine{}, derrors.Wrap(derrors.KindInternal, "db.intercompany_po_line_scan", "scan intercompany_po_line", err)
	}
	return l, nil
}
