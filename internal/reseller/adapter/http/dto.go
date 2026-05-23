// Package http is the driving adapter for the reseller bounded
// context. Same conventions as enterprise / warehouse / crm:
//   - One handler per surface (admin, platform).
//   - DTOs live next to the handler they're used by.
//   - Tenant scoping for the platform surface is enforced by a
//     middleware that resolves the session token to a reseller id
//     and stashes it in the request context.
package http

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/reseller/domain"
	"github.com/ion-core/backend/pkg/errors"
	"github.com/ion-core/backend/pkg/httpserver"
)

// actorUserID pulls the authenticated user's UUID from the request
// context. Returns nil when no claims are attached (shouldn't happen
// behind RequireAuth, but defensive). Mirror of the enterprise
// helper — kept context-local so this package can move to its own
// binary without cross-imports.
func actorUserID(ctx context.Context) *uuid.UUID {
	c := httpserver.ClaimsFromContext(ctx)
	if c == nil {
		return nil
	}
	id := c.UserID
	return &id
}

func parseUUID(s, field string) (uuid.UUID, error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil, errors.Validation(
			field+".id_invalid",
			field+" is not a valid uuid",
		)
	}
	return id, nil
}

func rfc3339Ptr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}

func uuidPtrString(u *uuid.UUID) *string {
	if u == nil {
		return nil
	}
	s := u.String()
	return &s
}

// =====================================================================
// Reseller account DTO + mapping
// =====================================================================

type resellerAccountDTO struct {
	ID                 string  `json:"id"`
	ParentSubsidiaryID *string `json:"parent_subsidiary_id,omitempty"`
	Name               string  `json:"name"`
	NPWP               string  `json:"npwp,omitempty"`
	ContactEmail       string  `json:"contact_email,omitempty"`
	ContactPhone       string  `json:"contact_phone,omitempty"`
	Status             string  `json:"status"`
	MarginPct          float64 `json:"margin_pct"`
	CreditLimit        float64 `json:"credit_limit"`
	Balance            float64 `json:"balance"`
	CreatedAt          string  `json:"created_at"`
	UpdatedAt          string  `json:"updated_at"`
	ApprovedAt         *string `json:"approved_at,omitempty"`
	ApprovedBy         *string `json:"approved_by,omitempty"`
	SuspendReason      string  `json:"suspend_reason,omitempty"`
}

func toResellerAccountDTO(a domain.ResellerAccount) resellerAccountDTO {
	return resellerAccountDTO{
		ID:                 a.ID.String(),
		ParentSubsidiaryID: uuidPtrString(a.ParentSubsidiaryID),
		Name:               a.Name,
		NPWP:               a.NPWP,
		ContactEmail:       a.ContactEmail,
		ContactPhone:       a.ContactPhone,
		Status:             string(a.Status),
		MarginPct:          a.MarginPct,
		CreditLimit:        a.CreditLimit,
		Balance:            a.Balance,
		CreatedAt:          a.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:          a.UpdatedAt.UTC().Format(time.RFC3339),
		ApprovedAt:         rfc3339Ptr(a.ApprovedAt),
		ApprovedBy:         uuidPtrString(a.ApprovedBy),
		SuspendReason:      a.SuspendReason,
	}
}

// =====================================================================
// Wholesale SKU DTO + mapping
// =====================================================================

type wholesaleSKUDTO struct {
	ID                   string  `json:"id"`
	SupplierSubsidiaryID string  `json:"supplier_subsidiary_id"`
	Name                 string  `json:"name"`
	SKUCode              string  `json:"sku_code"`
	UnitPrice            float64 `json:"unit_price"`
	Unit                 string  `json:"unit"`
	IsActive             bool    `json:"is_active"`
	CreatedAt            string  `json:"created_at"`
	UpdatedAt            string  `json:"updated_at"`
}

func toWholesaleSKUDTO(s domain.WholesaleSKU) wholesaleSKUDTO {
	return wholesaleSKUDTO{
		ID:                   s.ID.String(),
		SupplierSubsidiaryID: s.SupplierSubsidiaryID.String(),
		Name:                 s.Name,
		SKUCode:              s.SKUCode,
		UnitPrice:            s.UnitPrice,
		Unit:                 s.Unit,
		IsActive:             s.IsActive,
		CreatedAt:            s.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:            s.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// =====================================================================
// Wholesale order DTO + mapping
// =====================================================================

type wholesaleOrderLineDTO struct {
	ID        string  `json:"id"`
	SKUID     string  `json:"sku_id"`
	Qty       int     `json:"qty"`
	UnitPrice float64 `json:"unit_price"`
	LineTotal float64 `json:"line_total"`
}

type wholesaleOrderDTO struct {
	ID                   string                  `json:"id"`
	ResellerAccountID    string                  `json:"reseller_account_id"`
	SupplierSubsidiaryID string                  `json:"supplier_subsidiary_id"`
	OrderNo              string                  `json:"order_no"`
	Status               string                  `json:"status"`
	Subtotal             float64                 `json:"subtotal"`
	Total                float64                 `json:"total"`
	CreatedAt            string                  `json:"created_at"`
	UpdatedAt            string                  `json:"updated_at"`
	ApprovedAt           *string                 `json:"approved_at,omitempty"`
	FulfilledAt          *string                 `json:"fulfilled_at,omitempty"`
	ApprovedBy           *string                 `json:"approved_by,omitempty"`
	Lines                []wholesaleOrderLineDTO `json:"lines"`
}

func toWholesaleOrderDTO(o domain.WholesaleOrder) wholesaleOrderDTO {
	lines := make([]wholesaleOrderLineDTO, 0, len(o.Lines))
	for _, l := range o.Lines {
		lines = append(lines, wholesaleOrderLineDTO{
			ID:        l.ID.String(),
			SKUID:     l.SKUID.String(),
			Qty:       l.Qty,
			UnitPrice: l.UnitPrice,
			LineTotal: l.LineTotal,
		})
	}
	return wholesaleOrderDTO{
		ID:                   o.ID.String(),
		ResellerAccountID:    o.ResellerAccountID.String(),
		SupplierSubsidiaryID: o.SupplierSubsidiaryID.String(),
		OrderNo:              o.OrderNo,
		Status:               string(o.Status),
		Subtotal:             o.Subtotal,
		Total:                o.Total,
		CreatedAt:            o.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:            o.UpdatedAt.UTC().Format(time.RFC3339),
		ApprovedAt:           rfc3339Ptr(o.ApprovedAt),
		FulfilledAt:          rfc3339Ptr(o.FulfilledAt),
		ApprovedBy:           uuidPtrString(o.ApprovedBy),
		Lines:                lines,
	}
}

// =====================================================================
// Platform session DTO
// =====================================================================

type platformSessionDTO struct {
	SessionToken      string  `json:"session_token"`
	ResellerAccountID string  `json:"reseller_account_id"`
	ExpiresAt         *string `json:"expires_at,omitempty"`
}

func toPlatformSessionDTO(s domain.PlatformSession) platformSessionDTO {
	return platformSessionDTO{
		SessionToken:      s.SessionToken,
		ResellerAccountID: s.ResellerAccountID.String(),
		ExpiresAt:         rfc3339Ptr(s.ExpiresAt),
	}
}

// =====================================================================
// Request bodies
// =====================================================================

type onboardResellerRequest struct {
	Name               string  `json:"name"`
	NPWP               string  `json:"npwp"`
	ContactEmail       string  `json:"contact_email"`
	ContactPhone       string  `json:"contact_phone"`
	ParentSubsidiaryID *string `json:"parent_subsidiary_id,omitempty"`
}

type suspendResellerRequest struct {
	Reason string `json:"reason"`
}

type createSKURequest struct {
	SupplierSubsidiaryID string  `json:"supplier_subsidiary_id"`
	Name                 string  `json:"name"`
	SKUCode              string  `json:"sku_code"`
	UnitPrice            float64 `json:"unit_price"`
	Unit                 string  `json:"unit"`
}

type updateSKURequest struct {
	Name      *string  `json:"name,omitempty"`
	UnitPrice *float64 `json:"unit_price,omitempty"`
	Unit      *string  `json:"unit,omitempty"`
	IsActive  *bool    `json:"is_active,omitempty"`
}

type createOrderLineRequest struct {
	SKUID string `json:"sku_id"`
	Qty   int    `json:"qty"`
}

type createOrderRequest struct {
	Lines []createOrderLineRequest `json:"lines"`
}

type issueSessionRequest struct {
	ResellerID string `json:"reseller_id"`
	Secret     string `json:"secret"`
	TTLSeconds int    `json:"ttl_seconds,omitempty"`
}

// writeJSON / writeError thin wrappers to keep handlers readable. We
// import the package-level helpers and just rename for brevity.
func writeJSON(w http.ResponseWriter, status int, body any) {
	httpserver.WriteJSON(w, status, body)
}

func writeErr(w http.ResponseWriter, err error) {
	httpserver.WriteError(w, err)
}
