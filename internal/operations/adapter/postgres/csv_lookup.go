package postgres

import (
	"context"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/operations/port"
)

// CompositeCSVLookup wires together the three bridge-side lookup
// methods (customer + plan from crm, port from network, template from
// field) into the single CSVLookupPort the importer depends on.
//
// Each leg is optional — a nil leg returns (nil, nil) so the importer
// surfaces a per-row "not found" error rather than crashing.
type CompositeCSVLookup struct {
	CustomerAndPlan customerPlanLookup
	Port            portLookup
	Template        templateLookup
}

type customerPlanLookup interface {
	CustomerIDByNumber(ctx context.Context, customerNo string) (*uuid.UUID, error)
	PlanIDByCode(ctx context.Context, code string) (*uuid.UUID, error)
}

type portLookup interface {
	PortIDByCode(ctx context.Context, code string) (*uuid.UUID, error)
}

type templateLookup interface {
	WOTemplateIDByCode(ctx context.Context, code string) (*uuid.UUID, error)
}

var _ port.CSVLookupPort = (*CompositeCSVLookup)(nil)

func (c *CompositeCSVLookup) CustomerIDByNumber(ctx context.Context, no string) (*uuid.UUID, error) {
	if c.CustomerAndPlan == nil {
		return nil, nil
	}
	return c.CustomerAndPlan.CustomerIDByNumber(ctx, no)
}

func (c *CompositeCSVLookup) PlanIDByCode(ctx context.Context, code string) (*uuid.UUID, error) {
	if c.CustomerAndPlan == nil {
		return nil, nil
	}
	return c.CustomerAndPlan.PlanIDByCode(ctx, code)
}

func (c *CompositeCSVLookup) PortIDByCode(ctx context.Context, code string) (*uuid.UUID, error) {
	if c.Port == nil {
		return nil, nil
	}
	return c.Port.PortIDByCode(ctx, code)
}

func (c *CompositeCSVLookup) WOTemplateIDByCode(ctx context.Context, code string) (*uuid.UUID, error) {
	if c.Template == nil {
		return nil, nil
	}
	return c.Template.WOTemplateIDByCode(ctx, code)
}
