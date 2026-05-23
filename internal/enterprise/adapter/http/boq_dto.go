package http

import (
	"encoding/json"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/enterprise/domain"
)

// =====================================================================
// SLA template DTOs
// =====================================================================

type slaTemplateDTO struct {
	ID          string          `json:"id"`
	Key         string          `json:"key"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Details     json.RawMessage `json:"details"`
	Active      bool            `json:"active"`
	CreatedAt   string          `json:"created_at"`
	UpdatedAt   string          `json:"updated_at"`
}

func toSLATemplateDTO(t domain.SLATemplate) slaTemplateDTO {
	d := t.Details
	if len(d) == 0 {
		d = []byte("{}")
	}
	return slaTemplateDTO{
		ID:          t.ID.String(),
		Key:         t.Key,
		Name:        t.Name,
		Description: t.Description,
		Details:     json.RawMessage(d),
		Active:      t.Active,
		CreatedAt:   rfc3339(t.CreatedAt),
		UpdatedAt:   rfc3339(t.UpdatedAt),
	}
}

type createSLATemplateRequest struct {
	Key         string          `json:"key"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Details     json.RawMessage `json:"details"`
}

type updateSLATemplateRequest struct {
	Name        *string         `json:"name,omitempty"`
	Description *string         `json:"description,omitempty"`
	Details     json.RawMessage `json:"details,omitempty"`
	Active      *bool           `json:"active,omitempty"`
}

// =====================================================================
// Approval template DTOs
// =====================================================================

type approvalTemplateMemberDTO struct {
	UserID  string `json:"user_id"`
	StepNo  int    `json:"step_no"`
	RoleTag string `json:"role_tag"`
}

type approvalTemplateDTO struct {
	ID          string                       `json:"id"`
	Key         string                       `json:"key"`
	Name        string                       `json:"name"`
	Mode        string                       `json:"mode"`
	Description string                       `json:"description"`
	Active      bool                         `json:"active"`
	PublishedAt *string                      `json:"published_at,omitempty"`
	Members     []approvalTemplateMemberDTO  `json:"members,omitempty"`
	CreatedAt   string                       `json:"created_at"`
	UpdatedAt   string                       `json:"updated_at"`
}

func toApprovalTemplateDTO(t domain.ApprovalTemplate, members []domain.ApprovalTemplateMember) approvalTemplateDTO {
	out := approvalTemplateDTO{
		ID:          t.ID.String(),
		Key:         t.Key,
		Name:        t.Name,
		Mode:        string(t.Mode),
		Description: t.Description,
		Active:      t.Active,
		PublishedAt: rfc3339Ptr(t.PublishedAt),
		CreatedAt:   rfc3339(t.CreatedAt),
		UpdatedAt:   rfc3339(t.UpdatedAt),
	}
	if members != nil {
		out.Members = make([]approvalTemplateMemberDTO, 0, len(members))
		for _, m := range members {
			out.Members = append(out.Members, approvalTemplateMemberDTO{
				UserID:  m.UserID.String(),
				StepNo:  m.StepNo,
				RoleTag: m.RoleTag,
			})
		}
	}
	return out
}

type createApprovalTemplateRequest struct {
	Key         string                      `json:"key"`
	Name        string                      `json:"name"`
	Mode        string                      `json:"mode"`
	Description string                      `json:"description"`
	Members     []approvalTemplateMemberDTO `json:"members"`
}

type updateApprovalTemplateRequest struct {
	Name        *string                      `json:"name,omitempty"`
	Description *string                      `json:"description,omitempty"`
	Active      *bool                        `json:"active,omitempty"`
	Members     *[]approvalTemplateMemberDTO `json:"members,omitempty"`
}

// =====================================================================
// BOQ DTOs
// =====================================================================

type boqDTO struct {
	ID                  string  `json:"id"`
	BOQNumber           string  `json:"boq_number"`
	OpportunityID       string  `json:"opportunity_id"`
	PricebookID         string  `json:"pricebook_id"`
	VersionNo           int     `json:"version_no"`
	Status              string  `json:"status"`
	// Field-masking-eligible fields. Vendor responses strip these to
	// null; sales/finance see the real values. The HTTP handler
	// applies masking — see boq_handler.go.
	SellTotal           float64 `json:"sell_total"`
	SubtotalAmount      float64 `json:"subtotal_amount"`
	TaxPct              float64 `json:"tax_pct"`
	TaxAmount           float64 `json:"tax_amount"`
	CostTotal           float64 `json:"cost_total"`
	MarginPct           float64 `json:"margin_pct"`
	SnapshotHash        string  `json:"snapshot_hash"`
	ApprovalTemplateID  *string `json:"approval_template_id,omitempty"`
	SourceRFQID         *string `json:"source_rfq_id,omitempty"`
	// Wave 106 — commercial owner subsidiary FK (TC-BQ-013). Nullable;
	// omitted from the response when unset so legacy callers see the
	// same shape.
	CommercialOwnerSubsidiaryID *string `json:"commercial_owner_subsidiary_id,omitempty"`
	SubmittedAt         *string `json:"submitted_at,omitempty"`
	ApprovedAt          *string `json:"approved_at,omitempty"`
	RejectedAt          *string `json:"rejected_at,omitempty"`
	SupersededAt        *string `json:"superseded_at,omitempty"`
	RejectionReasonCode string  `json:"rejection_reason_code,omitempty"`
	RejectionComment    string  `json:"rejection_comment,omitempty"`
	Notes               string  `json:"notes"`
	Revision            int     `json:"revision"`
	CreatedBy           *string `json:"created_by,omitempty"`
	CreatedAt           string  `json:"created_at"`
	UpdatedAt           string  `json:"updated_at"`
}

func toBOQDTO(b domain.BOQ) boqDTO {
	return boqDTO{
		ID:                          b.ID.String(),
		BOQNumber:                   b.BOQNumber,
		OpportunityID:               b.OpportunityID.String(),
		PricebookID:                 b.PricebookID.String(),
		VersionNo:                   b.VersionNo,
		Status:                      string(b.Status),
		SellTotal:                   b.SellTotal,
		SubtotalAmount:              b.SubtotalAmount,
		TaxPct:                      b.TaxPct,
		TaxAmount:                   b.TaxAmount,
		CostTotal:                   b.CostTotal,
		MarginPct:                   b.MarginPct,
		SnapshotHash:                b.SnapshotHash,
		ApprovalTemplateID:          uuidPtrString(b.ApprovalTemplateID),
		SourceRFQID:                 uuidPtrString(b.SourceRFQID),
		CommercialOwnerSubsidiaryID: uuidPtrString(b.CommercialOwnerSubsidiaryID),
		SubmittedAt:                 rfc3339Ptr(b.SubmittedAt),
		ApprovedAt:                  rfc3339Ptr(b.ApprovedAt),
		RejectedAt:                  rfc3339Ptr(b.RejectedAt),
		SupersededAt:                rfc3339Ptr(b.SupersededAt),
		RejectionReasonCode:         string(b.RejectionReasonCode),
		RejectionComment:            b.RejectionComment,
		Notes:                       b.Notes,
		Revision:                    b.Revision,
		CreatedBy:                   uuidPtrString(b.CreatedBy),
		CreatedAt:                   rfc3339(b.CreatedAt),
		UpdatedAt:                   rfc3339(b.UpdatedAt),
	}
}

type boqLineDTO struct {
	ID                        string   `json:"id"`
	BOQVersionID              string   `json:"boq_version_id"`
	PricebookLineID           string   `json:"pricebook_line_id"`
	SKU                       string   `json:"sku"`
	Name                      string   `json:"name"`
	Unit                      string   `json:"unit"`
	BasePriceSnapshot         float64  `json:"base_price_snapshot"`
	MinMarginSnapshot         float64  `json:"min_margin_snapshot"`
	MaxDiscountSnapshot       float64  `json:"max_discount_snapshot"`
	AssignedProviderCompanyID *string  `json:"assigned_provider_company_id,omitempty"`
	ProviderUserID            *string  `json:"provider_user_id,omitempty"`
	// Vendor-cost fields visible to vendor + sales support. Sell +
	// margin are masked from vendor responses by middleware.
	VendorUnitCost            *float64 `json:"vendor_unit_cost,omitempty"`
	SellUnitPrice             float64  `json:"sell_unit_price"`
	Quantity                  float64  `json:"quantity"`
	LineDiscountPct           float64  `json:"line_discount_pct"`
	SLATemplateID             string   `json:"sla_template_id"`
	Status                    string   `json:"status"`
	Notes                     string   `json:"notes"`
	SortOrder                 int      `json:"sort_order"`
	// Wave 106 — TC-BQ-013 ic_po_required derived flag. True when the
	// line's assigned_provider_company_id differs from the BOQ header's
	// commercial_owner_subsidiary_id. Computed on read by the DTO mapper.
	ICPORequired bool `json:"ic_po_required"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

// toBOQLineDTO maps a domain BOQLine to its API DTO. When `parent` is
// supplied the ic_po_required flag is derived; when nil (legacy call
// sites or shapes that don't carry the header) the flag stays false.
func toBOQLineDTO(l domain.BOQLine, parent *domain.BOQ) boqLineDTO {
	out := boqLineDTO{
		ID:                        l.ID.String(),
		BOQVersionID:              l.BOQVersionID.String(),
		PricebookLineID:           l.PricebookLineID.String(),
		SKU:                       l.SKU,
		Name:                      l.Name,
		Unit:                      l.Unit,
		BasePriceSnapshot:         l.BasePriceSnapshot,
		MinMarginSnapshot:         l.MinMarginSnapshot,
		MaxDiscountSnapshot:       l.MaxDiscountSnapshot,
		AssignedProviderCompanyID: uuidPtrString(l.AssignedProviderCompanyID),
		ProviderUserID:            uuidPtrString(l.ProviderUserID),
		VendorUnitCost:            l.VendorUnitCost,
		SellUnitPrice:             l.SellUnitPrice,
		Quantity:                  l.Quantity,
		LineDiscountPct:           l.LineDiscountPct,
		SLATemplateID:             l.SLATemplateID.String(),
		Status:                    string(l.Status),
		Notes:                     l.Notes,
		SortOrder:                 l.SortOrder,
		CreatedAt:                 rfc3339(l.CreatedAt),
		UpdatedAt:                 rfc3339(l.UpdatedAt),
	}
	if parent != nil {
		out.ICPORequired = parent.LineICPORequired(&l)
	}
	return out
}

type createBOQRequest struct {
	OpportunityID string `json:"opportunity_id"`
	PricebookID   string `json:"pricebook_id"`
	Notes         string `json:"notes"`
}

type updateBOQRequest struct {
	Notes      *string `json:"notes,omitempty"`
	IfRevision *int    `json:"if_revision,omitempty"`
}

type createBOQLineRequest struct {
	PricebookLineID string  `json:"pricebook_line_id"`
	SLATemplateID   string  `json:"sla_template_id"`
	Quantity        float64 `json:"quantity"`
	Notes           string  `json:"notes"`
	SortOrder       int     `json:"sort_order"`
}

type updateBOQLineRequest struct {
	Quantity                  *float64 `json:"quantity,omitempty"`
	SellUnitPrice             *float64 `json:"sell_unit_price,omitempty"`
	LineDiscountPct           *float64 `json:"line_discount_pct,omitempty"`
	AssignedProviderCompanyID *string  `json:"assigned_provider_company_id,omitempty"`
	ProviderUserID            *string  `json:"provider_user_id,omitempty"`
	SLATemplateID             *string  `json:"sla_template_id,omitempty"`
	Notes                     *string  `json:"notes,omitempty"`
	SortOrder                 *int     `json:"sort_order,omitempty"`
}

type setVendorCostRequest struct {
	VendorUnitCost float64 `json:"vendor_unit_cost"`
}

type submitBOQRequest struct {
	ApprovalTemplateID string `json:"approval_template_id"`
	IfRevision         *int   `json:"if_revision,omitempty"`
}

type approvalActionRequest struct {
	ReasonCode string `json:"reason_code"`
	Comment    string `json:"comment"`
}

// =====================================================================
// Approval instance DTO
// =====================================================================

type approvalInstanceDTO struct {
	ID              string  `json:"id"`
	BOQVersionID    string  `json:"boq_version_id"`
	TemplateID      string  `json:"template_id"`
	StepNo          int     `json:"step_no"`
	ApproverUserID  string  `json:"approver_user_id"`
	RoleTag         string  `json:"role_tag"`
	Status          string  `json:"status"`
	ReasonCode      string  `json:"reason_code,omitempty"`
	Comment         string  `json:"comment,omitempty"`
	ActedAt         *string `json:"acted_at,omitempty"`
	ActedAtOriginal *string `json:"acted_at_original,omitempty"`
	CreatedAt       string  `json:"created_at"`
	UpdatedAt       string  `json:"updated_at"`
}

func toApprovalInstanceDTO(a domain.ApprovalInstance) approvalInstanceDTO {
	return approvalInstanceDTO{
		ID:              a.ID.String(),
		BOQVersionID:    a.BOQVersionID.String(),
		TemplateID:      a.TemplateID.String(),
		StepNo:          a.StepNo,
		ApproverUserID:  a.ApproverUserID.String(),
		RoleTag:         a.RoleTag,
		Status:          string(a.Status),
		ReasonCode:      string(a.ReasonCode),
		Comment:         a.Comment,
		ActedAt:         rfc3339Ptr(a.ActedAt),
		ActedAtOriginal: rfc3339Ptr(a.ActedAtOriginal),
		CreatedAt:       rfc3339(a.CreatedAt),
		UpdatedAt:       rfc3339(a.UpdatedAt),
	}
}

// =====================================================================
// Helpers used across DTOs
// =====================================================================

// uuidPtr converts a hex string to *uuid.UUID, returning nil for empty.
// Used in request handlers — DTOs in the response path use uuidPtrString
// from dto.go (Phase 2) which we leverage here too.
func uuidPtr(s string) (*uuid.UUID, error) {
	if s == "" {
		return nil, nil
	}
	u, err := uuid.Parse(s)
	if err != nil {
		return nil, err
	}
	return &u, nil
}
