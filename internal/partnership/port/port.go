// Package port defines the driving (UseCase) and driven (Repository)
// contracts for the partnership bounded context.
//
// Same hexagonal layout as identity / crm / warehouse / enterprise /
// reseller: HTTP handlers depend on UseCase interfaces; the UseCase
// depends on Repository interfaces; postgres adapters implement the
// repositories. The domain stays oblivious to both transport and
// storage so the bounded context can be extracted into its own
// service (cmd/partnership-svc) without touching domain rules.
//
// Wave 100 scope: agreements + monthly_submissions + settlements +
// compliance_evaluations. Together they cover the Partnership Monthly
// Submission, Partnership Settlement, and Monthly Compliance Check TC
// families.
package port

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/partnership/domain"
)

// =====================================================================
// Use-case inputs / filters
// =====================================================================

// CreateAgreementInput is the create-agreement payload. The usecase
// builds a domain.Agreement from this and persists; the HTTP DTO maps
// straight onto these fields.
type CreateAgreementInput struct {
	ResellerAccountID      uuid.UUID
	TermsJSON              map[string]any
	RevsharePct            float64
	RampMonths             int
	ComplianceThresholdPct float64
	EffectiveFrom          time.Time
	EffectiveTo            *time.Time
	SignedBy               *uuid.UUID
}

type AgreementListFilter struct {
	ResellerAccountID *uuid.UUID
	ActiveAt          *time.Time
	Limit             int
	Offset            int
}

// DraftSubmissionInput is the per-month "open a draft row" call. The
// usecase first looks up the agreement active at the period_end of
// (year, month) and stamps agreement_id onto the row.
type DraftSubmissionInput struct {
	ResellerAccountID uuid.UUID
	PeriodYear        int
	PeriodMonth       int
}

// UpdateSubmissionInput carries the editable fields on a draft or
// returned submission. Pointer fields are "only update if non-nil" so
// the HTTP PATCH path can send partial payloads.
type UpdateSubmissionInput struct {
	ID              uuid.UUID
	GrossRevenue    *float64
	NetRevenue      *float64
	SubscriberCount *int
	ChurnCount      *int
	EvidenceURL     *string
	EvidenceHash    *string
}

type SubmissionListFilter struct {
	ResellerAccountID *uuid.UUID
	Status            string
	PeriodYear        *int
	PeriodMonth       *int
	Limit             int
	Offset            int
}

type SettlementListFilter struct {
	ResellerAccountID *uuid.UUID
	Status            string
	PeriodYear        *int
	PeriodMonth       *int
	Limit             int
	Offset            int
}

type ComplianceListFilter struct {
	ResellerAccountID *uuid.UUID
	Status            string
	PeriodYear        *int
	PeriodMonth       *int
	Limit             int
	Offset            int
}

// EvaluateMonthSummary is the return shape from the compliance cron's
// EvaluateMonth call. Lets the cron log a one-line summary without
// shuffling the full evaluation slice.
type EvaluateMonthSummary struct {
	Evaluated   int // total rows written
	RampSkipped int
	Passed      int
	Breached    int
	Skipped     int // resellers with no confirmed submission for the period
}

// =====================================================================
// Repository interfaces (driven ports)
// =====================================================================

type AgreementRepository interface {
	Create(ctx context.Context, a *domain.Agreement) error
	FindByID(ctx context.Context, id uuid.UUID) (*domain.Agreement, error)
	// FindActive returns the agreement in force for `reseller` on date
	// `at`. Returns NotFound if no agreement matches.
	FindActive(ctx context.Context, resellerID uuid.UUID, at time.Time) (*domain.Agreement, error)
	List(ctx context.Context, f AgreementListFilter) ([]domain.Agreement, int, error)
	Update(ctx context.Context, a *domain.Agreement) error
	// ListResellersWithActiveAgreement returns the distinct reseller
	// ids that have at least one agreement covering date `at`. Used by
	// the compliance cron to iterate every active reseller in one query.
	ListResellersWithActiveAgreement(ctx context.Context, at time.Time) ([]uuid.UUID, error)
}

type MonthlySubmissionRepository interface {
	Create(ctx context.Context, s *domain.MonthlySubmission) error
	FindByID(ctx context.Context, id uuid.UUID) (*domain.MonthlySubmission, error)
	// FindByResellerPeriod looks up the (one) submission for a given
	// (reseller, year, month). Returns NotFound if missing.
	FindByResellerPeriod(ctx context.Context, resellerID uuid.UUID, year, month int) (*domain.MonthlySubmission, error)
	List(ctx context.Context, f SubmissionListFilter) ([]domain.MonthlySubmission, int, error)
	// UpdateFields persists the editable numeric/evidence columns
	// (used by PATCH on draft|returned rows).
	UpdateFields(ctx context.Context, s *domain.MonthlySubmission) error
	// UpdateStatus persists status + per-status timestamps + reason.
	// Used by Submit / Confirm / Return / Cancel / MarkDraft so each
	// transition lives behind one repo method.
	UpdateStatus(ctx context.Context, s *domain.MonthlySubmission) error
	// CountConfirmedBefore counts confirmed submissions for `reseller`
	// strictly before (year, month). Used by the compliance evaluator
	// to compute monthsSinceFirstSubmission.
	CountConfirmedBefore(ctx context.Context, resellerID uuid.UUID, year, month int) (int, error)
}

type SettlementRepository interface {
	Create(ctx context.Context, s *domain.Settlement) error
	FindByID(ctx context.Context, id uuid.UUID) (*domain.Settlement, error)
	FindBySubmission(ctx context.Context, submissionID uuid.UUID) (*domain.Settlement, error)
	List(ctx context.Context, f SettlementListFilter) ([]domain.Settlement, int, error)
	// UpdateStatus persists status + approved_by/approved_at + paid_at.
	UpdateStatus(ctx context.Context, s *domain.Settlement) error
	// UpdatePDF persists pdf_url + pdf_hash after the PDF generator
	// runs. Split from UpdateStatus so the generator can fail without
	// touching the lifecycle columns.
	UpdatePDF(ctx context.Context, id uuid.UUID, url, hash string) error
}

type ComplianceEvaluationRepository interface {
	// Create inserts; on (reseller, year, month) conflict it returns a
	// typed Conflict error so the cron can log + skip.
	Create(ctx context.Context, e *domain.ComplianceEvaluation) error
	FindByResellerPeriod(ctx context.Context, resellerID uuid.UUID, year, month int) (*domain.ComplianceEvaluation, error)
	List(ctx context.Context, f ComplianceListFilter) ([]domain.ComplianceEvaluation, int, error)
}

// =====================================================================
// Stub-friendly side-effect interfaces
// =====================================================================

// EvidenceStore writes submission-evidence blobs and returns a URL +
// sha256 hash. Wave 100 ships a local-disk stub (writes to /tmp);
// Wave 100b can swap in an S3 adapter without touching the usecase.
type EvidenceStore interface {
	Store(ctx context.Context, content []byte, filename string) (url string, hash string, err error)
}

// SettlementPDFGenerator renders a settlement PDF (or PDF-ish
// placeholder) and returns the bytes + sha256 hash. Wave 100 ships a
// text/HTML byte-stream stub — the real PDF library swap is a follow-up.
type SettlementPDFGenerator interface {
	Generate(
		ctx context.Context,
		settlement *domain.Settlement,
		agreement *domain.Agreement,
		submission *domain.MonthlySubmission,
	) (pdfBytes []byte, hash string, err error)
}
