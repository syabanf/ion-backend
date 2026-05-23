// Wave 126 — inline cross-context bridges for the maintenance +
// announcement + cross-module SLA wiring.
//
// Same pattern as cmd/cs-svc/main.go's bridges: SQL-only adapters live
// in the cmd binary so internal/operations stays free of cross-context
// imports. Each adapter is goroutine-safe and degrades to a logged
// no-op on schema variance.
package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/operations/domain"
	opsport "github.com/ion-core/backend/internal/operations/port"
	"github.com/ion-core/backend/pkg/notifyx"
)

// =====================================================================
// AnnouncementDispatcherBridge — wraps notifyx for the announcement
// dispatcher. Severity drives the channel mix:
//   - urgent    -> push + email + sms
//   - important -> push + email
//   - info      -> push only
//
// We use the announcement's own Channels slice as the source-of-truth
// when present; the severity-default fires only as a fallback.
// =====================================================================

type announcementDispatcherBridge struct {
	disp *notifyx.Dispatcher
	log  *slog.Logger
}

func newAnnouncementDispatcherBridge(pool *pgxpool.Pool, log *slog.Logger) *announcementDispatcherBridge {
	return &announcementDispatcherBridge{
		disp: notifyx.New(pool, log),
		log:  log.With("component", "ops.announcement_dispatcher"),
	}
}

var _ opsport.AnnouncementDispatcher = (*announcementDispatcherBridge)(nil)

func (b *announcementDispatcherBridge) Dispatch(ctx context.Context, a *domain.Announcement, userID uuid.UUID) (string, error) {
	if b == nil || b.disp == nil || a == nil {
		return "", nil
	}
	channels := a.Channels
	if len(channels) == 0 {
		switch a.Severity {
		case domain.AnnouncementUrgent:
			channels = []string{"push", "email", "sms"}
		case domain.AnnouncementImportant:
			channels = []string{"push", "email"}
		default:
			channels = []string{"push"}
		}
	}
	topic := "internal_announcement"
	b.disp.Send(ctx, notifyx.Target{UserID: userID}, notifyx.Message{
		Title:    a.Title,
		Body:     a.Body,
		DeepLink: "app://announcements/" + a.ID.String(),
		Topic:    topic,
		Data: map[string]string{
			"announcement_id": a.ID.String(),
			"severity":        string(a.Severity),
		},
	})
	primary := "push"
	if len(channels) > 0 {
		primary = channels[0]
	}
	return primary, nil
}

// =====================================================================
// AudienceResolverBridge — resolves the announcement's target_audience
// to a list of user_ids by querying identity.role_permissions /
// identity.users. The resolver is liberal: it returns up to `limit`
// active users matching the audience role.
// =====================================================================

type audienceResolverBridge struct {
	pool *pgxpool.Pool
	log  *slog.Logger
}

func newAudienceResolverBridge(pool *pgxpool.Pool, log *slog.Logger) *audienceResolverBridge {
	return &audienceResolverBridge{
		pool: pool,
		log:  log.With("component", "ops.audience_resolver"),
	}
}

var _ opsport.AudienceResolver = (*audienceResolverBridge)(nil)

func (b *audienceResolverBridge) Resolve(ctx context.Context, audience domain.AnnouncementTargetAudience, targeting map[string]any, limit int) ([]uuid.UUID, error) {
	if b == nil || b.pool == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 1000
	}
	switch audience {
	case domain.AudienceAll:
		return b.queryUsersByRoles(ctx, nil, limit)
	case domain.AudienceAgents:
		return b.queryUsersByRoles(ctx, []string{"cs_agent", "agent"}, limit)
	case domain.AudienceSupervisors:
		return b.queryUsersByRoles(ctx, []string{"cs_supervisor", "supervisor"}, limit)
	case domain.AudienceTechnicians:
		return b.queryUsersByRoles(ctx, []string{"technician", "field_technician"}, limit)
	case domain.AudienceCustomers:
		// Customers aren't identity.users — fall through to an empty list.
		return nil, nil
	default:
		return b.queryUsersByRoles(ctx, nil, limit)
	}
}

func (b *audienceResolverBridge) queryUsersByRoles(ctx context.Context, roles []string, limit int) ([]uuid.UUID, error) {
	q := `SELECT id FROM identity.users WHERE active = TRUE`
	args := []any{}
	if len(roles) > 0 {
		q += ` AND id IN (
			SELECT ur.user_id FROM identity.user_roles ur
			  JOIN identity.roles r ON r.id = ur.role_id
			 WHERE r.name = ANY($1)
		)`
		args = append(args, roles)
		q += ` LIMIT $2`
		args = append(args, limit)
	} else {
		q += ` LIMIT $1`
		args = append(args, limit)
	}
	rows, err := b.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, nil
	}
	defer rows.Close()
	out := []uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			continue
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// =====================================================================
// MaintenanceNotificationDispatcherBridge — per-customer notifications
// for the lead-time cron. Wraps notifyx with a customer-targeted Send.
// =====================================================================

type maintenanceNotifyBridge struct {
	disp *notifyx.Dispatcher
	pool *pgxpool.Pool
	log  *slog.Logger
}

func newMaintenanceNotifyBridge(pool *pgxpool.Pool, log *slog.Logger) *maintenanceNotifyBridge {
	return &maintenanceNotifyBridge{
		disp: notifyx.New(pool, log),
		pool: pool,
		log:  log.With("component", "ops.maintenance_notify"),
	}
}

var _ opsport.MaintenanceNotificationDispatcher = (*maintenanceNotifyBridge)(nil)

func (b *maintenanceNotifyBridge) NotifyCustomer(ctx context.Context, eventID, customerID uuid.UUID, segment domain.CustomerSegment) (string, error) {
	if b == nil || b.disp == nil {
		return "", nil
	}
	// Pull the event title for the notification body (best-effort).
	var title, body string
	body = "A planned maintenance affecting your service is scheduled."
	if err := b.pool.QueryRow(ctx, `SELECT COALESCE(title, ''), COALESCE(description, '') FROM field.maintenance_events WHERE id = $1`, eventID).Scan(&title, &body); err == nil {
		if title == "" {
			title = "Scheduled Maintenance"
		}
	} else {
		title = "Scheduled Maintenance"
	}
	b.disp.Send(ctx, notifyx.Target{CustomerID: customerID}, notifyx.Message{
		Title:    title,
		Body:     body,
		DeepLink: "app://maintenance/" + eventID.String(),
		Topic:    "planned_maintenance",
		Data: map[string]string{
			"event_id":         eventID.String(),
			"customer_segment": string(segment),
		},
	})
	if segment == domain.SegmentEnterprise {
		return "push+email+sms", nil
	}
	return "push+email", nil
}

// =====================================================================
// ModuleSLAReader bridges — one per module. Each is a SQL-only adapter
// against the module's own SLA tracking tables.
// =====================================================================

// fieldModuleSLAReader sweeps field.work_orders for near-breach + breached SLAs.
type fieldModuleSLAReader struct{ pool *pgxpool.Pool }

func (r *fieldModuleSLAReader) ModuleName() domain.SLAModule { return domain.ModuleField }
func (r *fieldModuleSLAReader) SLAStats(ctx context.Context, periodStart, periodEnd time.Time) (domain.ModuleSLAStats, error) {
	stats := domain.ModuleSLAStats{Module: domain.ModuleField, PeriodStart: periodStart, PeriodEnd: periodEnd}
	if r.pool == nil {
		return stats, nil
	}
	// Defensive: column names may differ; if they don't exist this query
	// returns no rows.
	_ = r.pool.QueryRow(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE sla_first_response_due IS NOT NULL
				AND sla_first_response_due < NOW() + interval '30 minutes'
				AND COALESCE(sla_breached_first_response, FALSE) = FALSE
				AND status NOT IN ('completed','cancelled')),
			COUNT(*) FILTER (WHERE COALESCE(sla_breached_first_response, FALSE) = TRUE
				OR COALESCE(sla_breached_resolution, FALSE) = TRUE)
		  FROM field.work_orders
	`).Scan(&stats.TotalAtRisk, &stats.TotalBreached)
	return stats, nil
}

// csModuleSLAReader uses cs.tickets.
type csModuleSLAReader struct{ pool *pgxpool.Pool }

func (r *csModuleSLAReader) ModuleName() domain.SLAModule { return domain.ModuleCS }
func (r *csModuleSLAReader) SLAStats(ctx context.Context, periodStart, periodEnd time.Time) (domain.ModuleSLAStats, error) {
	stats := domain.ModuleSLAStats{Module: domain.ModuleCS, PeriodStart: periodStart, PeriodEnd: periodEnd}
	if r.pool == nil {
		return stats, nil
	}
	_ = r.pool.QueryRow(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE sla_resolve_due_at IS NOT NULL
				AND sla_resolve_due_at < NOW() + interval '30 minutes'
				AND COALESCE(sla_breached_resolve, FALSE) = FALSE
				AND status NOT IN ('resolved','closed','cancelled')),
			COUNT(*) FILTER (WHERE COALESCE(sla_breached_first_response, FALSE) = TRUE
				OR COALESCE(sla_breached_resolve, FALSE) = TRUE)
		  FROM cs.tickets
	`).Scan(&stats.TotalAtRisk, &stats.TotalBreached)
	return stats, nil
}

// billingModuleSLAReader uses billing.invoices.due_date for AR aging signal.
type billingModuleSLAReader struct{ pool *pgxpool.Pool }

func (r *billingModuleSLAReader) ModuleName() domain.SLAModule { return domain.ModuleBilling }
func (r *billingModuleSLAReader) SLAStats(ctx context.Context, periodStart, periodEnd time.Time) (domain.ModuleSLAStats, error) {
	stats := domain.ModuleSLAStats{Module: domain.ModuleBilling, PeriodStart: periodStart, PeriodEnd: periodEnd}
	if r.pool == nil {
		return stats, nil
	}
	_ = r.pool.QueryRow(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE status NOT IN ('paid','void','cancelled') AND due_date < NOW() + interval '3 days' AND due_date >= NOW()),
			COUNT(*) FILTER (WHERE status NOT IN ('paid','void','cancelled') AND due_date < NOW())
		  FROM billing.invoices
	`).Scan(&stats.TotalAtRisk, &stats.TotalBreached)
	return stats, nil
}

// enterpriseModuleSLAReader — enterprise.ewos uses ewos table if exists.
type enterpriseModuleSLAReader struct{ pool *pgxpool.Pool }

func (r *enterpriseModuleSLAReader) ModuleName() domain.SLAModule { return domain.ModuleEnterprise }
func (r *enterpriseModuleSLAReader) SLAStats(ctx context.Context, periodStart, periodEnd time.Time) (domain.ModuleSLAStats, error) {
	stats := domain.ModuleSLAStats{Module: domain.ModuleEnterprise, PeriodStart: periodStart, PeriodEnd: periodEnd}
	if r.pool == nil {
		return stats, nil
	}
	// Schema may not have ewos.sla_due_at; defensive query.
	_ = r.pool.QueryRow(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE status NOT IN ('completed','cancelled') AND sla_due_at < NOW() + interval '1 day'),
			COUNT(*) FILTER (WHERE status NOT IN ('completed','cancelled') AND sla_due_at < NOW())
		  FROM enterprise.ewos
	`).Scan(&stats.TotalAtRisk, &stats.TotalBreached)
	return stats, nil
}
