package usecase

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/cs/domain"
	"github.com/ion-core/backend/internal/cs/port"
)

// =====================================================================
// Wave 123 — CommentService.AddComment + @mention resolution tests.
//
// Verifies:
//  - mention parsing extracts the right tokens
//  - resolver returns user IDs (unknown names get dropped silently)
//  - one mention row per (comment, user) — self-mention is skipped
//  - NotificationBridge.NotifyMention is fanned out exactly once per
//    resolved user
//  - first_response_at stamps on the first non-internal comment
// =====================================================================

func TestAddComment_ExtractsAndResolvesMentions(t *testing.T) {
	ctx := context.Background()

	authorID := uuid.New()
	noc := uuid.New()
	field := uuid.New()
	customer := uuid.New()

	// Pre-existing ticket in open status. Author is the customer's
	// CS agent; mentioned users are noc_engineer + field.tech.
	ticket := mustNewTicket(t, customer, authorID)
	tickets := &stubTicketRepo{byID: map[uuid.UUID]*domain.Ticket{ticket.ID: ticket}}
	comments := &stubCommentRepo{}
	mentions := &stubMentionRepo{}
	events := &stubEventRepo{}
	resolver := &stubResolver{lookup: map[string]uuid.UUID{
		"noc_engineer": noc,
		"field.tech":   field,
		// "ghost_user" intentionally missing — resolver drops it.
	}}
	notifier := &stubNotifier{}

	svc := NewCommentService(tickets, comments, mentions, events, resolver, notifier)

	c, ms, err := svc.AddComment(ctx, port.CreateCommentInput{
		TicketID:   ticket.ID,
		AuthorID:   authorID,
		AuthorRole: "cs_agent",
		Body:       "ping @noc_engineer @field.tech also @ghost_user @noc_engineer (dupe)",
	})
	if err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	if c == nil {
		t.Fatal("nil comment")
	}
	if len(ms) != 2 {
		t.Fatalf("expected 2 mentions, got %d", len(ms))
	}
	if comments.inserted != 1 {
		t.Fatalf("comments inserted = %d want 1", comments.inserted)
	}
	if mentions.inserted != 2 {
		t.Fatalf("mentions inserted = %d want 2", mentions.inserted)
	}
	if notifier.mentionCalls != 2 {
		t.Fatalf("NotifyMention calls = %d want 2", notifier.mentionCalls)
	}
	// first_response_at should have been stamped on the ticket
	if ticket.FirstResponseAt == nil {
		t.Fatal("expected first_response_at stamped after first comment")
	}
}

// TestAddComment_SkipsSelfMention guards against pinging the author.
func TestAddComment_SkipsSelfMention(t *testing.T) {
	ctx := context.Background()

	authorID := uuid.New()
	ticket := mustNewTicket(t, uuid.New(), authorID)
	tickets := &stubTicketRepo{byID: map[uuid.UUID]*domain.Ticket{ticket.ID: ticket}}
	comments := &stubCommentRepo{}
	mentions := &stubMentionRepo{}
	events := &stubEventRepo{}
	resolver := &stubResolver{lookup: map[string]uuid.UUID{
		"self": authorID, // points at the author
	}}
	notifier := &stubNotifier{}

	svc := NewCommentService(tickets, comments, mentions, events, resolver, notifier)
	_, ms, err := svc.AddComment(ctx, port.CreateCommentInput{
		TicketID:   ticket.ID,
		AuthorID:   authorID,
		AuthorRole: "cs_agent",
		Body:       "@self look at this",
	})
	if err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	if len(ms) != 0 {
		t.Fatalf("expected 0 mentions on self-mention, got %d", len(ms))
	}
	if notifier.mentionCalls != 0 {
		t.Fatalf("expected zero NotifyMention calls, got %d", notifier.mentionCalls)
	}
}

// TestAddComment_InternalDoesNotStampFirstResponse ensures internal
// notes don't count as the first agent touch.
func TestAddComment_InternalDoesNotStampFirstResponse(t *testing.T) {
	ctx := context.Background()
	authorID := uuid.New()
	ticket := mustNewTicket(t, uuid.New(), authorID)
	tickets := &stubTicketRepo{byID: map[uuid.UUID]*domain.Ticket{ticket.ID: ticket}}
	svc := NewCommentService(tickets, &stubCommentRepo{}, &stubMentionRepo{}, &stubEventRepo{}, &stubResolver{}, &stubNotifier{})

	if _, _, err := svc.AddComment(ctx, port.CreateCommentInput{
		TicketID: ticket.ID, AuthorID: authorID, AuthorRole: "cs_agent",
		Body: "internal note", IsInternal: true,
	}); err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	if ticket.FirstResponseAt != nil {
		t.Fatal("internal-only comment should not stamp first_response_at")
	}
}

// TestAddComment_RefusesClosedTicket ensures terminal tickets reject
// new comments — Reopen first.
func TestAddComment_RefusesClosedTicket(t *testing.T) {
	ctx := context.Background()
	authorID := uuid.New()
	ticket := mustNewTicket(t, uuid.New(), authorID)
	if err := ticket.Start(authorID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := ticket.Resolve(authorID, "fixed"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if err := ticket.Close(authorID); err != nil {
		t.Fatalf("Close: %v", err)
	}
	tickets := &stubTicketRepo{byID: map[uuid.UUID]*domain.Ticket{ticket.ID: ticket}}
	svc := NewCommentService(tickets, &stubCommentRepo{}, &stubMentionRepo{}, &stubEventRepo{}, &stubResolver{}, &stubNotifier{})

	_, _, err := svc.AddComment(ctx, port.CreateCommentInput{
		TicketID: ticket.ID, AuthorID: authorID, AuthorRole: "cs_agent", Body: "x",
	})
	if err == nil {
		t.Fatal("expected error on closed ticket comment")
	}
}

// TestEditComment_GatesByAuthor verifies the author-or-supervisor gate.
func TestEditComment_GatesByAuthor(t *testing.T) {
	ctx := context.Background()
	author := uuid.New()
	otherAgent := uuid.New()
	c, _ := domain.NewComment(uuid.New(), author, "cs_agent", "hello", false, nil)
	comments := &stubCommentRepo{byID: map[uuid.UUID]*domain.Comment{c.ID: c}}
	svc := NewCommentService(&stubTicketRepo{}, comments, &stubMentionRepo{}, &stubEventRepo{}, &stubResolver{}, &stubNotifier{})

	// Other agent (non-supervisor) — refused
	if _, err := svc.EditComment(ctx, c.ID, "edited", otherAgent, false); err == nil {
		t.Fatal("expected forbidden for non-author non-supervisor")
	}
	// Author — allowed
	if _, err := svc.EditComment(ctx, c.ID, "edited", author, false); err != nil {
		t.Fatalf("author edit: %v", err)
	}
	// Other agent as supervisor — allowed
	if _, err := svc.EditComment(ctx, c.ID, "edited again", otherAgent, true); err != nil {
		t.Fatalf("supervisor edit: %v", err)
	}
}

// =====================================================================
// helpers / stubs
// =====================================================================

func mustNewTicket(t *testing.T, customer, opener uuid.UUID) *domain.Ticket {
	t.Helper()
	tk, err := domain.NewTicket(customer, opener, domain.OpenedViaPortal, domain.TicketTypeTechnical, "title", "desc", domain.PriorityNormal)
	if err != nil {
		t.Fatalf("NewTicket: %v", err)
	}
	tk.TicketNo = "TKT-2026-00000001"
	return tk
}

// stubTicketRepo is an in-memory port.TicketRepository.
type stubTicketRepo struct {
	mu   sync.Mutex
	byID map[uuid.UUID]*domain.Ticket
}

func (r *stubTicketRepo) ensure() {
	if r.byID == nil {
		r.byID = map[uuid.UUID]*domain.Ticket{}
	}
}
func (r *stubTicketRepo) Create(_ context.Context, t *domain.Ticket) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensure()
	r.byID[t.ID] = t
	return nil
}
func (r *stubTicketRepo) FindByID(_ context.Context, id uuid.UUID) (*domain.Ticket, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensure()
	tk, ok := r.byID[id]
	if !ok {
		return nil, errNotFound
	}
	return tk, nil
}
func (r *stubTicketRepo) List(_ context.Context, _ port.TicketListFilter) ([]domain.Ticket, int, error) {
	return nil, 0, nil
}
func (r *stubTicketRepo) Update(_ context.Context, t *domain.Ticket) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ensure()
	r.byID[t.ID] = t
	return nil
}
func (r *stubTicketRepo) NextTicketNo(_ context.Context, year int) (string, error) {
	return "TKT-2026-00000001", nil
}

// stubCommentRepo
type stubCommentRepo struct {
	mu       sync.Mutex
	inserted int
	byID     map[uuid.UUID]*domain.Comment
}

func (r *stubCommentRepo) Insert(_ context.Context, c *domain.Comment) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byID == nil {
		r.byID = map[uuid.UUID]*domain.Comment{}
	}
	r.byID[c.ID] = c
	r.inserted++
	return nil
}
func (r *stubCommentRepo) Update(_ context.Context, c *domain.Comment) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byID[c.ID] = c
	return nil
}
func (r *stubCommentRepo) FindByID(_ context.Context, id uuid.UUID) (*domain.Comment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.byID[id]
	if !ok {
		return nil, errNotFound
	}
	return c, nil
}
func (r *stubCommentRepo) List(_ context.Context, _ port.CommentListFilter) ([]domain.Comment, error) {
	return nil, nil
}

// stubMentionRepo
type stubMentionRepo struct {
	mu       sync.Mutex
	inserted int
}

func (r *stubMentionRepo) Insert(_ context.Context, _ *domain.Mention) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.inserted++
	return nil
}
func (r *stubMentionRepo) List(_ context.Context, _ port.MentionListFilter) ([]domain.Mention, error) {
	return nil, nil
}
func (r *stubMentionRepo) FindByID(_ context.Context, _ uuid.UUID) (*domain.Mention, error) {
	return nil, errNotFound
}
func (r *stubMentionRepo) MarkRead(_ context.Context, _ uuid.UUID, _ time.Time) error { return nil }
func (r *stubMentionRepo) ListUnreadOlderThan(_ context.Context, _ time.Time, _ int) ([]domain.Mention, error) {
	return nil, nil
}

// stubEventRepo
type stubEventRepo struct{ inserted int }

func (r *stubEventRepo) Insert(_ context.Context, _ *domain.TicketEvent) error {
	r.inserted++
	return nil
}
func (r *stubEventRepo) List(_ context.Context, _ uuid.UUID, _, _ int) ([]domain.TicketEvent, error) {
	return nil, nil
}

// stubResolver
type stubResolver struct{ lookup map[string]uuid.UUID }

func (r *stubResolver) Resolve(_ context.Context, names []string) (map[string]uuid.UUID, error) {
	out := map[string]uuid.UUID{}
	for _, n := range names {
		if id, ok := r.lookup[strings.ToLower(n)]; ok {
			out[n] = id
		}
	}
	return out, nil
}

// stubNotifier
type stubNotifier struct {
	mu              sync.Mutex
	mentionCalls    int
	assignmentCalls int
}

func (n *stubNotifier) NotifyMention(_ context.Context, _ *domain.Mention, _, _ string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.mentionCalls++
}
func (n *stubNotifier) NotifyAssignment(_ context.Context, _, _ uuid.UUID, _ string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.assignmentCalls++
}

// errNotFound is a sentinel for the test stubs.
var errNotFound = stubErr("not found")

type stubErr string

func (e stubErr) Error() string { return string(e) }
