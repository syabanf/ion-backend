package usecase

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/enterprise/domain"
	"github.com/ion-core/backend/internal/enterprise/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// Wave 106 — usecase-level tests for the polish bundle
//
// We exercise:
//   - RunQuotationExpirySweep flips issued-but-past-valid_until rows
//   - MarkOpportunityAutoLost (single-row path used by the cron watcher)
//   - PreBOQ structured-validator integration via CompletePreBOQ
//   - ReassignOpportunity audit + notification fan-out
//
// All tests use in-memory fakes so they run without DATABASE_URL.
// =====================================================================

// ----- fakes ---------------------------------------------------------

type stubOppRepoWave106 struct {
	rows map[uuid.UUID]*domain.Opportunity
}

var _ port.OpportunityRepository = (*stubOppRepoWave106)(nil)

func newStubOppRepo() *stubOppRepoWave106 {
	return &stubOppRepoWave106{rows: map[uuid.UUID]*domain.Opportunity{}}
}

func (s *stubOppRepoWave106) List(_ context.Context, _ port.OpportunityListFilter) ([]domain.Opportunity, int, error) {
	return nil, 0, nil
}
func (s *stubOppRepoWave106) FindByID(_ context.Context, id uuid.UUID) (*domain.Opportunity, error) {
	o, ok := s.rows[id]
	if !ok {
		return nil, derrors.NotFound("opportunity.not_found", "missing")
	}
	clone := *o
	return &clone, nil
}
func (s *stubOppRepoWave106) Create(_ context.Context, o *domain.Opportunity) error {
	s.rows[o.ID] = o
	return nil
}
func (s *stubOppRepoWave106) Update(_ context.Context, o *domain.Opportunity, _ *int) error {
	s.rows[o.ID] = o
	return nil
}
func (s *stubOppRepoWave106) FindExpiredAutoLostCandidates(_ context.Context) ([]domain.Opportunity, error) {
	out := []domain.Opportunity{}
	for _, o := range s.rows {
		if o.IsAutoLostExpired(time.Now()) {
			out = append(out, *o)
		}
	}
	return out, nil
}

type stubQuotationRepoWave106 struct {
	rows []domain.Quotation
}

var _ port.QuotationRepository = (*stubQuotationRepoWave106)(nil)

func (s *stubQuotationRepoWave106) List(_ context.Context, f port.QuotationListFilter) ([]domain.Quotation, int, error) {
	out := []domain.Quotation{}
	for _, q := range s.rows {
		if f.Status != "" && string(q.Status) != f.Status {
			continue
		}
		out = append(out, q)
	}
	// Honor pagination loosely so the sweep terminates.
	if f.Offset >= len(out) {
		return nil, len(out), nil
	}
	end := f.Offset + f.Limit
	if end > len(out) {
		end = len(out)
	}
	return out[f.Offset:end], len(out), nil
}
func (s *stubQuotationRepoWave106) FindByID(_ context.Context, id uuid.UUID) (*domain.Quotation, error) {
	for i := range s.rows {
		if s.rows[i].ID == id {
			c := s.rows[i]
			return &c, nil
		}
	}
	return nil, derrors.NotFound("quotation.not_found", "missing")
}
func (s *stubQuotationRepoWave106) FindPDFBytes(_ context.Context, _ uuid.UUID) ([]byte, string, error) {
	return nil, "", nil
}
func (s *stubQuotationRepoWave106) FindHighestVersion(_ context.Context, _ string) (*domain.Quotation, error) {
	return nil, derrors.NotFound("quotation.not_found", "missing")
}
func (s *stubQuotationRepoWave106) FindLatestForBOQ(_ context.Context, _ uuid.UUID) (*domain.Quotation, error) {
	return nil, derrors.NotFound("quotation.not_found", "missing")
}
func (s *stubQuotationRepoWave106) Create(_ context.Context, q *domain.Quotation) error {
	s.rows = append(s.rows, *q)
	return nil
}
func (s *stubQuotationRepoWave106) Update(_ context.Context, q *domain.Quotation, _ *int) error {
	for i := range s.rows {
		if s.rows[i].ID == q.ID {
			s.rows[i] = *q
			return nil
		}
	}
	return derrors.NotFound("quotation.not_found", "missing")
}

type stubPreBOQFieldsRepo struct {
	fields []domain.PreBOQRequiredField
}

var _ port.PreBOQRequiredFieldRepository = (*stubPreBOQFieldsRepo)(nil)

func (s *stubPreBOQFieldsRepo) ListAll(_ context.Context) ([]domain.PreBOQRequiredField, error) {
	return s.fields, nil
}

// ----- helpers -------------------------------------------------------

func newTestServiceWave106() *Service {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	return NewService(nil, nil, newStubOppRepo(), log)
}

// =====================================================================
// Tests
// =====================================================================

// TestService_MarkOpportunityAutoLost — single-row auto-Lost path used
// by the Wave 106 cron watcher.
func TestService_MarkOpportunityAutoLost(t *testing.T) {
	svc := newTestServiceWave106()
	repo := svc.opps.(*stubOppRepoWave106)

	// Build a Cold opportunity with an explicitly-elapsed activity
	// timestamp so IsAutoLostExpired returns true.
	o, err := domain.NewOpportunity("Acme")
	if err != nil {
		t.Fatalf("NewOpportunity: %v", err)
	}
	// Cold's auto-Lost window is 30d. Push activity to 31 days ago.
	o.LastActivityAt = time.Now().Add(-31 * 24 * time.Hour)
	repo.rows[o.ID] = o

	got, err := svc.MarkOpportunityAutoLost(context.Background(), o.ID)
	if err != nil {
		t.Fatalf("MarkOpportunityAutoLost: %v", err)
	}
	if got.Stage != domain.OpportunityStageLost {
		t.Errorf("stage = %s, want lost", got.Stage)
	}
	if !got.AutoLost {
		t.Errorf("auto_lost flag not set")
	}

	// Idempotent: calling again on Lost should return the entity without error.
	again, err := svc.MarkOpportunityAutoLost(context.Background(), o.ID)
	if err != nil {
		t.Fatalf("idempotent call: %v", err)
	}
	if again.Stage != domain.OpportunityStageLost {
		t.Errorf("idempotent stage drift: %s", again.Stage)
	}
}

// TestService_MarkOpportunityAutoLost_NotExpiredRejected — drift
// defense: when called on a non-expired row we refuse via Conflict.
func TestService_MarkOpportunityAutoLost_NotExpiredRejected(t *testing.T) {
	svc := newTestServiceWave106()
	repo := svc.opps.(*stubOppRepoWave106)

	o, _ := domain.NewOpportunity("Fresh Co")
	// LastActivityAt is "now" from constructor; 30d window has not lapsed.
	repo.rows[o.ID] = o

	_, err := svc.MarkOpportunityAutoLost(context.Background(), o.ID)
	if err == nil {
		t.Fatalf("want Conflict on non-expired, got nil")
	}
	de := derrors.As(err)
	if de == nil || de.Code != "opportunity.auto_lost_window_not_expired" {
		t.Errorf("want auto_lost_window_not_expired, got %v", err)
	}
}

// TestService_ReassignOpportunity — TC-OP-011 wiring across domain +
// audit + (stubbed) notify fan-out. We don't assert audit row payload
// here; the audit writer defaults to Nop so the call is a no-op.
func TestService_ReassignOpportunity(t *testing.T) {
	svc := newTestServiceWave106()
	repo := svc.opps.(*stubOppRepoWave106)
	user1 := uuid.New()
	user2 := uuid.New()

	o, _ := domain.NewOpportunity("Acme")
	o.OwnerUserID = &user1
	repo.rows[o.ID] = o

	got, err := svc.ReassignOpportunity(context.Background(), port.ReassignOpportunityInput{
		ID:         o.ID,
		NewOwnerID: user2,
		ByUserID:   uuid.New(),
	})
	if err != nil {
		t.Fatalf("ReassignOpportunity: %v", err)
	}
	if got.OwnerUserID == nil || *got.OwnerUserID != user2 {
		t.Errorf("owner not rotated: got %v, want %v", got.OwnerUserID, user2)
	}
}

// TestService_CompletePreBOQ_StructuredValidator — Wave 106 TC-OP-009.
// When the required-fields config is wired the snapshot must pass the
// structured validator before the legacy "non-empty JSON" check runs.
func TestService_CompletePreBOQ_StructuredValidator(t *testing.T) {
	svc := newTestServiceWave106()
	repo := svc.opps.(*stubOppRepoWave106)
	svc.preBOQRequiredFields = &stubPreBOQFieldsRepo{
		fields: []domain.PreBOQRequiredField{
			{FieldKey: "customer_name", FieldType: "string", Required: true},
			{FieldKey: "expected_capacity_mbps", FieldType: "number", Required: true},
		},
	}

	o, _ := domain.NewOpportunity("Acme")
	repo.rows[o.ID] = o

	// Missing field — must reject.
	_, err := svc.CompletePreBOQ(context.Background(), port.CompletePreBOQInput{
		ID:       o.ID,
		Snapshot: []byte(`{"customer_name":"Acme"}`),
	})
	if err == nil {
		t.Fatalf("want validation error for missing field, got nil")
	}
	de := derrors.As(err)
	if de == nil || de.Code != "opportunity.pre_boq_missing_required_fields" {
		t.Errorf("want pre_boq_missing_required_fields, got %v", err)
	}

	// All fields present — must succeed.
	_, err = svc.CompletePreBOQ(context.Background(), port.CompletePreBOQInput{
		ID:       o.ID,
		Snapshot: []byte(`{"customer_name":"Acme","expected_capacity_mbps":100}`),
	})
	if err != nil {
		t.Fatalf("happy-path CompletePreBOQ: %v", err)
	}
}

// TestService_CompletePreBOQ_LegacyFallback — when the required-fields
// repo is nil, CompletePreBOQ falls back to the legacy "any non-empty
// JSON" semantics. Confirms backward compat with deployments that
// haven't applied migration 0071 yet.
func TestService_CompletePreBOQ_LegacyFallback(t *testing.T) {
	svc := newTestServiceWave106()
	repo := svc.opps.(*stubOppRepoWave106)

	o, _ := domain.NewOpportunity("Acme")
	repo.rows[o.ID] = o

	// preBOQRequiredFields is nil — anything non-empty is accepted.
	_, err := svc.CompletePreBOQ(context.Background(), port.CompletePreBOQInput{
		ID:       o.ID,
		Snapshot: []byte(`{}`),
	})
	if err == nil {
		t.Logf("note: empty JSON object IS rejected by domain (pre_boq_empty)")
	}
	// Non-empty body must succeed.
	_, err = svc.CompletePreBOQ(context.Background(), port.CompletePreBOQInput{
		ID:       o.ID,
		Snapshot: []byte(`{"anything":"goes"}`),
	})
	if err != nil {
		t.Fatalf("legacy fallback rejected non-empty body: %v", err)
	}
}

// TestService_RunQuotationExpirySweep — Wave 106 cron RunOnce coverage.
// Two issued quotations: one past valid_until, one fresh. After a
// sweep only the expired one is flipped.
func TestService_RunQuotationExpirySweep(t *testing.T) {
	svc := newTestServiceWave106()
	qrepo := &stubQuotationRepoWave106{}
	svc.quotations = qrepo

	now := time.Now().UTC()
	expiredID := uuid.New()
	freshID := uuid.New()
	qrepo.rows = []domain.Quotation{
		{
			ID:         expiredID,
			Status:     domain.QuotationStatusIssued,
			ValidUntil: now.Add(-24 * time.Hour), // expired yesterday
			IssuedAt:   now.Add(-30 * 24 * time.Hour),
		},
		{
			ID:         freshID,
			Status:     domain.QuotationStatusIssued,
			ValidUntil: now.Add(7 * 24 * time.Hour), // 7d valid
			IssuedAt:   now,
		},
	}

	flipped, err := svc.RunQuotationExpirySweep(context.Background())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(flipped) != 1 {
		t.Fatalf("flipped count = %d, want 1", len(flipped))
	}
	if flipped[0] != expiredID {
		t.Errorf("flipped id = %v, want %v", flipped[0], expiredID)
	}
	// The fresh one still issued; the expired one flipped.
	for _, q := range qrepo.rows {
		if q.ID == expiredID && q.Status != domain.QuotationStatusExpired {
			t.Errorf("expired row status = %s, want expired", q.Status)
		}
		if q.ID == freshID && q.Status != domain.QuotationStatusIssued {
			t.Errorf("fresh row status = %s, want issued", q.Status)
		}
	}
}
