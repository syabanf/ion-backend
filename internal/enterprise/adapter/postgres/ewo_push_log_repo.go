package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/enterprise/domain"
	"github.com/ion-core/backend/internal/enterprise/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// Wave 103 — EWO push notification log repository
// =====================================================================

type EWOPushLogRepository struct {
	pool *pgxpool.Pool
}

func NewEWOPushLogRepository(pool *pgxpool.Pool) *EWOPushLogRepository {
	return &EWOPushLogRepository{pool: pool}
}

var _ port.EWOPushLogRepository = (*EWOPushLogRepository)(nil)

const pushLogCols = `
	id, ewo_id, subject, target_user_id,
	COALESCE(payload, '{}'::jsonb),
	sent_at, dispatch_status, COALESCE(error_msg, '')
`

func (r *EWOPushLogRepository) Create(
	ctx context.Context,
	e *domain.EWOPushEvent,
) error {
	if e == nil {
		return derrors.Validation("push_log.nil", "push event is nil")
	}
	if e.ID == uuid.Nil {
		e.ID = uuid.New()
	}
	payloadJSON := []byte("{}")
	if len(e.Payload) > 0 {
		b, err := json.Marshal(e.Payload)
		if err == nil {
			payloadJSON = b
		}
	}
	status := e.DispatchStatus
	if status == "" {
		status = "sent"
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO enterprise.ewo_push_log
			(id, ewo_id, subject, target_user_id, payload,
			 sent_at, dispatch_status, error_msg)
		VALUES ($1, $2, $3, $4, $5::jsonb,
		        COALESCE(NULLIF($6, '0001-01-01 00:00:00+00'::timestamptz), NOW()),
		        $7, NULLIF($8, ''))
	`,
		e.ID, e.EWOID, string(e.Subject), e.TargetUserID, payloadJSON,
		e.SentAt, status, e.ErrorMsg,
	)
	if err != nil {
		return mapDBError(err, "push_log", "insert push log")
	}
	return nil
}

func (r *EWOPushLogRepository) ListForUser(
	ctx context.Context,
	userID uuid.UUID,
	limit int,
) ([]domain.EWOPushEvent, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := r.pool.Query(ctx, `SELECT `+pushLogCols+`
		FROM enterprise.ewo_push_log
		WHERE target_user_id = $1
		ORDER BY sent_at DESC
		LIMIT $2`, userID, limit)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal,
			"db.push_log_list", "list push log", err)
	}
	defer rows.Close()
	out := []domain.EWOPushEvent{}
	for rows.Next() {
		e, err := scanPushLog(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

func (r *EWOPushLogRepository) HasSubject(
	ctx context.Context,
	ewoID, targetUserID uuid.UUID,
	subject domain.EWOPushSubject,
) (bool, error) {
	var n int
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM enterprise.ewo_push_log
		WHERE ewo_id = $1 AND target_user_id = $2 AND subject = $3`,
		ewoID, targetUserID, string(subject)).Scan(&n)
	if err != nil {
		return false, derrors.Wrap(derrors.KindInternal,
			"db.push_log_has_subject", "lookup push log", err)
	}
	return n > 0, nil
}

func scanPushLog(row pgx.Row) (domain.EWOPushEvent, error) {
	var (
		e           domain.EWOPushEvent
		subject     string
		payloadJSON []byte
	)
	err := row.Scan(
		&e.ID, &e.EWOID, &subject, &e.TargetUserID, &payloadJSON,
		&e.SentAt, &e.DispatchStatus, &e.ErrorMsg,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.EWOPushEvent{}, derrors.NotFound(
			"push_log.not_found", "push log not found")
	}
	if err != nil {
		return domain.EWOPushEvent{}, derrors.Wrap(derrors.KindInternal,
			"db.push_log_scan", "scan push log", err)
	}
	e.Subject = domain.EWOPushSubject(subject)
	if len(payloadJSON) > 0 {
		_ = json.Unmarshal(payloadJSON, &e.Payload)
	}
	return e, nil
}
