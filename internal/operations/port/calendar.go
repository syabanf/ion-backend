// Wave 126 — port interfaces for the maintenance enhancements,
// internal-announcement dispatcher, operational calendar, and the
// cross-module SLA Ops View.
//
// Hexagonal layout: usecases depend on these interfaces; the postgres
// adapter implements them; the field-svc / cs-svc cmd wires it up. CS
// Dashboard aggregations are intentionally in this package too — they
// live alongside the rest of the Wave 126 surface so the SQL stays
// inside internal/operations/adapter/postgres/.
package port

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/operations/domain"
)

// =====================================================================
// 1. Maintenance — affected-customer materialization + escalation chain
// =====================================================================

// MaintenanceAffectedCustomerRepository persists
// operations.maintenance_affected_customers rows.
type MaintenanceAffectedCustomerRepository interface {
	// CreateBatch upserts a batch of rows. Existing (event, customer)
	// tuples are no-ops (ON CONFLICT DO NOTHING). Returns the count of
	// rows actually written.
	CreateBatch(ctx context.Context, rows []domain.MaintenanceAffectedCustomer) (int, error)
	// ListByEvent returns every affected-customer row for a given event,
	// ordered by created_at asc.
	ListByEvent(ctx context.Context, eventID uuid.UUID) ([]domain.MaintenanceAffectedCustomer, error)
	// ListPendingNotification returns rows where notified_at IS NULL.
	// Used by the lead-time cron.
	ListPendingNotification(ctx context.Context, eventID uuid.UUID, limit int) ([]domain.MaintenanceAffectedCustomer, error)
	// MarkNotified stamps notified_at + channel; clears error_msg.
	MarkNotified(ctx context.Context, id uuid.UUID, channel string) error
	// MarkNotifyError stamps error_msg without touching notified_at —
	// the cron can retry next tick.
	MarkNotifyError(ctx context.Context, id uuid.UUID, errMsg string) error
}

// MaintenanceEscalationRepository persists operations.maintenance_escalations.
type MaintenanceEscalationRepository interface {
	Create(ctx context.Context, e *domain.MaintenanceEscalation) error
	ListByEvent(ctx context.Context, eventID uuid.UUID) ([]domain.MaintenanceEscalation, error)
	HighestLevel(ctx context.Context, eventID uuid.UUID) (int, error)
	MarkAcknowledged(ctx context.Context, id uuid.UUID, at time.Time) error
	MarkResolved(ctx context.Context, id uuid.UUID, at time.Time) error
}

// MaintenanceReader is the minimal read surface the usecase needs from
// field.maintenance_events. Implemented as a SQL bridge in
// internal/operations/adapter/postgres/.
type MaintenanceReader interface {
	FindEvent(ctx context.Context, eventID uuid.UUID) (*MaintenanceEventSummary, error)
	ListPendingApproval(ctx context.Context, limit int) ([]MaintenanceEventSummary, error)
	ListPendingLeadTimeNotify(ctx context.Context, withinHours int, limit int) ([]MaintenanceEventSummary, error)
	ListInProgress(ctx context.Context, limit int) ([]MaintenanceEventSummary, error)
	MarkApproved(ctx context.Context, eventID, byUserID uuid.UUID, at time.Time) error
	MarkOverrun(ctx context.Context, eventID uuid.UUID, at time.Time) error
	UpdateAffectedCount(ctx context.Context, eventID uuid.UUID, count int) error
}

// MaintenanceEventSummary is the slim projection of
// field.maintenance_events the usecase consumes.
type MaintenanceEventSummary struct {
	ID                     uuid.UUID
	Title                  string
	Status                 string
	ScheduledStart         time.Time
	ScheduledEnd           *time.Time
	BranchID               *uuid.UUID
	CustomerSegment        domain.CustomerSegment
	LeadTimeNotifyHours    int
	ApprovalRequired       bool
	ApprovedBy             *uuid.UUID
	ApprovedAt             *time.Time
	OverrunAt              *time.Time
	OverrunNotified        bool
	AffectedCustomerCount  int
	EventKind              string
}

// =====================================================================
// 2. Internal Announcements
// =====================================================================

// AnnouncementRepository persists operations.internal_announcements.
type AnnouncementRepository interface {
	Create(ctx context.Context, a *domain.Announcement) error
	FindByID(ctx context.Context, id uuid.UUID) (*domain.Announcement, error)
	ListPending(ctx context.Context, asOf time.Time, limit int) ([]domain.Announcement, error)
	Update(ctx context.Context, a *domain.Announcement) error
}

// AnnouncementRecipientRepository persists operations.announcement_recipients.
type AnnouncementRecipientRepository interface {
	CreateBatch(ctx context.Context, rows []domain.AnnouncementRecipient) (int, error)
	ListByAnnouncement(ctx context.Context, announcementID uuid.UUID) ([]domain.AnnouncementRecipient, error)
	ListMyInbox(ctx context.Context, userID uuid.UUID, unreadOnly bool, limit int) ([]AnnouncementInboxEntry, error)
	MarkDelivered(ctx context.Context, id uuid.UUID, channel string, at time.Time) error
	MarkRead(ctx context.Context, announcementID, userID uuid.UUID, at time.Time) error
	CountDelivered(ctx context.Context, announcementID uuid.UUID) (delivered int, total int, err error)
}

// AnnouncementInboxEntry is the per-row payload for GET /announcements/inbox.
type AnnouncementInboxEntry struct {
	RecipientID    uuid.UUID
	AnnouncementID uuid.UUID
	Title          string
	Body           string
	Severity       domain.AnnouncementSeverity
	DeliveredAt    *time.Time
	ReadAt         *time.Time
	CreatedAt      time.Time
}

// =====================================================================
// 3. Operational Calendar
// =====================================================================

// CalendarEventRepository persists operations.calendar_events.
type CalendarEventRepository interface {
	Create(ctx context.Context, e *domain.CalendarEvent) error
	FindByID(ctx context.Context, id uuid.UUID) (*domain.CalendarEvent, error)
	ListInRange(ctx context.Context, from, to time.Time, scope domain.EventScope, scopeID *uuid.UUID, limit int) ([]domain.CalendarEvent, error)
	UpsertBySource(ctx context.Context, e *domain.CalendarEvent) error // idempotent (source, source_id)
	Update(ctx context.Context, e *domain.CalendarEvent) error
}

// CalendarSyncSource is the contract each upstream source implements so
// the CalendarService.AutoSync loop can pull rows for that source's
// time window. Keeps the auto-sync logic agnostic of upstream schemas.
type CalendarSyncSource interface {
	Source() domain.EventSource
	List(ctx context.Context, from, to time.Time, limit int) ([]domain.CalendarEvent, error)
}

// =====================================================================
// 4. Cross-Module SLA Ops View
// =====================================================================

// CrossModuleSLASnapshotRepository persists
// operations.cross_module_sla_snapshots.
type CrossModuleSLASnapshotRepository interface {
	Create(ctx context.Context, s *domain.SLASnapshot) error
	LatestForModule(ctx context.Context, module domain.SLAModule) (*domain.SLASnapshot, error)
	ListLatest(ctx context.Context) ([]domain.SLASnapshot, error)
	History(ctx context.Context, module domain.SLAModule, from, to time.Time, limit int) ([]domain.SLASnapshot, error)
}

// ModuleSLAReader is implemented by each module (cs/field/billing/etc.)
// — the cross-module aggregator calls each in turn and persists the
// returned stats as a snapshot row.
type ModuleSLAReader interface {
	ModuleName() domain.SLAModule
	SLAStats(ctx context.Context, periodStart, periodEnd time.Time) (domain.ModuleSLAStats, error)
}

// =====================================================================
// 5. Cross-context bridges (port-only) — implementations live in
// cmd/field-svc/main.go + cmd/cs-svc/main.go.
// =====================================================================

// CustomerSegmentResolver bridges into CRM + Network: given an OLT/ODP
// scope (or branch), returns the list of customer IDs and their segment.
// Used by MaterializeAffectedCustomers.
type CustomerSegmentResolver interface {
	// ResolveByMaintenanceEvent returns the cascade of customers
	// affected by a maintenance event. The bridge walks the event's
	// affected_nodes -> network.ports.customer_id -> crm.customers
	// (for the customer_type).
	ResolveByMaintenanceEvent(ctx context.Context, eventID uuid.UUID) ([]AffectedCustomerInfo, error)
}

// AffectedCustomerInfo is one row from the resolver.
type AffectedCustomerInfo struct {
	CustomerID      uuid.UUID
	CustomerSegment domain.CustomerSegment
}

// AnnouncementDispatcher wraps notifyx so the usecase doesn't import it
// directly. Per-recipient send; severity is passed through so the
// dispatcher can pick a template + channel mix (urgent → push+email+sms,
// important → push+email, info → push only).
type AnnouncementDispatcher interface {
	Dispatch(ctx context.Context, announcement *domain.Announcement, userID uuid.UUID) (channel string, err error)
}

// AudienceResolver bridges into identity.users: given a TargetAudience
// (and the announcement's targeting JSONB), returns the list of user IDs
// to deliver to. Implementation queries identity.role_permissions /
// identity.user_roles.
type AudienceResolver interface {
	Resolve(ctx context.Context, audience domain.AnnouncementTargetAudience, targeting map[string]any, limit int) ([]uuid.UUID, error)
}

// MaintenanceNotificationDispatcher dispatches per-affected-customer
// notifications for the lead-time cron. Severity isn't an axis here —
// the dispatcher picks the channel by customer preference.
type MaintenanceNotificationDispatcher interface {
	NotifyCustomer(ctx context.Context, eventID uuid.UUID, customerID uuid.UUID, segment domain.CustomerSegment) (channel string, err error)
}

// =====================================================================
// 6. CS Dashboard aggregations (housed here so the SQL stays in the
// internal/operations/adapter/postgres adapter — keeps the cross-cut
// SLA queries co-located).
// =====================================================================

// CSDashboardAggregationRepository persists cs.dashboard_aggregations
// rows. Each kind has a different payload shape; we keep it as JSONB
// for forward-compat with the dashboard's evolving columns.
type CSDashboardAggregationRepository interface {
	Create(ctx context.Context, kind string, scopeUserID, scopeTeamID *uuid.UUID, periodStart, periodEnd *time.Time, payload map[string]any) error
	LatestByKind(ctx context.Context, kind string, scopeUserID, scopeTeamID *uuid.UUID) (*DashboardAggregationRow, error)
}

// DashboardAggregationRow is the slim repo projection.
type DashboardAggregationRow struct {
	ID            uuid.UUID
	Kind          string
	ScopeUserID   *uuid.UUID
	ScopeTeamID   *uuid.UUID
	AggregatedAt  time.Time
	PeriodStart   *time.Time
	PeriodEnd     *time.Time
	Payload       map[string]any
}

// CSDashboardLiveReader is the SQL-only reader bridge that the
// CSDashboardService uses to compute the agent/team/escalation
// aggregations from the cs.* tables when no precomputed row exists.
type CSDashboardLiveReader interface {
	AgentQueue(ctx context.Context, userID uuid.UUID) (AgentQueueSnapshot, error)
	SupervisorTeamSLA(ctx context.Context, supervisorUserID uuid.UUID) (TeamSLASnapshot, error)
	EscalationQueue(ctx context.Context, minLevel int, limit int) ([]EscalationRow, error)
	SatisfactionSummary(ctx context.Context, from, to time.Time) (SatisfactionSummary, error)
	ChannelDistribution(ctx context.Context, from, to time.Time) (map[string]int, error)
	ActiveAgentIDs(ctx context.Context, limit int) ([]uuid.UUID, error)
	SupervisorTeamIDs(ctx context.Context, limit int) ([]uuid.UUID, error)
}

// AgentQueueSnapshot is the per-agent payload.
type AgentQueueSnapshot struct {
	UserID                uuid.UUID
	OpenAssigned          int
	UnassignedAvailable   int
	PendingInternal       int
	PendingCustomer       int
	ResolvedToday         int
	SLAAtRisk             int
	SLABreached           int
	OpenTickets           []AgentQueueTicket
}

// AgentQueueTicket is one row in the agent's open queue.
type AgentQueueTicket struct {
	TicketID                uuid.UUID
	Title                   string
	Priority                string
	Status                  string
	FirstResponseRemainingS int64
	ResolveRemainingS       int64
	Breached                bool
}

// TeamSLASnapshot is the supervisor team SLA payload.
type TeamSLASnapshot struct {
	SupervisorUserID    uuid.UUID
	TeamIDs             []uuid.UUID
	OpenCount           int
	ResolvedLast24h     int
	BreachedFirstResp   int
	BreachedResolve     int
	AvgFirstResponseMin int
	AvgResolveMin       int
	CompliancePct       float64
}

// EscalationRow is one row in the escalation queue list.
type EscalationRow struct {
	TicketID     uuid.UUID
	Title        string
	Priority     string
	Status       string
	Level        int
	EscalatedAt  time.Time
	AssignedTo   *uuid.UUID
}

// SatisfactionSummary is the CSAT aggregations payload.
type SatisfactionSummary struct {
	From         time.Time
	To           time.Time
	Count        int
	AvgRating    float64
	NPSScore     float64
	CriticalLow  []CSATCriticalRow
}

// CSATCriticalRow is one critically-low CSAT response (rating <=2).
type CSATCriticalRow struct {
	TicketID  uuid.UUID
	Rating    int
	Comment   string
	CreatedAt time.Time
}
