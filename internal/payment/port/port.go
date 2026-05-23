// Package port defines the driving (UseCase) and driven (Repository,
// GatewayClient) contracts for the payment bounded context.
//
// Same hexagonal layout as identity / reseller / enterprise: HTTP
// handlers depend on a UseCase interface; the UseCase depends on
// repository + gateway-client interfaces; postgres adapters implement
// the repositories; per-gateway adapters implement GatewayClient. The
// domain stays oblivious to both transport and storage so the bounded
// context can move into its own service (cmd/payment-svc) without
// touching domain rules.
package port

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/payment/domain"
)

// =====================================================================
// Inputs
// =====================================================================

// CreateIntentInput is the create-intent surface. IdempotencyKey is
// optional but strongly encouraged — if the caller supplies one, the
// usecase short-circuits to the existing intent on retry rather than
// double-billing.
type CreateIntentInput struct {
	InvoiceID       uuid.UUID
	CustomerID      *uuid.UUID
	Amount          float64
	Currency        string
	IdempotencyKey  string
	PreferredMethod string // e.g. "va_bca" — routing may still override
}

// IntentListFilter scopes an intent list query.
type IntentListFilter struct {
	InvoiceID  *uuid.UUID
	CustomerID *uuid.UUID
	Status     string
	Limit      int
	Offset     int
}

// RequestRefundInput is the refund-request surface. Amount must be > 0
// and the intent's remaining refundable balance must accommodate it.
type RequestRefundInput struct {
	PaymentIntentID uuid.UUID
	Amount          float64
	Reason          string
	RequestedBy     *uuid.UUID
}

// RefundListFilter scopes a refund list query.
type RefundListFilter struct {
	PaymentIntentID *uuid.UUID
	Status          string
	Limit           int
	Offset          int
}

// UploadH2HStatementInput is the H2H bank statement upload surface.
type UploadH2HStatementInput struct {
	GatewayCode string
	Filename    string
	Content     []byte
}

// WebhookIngestInput is the public webhook ingest surface. Signature
// is the raw header value the gateway sent — verification happens
// in the usecase via the GatewayClient (which knows the per-gateway
// signing scheme).
type WebhookIngestInput struct {
	GatewayCode string
	Signature   string
	Payload     []byte
	EventID     string // optional override; usecase hashes body if empty
}

// =====================================================================
// UseCase (driving ports)
// =====================================================================

// IntentUseCase is the create / route / cancel / lookup surface.
type IntentUseCase interface {
	CreateIntent(ctx context.Context, in CreateIntentInput) (*domain.PaymentIntent, error)
	RouteAndPay(ctx context.Context, id uuid.UUID) (*domain.PaymentIntent, error)
	ConfirmFromWebhook(ctx context.Context, webhookID uuid.UUID) (*domain.PaymentIntent, error)
	CancelIntent(ctx context.Context, id uuid.UUID) (*domain.PaymentIntent, error)
	ExpireStaleIntents(ctx context.Context, olderThan time.Duration) (int, error)
	GetIntent(ctx context.Context, id uuid.UUID) (*domain.PaymentIntent, error)
	ListIntents(ctx context.Context, f IntentListFilter) ([]domain.PaymentIntent, int, error)
}

// WebhookUseCase is the public webhook ingest surface.
type WebhookUseCase interface {
	Ingest(ctx context.Context, in WebhookIngestInput) (*domain.PaymentWebhook, error)
	GetWebhook(ctx context.Context, id uuid.UUID) (*domain.PaymentWebhook, error)
}

// RefundUseCase is the refund request → approval → process → complete surface.
type RefundUseCase interface {
	RequestRefund(ctx context.Context, in RequestRefundInput) (*domain.Refund, error)
	ApproveRefund(ctx context.Context, id, by uuid.UUID) (*domain.Refund, error)
	RejectRefund(ctx context.Context, id uuid.UUID, reason string) (*domain.Refund, error)
	ProcessRefund(ctx context.Context, id uuid.UUID) (*domain.Refund, error)
	MarkRefundCompleted(ctx context.Context, id uuid.UUID, externalRef string) (*domain.Refund, error)
	GetRefund(ctx context.Context, id uuid.UUID) (*domain.Refund, error)
	ListRefunds(ctx context.Context, f RefundListFilter) ([]domain.Refund, int, error)
}

// H2HUseCase is the H2H bank statement upload + match surface.
type H2HUseCase interface {
	UploadStatement(ctx context.Context, in UploadH2HStatementInput) (*domain.H2HBankStatement, error)
	ParseStatement(ctx context.Context, id uuid.UUID) (*domain.H2HBankStatement, error)
	MatchStatement(ctx context.Context, id uuid.UUID) (*domain.H2HBankStatement, error)
	GetStatement(ctx context.Context, id uuid.UUID) (*domain.H2HBankStatement, error)
	ListStatements(ctx context.Context, limit, offset int) ([]domain.H2HBankStatement, int, error)
}

// GatewayUseCase is the admin gateway-registry surface (read-only for
// Wave 111 — gateway.write is reserved for a follow-up wave that adds
// the CRUD endpoints).
type GatewayUseCase interface {
	ListGateways(ctx context.Context, onlyActive bool) ([]domain.PaymentGateway, error)
	GetGatewayByCode(ctx context.Context, code string) (*domain.PaymentGateway, error)
}

// =====================================================================
// Routing policy
// =====================================================================

// RoutingPolicy chooses the best gateway for a given intent + candidate
// list. The default impl in usecase/routing.go filters by active +
// MatchesAmount + SupportsMethod, then sorts by priority. Custom
// implementations can plug in per-customer overrides etc.
type RoutingPolicy interface {
	ChooseGateway(
		ctx context.Context,
		intent *domain.PaymentIntent,
		preferredMethod string,
		available []domain.PaymentGateway,
	) (*domain.PaymentGateway, domain.RouteDecision, error)
}

// =====================================================================
// Gateway client — driven port for one external processor
// =====================================================================

// CreatePaymentInput tells the gateway client what payment to set up.
type CreatePaymentInput struct {
	IntentID      uuid.UUID
	InvoiceID     uuid.UUID
	Amount        float64
	Currency      string
	Method        string
	CustomerEmail string
}

// CreatePaymentResult is what the gateway returns after a successful
// create call. ExternalRef may be a VA number, a payment URL, or a
// gateway-specific opaque id.
type CreatePaymentResult struct {
	ExternalRef  string
	PaymentURL   string
	VANumber     string
	ExpiresAt    *time.Time
}

// RefundResult mirrors CreatePaymentResult for refunds. ExternalRef is
// what the gateway returns from RefundPayment.
type RefundResult struct {
	ExternalRef string
}

// CheckStatusResult is the polled status from the gateway. Some
// gateways don't fire webhooks for every edge case; the
// CheckStatus method exists so the ExpireStaleIntents cron can
// disambiguate "really never paid" from "webhook lost in flight".
type CheckStatusResult struct {
	Status      string
	ExternalRef string
	PaidAt      *time.Time
	FailureCode string
}

// GatewayClient is the per-gateway adapter contract. One impl per
// processor (xendit, bca_h2h, midtrans, stripe). Stub-mode adapters
// satisfy the contract without making real network calls — see
// internal/payment/adapter/gateway/ for the stubs.
type GatewayClient interface {
	Code() string
	CreatePayment(ctx context.Context, in CreatePaymentInput) (*CreatePaymentResult, error)
	RefundPayment(ctx context.Context, intent *domain.PaymentIntent, amount float64, reason string) (*RefundResult, error)
	CheckStatus(ctx context.Context, intent *domain.PaymentIntent) (*CheckStatusResult, error)

	// VerifySignature is called by the WebhookUseCase before storing
	// the inbound payload. Returns true when the signature header
	// matches the body + secret.
	VerifySignature(payload []byte, signature string) bool

	// ParseWebhook extracts the external_event_id + intent ref +
	// (terminal status, paid_at, failure code) from the gateway's
	// webhook body. The usecase then drives the intent's state machine
	// from there.
	ParseWebhook(payload []byte) (ParsedWebhook, error)

	// ParseH2HStatement parses a raw bank statement file (CSV/TXT/MT940
	// — whatever the gateway sends). Returns one row per line. Only
	// H2H-bank gateways implement this meaningfully; other gateways
	// return an "unsupported" error.
	ParseH2HStatement(content []byte) ([]ParsedH2HLine, error)
}

// ParsedWebhook is the gateway-agnostic webhook payload.
type ParsedWebhook struct {
	ExternalEventID    string
	ExternalPaymentRef string
	NewStatus          domain.PaymentStatus
	PaidAt             *time.Time
	FailureCode        string
	FailureReason      string
	RefundExternalRef  string // populated on refund-completion webhooks
}

// ParsedH2HLine is one row from a parsed bank statement.
type ParsedH2HLine struct {
	RawJSON        []byte
	Amount         float64
	ValueDate      time.Time
	ReferenceText  string
}

// EncryptionService seals / opens gateway credential bundles. Reuses
// pkg/cryptutil's Sealer; the interface stays here so the usecase can
// be tested without a real sealer.
type EncryptionService interface {
	Seal(plain string) ([]byte, error)
	Open(sealed []byte) (string, error)
}

// =====================================================================
// Repositories (driven ports)
// =====================================================================

type PaymentGatewayRepository interface {
	Create(ctx context.Context, g *domain.PaymentGateway) error
	FindByID(ctx context.Context, id uuid.UUID) (*domain.PaymentGateway, error)
	FindByCode(ctx context.Context, code string) (*domain.PaymentGateway, error)
	ListActive(ctx context.Context) ([]domain.PaymentGateway, error)
	ListAll(ctx context.Context) ([]domain.PaymentGateway, error)
	Update(ctx context.Context, g *domain.PaymentGateway) error
}

type PaymentMethodRepository interface {
	Create(ctx context.Context, m *domain.PaymentMethod) error
	FindByID(ctx context.Context, id uuid.UUID) (*domain.PaymentMethod, error)
	ListForCustomer(ctx context.Context, customerID uuid.UUID) ([]domain.PaymentMethod, error)
	MarkUsed(ctx context.Context, id uuid.UUID, at time.Time) error
}

type PaymentIntentRepository interface {
	// CreateOrFetchByIdempotency runs INSERT ... ON CONFLICT DO NOTHING
	// against the idempotency_key UNIQUE constraint. On conflict the
	// repository re-fetches the existing row and returns it; callers
	// see (fresh=true) only on a brand-new row.
	CreateOrFetchByIdempotency(ctx context.Context, intent *domain.PaymentIntent) (fresh bool, persisted *domain.PaymentIntent, err error)
	FindByID(ctx context.Context, id uuid.UUID) (*domain.PaymentIntent, error)
	FindByExternalRef(ctx context.Context, ref string) (*domain.PaymentIntent, error)
	List(ctx context.Context, f IntentListFilter) ([]domain.PaymentIntent, int, error)
	Update(ctx context.Context, intent *domain.PaymentIntent) error

	// ListPendingOlderThan returns intents stuck in 'pending' past the
	// cutoff. Used by the ExpireStaleIntents cron.
	ListPendingOlderThan(ctx context.Context, cutoff time.Time, limit int) ([]domain.PaymentIntent, error)
}

type PaymentWebhookRepository interface {
	// CreateOrFetchByDedup inserts the row using
	// (gateway_id, external_event_id) ON CONFLICT DO NOTHING. On
	// conflict (i.e. a re-delivery) the existing row is re-fetched
	// and `fresh=false` is returned so the caller flips to Duplicate.
	CreateOrFetchByDedup(ctx context.Context, w *domain.PaymentWebhook) (fresh bool, persisted *domain.PaymentWebhook, err error)
	FindByID(ctx context.Context, id uuid.UUID) (*domain.PaymentWebhook, error)
	Update(ctx context.Context, w *domain.PaymentWebhook) error
}

type RefundRepository interface {
	Create(ctx context.Context, r *domain.Refund) error
	FindByID(ctx context.Context, id uuid.UUID) (*domain.Refund, error)
	List(ctx context.Context, f RefundListFilter) ([]domain.Refund, int, error)
	Update(ctx context.Context, r *domain.Refund) error

	// SumCompletedForIntent returns the cumulative refunded amount
	// across completed refund rows for a given intent. Used to drive
	// the intent's partial_refunded / refunded transition.
	SumCompletedForIntent(ctx context.Context, intentID uuid.UUID) (float64, error)
}

type H2HRepository interface {
	CreateStatement(ctx context.Context, s *domain.H2HBankStatement) error
	FindStatementByID(ctx context.Context, id uuid.UUID) (*domain.H2HBankStatement, error)
	FindStatementByHash(ctx context.Context, gatewayID uuid.UUID, hash string) (*domain.H2HBankStatement, error)
	ListStatements(ctx context.Context, limit, offset int) ([]domain.H2HBankStatement, int, error)
	UpdateStatement(ctx context.Context, s *domain.H2HBankStatement) error

	InsertLines(ctx context.Context, statementID uuid.UUID, lines []domain.H2HBankLine) error
	ListLinesForStatement(ctx context.Context, statementID uuid.UUID) ([]domain.H2HBankLine, error)
	UpdateLineMatch(ctx context.Context, line *domain.H2HBankLine) error
	ListUnmatchedLines(ctx context.Context, statementID uuid.UUID) ([]domain.H2HBankLine, error)
}
