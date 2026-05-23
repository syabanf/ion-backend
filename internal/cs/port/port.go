// Package port defines the driving (UseCase) and driven (Repository)
// contracts for the customer-service bounded context.
//
// Same hexagonal layout as identity / crm / reseller / hris:
// HTTP handlers depend on a UseCase interface; the UseCase depends on
// repository interfaces; postgres adapters implement the repository
// interfaces. The domain stays oblivious to both transport and
// storage so the bounded context can be extracted into its own
// service (cmd/cs-svc) without touching domain rules.
package port

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/cs/domain"
)

// =====================================================================
// Ticket inputs
// =====================================================================

// CreateTicketInput is the usecase-facing create payload. The handler
// strips it from the HTTP DTO; the usecase enforces invariants via
// domain.NewTicket.
type CreateTicketInput struct {
	CustomerID     uuid.UUID
	OpenedBy       uuid.UUID
	OpenedVia      domain.OpenedVia
	TicketType     domain.TicketType
	Title          string
	Description    string
	Priority       domain.Priority
	SourceMetadata map[string]any
}

// TicketListFilter scopes a ticket list query. Zero-value fields are
// not applied. Limit/Offset default in the repo if unset.
type TicketListFilter struct {
	CustomerID       *uuid.UUID
	AssignedUserID   *uuid.UUID
	AssignedTeamID   *uuid.UUID
	Status           string
	Priority         string
	TicketType       string
	OpenedVia        string
	OnlyUnassigned   bool
	Limit            int
	Offset           int
}

// CommentListFilter scopes a per-ticket comment query.
type CommentListFilter struct {
	TicketID         uuid.UUID
	IncludeDeleted   bool
	IncludeInternal  bool
	Limit            int
	Offset           int
}

// MentionListFilter scopes a per-user mention inbox.
type MentionListFilter struct {
	MentionedUserID uuid.UUID
	UnreadOnly      bool
	Since           *time.Time
	Limit           int
	Offset          int
}

// CreateCommentInput carries the HTTP create payload through to the
// usecase. Mentions are extracted by the usecase from Body.
type CreateCommentInput struct {
	TicketID    uuid.UUID
	AuthorID    uuid.UUID
	AuthorRole  string
	Body        string
	IsInternal  bool
	Attachments []domain.CommentAttachment
}

// =====================================================================
// UseCase — what HTTP handlers depend on
// =====================================================================

// TicketUseCase is the orchestration surface for the ticket aggregate.
// Every transition emits a TicketEvent + optional notification fan-out.
type TicketUseCase interface {
	CreateTicket(ctx context.Context, in CreateTicketInput) (*domain.Ticket, error)
	GetTicket(ctx context.Context, id uuid.UUID) (*domain.Ticket, error)
	ListTickets(ctx context.Context, f TicketListFilter) ([]domain.Ticket, int, error)

	AssignTicket(ctx context.Context, ticketID, assignedUserID, byUserID uuid.UUID, actorRole string) (*domain.Ticket, error)
	StartTicket(ctx context.Context, ticketID, byUserID uuid.UUID, actorRole string) (*domain.Ticket, error)
	PauseTicket(ctx context.Context, ticketID uuid.UUID, kind domain.PauseKind, reason string, byUserID uuid.UUID, actorRole string) (*domain.Ticket, error)
	ResumeTicket(ctx context.Context, ticketID, byUserID uuid.UUID, actorRole string) (*domain.Ticket, error)
	ResolveTicket(ctx context.Context, ticketID uuid.UUID, resolution string, byUserID uuid.UUID, actorRole string) (*domain.Ticket, error)
	CloseTicket(ctx context.Context, ticketID, byUserID uuid.UUID, actorRole string) (*domain.Ticket, error)
	ReopenTicket(ctx context.Context, ticketID, byUserID uuid.UUID, reason, actorRole string) (*domain.Ticket, error)
	ChangePriority(ctx context.Context, ticketID uuid.UUID, newPriority domain.Priority, byUserID uuid.UUID, actorRole string) (*domain.Ticket, error)

	ListEvents(ctx context.Context, ticketID uuid.UUID, limit, offset int) ([]domain.TicketEvent, error)
}

// CommentUseCase covers comment CRUD + @mention parsing.
type CommentUseCase interface {
	AddComment(ctx context.Context, in CreateCommentInput) (*domain.Comment, []domain.Mention, error)
	EditComment(ctx context.Context, commentID uuid.UUID, newBody string, byUserID uuid.UUID, isSupervisor bool) (*domain.Comment, error)
	DeleteComment(ctx context.Context, commentID, byUserID uuid.UUID, isSupervisor bool) error
	ListComments(ctx context.Context, f CommentListFilter) ([]domain.Comment, error)
}

// MentionUseCase covers the per-user mention inbox.
type MentionUseCase interface {
	ListMyMentions(ctx context.Context, f MentionListFilter) ([]domain.Mention, error)
	MarkAsRead(ctx context.Context, mentionID, byUserID uuid.UUID) error
}

// ChannelUseCase covers admin CRUD on cs.ticket_channels.
type ChannelUseCase interface {
	ListChannels(ctx context.Context, onlyActive bool) ([]domain.Channel, error)
	CreateChannel(ctx context.Context, code, name string, kind domain.ChannelKind, isActive bool, config map[string]any) (*domain.Channel, error)
	UpdateChannel(ctx context.Context, code string, name *string, kind *domain.ChannelKind, isActive *bool, config map[string]any) (*domain.Channel, error)
}

// =====================================================================
// Repository interfaces (driven ports)
// =====================================================================

type TicketRepository interface {
	Create(ctx context.Context, t *domain.Ticket) error
	FindByID(ctx context.Context, id uuid.UUID) (*domain.Ticket, error)
	List(ctx context.Context, f TicketListFilter) ([]domain.Ticket, int, error)
	// Update persists every mutable column on the ticket. The usecase
	// re-loads + mutates + Update so the SM rules in the domain remain
	// the single source of truth.
	Update(ctx context.Context, t *domain.Ticket) error
	// NextTicketNo returns the next sequential ticket number in
	// `TKT-YYYY-NNNNNNNN` format. Atomic on the DB side (the adapter
	// uses a SERIAL counter / COUNT(*)+1 in a transaction).
	NextTicketNo(ctx context.Context, year int) (string, error)
}

type TicketEventRepository interface {
	Insert(ctx context.Context, ev *domain.TicketEvent) error
	List(ctx context.Context, ticketID uuid.UUID, limit, offset int) ([]domain.TicketEvent, error)
}

type TicketCommentRepository interface {
	Insert(ctx context.Context, c *domain.Comment) error
	Update(ctx context.Context, c *domain.Comment) error
	FindByID(ctx context.Context, id uuid.UUID) (*domain.Comment, error)
	List(ctx context.Context, f CommentListFilter) ([]domain.Comment, error)
}

type TicketMentionRepository interface {
	Insert(ctx context.Context, m *domain.Mention) error
	List(ctx context.Context, f MentionListFilter) ([]domain.Mention, error)
	FindByID(ctx context.Context, id uuid.UUID) (*domain.Mention, error)
	MarkRead(ctx context.Context, id uuid.UUID, at time.Time) error
	// ListUnreadOlderThan is used by the mention-reminder cron — it
	// returns mentions older than `cutoff` whose read_at is still null.
	ListUnreadOlderThan(ctx context.Context, cutoff time.Time, limit int) ([]domain.Mention, error)
}

type TicketChannelRepository interface {
	Insert(ctx context.Context, c *domain.Channel) error
	Update(ctx context.Context, c *domain.Channel) error
	FindByCode(ctx context.Context, code string) (*domain.Channel, error)
	List(ctx context.Context, onlyActive bool) ([]domain.Channel, error)
}

// =====================================================================
// Cross-context bridges
// =====================================================================

// MentionResolver maps `@username` literals to user IDs. The default
// implementation in cmd/cs-svc/main.go is a tiny SQL query against
// identity.users (matching on email local-part for now since the
// identity schema doesn't yet carry a dedicated `username` column).
//
// Unknown names get silently dropped — saving the comment must not
// fail just because the user typed `@maybeATypo`.
type MentionResolver interface {
	Resolve(ctx context.Context, usernames []string) (map[string]uuid.UUID, error)
}

// NotificationBridge fans CS events out to the platform-wide notifier
// (notifyx). The default impl wraps a *notifyx.Dispatcher, but the
// interface here keeps the usecase free of pkg/notifyx imports so
// tests can pass a stub.
//
// Both methods are best-effort: a failure to send a push MUST NOT
// fail the database write.
type NotificationBridge interface {
	NotifyMention(ctx context.Context, mention *domain.Mention, ticketTitle, mentionedByName string)
	NotifyAssignment(ctx context.Context, ticketID, assignedUserID uuid.UUID, ticketTitle string)
}

// AutoCloseRepository is the slice the daily auto-close cron needs.
// Combined with TicketRepository.Update to mark resolved → closed.
type AutoCloseRepository interface {
	ListResolvedOlderThan(ctx context.Context, cutoff time.Time, limit int) ([]domain.Ticket, error)
}
