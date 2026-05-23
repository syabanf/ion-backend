// Wave 124 — port interfaces for SLA matrix, service requests, teams,
// CSAT, communications, plus the cross-context bridges
// (CustomerTypeResolver, WOFromTicketBridge, CSATInviteDispatcher).
package port

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/cs/domain"
)

// =====================================================================
// SLA matrix
// =====================================================================

// SLAMatrixRepository — CRUD over cs.sla_matrix.
type SLAMatrixRepository interface {
	// FindByKey returns the most-recent active matrix row for the
	// (customerType, ticketType, priority) tuple whose effective_from
	// is on or before `at`. Returns NotFound if no row matches.
	FindByKey(ctx context.Context, ct domain.CustomerType, tt domain.TicketType, p domain.Priority, at time.Time) (*domain.SLAMatrixEntry, error)
	FindByID(ctx context.Context, id uuid.UUID) (*domain.SLAMatrixEntry, error)
	List(ctx context.Context, f SLAMatrixFilter) ([]domain.SLAMatrixEntry, error)
	Upsert(ctx context.Context, e *domain.SLAMatrixEntry) error
}

// SLAMatrixFilter scopes a list query.
type SLAMatrixFilter struct {
	CustomerType string
	TicketType   string
	Priority     string
	OnlyActive   bool
	Limit        int
	Offset       int
}

// =====================================================================
// Service Request
// =====================================================================

type ServiceRequestRepository interface {
	Insert(ctx context.Context, sr *domain.ServiceRequest) error
	Update(ctx context.Context, sr *domain.ServiceRequest) error
	FindByID(ctx context.Context, id uuid.UUID) (*domain.ServiceRequest, error)
	List(ctx context.Context, f ServiceRequestFilter) ([]domain.ServiceRequest, int, error)
	ListPendingApproval(ctx context.Context, limit int) ([]domain.ServiceRequest, error)
}

type ServiceRequestFilter struct {
	CustomerID  *uuid.UUID
	TicketID    *uuid.UUID
	Status      string
	RequestType string
	Limit       int
	Offset      int
}

// =====================================================================
// Team + members
// =====================================================================

type TeamRepository interface {
	Insert(ctx context.Context, t *domain.Team) error
	Update(ctx context.Context, t *domain.Team) error
	FindByID(ctx context.Context, id uuid.UUID) (*domain.Team, error)
	List(ctx context.Context, onlyActive bool) ([]domain.Team, error)
}

type TeamMemberRepository interface {
	Insert(ctx context.Context, m *domain.TeamMember) error
	Update(ctx context.Context, m *domain.TeamMember) error
	FindActiveByTeamUser(ctx context.Context, teamID, userID uuid.UUID) (*domain.TeamMember, error)
	ListByTeam(ctx context.Context, teamID uuid.UUID, includeLeft bool) ([]domain.TeamMember, error)
	// OpenTicketCountByUser returns the number of open tickets per user
	// for the given team (used by round-robin). The map's key is the
	// member's user_id; missing users default to 0.
	OpenTicketCountByUser(ctx context.Context, teamID uuid.UUID) (map[uuid.UUID]int, error)
}

// =====================================================================
// Assignment history
// =====================================================================

type AssignmentHistoryRepository interface {
	Insert(ctx context.Context, ev *domain.AssignmentEvent) error
	ListByTicket(ctx context.Context, ticketID uuid.UUID, limit int) ([]domain.AssignmentEvent, error)
}

// =====================================================================
// CSAT
// =====================================================================

type CSATRepository interface {
	Insert(ctx context.Context, r *domain.CSATResponse) error
	FindByTicket(ctx context.Context, ticketID uuid.UUID) (*domain.CSATResponse, error)
	Aggregations(ctx context.Context, f CSATAggregationFilter) (CSATAggregations, error)
	// ListTicketsNeedingFollowupInvite returns ticket IDs of tickets
	// resolved between (now-followup, now-cutoff) that have no csat
	// response yet. Used by the CSAT followup cron — one re-invite
	// per ticket, tracked via cs.ticket_events kind='csat_invite_resent'.
	ListTicketsNeedingFollowupInvite(ctx context.Context, resolvedSince, resolvedBefore time.Time, limit int) ([]uuid.UUID, error)
}

// CSATAggregationFilter — optional from/to and per-agent / per-team filter.
type CSATAggregationFilter struct {
	From          *time.Time
	To            *time.Time
	AgentUserID   *uuid.UUID
	TeamID        *uuid.UUID
}

// CSATAggregations summarizes a slice of CSAT rows.
type CSATAggregations struct {
	Count        int
	AvgRating    float64
	Promoters    int // rating 5
	Passives     int // rating 4
	Detractors   int // rating 1-2
	Neutrals     int // rating 3
	PromoterPct  float64
	DetractorPct float64
	NPSScore     float64 // PromoterPct - DetractorPct
}

// =====================================================================
// Communication
// =====================================================================

type CommunicationRepository interface {
	Insert(ctx context.Context, c *domain.Communication) error
	Update(ctx context.Context, c *domain.Communication) error
	FindByID(ctx context.Context, id uuid.UUID) (*domain.Communication, error)
	FindByExternalMessageID(ctx context.Context, mid string) (*domain.Communication, error)
	ListByTicket(ctx context.Context, ticketID uuid.UUID, limit, offset int) ([]domain.Communication, error)
}

// =====================================================================
// Cross-context bridges (Wave 124)
// =====================================================================

// CustomerTypeResolver maps a crm.customers row's customer_type to the
// CS domain CustomerType. Default impl in cmd/cs-svc/main.go is a SQL
// query against crm.customers; missing rows fall back to residential.
type CustomerTypeResolver interface {
	Resolve(ctx context.Context, customerID uuid.UUID) (domain.CustomerType, error)
}

// WOFromTicketBridge creates a WO in field.work_orders from a ticket.
// Returns the new WO id. Default impl is an inline SQL INSERT in
// cmd/cs-svc/main.go.
type WOFromTicketBridge interface {
	CreateWOFromTicket(ctx context.Context, ticketID uuid.UUID, woTemplateID *uuid.UUID, scheduledAt *time.Time, createdBy uuid.UUID) (uuid.UUID, error)
}

// CSATInviteDispatcher sends the CSAT invite via the customer's
// preferred channel (email / whatsapp / sms / inapp). Wraps notifyx.
type CSATInviteDispatcher interface {
	SendInvite(ctx context.Context, ticketID, customerID uuid.UUID, channel string) error
}

// =====================================================================
// Wave 124 — additional driving (usecase) contracts.
//
// Listed here so the HTTP handler depends on interfaces, not concrete
// usecase structs.
// =====================================================================

type SLAUseCase interface {
	GetMatrix(ctx context.Context, f SLAMatrixFilter) ([]domain.SLAMatrixEntry, error)
	UpsertMatrix(ctx context.Context, e *domain.SLAMatrixEntry) error
	TicketSLA(ctx context.Context, ticketID uuid.UUID) (TicketSLASummary, error)
	EvaluateBreaches(ctx context.Context) (BreachReport, error)
}

// TicketSLASummary is what GET /api/cs/tickets/{id}/sla returns.
type TicketSLASummary struct {
	TicketID                  uuid.UUID
	SLAMatrixID               *uuid.UUID
	FirstResponseDueAt        *time.Time
	ResolveDueAt              *time.Time
	RemainingFirstRespSeconds int64
	RemainingResolveSeconds   int64
	BreachedFirstResponse     bool
	BreachedResolve           bool
	WarnedAt                  *time.Time
}

// BreachReport summarizes a single EvaluateBreaches sweep.
type BreachReport struct {
	Evaluated              int
	NewFirstResponseBreach int
	NewResolveBreach       int
	WarningsDispatched     int
}

type ServiceRequestUseCase interface {
	Submit(ctx context.Context, in SubmitServiceRequestInput) (*domain.ServiceRequest, error)
	Approve(ctx context.Context, srID, byUserID uuid.UUID) (*domain.ServiceRequest, error)
	Reject(ctx context.Context, srID, byUserID uuid.UUID, reason string) (*domain.ServiceRequest, error)
	StartFulfillment(ctx context.Context, srID, byUserID uuid.UUID) (*domain.ServiceRequest, error)
	MarkFulfilled(ctx context.Context, srID, byUserID uuid.UUID) (*domain.ServiceRequest, error)
	Cancel(ctx context.Context, srID, byUserID uuid.UUID, reason string) (*domain.ServiceRequest, error)
	Get(ctx context.Context, id uuid.UUID) (*domain.ServiceRequest, error)
	List(ctx context.Context, f ServiceRequestFilter) ([]domain.ServiceRequest, int, error)
}

// SubmitServiceRequestInput — fields needed to spawn an SR.
type SubmitServiceRequestInput struct {
	CustomerID  uuid.UUID
	TicketID    *uuid.UUID // optional; if nil, usecase auto-creates a ticket
	RequestType domain.ServiceRequestType
	SubmittedBy uuid.UUID
	OpenedVia   domain.OpenedVia
	Title       string
	Description string
	Priority    domain.Priority
	Payload     map[string]any
}

type TeamUseCase interface {
	CreateTeam(ctx context.Context, name, desc string, managerUserID *uuid.UUID, focus []domain.TicketType) (*domain.Team, error)
	ListTeams(ctx context.Context, onlyActive bool) ([]domain.Team, error)
	GetTeam(ctx context.Context, id uuid.UUID) (*domain.Team, error)
	AddMember(ctx context.Context, teamID, userID uuid.UUID, role domain.TeamMemberRole) (*domain.TeamMember, error)
	RemoveMember(ctx context.Context, teamID, userID uuid.UUID) error
	ListMembers(ctx context.Context, teamID uuid.UUID, includeLeft bool) ([]domain.TeamMember, error)
	AssignTicketToTeam(ctx context.Context, ticketID, teamID, byUserID uuid.UUID, actorRole string) (*domain.Ticket, error)
	RoundRobinAssign(ctx context.Context, ticketID, teamID, byUserID uuid.UUID, actorRole string) (*domain.Ticket, error)
}

type WOFromTicketUseCase interface {
	CreateWO(ctx context.Context, ticketID uuid.UUID, woTemplateID *uuid.UUID, scheduledAt *time.Time, byUserID uuid.UUID, actorRole string) (uuid.UUID, error)
}

type CSATUseCase interface {
	SendInvite(ctx context.Context, ticketID uuid.UUID, channel string) error
	RecordResponse(ctx context.Context, ticketID uuid.UUID, rating int, comment, channel string) (*domain.CSATResponse, error)
	Aggregations(ctx context.Context, f CSATAggregationFilter) (CSATAggregations, error)
	GetByTicket(ctx context.Context, ticketID uuid.UUID) (*domain.CSATResponse, error)
}

type CommunicationUseCase interface {
	LogOutbound(ctx context.Context, in LogOutboundInput) (*domain.Communication, error)
	LogInbound(ctx context.Context, in LogInboundInput) (*domain.Communication, error)
	List(ctx context.Context, ticketID uuid.UUID, limit, offset int) ([]domain.Communication, error)
}

type LogOutboundInput struct {
	TicketID          uuid.UUID
	Kind              domain.CommunicationKind
	CounterpartyKind  domain.CounterpartyKind
	CounterpartyID    *uuid.UUID
	CounterpartyLabel string
	Subject           string
	Body              string
	Attachments       []domain.CommAttachment
	ByUserID          uuid.UUID
	ActorRole         string
}

type LogInboundInput struct {
	Kind              domain.CommunicationKind
	ExternalMessageID string
	TicketID          *uuid.UUID // optional explicit link
	CounterpartyLabel string     // sender (email-from, whatsapp-msisdn)
	Subject           string
	Body              string
	Attachments       []domain.CommAttachment
}
