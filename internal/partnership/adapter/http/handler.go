package http

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/partnership/port"
	"github.com/ion-core/backend/internal/partnership/usecase"
	"github.com/ion-core/backend/pkg/auth"
	"github.com/ion-core/backend/pkg/errors"
	"github.com/ion-core/backend/pkg/httpserver"
)

// Handler is the single HTTP surface for the partnership bounded
// context (admin + reseller-facing share this surface; tenant scoping
// is enforced by the caller's permission set rather than a separate
// path prefix, mirroring how enterprise + warehouse expose their
// admin/reseller views).
//
// Routes are documented in the Mount method. Auth chain follows the
// enterprise pattern: RequireAuth(verifier) + RequirePermission(...).
type Handler struct {
	agreements  *usecase.AgreementService
	submissions *usecase.SubmissionService
	settlements *usecase.SettlementService
	compliances *usecase.ComplianceService
	verifier    *auth.Verifier
}

func NewHandler(
	agreements *usecase.AgreementService,
	submissions *usecase.SubmissionService,
	settlements *usecase.SettlementService,
	compliances *usecase.ComplianceService,
	verifier *auth.Verifier,
) *Handler {
	return &Handler{
		agreements:  agreements,
		submissions: submissions,
		settlements: settlements,
		compliances: compliances,
		verifier:    verifier,
	}
}

// Mount wires every partnership route onto the supplied chi router.
//
// Routes:
//
//	# Agreements
//	GET    /api/partnership/agreements                [partnership.agreement.read]
//	POST   /api/partnership/agreements                [partnership.agreement.write]
//	GET    /api/partnership/agreements/{id}           [partnership.agreement.read]
//
//	# Submissions
//	GET    /api/partnership/submissions               [partnership.submission.read]
//	POST   /api/partnership/submissions               [partnership.submission.write]
//	PATCH  /api/partnership/submissions/{id}          [partnership.submission.write]
//	POST   /api/partnership/submissions/{id}/submit   [partnership.submission.write]
//	POST   /api/partnership/submissions/{id}/confirm  [partnership.submission.confirm]
//	POST   /api/partnership/submissions/{id}/return   [partnership.submission.confirm]
//	POST   /api/partnership/submissions/{id}/cancel   [partnership.submission.write]
//
//	# Settlements
//	GET    /api/partnership/settlements               [partnership.settlement.read]
//	GET    /api/partnership/settlements/{id}          [partnership.settlement.read]
//	POST   /api/partnership/settlements/{id}/approve  [partnership.settlement.approve]
//	POST   /api/partnership/settlements/{id}/mark-paid [partnership.settlement.approve]
//
//	# Compliance
//	GET    /api/partnership/compliance                                       [partnership.compliance.read]
//	GET    /api/partnership/compliance/{reseller_id}/{year}/{month}          [partnership.compliance.read]
//	POST   /api/partnership/compliance/evaluate/{year}/{month}               [partnership.compliance.read]
func (h *Handler) Mount(r chi.Router) {
	r.Route("/api/partnership", func(r chi.Router) {
		r.Use(httpserver.RequireAuth(h.verifier))

		// Agreements
		r.With(httpserver.RequirePermission("partnership.agreement.read")).
			Get("/agreements", h.listAgreements)
		r.With(httpserver.RequirePermission("partnership.agreement.write")).
			Post("/agreements", h.createAgreement)
		r.With(httpserver.RequirePermission("partnership.agreement.read")).
			Get("/agreements/{id}", h.getAgreement)

		// Submissions
		r.With(httpserver.RequirePermission("partnership.submission.read")).
			Get("/submissions", h.listSubmissions)
		r.With(httpserver.RequirePermission("partnership.submission.write")).
			Post("/submissions", h.draftSubmission)
		r.With(httpserver.RequirePermission("partnership.submission.read")).
			Get("/submissions/{id}", h.getSubmission)
		r.With(httpserver.RequirePermission("partnership.submission.write")).
			Patch("/submissions/{id}", h.updateSubmission)
		r.With(httpserver.RequirePermission("partnership.submission.write")).
			Post("/submissions/{id}/submit", h.submitSubmission)
		r.With(httpserver.RequirePermission("partnership.submission.confirm")).
			Post("/submissions/{id}/confirm", h.confirmSubmission)
		r.With(httpserver.RequirePermission("partnership.submission.confirm")).
			Post("/submissions/{id}/return", h.returnSubmission)
		r.With(httpserver.RequirePermission("partnership.submission.write")).
			Post("/submissions/{id}/cancel", h.cancelSubmission)

		// Settlements
		r.With(httpserver.RequirePermission("partnership.settlement.read")).
			Get("/settlements", h.listSettlements)
		r.With(httpserver.RequirePermission("partnership.settlement.read")).
			Get("/settlements/{id}", h.getSettlement)
		r.With(httpserver.RequirePermission("partnership.settlement.approve")).
			Post("/settlements/{id}/approve", h.approveSettlement)
		r.With(httpserver.RequirePermission("partnership.settlement.approve")).
			Post("/settlements/{id}/mark-paid", h.markSettlementPaid)

		// Compliance
		r.With(httpserver.RequirePermission("partnership.compliance.read")).
			Get("/compliance", h.listCompliance)
		r.With(httpserver.RequirePermission("partnership.compliance.read")).
			Get("/compliance/{reseller_id}/{year}/{month}", h.getCompliance)
		r.With(httpserver.RequirePermission("partnership.compliance.read")).
			Post("/compliance/evaluate/{year}/{month}", h.evaluateCompliance)
	})
}

// ---------------------------------------------------------------------
// Agreements
// ---------------------------------------------------------------------

func (h *Handler) listAgreements(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page := httpserver.ParseIntDefault(q.Get("page"), 1)
	if page < 1 {
		page = 1
	}
	pageSize := httpserver.ParseIntDefault(q.Get("page_size"), 50)
	f := port.AgreementListFilter{
		Limit:  pageSize,
		Offset: (page - 1) * pageSize,
	}
	if s := q.Get("reseller_account_id"); s != "" {
		u, err := parseUUID(s, "reseller_account_id")
		if err != nil {
			writeErr(w, err)
			return
		}
		f.ResellerAccountID = &u
	}
	if s := q.Get("active_at"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			writeErr(w, errors.Validation("agreement.active_at_invalid", "active_at must be RFC3339"))
			return
		}
		f.ActiveAt = &t
	}
	items, total, err := h.agreements.ListAgreements(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]agreementDTO, 0, len(items))
	for _, a := range items {
		out = append(out, toAgreementDTO(a))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":     out,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

func (h *Handler) createAgreement(w http.ResponseWriter, r *http.Request) {
	var req createAgreementRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	reseller, err := parseUUID(req.ResellerAccountID, "reseller_account_id")
	if err != nil {
		writeErr(w, err)
		return
	}
	effFrom, err := time.Parse("2006-01-02", req.EffectiveFrom)
	if err != nil {
		writeErr(w, errors.Validation("agreement.effective_from_invalid", "effective_from must be YYYY-MM-DD"))
		return
	}
	in := port.CreateAgreementInput{
		ResellerAccountID:      reseller,
		TermsJSON:              req.TermsJSON,
		RevsharePct:            req.RevsharePct,
		RampMonths:             req.RampMonths,
		ComplianceThresholdPct: req.ComplianceThresholdPct,
		EffectiveFrom:          effFrom,
	}
	if req.EffectiveTo != nil && *req.EffectiveTo != "" {
		t, err := time.Parse("2006-01-02", *req.EffectiveTo)
		if err != nil {
			writeErr(w, errors.Validation("agreement.effective_to_invalid", "effective_to must be YYYY-MM-DD"))
			return
		}
		in.EffectiveTo = &t
	}
	if u := actorUserID(r.Context()); u != nil {
		in.SignedBy = u
	}
	a, err := h.agreements.CreateAgreement(r.Context(), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toAgreementDTO(*a))
}

func (h *Handler) getAgreement(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "agreement")
	if !ok {
		return
	}
	a, err := h.agreements.GetAgreement(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toAgreementDTO(*a))
}

// ---------------------------------------------------------------------
// Submissions
// ---------------------------------------------------------------------

func (h *Handler) listSubmissions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page := httpserver.ParseIntDefault(q.Get("page"), 1)
	if page < 1 {
		page = 1
	}
	pageSize := httpserver.ParseIntDefault(q.Get("page_size"), 50)
	f := port.SubmissionListFilter{
		Status: q.Get("status"),
		Limit:  pageSize,
		Offset: (page - 1) * pageSize,
	}
	if s := q.Get("reseller_account_id"); s != "" {
		u, err := parseUUID(s, "reseller_account_id")
		if err != nil {
			writeErr(w, err)
			return
		}
		f.ResellerAccountID = &u
	}
	if s := q.Get("period_year"); s != "" {
		y, err := strconv.Atoi(s)
		if err != nil {
			writeErr(w, errors.Validation("submission.period_year_invalid", "period_year must be an integer"))
			return
		}
		f.PeriodYear = &y
	}
	if s := q.Get("period_month"); s != "" {
		m, err := strconv.Atoi(s)
		if err != nil {
			writeErr(w, errors.Validation("submission.period_month_invalid", "period_month must be an integer"))
			return
		}
		f.PeriodMonth = &m
	}
	items, total, err := h.submissions.ListSubmissions(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]submissionDTO, 0, len(items))
	for _, s := range items {
		out = append(out, toSubmissionDTO(s))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":     out,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

func (h *Handler) draftSubmission(w http.ResponseWriter, r *http.Request) {
	var req createSubmissionRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	reseller, err := parseUUID(req.ResellerAccountID, "reseller_account_id")
	if err != nil {
		writeErr(w, err)
		return
	}
	s, err := h.submissions.DraftSubmission(r.Context(), port.DraftSubmissionInput{
		ResellerAccountID: reseller,
		PeriodYear:        req.PeriodYear,
		PeriodMonth:       req.PeriodMonth,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toSubmissionDTO(*s))
}

func (h *Handler) getSubmission(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "submission")
	if !ok {
		return
	}
	s, err := h.submissions.GetSubmission(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSubmissionDTO(*s))
}

func (h *Handler) updateSubmission(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "submission")
	if !ok {
		return
	}
	var req updateSubmissionRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	s, err := h.submissions.UpdateSubmission(r.Context(), port.UpdateSubmissionInput{
		ID:              id,
		GrossRevenue:    req.GrossRevenue,
		NetRevenue:      req.NetRevenue,
		SubscriberCount: req.SubscriberCount,
		ChurnCount:      req.ChurnCount,
		EvidenceURL:     req.EvidenceURL,
		EvidenceHash:    req.EvidenceHash,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSubmissionDTO(*s))
}

func (h *Handler) submitSubmission(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "submission")
	if !ok {
		return
	}
	actor := uuid.Nil
	if u := actorUserID(r.Context()); u != nil {
		actor = *u
	}
	if actor == uuid.Nil {
		writeErr(w, errors.Unauthorized("auth.missing", "authentication required"))
		return
	}
	s, err := h.submissions.SubmitForReview(r.Context(), id, actor)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSubmissionDTO(*s))
}

func (h *Handler) confirmSubmission(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "submission")
	if !ok {
		return
	}
	actor := uuid.Nil
	if u := actorUserID(r.Context()); u != nil {
		actor = *u
	}
	if actor == uuid.Nil {
		writeErr(w, errors.Unauthorized("auth.missing", "authentication required"))
		return
	}
	sub, stl, err := h.submissions.ConfirmSubmission(r.Context(), id, actor)
	if err != nil {
		writeErr(w, err)
		return
	}
	resp := map[string]any{
		"submission": toSubmissionDTO(*sub),
	}
	if stl != nil {
		resp["settlement"] = toSettlementDTO(*stl)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) returnSubmission(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "submission")
	if !ok {
		return
	}
	var req returnSubmissionRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	actor := uuid.Nil
	if u := actorUserID(r.Context()); u != nil {
		actor = *u
	}
	if actor == uuid.Nil {
		writeErr(w, errors.Unauthorized("auth.missing", "authentication required"))
		return
	}
	s, err := h.submissions.ReturnSubmission(r.Context(), id, req.Reason, actor)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSubmissionDTO(*s))
}

func (h *Handler) cancelSubmission(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "submission")
	if !ok {
		return
	}
	s, err := h.submissions.CancelSubmission(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSubmissionDTO(*s))
}

// ---------------------------------------------------------------------
// Settlements
// ---------------------------------------------------------------------

func (h *Handler) listSettlements(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page := httpserver.ParseIntDefault(q.Get("page"), 1)
	if page < 1 {
		page = 1
	}
	pageSize := httpserver.ParseIntDefault(q.Get("page_size"), 50)
	f := port.SettlementListFilter{
		Status: q.Get("status"),
		Limit:  pageSize,
		Offset: (page - 1) * pageSize,
	}
	if s := q.Get("reseller_account_id"); s != "" {
		u, err := parseUUID(s, "reseller_account_id")
		if err != nil {
			writeErr(w, err)
			return
		}
		f.ResellerAccountID = &u
	}
	if s := q.Get("period_year"); s != "" {
		y, err := strconv.Atoi(s)
		if err != nil {
			writeErr(w, errors.Validation("settlement.period_year_invalid", "period_year must be an integer"))
			return
		}
		f.PeriodYear = &y
	}
	if s := q.Get("period_month"); s != "" {
		m, err := strconv.Atoi(s)
		if err != nil {
			writeErr(w, errors.Validation("settlement.period_month_invalid", "period_month must be an integer"))
			return
		}
		f.PeriodMonth = &m
	}
	items, total, err := h.settlements.ListSettlements(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]settlementDTO, 0, len(items))
	for _, s := range items {
		out = append(out, toSettlementDTO(s))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":     out,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

func (h *Handler) getSettlement(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "settlement")
	if !ok {
		return
	}
	s, err := h.settlements.GetSettlement(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSettlementDTO(*s))
}

func (h *Handler) approveSettlement(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "settlement")
	if !ok {
		return
	}
	actor := uuid.Nil
	if u := actorUserID(r.Context()); u != nil {
		actor = *u
	}
	if actor == uuid.Nil {
		writeErr(w, errors.Unauthorized("auth.missing", "authentication required"))
		return
	}
	s, err := h.settlements.ApproveSettlement(r.Context(), id, actor)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSettlementDTO(*s))
}

func (h *Handler) markSettlementPaid(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "settlement")
	if !ok {
		return
	}
	var req markPaidRequest
	// PaidAt is optional, so we tolerate an empty body.
	_ = httpserver.DecodeJSON(r, &req)
	at := time.Now().UTC()
	if req.PaidAt != "" {
		t, err := time.Parse(time.RFC3339, req.PaidAt)
		if err != nil {
			writeErr(w, errors.Validation("settlement.paid_at_invalid", "paid_at must be RFC3339"))
			return
		}
		at = t.UTC()
	}
	s, err := h.settlements.MarkSettlementPaid(r.Context(), id, at)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSettlementDTO(*s))
}

// ---------------------------------------------------------------------
// Compliance
// ---------------------------------------------------------------------

func (h *Handler) listCompliance(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page := httpserver.ParseIntDefault(q.Get("page"), 1)
	if page < 1 {
		page = 1
	}
	pageSize := httpserver.ParseIntDefault(q.Get("page_size"), 50)
	f := port.ComplianceListFilter{
		Status: q.Get("status"),
		Limit:  pageSize,
		Offset: (page - 1) * pageSize,
	}
	if s := q.Get("reseller_account_id"); s != "" {
		u, err := parseUUID(s, "reseller_account_id")
		if err != nil {
			writeErr(w, err)
			return
		}
		f.ResellerAccountID = &u
	}
	if s := q.Get("period_year"); s != "" {
		y, err := strconv.Atoi(s)
		if err != nil {
			writeErr(w, errors.Validation("compliance.period_year_invalid", "period_year must be an integer"))
			return
		}
		f.PeriodYear = &y
	}
	if s := q.Get("period_month"); s != "" {
		m, err := strconv.Atoi(s)
		if err != nil {
			writeErr(w, errors.Validation("compliance.period_month_invalid", "period_month must be an integer"))
			return
		}
		f.PeriodMonth = &m
	}
	items, total, err := h.compliances.ListEvaluations(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]complianceDTO, 0, len(items))
	for _, e := range items {
		out = append(out, toComplianceDTO(e))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":     out,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

func (h *Handler) getCompliance(w http.ResponseWriter, r *http.Request) {
	resellerStr := chi.URLParam(r, "reseller_id")
	resellerID, err := parseUUID(resellerStr, "reseller_id")
	if err != nil {
		writeErr(w, err)
		return
	}
	year, err := strconv.Atoi(chi.URLParam(r, "year"))
	if err != nil {
		writeErr(w, errors.Validation("compliance.year_invalid", "year must be an integer"))
		return
	}
	month, err := strconv.Atoi(chi.URLParam(r, "month"))
	if err != nil {
		writeErr(w, errors.Validation("compliance.month_invalid", "month must be an integer"))
		return
	}
	e, err := h.compliances.GetEvaluation(r.Context(), resellerID, year, month)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toComplianceDTO(*e))
}

// evaluateCompliance is the admin trigger — runs the evaluator
// synchronously for the supplied (year, month). Returns the summary.
func (h *Handler) evaluateCompliance(w http.ResponseWriter, r *http.Request) {
	year, err := strconv.Atoi(chi.URLParam(r, "year"))
	if err != nil {
		writeErr(w, errors.Validation("compliance.year_invalid", "year must be an integer"))
		return
	}
	month, err := strconv.Atoi(chi.URLParam(r, "month"))
	if err != nil {
		writeErr(w, errors.Validation("compliance.month_invalid", "month must be an integer"))
		return
	}
	summary, err := h.compliances.EvaluateMonth(r.Context(), year, month)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"year":                  year,
		"month":                 month,
		"evaluated":             summary.Evaluated,
		"ramp_skipped":          summary.RampSkipped,
		"passed":                summary.Passed,
		"breached":              summary.Breached,
		"skipped_no_submission": summary.Skipped,
	})
}
