// Package djp is the driven adapter for the Indonesian DJP e-Faktur
// integration.
//
// Wave 93 ships a STUB only — the real adapter (HTTP client, signing,
// retries, response parsing) lands in a later wave. The stub exists
// so the rest of the wave's wiring is exercised end-to-end and
// downstream code paths (SubmitFaktur) compile + return a meaningful
// error in dev / smoke tests.
//
// Swapping the stub for the real client is a single line in cmd/
// — neither the usecase nor the domain layer changes.
package djp

import (
	"context"

	"github.com/ion-core/backend/internal/tax/domain"
	"github.com/ion-core/backend/internal/tax/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// StubGateway implements port.DJPGateway by returning
// "not implemented" errors. Use it as a placeholder until the real
// DJP HTTP client is wired up.
type StubGateway struct{}

// NewStubGateway returns the no-op DJP gateway. Allocation-free; the
// struct holds no state so callers may share one instance across all
// goroutines.
func NewStubGateway() *StubGateway { return &StubGateway{} }

// Compile-time assertion.
var _ port.DJPGateway = (*StubGateway)(nil)

// IssueFaktur signals that the DJP integration is not yet wired. The
// usecase surfaces this directly to the HTTP layer so operators see a
// 503 with a clear code rather than a misleading 500.
//
// We use KindUnavailable (maps to HTTP 503 in the adapter) instead of
// inventing a new kind — pkg/errors does not currently have a
// dedicated NotImplemented Kind. The error code "djp.scaffold" is
// stable + machine-readable so the frontend can detect this state.
func (StubGateway) IssueFaktur(ctx context.Context, f *domain.FakturPajak) (string, []byte, error) {
	return "", nil, derrors.New(
		derrors.KindUnavailable,
		"djp.scaffold",
		"DJP integration not yet wired — Wave 93 ships the scaffold only",
	)
}

// CheckStatus — same scaffold-only behavior as IssueFaktur.
func (StubGateway) CheckStatus(ctx context.Context, nomorSeri string) (string, []byte, error) {
	return "", nil, derrors.New(
		derrors.KindUnavailable,
		"djp.scaffold",
		"DJP integration not yet wired — Wave 93 ships the scaffold only",
	)
}
