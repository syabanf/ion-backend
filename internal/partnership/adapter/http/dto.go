// Package http is the driving adapter for the partnership bounded
// context. Same conventions as reseller / enterprise / warehouse:
//   - One handler per surface (Wave 100 ships a single Handler).
//   - DTOs live next to the handler they're used by.
//   - FormatRFC3339 + ParseUUIDParam via pkg/httpserver.
package http

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/partnership/domain"
	"github.com/ion-core/backend/pkg/errors"
	"github.com/ion-core/backend/pkg/httpserver"
)

// actorUserID pulls the authenticated user's UUID from the request
// context. Returns nil when no claims are attached (shouldn't happen
// behind RequireAuth, but defensive). Mirror of the reseller helper.
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

func writeJSON(w http.ResponseWriter, status int, body any) {
	httpserver.WriteJSON(w, status, body)
}

func writeErr(w http.ResponseWriter, err error) {
	httpserver.WriteError(w, err)
}

// =====================================================================
// Agreement DTO + mapping
// =====================================================================

type agreementDTO struct {
	ID                     string         `json:"id"`
	ResellerAccountID      string         `json:"reseller_account_id"`
	TermsJSON              map[string]any `json:"terms_json"`
	RevsharePct            float64        `json:"revshare_pct"`
	RampMonths             int            `json:"ramp_months"`
	ComplianceThresholdPct float64        `json:"compliance_threshold_pct"`
	EffectiveFrom          string         `json:"effective_from"`
	EffectiveTo            *string        `json:"effective_to,omitempty"`
	SignedBy               *string        `json:"signed_by,omitempty"`
	SignedAt               *string        `json:"signed_at,omitempty"`
	CreatedAt              string         `json:"created_at"`
	UpdatedAt              string         `json:"updated_at"`
}

func toAgreementDTO(a domain.Agreement) agreementDTO {
	var effectiveTo *string
	if a.EffectiveTo != nil {
		s := a.EffectiveTo.UTC().Format("2006-01-02")
		effectiveTo = &s
	}
	return agreementDTO{
		ID:                     a.ID.String(),
		ResellerAccountID:      a.ResellerAccountID.String(),
		TermsJSON:              a.TermsJSON,
		RevsharePct:            a.RevsharePct,
		RampMonths:             a.RampMonths,
		ComplianceThresholdPct: a.ComplianceThresholdPct,
		EffectiveFrom:          a.EffectiveFrom.UTC().Format("2006-01-02"),
		EffectiveTo:            effectiveTo,
		SignedBy:               uuidPtrString(a.SignedBy),
		SignedAt:               rfc3339Ptr(a.SignedAt),
		CreatedAt:              a.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:              a.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

type createAgreementRequest struct {
	ResellerAccountID      string         `json:"reseller_account_id"`
	TermsJSON              map[string]any `json:"terms_json"`
	RevsharePct            float64        `json:"revshare_pct"`
	RampMonths             int            `json:"ramp_months"`
	ComplianceThresholdPct float64        `json:"compliance_threshold_pct"`
	EffectiveFrom          string         `json:"effective_from"` // YYYY-MM-DD
	EffectiveTo            *string        `json:"effective_to,omitempty"`
}

// =====================================================================
// Monthly submission DTO + mapping
// =====================================================================

type submissionDTO struct {
	ID                string   `json:"id"`
	AgreementID       string   `json:"agreement_id"`
	ResellerAccountID string   `json:"reseller_account_id"`
	PeriodYear        int      `json:"period_year"`
	PeriodMonth       int      `json:"period_month"`
	Status            string   `json:"status"`
	GrossRevenue      *float64 `json:"gross_revenue,omitempty"`
	NetRevenue        *float64 `json:"net_revenue,omitempty"`
	SubscriberCount   *int     `json:"subscriber_count,omitempty"`
	ChurnCount        *int     `json:"churn_count,omitempty"`
	EvidenceURL       string   `json:"evidence_url,omitempty"`
	EvidenceHash      string   `json:"evidence_hash,omitempty"`
	SubmittedBy       *string  `json:"submitted_by,omitempty"`
	SubmittedAt       *string  `json:"submitted_at,omitempty"`
	ConfirmedBy       *string  `json:"confirmed_by,omitempty"`
	ConfirmedAt       *string  `json:"confirmed_at,omitempty"`
	ReturnedReason    string   `json:"returned_reason,omitempty"`
	ReturnedAt        *string  `json:"returned_at,omitempty"`
	CreatedAt         string   `json:"created_at"`
	UpdatedAt         string   `json:"updated_at"`
}

func toSubmissionDTO(s domain.MonthlySubmission) submissionDTO {
	return submissionDTO{
		ID:                s.ID.String(),
		AgreementID:       s.AgreementID.String(),
		ResellerAccountID: s.ResellerAccountID.String(),
		PeriodYear:        s.PeriodYear,
		PeriodMonth:       s.PeriodMonth,
		Status:            string(s.Status),
		GrossRevenue:      s.GrossRevenue,
		NetRevenue:        s.NetRevenue,
		SubscriberCount:   s.SubscriberCount,
		ChurnCount:        s.ChurnCount,
		EvidenceURL:       s.EvidenceURL,
		EvidenceHash:      s.EvidenceHash,
		SubmittedBy:       uuidPtrString(s.SubmittedBy),
		SubmittedAt:       rfc3339Ptr(s.SubmittedAt),
		ConfirmedBy:       uuidPtrString(s.ConfirmedBy),
		ConfirmedAt:       rfc3339Ptr(s.ConfirmedAt),
		ReturnedReason:    s.ReturnedReason,
		ReturnedAt:        rfc3339Ptr(s.ReturnedAt),
		CreatedAt:         s.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:         s.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

type createSubmissionRequest struct {
	ResellerAccountID string `json:"reseller_account_id"`
	PeriodYear        int    `json:"period_year"`
	PeriodMonth       int    `json:"period_month"`
}

type updateSubmissionRequest struct {
	GrossRevenue    *float64 `json:"gross_revenue,omitempty"`
	NetRevenue      *float64 `json:"net_revenue,omitempty"`
	SubscriberCount *int     `json:"subscriber_count,omitempty"`
	ChurnCount      *int     `json:"churn_count,omitempty"`
	EvidenceURL     *string  `json:"evidence_url,omitempty"`
	EvidenceHash    *string  `json:"evidence_hash,omitempty"`
}

type returnSubmissionRequest struct {
	Reason string `json:"reason"`
}

// =====================================================================
// Settlement DTO + mapping
// =====================================================================

type settlementDTO struct {
	ID                     string         `json:"id"`
	SubmissionID           string         `json:"submission_id"`
	AgreementID            string         `json:"agreement_id"`
	AgreementTermsSnapshot map[string]any `json:"agreement_terms_snapshot"`
	PeriodYear             int            `json:"period_year"`
	PeriodMonth            int            `json:"period_month"`
	GrossRevenue           float64        `json:"gross_revenue"`
	NetRevenue             float64        `json:"net_revenue"`
	RevshareAmount         float64        `json:"revshare_amount"`
	TaxAmount              float64        `json:"tax_amount"`
	PayableAmount          float64        `json:"payable_amount"`
	FormulaHash            string         `json:"formula_hash"`
	FormulaHashValid       bool           `json:"formula_hash_valid"`
	Status                 string         `json:"status"`
	PDFURL                 string         `json:"pdf_url,omitempty"`
	PDFHash                string         `json:"pdf_hash,omitempty"`
	ApprovedBy             *string        `json:"approved_by,omitempty"`
	ApprovedAt             *string        `json:"approved_at,omitempty"`
	PaidAt                 *string        `json:"paid_at,omitempty"`
	CreatedAt              string         `json:"created_at"`
	UpdatedAt              string         `json:"updated_at"`
}

func toSettlementDTO(s domain.Settlement) settlementDTO {
	return settlementDTO{
		ID:                     s.ID.String(),
		SubmissionID:           s.SubmissionID.String(),
		AgreementID:            s.AgreementID.String(),
		AgreementTermsSnapshot: s.AgreementTermsSnapshot,
		PeriodYear:             s.PeriodYear,
		PeriodMonth:            s.PeriodMonth,
		GrossRevenue:           s.GrossRevenue,
		NetRevenue:             s.NetRevenue,
		RevshareAmount:         s.RevshareAmount,
		TaxAmount:              s.TaxAmount,
		PayableAmount:          s.PayableAmount,
		FormulaHash:            s.FormulaHash,
		FormulaHashValid:       s.VerifyFormulaHash(),
		Status:                 string(s.Status),
		PDFURL:                 s.PDFURL,
		PDFHash:                s.PDFHash,
		ApprovedBy:             uuidPtrString(s.ApprovedBy),
		ApprovedAt:             rfc3339Ptr(s.ApprovedAt),
		PaidAt:                 rfc3339Ptr(s.PaidAt),
		CreatedAt:              s.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:              s.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

type markPaidRequest struct {
	PaidAt string `json:"paid_at,omitempty"` // RFC3339; defaults to NOW when empty
}

// =====================================================================
// Compliance DTO + mapping
// =====================================================================

type complianceDTO struct {
	ID                string  `json:"id"`
	ResellerAccountID string  `json:"reseller_account_id"`
	AgreementID       string  `json:"agreement_id"`
	PeriodYear        int     `json:"period_year"`
	PeriodMonth       int     `json:"period_month"`
	ThresholdPct      float64 `json:"threshold_pct"`
	AchievedPct       float64 `json:"achieved_pct"`
	Status            string  `json:"status"`
	Reason            string  `json:"reason,omitempty"`
	EvaluatedAt       string  `json:"evaluated_at"`
}

func toComplianceDTO(e domain.ComplianceEvaluation) complianceDTO {
	return complianceDTO{
		ID:                e.ID.String(),
		ResellerAccountID: e.ResellerAccountID.String(),
		AgreementID:       e.AgreementID.String(),
		PeriodYear:        e.PeriodYear,
		PeriodMonth:       e.PeriodMonth,
		ThresholdPct:      e.ThresholdPct,
		AchievedPct:       e.AchievedPct,
		Status:            string(e.Status),
		Reason:            e.Reason,
		EvaluatedAt:       e.EvaluatedAt.UTC().Format(time.RFC3339),
	}
}
