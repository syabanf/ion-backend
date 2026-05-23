package usecase

import (
	"context"

	"github.com/ion-core/backend/internal/payment/domain"
	"github.com/ion-core/backend/internal/payment/port"
)

// GatewayService is the read-only admin surface over the gateway
// registry. Wave 111 keeps writes (CRUD) on the migration / seed
// side — operators add new gateways by re-running the seed. A future
// wave promotes this to full CRUD.
type GatewayService struct {
	gateways port.PaymentGatewayRepository
}

func NewGatewayService(gateways port.PaymentGatewayRepository) *GatewayService {
	return &GatewayService{gateways: gateways}
}

var _ port.GatewayUseCase = (*GatewayService)(nil)

func (s *GatewayService) ListGateways(ctx context.Context, onlyActive bool) ([]domain.PaymentGateway, error) {
	if onlyActive {
		return s.gateways.ListActive(ctx)
	}
	return s.gateways.ListAll(ctx)
}

func (s *GatewayService) GetGatewayByCode(ctx context.Context, code string) (*domain.PaymentGateway, error) {
	return s.gateways.FindByCode(ctx, code)
}
