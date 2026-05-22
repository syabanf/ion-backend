// Package http — DTOs for the CRM adapter.
//
// All HTTP-layer request/response shapes for CRM live in this file
// (products, leads, documents, conversion, customers, orders,
// onboarding schemas, sales dashboard, KTP OCR). Conversion helpers
// `toXxxDTO` sit next to their target type so a change to the wire
// shape touches one file instead of three.
//
// Why DTOs are an adapter concern (not a domain concern):
//   - Domain types should stay framework-free; they shouldn't know
//     about JSON tags or HTTP versioning.
//   - The wire format can drift (rename, add field, deprecate) without
//     touching usecase or domain code.
//   - One file per bounded context keeps the surface easy to grep when
//     a contract question arises ("what does /api/crm/leads return?").
package http

import (
	"encoding/json"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/crm/domain"
	"github.com/ion-core/backend/internal/crm/port"
	"github.com/ion-core/backend/pkg/httpserver"
	"github.com/ion-core/backend/pkg/sanitize"
)

// =====================================================================
// Products
// =====================================================================

type productDTO struct {
	ID                      string  `json:"id"`
	Code                    string  `json:"code"`
	Name                    string  `json:"name"`
	SpeedMbps               int     `json:"speed_mbps"`
	MonthlyPrice            float64 `json:"monthly_price"`
	OTCPrice                float64 `json:"otc_price"`
	TempActivationWindowHrs int     `json:"temp_activation_window_hours"`
	Active                  bool    `json:"active"`
	// Wave 77 (TC-PRD-014/016/018/022/027): per-kind schema slots.
	OnboardingSchemaID *string `json:"onboarding_schema_id,omitempty"`
	BillingSchemaID    *string `json:"billing_schema_id,omitempty"`
	ServiceSchemaID    *string `json:"service_schema_id,omitempty"`
	CommissionSchemaID *string `json:"commission_schema_id,omitempty"`
	SuspensionSchemaID *string `json:"suspension_schema_id,omitempty"`
}

func toProductDTO(p domain.Product) productDTO {
	d := productDTO{
		ID:                      p.ID.String(),
		Code:                    p.Code,
		Name:                    p.Name,
		SpeedMbps:               p.SpeedMbps,
		MonthlyPrice:            p.MonthlyPrice,
		OTCPrice:                p.OTCPrice,
		TempActivationWindowHrs: p.TempActivationWindowHrs,
		Active:                  p.Active,
	}
	uuidPtrStr := func(u *uuid.UUID) *string {
		if u == nil {
			return nil
		}
		s := u.String()
		return &s
	}
	d.OnboardingSchemaID = uuidPtrStr(p.OnboardingSchemaID)
	d.BillingSchemaID = uuidPtrStr(p.BillingSchemaID)
	d.ServiceSchemaID = uuidPtrStr(p.ServiceSchemaID)
	d.CommissionSchemaID = uuidPtrStr(p.CommissionSchemaID)
	d.SuspensionSchemaID = uuidPtrStr(p.SuspensionSchemaID)
	return d
}

type createProductRequest struct {
	Code         string  `json:"code"`
	Name         string  `json:"name"`
	SpeedMbps    int     `json:"speed_mbps"`
	MonthlyPrice float64 `json:"monthly_price"`
	OTCPrice     float64 `json:"otc_price"`
	// Wave 77: optional schema slot assignment at create time.
	OnboardingSchemaID string `json:"onboarding_schema_id,omitempty"`
	BillingSchemaID    string `json:"billing_schema_id,omitempty"`
	ServiceSchemaID    string `json:"service_schema_id,omitempty"`
	CommissionSchemaID string `json:"commission_schema_id,omitempty"`
	SuspensionSchemaID string `json:"suspension_schema_id,omitempty"`
}

// updateProductRequest — Wave 77 PATCH. Pointer-or-clear pattern lets
// the FE distinguish "leave alone" (omit) vs "explicitly clear" (set
// clear_*=true) vs "reassign" (set id).
type updateProductRequest struct {
	Name          *string  `json:"name,omitempty"`
	SpeedMbps     *int     `json:"speed_mbps,omitempty"`
	MonthlyPrice  *float64 `json:"monthly_price,omitempty"`
	OTCPrice      *float64 `json:"otc_price,omitempty"`
	TempWindowHrs *int     `json:"temp_activation_window_hours,omitempty"`
	Active        *bool    `json:"active,omitempty"`

	OnboardingSchemaID *string `json:"onboarding_schema_id,omitempty"`
	ClearOnboarding    bool    `json:"clear_onboarding,omitempty"`
	BillingSchemaID    *string `json:"billing_schema_id,omitempty"`
	ClearBilling       bool    `json:"clear_billing,omitempty"`
	ServiceSchemaID    *string `json:"service_schema_id,omitempty"`
	ClearService       bool    `json:"clear_service,omitempty"`
	CommissionSchemaID *string `json:"commission_schema_id,omitempty"`
	ClearCommission    bool    `json:"clear_commission,omitempty"`
	SuspensionSchemaID *string `json:"suspension_schema_id,omitempty"`
	ClearSuspension    bool    `json:"clear_suspension,omitempty"`
}

// =====================================================================
// Leads
// =====================================================================

type leadDTO struct {
	ID                  string          `json:"id"`
	LeadNumber          string          `json:"lead_number"`
	Status              string          `json:"status"`
	LeadType            string          `json:"lead_type"` // Wave 76 (TC-CRM-002)
	FullName            string          `json:"full_name"`
	Phone               string          `json:"phone"`
	Email               string          `json:"email,omitempty"`
	NIK                 string          `json:"nik,omitempty"`
	Address             string          `json:"address"`
	GPSLat              *float64        `json:"gps_lat,omitempty"`
	GPSLng              *float64        `json:"gps_lng,omitempty"`
	CoverageVerdict     *string         `json:"coverage_verdict,omitempty"`
	CoverageSnapshot    json.RawMessage `json:"coverage_snapshot,omitempty"`
	AcceptExcessCable   bool            `json:"accept_excess_cable"`
	NearestNodeID       *string         `json:"nearest_node_id,omitempty"`
	CableDistanceM      *float64        `json:"cable_distance_m,omitempty"`
	ExcessCharge        *float64        `json:"excess_charge,omitempty"`
	BranchID            *string         `json:"branch_id,omitempty"`
	BranchName          string          `json:"branch_name,omitempty"`
	BranchCode          string          `json:"branch_code,omitempty"`
	ProductID           *string         `json:"product_id,omitempty"`
	ProductName         string          `json:"product_name,omitempty"`
	ProductCode         string          `json:"product_code,omitempty"`
	SalesID             *string         `json:"sales_id,omitempty"`
	SalesName           string          `json:"sales_name,omitempty"`
	Source              string          `json:"source"`
	// Wave 76 (TC-CRM-007/008): referrer customer FK + the joined
	// customer name so the UI can render "Budi Santoso" instead of
	// the raw UUID (TC-CRM-010).
	ReferrerCustomerID   *string `json:"referrer_customer_id,omitempty"`
	ReferrerCustomerName string  `json:"referrer_customer_name,omitempty"`
	Notes                string  `json:"notes,omitempty"`
	ConvertedCustomerID  *string         `json:"converted_customer_id,omitempty"`
	ConvertedOrderID     *string         `json:"converted_order_id,omitempty"`
	ConvertedAt          *string         `json:"converted_at,omitempty"`
	CreatedAt            string          `json:"created_at"`
	Documents            []documentDTO   `json:"documents,omitempty"`
}

func toLeadDTO(lw port.LeadWithDocs) leadDTO {
	l := lw.Lead
	leadType := string(l.LeadType)
	if leadType == "" {
		leadType = string(domain.LeadTypeBroadband)
	}
	d := leadDTO{
		ID:                l.ID.String(),
		LeadNumber:        l.LeadNumber,
		Status:            string(l.Status),
		LeadType:          leadType,
		FullName:          l.FullName,
		Phone:             l.Phone,
		Email:             l.Email,
		NIK:               sanitize.NIK(l.NIK),
		Address:           l.Address,
		GPSLat:            l.GPSLat,
		GPSLng:            l.GPSLng,
		AcceptExcessCable: l.AcceptExcessCable,
		CableDistanceM:    l.CableDistanceM,
		ExcessCharge:      l.ExcessCharge,
		BranchName:        lw.BranchName,
		BranchCode:        lw.BranchCode,
		ProductName:       lw.ProductName,
		ProductCode:       lw.ProductCode,
		SalesName:         lw.SalesName,
		Source:            string(l.Source),
		Notes:             l.Notes,
		CreatedAt:         httpserver.FormatRFC3339(l.CreatedAt),
	}
	if l.ReferrerCustomerID != nil {
		s := l.ReferrerCustomerID.String()
		d.ReferrerCustomerID = &s
		d.ReferrerCustomerName = lw.ReferrerName
	}
	if l.CoverageVerdict != nil {
		v := string(*l.CoverageVerdict)
		d.CoverageVerdict = &v
	}
	if len(l.CoverageSnapshot) > 0 {
		d.CoverageSnapshot = json.RawMessage(l.CoverageSnapshot)
	}
	if l.NearestNodeID != nil {
		s := l.NearestNodeID.String()
		d.NearestNodeID = &s
	}
	if l.BranchID != nil {
		s := l.BranchID.String()
		d.BranchID = &s
	}
	if l.ProductID != nil {
		s := l.ProductID.String()
		d.ProductID = &s
	}
	if l.SalesID != nil {
		s := l.SalesID.String()
		d.SalesID = &s
	}
	if l.ConvertedCustomerID != nil {
		s := l.ConvertedCustomerID.String()
		d.ConvertedCustomerID = &s
	}
	if l.ConvertedOrderID != nil {
		s := l.ConvertedOrderID.String()
		d.ConvertedOrderID = &s
	}
	if l.ConvertedAt != nil {
		s := httpserver.FormatRFC3339(*l.ConvertedAt)
		d.ConvertedAt = &s
	}
	for _, doc := range lw.Documents {
		d.Documents = append(d.Documents, toDocumentDTO(doc))
	}
	return d
}

type createLeadRequest struct {
	FullName          string   `json:"full_name"`
	Phone             string   `json:"phone"`
	Email             string   `json:"email,omitempty"`
	NIK               string   `json:"nik,omitempty"`
	Address           string   `json:"address"`
	GPSLat            *float64 `json:"gps_lat,omitempty"`
	GPSLng            *float64 `json:"gps_lng,omitempty"`
	ProductID         string   `json:"product_id,omitempty"`
	SalesID           string   `json:"sales_id,omitempty"`
	Source            string   `json:"source,omitempty"`
	Notes             string   `json:"notes,omitempty"`
	AcceptExcessCable bool     `json:"accept_excess_cable,omitempty"`
	// Wave 76 additions.
	LeadType           string `json:"lead_type,omitempty"`            // 'broadband' (default) | 'enterprise'
	ReferrerCustomerID string `json:"referrer_customer_id,omitempty"` // required when source='referral'
}

type updateLeadRequest struct {
	FullName          *string  `json:"full_name,omitempty"`
	Phone             *string  `json:"phone,omitempty"`
	Email             *string  `json:"email,omitempty"`
	NIK               *string  `json:"nik,omitempty"`
	Address           *string  `json:"address,omitempty"`
	GPSLat            *float64 `json:"gps_lat,omitempty"`
	GPSLng            *float64 `json:"gps_lng,omitempty"`
	ClearGPS          bool     `json:"clear_gps,omitempty"`
	ProductID         *string  `json:"product_id,omitempty"`
	ClearProduct      bool     `json:"clear_product,omitempty"`
	SalesID           *string  `json:"sales_id,omitempty"`
	ClearSales        bool     `json:"clear_sales,omitempty"`
	Notes             *string  `json:"notes,omitempty"`
	AcceptExcessCable *bool    `json:"accept_excess_cable,omitempty"`
	Status            *string  `json:"status,omitempty"`
}

// =====================================================================
// Documents
// =====================================================================

type documentDTO struct {
	ID        string `json:"id"`
	LeadID    string `json:"lead_id"`
	DocKey    string `json:"doc_key"`
	Label     string `json:"label"`
	Required  bool   `json:"required"`
	Submitted bool   `json:"submitted"`
	FileURL   string `json:"file_url,omitempty"`
	Notes     string `json:"notes,omitempty"`
}

func toDocumentDTO(d domain.OrderDocument) documentDTO {
	return documentDTO{
		ID:        d.ID.String(),
		LeadID:    d.LeadID.String(),
		DocKey:    d.DocKey,
		Label:     d.Label,
		Required:  d.Required,
		Submitted: d.Submitted,
		FileURL:   d.FileURL,
		Notes:     d.Notes,
	}
}

type updateDocumentRequest struct {
	Submitted *bool   `json:"submitted,omitempty"`
	FileURL   *string `json:"file_url,omitempty"`
	Notes     *string `json:"notes,omitempty"`
}

// =====================================================================
// Customers
// =====================================================================

type customerDTO struct {
	ID                 string   `json:"id"`
	CustomerNumber     string   `json:"customer_number"`
	CustomerType       string   `json:"customer_type"`
	FullName           string   `json:"full_name"`
	Phone              string   `json:"phone"`
	Email              string   `json:"email,omitempty"`
	NIK                string   `json:"nik,omitempty"`
	Address            string   `json:"address"`
	GPSLat             *float64 `json:"gps_lat,omitempty"`
	GPSLng             *float64 `json:"gps_lng,omitempty"`
	BranchID           *string  `json:"branch_id,omitempty"`
	InstallationNodeID *string  `json:"installation_node_id,omitempty"`
	Status             string   `json:"status"`
	CreatedAt          string   `json:"created_at"`
}

func toCustomerDTO(c domain.Customer) customerDTO {
	d := customerDTO{
		ID:             c.ID.String(),
		CustomerNumber: c.CustomerNumber,
		CustomerType:   string(c.CustomerType),
		FullName:       c.FullName,
		Phone:          c.Phone,
		Email:          c.Email,
		NIK:            sanitize.NIK(c.NIK),
		Address:        c.Address,
		GPSLat:         c.GPSLat,
		GPSLng:         c.GPSLng,
		Status:         string(c.Status),
		CreatedAt:      httpserver.FormatRFC3339(c.CreatedAt),
	}
	if c.BranchID != nil {
		s := c.BranchID.String()
		d.BranchID = &s
	}
	if c.InstallationNodeID != nil {
		s := c.InstallationNodeID.String()
		d.InstallationNodeID = &s
	}
	return d
}

// =====================================================================
// Orders
// =====================================================================

type orderDTO struct {
	ID                string  `json:"id"`
	OrderNumber       string  `json:"order_number"`
	LeadID            *string `json:"lead_id,omitempty"`
	CustomerID        string  `json:"customer_id"`
	ProductID         *string `json:"product_id,omitempty"`
	MonthlyPrice      float64 `json:"monthly_price"`
	OTCPrice          float64 `json:"otc_price"`
	OTCType           string  `json:"otc_type"`
	ExcessCharge      float64 `json:"excess_charge"`
	AcceptExcessCable bool    `json:"accept_excess_cable"`
	NearestNodeID     *string `json:"nearest_node_id,omitempty"`
	BranchID          *string `json:"branch_id,omitempty"`
	SalesID           *string `json:"sales_id,omitempty"`
	Status            string  `json:"status"`
	Notes             string  `json:"notes,omitempty"`
	CreatedAt         string  `json:"created_at"`
}

func toOrderDTO(o domain.Order) orderDTO {
	otcType := string(o.OTCType)
	if otcType == "" {
		otcType = "postpaid"
	}
	d := orderDTO{
		ID:                o.ID.String(),
		OrderNumber:       o.OrderNumber,
		CustomerID:        o.CustomerID.String(),
		MonthlyPrice:      o.MonthlyPrice,
		OTCPrice:          o.OTCPrice,
		OTCType:           otcType,
		ExcessCharge:      o.ExcessCharge,
		AcceptExcessCable: o.AcceptExcessCable,
		Status:            string(o.Status),
		Notes:             o.Notes,
		CreatedAt:         httpserver.FormatRFC3339(o.CreatedAt),
	}
	if o.LeadID != nil {
		s := o.LeadID.String()
		d.LeadID = &s
	}
	if o.ProductID != nil {
		s := o.ProductID.String()
		d.ProductID = &s
	}
	if o.NearestNodeID != nil {
		s := o.NearestNodeID.String()
		d.NearestNodeID = &s
	}
	if o.BranchID != nil {
		s := o.BranchID.String()
		d.BranchID = &s
	}
	if o.SalesID != nil {
		s := o.SalesID.String()
		d.SalesID = &s
	}
	return d
}

// =====================================================================
// Onboarding Schemas (M4 r2)
// =====================================================================

type schemaDTO struct {
	ID           string                             `json:"id"`
	CustomerType string                             `json:"customer_type"`
	ProductType  string                             `json:"product_type"`
	Version      int                                `json:"version"`
	Active       bool                               `json:"active"`
	Notes        string                             `json:"notes,omitempty"`
	Documents    []domain.OnboardingContentDocument `json:"documents"`
	ContentRaw   json.RawMessage                    `json:"content,omitempty"`
	CreatedAt    string                             `json:"created_at"`
}

func toSchemaDTO(s domain.OnboardingSchema) schemaDTO {
	raw, _ := s.MarshalContent()
	return schemaDTO{
		ID:           s.ID.String(),
		CustomerType: s.CustomerType,
		ProductType:  s.ProductType,
		Version:      s.Version,
		Active:       s.Active,
		Notes:        s.Notes,
		Documents:    s.Content.Documents,
		ContentRaw:   raw,
		CreatedAt:    httpserver.FormatRFC3339(s.CreatedAt),
	}
}

// =====================================================================
// Sales Dashboard (M4 r2)
//
// Query: ?mine=true scopes to the caller; absent = full network view
// (requires the dashboard.read permission which only senior sales roles
// + ops admin have).
// =====================================================================

type salesDashboardDTO struct {
	LeadsByStatus      map[string]int `json:"leads_by_status"`
	ConvertedThisMonth int            `json:"converted_this_month"`
	OrdersThisMonth    int            `json:"orders_this_month"`
	TotalThisMonth     float64        `json:"total_this_month"`
	RecentLeads        []leadDTO      `json:"recent_leads"`
	RecentConversions  []orderDTO     `json:"recent_conversions"`
}

// =====================================================================
// KTP OCR
// =====================================================================

type ktpOCRResponse struct {
	NIK         string  `json:"nik"`
	FullName    string  `json:"full_name"`
	BirthPlace  string  `json:"birth_place,omitempty"`
	BirthDate   string  `json:"birth_date,omitempty"` // yyyy-mm-dd
	Gender      string  `json:"gender,omitempty"`     // L / P
	Address     string  `json:"address,omitempty"`
	RTRW        string  `json:"rt_rw,omitempty"`
	Kelurahan   string  `json:"kelurahan,omitempty"`
	Kecamatan   string  `json:"kecamatan,omitempty"`
	Religion    string  `json:"religion,omitempty"`
	MaritalStat string  `json:"marital_status,omitempty"`
	Occupation  string  `json:"occupation,omitempty"`
	Citizenship string  `json:"citizenship,omitempty"`
	ValidUntil  string  `json:"valid_until,omitempty"`
	Confidence  float64 `json:"confidence"`
	Stub        bool    `json:"stub"`
}
