// Wave 120 — H2H matching ambiguity edge.
//
// Pins TC-PH2-* "when multiple payment intents would match the same
// statement line within a ±2 day window with identical amount + no
// distinguishing reference, the matcher must NOT silently bind to a
// random intent — the finance dashboard needs the line flagged as
// ambiguous so the operator can review manually".
//
// Current state: MatchByReference picks the first hit per line at the
// highest confidence tier; if two intents both pass `amount + ±48h
// window` (the 0.50 'amount_date_window' tier) the matcher binds to
// whichever the caller iterated first. Wave 128B closes the gap by
// counting same-tier ties in the usecase-level MatchStatement loop and
// flagging colliding lines with match_method='ambiguous' instead of
// auto-binding.

package usecase

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/payment/domain"
	"github.com/ion-core/backend/internal/payment/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

func TestH2H_AmbiguousMatch_DomainMatcherPinsTier(t *testing.T) {
	// Two intents — same amount, same value-date window. Neither has a
	// reference, so both fall to the 0.50 amount_date_window tier.
	now := time.Date(2026, 5, 15, 9, 0, 0, 0, time.UTC)
	intentAmount := 250000.00
	intentPaid := now.Add(-12 * time.Hour)

	// Line ref empty — neither intent reference matches by string.
	confA, methodA := domain.MatchByReference(
		"",            // lineRef
		intentAmount,  // lineAmount
		now,           // lineValueDate
		"INTENT-A123", // intentRefShort
		intentAmount,
		&intentPaid,
	)
	confB, methodB := domain.MatchByReference(
		"",
		intentAmount,
		now,
		"INTENT-B456",
		intentAmount,
		&intentPaid,
	)
	if confA != 0.50 || methodA != "amount_date_window" {
		t.Fatalf("intent A: want (0.50, amount_date_window), got (%.2f, %s)", confA, methodA)
	}
	if confB != 0.50 || methodB != "amount_date_window" {
		t.Fatalf("intent B: want (0.50, amount_date_window), got (%.2f, %s)", confB, methodB)
	}
	// Both at 0.50 → operator review needed. The matcher's caller (the
	// MatchStatement loop in usecase/h2h.go) is responsible for binding;
	// today it picks the first one. The TestH2H_DistinguishingReference
	// case below proves that when a reference distinguishes the two,
	// the collision goes away.
}

func TestH2H_DistinguishingReference_EscalatesConfidence(t *testing.T) {
	now := time.Date(2026, 5, 15, 9, 0, 0, 0, time.UTC)
	intentPaid := now.Add(-12 * time.Hour)
	amount := 250000.00

	// Line ref contains intent B's short id — intent A stays at 0.50
	// (amount_date_window) while intent B jumps to 0.85
	// (reference_substring_amount). The matcher picks B unambiguously.
	confA, methodA := domain.MatchByReference(
		"transfer for INTENT-B456 broadband",
		amount,
		now,
		"INTENT-A123",
		amount,
		&intentPaid,
	)
	confB, methodB := domain.MatchByReference(
		"transfer for INTENT-B456 broadband",
		amount,
		now,
		"INTENT-B456",
		amount,
		&intentPaid,
	)
	if confA != 0.50 {
		t.Errorf("intent A: want 0.50 amount_date_window, got (%.2f, %s)", confA, methodA)
	}
	if confB < 0.85 {
		t.Errorf("intent B: want >= 0.85 substring/exact, got (%.2f, %s)", confB, methodB)
	}
	// Difference is large enough that a sane matcher binds to B without
	// review.
	if confB-confA < 0.30 {
		t.Errorf("expected a > 0.30 confidence gap to disambiguate, got A=%.2f B=%.2f", confA, confB)
	}
}

// TestH2H_AmbiguousMatch_FlaggedAsAmbiguous_Future pins TC-PH2-*: a
// line that ties with 2+ candidates at the SAME best confidence tier
// must NOT auto-bind. Closed in Wave 128B: MatchStatement counts ties
// and writes match_method='ambiguous' (with payment_intent_id=NULL)
// for lines that need operator review.
func TestH2H_AmbiguousMatch_FlaggedAsAmbiguous_Future(t *testing.T) {
	ctx := context.Background()

	// Build a statement with one line and two pending intents that BOTH
	// match the line at the 0.50 amount_date_window tier (same amount,
	// same window, neither has a distinguishing reference).
	now := time.Date(2026, 5, 15, 9, 0, 0, 0, time.UTC)
	amount := 250000.00
	lineDate := now

	stmt := &domain.H2HBankStatement{
		ID:        uuid.New(),
		GatewayID: uuid.New(),
		Status:    domain.H2HStatementStatusParsed,
		LineCount: 1,
		CreatedAt: now,
	}
	line := domain.H2HBankLine{
		ID:            uuid.New(),
		StatementID:   stmt.ID,
		Amount:        &amount,
		ValueDate:     &lineDate,
		ReferenceText: "", // no reference → both intents fall to 0.50 tier
		CreatedAt:     now,
	}
	h2hRepo := newFakeH2HRepo()
	h2hRepo.statements[stmt.ID] = stmt
	h2hRepo.unmatched[stmt.ID] = []domain.H2HBankLine{line}

	intentsRepo := newFakeIntentList()
	intentPaid := now.Add(-12 * time.Hour)
	intentA := domain.PaymentIntent{
		ID: uuid.New(), Status: domain.PaymentStatusPending,
		Amount: amount, PaidAt: &intentPaid, CreatedAt: now,
	}
	intentB := domain.PaymentIntent{
		ID: uuid.New(), Status: domain.PaymentStatusPending,
		Amount: amount, PaidAt: &intentPaid, CreatedAt: now,
	}
	intentsRepo.rows = []domain.PaymentIntent{intentA, intentB}

	svc := NewH2HService(h2hRepo, intentsRepo, nil, nil, nil)

	out, err := svc.MatchStatement(ctx, stmt.ID)
	if err != nil {
		t.Fatalf("MatchStatement: %v", err)
	}
	if out.Status != domain.H2HStatementStatusPartial {
		t.Errorf("statement status = %s, want partial (ambiguous line counts as unmatched)", out.Status)
	}
	if out.MatchedCount != 0 {
		t.Errorf("MatchedCount = %d, want 0 (the tie must NOT auto-bind)", out.MatchedCount)
	}
	if out.UnmatchedCount != 1 {
		t.Errorf("UnmatchedCount = %d, want 1", out.UnmatchedCount)
	}

	// Verify the line was flagged ambiguous + no intent stamped.
	updated := h2hRepo.lastLineUpdate
	if updated == nil {
		t.Fatalf("expected UpdateLineMatch to fire with the ambiguous flag")
	}
	if updated.MatchMethod != "ambiguous" {
		t.Errorf("line match_method = %q, want 'ambiguous'", updated.MatchMethod)
	}
	if updated.PaymentIntentID != nil {
		t.Errorf("line payment_intent_id = %v, want NULL (no auto-bind on tie)", *updated.PaymentIntentID)
	}
	if updated.MatchConfidence == nil || *updated.MatchConfidence != 0.50 {
		t.Errorf("line match_confidence = %v, want 0.50 (the tied tier)", updated.MatchConfidence)
	}
}

// TestH2H_UnambiguousMatch_Binds asserts the complementary path: when
// a single candidate wins outright (no tie), MatchStatement DOES
// auto-bind. Belt-and-braces against a regression where the new
// ambiguity guard accidentally rejects clean matches.
func TestH2H_UnambiguousMatch_Binds(t *testing.T) {
	ctx := context.Background()

	now := time.Date(2026, 5, 15, 9, 0, 0, 0, time.UTC)
	amount := 250000.00
	lineDate := now

	stmt := &domain.H2HBankStatement{
		ID:        uuid.New(),
		GatewayID: uuid.New(),
		Status:    domain.H2HStatementStatusParsed,
		LineCount: 1,
		CreatedAt: now,
	}
	intentPaid := now.Add(-12 * time.Hour)
	winner := domain.PaymentIntent{
		ID: uuid.New(), Status: domain.PaymentStatusPending,
		Amount: amount, PaidAt: &intentPaid, CreatedAt: now,
	}
	// Line reference contains winner's short id — winner gets 0.85
	// (reference_substring_amount), the second intent stays at 0.50
	// (amount_date_window). No tie at the BEST tier.
	short := winner.ID.String()[:12]
	loser := domain.PaymentIntent{
		ID: uuid.New(), Status: domain.PaymentStatusPending,
		Amount: amount, PaidAt: &intentPaid, CreatedAt: now,
	}
	line := domain.H2HBankLine{
		ID:            uuid.New(),
		StatementID:   stmt.ID,
		Amount:        &amount,
		ValueDate:     &lineDate,
		ReferenceText: "wire ref " + short + " thx",
		CreatedAt:     now,
	}
	h2hRepo := newFakeH2HRepo()
	h2hRepo.statements[stmt.ID] = stmt
	h2hRepo.unmatched[stmt.ID] = []domain.H2HBankLine{line}

	intentsRepo := newFakeIntentList()
	intentsRepo.rows = []domain.PaymentIntent{winner, loser}

	svc := NewH2HService(h2hRepo, intentsRepo, nil, nil, nil)
	out, err := svc.MatchStatement(ctx, stmt.ID)
	if err != nil {
		t.Fatalf("MatchStatement: %v", err)
	}
	if out.Status != domain.H2HStatementStatusMatched {
		t.Errorf("status = %s, want matched (unambiguous winner)", out.Status)
	}
	if out.MatchedCount != 1 {
		t.Errorf("MatchedCount = %d, want 1", out.MatchedCount)
	}
	updated := h2hRepo.lastLineUpdate
	if updated == nil || updated.PaymentIntentID == nil {
		t.Fatalf("expected the winner to be auto-bound")
	}
	if *updated.PaymentIntentID != winner.ID {
		t.Errorf("bound intent = %s, want winner %s", *updated.PaymentIntentID, winner.ID)
	}
	if updated.MatchMethod == "ambiguous" {
		t.Errorf("unambiguous line must NOT be flagged ambiguous")
	}
}

// =====================================================================
// In-memory fakes for H2H + intent list, scoped to this test file.
// =====================================================================

type fakeH2HRepo struct {
	mu             sync.Mutex
	statements     map[uuid.UUID]*domain.H2HBankStatement
	unmatched      map[uuid.UUID][]domain.H2HBankLine
	lastLineUpdate *domain.H2HBankLine
}

func newFakeH2HRepo() *fakeH2HRepo {
	return &fakeH2HRepo{
		statements: map[uuid.UUID]*domain.H2HBankStatement{},
		unmatched:  map[uuid.UUID][]domain.H2HBankLine{},
	}
}

func (r *fakeH2HRepo) CreateStatement(_ context.Context, s *domain.H2HBankStatement) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *s
	r.statements[s.ID] = &cp
	return nil
}
func (r *fakeH2HRepo) FindStatementByID(_ context.Context, id uuid.UUID) (*domain.H2HBankStatement, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.statements[id]
	if !ok {
		return nil, derrors.NotFound("h2h.statement_not_found", "not found")
	}
	cp := *s
	return &cp, nil
}
func (r *fakeH2HRepo) FindStatementByHash(_ context.Context, _ uuid.UUID, _ string) (*domain.H2HBankStatement, error) {
	return nil, nil
}
func (r *fakeH2HRepo) ListStatements(_ context.Context, _, _ int) ([]domain.H2HBankStatement, int, error) {
	return nil, 0, nil
}
func (r *fakeH2HRepo) UpdateStatement(_ context.Context, s *domain.H2HBankStatement) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *s
	r.statements[s.ID] = &cp
	return nil
}
func (r *fakeH2HRepo) InsertLines(_ context.Context, _ uuid.UUID, _ []domain.H2HBankLine) error {
	return nil
}
func (r *fakeH2HRepo) ListLinesForStatement(_ context.Context, _ uuid.UUID) ([]domain.H2HBankLine, error) {
	return nil, nil
}
func (r *fakeH2HRepo) UpdateLineMatch(_ context.Context, line *domain.H2HBankLine) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *line
	r.lastLineUpdate = &cp
	return nil
}
func (r *fakeH2HRepo) ListUnmatchedLines(_ context.Context, statementID uuid.UUID) ([]domain.H2HBankLine, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]domain.H2HBankLine, len(r.unmatched[statementID]))
	copy(out, r.unmatched[statementID])
	return out, nil
}

// fakeIntentList is a minimal PaymentIntentRepository that returns a
// fixed slice from List and stubs the rest. Only List is exercised by
// the H2H matcher loop.
type fakeIntentList struct {
	rows []domain.PaymentIntent
}

func newFakeIntentList() *fakeIntentList { return &fakeIntentList{} }

func (f *fakeIntentList) CreateOrFetchByIdempotency(_ context.Context, intent *domain.PaymentIntent) (bool, *domain.PaymentIntent, error) {
	return true, intent, nil
}
func (f *fakeIntentList) FindByID(_ context.Context, id uuid.UUID) (*domain.PaymentIntent, error) {
	for i := range f.rows {
		if f.rows[i].ID == id {
			r := f.rows[i]
			return &r, nil
		}
	}
	return nil, derrors.NotFound("payment_intent.not_found", "not found")
}
func (f *fakeIntentList) FindByExternalRef(_ context.Context, _ string) (*domain.PaymentIntent, error) {
	return nil, derrors.NotFound("payment_intent.not_found", "not found")
}
func (f *fakeIntentList) List(_ context.Context, filt port.IntentListFilter) ([]domain.PaymentIntent, int, error) {
	out := []domain.PaymentIntent{}
	for _, r := range f.rows {
		if filt.Status != "" && string(r.Status) != filt.Status {
			continue
		}
		out = append(out, r)
	}
	// Honour offset/limit so the pagination loop in MatchStatement
	// terminates.
	start := filt.Offset
	if start > len(out) {
		start = len(out)
	}
	end := len(out)
	if filt.Limit > 0 && start+filt.Limit < end {
		end = start + filt.Limit
	}
	return out[start:end], len(out), nil
}
func (f *fakeIntentList) Update(_ context.Context, _ *domain.PaymentIntent) error { return nil }
func (f *fakeIntentList) ListPendingOlderThan(_ context.Context, _ time.Time, _ int) ([]domain.PaymentIntent, error) {
	return nil, nil
}
