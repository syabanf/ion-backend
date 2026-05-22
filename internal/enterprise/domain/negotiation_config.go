package domain

import (
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// NegotiationConfig is the BOQ-attached configuration block that drives
// the negotiation lifecycle. Lives on `boq_versions.negotiation_*`
// columns + a side table for participants.
//
// Per CPQ TC-NEG-002, the config is mutable while the BOQ is draft
// and immutable once the BOQ is approved (we stamp `LockedAt` on the
// approval transition).
type NegotiationConfig struct {
	BOQVersionID             uuid.UUID
	Enabled                  bool
	Type                     string // 'standard' | 'custom'
	Mode                     ApprovalMode
	PricingAdjustmentAllowed bool
	MarginFloorPct           float64
	DiscountCeilingPct       float64
	LockedAt                 *time.Time
	Participants             []NegotiationParticipant
}

// NegotiationParticipant pins a user + role to a step of the chain.
// `RoleTag` carries the human label that drives the NG-1/NG-2 logic
// — "vp_sales" must appear when PricingAdjustmentAllowed=true; "cco"
// is the auto-inject target on guardrail breach.
type NegotiationParticipant struct {
	ID            uuid.UUID
	BOQVersionID  uuid.UUID
	UserID        uuid.UUID
	StepNo        int
	RoleTag       string
	CreatedAt     time.Time
}

// ValidateConfig enforces NG-1 (VP required when pricing adjustment
// is allowed) plus structural sanity checks. Called by the usecase
// before persistence.
func ValidateConfig(c *NegotiationConfig) error {
	if !c.Enabled {
		// Disabled config — no further checks; we accept whatever
		// the operator stashed in case they want to enable later.
		return nil
	}
	if c.Type != "standard" && c.Type != "custom" {
		return errors.Validation(
			"negotiation_config.type_invalid",
			"type must be 'standard' or 'custom'",
		)
	}
	if c.Mode != ApprovalModeSequential && c.Mode != ApprovalModeParallel {
		return errors.Validation(
			"negotiation_config.mode_invalid",
			"mode must be 'sequential' or 'parallel'",
		)
	}
	if c.MarginFloorPct < 0 || c.MarginFloorPct >= 100 {
		return errors.Validation(
			"negotiation_config.margin_floor_invalid",
			"margin_floor_pct must be in [0, 100)",
		)
	}
	if c.DiscountCeilingPct < 0 || c.DiscountCeilingPct > 100 {
		return errors.Validation(
			"negotiation_config.discount_ceiling_invalid",
			"discount_ceiling_pct must be in [0, 100]",
		)
	}
	if len(c.Participants) == 0 {
		return errors.Validation(
			"negotiation_config.participants_required",
			"at least one participant is required",
		)
	}
	if c.PricingAdjustmentAllowed {
		// NG-1: VP must be present in the chain when pricing
		// adjustment is allowed. The role_tag is the gate.
		hasVP := false
		for _, p := range c.Participants {
			if p.RoleTag == "vp_sales" {
				hasVP = true
				break
			}
		}
		if !hasVP {
			return errors.Validation(
				"negotiation_config.vp_required_for_pricing_adjustment",
				"a vp_sales participant is required when pricing_adjustment_allowed=true",
			)
		}
	}
	return nil
}

// EnsureMutable rejects edits after the config has been locked
// (TC-NEG-002 → HTTP 409 negotiation_config_locked).
func EnsureMutable(c *NegotiationConfig) error {
	if c.LockedAt != nil {
		return errors.Conflict(
			"negotiation_config.locked",
			"config is immutable after BOQ approval — config locked at "+c.LockedAt.Format(time.RFC3339),
		)
	}
	return nil
}

// FindVP returns the participant tagged `vp_sales`, or nil if none.
// The caller checks this when applying NG-2 (VP-only price edit).
func (c *NegotiationConfig) FindVP() *NegotiationParticipant {
	for i := range c.Participants {
		if c.Participants[i].RoleTag == "vp_sales" {
			return &c.Participants[i]
		}
	}
	return nil
}

// FindCCO returns the participant tagged `cco`, or nil if none.
// Used during auto-inject: if the chain already includes CCO, no
// extra step is added; if not, NG-3 appends one (handled in usecase).
func (c *NegotiationConfig) FindCCO() *NegotiationParticipant {
	for i := range c.Participants {
		if c.Participants[i].RoleTag == "cco" {
			return &c.Participants[i]
		}
	}
	return nil
}

// LastStepNo returns the highest step_no in the chain — used to
// compute the position of an auto-injected CCO step.
func (c *NegotiationConfig) LastStepNo() int {
	max := 0
	for _, p := range c.Participants {
		if p.StepNo > max {
			max = p.StepNo
		}
	}
	return max
}
