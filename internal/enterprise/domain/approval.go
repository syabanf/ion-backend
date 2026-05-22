package domain

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// =====================================================================
// Approval template — reusable chain definition
// =====================================================================

// ApprovalMode is the execution strategy for a chain.
//
//	sequential — each step must complete before the next; rejection
//	             at any step aborts the round
//	parallel   — all steps active concurrently; ALL must approve for
//	             the BOQ to advance, ANY rejection aborts the round
//	             and flips peer pending instances to superseded_reset
type ApprovalMode string

const (
	ApprovalModeSequential ApprovalMode = "sequential"
	ApprovalModeParallel   ApprovalMode = "parallel"
)

// ApprovalTemplate is the reusable chain definition. Members live in
// a separate entity (ApprovalTemplateMember) so a step can reference
// a specific user_id with audit-friendly role_tag.
type ApprovalTemplate struct {
	ID          uuid.UUID
	Key         string
	Name        string
	Mode        ApprovalMode
	Description string
	Active      bool
	PublishedAt *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// ApprovalTemplateMember pins a specific user to a step of a template.
// For sequential templates the step_no defines execution order;
// for parallel templates step_no is decorative (we still keep it
// stable for audit consistency).
type ApprovalTemplateMember struct {
	ID         uuid.UUID
	TemplateID uuid.UUID
	UserID     uuid.UUID
	StepNo     int
	RoleTag    string
	CreatedAt  time.Time
}

func NewApprovalTemplate(key, name string, mode ApprovalMode) (*ApprovalTemplate, error) {
	key = strings.TrimSpace(key)
	name = strings.TrimSpace(name)
	if key == "" {
		return nil, errors.Validation("approval_template.key_required", "key is required")
	}
	if name == "" {
		return nil, errors.Validation("approval_template.name_required", "name is required")
	}
	if mode != ApprovalModeSequential && mode != ApprovalModeParallel {
		return nil, errors.Validation(
			"approval_template.mode_invalid",
			"mode must be 'sequential' or 'parallel'",
		)
	}
	now := time.Now().UTC()
	return &ApprovalTemplate{
		ID:        uuid.New(),
		Key:       key,
		Name:      name,
		Mode:      mode,
		Active:    true,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

// Publish stamps the published_at timestamp. Templates can only be
// picked on BOQ submit when active AND published (matches CPQ TC-AP-006).
func (t *ApprovalTemplate) Publish() error {
	if !t.Active {
		return errors.Conflict(
			"approval_template.inactive",
			"cannot publish an inactive template",
		)
	}
	now := time.Now().UTC()
	t.PublishedAt = &now
	t.UpdatedAt = now
	return nil
}

// =====================================================================
// Approval instance — per-BOQ × step row
// =====================================================================

type ApprovalInstanceStatus string

const (
	ApprovalInstanceStatusPending          ApprovalInstanceStatus = "pending"
	ApprovalInstanceStatusApproved         ApprovalInstanceStatus = "approved"
	ApprovalInstanceStatusRejected         ApprovalInstanceStatus = "rejected"
	// Set when an upstream price change resets an already-approved step
	// (CPQ Edge #2). The original acted_at is preserved separately.
	ApprovalInstanceStatusSupersededReset  ApprovalInstanceStatus = "superseded_reset"
)

// ApprovalReasonCode is the closed set of categorical codes a
// rejecting approver must pick. The full free-text comment is
// also required (CPQ §4.8 — both `reason_code` AND `comment`).
type ApprovalReasonCode string

const (
	ApprovalReasonNone          ApprovalReasonCode = ""
	ApprovalReasonPricing       ApprovalReasonCode = "pricing"
	ApprovalReasonScope         ApprovalReasonCode = "scope"
	ApprovalReasonDocumentation ApprovalReasonCode = "documentation"
	ApprovalReasonCompliance    ApprovalReasonCode = "compliance"
	ApprovalReasonOther         ApprovalReasonCode = "other"
)

func IsValidApprovalReasonCode(code ApprovalReasonCode) bool {
	switch code {
	case ApprovalReasonPricing,
		ApprovalReasonScope,
		ApprovalReasonDocumentation,
		ApprovalReasonCompliance,
		ApprovalReasonOther:
		return true
	}
	return false
}

type ApprovalInstance struct {
	ID              uuid.UUID
	BOQVersionID    uuid.UUID
	TemplateID      uuid.UUID
	StepNo          int
	ApproverUserID  uuid.UUID
	RoleTag         string
	Status          ApprovalInstanceStatus
	ReasonCode      ApprovalReasonCode
	Comment         string
	ActedAt         *time.Time
	ActedAtOriginal *time.Time // preserved when superseded_reset (Edge #2)
	ResetReason     string     // why the chain was reset (Edge #2)
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// NewApprovalInstance materializes a chain step in the pending state.
// Constructed by the usecase when BOQ submit fires.
func NewApprovalInstance(
	boqVersionID, templateID uuid.UUID,
	stepNo int,
	approverUserID uuid.UUID,
	roleTag string,
) (*ApprovalInstance, error) {
	if boqVersionID == uuid.Nil {
		return nil, errors.Validation("approval_instance.boq_required", "boq_version_id is required")
	}
	if templateID == uuid.Nil {
		return nil, errors.Validation("approval_instance.template_required", "template_id is required")
	}
	if approverUserID == uuid.Nil {
		return nil, errors.Validation("approval_instance.approver_required", "approver_user_id is required")
	}
	if stepNo < 1 {
		return nil, errors.Validation("approval_instance.step_invalid", "step_no must be >= 1")
	}
	now := time.Now().UTC()
	return &ApprovalInstance{
		ID:             uuid.New(),
		BOQVersionID:   boqVersionID,
		TemplateID:     templateID,
		StepNo:         stepNo,
		ApproverUserID: approverUserID,
		RoleTag:        roleTag,
		Status:         ApprovalInstanceStatusPending,
		CreatedAt:      now,
		UpdatedAt:      now,
	}, nil
}

// Approve transitions pending → approved. Idempotent on already-
// approved by the same actor; rejects all other prior states.
func (a *ApprovalInstance) Approve(actorUserID uuid.UUID) error {
	if a.ApproverUserID != actorUserID {
		return errors.Forbidden(
			"approval_instance.not_assignee",
			"only the assigned approver can act on this step",
		)
	}
	switch a.Status {
	case ApprovalInstanceStatusPending:
		// fall through to commit
	case ApprovalInstanceStatusApproved:
		return nil // idempotent
	default:
		return errors.Conflict(
			"approval_instance.invalid_state",
			"can only approve from pending state (current: "+string(a.Status)+")",
		)
	}
	now := time.Now().UTC()
	a.Status = ApprovalInstanceStatusApproved
	a.ActedAt = &now
	a.UpdatedAt = now
	return nil
}

// Reject transitions pending → rejected. Both `reasonCode` AND
// `comment` are required (CPQ §4.8 + TC-AP-009).
func (a *ApprovalInstance) Reject(actorUserID uuid.UUID, code ApprovalReasonCode, comment string) error {
	if a.ApproverUserID != actorUserID {
		return errors.Forbidden(
			"approval_instance.not_assignee",
			"only the assigned approver can act on this step",
		)
	}
	if a.Status != ApprovalInstanceStatusPending {
		return errors.Conflict(
			"approval_instance.invalid_state",
			"can only reject from pending state (current: "+string(a.Status)+")",
		)
	}
	if !IsValidApprovalReasonCode(code) {
		return errors.Validation(
			"approval_instance.reason_code_invalid",
			"reason_code must be one of the documented values",
		)
	}
	comment = strings.TrimSpace(comment)
	if comment == "" {
		return errors.Validation(
			"approval_instance.comment_required",
			"a free-text comment is required when rejecting",
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

// SupersedeReset is called by the usecase when an upstream price
// change invalidates an already-approved step. The original
// acted_at is preserved so audit can show "first approved at X,
// reset by upstream edit at Y" (Edge #2).
func (a *ApprovalInstance) SupersedeReset() error {
	if a.Status == ApprovalInstanceStatusApproved && a.ActedAt != nil {
		a.ActedAtOriginal = a.ActedAt
	}
	a.Status = ApprovalInstanceStatusSupersededReset
	a.UpdatedAt = time.Now().UTC()
	return nil
}

// SupersedeResetWithReason is the Edge #2 variant — records WHY the
// reset fired so the audit trail explains the chain restart.
//   pricing_changed   — sell / discount / qty / vendor cost edit
//   line_added        — a new line landed on the BOQ
//   line_removed      — a line was removed
//   chain_changed     — the approval template was swapped
func (a *ApprovalInstance) SupersedeResetWithReason(reason string) error {
	if err := a.SupersedeReset(); err != nil {
		return err
	}
	a.ResetReason = reason
	return nil
}
