// Package usecase wires the tax bounded context together.
//
// Service depends only on the port interfaces, never on the postgres
// adapters or the DJP HTTP client directly — that's what lets the
// bounded context move to its own service binary later without
// touching the domain.
package usecase

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/tax/domain"
	"github.com/ion-core/backend/internal/tax/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// Service is the tax UseCase implementation.
//
// The DJP gateway is optional at construction time — when nil, the
// SubmitFaktur method surfaces a clear "not configured" error rather
// than panicking. cmd/tax-svc (or whichever binary wires this up)
// always passes the stub gateway, so the nil branch is defensive.
type Service struct {
	profiles port.CompanyTaxProfileRepository
	fakturs  port.FakturPajakRepository
	djp      port.DJPGateway

	log *slog.Logger
}

// NewService constructs the tax UseCase. All four dependencies are
// required for full behavior; passing nil for `djp` is supported but
// SubmitFaktur will return KindUnavailable until a real gateway is
// wired.
func NewService(
	profiles port.CompanyTaxProfileRepository,
	fakturs port.FakturPajakRepository,
	djp port.DJPGateway,
	log *slog.Logger,
) *Service {
	return &Service{
		profiles: profiles,
		fakturs:  fakturs,
		djp:      djp,
		log:      log,
	}
}

// Compile-time check that Service satisfies the driving port.
var _ port.UseCase = (*Service)(nil)

// =====================================================================
// Profile reads + writes
// =====================================================================

// GetActiveProfile returns the active profile at `at` (defaults to
// now when zero). Surfaces NotFound when the subsidiary has no
// profile covering that timestamp — the caller (usually invoice
// generation) decides whether to treat that as "non-PKP fallback" or
// hard error.
func (s *Service) GetActiveProfile(ctx context.Context, subsidiaryID uuid.UUID, at time.Time) (*domain.CompanyTaxProfile, error) {
	if subsidiaryID == uuid.Nil {
		return nil, derrors.Validation(
			"tax_profile.subsidiary_required",
			"subsidiary_id is required",
		)
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	return s.profiles.FindActiveBySubsidiary(ctx, subsidiaryID, at)
}

// GetProfile fetches by id.
func (s *Service) GetProfile(ctx context.Context, id uuid.UUID) (*domain.CompanyTaxProfile, error) {
	return s.profiles.FindByID(ctx, id)
}

// ListProfiles returns paginated profiles, optionally filtered by
// subsidiary.
func (s *Service) ListProfiles(ctx context.Context, f port.CompanyTaxProfileFilter) ([]domain.CompanyTaxProfile, int, error) {
	return s.profiles.List(ctx, f)
}

// CreateProfile validates input + persists a new profile row. Conflict
// errors from the unique (subsidiary_id, effective_from) constraint
// surface as KindConflict via mapDBError in the postgres adapter.
func (s *Service) CreateProfile(ctx context.Context, in port.CreateCompanyTaxProfileInput) (*domain.CompanyTaxProfile, error) {
	from, err := parseDate(in.EffectiveFrom, "effective_from")
	if err != nil {
		return nil, err
	}
	var to *time.Time
	if in.EffectiveTo != "" {
		t, err := parseDate(in.EffectiveTo, "effective_to")
		if err != nil {
			return nil, err
		}
		to = &t
	}
	// Apply sensible defaults at the usecase edge so HTTP callers
	// that don't pass rates still produce a valid PKP profile.
	if in.PPNRate == 0 && in.IsPKP {
		in.PPNRate = 0.11
	}
	if in.PPh23Rate == 0 {
		in.PPh23Rate = 0.02
	}
	p, err := domain.NewCompanyTaxProfile(
		in.SubsidiaryID,
		in.Name, in.NPWP,
		in.IsPKP,
		in.PPNRate, in.PPh23Rate, in.PPhFinalRate,
		from, to,
	)
	if err != nil {
		return nil, err
	}
	if err := s.profiles.Create(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}

// =====================================================================
// Faktur lifecycle
// =====================================================================

// IssueFakturForInvoice creates a Draft FakturPajak record. Does NOT
// call DJP — that's SubmitFaktur's job. Splitting the two lets the
// operator review the draft (and edit DPP/PPN if needed) before
// committing to a DJP-issued nomor_seri.
func (s *Service) IssueFakturForInvoice(ctx context.Context, in port.IssueFakturInput) (*domain.FakturPajak, error) {
	jenis := domain.JenisFaktur(in.JenisFaktur)
	if jenis == "" {
		jenis = domain.JenisFakturStandard
	}
	f, err := domain.NewDraftFaktur(
		in.InvoiceID, in.SubsidiaryID, jenis,
		in.NPWPLawan, in.DPP, in.PPN,
	)
	if err != nil {
		return nil, err
	}
	if err := s.fakturs.Create(ctx, f); err != nil {
		return nil, err
	}
	return f, nil
}

// SubmitFaktur calls DJPGateway.IssueFaktur, persists the returned
// nomor_seri + payload, and flips the lifecycle to Submitted.
//
// When the DJP gateway is nil we return a clear "not configured"
// error rather than panicking — this keeps the wave wireable even
// in deployments without DJP credentials.
func (s *Service) SubmitFaktur(ctx context.Context, fakturID uuid.UUID) (*domain.FakturPajak, error) {
	if s.djp == nil {
		return nil, derrors.New(derrors.KindUnavailable,
			"djp.not_configured",
			"DJP gateway is not configured in this deployment",
		)
	}
	f, err := s.fakturs.FindByID(ctx, fakturID)
	if err != nil {
		return nil, err
	}
	if f.Status != domain.FakturStatusDraft {
		return nil, derrors.Conflict(
			"faktur.not_draft",
			"only Draft fakturs can be submitted to DJP",
		)
	}
	nomorSeri, payload, err := s.djp.IssueFaktur(ctx, f)
	if err != nil {
		// Let the error bubble — the DJP stub returns KindUnavailable
		// already, and a real DJP failure should propagate untouched so
		// the operator sees the underlying reason.
		return nil, err
	}
	if err := f.MarkSubmitted(nomorSeri, payload); err != nil {
		return nil, err
	}
	if err := s.fakturs.UpdateStatus(ctx, f); err != nil {
		return nil, err
	}
	return f, nil
}

// GetFaktur fetches one faktur by id.
func (s *Service) GetFaktur(ctx context.Context, id uuid.UUID) (*domain.FakturPajak, error) {
	return s.fakturs.FindByID(ctx, id)
}

// =====================================================================
// Helpers
// =====================================================================

// parseDate parses YYYY-MM-DD with a typed validation error on bad
// input. The HTTP layer maps validation errors to 400.
func parseDate(s, field string) (time.Time, error) {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}, derrors.Validation(
			"tax."+field+"_invalid",
			field+" must be a YYYY-MM-DD date",
		)
	}
	return t.UTC(), nil
}
