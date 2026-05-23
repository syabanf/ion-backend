// Wave 89 (Tier 3) — Product BOM template usecase.
package usecase

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/warehouse/domain"
	"github.com/ion-core/backend/internal/warehouse/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

func (s *Service) WithBOMTemplates(r port.ProductBOMTemplateRepository) *Service {
	s.bomTemplates = r
	return s
}

func errBOMNotConfigured() error {
	return derrors.Wrap(derrors.KindInternal, "bom.not_configured",
		"BOM templates are not configured for this service", nil)
}

// CreateBOMTemplate creates a new template + lines. The partial
// unique index on (product_id) WHERE active=TRUE enforces "at most
// one active template per product" at the DB layer — callers should
// deactivate the existing template (if any) before creating a new
// active one. We don't auto-deactivate here because doing so silently
// would surprise admins; the dashboard does the explicit deactivate
// + create flow.
func (s *Service) CreateBOMTemplate(
	ctx context.Context, in port.CreateBOMTemplateInput,
) (*port.BOMTemplateDetail, error) {
	if s.bomTemplates == nil {
		return nil, errBOMNotConfigured()
	}
	now := time.Now().UTC()
	tpl, items, err := domain.NewProductBOMTemplate(
		in.ProductID, in.Name, in.Description, in.Items, in.CreatedBy, now,
	)
	if err != nil {
		return nil, err
	}
	if err := s.bomTemplates.Create(ctx, tpl, items); err != nil {
		return nil, err
	}
	return &port.BOMTemplateDetail{Template: *tpl, Items: items}, nil
}

func (s *Service) GetBOMTemplate(ctx context.Context, id uuid.UUID) (*port.BOMTemplateDetail, error) {
	if s.bomTemplates == nil {
		return nil, errBOMNotConfigured()
	}
	return s.bomTemplates.FindByID(ctx, id)
}

func (s *Service) GetActiveBOMTemplateForProduct(
	ctx context.Context, productID uuid.UUID,
) (*port.BOMTemplateDetail, error) {
	if s.bomTemplates == nil {
		return nil, errBOMNotConfigured()
	}
	return s.bomTemplates.FindActiveForProduct(ctx, productID)
}

func (s *Service) ListBOMTemplatesForProduct(
	ctx context.Context, productID uuid.UUID, activeOnly bool,
) ([]domain.ProductBOMTemplate, error) {
	if s.bomTemplates == nil {
		return nil, errBOMNotConfigured()
	}
	return s.bomTemplates.ListForProduct(ctx, productID, activeOnly)
}

func (s *Service) DeactivateBOMTemplate(ctx context.Context, id uuid.UUID) error {
	if s.bomTemplates == nil {
		return errBOMNotConfigured()
	}
	return s.bomTemplates.Deactivate(ctx, id)
}
