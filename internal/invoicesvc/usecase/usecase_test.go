// Wave 115 — invoicesvc usecase tests.
//
// Stub repos + cross-context readers exercise each service's happy path
// + the most load-bearing edge cases (immutable snapshot, terminal
// credit-note states, bulk partial roll-up, customer-scope enforcement).
package usecase

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/invoicesvc/domain"
	"github.com/ion-core/backend/internal/invoicesvc/port"
)

// =====================================================================
// Stubs
// =====================================================================

type stubSnapshotRepo struct {
	rows []domain.InvoiceSnapshot
}

func (s *stubSnapshotRepo) Create(_ context.Context, snap *domain.InvoiceSnapshot) error {
	s.rows = append(s.rows, *snap)
	return nil
}
func (s *stubSnapshotRepo) FindByID(_ context.Context, id uuid.UUID) (*domain.InvoiceSnapshot, error) {
	for i := range s.rows {
		if s.rows[i].ID == id {
			r := s.rows[i]
			return &r, nil
		}
	}
	return nil, nil
}
func (s *stubSnapshotRepo) ListByInvoice(_ context.Context, invoiceID uuid.UUID) ([]domain.InvoiceSnapshot, error) {
	out := []domain.InvoiceSnapshot{}
	for _, r := range s.rows {
		if r.InvoiceID == invoiceID {
			out = append(out, r)
		}
	}
	return out, nil
}
func (s *stubSnapshotRepo) FindLatestByInvoice(_ context.Context, invoiceID uuid.UUID) (*domain.InvoiceSnapshot, error) {
	rows, _ := s.ListByInvoice(context.Background(), invoiceID)
	if len(rows) == 0 {
		return nil, nil
	}
	r := rows[len(rows)-1]
	return &r, nil
}
func (s *stubSnapshotRepo) ExistsForInvoice(_ context.Context, invoiceID uuid.UUID) (bool, error) {
	rows, _ := s.ListByInvoice(context.Background(), invoiceID)
	return len(rows) > 0, nil
}

type stubInvoiceReader struct {
	byID       map[uuid.UUID]port.InvoiceProjection
	byCustomer map[uuid.UUID][]port.InvoiceProjection
	bulkSet    []uuid.UUID
	bulkErr    error
	aggResult  *port.AggregationResult
	cycleResult *port.CycleHealthResult
	topOverdue  []port.TopOverdueRow
	payments    map[uuid.UUID][]port.PaymentHistoryRow
	reminders   map[uuid.UUID][]port.ReminderHistoryRow
	stale       []uuid.UUID
}

func newStubReader() *stubInvoiceReader {
	return &stubInvoiceReader{
		byID:       map[uuid.UUID]port.InvoiceProjection{},
		byCustomer: map[uuid.UUID][]port.InvoiceProjection{},
		payments:   map[uuid.UUID][]port.PaymentHistoryRow{},
		reminders:  map[uuid.UUID][]port.ReminderHistoryRow{},
	}
}

func (s *stubInvoiceReader) FindByID(_ context.Context, id uuid.UUID) (*port.InvoiceProjection, error) {
	if p, ok := s.byID[id]; ok {
		return &p, nil
	}
	return nil, nil
}
func (s *stubInvoiceReader) ListByCustomer(_ context.Context, customerID uuid.UUID, limit, offset int) ([]port.InvoiceProjection, int, error) {
	rows := s.byCustomer[customerID]
	total := len(rows)
	if offset >= len(rows) {
		return nil, total, nil
	}
	end := offset + limit
	if end > len(rows) {
		end = len(rows)
	}
	return rows[offset:end], total, nil
}
func (s *stubInvoiceReader) ListByCycle(_ context.Context, _ uuid.UUID) ([]port.InvoiceProjection, error) {
	return nil, nil
}
func (s *stubInvoiceReader) FindForBulkRun(_ context.Context, _ map[string]any) ([]uuid.UUID, error) {
	return s.bulkSet, s.bulkErr
}
func (s *stubInvoiceReader) Aggregations(_ context.Context, _ port.InvoiceQueryFilter) (*port.AggregationResult, error) {
	if s.aggResult == nil {
		return &port.AggregationResult{ByStatus: map[string]int{}}, nil
	}
	return s.aggResult, nil
}
func (s *stubInvoiceReader) CycleHealth(_ context.Context, _ uuid.UUID) (*port.CycleHealthResult, error) {
	if s.cycleResult == nil {
		return &port.CycleHealthResult{}, nil
	}
	return s.cycleResult, nil
}
func (s *stubInvoiceReader) TopOverdueCustomers(_ context.Context, _ int) ([]port.TopOverdueRow, error) {
	return s.topOverdue, nil
}
func (s *stubInvoiceReader) PaymentHistory(_ context.Context, customerID uuid.UUID, _ int) ([]port.PaymentHistoryRow, error) {
	return s.payments[customerID], nil
}
func (s *stubInvoiceReader) ReminderHistory(_ context.Context, customerID uuid.UUID, _ int) ([]port.ReminderHistoryRow, error) {
	return s.reminders[customerID], nil
}
func (s *stubInvoiceReader) IssuedInLast24h(_ context.Context, _ int) ([]uuid.UUID, error) {
	return s.stale, nil
}

type stubCreditNoteRepo struct {
	rows  map[uuid.UUID]domain.CreditNote
	count int
}

func newStubCNRepo() *stubCreditNoteRepo {
	return &stubCreditNoteRepo{rows: map[uuid.UUID]domain.CreditNote{}}
}

func (s *stubCreditNoteRepo) Create(_ context.Context, cn *domain.CreditNote) error {
	s.rows[cn.ID] = *cn
	return nil
}
func (s *stubCreditNoteRepo) Update(_ context.Context, cn *domain.CreditNote) error {
	if _, ok := s.rows[cn.ID]; !ok {
		return errors.New("not found")
	}
	s.rows[cn.ID] = *cn
	return nil
}
func (s *stubCreditNoteRepo) FindByID(_ context.Context, id uuid.UUID) (*domain.CreditNote, error) {
	if r, ok := s.rows[id]; ok {
		return &r, nil
	}
	return nil, nil
}
func (s *stubCreditNoteRepo) List(_ context.Context, f port.CreditNoteFilter) ([]domain.CreditNote, int, error) {
	out := []domain.CreditNote{}
	for _, r := range s.rows {
		if f.Status != "" && string(r.Status) != f.Status {
			continue
		}
		out = append(out, r)
	}
	return out, len(out), nil
}
func (s *stubCreditNoteRepo) NextCreditNumber(_ context.Context) (string, error) {
	s.count++
	return "CN-TEST-" + uuidShort(s.count), nil
}

// SumIssuedAndAppliedForInvoice sums non-voided (issued + applied) CN
// amounts for one invoice. Wave 128B added this to the repo contract so
// the usecase can run the invoice-ceiling validator on Create.
func (s *stubCreditNoteRepo) SumIssuedAndAppliedForInvoice(_ context.Context, invoiceID uuid.UUID) (float64, error) {
	var sum float64
	for _, r := range s.rows {
		if r.InvoiceID != invoiceID {
			continue
		}
		if r.Status == domain.CreditNoteStatusIssued || r.Status == domain.CreditNoteStatusApplied {
			sum += r.Amount
		}
	}
	return sum, nil
}

func uuidShort(n int) string {
	if n < 10 {
		return "000" + string(rune('0'+n))
	}
	if n < 100 {
		return "00" + string(rune('0'+n/10)) + string(rune('0'+n%10))
	}
	return uuid.New().String()[:4]
}

type stubBulkJobRepo struct {
	rows map[uuid.UUID]domain.BulkGenerationJob
}

func newStubBulkJobRepo() *stubBulkJobRepo {
	return &stubBulkJobRepo{rows: map[uuid.UUID]domain.BulkGenerationJob{}}
}

func (s *stubBulkJobRepo) Create(_ context.Context, j *domain.BulkGenerationJob) error {
	s.rows[j.ID] = *j
	return nil
}
func (s *stubBulkJobRepo) Update(_ context.Context, j *domain.BulkGenerationJob) error {
	s.rows[j.ID] = *j
	return nil
}
func (s *stubBulkJobRepo) FindByID(_ context.Context, id uuid.UUID) (*domain.BulkGenerationJob, error) {
	if r, ok := s.rows[id]; ok {
		return &r, nil
	}
	return nil, nil
}
func (s *stubBulkJobRepo) List(_ context.Context, _ port.BulkJobFilter) ([]domain.BulkGenerationJob, int, error) {
	out := []domain.BulkGenerationJob{}
	for _, r := range s.rows {
		out = append(out, r)
	}
	return out, len(out), nil
}
func (s *stubBulkJobRepo) ListPending(_ context.Context, _ int) ([]domain.BulkGenerationJob, error) {
	out := []domain.BulkGenerationJob{}
	for _, r := range s.rows {
		if r.Status == domain.JobStatusPending {
			out = append(out, r)
		}
	}
	return out, nil
}

type stubBulkItemRepo struct {
	items map[uuid.UUID]domain.BulkGenerationItem
}

func newStubBulkItemRepo() *stubBulkItemRepo {
	return &stubBulkItemRepo{items: map[uuid.UUID]domain.BulkGenerationItem{}}
}

func (s *stubBulkItemRepo) CreateBatch(_ context.Context, items []domain.BulkGenerationItem) error {
	for _, it := range items {
		s.items[it.ID] = it
	}
	return nil
}
func (s *stubBulkItemRepo) Update(_ context.Context, it *domain.BulkGenerationItem) error {
	s.items[it.ID] = *it
	return nil
}
func (s *stubBulkItemRepo) ListByJob(_ context.Context, jobID uuid.UUID) ([]domain.BulkGenerationItem, error) {
	out := []domain.BulkGenerationItem{}
	for _, it := range s.items {
		if it.JobID == jobID {
			out = append(out, it)
		}
	}
	return out, nil
}
func (s *stubBulkItemRepo) ListQueuedForJob(_ context.Context, jobID uuid.UUID, _ int) ([]domain.BulkGenerationItem, error) {
	out := []domain.BulkGenerationItem{}
	for _, it := range s.items {
		if it.JobID == jobID && it.Status == domain.ItemStatusQueued {
			out = append(out, it)
		}
	}
	return out, nil
}

type stubGenerator struct {
	always *port.GeneratedInvoice
	err    error
}

func (s stubGenerator) GenerateForCustomer(_ context.Context, _, _ uuid.UUID, _ port.GenerationKind) (*port.GeneratedInvoice, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.always != nil {
		out := *s.always
		out.InvoiceID = uuid.New() // fresh id per call
		return &out, nil
	}
	return &port.GeneratedInvoice{InvoiceID: uuid.New(), InvoiceNumber: "INV-X"}, nil
}

// =====================================================================
// SnapshotService
// =====================================================================

func TestSnapshotService_CreateLoadsInvoiceViaReader(t *testing.T) {
	reader := newStubReader()
	repo := &stubSnapshotRepo{}
	invID := uuid.New()
	custID := uuid.New()
	reader.byID[invID] = port.InvoiceProjection{
		ID:           invID,
		CustomerID:   custID,
		Status:       "issued",
		Total:        500.00,
		SourceModule: "billing",
	}
	svc := NewSnapshotService(repo, reader)
	snap, err := svc.CreateSnapshot(context.Background(), invID, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if snap.InvoiceID != invID {
		t.Errorf("snapshot invoice id mismatch")
	}
	if snap.TotalAmount != 500.00 {
		t.Errorf("expected total 500, got %f", snap.TotalAmount)
	}
	if len(repo.rows) != 1 {
		t.Errorf("expected 1 persisted snapshot")
	}
}

func TestSnapshotService_NotFoundWhenReaderReturnsNil(t *testing.T) {
	reader := newStubReader()
	repo := &stubSnapshotRepo{}
	svc := NewSnapshotService(repo, reader)
	_, err := svc.CreateSnapshot(context.Background(), uuid.New(), nil, nil)
	if err == nil {
		t.Fatal("expected not-found error")
	}
}

func TestSnapshotService_RejectsZeroInvoice(t *testing.T) {
	svc := NewSnapshotService(&stubSnapshotRepo{}, newStubReader())
	if _, err := svc.CreateSnapshot(context.Background(), uuid.Nil, nil, nil); err == nil {
		t.Fatal("expected validation error")
	}
}

// =====================================================================
// CreditNoteService
// =====================================================================

func TestCreditNoteService_LifecycleIssueApply(t *testing.T) {
	repo := newStubCNRepo()
	svc := NewCreditNoteService(repo)
	invID := uuid.New()
	cn, err := svc.Create(context.Background(), invID, nil, 100, "refund", nil)
	if err != nil {
		t.Fatal(err)
	}
	if cn.Status != domain.CreditNoteStatusDraft {
		t.Fatalf("expected draft, got %s", cn.Status)
	}
	approver := uuid.New()
	cn2, err := svc.Issue(context.Background(), cn.ID, &approver)
	if err != nil {
		t.Fatal(err)
	}
	if cn2.Status != domain.CreditNoteStatusIssued {
		t.Errorf("expected issued, got %s", cn2.Status)
	}
	if cn2.CreditNo == "" {
		t.Error("credit_no should be set after Issue")
	}
	cn3, err := svc.Apply(context.Background(), cn.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cn3.Status != domain.CreditNoteStatusApplied {
		t.Errorf("expected applied, got %s", cn3.Status)
	}
}

func TestCreditNoteService_VoidFromDraftRequiresReason(t *testing.T) {
	repo := newStubCNRepo()
	svc := NewCreditNoteService(repo)
	cn, _ := svc.Create(context.Background(), uuid.New(), nil, 50, "x", nil)
	if _, err := svc.Void(context.Background(), cn.ID, nil, ""); err == nil {
		t.Fatal("expected validation error for empty reason")
	}
	if _, err := svc.Void(context.Background(), cn.ID, nil, "wrong-customer"); err != nil {
		t.Fatal(err)
	}
}

func TestCreditNoteService_NotFoundOnMissing(t *testing.T) {
	svc := NewCreditNoteService(newStubCNRepo())
	if _, err := svc.Apply(context.Background(), uuid.New()); err == nil {
		t.Fatal("expected not-found")
	}
}

// =====================================================================
// BulkService
// =====================================================================

func TestBulkService_StartJobMaterializesItems(t *testing.T) {
	jobs := newStubBulkJobRepo()
	items := newStubBulkItemRepo()
	reader := newStubReader()
	reader.bulkSet = []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	svc := NewBulkService(jobs, items, reader, nil)
	job, err := svc.StartJob(context.Background(), port.StartBulkJobInput{
		Kind: domain.BulkJobMonthlyCycle,
	})
	if err != nil {
		t.Fatal(err)
	}
	if job.TotalExpected != 3 {
		t.Errorf("expected 3 expected, got %d", job.TotalExpected)
	}
	if got := len(items.items); got != 3 {
		t.Errorf("expected 3 items, got %d", got)
	}
}

func TestBulkService_RunJobAllSuccess(t *testing.T) {
	jobs := newStubBulkJobRepo()
	items := newStubBulkItemRepo()
	reader := newStubReader()
	reader.bulkSet = []uuid.UUID{uuid.New(), uuid.New()}
	svc := NewBulkService(jobs, items, reader, stubGenerator{})
	job, _ := svc.StartJob(context.Background(), port.StartBulkJobInput{Kind: domain.BulkJobMonthlyCycle})
	out, err := svc.RunJob(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != domain.JobStatusCompleted {
		t.Errorf("expected completed, got %s", out.Status)
	}
	if out.TotalGenerated != 2 {
		t.Errorf("expected 2 generated, got %d", out.TotalGenerated)
	}
}

func TestBulkService_RunJobAllFail(t *testing.T) {
	jobs := newStubBulkJobRepo()
	items := newStubBulkItemRepo()
	reader := newStubReader()
	reader.bulkSet = []uuid.UUID{uuid.New(), uuid.New()}
	svc := NewBulkService(jobs, items, reader, stubGenerator{err: errors.New("billing-svc down")})
	job, _ := svc.StartJob(context.Background(), port.StartBulkJobInput{Kind: domain.BulkJobMonthlyCycle})
	out, err := svc.RunJob(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != domain.JobStatusFailed {
		t.Errorf("expected failed, got %s", out.Status)
	}
}

func TestBulkService_RunJobNoGenerator_FailsAllItems(t *testing.T) {
	jobs := newStubBulkJobRepo()
	items := newStubBulkItemRepo()
	reader := newStubReader()
	reader.bulkSet = []uuid.UUID{uuid.New()}
	svc := NewBulkService(jobs, items, reader, nil)
	job, _ := svc.StartJob(context.Background(), port.StartBulkJobInput{Kind: domain.BulkJobMonthlyCycle})
	out, err := svc.RunJob(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != domain.JobStatusFailed {
		t.Errorf("expected failed with no generator, got %s", out.Status)
	}
}

func TestBulkService_RunJobIdempotentOnTerminal(t *testing.T) {
	jobs := newStubBulkJobRepo()
	items := newStubBulkItemRepo()
	reader := newStubReader()
	reader.bulkSet = []uuid.UUID{uuid.New()}
	svc := NewBulkService(jobs, items, reader, stubGenerator{})
	job, _ := svc.StartJob(context.Background(), port.StartBulkJobInput{Kind: domain.BulkJobMonthlyCycle})
	_, _ = svc.RunJob(context.Background(), job.ID)
	// second run on a terminal job: returns the existing job snapshot
	out, err := svc.RunJob(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != domain.JobStatusCompleted {
		t.Errorf("re-run should return same terminal status, got %s", out.Status)
	}
}

// =====================================================================
// MonitoringService
// =====================================================================

func TestMonitoringService_MyInvoices_RequiresCustomer(t *testing.T) {
	reader := newStubReader()
	svc := NewMonitoringService(reader)
	if _, _, err := svc.MyInvoices(context.Background(), uuid.Nil, port.CustomerInvoiceFilter{}); err == nil {
		t.Fatal("expected unauthorized")
	}
}

func TestMonitoringService_MyInvoice_RejectsCrossCustomer(t *testing.T) {
	reader := newStubReader()
	otherCust := uuid.New()
	invID := uuid.New()
	reader.byID[invID] = port.InvoiceProjection{ID: invID, CustomerID: otherCust}
	svc := NewMonitoringService(reader)
	// Caller claims a different customer_id than the invoice's actual.
	myCust := uuid.New()
	_, err := svc.MyInvoice(context.Background(), myCust, invID)
	if err == nil {
		t.Fatal("expected not-found for cross-customer read")
	}
}

func TestMonitoringService_MyInvoice_OK(t *testing.T) {
	reader := newStubReader()
	cust := uuid.New()
	invID := uuid.New()
	reader.byID[invID] = port.InvoiceProjection{ID: invID, CustomerID: cust, Total: 100}
	svc := NewMonitoringService(reader)
	proj, err := svc.MyInvoice(context.Background(), cust, invID)
	if err != nil {
		t.Fatal(err)
	}
	if proj.Total != 100 {
		t.Errorf("expected total 100, got %f", proj.Total)
	}
}

func TestMonitoringService_Aggregations_PassThrough(t *testing.T) {
	reader := newStubReader()
	reader.aggResult = &port.AggregationResult{
		TotalCount:    10,
		TotalAmount:   12345.67,
		OverdueCount:  3,
		OverdueAmount: 999.99,
		ByStatus:      map[string]int{"paid": 7, "overdue": 3},
		AgingBuckets:  port.AgingBuckets{Bucket0_30: 500, Bucket31_60: 200},
	}
	svc := NewMonitoringService(reader)
	agg, err := svc.Aggregations(context.Background(), port.InvoiceQueryFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if agg.TotalCount != 10 || agg.OverdueCount != 3 {
		t.Errorf("aggregation mismatch: %+v", agg)
	}
	if agg.AgingBuckets.Bucket0_30 != 500 {
		t.Errorf("aging bucket lost")
	}
}

func TestMonitoringService_CycleHealth_StaleBy24h(t *testing.T) {
	reader := newStubReader()
	old := time.Now().Add(-48 * time.Hour)
	reader.cycleResult = &port.CycleHealthResult{
		LastRunAt:  &old,
		StaleBy24h: true,
	}
	svc := NewMonitoringService(reader)
	res, err := svc.CycleHealth(context.Background(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	if !res.StaleBy24h {
		t.Error("expected stale by 24h")
	}
}
