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

// OpportunityRepository implements `port.OpportunityRepository` against
// `enterprise.opportunities`. The repo exposes optimistic concurrency
// via the `revision` column — see Update for the locking semantics.
type OpportunityRepository struct {
	pool *pgxpool.Pool
}

func NewOpportunityRepository(pool *pgxpool.Pool) *OpportunityRepository {
	return &OpportunityRepository{pool: pool}
}

var _ port.OpportunityRepository = (*OpportunityRepository)(nil)

const opportunityCols = `
	id, opportunity_number,
	customer_id, account_name,
	COALESCE(account_industry,''), COALESCE(account_size,''),
	COALESCE(pic_name,''), COALESCE(pic_title,''), COALESCE(pic_phone,''), COALESCE(pic_email,''),
	owner_user_id, branch_id,
	stage, substage,
	estimated_value, currency, expected_close_at,
	pricebook_id,
	source, referrer_customer_id,
	COALESCE(pre_boq, '{}'::jsonb), pre_boq_completed_at,
	stage_entered_at, last_activity_at,
	COALESCE(lost_reason_code,''), COALESCE(lost_reason,''), auto_lost,
	won_at, COALESCE(po_reference,''),
	COALESCE(notes,''),
	revision, created_at, updated_at
`

func (r *OpportunityRepository) List(ctx context.Context, f port.OpportunityListFilter) ([]domain.Opportunity, int, error) {
	var wh []string
	var args []any
	if f.Stage != "" {
		args = append(args, f.Stage)
		wh = append(wh, fmt.Sprintf("stage = $%d", len(args)))
	} else if !f.IncludeArchivedLost {
		// Default: hide Lost from the pipeline unless explicitly asked.
		wh = append(wh, "stage <> 'lost'")
	}
	if f.OwnerUserID != nil {
		args = append(args, *f.OwnerUserID)
		wh = append(wh, fmt.Sprintf("owner_user_id = $%d", len(args)))
	}
	if f.BranchID != nil {
		args = append(args, *f.BranchID)
		wh = append(wh, fmt.Sprintf("branch_id = $%d", len(args)))
	}
	if s := strings.TrimSpace(f.Search); s != "" {
		args = append(args, "%"+s+"%")
		wh = append(wh, fmt.Sprintf(
			"(account_name ILIKE $%d OR opportunity_number ILIKE $%d)",
			len(args), len(args)))
	}
	where := ""
	if len(wh) > 0 {
		where = " WHERE " + strings.Join(wh, " AND ")
	}

	var total int
	if err := r.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM enterprise.opportunities`+where, args...,
	).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.opportunity_count", "count opportunities", err)
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
	sql := `SELECT ` + opportunityCols + ` FROM enterprise.opportunities` + where +
		` ORDER BY last_activity_at DESC LIMIT $` + fmt.Sprint(len(args)-1) +
		` OFFSET $` + fmt.Sprint(len(args))

	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "db.opportunity_list", "list opportunities", err)
	}
	defer rows.Close()
	out := []domain.Opportunity{}
	for rows.Next() {
		o, err := scanOpportunity(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, o)
	}
	return out, total, nil
}

func (r *OpportunityRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.Opportunity, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+opportunityCols+` FROM enterprise.opportunities WHERE id = $1`, id)
	o, err := scanOpportunity(row)
	if err != nil {
		return nil, err
	}
	return &o, nil
}

func (r *OpportunityRepository) Create(ctx context.Context, o *domain.Opportunity) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO enterprise.opportunities
			(id, opportunity_number,
			 customer_id, account_name, account_industry, account_size,
			 pic_name, pic_title, pic_phone, pic_email,
			 owner_user_id, branch_id,
			 stage, substage,
			 estimated_value, currency, expected_close_at,
			 pricebook_id,
			 source, referrer_customer_id,
			 pre_boq, pre_boq_completed_at,
			 stage_entered_at, last_activity_at,
			 lost_reason_code, lost_reason, auto_lost,
			 won_at, po_reference, notes,
			 revision, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,
		        $18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30,$31,$32,$33)
	`,
		o.ID, o.OpportunityNumber,
		o.CustomerID, o.AccountName, o.AccountIndustry, o.AccountSize,
		o.PICName, o.PICTitle, o.PICPhone, o.PICEmail,
		o.OwnerUserID, o.BranchID,
		string(o.Stage), string(o.Substage),
		o.EstimatedValue, o.Currency, o.ExpectedCloseAt,
		o.PricebookID,
		string(o.Source), o.ReferrerCustomerID,
		o.PreBOQ, o.PreBOQCompletedAt,
		o.StageEnteredAt, o.LastActivityAt,
		string(o.LostReasonCode), o.LostReason, o.AutoLost,
		o.WonAt, o.POReference, o.Notes,
		o.Revision, o.CreatedAt, o.UpdatedAt,
	)
	if err != nil {
		return mapDBError(err, "opportunity", "insert opportunity")
	}
	return nil
}

// Update writes the row's full state back to the DB. When
// `ifRevision` is non-nil we add a `WHERE revision = ?` predicate —
// if no row matches, the row has been mutated since the caller read
// it, and we return HTTP 409 `stale_version` (CPQ TC-CONC-005).
func (r *OpportunityRepository) Update(ctx context.Context, o *domain.Opportunity, ifRevision *int) error {
	var (
		tag pgconnTag
		err error
	)
	if ifRevision != nil {
		tag, err = r.pool.Exec(ctx, `
			UPDATE enterprise.opportunities
			SET customer_id = $2, account_name = $3,
			    account_industry = $4, account_size = $5,
			    pic_name = $6, pic_title = $7, pic_phone = $8, pic_email = $9,
			    owner_user_id = $10, branch_id = $11,
			    stage = $12, substage = $13,
			    estimated_value = $14, currency = $15, expected_close_at = $16,
			    pricebook_id = $17,
			    source = $18, referrer_customer_id = $19,
			    pre_boq = $20, pre_boq_completed_at = $21,
			    stage_entered_at = $22, last_activity_at = $23,
			    lost_reason_code = $24, lost_reason = $25, auto_lost = $26,
			    won_at = $27, po_reference = $28, notes = $29,
			    revision = $30, updated_at = NOW()
			WHERE id = $1 AND revision = $31
		`,
			o.ID,
			o.CustomerID, o.AccountName, o.AccountIndustry, o.AccountSize,
			o.PICName, o.PICTitle, o.PICPhone, o.PICEmail,
			o.OwnerUserID, o.BranchID,
			string(o.Stage), string(o.Substage),
			o.EstimatedValue, o.Currency, o.ExpectedCloseAt,
			o.PricebookID,
			string(o.Source), o.ReferrerCustomerID,
			o.PreBOQ, o.PreBOQCompletedAt,
			o.StageEnteredAt, o.LastActivityAt,
			string(o.LostReasonCode), o.LostReason, o.AutoLost,
			o.WonAt, o.POReference, o.Notes,
			o.Revision,
			*ifRevision,
		)
	} else {
		tag, err = r.pool.Exec(ctx, `
			UPDATE enterprise.opportunities
			SET customer_id = $2, account_name = $3,
			    account_industry = $4, account_size = $5,
			    pic_name = $6, pic_title = $7, pic_phone = $8, pic_email = $9,
			    owner_user_id = $10, branch_id = $11,
			    stage = $12, substage = $13,
			    estimated_value = $14, currency = $15, expected_close_at = $16,
			    pricebook_id = $17,
			    source = $18, referrer_customer_id = $19,
			    pre_boq = $20, pre_boq_completed_at = $21,
			    stage_entered_at = $22, last_activity_at = $23,
			    lost_reason_code = $24, lost_reason = $25, auto_lost = $26,
			    won_at = $27, po_reference = $28, notes = $29,
			    revision = $30, updated_at = NOW()
			WHERE id = $1
		`,
			o.ID,
			o.CustomerID, o.AccountName, o.AccountIndustry, o.AccountSize,
			o.PICName, o.PICTitle, o.PICPhone, o.PICEmail,
			o.OwnerUserID, o.BranchID,
			string(o.Stage), string(o.Substage),
			o.EstimatedValue, o.Currency, o.ExpectedCloseAt,
			o.PricebookID,
			string(o.Source), o.ReferrerCustomerID,
			o.PreBOQ, o.PreBOQCompletedAt,
			o.StageEnteredAt, o.LastActivityAt,
			string(o.LostReasonCode), o.LostReason, o.AutoLost,
			o.WonAt, o.POReference, o.Notes,
			o.Revision,
		)
	}
	if err != nil {
		return mapDBError(err, "opportunity", "update opportunity")
	}
	if tag.RowsAffected() == 0 {
		if ifRevision != nil {
			// Either the row vanished or someone else updated it.
			// Disambiguate by re-reading.
			if _, err2 := r.FindByID(ctx, o.ID); err2 != nil {
				return err2 // probably NotFound
			}
			return derrors.Conflict(
				"opportunity.stale_version",
				"opportunity has been modified since you loaded it; please refresh and retry",
			)
		}
		return derrors.NotFound("opportunity.not_found", "opportunity not found")
	}
	return nil
}

func (r *OpportunityRepository) FindExpiredAutoLostCandidates(ctx context.Context) ([]domain.Opportunity, error) {
	// Postgres can do the window check far cheaper than pulling every
	// open opportunity into Go. The boundary math mirrors
	// domain.IsAutoLostExpired — `> window` so an exact-equal age is
	// NOT expired (TC-OP-008 boundary).
	const sql = `
		SELECT ` + opportunityCols + `
		FROM enterprise.opportunities
		WHERE stage = 'cold' AND NOW() - last_activity_at > INTERVAL '30 days'
		   OR stage = 'warm' AND NOW() - last_activity_at > INTERVAL '7 days'
		   OR stage = 'hot'  AND NOW() - last_activity_at > INTERVAL '3 days'
	`
	rows, err := r.pool.Query(ctx, sql)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.opportunity_auto_lost_query", "scan auto-lost candidates", err)
	}
	defer rows.Close()
	out := []domain.Opportunity{}
	for rows.Next() {
		o, err := scanOpportunity(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, nil
}

func scanOpportunity(row pgx.Row) (domain.Opportunity, error) {
	var (
		o        domain.Opportunity
		stage    string
		substage string
		source   string
		lostCode string
	)
	err := row.Scan(
		&o.ID, &o.OpportunityNumber,
		&o.CustomerID, &o.AccountName,
		&o.AccountIndustry, &o.AccountSize,
		&o.PICName, &o.PICTitle, &o.PICPhone, &o.PICEmail,
		&o.OwnerUserID, &o.BranchID,
		&stage, &substage,
		&o.EstimatedValue, &o.Currency, &o.ExpectedCloseAt,
		&o.PricebookID,
		&source, &o.ReferrerCustomerID,
		&o.PreBOQ, &o.PreBOQCompletedAt,
		&o.StageEnteredAt, &o.LastActivityAt,
		&lostCode, &o.LostReason, &o.AutoLost,
		&o.WonAt, &o.POReference, &o.Notes,
		&o.Revision, &o.CreatedAt, &o.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Opportunity{}, derrors.NotFound("opportunity.not_found", "opportunity not found")
	}
	if err != nil {
		return domain.Opportunity{}, derrors.Wrap(derrors.KindInternal, "db.opportunity_scan", "scan opportunity", err)
	}
	o.Stage = domain.OpportunityStage(stage)
	o.Substage = domain.OpportunitySubstage(substage)
	o.Source = domain.OpportunitySource(source)
	o.LostReasonCode = domain.LostReasonCode(lostCode)
	return o, nil
}

// pgconnTag is a tiny shim so the same code path can call Exec via
// either branch above without importing the full pgconn type in every
// caller. pgxpool.Exec returns pgconn.CommandTag, which exposes
// RowsAffected — that's all we need.
type pgconnTag = interface {
	RowsAffected() int64
}
