// Package qr provides a deterministic in-process QRCodeGenerator that
// wraps domain.GenerateQR / domain.ParseQR. Live wiring uses this; tests
// can swap a mock for asserting payload contents.
package qr

import (
	"github.com/ion-core/backend/internal/warehouse/domain"
	"github.com/ion-core/backend/internal/warehouse/port"
)

type DeterministicGenerator struct{}

func New() *DeterministicGenerator { return &DeterministicGenerator{} }

var _ port.QRCodeGenerator = (*DeterministicGenerator)(nil)

func (g *DeterministicGenerator) Generate(in port.QRGenerateInput) string {
	return domain.GenerateQR(domain.NewQRSource(in.ItemType, in.ItemID, in.Serial))
}

func (g *DeterministicGenerator) Parse(scanned string) (*domain.QRPayload, error) {
	return domain.ParseQR(scanned)
}
