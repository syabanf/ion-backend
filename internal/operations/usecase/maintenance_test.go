package usecase

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/operations/domain"
	"github.com/ion-core/backend/internal/operations/port"
)

// =====================================================================
// Stub repos / readers for the maintenance service.
// =====================================================================

type stubMaintReader struct {
	event        *port.MaintenanceEventSummary
	inProgress   []port.MaintenanceEventSummary
	pending      []port.MaintenanceEventSummary
	approvedAt   *time.Time
	overrunCalls int
	updatedCount int
}

func (s *stubMaintReader) FindEvent(_ context.Context, _ uuid.UUID) (*port.MaintenanceEventSummary, error) {
	return s.event, nil
}
func (s *stubMaintReader) ListPendingApproval(_ context.Context, _ int) ([]port.MaintenanceEventSummary, error) {
	return nil, nil
}
func (s *stubMaintReader) ListPendingLeadTimeNotify(_ context.Context, _ int, _ int) ([]port.MaintenanceEventSummary, error) {
	return s.pending, nil
}
func (s *stubMaintReader) ListInProgress(_ context.Context, _ int) ([]port.MaintenanceEventSummary, error) {
	return s.inProgress, nil
}
func (s *stubMaintReader) MarkApproved(_ context.Context, _, _ uuid.UUID, at time.Time) error {
	s.approvedAt = &at
	return nil
}
func (s *stubMaintReader) MarkOverrun(_ context.Context, _ uuid.UUID, _ time.Time) error {
	s.overrunCalls++
	return nil
}
func (s *stubMaintReader) UpdateAffectedCount(_ context.Context, _ uuid.UUID, count int) error {
	s.updatedCount = count
	return nil
}

type stubAffectedRepo struct {
	rows         []domain.MaintenanceAffectedCustomer
	notified     map[uuid.UUID]string
	notifyErrors map[uuid.UUID]string
}

func newStubAffected() *stubAffectedRepo {
	return &stubAffectedRepo{
		notified:     map[uuid.UUID]string{},
		notifyErrors: map[uuid.UUID]string{},
	}
}

func (s *stubAffectedRepo) CreateBatch(_ context.Context, rows []domain.MaintenanceAffectedCustomer) (int, error) {
	s.rows = append(s.rows, rows...)
	return len(rows), nil
}
func (s *stubAffectedRepo) ListByEvent(_ context.Context, _ uuid.UUID) ([]domain.MaintenanceAffectedCustomer, error) {
	return s.rows, nil
}
func (s *stubAffectedRepo) ListPendingNotification(_ context.Context, _ uuid.UUID, _ int) ([]domain.MaintenanceAffectedCustomer, error) {
	out := []domain.MaintenanceAffectedCustomer{}
	for _, r := range s.rows {
		if _, ok := s.notified[r.ID]; !ok {
			out = append(out, r)
		}
	}
	return out, nil
}
func (s *stubAffectedRepo) MarkNotified(_ context.Context, id uuid.UUID, channel string) error {
	s.notified[id] = channel
	return nil
}
func (s *stubAffectedRepo) MarkNotifyError(_ context.Context, id uuid.UUID, msg string) error {
	s.notifyErrors[id] = msg
	return nil
}

type stubEscalations struct {
	rows []domain.MaintenanceEscalation
}

func (s *stubEscalations) Create(_ context.Context, e *domain.MaintenanceEscalation) error {
	s.rows = append(s.rows, *e)
	return nil
}
func (s *stubEscalations) ListByEvent(_ context.Context, _ uuid.UUID) ([]domain.MaintenanceEscalation, error) {
	return s.rows, nil
}
func (s *stubEscalations) HighestLevel(_ context.Context, _ uuid.UUID) (int, error) {
	max := 0
	for _, r := range s.rows {
		if r.Level > max {
			max = r.Level
		}
	}
	return max, nil
}
func (s *stubEscalations) MarkAcknowledged(_ context.Context, _ uuid.UUID, _ time.Time) error {
	return nil
}
func (s *stubEscalations) MarkResolved(_ context.Context, _ uuid.UUID, _ time.Time) error {
	return nil
}

type stubSegmentResolver struct {
	out []port.AffectedCustomerInfo
	err error
}

func (s *stubSegmentResolver) ResolveByMaintenanceEvent(_ context.Context, _ uuid.UUID) ([]port.AffectedCustomerInfo, error) {
	return s.out, s.err
}

type stubNotifyDispatcher struct {
	called  int
	failOn  uuid.UUID
}

func (s *stubNotifyDispatcher) NotifyCustomer(_ context.Context, _ uuid.UUID, customerID uuid.UUID, _ domain.CustomerSegment) (string, error) {
	s.called++
	if customerID == s.failOn {
		return "", errors.New("notify failed")
	}
	return "push+email", nil
}

// =====================================================================
// Tests
// =====================================================================

func TestMaterializeAffectedCustomers_Persists(t *testing.T) {
	eventID := uuid.New()
	reader := &stubMaintReader{event: &port.MaintenanceEventSummary{ID: eventID}}
	affected := newStubAffected()
	segmentRes := &stubSegmentResolver{
		out: []port.AffectedCustomerInfo{
			{CustomerID: uuid.New(), CustomerSegment: domain.SegmentBroadband},
			{CustomerID: uuid.New(), CustomerSegment: domain.SegmentEnterprise},
		},
	}
	svc := NewMaintenanceService(MaintenanceDeps{
		Reader:     reader,
		Affected:   affected,
		SegmentRes: segmentRes,
	})
	total, err := svc.MaterializeAffectedCustomers(context.Background(), eventID)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if total != 2 {
		t.Errorf("expected 2 customers, got %d", total)
	}
	if len(affected.rows) != 2 {
		t.Errorf("expected 2 rows persisted, got %d", len(affected.rows))
	}
	if reader.updatedCount != 2 {
		t.Errorf("expected reader to record count=2, got %d", reader.updatedCount)
	}
}

func TestMaterializeAffectedCustomers_EmptyCascade(t *testing.T) {
	eventID := uuid.New()
	reader := &stubMaintReader{event: &port.MaintenanceEventSummary{ID: eventID}}
	affected := newStubAffected()
	segmentRes := &stubSegmentResolver{out: nil}
	svc := NewMaintenanceService(MaintenanceDeps{
		Reader: reader, Affected: affected, SegmentRes: segmentRes,
	})
	total, err := svc.MaterializeAffectedCustomers(context.Background(), eventID)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if total != 0 {
		t.Errorf("expected 0, got %d", total)
	}
}

func TestNotifyLeadTime_RespectsApprovalGate(t *testing.T) {
	eventID := uuid.New()
	start := time.Now().Add(-23 * time.Hour) // already past 24h lead-time
	pending := []port.MaintenanceEventSummary{
		{
			ID: eventID, ScheduledStart: start,
			CustomerSegment: domain.SegmentBroadband,
			LeadTimeNotifyHours: 24,
			ApprovalRequired: true, // not yet approved
		},
	}
	reader := &stubMaintReader{pending: pending}
	affected := newStubAffected()
	affected.rows = []domain.MaintenanceAffectedCustomer{
		{ID: uuid.New(), MaintenanceEventID: eventID, CustomerID: uuid.New()},
	}
	dispatcher := &stubNotifyDispatcher{}
	svc := NewMaintenanceService(MaintenanceDeps{
		Reader: reader, Affected: affected, Dispatcher: dispatcher,
	})
	n, err := svc.NotifyLeadTime(context.Background())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 dispatches (approval gate), got %d", n)
	}
	if dispatcher.called != 0 {
		t.Errorf("dispatcher should not be called on un-approved event")
	}
}

func TestNotifyLeadTime_DispatchesWhenWithinWindow(t *testing.T) {
	eventID := uuid.New()
	now := time.Now()
	approved := now.Add(-time.Hour)
	// 24h lead-time: start 12h from now → within window
	pending := []port.MaintenanceEventSummary{
		{
			ID: eventID, ScheduledStart: now.Add(12 * time.Hour),
			CustomerSegment: domain.SegmentBroadband,
			LeadTimeNotifyHours: 24,
			ApprovalRequired: false,
			ApprovedAt:       &approved,
		},
	}
	reader := &stubMaintReader{pending: pending}
	affected := newStubAffected()
	c1 := uuid.New()
	c2 := uuid.New()
	affected.rows = []domain.MaintenanceAffectedCustomer{
		{ID: c1, MaintenanceEventID: eventID, CustomerID: uuid.New()},
		{ID: c2, MaintenanceEventID: eventID, CustomerID: uuid.New()},
	}
	dispatcher := &stubNotifyDispatcher{}
	svc := NewMaintenanceService(MaintenanceDeps{
		Reader: reader, Affected: affected, Dispatcher: dispatcher,
	})
	n, err := svc.NotifyLeadTime(context.Background())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if n != 2 {
		t.Errorf("expected 2 dispatches, got %d", n)
	}
}

func TestDetectOverrun_FlagsPastWindow(t *testing.T) {
	end := time.Now().Add(-2 * time.Hour) // past
	reader := &stubMaintReader{
		inProgress: []port.MaintenanceEventSummary{
			{ID: uuid.New(), Status: "in_progress", ScheduledEnd: &end},
			{ID: uuid.New(), Status: "completed", ScheduledEnd: &end}, // not overrun
		},
	}
	svc := NewMaintenanceService(MaintenanceDeps{
		Reader: reader, OverrunTol: 30 * time.Minute,
	})
	n, err := svc.DetectOverrun(context.Background())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if n != 1 {
		t.Errorf("expected 1 flagged, got %d", n)
	}
}

func TestEscalateOverrun_AdvancesLevel(t *testing.T) {
	escs := &stubEscalations{}
	svc := NewMaintenanceService(MaintenanceDeps{Escalations: escs})
	eventID := uuid.New()
	e, err := svc.EscalateOverrun(context.Background(), eventID, "overrun detected", nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if e.Level != 1 {
		t.Errorf("first escalation should be level 1, got %d", e.Level)
	}
	e2, err := svc.EscalateOverrun(context.Background(), eventID, "still ongoing", nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if e2.Level != 2 {
		t.Errorf("second escalation should be level 2, got %d", e2.Level)
	}
}

func TestApprove_RejectsDoubleApproval(t *testing.T) {
	now := time.Now()
	reader := &stubMaintReader{event: &port.MaintenanceEventSummary{
		ID:         uuid.New(),
		ApprovedAt: &now,
	}}
	svc := NewMaintenanceService(MaintenanceDeps{Reader: reader})
	err := svc.Approve(context.Background(), uuid.New(), uuid.New())
	if err == nil {
		t.Fatalf("expected conflict error for double-approval")
	}
}
