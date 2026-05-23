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

// CustomerPORepository implements `port.CustomerPORepository` against
// `enterprise.customer_pos` (Wave 95 / migration 0064).
type CustomerPORepository struct {
	pool *pgxpool.Pool
}

func NewCustomerPORepository(pool *pgxpool.Pool) *CustomerPORepository {
	return &CustomerPORepository{pool: pool}
}

var _ port.CustomerPORepository = (*CustomerPORepository)(nil)

const customerPOCols = `
	id, opportunity_id, boq_version_id, customer_id,
	commercial_owner_subsidiary_id,
	po_number, po_value,
	COALESCE(file_url, ''), COALESCE(file_hash, ''),
	uploaded_by, uploaded_at,
	status,
	validated_at, accepted_at, rejected_at, cancelled_at,
	COALESCE(rejection_reason, ''),
	COALESCE(notes, ''),
	created_at, updated_at
`

func (r *CustomerPORepository) Create(ctx context.Context, po *domain.CustomerPO) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO enterprise.customer_pos
			(id, opportunity_id, boq_version_id, customer_id,
			 commercial_owner_subsidiary_id,
			 po_number, po_value, file_url, file_hash,
			 uploaded_by, uploaded_at,
			 status,
			 validated_at, accepted_at, rejected_at, cancelled_at,
			 rejection_reason, notes,
			 created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11,
		        $12, $13, $14, $15, $16, $17, $18, $19, $20)
	`,
		po.ID, po.OpportunityID, po.BOQVersionID, po.CustomerID,
		po.CommercialOwnerSubsidiaryID,
		po.PONumber, po.POValue, po.FileURL, po.FileHash,
		po.UploadedBy, po.UploadedAt,
		string(po.Status),
		po.ValidatedAt, po.AcceptedAt, po.RejectedAt, po.CancelledAt,
		po.RejectionReason, po.Notes,
		po.CreatedAt, po.UpdatedAt,
	)
	if err != nil {
		return mapDBError(err, "customer_po", "insert customer_po")
	}
	return nil
}

func (r *CustomerPORepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.CustomerPO, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+customerPOCols+` FROM enterprise.customer_pos WHERE id = $1`,
		id,
	)
	po, err := scanCustomerPO(row)
	if err != nil {
		return nil, err
	}
	return &po, nil
}

func (r *CustomerPORepository) List(ctx context.Context, f port.CustomerPOListFilter) ([]domain.CustomerPO, int, error) {
	var wh []string
	var args []any
	if f.OpportunityID != nil {
		args = append(args, *f.OpportunityID)
		wh = append(wh, fmt.Sprintf("opportunity_id = $%d", len(args)))
	}
	if f.BOQVersionID != nil {
		args = append(args, *f.BOQVersionID)
		wh = append(wh, fmt.Sprintf("boq_version_id = $%d", len(args)))
	}
	if f.CommercialOwnerSubsidiaryID != nil {
		args = append(args, *f.CommercialOwnerSubsidiaryID)
		wh = append(wh, fmt.Sprintf("commercial_owner_subsidiary_id = $%d", len(args)))
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
		`SELECT COUNT(*) FROM enterprise.customer_pos`+where, args...,
	).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.customer_po_count", "count customer_pos", err)
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
	sql := `SELECT ` + customerPOCols + ` FROM enterprise.customer_pos` + where +
		` ORDER BY created_at DESC LIMIT $` + fmt.Sprint(len(args)-1) +
		` OFFSET $` + fmt.Sprint(len(args))

	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.customer_po_list", "list customer_pos", err)
	}
	defer rows.Close()

	out := []domain.CustomerPO{}
	for rows.Next() {
		po, err := scanCustomerPO(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, po)
	}
	return out, total, nil
}

// UpdateStatus persists state-machine transitions. It pushes every
// nullable timestamp + reason field at once so callers can drive
// arbitrary state changes through a single round-trip.
func (r *CustomerPORepository) UpdateStatus(ctx context.Context, po *domain.CustomerPO) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE enterprise.customer_pos
		SET status = $2,
		    validated_at = $3,
		    accepted_at = $4,
		    rejected_at = $5,
		    cancelled_at = $6,
		    rejection_reason = $7,
		    notes = $8,
		    updated_at = NOW()
		WHERE id = $1
	`,
		po.ID, string(po.Status),
		po.ValidatedAt, po.AcceptedAt, po.RejectedAt, po.CancelledAt,
		po.RejectionReason, po.Notes,
	)
	if err != nil {
		return mapDBError(err, "customer_po", "update customer_po")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("customer_po.not_found", "customer_po not found")
	}
	return nil
}

func scanCustomerPO(row pgx.Row) (domain.CustomerPO, error) {
	var (
		po     domain.CustomerPO
		status string
	)
	err := row.Scan(
		&po.ID, &po.OpportunityID, &po.BOQVersionID, &po.CustomerID,
		&po.CommercialOwnerSubsidiaryID,
		&po.PONumber, &po.POValue, &po.FileURL, &po.FileHash,
		&po.UploadedBy, &po.UploadedAt,
		&status,
		&po.ValidatedAt, &po.AcceptedAt, &po.RejectedAt, &po.CancelledAt,
		&po.RejectionReason, &po.Notes,
		&po.CreatedAt, &po.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.CustomerPO{}, derrors.NotFound("customer_po.not_found", "customer_po not found")
	}
	if err != nil {
		return domain.CustomerPO{}, derrors.Wrap(derrors.KindInternal, "db.customer_po_scan", "scan customer_po", err)
	}
	po.Status = domain.CustomerPOStatus(status)
	return po, nil
}
