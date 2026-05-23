// Wave 117 — Item category management service.
package usecase

import (
	"context"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/warehouse/domain"
	"github.com/ion-core/backend/internal/warehouse/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// WithItemCategories attaches the category repo. Without this the
// category endpoints surface a clean "not configured" error.
func (s *Service) WithItemCategories(r port.ItemCategoryRepository) *Service {
	s.itemCategories = r
	return s
}

func errItemCategoriesNotConfigured() error {
	return derrors.Wrap(derrors.KindInternal, "item_category.not_configured",
		"item category repository is not configured for this service", nil)
}

func (s *Service) CreateItemCategory(ctx context.Context, in port.CreateItemCategoryInput) (*domain.ItemCategoryDef, error) {
	if s.itemCategories == nil {
		return nil, errItemCategoriesNotConfigured()
	}
	// Guard against duplicate codes pre-DB for a cleaner error code.
	if existing, err := s.itemCategories.FindByCode(ctx, in.Code); err == nil && existing != nil {
		return nil, derrors.Conflict("item_category.code_taken", "code already in use")
	}
	cat, err := domain.NewItemCategoryDef(in.Code, in.Name, in.TypeCode)
	if err != nil {
		return nil, err
	}
	cat.ParentID = in.ParentID
	cat.Description = in.Description
	if in.DefaultUnit != "" {
		cat.DefaultUnit = in.DefaultUnit
	}
	if in.SubWarehouseAllowedDefault != nil {
		cat.SubWarehouseAllowedDefault = *in.SubWarehouseAllowedDefault
	}
	if in.RequiresSerialAtIntake != nil {
		cat.RequiresSerialAtIntake = *in.RequiresSerialAtIntake
	}
	if err := s.itemCategories.Create(ctx, cat); err != nil {
		return nil, err
	}
	s.auditf(ctx, "item_category.create", "category=%s type=%s", cat.Code, cat.TypeCode)
	return cat, nil
}

func (s *Service) GetItemCategory(ctx context.Context, id uuid.UUID) (*domain.ItemCategoryDef, error) {
	if s.itemCategories == nil {
		return nil, errItemCategoriesNotConfigured()
	}
	return s.itemCategories.FindByID(ctx, id)
}

func (s *Service) ListItemCategories(ctx context.Context, f port.ItemCategoryListFilter) ([]domain.ItemCategoryDef, error) {
	if s.itemCategories == nil {
		return nil, errItemCategoriesNotConfigured()
	}
	return s.itemCategories.List(ctx, f)
}

func (s *Service) UpdateItemCategory(ctx context.Context, in port.UpdateItemCategoryInput) (*domain.ItemCategoryDef, error) {
	if s.itemCategories == nil {
		return nil, errItemCategoriesNotConfigured()
	}
	out, err := s.itemCategories.Update(ctx, in)
	if err != nil {
		return nil, err
	}
	s.auditf(ctx, "item_category.update", "id=%s", out.ID)
	return out, nil
}
