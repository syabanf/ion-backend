// Package usecase wires the partnership bounded context together.
//
// Each service depends only on port interfaces — never on the postgres
// adapters directly — so the bounded context can move to its own
// binary (cmd/partnership-svc) without dragging concrete repo types
// into other contexts.
//
// Wave 100 ships three services: AgreementService, SubmissionService,
// SettlementService, and ComplianceService. The compliance evaluator
// cron lives in internal/partnership/cron.
package usecase

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/partnership/domain"
	"github.com/ion-core/backend/internal/partnership/port"
	"github.com/ion-core/backend/pkg/audit"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// AgreementService implements the agreement-CRUD surface for the
// partnership context.
//
// We don't expose a "delete agreement" path — historical agreements
// must stay because settlements snapshot their terms by FK, and we
// want a paper trail. Operators close an agreement by setting
// effective_to instead.
type AgreementService struct {
	agreements port.AgreementRepository
	audit      audit.Writer
}

func NewAgreementService(agreements port.AgreementRepository, auditW audit.Writer) *AgreementService {
	if auditW == nil {
		auditW = audit.Nop{}
	}
	return &AgreementService{agreements: agreements, audit: auditW}
}

// CreateAgreement persists a new agreement row.
//
// Validation:
//   - reseller_account_id required
//   - revshare_pct must be in [0, 1]
//   - compliance_threshold_pct must be in [0, 1]
//   - ramp_months >= 0
//   - effective_to (if set) must be on or after effective_from
//
// Cross-row uniqueness (e.g. "no overlapping effective ranges") is NOT
// enforced — the lookup "active agreement at date X" picks the most-
// recent effective_from that contains X, so overlaps just shadow the
// older row. Operators clean this up by setting effective_to on the
// shadowed row.
func (s *AgreementService) CreateAgreement(ctx context.Context, in port.CreateAgreementInput) (*domain.Agreement, error) {
	if in.ResellerAccountID == uuid.Nil {
		return nil, derrors.Validation("agreement.reseller_required", "reseller_account_id is required")
	}
	if in.RevsharePct < 0 || in.RevsharePct > 1 {
		return nil, derrors.Validation("agreement.revshare_invalid", "revshare_pct must be 0..1")
	}
	if in.ComplianceThresholdPct < 0 || in.ComplianceThresholdPct > 1 {
		return nil, derrors.Validation("agreement.threshold_invalid", "compliance_threshold_pct must be 0..1")
	}
	if in.RampMonths < 0 {
		return nil, derrors.Validation("agreement.ramp_invalid", "ramp_months must be >= 0")
	}
	if in.EffectiveTo != nil && in.EffectiveTo.Before(in.EffectiveFrom) {
		return nil, derrors.Validation("agreement.dates_invalid", "effective_to must be on or after effective_from")
	}
	now := time.Now().UTC()
	terms := in.TermsJSON
	if terms == nil {
		terms = map[string]any{}
	}
	a := &domain.Agreement{
		ID:                     uuid.New(),
		ResellerAccountID:      in.ResellerAccountID,
		TermsJSON:              terms,
		RevsharePct:            in.RevsharePct,
		RampMonths:             in.RampMonths,
		ComplianceThresholdPct: in.ComplianceThresholdPct,
		EffectiveFrom:          in.EffectiveFrom,
		EffectiveTo:            in.EffectiveTo,
		SignedBy:               in.SignedBy,
		CreatedAt:              now,
		UpdatedAt:              now,
	}
	if in.SignedBy != nil {
		a.SignedAt = &now
	}
	if err := s.agreements.Create(ctx, a); err != nil {
		return nil, err
	}

	uid := uuid.Nil
	if in.SignedBy != nil {
		uid = *in.SignedBy
	}
	audit.SafeWrite(ctx, s.audit, audit.Entry{
		UserID:     uid,
		Module:     "partnership",
		RecordType: "partnership.agreement",
		RecordID:   a.ID.String(),
		Reason:     "agreement_created",
	})
	return a, nil
}

// GetActiveAgreement returns the agreement in force for `resellerID`
// on date `at`. `at` defaults to NOW when zero.
func (s *AgreementService) GetActiveAgreement(ctx context.Context, resellerID uuid.UUID, at time.Time) (*domain.Agreement, error) {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	return s.agreements.FindActive(ctx, resellerID, at)
}

func (s *AgreementService) GetAgreement(ctx context.Context, id uuid.UUID) (*domain.Agreement, error) {
	return s.agreements.FindByID(ctx, id)
}

func (s *AgreementService) ListAgreements(ctx context.Context, f port.AgreementListFilter) ([]domain.Agreement, int, error) {
	return s.agreements.List(ctx, f)
}
