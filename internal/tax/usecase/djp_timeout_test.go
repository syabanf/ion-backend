package usecase

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/tax/domain"
	"github.com/ion-core/backend/internal/tax/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// Wave 108 — Edge #19: Faktur DJP submission timeout
//
// When the DJP gateway returns a context-deadline error, SubmitFaktur
// must:
//   - leave the FakturPajak in Draft (no state flip)
//   - propagate the underlying error so the operator sees the real
//     reason (timeout vs. validation vs. network)
//
// The state preservation is the load-bearing invariant: a Submitted
// faktur with no DJP-issued nomor_seri would be unreconcilable in the
// production faktur ledger. The contract is "fail loud, don't
// half-commit".
//
// Audit-row policy: per the audit doc, a submission failure should
// also produce an `enterprise.faktur_pajak / djp_submit_failed` audit
// row carrying the error message. Today the tax usecase doesn't take an
// audit.Writer (single-shot service for Wave 93 scaffold; audit lands
// in the DJP real-client wave). The test therefore pins only the
// state-preservation + error-propagation contract — once the audit
// writer is wired the test gets a second assertion block.
// =====================================================================

// ----- fakes ---------------------------------------------------------

type timeoutDJPGateway struct {
	calls int
}

var _ port.DJPGateway = (*timeoutDJPGateway)(nil)

func (g *timeoutDJPGateway) IssueFaktur(_ context.Context, _ *domain.FakturPajak) (string, []byte, error) {
	g.calls++
	// Simulate the canonical DJP-side timeout: the gateway wraps the
	// underlying context.DeadlineExceeded with a typed error so the
	// caller (usecase) sees the same shape whether the timeout fires
	// during the TCP connect, the TLS handshake, or the request body
	// write.
	return "", nil, derrors.Wrap(
		derrors.KindUnavailable,
		"djp.timeout",
		"djp gateway timed out",
		context.DeadlineExceeded,
	)
}

func (g *timeoutDJPGateway) CheckStatus(_ context.Context, _ string) (string, []byte, error) {
	return "", nil, derrors.New(
		derrors.KindUnavailable,
		"djp.scaffold",
		"not used by submit test",
	)
}

type stubFakturRepoForTimeout struct {
	row           *domain.FakturPajak
	updateCalled  bool
	updateStatus  domain.FakturStatus
}

var _ port.FakturPajakRepository = (*stubFakturRepoForTimeout)(nil)

func (r *stubFakturRepoForTimeout) Create(_ context.Context, _ *domain.FakturPajak) error {
	return nil
}

func (r *stubFakturRepoForTimeout) FindByID(_ context.Context, _ uuid.UUID) (*domain.FakturPajak, error) {
	return r.row, nil
}

func (r *stubFakturRepoForTimeout) UpdateStatus(_ context.Context, f *domain.FakturPajak) error {
	r.updateCalled = true
	r.updateStatus = f.Status
	return nil
}

func (r *stubFakturRepoForTimeout) FindByInvoice(_ context.Context, _ uuid.UUID) ([]domain.FakturPajak, error) {
	return nil, nil
}

type stubProfileRepoForTimeout struct{}

var _ port.CompanyTaxProfileRepository = (*stubProfileRepoForTimeout)(nil)

func (r *stubProfileRepoForTimeout) Create(_ context.Context, _ *domain.CompanyTaxProfile) error {
	return nil
}

func (r *stubProfileRepoForTimeout) FindByID(_ context.Context, _ uuid.UUID) (*domain.CompanyTaxProfile, error) {
	return nil, derrors.New(derrors.KindNotFound, "tax_profile.not_found", "n/a")
}

func (r *stubProfileRepoForTimeout) FindActiveBySubsidiary(_ context.Context, _ uuid.UUID, _ time.Time) (*domain.CompanyTaxProfile, error) {
	return nil, derrors.New(derrors.KindNotFound, "tax_profile.not_found", "n/a")
}

func (r *stubProfileRepoForTimeout) Update(_ context.Context, _ *domain.CompanyTaxProfile) error {
	return nil
}

func (r *stubProfileRepoForTimeout) List(_ context.Context, _ port.CompanyTaxProfileFilter) ([]domain.CompanyTaxProfile, int, error) {
	return nil, 0, nil
}

// ----- tests ---------------------------------------------------------

func TestSubmitFaktur_DJPTimeout_LeavesFakturInDraft(t *testing.T) {
	// Build a Draft faktur — the only legal source state for
	// SubmitFaktur per the lifecycle in faktur_pajak.go::TransitionTo.
	f, err := domain.NewDraftFaktur(
		uuid.New(), uuid.New(),
		domain.JenisFakturStandard,
		"01.234.567.8-901.000",
		1_000_000, 110_000,
	)
	if err != nil {
		t.Fatalf("NewDraftFaktur: %v", err)
	}
	if f.Status != domain.FakturStatusDraft {
		t.Fatalf("setup status = %q, want draft", f.Status)
	}

	repo := &stubFakturRepoForTimeout{row: f}
	gw := &timeoutDJPGateway{}
	svc := NewService(&stubProfileRepoForTimeout{}, repo, gw, nil)

	_, err = svc.SubmitFaktur(context.Background(), f.ID)
	if err == nil {
		t.Fatal("SubmitFaktur on timeout must return the gateway error")
	}

	// Error propagation: the operator should see the timeout reason, not
	// a generic "faktur.illegal_transition". The KindUnavailable + the
	// underlying DeadlineExceeded must reach the caller.
	var de *derrors.Error
	if !errors.As(err, &de) {
		t.Fatalf("err type: %T %v", err, err)
	}
	if de.Kind != derrors.KindUnavailable {
		t.Errorf("kind = %v, want Unavailable", de.Kind)
	}
	if de.Code != "djp.timeout" {
		t.Errorf("code = %q, want djp.timeout", de.Code)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("wrapped err should match context.DeadlineExceeded; got %v", err)
	}

	// State preservation: no Update should have fired on the repo, AND
	// the in-memory row must stay Draft (no flip during the failure).
	if repo.updateCalled {
		t.Error("UpdateStatus must NOT be called on a timeout — faktur must stay Draft")
	}
	if f.Status != domain.FakturStatusDraft {
		t.Errorf("faktur status after timeout = %q, want draft", f.Status)
	}
	if gw.calls != 1 {
		t.Errorf("gateway IssueFaktur calls = %d, want exactly 1 (no silent retry)", gw.calls)
	}
}
