package postgres

import (
	"context"
	stderrors "errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/operations/domain"
	"github.com/ion-core/backend/internal/operations/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// MaintenanceAffectedCustomerRepository persists rows of
// operations.maintenance_affected_customers.
type MaintenanceAffectedCustomerRepository struct {
	pool *pgxpool.Pool
}

func NewMaintenanceAffectedCustomerRepository(pool *pgxpool.Pool) *MaintenanceAffectedCustomerRepository {
	return &MaintenanceAffectedCustomerRepository{pool: pool}
}

var _ port.MaintenanceAffectedCustomerRepository = (*MaintenanceAffectedCustomerRepository)(nil)

func (r *MaintenanceAffectedCustomerRepository) CreateBatch(ctx context.Context, rows []domain.MaintenanceAffectedCustomer) (int, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	written := 0
	for _, row := range rows {
		tag, err := r.pool.Exec(ctx, `
			INSERT INTO operations.maintenance_affected_customers
				(id, maintenance_event_id, customer_id, customer_segment)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (maintenance_event_id, customer_id) DO NOTHING
		`, row.ID, row.MaintenanceEventID, row.CustomerID, string(row.CustomerSegment))
		if err != nil {
			return written, mapDBError(err, "maint_affected_customer", "insert affected customer")
		}
		written += int(tag.RowsAffected())
	}
	return written, nil
}

func (r *MaintenanceAffectedCustomerRepository) ListByEvent(ctx context.Context, eventID uuid.UUID) ([]domain.MaintenanceAffectedCustomer, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, maintenance_event_id, customer_id,
		       COALESCE(customer_segment, 'broadband'),
		       notified_at, COALESCE(notification_channel, ''),
		       COALESCE(error_msg, '')
		  FROM operations.maintenance_affected_customers
		 WHERE maintenance_event_id = $1
		 ORDER BY created_at ASC
	`, eventID)
	if err != nil {
		return nil, mapDBError(err, "maint_affected_customer", "list affected customers")
	}
	defer rows.Close()
	out := []domain.MaintenanceAffectedCustomer{}
	for rows.Next() {
		var row domain.MaintenanceAffectedCustomer
		var seg, channel, errMsg string
		var notifiedAt *time.Time
		if err := rows.Scan(&row.ID, &row.MaintenanceEventID, &row.CustomerID, &seg, &notifiedAt, &channel, &errMsg); err != nil {
			return nil, err
		}
		row.CustomerSegment = domain.CustomerSegment(seg)
		row.NotifiedAt = notifiedAt
		row.NotificationChannel = channel
		row.ErrorMsg = errMsg
		out = append(out, row)
	}
	return out, rows.Err()
}

func (r *MaintenanceAffectedCustomerRepository) ListPendingNotification(ctx context.Context, eventID uuid.UUID, limit int) ([]domain.MaintenanceAffectedCustomer, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id, maintenance_event_id, customer_id,
		       COALESCE(customer_segment, 'broadband')
		  FROM operations.maintenance_affected_customers
		 WHERE maintenance_event_id = $1
		   AND notified_at IS NULL
		 ORDER BY created_at ASC
		 LIMIT $2
	`, eventID, limit)
	if err != nil {
		return nil, mapDBError(err, "maint_affected_customer", "list pending notifications")
	}
	defer rows.Close()
	out := []domain.MaintenanceAffectedCustomer{}
	for rows.Next() {
		var row domain.MaintenanceAffectedCustomer
		var seg string
		if err := rows.Scan(&row.ID, &row.MaintenanceEventID, &row.CustomerID, &seg); err != nil {
			return nil, err
		}
		row.CustomerSegment = domain.CustomerSegment(seg)
		out = append(out, row)
	}
	return out, rows.Err()
}

func (r *MaintenanceAffectedCustomerRepository) MarkNotified(ctx context.Context, id uuid.UUID, channel string) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE operations.maintenance_affected_customers
		   SET notified_at = NOW(),
		       notification_channel = $2,
		       error_msg = NULL
		 WHERE id = $1
	`, id, channel)
	if err != nil {
		return mapDBError(err, "maint_affected_customer", "mark notified")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("maint_affected_customer.not_found", "affected-customer row not found")
	}
	return nil
}

func (r *MaintenanceAffectedCustomerRepository) MarkNotifyError(ctx context.Context, id uuid.UUID, errMsg string) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE operations.maintenance_affected_customers
		   SET error_msg = $2
		 WHERE id = $1
	`, id, errMsg)
	if err != nil {
		return mapDBError(err, "maint_affected_customer", "mark notify error")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("maint_affected_customer.not_found", "affected-customer row not found")
	}
	return nil
}

// =====================================================================
// MaintenanceEscalationRepository
// =====================================================================

type MaintenanceEscalationRepository struct {
	pool *pgxpool.Pool
}

func NewMaintenanceEscalationRepository(pool *pgxpool.Pool) *MaintenanceEscalationRepository {
	return &MaintenanceEscalationRepository{pool: pool}
}

var _ port.MaintenanceEscalationRepository = (*MaintenanceEscalationRepository)(nil)

func (r *MaintenanceEscalationRepository) Create(ctx context.Context, e *domain.MaintenanceEscalation) error {
	if e == nil {
		return derrors.Validation("maint_escalation.nil", "escalation is nil")
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO operations.maintenance_escalations
			(id, maintenance_event_id, level, reason, escalated_to_user_id, escalated_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, e.ID, e.MaintenanceEventID, e.Level, e.Reason, e.EscalatedToUserID, e.EscalatedAt)
	return mapDBError(err, "maint_escalation", "insert escalation")
}

func (r *MaintenanceEscalationRepository) ListByEvent(ctx context.Context, eventID uuid.UUID) ([]domain.MaintenanceEscalation, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, maintenance_event_id, level, COALESCE(reason, ''),
		       escalated_to_user_id, escalated_at,
		       acknowledged_at, resolved_at
		  FROM operations.maintenance_escalations
		 WHERE maintenance_event_id = $1
		 ORDER BY escalated_at ASC
	`, eventID)
	if err != nil {
		return nil, mapDBError(err, "maint_escalation", "list by event")
	}
	defer rows.Close()
	out := []domain.MaintenanceEscalation{}
	for rows.Next() {
		var e domain.MaintenanceEscalation
		if err := rows.Scan(&e.ID, &e.MaintenanceEventID, &e.Level, &e.Reason,
			&e.EscalatedToUserID, &e.EscalatedAt, &e.AcknowledgedAt, &e.ResolvedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (r *MaintenanceEscalationRepository) HighestLevel(ctx context.Context, eventID uuid.UUID) (int, error) {
	var level *int
	err := r.pool.QueryRow(ctx, `
		SELECT MAX(level)
		  FROM operations.maintenance_escalations
		 WHERE maintenance_event_id = $1
	`, eventID).Scan(&level)
	if err != nil && !stderrors.Is(err, pgx.ErrNoRows) {
		return 0, mapDBError(err, "maint_escalation", "highest level")
	}
	if level == nil {
		return 0, nil
	}
	return *level, nil
}

func (r *MaintenanceEscalationRepository) MarkAcknowledged(ctx context.Context, id uuid.UUID, at time.Time) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE operations.maintenance_escalations
		   SET acknowledged_at = $2
		 WHERE id = $1
	`, id, at)
	return mapDBError(err, "maint_escalation", "ack")
}

func (r *MaintenanceEscalationRepository) MarkResolved(ctx context.Context, id uuid.UUID, at time.Time) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE operations.maintenance_escalations
		   SET resolved_at = $2
		 WHERE id = $1
	`, id, at)
	return mapDBError(err, "maint_escalation", "resolve")
}
