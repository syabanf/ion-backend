package http

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/enterprise/domain"
	"github.com/ion-core/backend/pkg/httpserver"
)

// rfc3339 returns t in canonical RFC 3339 with UTC offset. We avoid
// using zero-value timestamps in the wire format — pointer-nil
// becomes JSON null instead.
func rfc3339(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

func rfc3339Ptr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := rfc3339(*t)
	return &s
}

func dateOnly(t time.Time) string {
	return t.UTC().Format("2006-01-02")
}

func dateOnlyPtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := dateOnly(*t)
	return &s
}

func uuidPtrString(u *uuid.UUID) *string {
	if u == nil {
		return nil
	}
	s := u.String()
	return &s
}

// actorUserID pulls the authenticated user's UUID from the request
// context. Returns nil when no claims are attached (shouldn't happen
// behind RequireAuth, but defensive).
func actorUserID(ctx context.Context) *uuid.UUID {
	c := httpserver.ClaimsFromContext(ctx)
	if c == nil {
		return nil
	}
	id := c.UserID
	return &id
}

// =====================================================================
// Pricebook DTOs
// =====================================================================

type pricebookDTO struct {
	ID               string  `json:"id"`
	Code             string  `json:"code"`
	Name             string  `json:"name"`
	Currency         string  `json:"currency"`
	EffectiveFrom    string  `json:"effective_from"`
	EffectiveTo      *string `json:"effective_to,omitempty"`
	HoldingCompanyID string  `json:"holding_company_id"`
	VersionNo        int     `json:"version_no"`
	Status           string  `json:"status"`
	PublishedAt      *string `json:"published_at,omitempty"`
	SupersededAt     *string `json:"superseded_at,omitempty"`
	Notes            string  `json:"notes"`
	CreatedBy        *string `json:"created_by,omitempty"`
	CreatedAt        string  `json:"created_at"`
	UpdatedAt        string  `json:"updated_at"`
}

func toPricebookDTO(p domain.Pricebook) pricebookDTO {
	return pricebookDTO{
		ID:               p.ID.String(),
		Code:             p.Code,
		Name:             p.Name,
		Currency:         p.Currency,
		EffectiveFrom:    dateOnly(p.EffectiveFrom),
		EffectiveTo:      dateOnlyPtr(p.EffectiveTo),
		HoldingCompanyID: p.HoldingCompanyID,
		VersionNo:        p.VersionNo,
		Status:           string(p.Status),
		PublishedAt:      rfc3339Ptr(p.PublishedAt),
		SupersededAt:     rfc3339Ptr(p.SupersededAt),
		Notes:            p.Notes,
		CreatedBy:        uuidPtrString(p.CreatedBy),
		CreatedAt:        rfc3339(p.CreatedAt),
		UpdatedAt:        rfc3339(p.UpdatedAt),
	}
}

type createPricebookRequest struct {
	Code             string `json:"code"`
	Name             string `json:"name"`
	Currency         string `json:"currency"`
	EffectiveFrom    string `json:"effective_from"`
	EffectiveTo      string `json:"effective_to"`
	HoldingCompanyID string `json:"holding_company_id"`
	Notes            string `json:"notes"`
}

type updatePricebookRequest struct {
	Name          *string `json:"name,omitempty"`
	EffectiveFrom *string `json:"effective_from,omitempty"`
	EffectiveTo   *string `json:"effective_to,omitempty"`
	Notes         *string `json:"notes,omitempty"`
}

// =====================================================================
// Pricebook line DTOs
// =====================================================================

type pricebookLineDTO struct {
	ID                        string   `json:"id"`
	PricebookID               string   `json:"pricebook_id"`
	SKU                       string   `json:"sku"`
	Name                      string   `json:"name"`
	Category                  string   `json:"category"`
	Description               string   `json:"description"`
	Unit                      string   `json:"unit"`
	BasePrice                 float64  `json:"base_price"`
	DefaultMarginPct          float64  `json:"default_margin_pct"`
	MinMarginPct              float64  `json:"min_margin_pct"`
	MaxDiscountPct            float64  `json:"max_discount_pct"`
	AllowedProviderCompanyIDs []string `json:"allowed_provider_company_ids"`
	OwnerRole                 string   `json:"owner_role"`
	SortOrder                 int      `json:"sort_order"`
	Active                    bool     `json:"active"`
	CreatedAt                 string   `json:"created_at"`
	UpdatedAt                 string   `json:"updated_at"`
}

func toPricebookLineDTO(l domain.PricebookLine) pricebookLineDTO {
	providers := make([]string, 0, len(l.AllowedProviderCompanyIDs))
	for _, u := range l.AllowedProviderCompanyIDs {
		providers = append(providers, u.String())
	}
	return pricebookLineDTO{
		ID:                        l.ID.String(),
		PricebookID:               l.PricebookID.String(),
		SKU:                       l.SKU,
		Name:                      l.Name,
		Category:                  l.Category,
		Description:               l.Description,
		Unit:                      l.Unit,
		BasePrice:                 l.BasePrice,
		DefaultMarginPct:          l.DefaultMarginPct,
		MinMarginPct:              l.MinMarginPct,
		MaxDiscountPct:            l.MaxDiscountPct,
		AllowedProviderCompanyIDs: providers,
		OwnerRole:                 l.OwnerRole,
		SortOrder:                 l.SortOrder,
		Active:                    l.Active,
		CreatedAt:                 rfc3339(l.CreatedAt),
		UpdatedAt:                 rfc3339(l.UpdatedAt),
	}
}

type createPricebookLineRequest struct {
	SKU                       string   `json:"sku"`
	Name                      string   `json:"name"`
	Category                  string   `json:"category"`
	Description               string   `json:"description"`
	Unit                      string   `json:"unit"`
	BasePrice                 float64  `json:"base_price"`
	DefaultMarginPct          float64  `json:"default_margin_pct"`
	MinMarginPct              float64  `json:"min_margin_pct"`
	MaxDiscountPct            float64  `json:"max_discount_pct"`
	AllowedProviderCompanyIDs []string `json:"allowed_provider_company_ids"`
	OwnerRole                 string   `json:"owner_role"`
	SortOrder                 int      `json:"sort_order"`
}

type updatePricebookLineRequest struct {
	Name                      *string   `json:"name,omitempty"`
	Category                  *string   `json:"category,omitempty"`
	Description               *string   `json:"description,omitempty"`
	Unit                      *string   `json:"unit,omitempty"`
	BasePrice                 *float64  `json:"base_price,omitempty"`
	DefaultMarginPct          *float64  `json:"default_margin_pct,omitempty"`
	MinMarginPct              *float64  `json:"min_margin_pct,omitempty"`
	MaxDiscountPct            *float64  `json:"max_discount_pct,omitempty"`
	AllowedProviderCompanyIDs *[]string `json:"allowed_provider_company_ids,omitempty"`
	OwnerRole                 *string   `json:"owner_role,omitempty"`
	SortOrder                 *int      `json:"sort_order,omitempty"`
	Active                    *bool     `json:"active,omitempty"`
}

// =====================================================================
// Opportunity DTOs
// =====================================================================

type opportunityDTO struct {
	ID                  string  `json:"id"`
	OpportunityNumber   string  `json:"opportunity_number"`
	CustomerID          *string `json:"customer_id,omitempty"`
	AccountName         string  `json:"account_name"`
	AccountIndustry     string  `json:"account_industry"`
	AccountSize         string  `json:"account_size"`
	PICName             string  `json:"pic_name"`
	PICTitle            string  `json:"pic_title"`
	PICPhone            string  `json:"pic_phone"`
	PICEmail            string  `json:"pic_email"`
	OwnerUserID         *string `json:"owner_user_id,omitempty"`
	BranchID            *string `json:"branch_id,omitempty"`
	Stage               string  `json:"stage"`
	Substage            string  `json:"substage"`
	EstimatedValue      float64 `json:"estimated_value"`
	Currency            string  `json:"currency"`
	ExpectedCloseAt     *string `json:"expected_close_at,omitempty"`
	PricebookID         *string `json:"pricebook_id,omitempty"`
	Source              string  `json:"source"`
	ReferrerCustomerID  *string `json:"referrer_customer_id,omitempty"`
	// Pre-BOQ snapshot — raw JSON pass-through (TC-OP-005).
	PreBOQ              json.RawMessage `json:"pre_boq"`
	PreBOQCompletedAt   *string         `json:"pre_boq_completed_at,omitempty"`
	StageEnteredAt      string          `json:"stage_entered_at"`
	LastActivityAt      string          `json:"last_activity_at"`
	LostReasonCode      string          `json:"lost_reason_code,omitempty"`
	LostReason          string          `json:"lost_reason,omitempty"`
	AutoLost            bool            `json:"auto_lost"`
	WonAt               *string         `json:"won_at,omitempty"`
	POReference         string          `json:"po_reference,omitempty"`
	Notes               string          `json:"notes"`
	Revision            int             `json:"revision"`
	CreatedAt           string          `json:"created_at"`
	UpdatedAt           string          `json:"updated_at"`
}

func toOpportunityDTO(o domain.Opportunity) opportunityDTO {
	// Default pre_boq to an empty object so the JSON shape is stable
	// even if the DB returned NULL/empty.
	pre := o.PreBOQ
	if len(pre) == 0 {
		pre = []byte("{}")
	}
	return opportunityDTO{
		ID:                 o.ID.String(),
		OpportunityNumber:  o.OpportunityNumber,
		CustomerID:         uuidPtrString(o.CustomerID),
		AccountName:        o.AccountName,
		AccountIndustry:    o.AccountIndustry,
		AccountSize:        o.AccountSize,
		PICName:            o.PICName,
		PICTitle:           o.PICTitle,
		PICPhone:           o.PICPhone,
		PICEmail:           o.PICEmail,
		OwnerUserID:        uuidPtrString(o.OwnerUserID),
		BranchID:           uuidPtrString(o.BranchID),
		Stage:              string(o.Stage),
		Substage:           string(o.Substage),
		EstimatedValue:     o.EstimatedValue,
		Currency:           o.Currency,
		ExpectedCloseAt:    dateOnlyPtr(o.ExpectedCloseAt),
		PricebookID:        uuidPtrString(o.PricebookID),
		Source:             string(o.Source),
		ReferrerCustomerID: uuidPtrString(o.ReferrerCustomerID),
		PreBOQ:             json.RawMessage(pre),
		PreBOQCompletedAt:  rfc3339Ptr(o.PreBOQCompletedAt),
		StageEnteredAt:     rfc3339(o.StageEnteredAt),
		LastActivityAt:     rfc3339(o.LastActivityAt),
		LostReasonCode:     string(o.LostReasonCode),
		LostReason:         o.LostReason,
		AutoLost:           o.AutoLost,
		WonAt:              rfc3339Ptr(o.WonAt),
		POReference:        o.POReference,
		Notes:              o.Notes,
		Revision:           o.Revision,
		CreatedAt:          rfc3339(o.CreatedAt),
		UpdatedAt:          rfc3339(o.UpdatedAt),
	}
}

type createOpportunityRequest struct {
	AccountName        string  `json:"account_name"`
	AccountIndustry    string  `json:"account_industry"`
	AccountSize        string  `json:"account_size"`
	PICName            string  `json:"pic_name"`
	PICTitle           string  `json:"pic_title"`
	PICPhone           string  `json:"pic_phone"`
	PICEmail           string  `json:"pic_email"`
	OwnerUserID        string  `json:"owner_user_id"`
	BranchID           string  `json:"branch_id"`
	EstimatedValue     float64 `json:"estimated_value"`
	Currency           string  `json:"currency"`
	ExpectedCloseAt    string  `json:"expected_close_at"`
	Source             string  `json:"source"`
	ReferrerCustomerID string  `json:"referrer_customer_id"`
	CustomerID         string  `json:"customer_id"`
	Notes              string  `json:"notes"`
}

type updateOpportunityRequest struct {
	AccountName     *string  `json:"account_name,omitempty"`
	AccountIndustry *string  `json:"account_industry,omitempty"`
	AccountSize     *string  `json:"account_size,omitempty"`
	PICName         *string  `json:"pic_name,omitempty"`
	PICTitle        *string  `json:"pic_title,omitempty"`
	PICPhone        *string  `json:"pic_phone,omitempty"`
	PICEmail        *string  `json:"pic_email,omitempty"`
	OwnerUserID     *string  `json:"owner_user_id,omitempty"`
	BranchID        *string  `json:"branch_id,omitempty"`
	EstimatedValue  *float64 `json:"estimated_value,omitempty"`
	ExpectedCloseAt *string  `json:"expected_close_at,omitempty"`
	Notes           *string  `json:"notes,omitempty"`
	// Optimistic concurrency — clients echo back the revision they
	// loaded; the BE returns HTTP 409 stale_version if the stored
	// row has advanced.
	IfRevision *int `json:"if_revision,omitempty"`
}

type advanceStageRequest struct {
	TargetStage string `json:"target_stage"`
	POReference string `json:"po_reference"`
	IfRevision  *int   `json:"if_revision,omitempty"`
}

type markLostRequest struct {
	ReasonCode string `json:"reason_code"`
	Reason     string `json:"reason"`
	IfRevision *int   `json:"if_revision,omitempty"`
}

type completePreBOQRequest struct {
	Snapshot   json.RawMessage `json:"snapshot"`
	IfRevision *int            `json:"if_revision,omitempty"`
}

type pinPricebookRequest struct {
	PricebookID string `json:"pricebook_id"`
	IfRevision  *int   `json:"if_revision,omitempty"`
}
