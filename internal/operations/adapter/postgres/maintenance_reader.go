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

// MaintenanceReader is the SQL bridge to field.maintenance_events for
// the Wave 126 operations.maintenance usecases. SQL-only (no Go imports
// across the field bounded context).
type MaintenanceReader struct {
	pool *pgxpool.Pool
}

func NewMaintenanceReader(pool *pgxpool.Pool) *MaintenanceReader {
	return &MaintenanceReader{pool: pool}
}

var _ port.MaintenanceReader = (*MaintenanceReader)(nil)

func (r *MaintenanceReader) FindEvent(ctx context.Context, eventID uuid.UUID) (*port.MaintenanceEventSummary, error) {
	var out port.MaintenanceEventSummary
	var segment string
	err := r.pool.QueryRow(ctx, `
		SELECT id, COALESCE(title, ''), status,
		       scheduled_start, scheduled_end, branch_id,
		       COALESCE(customer_segment, 'broadband'),
		       COALESCE(lead_time_notify_hours, 24),
		       COALESCE(approval_required, FALSE),
		       approved_by, approved_at,
		       overrun_at, COALESCE(overrun_notified, FALSE),
		       COALESCE(affected_customer_count, 0),
		       COALESCE(event_kind, '')
		  FROM field.maintenance_events
		 WHERE id = $1
	`, eventID).Scan(
		&out.ID, &out.Title, &out.Status,
		&out.ScheduledStart, &out.ScheduledEnd, &out.BranchID,
		&segment, &out.LeadTimeNotifyHours,
		&out.ApprovalRequired, &out.ApprovedBy, &out.ApprovedAt,
		&out.OverrunAt, &out.OverrunNotified,
		&out.AffectedCustomerCount, &out.EventKind,
	)
	if stderrors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, mapDBError(err, "maint_reader", "find event")
	}
	out.CustomerSegment = domain.CustomerSegment(segment)
	return &out, nil
}

func (r *MaintenanceReader) ListPendingApproval(ctx context.Context, limit int) ([]port.MaintenanceEventSummary, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id, COALESCE(title, ''), status,
		       scheduled_start, scheduled_end, branch_id,
		       COALESCE(customer_segment, 'broadband'),
		       COALESCE(lead_time_notify_hours, 24),
		       COALESCE(approval_required, FALSE),
		       approved_by, approved_at,
		       overrun_at, COALESCE(overrun_notified, FALSE),
		       COALESCE(affected_customer_count, 0),
		       COALESCE(event_kind, '')
		  FROM field.maintenance_events
		 WHERE approval_required = TRUE
		   AND approved_at IS NULL
		   AND status NOT IN ('completed','cancelled')
		 ORDER BY scheduled_start ASC
		 LIMIT $1
	`, limit)
	if err != nil {
		return nil, mapDBError(err, "maint_reader", "list pending approval")
	}
	defer rows.Close()
	return collectMaintRows(rows)
}

func (r *MaintenanceReader) ListPendingLeadTimeNotify(ctx context.Context, withinHours int, limit int) ([]port.MaintenanceEventSummary, error) {
	if limit <= 0 {
		limit = 100
	}
	if withinHours <= 0 {
		withinHours = 72
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id, COALESCE(title, ''), status,
		       scheduled_start, scheduled_end, branch_id,
		       COALESCE(customer_segment, 'broadband'),
		       COALESCE(lead_time_notify_hours, 24),
		       COALESCE(approval_required, FALSE),
		       approved_by, approved_at,
		       overrun_at, COALESCE(overrun_notified, FALSE),
		       COALESCE(affected_customer_count, 0),
		       COALESCE(event_kind, '')
		  FROM field.maintenance_events
		 WHERE status NOT IN ('completed','cancelled')
		   AND scheduled_start BETWEEN NOW() AND NOW() + ($1 || ' hours')::interval
		 ORDER BY scheduled_start ASC
		 LIMIT $2
	`, withinHours, limit)
	if err != nil {
		return nil, mapDBError(err, "maint_reader", "list pending lead-time notify")
	}
	defer rows.Close()
	return collectMaintRows(rows)
}

func (r *MaintenanceReader) ListInProgress(ctx context.Context, limit int) ([]port.MaintenanceEventSummary, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id, COALESCE(title, ''), status,
		       scheduled_start, scheduled_end, branch_id,
		       COALESCE(customer_segment, 'broadband'),
		       COALESCE(lead_time_notify_hours, 24),
		       COALESCE(approval_required, FALSE),
		       approved_by, approved_at,
		       overrun_at, COALESCE(overrun_notified, FALSE),
		       COALESCE(affected_customer_count, 0),
		       COALESCE(event_kind, '')
		  FROM field.maintenance_events
		 WHERE status = 'in_progress'
		   AND overrun_at IS NULL
		 ORDER BY scheduled_end ASC NULLS LAST
		 LIMIT $1
	`, limit)
	if err != nil {
		return nil, mapDBError(err, "maint_reader", "list in-progress")
	}
	defer rows.Close()
	return collectMaintRows(rows)
}

func (r *MaintenanceReader) MarkApproved(ctx context.Context, eventID, byUserID uuid.UUID, at time.Time) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE field.maintenance_events
		   SET approved_by = $2,
		       approved_at = $3,
		       updated_at  = NOW()
		 WHERE id = $1
	`, eventID, byUserID, at)
	if err != nil {
		return mapDBError(err, "maint_reader", "mark approved")
	}
	if tag.RowsAffected() == 0 {
		return derrors.NotFound("maint_reader.not_found", "maintenance event not found")
	}
	return nil
}

func (r *MaintenanceReader) MarkOverrun(ctx context.Context, eventID uuid.UUID, at time.Time) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE field.maintenance_events
		   SET overrun_at = $2,
		       overrun_notified = FALSE,
		       updated_at = NOW()
		 WHERE id = $1
		   AND overrun_at IS NULL
	`, eventID, at)
	if err != nil {
		return mapDBError(err, "maint_reader", "mark overrun")
	}
	if tag.RowsAffected() == 0 {
		// Already flagged or row absent — idempotent no-op.
		return nil
	}
	return nil
}

func (r *MaintenanceReader) UpdateAffectedCount(ctx context.Context, eventID uuid.UUID, count int) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE field.maintenance_events
		   SET affected_customer_count = $2,
		       approval_required = ($2 > 100) OR (customer_segment IN ('enterprise','mixed') AND $2 > 50),
		       updated_at = NOW()
		 WHERE id = $1
	`, eventID, count)
	return mapDBError(err, "maint_reader", "update affected count")
}

func collectMaintRows(rows pgx.Rows) ([]port.MaintenanceEventSummary, error) {
	out := []port.MaintenanceEventSummary{}
	for rows.Next() {
		var ev port.MaintenanceEventSummary
		var segment string
		if err := rows.Scan(
			&ev.ID, &ev.Title, &ev.Status,
			&ev.ScheduledStart, &ev.ScheduledEnd, &ev.BranchID,
			&segment, &ev.LeadTimeNotifyHours,
			&ev.ApprovalRequired, &ev.ApprovedBy, &ev.ApprovedAt,
			&ev.OverrunAt, &ev.OverrunNotified,
			&ev.AffectedCustomerCount, &ev.EventKind,
		); err != nil {
			return nil, err
		}
		ev.CustomerSegment = domain.CustomerSegment(segment)
		out = append(out, ev)
	}
	return out, rows.Err()
}
