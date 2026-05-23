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

// =====================================================================
// In-memory fakes for the payment use-case tests.
// =====================================================================

type memIntentRepo struct {
	mu     sync.Mutex
	rows   map[uuid.UUID]*domain.PaymentIntent
	byIdem map[string]uuid.UUID
}

func newMemIntentRepo() *memIntentRepo {
	return &memIntentRepo{
		rows:   map[uuid.UUID]*domain.PaymentIntent{},
		byIdem: map[string]uuid.UUID{},
	}
}

func (m *memIntentRepo) CreateOrFetchByIdempotency(ctx context.Context, intent *domain.PaymentIntent) (bool, *domain.PaymentIntent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if intent.IdempotencyKey != nil && *intent.IdempotencyKey != "" {
		if existing, ok := m.byIdem[*intent.IdempotencyKey]; ok {
			cpy := *m.rows[existing]
			return false, &cpy, nil
		}
		m.byIdem[*intent.IdempotencyKey] = intent.ID
	}
	cpy := *intent
	m.rows[intent.ID] = &cpy
	return true, intent, nil
}

func (m *memIntentRepo) FindByID(ctx context.Context, id uuid.UUID) (*domain.PaymentIntent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rows[id]
	if !ok {
		return nil, derrors.NotFound("payment_intent.not_found", "not found")
	}
	cpy := *r
	return &cpy, nil
}

func (m *memIntentRepo) FindByExternalRef(ctx context.Context, ref string) (*domain.PaymentIntent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.rows {
		if r.ExternalPaymentRef != nil && *r.ExternalPaymentRef == ref {
			cpy := *r
			return &cpy, nil
		}
	}
	return nil, derrors.NotFound("payment_intent.not_found", "not found")
}

func (m *memIntentRepo) List(ctx context.Context, f port.IntentListFilter) ([]domain.PaymentIntent, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []domain.PaymentIntent{}
	for _, r := range m.rows {
		if f.Status != "" && string(r.Status) != f.Status {
			continue
		}
		out = append(out, *r)
	}
	return out, len(out), nil
}

func (m *memIntentRepo) Update(ctx context.Context, intent *domain.PaymentIntent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.rows[intent.ID]; !ok {
		return derrors.NotFound("payment_intent.not_found", "not found")
	}
	cpy := *intent
	m.rows[intent.ID] = &cpy
	return nil
}

func (m *memIntentRepo) ListPendingOlderThan(ctx context.Context, cutoff time.Time, limit int) ([]domain.PaymentIntent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []domain.PaymentIntent{}
	for _, r := range m.rows {
		if r.Status == domain.PaymentStatusPending && r.CreatedAt.Before(cutoff) {
			out = append(out, *r)
		}
	}
	return out, nil
}

type memGatewayRepo struct {
	rows []domain.PaymentGateway
}

func (m *memGatewayRepo) Create(ctx context.Context, g *domain.PaymentGateway) error { return nil }
func (m *memGatewayRepo) FindByID(ctx context.Context, id uuid.UUID) (*domain.PaymentGateway, error) {
	for _, g := range m.rows {
		if g.ID == id {
			return &g, nil
		}
	}
	return nil, derrors.NotFound("payment_gateway.not_found", "not found")
}
func (m *memGatewayRepo) FindByCode(ctx context.Context, code string) (*domain.PaymentGateway, error) {
	for _, g := range m.rows {
		if g.Code == code {
			return &g, nil
		}
	}
	return nil, derrors.NotFound("payment_gateway.not_found", "not found")
}
func (m *memGatewayRepo) ListActive(ctx context.Context) ([]domain.PaymentGateway, error) {
	out := []domain.PaymentGateway{}
	for _, g := range m.rows {
		if g.IsActive {
			out = append(out, g)
		}
	}
	return out, nil
}
func (m *memGatewayRepo) ListAll(ctx context.Context) ([]domain.PaymentGateway, error) {
	return m.rows, nil
}
func (m *memGatewayRepo) Update(ctx context.Context, g *domain.PaymentGateway) error { return nil }

type memWebhookRepo struct {
	mu      sync.Mutex
	rows    map[uuid.UUID]*domain.PaymentWebhook
	byDedup map[string]uuid.UUID
}

func newMemWebhookRepo() *memWebhookRepo {
	return &memWebhookRepo{
		rows:    map[uuid.UUID]*domain.PaymentWebhook{},
		byDedup: map[string]uuid.UUID{},
	}
}

func (m *memWebhookRepo) CreateOrFetchByDedup(ctx context.Context, w *domain.PaymentWebhook) (bool, *domain.PaymentWebhook, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := w.GatewayID.String() + ":" + w.ExternalEventID
	if existing, ok := m.byDedup[key]; ok {
		cpy := *m.rows[existing]
		return false, &cpy, nil
	}
	cpy := *w
	m.rows[w.ID] = &cpy
	m.byDedup[key] = w.ID
	return true, w, nil
}

func (m *memWebhookRepo) FindByID(ctx context.Context, id uuid.UUID) (*domain.PaymentWebhook, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.rows[id]
	if !ok {
		return nil, derrors.NotFound("payment_webhook.not_found", "not found")
	}
	cpy := *r
	return &cpy, nil
}

func (m *memWebhookRepo) Update(ctx context.Context, w *domain.PaymentWebhook) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.rows[w.ID]; !ok {
		return derrors.NotFound("payment_webhook.not_found", "not found")
	}
	cpy := *w
	m.rows[w.ID] = &cpy
	return nil
}

// stubGatewayClient is a minimal port.GatewayClient that returns
// deterministic payloads. Used for both intent + webhook tests.
type stubGatewayClient struct {
	code string
}

func (s *stubGatewayClient) Code() string { return s.code }
func (s *stubGatewayClient) CreatePayment(ctx context.Context, in port.CreatePaymentInput) (*port.CreatePaymentResult, error) {
	return &port.CreatePaymentResult{ExternalRef: "ext-" + in.IntentID.String()[:8]}, nil
}
func (s *stubGatewayClient) RefundPayment(ctx context.Context, intent *domain.PaymentIntent, amount float64, reason string) (*port.RefundResult, error) {
	return &port.RefundResult{ExternalRef: "refund-" + intent.ID.String()[:8]}, nil
}
func (s *stubGatewayClient) CheckStatus(ctx context.Context, intent *domain.PaymentIntent) (*port.CheckStatusResult, error) {
	return &port.CheckStatusResult{Status: string(intent.Status)}, nil
}
func (s *stubGatewayClient) VerifySignature(payload []byte, signature string) bool { return signature == "ok" }
func (s *stubGatewayClient) ParseWebhook(payload []byte) (port.ParsedWebhook, error) {
	// Test webhooks carry an "x-ref" delim in the body that we use to
	// reconstruct the external ref + new status.
	// Body shape: "ext:<external_ref>;status:succeeded"
	str := string(payload)
	parts := map[string]string{}
	for _, kv := range splitSemis(str) {
		k, v := splitColon(kv)
		parts[k] = v
	}
	out := port.ParsedWebhook{
		ExternalEventID:    parts["eid"],
		ExternalPaymentRef: parts["ext"],
	}
	switch parts["status"] {
	case "succeeded":
		out.NewStatus = domain.PaymentStatusSucceeded
		now := time.Now()
		out.PaidAt = &now
	case "failed":
		out.NewStatus = domain.PaymentStatusFailed
		out.FailureCode = "stub_failure"
	}
	return out, nil
}
func (s *stubGatewayClient) ParseH2HStatement(content []byte) ([]port.ParsedH2HLine, error) {
	return nil, nil
}

func splitSemis(s string) []string {
	out := []string{}
	current := ""
	for i := 0; i < len(s); i++ {
		if s[i] == ';' {
			out = append(out, current)
			current = ""
			continue
		}
		current += string(s[i])
	}
	if current != "" {
		out = append(out, current)
	}
	return out
}

func splitColon(s string) (string, string) {
	for i := 0; i < len(s); i++ {
		if s[i] == ':' {
			return s[:i], s[i+1:]
		}
	}
	return s, ""
}

type stubRegistry struct {
	clients map[string]port.GatewayClient
}

func (r *stubRegistry) Resolve(code string) (port.GatewayClient, error) {
	c, ok := r.clients[code]
	if !ok {
		return nil, derrors.Validation("gateway.not_found", "gateway "+code+" not registered")
	}
	return c, nil
}

// =====================================================================
// Tests
// =====================================================================

func TestIntentService_CreateIntent_HappyPath(t *testing.T) {
	intents := newMemIntentRepo()
	gwID := uuid.New()
	gateways := &memGatewayRepo{rows: []domain.PaymentGateway{
		{ID: gwID, Code: "xendit", IsActive: true, Priority: 10,
			Kind: domain.GatewayKindVAAggregator,
			SupportedMethods: []string{"va_bca"}},
	}}
	webhooks := newMemWebhookRepo()
	registry := &stubRegistry{clients: map[string]port.GatewayClient{
		"xendit": &stubGatewayClient{code: "xendit"},
	}}
	svc := NewIntentService(intents, gateways, webhooks, NewRoutingService(), registry, nil)

	invoiceID := uuid.New()
	intent, err := svc.CreateIntent(context.Background(), port.CreateIntentInput{
		InvoiceID:      invoiceID,
		Amount:         100000,
		IdempotencyKey: "ord-001",
	})
	if err != nil {
		t.Fatalf("CreateIntent: %v", err)
	}
	if intent.Status != domain.PaymentStatusPending {
		t.Fatalf("expected pending after route, got %q", intent.Status)
	}
	if intent.ExternalPaymentRef == nil {
		t.Fatalf("expected external_payment_ref to be populated")
	}
	if intent.RoutingDecision == nil || intent.RoutingDecision.ChosenGatewayCode != "xendit" {
		t.Fatalf("routing decision missing or wrong gateway")
	}

	// Idempotency replay returns the same row, no new side effects.
	again, err := svc.CreateIntent(context.Background(), port.CreateIntentInput{
		InvoiceID:      invoiceID,
		Amount:         100000,
		IdempotencyKey: "ord-001",
	})
	if err != nil {
		t.Fatalf("CreateIntent replay: %v", err)
	}
	if again.ID != intent.ID {
		t.Errorf("replay should return same intent id; got %s vs %s", again.ID, intent.ID)
	}
}

func TestIntentService_CreateIntent_NoGateway(t *testing.T) {
	intents := newMemIntentRepo()
	gateways := &memGatewayRepo{rows: []domain.PaymentGateway{
		{ID: uuid.New(), Code: "xendit", IsActive: false, Priority: 10,
			SupportedMethods: []string{"va_bca"}},
	}}
	webhooks := newMemWebhookRepo()
	registry := &stubRegistry{clients: map[string]port.GatewayClient{
		"xendit": &stubGatewayClient{code: "xendit"},
	}}
	svc := NewIntentService(intents, gateways, webhooks, NewRoutingService(), registry, nil)

	intent, err := svc.CreateIntent(context.Background(), port.CreateIntentInput{
		InvoiceID:      uuid.New(),
		Amount:         100000,
		IdempotencyKey: "ord-002",
	})
	if err != nil {
		t.Fatalf("CreateIntent: %v", err)
	}
	// Routing failure leaves the intent in 'created' state for finance
	// to investigate.
	if intent.Status != domain.PaymentStatusCreated {
		t.Fatalf("expected created after routing failure, got %q", intent.Status)
	}
}

func TestWebhookService_Idempotency(t *testing.T) {
	intents := newMemIntentRepo()
	gwID := uuid.New()
	gateways := &memGatewayRepo{rows: []domain.PaymentGateway{
		{ID: gwID, Code: "xendit", IsActive: true, Priority: 10,
			Kind: domain.GatewayKindVAAggregator,
			SupportedMethods: []string{"va_bca"}},
	}}
	webhooks := newMemWebhookRepo()
	registry := &stubRegistry{clients: map[string]port.GatewayClient{
		"xendit": &stubGatewayClient{code: "xendit"},
	}}
	intentSvc := NewIntentService(intents, gateways, webhooks, NewRoutingService(), registry, nil)
	webhookSvc := NewWebhookService(webhooks, intents, gateways, registry, nil)

	// Set up an intent + external ref so the webhook can bind.
	intent, err := intentSvc.CreateIntent(context.Background(), port.CreateIntentInput{
		InvoiceID:      uuid.New(),
		Amount:         100000,
		IdempotencyKey: "ord-wh-1",
	})
	if err != nil {
		t.Fatalf("CreateIntent: %v", err)
	}
	if intent.ExternalPaymentRef == nil {
		t.Fatalf("expected external_payment_ref")
	}
	payload := "eid:evt-1;ext:" + *intent.ExternalPaymentRef + ";status:succeeded"
	first, err := webhookSvc.Ingest(context.Background(), port.WebhookIngestInput{
		GatewayCode: "xendit",
		Signature:   "ok",
		Payload:     []byte(payload),
		EventID:     "evt-1",
	})
	if err != nil {
		t.Fatalf("first webhook ingest: %v", err)
	}
	if first.Status != domain.WebhookStatusProcessed {
		t.Fatalf("first webhook status = %q, want processed", first.Status)
	}

	// Re-deliver: dedup must short-circuit to duplicate.
	second, err := webhookSvc.Ingest(context.Background(), port.WebhookIngestInput{
		GatewayCode: "xendit",
		Signature:   "ok",
		Payload:     []byte(payload),
		EventID:     "evt-1",
	})
	if err != nil {
		t.Fatalf("second webhook ingest: %v", err)
	}
	if second.Status != domain.WebhookStatusDuplicate {
		t.Errorf("duplicate webhook status = %q, want duplicate", second.Status)
	}

	// Confirm the intent only transitioned once: status must be succeeded
	// (we didn't somehow apply the webhook twice).
	updated, err := intents.FindByID(context.Background(), intent.ID)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if updated.Status != domain.PaymentStatusSucceeded {
		t.Errorf("intent status = %q, want succeeded", updated.Status)
	}
}

func TestWebhookService_SuspectOnBadSignature(t *testing.T) {
	intents := newMemIntentRepo()
	gateways := &memGatewayRepo{rows: []domain.PaymentGateway{
		{ID: uuid.New(), Code: "xendit", IsActive: true, Priority: 10,
			Kind: domain.GatewayKindVAAggregator},
	}}
	webhooks := newMemWebhookRepo()
	registry := &stubRegistry{clients: map[string]port.GatewayClient{
		"xendit": &stubGatewayClient{code: "xendit"},
	}}
	webhookSvc := NewWebhookService(webhooks, intents, gateways, registry, nil)

	wh, err := webhookSvc.Ingest(context.Background(), port.WebhookIngestInput{
		GatewayCode: "xendit",
		Signature:   "bad",
		Payload:     []byte("eid:evt-2;ext:nope;status:succeeded"),
		EventID:     "evt-2",
	})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if wh.Status != domain.WebhookStatusSuspect {
		t.Errorf("status = %q, want suspect", wh.Status)
	}
	if wh.SignatureValid {
		t.Errorf("signature_valid should be false")
	}
}

func TestIntentService_ExpireStaleIntents(t *testing.T) {
	intents := newMemIntentRepo()
	gwID := uuid.New()
	gateways := &memGatewayRepo{rows: []domain.PaymentGateway{
		{ID: gwID, Code: "xendit", IsActive: true, Priority: 10,
			Kind: domain.GatewayKindVAAggregator, SupportedMethods: []string{"va_bca"}},
	}}
	webhooks := newMemWebhookRepo()
	registry := &stubRegistry{clients: map[string]port.GatewayClient{
		"xendit": &stubGatewayClient{code: "xendit"},
	}}
	svc := NewIntentService(intents, gateways, webhooks, NewRoutingService(), registry, nil)

	// Create one intent and back-date it.
	intent, err := svc.CreateIntent(context.Background(), port.CreateIntentInput{
		InvoiceID:      uuid.New(),
		Amount:         100000,
		IdempotencyKey: "stale-1",
	})
	if err != nil {
		t.Fatalf("CreateIntent: %v", err)
	}
	intents.mu.Lock()
	intents.rows[intent.ID].CreatedAt = time.Now().Add(-48 * time.Hour)
	intents.mu.Unlock()

	expired, err := svc.ExpireStaleIntents(context.Background(), 24*time.Hour)
	if err != nil {
		t.Fatalf("ExpireStaleIntents: %v", err)
	}
	if expired != 1 {
		t.Errorf("expired = %d, want 1", expired)
	}
	updated, _ := intents.FindByID(context.Background(), intent.ID)
	if updated.Status != domain.PaymentStatusExpired {
		t.Errorf("status = %q, want expired", updated.Status)
	}
}
