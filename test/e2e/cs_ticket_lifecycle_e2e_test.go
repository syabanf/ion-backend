// Wave 127 — CS ticket lifecycle E2E.
//
// Exercises Wave 123's cs.* schema end-to-end against live Postgres:
//
//   - Create → Assign → Start → Pause(PendingCustomer) → Resume → Resolve → Close → Reopen
//   - Comment with @mention → cs.ticket_mentions row + notifyx-style fan-out
//   - Mention parser ignores `name@email.com` (must not produce a mention row)
//   - Channel CRUD round-trip
//
// All tests t.Skip cleanly when DATABASE_URL is unset or cs.tickets is
// missing (migration 0082 not applied to the target smoke DB).
//
// The CS service wires up against the actual postgres adapter — no
// HTTP — so we exercise the usecase + repo layer the same way
// cmd/cs-svc/main.go does at boot. Notifyx fan-out is observed by a
// minimal stub bridge that records the calls; tests assert against that.
//
//go:build e2e

package e2e

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	cspg "github.com/ion-core/backend/internal/cs/adapter/postgres"
	csdom "github.com/ion-core/backend/internal/cs/domain"
	csport "github.com/ion-core/backend/internal/cs/port"
	csuc "github.com/ion-core/backend/internal/cs/usecase"
)

// csHarness wires the Wave 123 ticket + comment + mention + channel
// services against the live ion_p1c_smoke postgres. Identical shape to
// the nocHarness pattern from Wave 121C.
type csHarness struct {
	tickets  *csuc.TicketService
	comments *csuc.CommentService
	mentions *csuc.MentionService
	channels *csuc.ChannelService

	ticketRepo  *cspg.TicketRepository
	eventRepo   *cspg.TicketEventRepository
	commentRepo *cspg.TicketCommentRepository
	mentionRepo *cspg.TicketMentionRepository
	channelRepo *cspg.TicketChannelRepository

	notifier *recordingNotifier
	resolver *staticResolver
}

// recordingNotifier captures NotifyMention / NotifyAssignment calls so
// tests can assert on them without piping through notifyx + a pgx
// outbox.
type recordingNotifier struct {
	Mentions    []uuid.UUID
	Assignments []uuid.UUID
}

func (r *recordingNotifier) NotifyMention(_ context.Context, m *csdom.Mention, _ string, _ string) {
	if m == nil {
		return
	}
	r.Mentions = append(r.Mentions, m.MentionedUserID)
}
func (r *recordingNotifier) NotifyAssignment(_ context.Context, _, assigned uuid.UUID, _ string) {
	r.Assignments = append(r.Assignments, assigned)
}

// staticResolver returns a fixed username → uuid map. Lets us drive
// the mention parser without touching identity.users.
type staticResolver struct {
	M map[string]uuid.UUID
}

func (s *staticResolver) Resolve(_ context.Context, names []string) (map[string]uuid.UUID, error) {
	out := map[string]uuid.UUID{}
	for _, n := range names {
		k := strings.ToLower(strings.TrimSpace(n))
		if id, ok := s.M[k]; ok {
			out[k] = id
		}
	}
	return out, nil
}

func newCSHarness(t *testing.T) *csHarness {
	t.Helper()
	pool := w121cDB(t)
	w121cSkipIfMissingTable(t, pool, "cs.tickets")
	w121cSkipIfMissingTable(t, pool, "cs.ticket_events")
	w121cSkipIfMissingTable(t, pool, "cs.ticket_comments")
	w121cSkipIfMissingTable(t, pool, "cs.ticket_mentions")
	w121cSkipIfMissingTable(t, pool, "cs.ticket_channels")

	ticketRepo := cspg.NewTicketRepository(pool)
	eventRepo := cspg.NewTicketEventRepository(pool)
	commentRepo := cspg.NewTicketCommentRepository(pool)
	mentionRepo := cspg.NewTicketMentionRepository(pool)
	channelRepo := cspg.NewTicketChannelRepository(pool)

	notifier := &recordingNotifier{}
	resolver := &staticResolver{M: map[string]uuid.UUID{}}

	return &csHarness{
		tickets:     csuc.NewTicketService(ticketRepo, eventRepo, notifier),
		comments:    csuc.NewCommentService(ticketRepo, commentRepo, mentionRepo, eventRepo, resolver, notifier),
		mentions:    csuc.NewMentionService(mentionRepo, notifier),
		channels:    csuc.NewChannelService(channelRepo),
		ticketRepo:  ticketRepo,
		eventRepo:   eventRepo,
		commentRepo: commentRepo,
		mentionRepo: mentionRepo,
		channelRepo: channelRepo,
		notifier:    notifier,
		resolver:    resolver,
	}
}

// TC-TL-001..010 — TicketLifecycle SM full walk.
func TestCS_TicketLifecycle_FullWalk(t *testing.T) {
	h := newCSHarness(t)
	pool := w121cDB(t)
	ctx := context.Background()

	customerID := uuid.New()
	openedBy := uuid.New()
	assignee := uuid.New()

	tk, err := h.tickets.CreateTicket(ctx, csport.CreateTicketInput{
		CustomerID:  customerID,
		OpenedBy:    openedBy,
		OpenedVia:   csdom.OpenedViaPortal,
		TicketType:  csdom.TicketTypeTechnical,
		Title:       "W127 lifecycle " + uuid.New().String()[:8],
		Description: "Wave 127 e2e — full SM walk",
		Priority:    csdom.PriorityNormal,
	})
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	t.Cleanup(w121cCleanup(pool, "cs.tickets", "id", tk.ID.String()))

	if tk.Status != csdom.TicketStatusOpen {
		t.Fatalf("initial status: got %q want open", tk.Status)
	}

	if _, err := h.tickets.AssignTicket(ctx, tk.ID, assignee, openedBy, "supervisor"); err != nil {
		t.Fatalf("Assign: %v", err)
	}
	if _, err := h.tickets.StartTicket(ctx, tk.ID, assignee, "agent"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := h.tickets.PauseTicket(ctx, tk.ID, csdom.PauseWaitCustomer, "awaiting reply", assignee, "agent"); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if _, err := h.tickets.ResumeTicket(ctx, tk.ID, assignee, "agent"); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if _, err := h.tickets.ResolveTicket(ctx, tk.ID, "Wave 127 — root cause fixed", assignee, "agent"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if _, err := h.tickets.CloseTicket(ctx, tk.ID, assignee, "agent"); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := h.tickets.ReopenTicket(ctx, tk.ID, openedBy, "customer reported recurrence", "supervisor"); err != nil {
		t.Fatalf("Reopen: %v", err)
	}

	got, err := h.tickets.GetTicket(ctx, tk.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != csdom.TicketStatusInProgress {
		t.Errorf("after reopen: got %q want in_progress", got.Status)
	}
	if got.EscalationLevel != 1 {
		t.Errorf("after reopen: escalation_level=%d want 1", got.EscalationLevel)
	}
	if got.FirstResponseAt == nil {
		t.Errorf("first_response_at should be stamped after Start")
	}
	if h.notifier.Assignments == nil || h.notifier.Assignments[0] != assignee {
		t.Errorf("Assignment notifier never fired for assignee")
	}
}

// TC-TL-008 — reopen counter increments per Reopen.
func TestCS_TicketLifecycle_ReopenCounter(t *testing.T) {
	h := newCSHarness(t)
	pool := w121cDB(t)
	ctx := context.Background()

	customerID := uuid.New()
	openedBy := uuid.New()
	tk, err := h.tickets.CreateTicket(ctx, csport.CreateTicketInput{
		CustomerID: customerID, OpenedBy: openedBy,
		OpenedVia: csdom.OpenedViaPortal, TicketType: csdom.TicketTypeTechnical,
		Title: "W127 reopen counter", Priority: csdom.PriorityNormal,
	})
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	t.Cleanup(w121cCleanup(pool, "cs.tickets", "id", tk.ID.String()))

	if _, err := h.tickets.StartTicket(ctx, tk.ID, openedBy, "agent"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := h.tickets.ResolveTicket(ctx, tk.ID, "first fix", openedBy, "agent"); err != nil {
		t.Fatalf("Resolve#1: %v", err)
	}
	if _, err := h.tickets.ReopenTicket(ctx, tk.ID, openedBy, "recurred 1", "agent"); err != nil {
		t.Fatalf("Reopen#1: %v", err)
	}
	if _, err := h.tickets.ResolveTicket(ctx, tk.ID, "second fix", openedBy, "agent"); err != nil {
		t.Fatalf("Resolve#2: %v", err)
	}
	if _, err := h.tickets.ReopenTicket(ctx, tk.ID, openedBy, "recurred 2", "agent"); err != nil {
		t.Fatalf("Reopen#2: %v", err)
	}
	got, _ := h.tickets.GetTicket(ctx, tk.ID)
	if got.EscalationLevel != 2 {
		t.Errorf("escalation_level after 2 reopens: got %d want 2", got.EscalationLevel)
	}
}

// TC-MEN-001 — comment with @mention persists a cs.ticket_mentions row
// AND dispatches a notification (recorded via recordingNotifier).
func TestCS_Mention_ParseAndPersist(t *testing.T) {
	h := newCSHarness(t)
	pool := w121cDB(t)
	ctx := context.Background()

	noc := uuid.New()
	h.resolver.M["noc_engineer"] = noc

	customerID := uuid.New()
	author := uuid.New()
	tk, err := h.tickets.CreateTicket(ctx, csport.CreateTicketInput{
		CustomerID: customerID, OpenedBy: author,
		OpenedVia: csdom.OpenedViaPortal, TicketType: csdom.TicketTypeTechnical,
		Title: "W127 mention parse", Priority: csdom.PriorityNormal,
	})
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	t.Cleanup(w121cCleanup(pool, "cs.tickets", "id", tk.ID.String()))

	c, mentions, err := h.comments.AddComment(ctx, csport.CreateCommentInput{
		TicketID:   tk.ID,
		AuthorID:   author,
		AuthorRole: "agent",
		Body:       "Hey @noc_engineer please confirm uplink at ODP",
	})
	if err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	if c == nil || c.ID == uuid.Nil {
		t.Fatal("AddComment returned nil comment")
	}
	if len(mentions) != 1 {
		t.Fatalf("len(mentions)=%d want 1", len(mentions))
	}
	if mentions[0].MentionedUserID != noc {
		t.Errorf("mention user_id: got %s want %s", mentions[0].MentionedUserID, noc)
	}
	if len(h.notifier.Mentions) != 1 || h.notifier.Mentions[0] != noc {
		t.Errorf("notifier.Mentions: got %v", h.notifier.Mentions)
	}
}

// TC-MEN-002 — bare email address `name@email.com` MUST NOT produce a
// mention row. The parser strips email-like tokens.
func TestCS_Mention_EmailExclusion(t *testing.T) {
	h := newCSHarness(t)
	pool := w121cDB(t)
	ctx := context.Background()

	// Even if a user has email-localpart "support", a literal
	// "support@email.com" in the body must not trigger a mention.
	h.resolver.M["support"] = uuid.New()

	customerID := uuid.New()
	author := uuid.New()
	tk, err := h.tickets.CreateTicket(ctx, csport.CreateTicketInput{
		CustomerID: customerID, OpenedBy: author,
		OpenedVia: csdom.OpenedViaPortal, TicketType: csdom.TicketTypeBilling,
		Title: "W127 email-not-a-mention", Priority: csdom.PriorityNormal,
	})
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	t.Cleanup(w121cCleanup(pool, "cs.tickets", "id", tk.ID.String()))

	_, mentions, err := h.comments.AddComment(ctx, csport.CreateCommentInput{
		TicketID: tk.ID, AuthorID: author, AuthorRole: "agent",
		Body: "Forwarded the invoice to support@email.com for review",
	})
	if err != nil {
		t.Fatalf("AddComment: %v", err)
	}
	if len(mentions) != 0 {
		t.Errorf("email-only body produced %d mentions, want 0", len(mentions))
	}
	if len(h.notifier.Mentions) != 0 {
		t.Errorf("email-only body fired notifier %d times, want 0", len(h.notifier.Mentions))
	}
}

// TC-TCH-001..006 — Channel CRUD round-trip.
func TestCS_Channels_CRUD(t *testing.T) {
	h := newCSHarness(t)
	pool := w121cDB(t)
	ctx := context.Background()

	code := "W127-CH-" + uuid.New().String()[:8]
	ch, err := h.channels.CreateChannel(ctx, code, "Wave 127 test channel", csdom.ChannelKindBoth, true, map[string]any{
		"description": "wave127 e2e",
	})
	if err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	if ch.Code != code {
		t.Errorf("code roundtrip: got %q want %q", ch.Code, code)
	}
	t.Cleanup(w121cCleanup(pool, "cs.ticket_channels", "code", code))

	all, err := h.channels.ListChannels(ctx, false)
	if err != nil {
		t.Fatalf("ListChannels: %v", err)
	}
	found := false
	for _, c := range all {
		if c.Code == code {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("channel %s missing from ListChannels result", code)
	}

	newName := "Wave 127 renamed"
	active := false
	if _, err := h.channels.UpdateChannel(ctx, code, &newName, nil, &active, nil); err != nil {
		t.Fatalf("UpdateChannel: %v", err)
	}
	// Verify deactivation took.
	activeOnly, _ := h.channels.ListChannels(ctx, true)
	for _, c := range activeOnly {
		if c.Code == code {
			t.Errorf("channel %s still active-listed after deactivation", code)
		}
	}
}

// TC-TT-001 — Ticket type immutable post-create. The state machine
// doesn't expose a TicketType setter; this test asserts that.
func TestCS_TicketType_ImmutablePostCreate(t *testing.T) {
	h := newCSHarness(t)
	pool := w121cDB(t)
	ctx := context.Background()

	tk, err := h.tickets.CreateTicket(ctx, csport.CreateTicketInput{
		CustomerID: uuid.New(), OpenedBy: uuid.New(),
		OpenedVia: csdom.OpenedViaPortal, TicketType: csdom.TicketTypeBilling,
		Title: "W127 type immut", Priority: csdom.PriorityNormal,
	})
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	t.Cleanup(w121cCleanup(pool, "cs.tickets", "id", tk.ID.String()))

	// The domain.Ticket struct doesn't expose ChangeTicketType — only
	// ChangePriority. Verify the recorded ticket_type stays put through
	// a series of mutating operations.
	if _, err := h.tickets.ChangePriority(ctx, tk.ID, csdom.PriorityHigh, uuid.New(), "supervisor"); err != nil {
		t.Fatalf("ChangePriority: %v", err)
	}
	got, _ := h.tickets.GetTicket(ctx, tk.ID)
	if got.TicketType != csdom.TicketTypeBilling {
		t.Errorf("ticket_type drifted to %q after priority change", got.TicketType)
	}
}

// TC-TL-004 — pause accumulates pause_seconds via Resume. The domain
// uses wall-clock subtraction; we sleep ~1.5s to ensure the resume
// accumulator is at least 1.
func TestCS_PauseAccumulates(t *testing.T) {
	h := newCSHarness(t)
	pool := w121cDB(t)
	ctx := context.Background()

	tk, err := h.tickets.CreateTicket(ctx, csport.CreateTicketInput{
		CustomerID: uuid.New(), OpenedBy: uuid.New(),
		OpenedVia: csdom.OpenedViaPortal, TicketType: csdom.TicketTypeTechnical,
		Title: "W127 pause accum", Priority: csdom.PriorityNormal,
	})
	if err != nil {
		t.Fatalf("CreateTicket: %v", err)
	}
	t.Cleanup(w121cCleanup(pool, "cs.tickets", "id", tk.ID.String()))

	user := uuid.New()
	if _, err := h.tickets.StartTicket(ctx, tk.ID, user, "agent"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := h.tickets.PauseTicket(ctx, tk.ID, csdom.PauseWaitCustomer, "waiting", user, "agent"); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	time.Sleep(1500 * time.Millisecond)
	if _, err := h.tickets.ResumeTicket(ctx, tk.ID, user, "agent"); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	got, _ := h.tickets.GetTicket(ctx, tk.ID)
	if got.PauseSeconds < 1 {
		t.Errorf("pause_seconds after ~1.5s pause: got %d want >=1", got.PauseSeconds)
	}
}
