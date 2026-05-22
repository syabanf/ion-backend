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

type BOQRepository struct {
	pool *pgxpool.Pool
}

func NewBOQRepository(pool *pgxpool.Pool) *BOQRepository {
	return &BOQRepository{pool: pool}
}

var _ port.BOQRepository = (*BOQRepository)(nil)

const boqCols = `
	id, boq_number, opportunity_id, pricebook_id, version_no, status,
	sell_total, COALESCE(subtotal_amount, 0), COALESCE(tax_pct, 11.0),
	COALESCE(tax_amount, 0),
	cost_total, margin_pct,
	COALESCE(snapshot_hash,''),
	approval_template_id,
	source_rfq_id,
	submitted_at, approved_at, rejected_at, superseded_at,
	COALESCE(rejection_reason_code,''), COALESCE(rejection_comment,''),
	COALESCE(notes,''), revision, created_by, created_at, updated_at
`

func (r *BOQRepository) List(ctx context.Context, f port.BOQListFilter) ([]domain.BOQ, int, error) {
	var wh []string
	var args []any
	if f.OpportunityID != nil {
		args = append(args, *f.OpportunityID)
		wh = append(wh, fmt.Sprintf("opportunity_id = $%d", len(args)))
	}
	if f.ApprovalTemplateID != nil {
		args = append(args, *f.ApprovalTemplateID)
		wh = append(wh, fmt.Sprintf("approval_template_id = $%d", len(args)))
	}
	if f.Status != "" {
		args = append(args, f.Status)
		wh = append(wh, fmt.Sprintf("status = $%d", len(args)))
	}
	if s := strings.TrimSpace(f.Search); s != "" {
		args = append(args, "%"+s+"%")
		wh = append(wh, fmt.Sprintf("boq_number ILIKE $%d", len(args)))
	}
	where := ""
	if len(wh) > 0 {
		where = " WHERE " + strings.Join(wh, " AND ")
	}

	var total int
	if err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM enterprise.boq_versions`+where, args...).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.boq_count", "count boqs", err)
	}

	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	args = append(args, limit, f.Offset)
	sql := `SELECT ` + boqCols + ` FROM enterprise.boq_versions` + where +
		` ORDER BY created_at DESC LIMIT $` + fmt.Sprint(len(args)-1) +
		` OFFSET $` + fmt.Sprint(len(args))

	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.boq_list", "list boqs", err)
	}
	defer rows.Close()
	out := []domain.BOQ{}
	for rows.Next() {
		b, err := scanBOQ(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, b)
	}
	return out, total, nil
}

func (r *BOQRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.BOQ, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+boqCols+` FROM enterprise.boq_versions WHERE id = $1`, id)
	b, err := scanBOQ(row)
	if err != nil {
		return nil, err
	}
	return &b, nil
}

// SetSourceRFQID writes the backlink from a BOQ to the RFQ it
// fulfilled (E12 pre-launch). Idempotent; safe to call repeatedly.
func (r *BOQRepository) SetSourceRFQID(ctx context.Context, boqID, rfqID uuid.UUID) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE enterprise.boq_versions SET source_rfq_id = $2, updated_at = NOW() WHERE id = $1`,
		boqID, rfqID,
	)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "db.boq_set_source_rfq", "set source_rfq_id", err)
	}
	return nil
}

func (r *BOQRepository) FindHighestVersion(ctx context.Context, boqNumber string) (*domain.BOQ, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT `+boqCols+`
		FROM enterprise.boq_versions
		WHERE boq_number = $1
		ORDER BY version_no DESC
		LIMIT 1
	`, boqNumber)
	b, err := scanBOQ(row)
	if err != nil {
		return nil, err
	}
	return &b, nil
}

func (r *BOQRepository) Create(ctx context.Context, b *domain.BOQ) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO enterprise.boq_versions
			(id, boq_number, opportunity_id, pricebook_id, version_no, status,
			 sell_total, subtotal_amount, tax_pct, tax_amount,
			 cost_total, margin_pct, snapshot_hash,
			 approval_template_id, submitted_at, approved_at, rejected_at, superseded_at,
			 rejection_reason_code, rejection_comment,
			 notes, revision, created_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13,
		        $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25)
	`,
		b.ID, b.BOQNumber, b.OpportunityID, b.PricebookID, b.VersionNo, string(b.Status),
		b.SellTotal, b.SubtotalAmount, b.TaxPct, b.TaxAmount,
		b.CostTotal, b.MarginPct, b.SnapshotHash,
		b.ApprovalTemplateID, b.SubmittedAt, b.ApprovedAt, b.RejectedAt, b.SupersededAt,
		string(b.RejectionReasonCode), b.RejectionComment,
		b.Notes, b.Revision, b.CreatedBy, b.CreatedAt, b.UpdatedAt,
	)
	if err != nil {
		return mapDBError(err, "boq", "insert boq")
	}
	return nil
}

func (r *BOQRepository) Update(ctx context.Context, b *domain.BOQ, ifRevision *int) error {
	var (
		tag pgconnTag
		err error
	)
	if ifRevision != nil {
		tag, err = r.pool.Exec(ctx, `
			UPDATE enterprise.boq_versions
			SET status = $2, sell_total = $3,
			    subtotal_amount = $17, tax_pct = $18, tax_amount = $19,
			    cost_total = $4, margin_pct = $5,
			    snapshot_hash = $6, approval_template_id = $7,
			    submitted_at = $8, approved_at = $9, rejected_at = $10, superseded_at = $11,
			    rejection_reason_code = $12, rejection_comment = $13,
			    notes = $14, revision = $15, updated_at = NOW()
			WHERE id = $1 AND revision = $16
		`,
			b.ID, string(b.Status), b.SellTotal, b.CostTotal, b.MarginPct,
			b.SnapshotHash, b.ApprovalTemplateID,
			b.SubmittedAt, b.ApprovedAt, b.RejectedAt, b.SupersededAt,
			string(b.RejectionReasonCode), b.RejectionComment,
			b.Notes, b.Revision,
			*ifRevision,
			b.SubtotalAmount, b.TaxPct, b.TaxAmount,
		)
	} else {
		tag, err = r.pool.Exec(ctx, `
			UPDATE enterprise.boq_versions
			SET status = $2, sell_total = $3,
			    subtotal_amount = $16, tax_pct = $17, tax_amount = $18,
			    cost_total = $4, margin_pct = $5,
			    snapshot_hash = $6, approval_template_id = $7,
			    submitted_at = $8, approved_at = $9, rejected_at = $10, superseded_at = $11,
			    rejection_reason_code = $12, rejection_comment = $13,
			    notes = $14, revision = $15, updated_at = NOW()
			WHERE id = $1
		`,
			b.ID, string(b.Status), b.SellTotal, b.CostTotal, b.MarginPct,
			b.SnapshotHash, b.ApprovalTemplateID,
			b.SubmittedAt, b.ApprovedAt, b.RejectedAt, b.SupersededAt,
			string(b.RejectionReasonCode), b.RejectionComment,
			b.Notes, b.Revision,
			b.SubtotalAmount, b.TaxPct, b.TaxAmount,
		)
	}
	if err != nil {
		return mapDBError(err, "boq", "update boq")
	}
	if tag.RowsAffected() == 0 {
		if ifRevision != nil {
			if _, e2 := r.FindByID(ctx, b.ID); e2 != nil {
				return e2
			}
			return derrors.Conflict("boq.stale_version", "boq has been modified since you loaded it")
		}
		return derrors.NotFound("boq.not_found", "boq not found")
	}
	return nil
}

func scanBOQ(row pgx.Row) (domain.BOQ, error) {
	var (
		b      domain.BOQ
		status string
		rcode  string
	)
	err := row.Scan(
		&b.ID, &b.BOQNumber, &b.OpportunityID, &b.PricebookID, &b.VersionNo, &status,
		&b.SellTotal, &b.SubtotalAmount, &b.TaxPct, &b.TaxAmount,
		&b.CostTotal, &b.MarginPct, &b.SnapshotHash,
		&b.ApprovalTemplateID, &b.SourceRFQID,
		&b.SubmittedAt, &b.ApprovedAt, &b.RejectedAt, &b.SupersededAt,
		&rcode, &b.RejectionComment,
		&b.Notes, &b.Revision, &b.CreatedBy, &b.CreatedAt, &b.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.BOQ{}, derrors.NotFound("boq.not_found", "boq not found")
	}
	if err != nil {
		return domain.BOQ{}, derrors.Wrap(derrors.KindInternal, "db.boq_scan", "scan boq", err)
	}
	b.Status = domain.BOQStatus(status)
	b.RejectionReasonCode = domain.RejectionReasonCode(rcode)
	return b, nil
}
