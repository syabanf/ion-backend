package domain

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// Negotiation round — one per VP price submission
// =====================================================================

type NegotiationRoundStatus string

const (
	NegotiationRoundPendingApproval NegotiationRoundStatus = "pending_approval"
	NegotiationRoundApproved        NegotiationRoundStatus = "approved"
	NegotiationRoundRejected        NegotiationRoundStatus = "rejected"
	NegotiationRoundSuperseded      NegotiationRoundStatus = "superseded"
)

// CCOInjectionReason captures why the auto-inject fired. Empty when
// no injection happened — keeps the audit query simple.
type CCOInjectionReason string

const (
	CCOInjectionNone             CCOInjectionReason = ""
	CCOInjectionMarginFloor      CCOInjectionReason = "margin_floor"
	CCOInjectionDiscountCeiling  CCOInjectionReason = "discount_ceiling"
)

// LinePriceChange is the per-line snapshot the round records. We
// keep `before_*` + `after_*` so the audit can show the exact delta
// without re-reading historical BOQ state.
type LinePriceChange struct {
	LineID             uuid.UUID `json:"line_id"`
	BeforeSell         float64   `json:"before_sell"`
	AfterSell          float64   `json:"after_sell"`
	BeforeDiscountPct  float64   `json:"before_discount_pct"`
	AfterDiscountPct   float64   `json:"after_discount_pct"`
}

type NegotiationRound struct {
	ID                   uuid.UUID
	NegotiationID        uuid.UUID
	RoundNo              int
	Status               NegotiationRoundStatus
	PriceChanges         []LinePriceChange
	MarginBefore         float64
	MarginAfter          float64
	MaxDiscountAfter     float64
	CCOAutoInjected      bool
	CCOInjectionReason   CCOInjectionReason
	SubmittedBy          *uuid.UUID
	SubmittedAt          time.Time
	CompletedAt          *time.Time
	RejectionReasonCode  RejectionReasonCode
	RejectionComment     string
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// NewNegotiationRound constructs a pending-approval round. The
// usecase computes margin_before/after + the auto-inject decision
// before persisting and passes them in.
func NewNegotiationRound(
	negotiationID uuid.UUID,
	roundNo int,
	changes []LinePriceChange,
	marginBefore, marginAfter, maxDiscountAfter float64,
	submittedBy uuid.UUID,
) (*NegotiationRound, error) {
	if negotiationID == uuid.Nil {
		return nil, errors.Validation("negotiation_round.negotiation_required", "negotiation_id is required")
	}
	if roundNo < 1 {
		return nil, errors.Validation("negotiation_round.round_no_invalid", "round_no must be >= 1")
	}
	if len(changes) == 0 {
		return nil, errors.Validation("negotiation_round.no_changes", "at least one price change is required")
	}
	now := time.Now().UTC()
	return &NegotiationRound{
		ID:               uuid.New(),
		NegotiationID:    negotiationID,
		RoundNo:          roundNo,
		Status:           NegotiationRoundPendingApproval,
		PriceChanges:     changes,
		MarginBefore:     marginBefore,
		MarginAfter:      marginAfter,
		MaxDiscountAfter: maxDiscountAfter,
		SubmittedBy:      &submittedBy,
		SubmittedAt:      now,
		CreatedAt:        now,
		UpdatedAt:        now,
	}, nil
}

// EvaluateCCOInjection decides whether the chain needs an extra CCO
// step. Returns the reason (empty string = no injection).
//
// Per NG-2 / NG-3 / Edge #6 the rules are:
//   - margin_after < margin_floor → injection_reason = "margin_floor"
//   - max_discount_after > discount_ceiling → "discount_ceiling"
//
// Boundary semantics: margin = floor exactly → PASS (no injection)
// per TC-NEG-009. Same boundary handling for discount.
func EvaluateCCOInjection(
	marginAfter, marginFloor float64,
	maxDiscountAfter, discountCeiling float64,
) CCOInjectionReason {
	const eps = 1e-9
	if marginAfter+eps < marginFloor {
		return CCOInjectionMarginFloor
	}
	if maxDiscountAfter-eps > discountCeiling {
		return CCOInjectionDiscountCeiling
	}
	return CCOInjectionNone
}

// ValidateMarginFloor is the per-round version of the BOQ margin-floor
// check. Used during VP submit AFTER the auto-inject decision: if no
// CCO is in the chain to override, the BE blocks the save with
// HTTP 422 margin_floor_violation (TC-NEG-008). When a CCO is present
// the save proceeds (the CCO becomes the gate).
//
// Boundary: margin = floor exactly → PASS (TC-NEG-009).
func ValidateMarginFloor(marginAfter, marginFloor float64) error {
	const eps = 1e-9
	if marginAfter+eps < marginFloor {
		return errors.Validation(
			"negotiation_round.margin_floor_violation",
			"projected margin is below the negotiation margin floor",
		)
	}
	return nil
}

// MarshalPriceChanges returns the jsonb representation the postgres
// adapter persists. Centralized so the marshaling rules (snake_case,
// no extra whitespace) stay in one place.
func (r *NegotiationRound) MarshalPriceChanges() ([]byte, error) {
	if r.PriceChanges == nil {
		return []byte("[]"), nil
	}
	b, err := json.Marshal(r.PriceChanges)
	if err != nil {
		return nil, errors.Wrap(errors.KindInternal, "negotiation_round.marshal_changes", "marshal price changes", err)
	}
	return b, nil
}

// UnmarshalPriceChanges hydrates the slice from the stored jsonb.
// Tolerant of NULL/empty (returns an empty slice rather than erroring
// — the audit trail might predate the column).
func UnmarshalPriceChanges(raw []byte) ([]LinePriceChange, error) {
	if len(raw) == 0 {
		return []LinePriceChange{}, nil
	}
	var out []LinePriceChange
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, errors.Wrap(errors.KindInternal, "negotiation_round.unmarshal_changes", "unmarshal price changes", err)
	}
	return out, nil
}

// MarkApproved flips the round to approved. Called when the round's
// approval chain finishes. Usecase is responsible for the side
// effects (apply price changes to BOQ lines, fire re-quote).
func (r *NegotiationRound) MarkApproved() error {
	if r.Status != NegotiationRoundPendingApproval {
		return errors.Conflict(
			"negotiation_round.invalid_state_transition",
			"can only approve from pending_approval",
		)
	}
	now := time.Now().UTC()
	r.Status = NegotiationRoundApproved
	r.CompletedAt = &now
	r.UpdatedAt = now
	return nil
}

// MarkRejected captures the reason. Mirrors BOQ rejection semantics.
func (r *NegotiationRound) MarkRejected(code RejectionReasonCode, comment string) error {
	if r.Status != NegotiationRoundPendingApproval {
		return errors.Conflict(
			"negotiation_round.invalid_state_transition",
			"can only reject from pending_approval",
		)
	}
	comment = strings.TrimSpace(comment)
	if comment == "" {
		return errors.Validation("negotiation_round.comment_required", "comment is required on rejection")
	}
	now := time.Now().UTC()
	r.Status = NegotiationRoundRejected
	r.CompletedAt = &now
	r.RejectionReasonCode = code
	r.RejectionComment = comment
	r.UpdatedAt = now
	return nil
}

// Supersede flips a pending round when the parent BOQ goes into
// revision (Edge #1) — audit retains the row but it no longer counts.
func (r *NegotiationRound) Supersede() {
	r.Status = NegotiationRoundSuperseded
	r.UpdatedAt = time.Now().UTC()
}

// =====================================================================
// Round approval instance — per-step approval row
// =====================================================================

type NegotiationRoundApproval struct {
	ID              uuid.UUID
	RoundID         uuid.UUID
	StepNo          int
	ApproverUserID  uuid.UUID
	RoleTag         string
	Status          ApprovalInstanceStatus  // reuse the same enum as BOQ approvals
	ReasonCode      ApprovalReasonCode
	Comment         string
	ActedAt         *time.Time
	ActedAtOriginal *time.Time
	AutoInjected    bool
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// NewNegotiationRoundApproval materializes a chain step in the
// pending state. Mirrors NewApprovalInstance but scoped to a round
// (cannot reuse the latter because the target table is different).
func NewNegotiationRoundApproval(
	roundID uuid.UUID,
	stepNo int,
	approverUserID uuid.UUID,
	roleTag string,
	autoInjected bool,
) (*NegotiationRoundApproval, error) {
	if roundID == uuid.Nil {
		return nil, errors.Validation("negotiation_approval.round_required", "round_id is required")
	}
	if approverUserID == uuid.Nil {
		return nil, errors.Validation("negotiation_approval.approver_required", "approver_user_id is required")
	}
	if stepNo < 1 {
		return nil, errors.Validation("negotiation_approval.step_invalid", "step_no must be >= 1")
	}
	now := time.Now().UTC()
	return &NegotiationRoundApproval{
		ID:             uuid.New(),
		RoundID:        roundID,
		StepNo:         stepNo,
		ApproverUserID: approverUserID,
		RoleTag:        roleTag,
		Status:         ApprovalInstanceStatusPending,
		AutoInjected:   autoInjected,
		CreatedAt:      now,
		UpdatedAt:      now,
	}, nil
}

// Approve transitions pending → approved. Mirrors the BOQ-approval
// version but on the round-approval table.
func (a *NegotiationRoundApproval) Approve(actor uuid.UUID) error {
	if a.ApproverUserID != actor {
		return errors.Forbidden(
			"negotiation_approval.not_assignee",
			"only the assigned approver can act on this step",
		)
	}
	switch a.Status {
	case ApprovalInstanceStatusPending:
		// fall through
	case ApprovalInstanceStatusApproved:
		return nil // idempotent
	default:
		return errors.Conflict(
			"negotiation_approval.invalid_state",
			"can only approve from pending (current: "+string(a.Status)+")",
		)
	}
	now := time.Now().UTC()
	a.Status = ApprovalInstanceStatusApproved
	a.ActedAt = &now
	a.UpdatedAt = now
	return nil
}

// Reject transitions pending → rejected. Reason + comment required
// to match BOQ rejection ergonomics (CPQ §4.8).
func (a *NegotiationRoundApproval) Reject(actor uuid.UUID, code ApprovalReasonCode, comment string) error {
	if a.ApproverUserID != actor {
		return errors.Forbidden(
			"negotiation_approval.not_assignee",
			"only the assigned approver can act on this step",
		)
	}
	if a.Status != ApprovalInstanceStatusPending {
		return errors.Conflict(
			"negotiation_approval.invalid_state",
			"can only reject from pending (current: "+string(a.Status)+")",
		)
	}
	if !IsValidApprovalReasonCode(code) {
		return errors.Validation(
			"negotiation_approval.reason_code_invalid",
			"reason_code must be one of the documented values",
		)
	}
	comment = strings.TrimSpace(comment)
	if comment == "" {
		return errors.Validation(
			"negotiation_approval.comment_required",
			"a comment is required on rejection",
		)
	}
	now := time.Now().UTC()
	a.Status = ApprovalInstanceStatusRejected
	a.ReasonCode = code
	a.Comment = comment
	a.ActedAt = &now
	a.UpdatedAt = now
	return nil
}

// SupersedeReset mirrors the BOQ approval-instance version for the
// parallel-reject path (Edge #4): when one step rejects, pending
// peers flip to superseded_reset.
func (a *NegotiationRoundApproval) SupersedeReset() {
	if a.Status == ApprovalInstanceStatusApproved && a.ActedAt != nil {
		a.ActedAtOriginal = a.ActedAt
	}
	a.Status = ApprovalInstanceStatusSupersededReset
	a.UpdatedAt = time.Now().UTC()
}
