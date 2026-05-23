package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/cs/domain"
	"github.com/ion-core/backend/internal/cs/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// ServiceRequestRepository implements port.ServiceRequestRepository.
type ServiceRequestRepository struct {
	pool *pgxpool.Pool
}

func NewServiceRequestRepository(pool *pgxpool.Pool) *ServiceRequestRepository {
	return &ServiceRequestRepository{pool: pool}
}

var _ port.ServiceRequestRepository = (*ServiceRequestRepository)(nil)

const srCols = `
	id, ticket_id, customer_id, request_type, reference_id,
	status, submitted_by, approved_by, approval_decision_at,
	COALESCE(rejection_reason,''), fulfilled_at, COALESCE(cancelled_reason,''),
	sla_due_at, COALESCE(payload, '{}'::jsonb),
	created_at, updated_at
`

func (r *ServiceRequestRepository) Insert(ctx context.Context, sr *domain.ServiceRequest) error {
	payload, err := jsonbBytes(sr.Payload)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "cs.sr.marshal", "marshal payload", err)
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO cs.service_requests
			(id, ticket_id, customer_id, request_type, reference_id,
			 status, submitted_by, approved_by, approval_decision_at,
			 rejection_reason, fulfilled_at, cancelled_reason,
			 sla_due_at, payload, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
	`,
		sr.ID, sr.TicketID, sr.CustomerID, string(sr.RequestType), sr.ReferenceID,
		string(sr.Status), sr.SubmittedBy, sr.ApprovedBy, sr.ApprovalDecisionAt,
		nullableString(sr.RejectionReason), sr.FulfilledAt, nullableString(sr.CancelledReason),
		sr.SLADueAt, payload, sr.CreatedAt, sr.UpdatedAt,
	)
	if err != nil {
		return mapDBError(err, "cs.sr", "insert service request")
	}
	return nil
}

func (r *ServiceRequestRepository) Update(ctx context.Context, sr *domain.ServiceRequest) error {
	payload, err := jsonbBytes(sr.Payload)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "cs.sr.marshal", "marshal payload", err)
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE cs.service_requests SET
			request_type         = $2,
			reference_id         = $3,
			status               = $4,
			approved_by          = $5,
			approval_decision_at = $6,
			rejection_reason     = $7,
			fulfilled_at         = $8,
			cancelled_reason     = $9,
			sla_due_at           = $10,
			payload              = $11,
			updated_at           = NOW()
		WHERE id = $1
	`,
		sr.ID, string(sr.RequestType), sr.ReferenceID, string(sr.Status),
		sr.ApprovedBy, sr.ApprovalDecisionAt, nullableString(sr.RejectionReason),
		sr.FulfilledAt, nullableString(sr.CancelledReason),
		sr.SLADueAt, payload,
	)
	if err != nil {
		return mapDBError(err, "cs.sr", "update service request")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("cs.sr.not_found", "service request not found")
	}
	return nil
}

func (r *ServiceRequestRepository) FindByID(ctx context.Context, id uuid.UUID) (*domain.ServiceRequest, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+srCols+` FROM cs.service_requests WHERE id = $1`, id)
	sr, err := scanSR(row)
	if err != nil {
		return nil, err
	}
	return &sr, nil
}

func (r *ServiceRequestRepository) List(ctx context.Context, f port.ServiceRequestFilter) ([]domain.ServiceRequest, int, error) {
	var wh []string
	var args []any
	add := func(cond string, val any) {
		args = append(args, val)
		wh = append(wh, fmt.Sprintf(cond, len(args)))
	}
	if f.CustomerID != nil {
		add("customer_id = $%d", *f.CustomerID)
	}
	if f.TicketID != nil {
		add("ticket_id = $%d", *f.TicketID)
	}
	if f.Status != "" {
		add("status = $%d", f.Status)
	}
	if f.RequestType != "" {
		add("request_type = $%d", f.RequestType)
	}
	where := ""
	if len(wh) > 0 {
		where = " WHERE " + strings.Join(wh, " AND ")
	}
	var total int
	if err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM cs.service_requests`+where, args...).Scan(&total); err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "cs.sr.count", "count service requests", err)
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
	sql := `SELECT ` + srCols + ` FROM cs.service_requests` + where +
		` ORDER BY created_at DESC LIMIT $` + fmt.Sprint(len(args)-1) +
		` OFFSET $` + fmt.Sprint(len(args))
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, 0, derrors.Wrap(derrors.KindInternal, "cs.sr.list", "list service requests", err)
	}
	defer rows.Close()
	out := []domain.ServiceRequest{}
	for rows.Next() {
		sr, err := scanSR(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, sr)
	}
	return out, total, nil
}

func (r *ServiceRequestRepository) ListPendingApproval(ctx context.Context, limit int) ([]domain.ServiceRequest, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+srCols+`
		  FROM cs.service_requests
		 WHERE status = 'submitted'
		 ORDER BY created_at ASC
		 LIMIT $1
	`, limit)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "cs.sr.list_pending", "list pending approval", err)
	}
	defer rows.Close()
	out := []domain.ServiceRequest{}
	for rows.Next() {
		sr, err := scanSR(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sr)
	}
	return out, nil
}

func scanSR(row pgx.Row) (domain.ServiceRequest, error) {
	var sr domain.ServiceRequest
	var reqType, status string
	var payload []byte
	err := row.Scan(
		&sr.ID, &sr.TicketID, &sr.CustomerID, &reqType, &sr.ReferenceID,
		&status, &sr.SubmittedBy, &sr.ApprovedBy, &sr.ApprovalDecisionAt,
		&sr.RejectionReason, &sr.FulfilledAt, &sr.CancelledReason,
		&sr.SLADueAt, &payload,
		&sr.CreatedAt, &sr.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ServiceRequest{}, derrors.NotFound("cs.sr.not_found", "service request not found")
	}
	if err != nil {
		return domain.ServiceRequest{}, derrors.Wrap(derrors.KindInternal, "cs.sr.scan", "scan service request", err)
	}
	sr.RequestType = domain.ServiceRequestType(reqType)
	sr.Status = domain.ServiceRequestStatus(status)
	if m, err := unmarshalJSONBMap(payload); err == nil {
		sr.Payload = m
	}
	return sr, nil
}
