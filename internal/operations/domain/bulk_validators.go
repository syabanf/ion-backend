// Bulk validators — pre-flight checks that run during the 'validating'
// state transition. Each validator returns the reason for failure; an
// empty string means the item is fit to apply.
//
// Validators are stateless functions so the BulkExecutorService can call
// them on the same item from multiple goroutines (concurrency-limited
// worker pool). They consult the cross-context bridges via narrow
// inspection ports, not via SQL — keeping the domain layer free of
// adapter imports.
package domain

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// PlanChangeValidatorPort — narrow inspector the validator depends on.
// Implemented by the crm bridge.
type PlanChangeValidatorPort interface {
	// PlanExists returns true if a plan row with this id exists in the
	// crm catalog (crm.products).
	PlanExists(ctx context.Context, planID uuid.UUID) (bool, error)
	// CustomerCurrentPlan returns the customer's currently-active plan id
	// (nil if there isn't one) and the customer status. Returns
	// (nil, "", nil) when the customer can't be resolved at all.
	CustomerCurrentPlan(ctx context.Context, customerID uuid.UUID) (planID *uuid.UUID, status string, err error)
}

// ValidatePlanChangeItem runs the plan-change pre-flight checks. Empty
// return value = OK; non-empty = the rejection reason that should be
// captured on the item.
//
//	target plan must exist
//	customer must not be terminated
//	if customer's current plan == target plan → skip (no-op)
func ValidatePlanChangeItem(ctx context.Context, p PlanChangeValidatorPort, item *BulkPlanChangeItem) (reason string, skip bool, err error) {
	if p == nil {
		return "", false, nil
	}
	exists, err := p.PlanExists(ctx, item.TargetPlanID)
	if err != nil {
		return "", false, err
	}
	if !exists {
		return "target_plan_not_found", false, nil
	}
	current, status, err := p.CustomerCurrentPlan(ctx, item.CustomerID)
	if err != nil {
		return "", false, err
	}
	if status == "terminated" {
		return "customer_terminated", false, nil
	}
	if current != nil && *current == item.TargetPlanID {
		return "customer_already_on_target_plan", true, nil
	}
	return "", false, nil
}

// ODPMigrationValidatorPort — narrow inspector for the ODP migration
// pre-flight. Implemented by the network bridge.
type ODPMigrationValidatorPort interface {
	// PortHasCapacity returns true if the destination port can accept one
	// more customer. The bridge computes max_capacity - active_connections.
	PortHasCapacity(ctx context.Context, portID uuid.UUID) (bool, error)
	// WindowOverlapsMaintenance returns true if any planned maintenance
	// event overlaps the requested window. Implementations may return
	// (false, nil) if no maintenance schema is installed yet.
	WindowOverlapsMaintenance(ctx context.Context, portID uuid.UUID, start, end time.Time) (bool, error)
}

// ValidateODPMigrationItem runs the ODP-migration pre-flight checks.
func ValidateODPMigrationItem(ctx context.Context, p ODPMigrationValidatorPort, item *BulkODPMigrationItem) (reason string, err error) {
	if p == nil {
		return "", nil
	}
	ok, err := p.PortHasCapacity(ctx, item.ToOLTPortID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "destination_port_no_capacity", nil
	}
	if item.ScheduledWindowStart != nil && item.ScheduledWindowEnd != nil {
		overlap, err := p.WindowOverlapsMaintenance(ctx, item.ToOLTPortID,
			*item.ScheduledWindowStart, *item.ScheduledWindowEnd)
		if err != nil {
			return "", err
		}
		if overlap {
			return "window_overlaps_maintenance", nil
		}
	}
	return "", nil
}

// WOCreationValidatorPort — narrow inspector for the WO creation pre-
// flight. Implemented by the field bridge.
type WOCreationValidatorPort interface {
	// WOTemplateExists returns true if a wo_template row with this id is
	// defined. Returns (true, nil) if templateID is nil (the executor
	// can synthesise a WO without a template).
	WOTemplateExists(ctx context.Context, templateID *uuid.UUID) (bool, error)
	// CustomerHasOpenWOOfType returns true if the customer already has a
	// non-terminal WO of the same type — the bulk creator should treat
	// it as duplicate, not failure.
	CustomerHasOpenWOOfType(ctx context.Context, customerID uuid.UUID, woType string) (bool, error)
}

// ValidateWOCreationItem runs the WO-creation pre-flight checks. The
// `duplicate` flag distinguishes a real failure from a "skip because
// there's already an open WO" case.
func ValidateWOCreationItem(ctx context.Context, p WOCreationValidatorPort, item *BulkWOCreationItem) (reason string, duplicate bool, err error) {
	if p == nil {
		return "", false, nil
	}
	exists, err := p.WOTemplateExists(ctx, item.WOTemplateID)
	if err != nil {
		return "", false, err
	}
	if !exists {
		return "wo_template_not_found", false, nil
	}
	if item.WOType != "" {
		open, err := p.CustomerHasOpenWOOfType(ctx, item.CustomerID, item.WOType)
		if err != nil {
			return "", false, err
		}
		if open {
			return "customer_has_open_wo_of_type", true, nil
		}
	}
	return "", false, nil
}
