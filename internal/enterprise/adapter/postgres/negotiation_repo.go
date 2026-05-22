package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/enterprise/domain"
	"github.com/ion-core/backend/internal/enterprise/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// NegotiationConfigRepository
//
// Config lives in two places:
//   - boq_versions.negotiation_* columns (header settings)
//   - enterprise.negotiation_participants (chain members)
// We hide that split behind this repo so the usecase deals with a
// single composite domain.NegotiationConfig.
// =====================================================================

type NegotiationConfigRepository struct {
	pool *pgxpool.Pool
}

func NewNegotiationConfigRepository(pool *pgxpool.Pool) *NegotiationConfigRepository {
	return &NegotiationConfigRepository{pool: pool}
}

var _ port.NegotiationConfigRepository = (*NegotiationConfigRepository)(nil)

func (r *NegotiationConfigRepository) GetConfig(ctx context.Context, boqVersionID uuid.UUID) (*domain.NegotiationConfig, error) {
	var (
		c    domain.NegotiationConfig
		mode string
	)
	c.BOQVersionID = boqVersionID
	err := r.pool.QueryRow(ctx, `
		SELECT negotiation_enabled, negotiation_type, negotiation_mode,
		       pricing_adjustment_allowed, negotiation_margin_floor,
		       negotiation_discount_ceiling, negotiation_config_locked_at
		FROM enterprise.boq_versions
		WHERE id = $1
	`, boqVersionID).Scan(
		&c.Enabled, &c.Type, &mode,
		&c.PricingAdjustmentAllowed, &c.MarginFloorPct,
		&c.DiscountCeilingPct, &c.LockedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, derrors.NotFound("negotiation_config.not_found", "boq not found")
	}
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.negotiation_config_get", "read config", err)
	}
	c.Mode = domain.ApprovalMode(mode)
	parts, err := r.ListParticipants(ctx, boqVersionID)
	if err != nil {
		return nil, err
	}
	c.Participants = parts
	return &c, nil
}

func (r *NegotiationConfigRepository) SetConfig(ctx context.Context, cfg *domain.NegotiationConfig) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "db.negotiation_config_tx", "begin tx", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		UPDATE enterprise.boq_versions
		SET negotiation_enabled = $2, negotiation_type = $3, negotiation_mode = $4,
		    pricing_adjustment_allowed = $5, negotiation_margin_floor = $6,
		    negotiation_discount_ceiling = $7
		WHERE id = $1
	`, cfg.BOQVersionID, cfg.Enabled, cfg.Type, string(cfg.Mode),
		cfg.PricingAdjustmentAllowed, cfg.MarginFloorPct, cfg.DiscountCeilingPct,
	); err != nil {
		return derrors.Wrap(derrors.KindInternal, "db.negotiation_config_update", "update config", err)
	}
	// Replace participants.
	if _, err := tx.Exec(ctx,
		`DELETE FROM enterprise.negotiation_participants WHERE boq_version_id = $1`,
		cfg.BOQVersionID); err != nil {
		return derrors.Wrap(derrors.KindInternal, "db.negotiation_participants_delete", "delete participants", err)
	}
	for _, p := range cfg.Participants {
		if p.ID == uuid.Nil {
			p.ID = uuid.New()
		}
		if p.CreatedAt.IsZero() {
			p.CreatedAt = time.Now().UTC()
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO enterprise.negotiation_participants
				(id, boq_version_id, user_id, step_no, role_tag, created_at)
			VALUES ($1, $2, $3, $4, $5, $6)
		`, p.ID, cfg.BOQVersionID, p.UserID, p.StepNo, p.RoleTag, p.CreatedAt); err != nil {
			return mapDBError(err, "negotiation_participant", "insert participant")
		}
	}
	return tx.Commit(ctx)
}

func (r *NegotiationConfigRepository) LockConfig(ctx context.Context, boqVersionID uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE enterprise.boq_versions
		SET negotiation_config_locked_at = COALESCE(negotiation_config_locked_at, NOW())
		WHERE id = $1
	`, boqVersionID)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "db.negotiation_config_lock", "lock config", err)
	}
	return nil
}

func (r *NegotiationConfigRepository) ListParticipants(ctx context.Context, boqVersionID uuid.UUID) ([]domain.NegotiationParticipant, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, boq_version_id, user_id, step_no, COALESCE(role_tag,''), created_at
		FROM enterprise.negotiation_participants
		WHERE boq_version_id = $1
		ORDER BY step_no, user_id
	`, boqVersionID)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.negotiation_participants_list", "list participants", err)
	}
	defer rows.Close()
	out := []domain.NegotiationParticipant{}
	for rows.Next() {
		var p domain.NegotiationParticipant
		if err := rows.Scan(&p.ID, &p.BOQVersionID, &p.UserID, &p.StepNo, &p.RoleTag, &p.CreatedAt); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "db.negotiation_participant_scan", "scan participant", err)
		}
		out = append(out, p)
	}
	return out, nil
}

func (r *NegotiationConfigRepository) ReplaceParticipants(ctx context.Context, boqVersionID uuid.UUID, members []domain.NegotiationParticipant) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "db.negotiation_participants_tx", "begin tx", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx,
		`DELETE FROM enterprise.negotiation_participants WHERE boq_version_id = $1`,
		boqVersionID); err != nil {
		return derrors.Wrap(derrors.KindInternal, "db.negotiation_participants_delete", "delete", err)
	}
	for _, p := range members {
		if p.ID == uuid.Nil {
			p.ID = uuid.New()
		}
		if p.CreatedAt.IsZero() {
			p.CreatedAt = time.Now().UTC()
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO enterprise.negotiation_participants
				(id, boq_version_id, user_id, step_no, role_tag, created_at)
			VALUES ($1, $2, $3, $4, $5, $6)
		`, p.ID, boqVersionID, p.UserID, p.StepNo, p.RoleTag, p.CreatedAt); err != nil {
			return mapDBError(err, "negotiation_participant", "insert")
		}
	}
	return tx.Commit(ctx)
}

// =====================================================================
// NegotiationRepository
// =====================================================================

type NegotiationRepository struct {
	pool *pgxpool.Pool
}

func NewNegotiationRepository(pool *pgxpool.Pool) *NegotiationRepository {
	return &NegotiationRepository{pool: pool}
}

var _ port.NegotiationRepository = (*NegotiationRepository)(nil)

const negotiationCols = `
	id, boq_version_id, status,
	activated_at, activated_by,
	completed_at, aborted_at,
	COALESCE(abort_reason,''),
	resulting_quotation_id,
	revision, created_at, updated_at
`

func (r *NegotiationRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.Negotiation, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+negotiationCols+` FROM enterprise.negotiations WHERE id = $1`, id)
	n, err := scanNegotiation(row)
	if err != nil {
		return nil, err
	}
	return &n, nil
}

func (r *NegotiationRepository) FindByBOQ(ctx context.Context, boqVersionID uuid.UUID) (*domain.Negotiation, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+negotiationCols+` FROM enterprise.negotiations WHERE boq_version_id = $1`, boqVersionID)
	n, err := scanNegotiation(row)
	if err != nil {
		return nil, err
	}
	return &n, nil
}

func (r *NegotiationRepository) Create(ctx context.Context, n *domain.Negotiation) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO enterprise.negotiations
			(id, boq_version_id, status, activated_at, activated_by,
			 completed_at, aborted_at, abort_reason, resulting_quotation_id,
			 revision, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
	`,
		n.ID, n.BOQVersionID, string(n.Status), n.ActivatedAt, n.ActivatedBy,
		n.CompletedAt, n.AbortedAt, n.AbortReason, n.ResultingQuotationID,
		n.Revision, n.CreatedAt, n.UpdatedAt,
	)
	if err != nil {
		return mapDBError(err, "negotiation", "insert negotiation")
	}
	return nil
}

func (r *NegotiationRepository) Update(ctx context.Context, n *domain.Negotiation) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE enterprise.negotiations
		SET status = $2, activated_at = $3, activated_by = $4,
		    completed_at = $5, aborted_at = $6, abort_reason = $7,
		    resulting_quotation_id = $8, revision = $9, updated_at = NOW()
		WHERE id = $1
	`,
		n.ID, string(n.Status), n.ActivatedAt, n.ActivatedBy,
		n.CompletedAt, n.AbortedAt, n.AbortReason,
		n.ResultingQuotationID, n.Revision,
	)
	if err != nil {
		return mapDBError(err, "negotiation", "update negotiation")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("negotiation.not_found", "negotiation not found")
	}
	return nil
}

func scanNegotiation(row pgx.Row) (domain.Negotiation, error) {
	var (
		n      domain.Negotiation
		status string
	)
	err := row.Scan(
		&n.ID, &n.BOQVersionID, &status,
		&n.ActivatedAt, &n.ActivatedBy,
		&n.CompletedAt, &n.AbortedAt, &n.AbortReason,
		&n.ResultingQuotationID,
		&n.Revision, &n.CreatedAt, &n.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Negotiation{}, derrors.NotFound("negotiation.not_found", "negotiation not found")
	}
	if err != nil {
		return domain.Negotiation{}, derrors.Wrap(derrors.KindInternal, "db.negotiation_scan", "scan negotiation", err)
	}
	n.Status = domain.NegotiationStatus(status)
	return n, nil
}

// =====================================================================
// NegotiationRoundRepository
// =====================================================================

type NegotiationRoundRepository struct {
	pool *pgxpool.Pool
}

func NewNegotiationRoundRepository(pool *pgxpool.Pool) *NegotiationRoundRepository {
	return &NegotiationRoundRepository{pool: pool}
}

var _ port.NegotiationRoundRepository = (*NegotiationRoundRepository)(nil)

const roundCols = `
	id, negotiation_id, round_no, status,
	COALESCE(price_changes, '[]'::jsonb),
	margin_before, margin_after, max_discount_after,
	cco_auto_injected, COALESCE(cco_injection_reason,''),
	submitted_by, submitted_at, completed_at,
	COALESCE(rejection_reason_code,''), COALESCE(rejection_comment,''),
	created_at, updated_at
`

func (r *NegotiationRoundRepository) List(ctx context.Context, negotiationID uuid.UUID) ([]domain.NegotiationRound, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+roundCols+`
		FROM enterprise.negotiation_rounds
		WHERE negotiation_id = $1
		ORDER BY round_no DESC
	`, negotiationID)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.negotiation_round_list", "list rounds", err)
	}
	defer rows.Close()
	out := []domain.NegotiationRound{}
	for rows.Next() {
		round, err := scanRound(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, round)
	}
	return out, nil
}

func (r *NegotiationRoundRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.NegotiationRound, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+roundCols+` FROM enterprise.negotiation_rounds WHERE id = $1`, id)
	round, err := scanRound(row)
	if err != nil {
		return nil, err
	}
	return &round, nil
}

func (r *NegotiationRoundRepository) HighestRoundNo(ctx context.Context, negotiationID uuid.UUID) (int, error) {
	var max int
	err := r.pool.QueryRow(ctx,
		`SELECT COALESCE(MAX(round_no), 0) FROM enterprise.negotiation_rounds WHERE negotiation_id = $1`,
		negotiationID).Scan(&max)
	if err != nil {
		return 0, derrors.Wrap(derrors.KindInternal, "db.negotiation_round_max", "highest round_no", err)
	}
	return max, nil
}

func (r *NegotiationRoundRepository) Create(ctx context.Context, round *domain.NegotiationRound) error {
	priceJSON, err := round.MarshalPriceChanges()
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO enterprise.negotiation_rounds
			(id, negotiation_id, round_no, status,
			 price_changes, margin_before, margin_after, max_discount_after,
			 cco_auto_injected, cco_injection_reason,
			 submitted_by, submitted_at, completed_at,
			 rejection_reason_code, rejection_comment,
			 created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
	`,
		round.ID, round.NegotiationID, round.RoundNo, string(round.Status),
		priceJSON, round.MarginBefore, round.MarginAfter, round.MaxDiscountAfter,
		round.CCOAutoInjected, string(round.CCOInjectionReason),
		round.SubmittedBy, round.SubmittedAt, round.CompletedAt,
		string(round.RejectionReasonCode), round.RejectionComment,
		round.CreatedAt, round.UpdatedAt,
	)
	if err != nil {
		return mapDBError(err, "negotiation_round", "insert round")
	}
	return nil
}

func (r *NegotiationRoundRepository) Update(ctx context.Context, round *domain.NegotiationRound) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE enterprise.negotiation_rounds
		SET status = $2, completed_at = $3,
		    rejection_reason_code = $4, rejection_comment = $5,
		    updated_at = NOW()
		WHERE id = $1
	`,
		round.ID, string(round.Status), round.CompletedAt,
		string(round.RejectionReasonCode), round.RejectionComment,
	)
	if err != nil {
		return mapDBError(err, "negotiation_round", "update round")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("negotiation_round.not_found", "round not found")
	}
	return nil
}

func scanRound(row pgx.Row) (domain.NegotiationRound, error) {
	var (
		round     domain.NegotiationRound
		status    string
		injection string
		rejCode   string
		priceJSON []byte
	)
	err := row.Scan(
		&round.ID, &round.NegotiationID, &round.RoundNo, &status,
		&priceJSON, &round.MarginBefore, &round.MarginAfter, &round.MaxDiscountAfter,
		&round.CCOAutoInjected, &injection,
		&round.SubmittedBy, &round.SubmittedAt, &round.CompletedAt,
		&rejCode, &round.RejectionComment,
		&round.CreatedAt, &round.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.NegotiationRound{}, derrors.NotFound("negotiation_round.not_found", "round not found")
	}
	if err != nil {
		return domain.NegotiationRound{}, derrors.Wrap(derrors.KindInternal, "db.negotiation_round_scan", "scan round", err)
	}
	round.Status = domain.NegotiationRoundStatus(status)
	round.CCOInjectionReason = domain.CCOInjectionReason(injection)
	round.RejectionReasonCode = domain.RejectionReasonCode(rejCode)
	if changes, perr := domain.UnmarshalPriceChanges(priceJSON); perr == nil {
		round.PriceChanges = changes
	}
	return round, nil
}

// =====================================================================
// NegotiationRoundApprovalRepository
// =====================================================================

type NegotiationRoundApprovalRepository struct {
	pool *pgxpool.Pool
}

func NewNegotiationRoundApprovalRepository(pool *pgxpool.Pool) *NegotiationRoundApprovalRepository {
	return &NegotiationRoundApprovalRepository{pool: pool}
}

var _ port.NegotiationRoundApprovalRepository = (*NegotiationRoundApprovalRepository)(nil)

const roundApprovalCols = `
	id, round_id, step_no, approver_user_id, COALESCE(role_tag,''),
	status, COALESCE(reason_code,''), COALESCE(comment,''),
	acted_at, acted_at_original, auto_injected, created_at, updated_at
`

func (r *NegotiationRoundApprovalRepository) ListByRound(ctx context.Context, roundID uuid.UUID) ([]domain.NegotiationRoundApproval, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+roundApprovalCols+`
		FROM enterprise.negotiation_round_approvals
		WHERE round_id = $1
		ORDER BY step_no, approver_user_id
	`, roundID)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.negotiation_round_approvals_list", "list", err)
	}
	defer rows.Close()
	out := []domain.NegotiationRoundApproval{}
	for rows.Next() {
		a, err := scanRoundApproval(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

func (r *NegotiationRoundApprovalRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.NegotiationRoundApproval, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+roundApprovalCols+` FROM enterprise.negotiation_round_approvals WHERE id = $1`, id)
	a, err := scanRoundApproval(row)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func (r *NegotiationRoundApprovalRepository) CreateBatch(ctx context.Context, approvals []domain.NegotiationRoundApproval) error {
	if len(approvals) == 0 {
		return nil
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "db.round_approvals_tx", "begin tx", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, a := range approvals {
		if _, err := tx.Exec(ctx, `
			INSERT INTO enterprise.negotiation_round_approvals
				(id, round_id, step_no, approver_user_id, role_tag,
				 status, reason_code, comment, acted_at, acted_at_original,
				 auto_injected, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		`,
			a.ID, a.RoundID, a.StepNo, a.ApproverUserID, a.RoleTag,
			string(a.Status), string(a.ReasonCode), a.Comment,
			a.ActedAt, a.ActedAtOriginal, a.AutoInjected,
			a.CreatedAt, a.UpdatedAt,
		); err != nil {
			return mapDBError(err, "negotiation_round_approval", "insert")
		}
	}
	return tx.Commit(ctx)
}

// ListPendingForUser returns pending negotiation round approvals
// assigned to the given user, newest first. Joined indirectly via the
// approver_user_id column. Mirrors the BOQ approval-instance "my queue"
// query so the unified inbox can fan-out to two parallel selects.
func (r *NegotiationRoundApprovalRepository) ListPendingForUser(ctx context.Context, userID uuid.UUID, limit, offset int) ([]domain.NegotiationRoundApproval, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+roundApprovalCols+`
		FROM enterprise.negotiation_round_approvals
		WHERE approver_user_id = $1 AND status = 'pending'
		ORDER BY created_at DESC
		LIMIT $2 OFFSET $3
	`, userID, limit, offset)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.negotiation_round_approvals_my_queue", "list pending-for-user", err)
	}
	defer rows.Close()
	out := []domain.NegotiationRoundApproval{}
	for rows.Next() {
		a, err := scanRoundApproval(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

func (r *NegotiationRoundApprovalRepository) Update(ctx context.Context, a *domain.NegotiationRoundApproval) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE enterprise.negotiation_round_approvals
		SET status = $2, reason_code = $3, comment = $4,
		    acted_at = $5, acted_at_original = $6,
		    updated_at = NOW()
		WHERE id = $1
	`,
		a.ID, string(a.Status), string(a.ReasonCode), a.Comment,
		a.ActedAt, a.ActedAtOriginal,
	)
	if err != nil {
		return mapDBError(err, "negotiation_round_approval", "update")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("negotiation_round_approval.not_found", "approval not found")
	}
	return nil
}

func scanRoundApproval(row pgx.Row) (domain.NegotiationRoundApproval, error) {
	var (
		a      domain.NegotiationRoundApproval
		status string
		reason string
	)
	err := row.Scan(
		&a.ID, &a.RoundID, &a.StepNo, &a.ApproverUserID, &a.RoleTag,
		&status, &reason, &a.Comment,
		&a.ActedAt, &a.ActedAtOriginal, &a.AutoInjected,
		&a.CreatedAt, &a.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.NegotiationRoundApproval{}, derrors.NotFound("negotiation_round_approval.not_found", "approval not found")
	}
	if err != nil {
		return domain.NegotiationRoundApproval{}, derrors.Wrap(derrors.KindInternal, "db.round_approval_scan", "scan", err)
	}
	a.Status = domain.ApprovalInstanceStatus(status)
	a.ReasonCode = domain.ApprovalReasonCode(reason)
	return a, nil
}
