package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// OpportunityStage is the CPQ MVP sales pipeline progression. Forward
// only — backwards transitions return an `invalid_state_transition`
// conflict (CPQ TC-SM-OPP-004/005/006).
//
//	cold → warm → hot → won
//	            ↓     ↓
//	           lost  lost  (terminal; reason required)
type OpportunityStage string

const (
	OpportunityStageCold OpportunityStage = "cold"
	OpportunityStageWarm OpportunityStage = "warm"
	OpportunityStageHot  OpportunityStage = "hot"
	OpportunityStageWon  OpportunityStage = "won"
	OpportunityStageLost OpportunityStage = "lost"
)

// OpportunitySubstage adds finer-grained tracking within a stage —
// principally for `hot` (awaiting PO / PO under validation). We keep
// it as a string + CHECK constraint so substages can be added without
// migrating the enum.
type OpportunitySubstage string

const (
	OpportunitySubstageNone           OpportunitySubstage = ""
	OpportunitySubstageAwaitingPO     OpportunitySubstage = "awaiting_po"
	OpportunitySubstagePOValidation   OpportunitySubstage = "po_validation"
	OpportunitySubstageArchived       OpportunitySubstage = "archived"
)

// OpportunitySource matches the PRD §6.3 source enum, shared with CRM
// leads (broadband). Kept independent of crm.LeadSource because the
// enterprise context will become its own service — duplicating the
// short string set is cheaper than cross-context coupling.
type OpportunitySource string

const (
	OpportunitySourceManual         OpportunitySource = "manual"
	OpportunitySourceReferral       OpportunitySource = "referral"
	OpportunitySourceColdCall       OpportunitySource = "cold_call"
	OpportunitySourceWebsite        OpportunitySource = "website"
	OpportunitySourceWhatsApp       OpportunitySource = "whatsapp"
	OpportunitySourceSocialMediaDM  OpportunitySource = "social_media_dm"
	OpportunitySourceVoIPCall       OpportunitySource = "voip_call"
	OpportunitySourceLineCall       OpportunitySource = "line_call"
	OpportunitySourceWalkIn         OpportunitySource = "walk_in"
	OpportunitySourceEvent          OpportunitySource = "event"
	OpportunitySourcePartner        OpportunitySource = "partner"
	OpportunitySourceCSReferral     OpportunitySource = "cs_referral"
)

// LostReasonCode is the closed enum of reasons accepted by the
// `MarkLost` transition. CPQ TC-OP-003 requires a reason; we go one
// step further and require a categorical code in addition to the
// free-text `lost_reason` for analytics.
type LostReasonCode string

const (
	LostReasonNone              LostReasonCode = ""
	LostReasonPrice             LostReasonCode = "price"
	LostReasonFeatureGap        LostReasonCode = "feature_gap"
	LostReasonUnreachable       LostReasonCode = "unreachable"
	LostReasonCompetitor        LostReasonCode = "competitor"
	LostReasonProjectCancelled  LostReasonCode = "project_cancelled"
	LostReasonStageTimeout      LostReasonCode = "stage_timeout"
	LostReasonOther             LostReasonCode = "other"
)

// Opportunity is the enterprise pipeline entity. Roughly: "a deal in
// progress" — distinct from a Customer record (which is what the deal
// produces if it closes).
type Opportunity struct {
	ID                  uuid.UUID
	OpportunityNumber   string
	// Account
	CustomerID          *uuid.UUID // set when convertible / converted
	AccountName         string
	AccountIndustry     string
	AccountSize         string
	// PIC
	PICName, PICTitle, PICPhone, PICEmail string
	// Ownership
	OwnerUserID *uuid.UUID
	BranchID    *uuid.UUID
	// Stage
	Stage            OpportunityStage
	Substage         OpportunitySubstage
	// Commercial
	EstimatedValue   float64
	Currency         string
	ExpectedCloseAt  *time.Time
	PricebookID      *uuid.UUID
	// Source
	Source             OpportunitySource
	ReferrerCustomerID *uuid.UUID
	// Pre-BOQ — raw JSON snapshot per TC-OP-005
	PreBOQ                []byte
	PreBOQCompletedAt     *time.Time
	// SLA tracking
	StageEnteredAt   time.Time
	LastActivityAt   time.Time
	// Lost
	LostReasonCode LostReasonCode
	LostReason     string
	AutoLost       bool
	// Won
	WonAt        *time.Time
	POReference  string
	// Free text
	Notes string
	// Optimistic concurrency
	Revision int
	// Timestamps
	CreatedAt, UpdatedAt time.Time
}

// NewOpportunity constructs a Cold-stage opportunity with required
// fields validated. The opportunity_number is generated separately
// by the usecase (so the format / branch prefix can evolve without
// touching the domain).
func NewOpportunity(accountName string) (*Opportunity, error) {
	accountName = strings.TrimSpace(accountName)
	if accountName == "" {
		return nil, errors.Validation(
			"opportunity.account_name_required",
			"account_name is required",
		)
	}
	now := time.Now().UTC()
	return &Opportunity{
		ID:               uuid.New(),
		AccountName:      accountName,
		Stage:            OpportunityStageCold,
		Substage:         OpportunitySubstageNone,
		Currency:         "IDR",
		Source:           OpportunitySourceManual,
		PreBOQ:           []byte("{}"),
		StageEnteredAt:   now,
		LastActivityAt:   now,
		Revision:         1,
		CreatedAt:        now,
		UpdatedAt:        now,
	}, nil
}

// =====================================================================
// Stage transitions — forward only, gated by entry criteria
// =====================================================================

// AdvanceToWarm transitions a Cold opportunity to Warm. Per CPQ
// TC-SM-OPP-001 the entry criterion is "Pre-BOQ completed". We keep
// it permissive in the domain — the usecase decides whether to
// enforce checklist completeness based on Admin config (CPQ TC-OP-004
// → checklist_incomplete is config-driven, not hardcoded).
func (o *Opportunity) AdvanceToWarm() error {
	if o.Stage != OpportunityStageCold {
		return errors.Conflict(
			"opportunity.invalid_state_transition",
			"can only advance to warm from cold (current: "+string(o.Stage)+")",
		)
	}
	o.Stage = OpportunityStageWarm
	o.touchStage()
	return nil
}

// AdvanceToHot transitions a Warm opportunity to Hot. Substage is
// set to awaiting_po per CPQ TC-SM-OPP-002 — quotation has been
// issued, we're waiting for the customer's PO. Real quotation
// generation lands Phase 4; for now the substage is a hint.
func (o *Opportunity) AdvanceToHot() error {
	if o.Stage != OpportunityStageWarm {
		return errors.Conflict(
			"opportunity.invalid_state_transition",
			"can only advance to hot from warm (current: "+string(o.Stage)+")",
		)
	}
	o.Stage = OpportunityStageHot
	o.Substage = OpportunitySubstageAwaitingPO
	o.touchStage()
	return nil
}

// MarkWon transitions Hot → Won. CPQ TC-SM-OPP-003 entry criterion:
// "PO + checklist". The usecase enforces presence of `poRef`; the
// domain just requires it be non-empty and rejects backward / illegal
// transitions.
func (o *Opportunity) MarkWon(poRef string) error {
	poRef = strings.TrimSpace(poRef)
	if poRef == "" {
		return errors.Validation(
			"opportunity.po_reference_required",
			"po_reference is required to mark Won",
		)
	}
	if o.Stage != OpportunityStageHot {
		return errors.Conflict(
			"opportunity.invalid_state_transition",
			"can only mark won from hot (current: "+string(o.Stage)+")",
		)
	}
	now := time.Now().UTC()
	o.Stage = OpportunityStageWon
	o.Substage = OpportunitySubstagePOValidation
	o.POReference = poRef
	o.WonAt = &now
	o.touchStage()
	return nil
}

// MarkLost transitions any non-terminal stage to Lost. A categorical
// `code` AND a free-text `reason` are both required (CPQ TC-OP-003 +
// TC-AP-009-style "reason mandatory" pattern). `autoLost` toggles
// whether this was the SLA watchdog or a human action.
func (o *Opportunity) MarkLost(code LostReasonCode, reason string, autoLost bool) error {
	if o.Stage == OpportunityStageWon || o.Stage == OpportunityStageLost {
		return errors.Conflict(
			"opportunity.invalid_state_transition",
			"cannot mark lost from terminal stage "+string(o.Stage),
		)
	}
	reason = strings.TrimSpace(reason)
	if !isValidLostCode(code) || code == LostReasonNone {
		return errors.Validation(
			"opportunity.lost_reason_code_invalid",
			"lost_reason_code must be one of the documented values",
		)
	}
	if reason == "" {
		return errors.Validation(
			"opportunity.lost_reason_required",
			"a free-text reason is required when marking Lost",
		)
	}
	o.Stage = OpportunityStageLost
	o.Substage = OpportunitySubstageArchived
	o.LostReasonCode = code
	o.LostReason = reason
	o.AutoLost = autoLost
	o.touchStage()
	return nil
}

// CompletePreBOQ stamps the Pre-BOQ JSON snapshot + completion
// timestamp. Per CPQ TC-OP-005 this is a snapshot ON the opportunity,
// NOT a BOQVersion record. The shape of the JSON is intentionally
// loose at MVP and will be tightened in Phase 3.
func (o *Opportunity) CompletePreBOQ(snapshot []byte) error {
	if len(snapshot) == 0 {
		return errors.Validation(
			"opportunity.pre_boq_empty",
			"pre_boq snapshot must not be empty",
		)
	}
	now := time.Now().UTC()
	o.PreBOQ = snapshot
	o.PreBOQCompletedAt = &now
	o.touchActivity()
	return nil
}

// Reassign hands an opportunity from one owner to another (TC-OP-011).
// `prevOwner` captures the previous owner_user_id (nil-safe if the
// opportunity was unowned) so the audit row can record the rotation.
// Terminal stages reject reassignment — once Won/Lost the owner is
// historically frozen.
//
// Returns the previous owner so the usecase can log it without making
// a separate pre-read.
func (o *Opportunity) Reassign(newOwnerID uuid.UUID) (prevOwner *uuid.UUID, err error) {
	if newOwnerID == uuid.Nil {
		return nil, errors.Validation(
			"opportunity.reassign_new_owner_required",
			"new owner_user_id is required",
		)
	}
	if o.Stage == OpportunityStageWon || o.Stage == OpportunityStageLost {
		return nil, errors.Conflict(
			"opportunity.terminal",
			"cannot reassign a terminal opportunity",
		)
	}
	if o.OwnerUserID != nil && *o.OwnerUserID == newOwnerID {
		return o.OwnerUserID, errors.Validation(
			"opportunity.reassign_same_owner",
			"new owner is the same as the current owner",
		)
	}
	prev := o.OwnerUserID
	o.OwnerUserID = &newOwnerID
	o.touchActivity()
	return prev, nil
}

// PinPricebook locks a pricebook version onto the opportunity. The
// downstream BOQ (Phase 3) inherits this pin so Admin publishing a
// newer pricebook doesn't change the quoted prices. Once set,
// the pin is immutable — re-pinning would invalidate the deal's
// commercial commitments.
func (o *Opportunity) PinPricebook(pricebookID uuid.UUID) error {
	if o.PricebookID != nil && *o.PricebookID != pricebookID {
		return errors.Conflict(
			"opportunity.pricebook_already_pinned",
			"opportunity already pinned to a different pricebook version",
		)
	}
	o.PricebookID = &pricebookID
	o.touchActivity()
	return nil
}

// TouchActivity bumps last_activity_at — called by the usecase on any
// material write so the auto-Lost watchdog resets its window.
func (o *Opportunity) TouchActivity() {
	o.touchActivity()
}

// =====================================================================
// Auto-Lost SLA — windows per stage, per CPQ BR-9 / TC-OP-007 / OP-008
// =====================================================================

// AutoLostWindow returns the SLA window (since stage_entered_at) after
// which an opportunity in this stage is auto-Lost. Terminal stages
// return 0 (no window).
func (o *Opportunity) AutoLostWindow() time.Duration {
	switch o.Stage {
	case OpportunityStageCold:
		return 30 * 24 * time.Hour
	case OpportunityStageWarm:
		return 7 * 24 * time.Hour
	case OpportunityStageHot:
		return 3 * 24 * time.Hour
	default:
		return 0
	}
}

// IsAutoLostExpired returns true when the SLA window has elapsed and
// the opportunity should be flipped to Lost by the scheduler. Uses
// `LastActivityAt` (not `StageEnteredAt`) so a rep "touching" the
// opportunity (note added, PIC updated) resets the clock. Per CPQ
// TC-OP-008 the boundary is strict (T0+window+1 minute = expired).
func (o *Opportunity) IsAutoLostExpired(now time.Time) bool {
	window := o.AutoLostWindow()
	if window == 0 {
		return false
	}
	return now.Sub(o.LastActivityAt) > window
}

// =====================================================================
// Helpers
// =====================================================================

func (o *Opportunity) touchStage() {
	now := time.Now().UTC()
	o.StageEnteredAt = now
	o.LastActivityAt = now
	o.UpdatedAt = now
	o.Revision++
}

func (o *Opportunity) touchActivity() {
	now := time.Now().UTC()
	o.LastActivityAt = now
	o.UpdatedAt = now
	o.Revision++
}

func isValidLostCode(code LostReasonCode) bool {
	switch code {
	case LostReasonPrice,
		LostReasonFeatureGap,
		LostReasonUnreachable,
		LostReasonCompetitor,
		LostReasonProjectCancelled,
		LostReasonStageTimeout,
		LostReasonOther,
		LostReasonNone:
		return true
	}
	return false
}

// GenerateOpportunityNumber returns a sortable identifier in the form
// `OPP-YYYYMMDD-XXXXXXXX` (date prefix + 8 hex from a fresh UUID).
// Matches the broadband lead-number convention.
func GenerateOpportunityNumber(t time.Time) string {
	return "OPP-" + t.UTC().Format("20060102") + "-" + uuid.New().String()[:8]
}
