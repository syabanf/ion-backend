package http

import (
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/reseller/domain"
	"github.com/ion-core/backend/internal/reseller/port"
)

// =====================================================================
// Subscriber DTOs
// =====================================================================

type subscriberDTO struct {
	ID                string  `json:"id"`
	ResellerAccountID string  `json:"reseller_account_id"`
	CustomerName      string  `json:"customer_name"`
	CustomerEmail     string  `json:"customer_email,omitempty"`
	CustomerPhone     string  `json:"customer_phone,omitempty"`
	AddressLine       string  `json:"address_line,omitempty"`
	SubAreaID         *string `json:"sub_area_id,omitempty"`
	ServicePlanID     *string `json:"service_plan_id,omitempty"`
	MonthlyFee        float64 `json:"monthly_fee"`
	Status            string  `json:"status"`
	Notes             string  `json:"notes,omitempty"`
	ActivatedAt       string  `json:"activated_at"`
	SuspendedAt       *string `json:"suspended_at,omitempty"`
	TerminatedAt      *string `json:"terminated_at,omitempty"`
	CreatedAt         string  `json:"created_at"`
	UpdatedAt         string  `json:"updated_at"`
	SuspendReason     string  `json:"suspend_reason,omitempty"`
}

func toSubscriberDTO(s domain.Subscriber) subscriberDTO {
	return subscriberDTO{
		ID:                s.ID.String(),
		ResellerAccountID: s.ResellerAccountID.String(),
		CustomerName:      s.CustomerName,
		CustomerEmail:     s.CustomerEmail,
		CustomerPhone:     s.CustomerPhone,
		AddressLine:       s.AddressLine,
		SubAreaID:         uuidPtrString(s.SubAreaID),
		ServicePlanID:     uuidPtrString(s.ServicePlanID),
		MonthlyFee:        s.MonthlyFee,
		Status:            string(s.Status),
		Notes:             s.Notes,
		ActivatedAt:       s.ActivatedAt.UTC().Format(time.RFC3339),
		SuspendedAt:       rfc3339Ptr(s.SuspendedAt),
		TerminatedAt:      rfc3339Ptr(s.TerminatedAt),
		CreatedAt:         s.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:         s.UpdatedAt.UTC().Format(time.RFC3339),
		SuspendReason:     s.SuspendReason,
	}
}

type createSubscriberRequest struct {
	// ResellerAccountID is optional — the usecase pins it to the
	// resolved tenant. We accept it so a client that wants to be
	// explicit (or a future admin surface) doesn't have to omit it,
	// and the usecase refuses any mismatch with Forbidden /
	// `subscriber.cross_tenant`.
	ResellerAccountID string  `json:"reseller_account_id,omitempty"`
	CustomerName      string  `json:"customer_name"`
	CustomerEmail     string  `json:"customer_email,omitempty"`
	CustomerPhone     string  `json:"customer_phone,omitempty"`
	AddressLine       string  `json:"address_line,omitempty"`
	SubAreaID         *string `json:"sub_area_id,omitempty"`
	ServicePlanID     *string `json:"service_plan_id,omitempty"`
	MonthlyFee        float64 `json:"monthly_fee"`
	Notes             string  `json:"notes,omitempty"`
}

type updateSubscriberRequest struct {
	CustomerName  *string  `json:"customer_name,omitempty"`
	CustomerEmail *string  `json:"customer_email,omitempty"`
	CustomerPhone *string  `json:"customer_phone,omitempty"`
	AddressLine   *string  `json:"address_line,omitempty"`
	SubAreaID     *string  `json:"sub_area_id,omitempty"`
	ServicePlanID *string  `json:"service_plan_id,omitempty"`
	MonthlyFee    *float64 `json:"monthly_fee,omitempty"`
	Notes         *string  `json:"notes,omitempty"`
}

type suspendSubscriberRequest struct {
	Reason string `json:"reason"`
}

// =====================================================================
// Invoice DTOs
// =====================================================================

type subscriberInvoiceDTO struct {
	ID                string  `json:"id"`
	ResellerAccountID string  `json:"reseller_account_id"`
	SubscriberID      string  `json:"subscriber_id"`
	InvoiceNo         string  `json:"invoice_no"`
	PeriodYear        int     `json:"period_year,omitempty"`
	PeriodMonth       int     `json:"period_month,omitempty"`
	Amount            float64 `json:"amount"`
	Status            string  `json:"status"`
	IssuedAt          string  `json:"issued_at"`
	DueAt             *string `json:"due_at,omitempty"`
	PaidAt            *string `json:"paid_at,omitempty"`
	CreatedAt         string  `json:"created_at"`
	UpdatedAt         string  `json:"updated_at"`
}

func toSubscriberInvoiceDTO(i domain.SubscriberInvoice) subscriberInvoiceDTO {
	return subscriberInvoiceDTO{
		ID:                i.ID.String(),
		ResellerAccountID: i.ResellerAccountID.String(),
		SubscriberID:      i.SubscriberID.String(),
		InvoiceNo:         i.InvoiceNo,
		PeriodYear:        i.PeriodYear,
		PeriodMonth:       i.PeriodMonth,
		Amount:            i.Amount,
		Status:            string(i.Status),
		IssuedAt:          i.IssuedAt.UTC().Format(time.RFC3339),
		DueAt:             rfc3339Ptr(i.DueAt),
		PaidAt:            rfc3339Ptr(i.PaidAt),
		CreatedAt:         i.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:         i.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// =====================================================================
// Import DTOs
// =====================================================================

type subscriberImportDTO struct {
	ID                string                  `json:"id"`
	ResellerAccountID string                  `json:"reseller_account_id"`
	Source            string                  `json:"source,omitempty"`
	TotalRows         int                     `json:"total_rows"`
	OKRows            int                     `json:"ok_rows"`
	ErrorRows         int                     `json:"error_rows"`
	Status            string                  `json:"status"`
	ErrorSummary      []domain.ImportRowError `json:"error_summary,omitempty"`
	CreatedAt         string                  `json:"created_at"`
	CompletedAt       *string                 `json:"completed_at,omitempty"`
}

func toSubscriberImportDTO(im domain.SubscriberImport) subscriberImportDTO {
	return subscriberImportDTO{
		ID:                im.ID.String(),
		ResellerAccountID: im.ResellerAccountID.String(),
		Source:            im.Source,
		TotalRows:         im.TotalRows,
		OKRows:            im.OKRows,
		ErrorRows:         im.ErrorRows,
		Status:            string(im.Status),
		ErrorSummary:      im.ErrorSummary,
		CreatedAt:         im.CreatedAt.UTC().Format(time.RFC3339),
		CompletedAt:       rfc3339Ptr(im.CompletedAt),
	}
}

// =====================================================================
// Dashboard DTO — port.MTDDashboard is already JSON-tagged. We expose
// it directly without an indirection layer; the wrapper here just
// gives the field a name in case future fields are added.
// =====================================================================

type mtdDashboardDTO = port.MTDDashboard

// parseOptionalUUIDStr converts a *string into a *uuid.UUID, treating
// nil and empty string as "not set". Used by the update request body
// parsers.
func parseOptionalUUIDStr(s *string, field string) (*uuid.UUID, error) {
	if s == nil || *s == "" {
		return nil, nil
	}
	u, err := parseUUID(*s, field)
	if err != nil {
		return nil, err
	}
	return &u, nil
}
