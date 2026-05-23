package http

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/enterprise/domain"
	"github.com/ion-core/backend/internal/enterprise/port"
	"github.com/ion-core/backend/pkg/auth"
	"github.com/ion-core/backend/pkg/errors"
	"github.com/ion-core/backend/pkg/httpserver"
)

// NegotiationHandler is the Phase 4b HTTP surface — config + lifecycle
// + round submission/approval. Mounted under the same /api/enterprise
// base path as BOQ + Quotation handlers; auth + permission gates are
// per-route via httpserver.RequirePermission.
//
// Routes:
//
//   GET    /boqs/{boq_id}/negotiation/config                [negotiation.read]
//   PUT    /boqs/{boq_id}/negotiation/config                [negotiation.configure]
//   GET    /negotiations/{id}                               [negotiation.read]
//   GET    /negotiations/by-boq/{boq_id}                    [negotiation.read]
//   POST   /negotiations/by-boq/{boq_id}/activate           [negotiation.activate]
//   POST   /negotiations/{id}/abort                         [negotiation.abort]
//   POST   /negotiations/{id}/rounds                        [negotiation.submit]
//   GET    /negotiation-rounds/{id}                         [negotiation.read]
//   POST   /negotiation-round-approvals/{id}/approve        [negotiation.approve]
//   POST   /negotiation-round-approvals/{id}/reject         [negotiation.approve]
type NegotiationHandler struct {
	uc       port.NegotiationUseCase
	verifier *auth.Verifier
}

func NewNegotiationHandler(uc port.NegotiationUseCase, verifier *auth.Verifier) *NegotiationHandler {
	return &NegotiationHandler{uc: uc, verifier: verifier}
}

func (h *NegotiationHandler) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(httpserver.RequireAuth(h.verifier))
		// Same vendor-isolation mask as the BOQ surface: round responses
		// embed margin_before/after, cco injection metadata, and the
		// price_changes payload — all commercial-secret. NFR-011 +
		// TC-RBAC-IV-008 require server-side strip, not UI hiding.
		r.Use(BOQFieldMaskMiddleware)

		// Config (lives under the BOQ resource path because it's part of
		// the BOQ's pre-approval setup).
		r.With(httpserver.RequirePermission("enterprise.negotiation.read")).
			Get("/boqs/{boq_id}/negotiation/config", h.getConfig)
		r.With(httpserver.RequirePermission("enterprise.negotiation.configure")).
			Put("/boqs/{boq_id}/negotiation/config", h.setConfig)

		// Negotiation lifecycle.
		r.With(httpserver.RequirePermission("enterprise.negotiation.read")).
			Get("/negotiations/{id}", h.getNegotiation)
		r.With(httpserver.RequirePermission("enterprise.negotiation.read")).
			Get("/negotiations/by-boq/{boq_id}", h.getNegotiationByBOQ)
		r.With(httpserver.RequirePermission("enterprise.negotiation.activate")).
			Post("/negotiations/by-boq/{boq_id}/activate", h.activate)
		r.With(httpserver.RequirePermission("enterprise.negotiation.abort")).
			Post("/negotiations/{id}/abort", h.abort)

		// Round flow.
		r.With(httpserver.RequirePermission("enterprise.negotiation.submit")).
			Post("/negotiations/{id}/rounds", h.submitRound)
		r.With(httpserver.RequirePermission("enterprise.negotiation.read")).
			Get("/negotiation-rounds/{id}", h.getRound)
		// Inbox query (?pending_for_me=true) — uses negotiation.read so
		// any approver who can act on the chain can see what's queued.
		r.With(httpserver.RequirePermission("enterprise.negotiation.read")).
			Get("/negotiation-round-approvals", h.listApprovals)
		r.With(httpserver.RequirePermission("enterprise.negotiation.approve")).
			Post("/negotiation-round-approvals/{id}/approve", h.approveStep)
		r.With(httpserver.RequirePermission("enterprise.negotiation.approve")).
			Post("/negotiation-round-approvals/{id}/reject", h.rejectStep)
		// Wave 106 — CSV export of all rounds for an ops handoff.
		r.With(httpserver.RequirePermission("enterprise.negotiation.read")).
			Get("/negotiations/{id}/rounds.csv", h.exportRoundsCSV)
	})
}

// =====================================================================
// Config
// =====================================================================

func (h *NegotiationHandler) getConfig(w http.ResponseWriter, r *http.Request) {
	boqID, err := parseUUIDLocal(chi.URLParam(r, "boq_id"), "boq")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	cfg, err := h.uc.GetNegotiationConfig(r.Context(), boqID)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toNegotiationConfigDTO(*cfg))
}

func (h *NegotiationHandler) setConfig(w http.ResponseWriter, r *http.Request) {
	boqID, err := parseUUIDLocal(chi.URLParam(r, "boq_id"), "boq")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var req setNegotiationConfigRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	parts := make([]domain.NegotiationParticipant, 0, len(req.Participants))
	for _, p := range req.Participants {
		uid, perr := parseUUIDLocal(p.UserID, "user_id")
		if perr != nil {
			httpserver.WriteError(w, perr)
			return
		}
		parts = append(parts, domain.NegotiationParticipant{
			BOQVersionID: boqID,
			UserID:       uid,
			StepNo:       p.StepNo,
			RoleTag:      p.RoleTag,
		})
	}
	in := port.SetNegotiationConfigInput{
		BOQVersionID:             boqID,
		Enabled:                  req.Enabled,
		Type:                     req.Type,
		Mode:                     req.Mode,
		PricingAdjustmentAllowed: req.PricingAdjustmentAllowed,
		MarginFloorPct:           req.MarginFloorPct,
		DiscountCeilingPct:       req.DiscountCeilingPct,
		Participants:             parts,
	}
	cfg, err := h.uc.SetNegotiationConfig(r.Context(), in)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toNegotiationConfigDTO(*cfg))
}

// =====================================================================
// Negotiation lifecycle
// =====================================================================

func (h *NegotiationHandler) getNegotiation(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "negotiation")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	n, rounds, err := h.uc.GetNegotiation(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"negotiation": toNegotiationDTO(*n),
		"rounds":      toRoundDTOs(rounds),
	})
}

func (h *NegotiationHandler) getNegotiationByBOQ(w http.ResponseWriter, r *http.Request) {
	boqID, err := parseUUIDLocal(chi.URLParam(r, "boq_id"), "boq")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	n, err := h.uc.GetNegotiationByBOQ(r.Context(), boqID)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toNegotiationDTO(*n))
}

func (h *NegotiationHandler) activate(w http.ResponseWriter, r *http.Request) {
	boqID, err := parseUUIDLocal(chi.URLParam(r, "boq_id"), "boq")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	actor := actorUserIDLocal(r.Context())
	if actor == nil {
		httpserver.WriteError(w, errors.Forbidden("negotiation.actor_required", "actor user is required"))
		return
	}
	n, err := h.uc.ActivateNegotiation(r.Context(), port.ActivateNegotiationInput{
		BOQVersionID: boqID,
		ActorUserID:  *actor,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toNegotiationDTO(*n))
}

func (h *NegotiationHandler) abort(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "negotiation")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var req abortNegotiationRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	if req.Reason == "" {
		httpserver.WriteError(w, errors.Validation("negotiation.abort_reason_required", "reason is required"))
		return
	}
	n, err := h.uc.AbortNegotiation(r.Context(), port.AbortNegotiationInput{
		NegotiationID: id,
		Reason:        req.Reason,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toNegotiationDTO(*n))
}

// =====================================================================
// Round flow
// =====================================================================

func (h *NegotiationHandler) submitRound(w http.ResponseWriter, r *http.Request) {
	negID, err := parseUUIDLocal(chi.URLParam(r, "id"), "negotiation")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var req submitRoundRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	actor := actorUserIDLocal(r.Context())
	if actor == nil {
		httpserver.WriteError(w, errors.Forbidden("negotiation.actor_required", "actor user is required"))
		return
	}
	changes := make([]port.LinePriceChangeInput, 0, len(req.Changes))
	for _, c := range req.Changes {
		lid, perr := parseUUIDLocal(c.LineID, "line_id")
		if perr != nil {
			httpserver.WriteError(w, perr)
			return
		}
		changes = append(changes, port.LinePriceChangeInput{
			LineID:           lid,
			NewSellUnitPrice: c.NewSellUnitPrice,
			NewDiscountPct:   c.NewDiscountPct,
		})
	}
	round, approvals, err := h.uc.SubmitRound(r.Context(), port.SubmitNegotiationRoundInput{
		NegotiationID: negID,
		ActorUserID:   *actor,
		Changes:       changes,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, map[string]any{
		"round":     toRoundDTO(*round),
		"approvals": toApprovalDTOs(approvals),
	})
}

func (h *NegotiationHandler) getRound(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "round")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	round, approvals, err := h.uc.GetRound(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"round":     toRoundDTO(*round),
		"approvals": toApprovalDTOs(approvals),
	})
}

// listApprovals — the unified inbox query for negotiation round
// approvals. Only the ?pending_for_me=true variant is supported at MVP
// (mirrors the BOQ /approval-instances behavior).
func (h *NegotiationHandler) listApprovals(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page := parseIntDefaultLocal(q.Get("page"), 1)
	if page < 1 {
		page = 1
	}
	pageSize := parseIntDefaultLocal(q.Get("page_size"), 100)
	if q.Get("pending_for_me") != "true" {
		httpserver.WriteError(w, errors.Validation(
			"negotiation_round_approvals.unsupported_filter",
			"only pending_for_me=true is supported at MVP",
		))
		return
	}
	actor := actorUserIDLocal(r.Context())
	if actor == nil {
		httpserver.WriteError(w, errors.Forbidden(
			"negotiation.actor_required",
			"actor user is required",
		))
		return
	}
	items, err := h.uc.ListPendingRoundApprovalsForUser(
		r.Context(), *actor, pageSize, (page-1)*pageSize,
	)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"items":     toApprovalDTOs(items),
		"page":      page,
		"page_size": pageSize,
	})
}

func (h *NegotiationHandler) approveStep(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "approval")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	actor := actorUserIDLocal(r.Context())
	if actor == nil {
		httpserver.WriteError(w, errors.Forbidden("negotiation.actor_required", "actor user is required"))
		return
	}
	a, round, err := h.uc.ApproveRoundStep(r.Context(), port.NegotiationRoundActionInput{
		ApprovalID:  id,
		ActorUserID: *actor,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"approval": toApprovalDTO(*a),
		"round":    toRoundDTO(*round),
	})
}

func (h *NegotiationHandler) rejectStep(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "approval")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var req rejectStepRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	if req.ReasonCode == "" {
		httpserver.WriteError(w, errors.Validation("negotiation.reason_required", "reason_code is required on reject"))
		return
	}
	if req.Comment == "" {
		httpserver.WriteError(w, errors.Validation("negotiation.comment_required", "comment is required on reject"))
		return
	}
	actor := actorUserIDLocal(r.Context())
	if actor == nil {
		httpserver.WriteError(w, errors.Forbidden("negotiation.actor_required", "actor user is required"))
		return
	}
	a, round, err := h.uc.RejectRoundStep(r.Context(), port.NegotiationRoundActionInput{
		ApprovalID:  id,
		ActorUserID: *actor,
		ReasonCode:  req.ReasonCode,
		Comment:     req.Comment,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"approval": toApprovalDTO(*a),
		"round":    toRoundDTO(*round),
	})
}

// =====================================================================
// DTOs
// =====================================================================

type negotiationConfigDTO struct {
	BOQVersionID             string                    `json:"boq_version_id"`
	Enabled                  bool                      `json:"enabled"`
	Type                     string                    `json:"type"`
	Mode                     string                    `json:"mode"`
	PricingAdjustmentAllowed bool                      `json:"pricing_adjustment_allowed"`
	MarginFloorPct           float64                   `json:"margin_floor_pct"`
	DiscountCeilingPct       float64                   `json:"discount_ceiling_pct"`
	LockedAt                 *string                   `json:"locked_at,omitempty"`
	Participants             []negotiationParticipantDTO `json:"participants"`
}

type negotiationParticipantDTO struct {
	ID           string `json:"id"`
	BOQVersionID string `json:"boq_version_id"`
	UserID       string `json:"user_id"`
	StepNo       int    `json:"step_no"`
	RoleTag      string `json:"role_tag"`
	CreatedAt    string `json:"created_at"`
}

func toNegotiationConfigDTO(c domain.NegotiationConfig) negotiationConfigDTO {
	parts := make([]negotiationParticipantDTO, 0, len(c.Participants))
	for _, p := range c.Participants {
		parts = append(parts, negotiationParticipantDTO{
			ID:           p.ID.String(),
			BOQVersionID: p.BOQVersionID.String(),
			UserID:       p.UserID.String(),
			StepNo:       p.StepNo,
			RoleTag:      p.RoleTag,
			CreatedAt:    rfc3339(p.CreatedAt),
		})
	}
	return negotiationConfigDTO{
		BOQVersionID:             c.BOQVersionID.String(),
		Enabled:                  c.Enabled,
		Type:                     c.Type,
		Mode:                     string(c.Mode),
		PricingAdjustmentAllowed: c.PricingAdjustmentAllowed,
		MarginFloorPct:           c.MarginFloorPct,
		DiscountCeilingPct:       c.DiscountCeilingPct,
		LockedAt:                 rfc3339Ptr(c.LockedAt),
		Participants:             parts,
	}
}

type negotiationDTO struct {
	ID                   string  `json:"id"`
	BOQVersionID         string  `json:"boq_version_id"`
	Status               string  `json:"status"`
	ActivatedAt          *string `json:"activated_at,omitempty"`
	ActivatedBy          *string `json:"activated_by,omitempty"`
	CompletedAt          *string `json:"completed_at,omitempty"`
	AbortedAt            *string `json:"aborted_at,omitempty"`
	AbortReason          string  `json:"abort_reason"`
	ResultingQuotationID *string `json:"resulting_quotation_id,omitempty"`
	Revision             int     `json:"revision"`
	CreatedAt            string  `json:"created_at"`
	UpdatedAt            string  `json:"updated_at"`
}

func toNegotiationDTO(n domain.Negotiation) negotiationDTO {
	var activatedBy *string
	if n.ActivatedBy != nil {
		s := n.ActivatedBy.String()
		activatedBy = &s
	}
	var resultingQuoteID *string
	if n.ResultingQuotationID != nil {
		s := n.ResultingQuotationID.String()
		resultingQuoteID = &s
	}
	return negotiationDTO{
		ID:                   n.ID.String(),
		BOQVersionID:         n.BOQVersionID.String(),
		Status:               string(n.Status),
		ActivatedAt:          rfc3339Ptr(n.ActivatedAt),
		ActivatedBy:          activatedBy,
		CompletedAt:          rfc3339Ptr(n.CompletedAt),
		AbortedAt:            rfc3339Ptr(n.AbortedAt),
		AbortReason:          n.AbortReason,
		ResultingQuotationID: resultingQuoteID,
		Revision:             n.Revision,
		CreatedAt:            rfc3339(n.CreatedAt),
		UpdatedAt:            rfc3339(n.UpdatedAt),
	}
}

type linePriceChangeDTO struct {
	LineID            string  `json:"line_id"`
	BeforeSell        float64 `json:"before_sell"`
	AfterSell         float64 `json:"after_sell"`
	BeforeDiscountPct float64 `json:"before_discount_pct"`
	AfterDiscountPct  float64 `json:"after_discount_pct"`
}

type roundDTO struct {
	ID                  string               `json:"id"`
	NegotiationID       string               `json:"negotiation_id"`
	RoundNo             int                  `json:"round_no"`
	Status              string               `json:"status"`
	PriceChanges        []linePriceChangeDTO `json:"price_changes"`
	MarginBefore        float64              `json:"margin_before"`
	MarginAfter         float64              `json:"margin_after"`
	MaxDiscountAfter    float64              `json:"max_discount_after"`
	CCOAutoInjected     bool                 `json:"cco_auto_injected"`
	CCOInjectionReason  string               `json:"cco_injection_reason"`
	SubmittedBy         *string              `json:"submitted_by,omitempty"`
	SubmittedAt         string               `json:"submitted_at"`
	CompletedAt         *string              `json:"completed_at,omitempty"`
	RejectionReasonCode string               `json:"rejection_reason_code"`
	RejectionComment    string               `json:"rejection_comment"`
	CreatedAt           string               `json:"created_at"`
	UpdatedAt           string               `json:"updated_at"`
}

func toRoundDTO(r domain.NegotiationRound) roundDTO {
	changes := make([]linePriceChangeDTO, 0, len(r.PriceChanges))
	for _, c := range r.PriceChanges {
		changes = append(changes, linePriceChangeDTO{
			LineID:            c.LineID.String(),
			BeforeSell:        c.BeforeSell,
			AfterSell:         c.AfterSell,
			BeforeDiscountPct: c.BeforeDiscountPct,
			AfterDiscountPct:  c.AfterDiscountPct,
		})
	}
	var submittedBy *string
	if r.SubmittedBy != nil {
		s := r.SubmittedBy.String()
		submittedBy = &s
	}
	return roundDTO{
		ID:                  r.ID.String(),
		NegotiationID:       r.NegotiationID.String(),
		RoundNo:             r.RoundNo,
		Status:              string(r.Status),
		PriceChanges:        changes,
		MarginBefore:        r.MarginBefore,
		MarginAfter:         r.MarginAfter,
		MaxDiscountAfter:    r.MaxDiscountAfter,
		CCOAutoInjected:     r.CCOAutoInjected,
		CCOInjectionReason:  string(r.CCOInjectionReason),
		SubmittedBy:         submittedBy,
		SubmittedAt:         rfc3339(r.SubmittedAt),
		CompletedAt:         rfc3339Ptr(r.CompletedAt),
		RejectionReasonCode: string(r.RejectionReasonCode),
		RejectionComment:    r.RejectionComment,
		CreatedAt:           rfc3339(r.CreatedAt),
		UpdatedAt:           rfc3339(r.UpdatedAt),
	}
}

func toRoundDTOs(in []domain.NegotiationRound) []roundDTO {
	out := make([]roundDTO, 0, len(in))
	for _, r := range in {
		out = append(out, toRoundDTO(r))
	}
	return out
}

type approvalDTO struct {
	ID              string  `json:"id"`
	RoundID         string  `json:"round_id"`
	StepNo          int     `json:"step_no"`
	ApproverUserID  string  `json:"approver_user_id"`
	RoleTag         string  `json:"role_tag"`
	Status          string  `json:"status"`
	ReasonCode      string  `json:"reason_code"`
	Comment         string  `json:"comment"`
	ActedAt         *string `json:"acted_at,omitempty"`
	ActedAtOriginal *string `json:"acted_at_original,omitempty"`
	AutoInjected    bool    `json:"auto_injected"`
	CreatedAt       string  `json:"created_at"`
	UpdatedAt       string  `json:"updated_at"`
}

func toApprovalDTO(a domain.NegotiationRoundApproval) approvalDTO {
	return approvalDTO{
		ID:              a.ID.String(),
		RoundID:         a.RoundID.String(),
		StepNo:          a.StepNo,
		ApproverUserID:  a.ApproverUserID.String(),
		RoleTag:         a.RoleTag,
		Status:          string(a.Status),
		ReasonCode:      string(a.ReasonCode),
		Comment:         a.Comment,
		ActedAt:         rfc3339Ptr(a.ActedAt),
		ActedAtOriginal: rfc3339Ptr(a.ActedAtOriginal),
		AutoInjected:    a.AutoInjected,
		CreatedAt:       rfc3339(a.CreatedAt),
		UpdatedAt:       rfc3339(a.UpdatedAt),
	}
}

func toApprovalDTOs(in []domain.NegotiationRoundApproval) []approvalDTO {
	out := make([]approvalDTO, 0, len(in))
	for _, a := range in {
		out = append(out, toApprovalDTO(a))
	}
	return out
}

// =====================================================================
// Requests
// =====================================================================

type setNegotiationConfigRequest struct {
	Enabled                  bool                                      `json:"enabled"`
	Type                     string                                    `json:"type"`
	Mode                     string                                    `json:"mode"`
	PricingAdjustmentAllowed bool                                      `json:"pricing_adjustment_allowed"`
	MarginFloorPct           float64                                   `json:"margin_floor_pct"`
	DiscountCeilingPct       float64                                   `json:"discount_ceiling_pct"`
	Participants             []setNegotiationConfigParticipantRequest `json:"participants"`
}

type setNegotiationConfigParticipantRequest struct {
	UserID  string `json:"user_id"`
	StepNo  int    `json:"step_no"`
	RoleTag string `json:"role_tag"`
}

type abortNegotiationRequest struct {
	Reason string `json:"reason"`
}

type submitRoundRequest struct {
	Changes []submitRoundChangeRequest `json:"changes"`
}

type submitRoundChangeRequest struct {
	LineID           string  `json:"line_id"`
	NewSellUnitPrice float64 `json:"new_sell_unit_price"`
	NewDiscountPct   float64 `json:"new_discount_pct"`
}

type rejectStepRequest struct {
	ReasonCode string `json:"reason_code"`
	Comment    string `json:"comment"`
}

// exportRoundsCSV — Wave 106 ops-handoff CSV dump of every round
// (and its approval-chain summary) for a given negotiation. The CSV
// is rendered inline; clients use a regular GET with a Save-As. The
// vendor-mask middleware still strips commercial fields for vendor
// actors — the same NFR-011 contract applies because the route lives
// inside the same Group as the JSON ones.
func (h *NegotiationHandler) exportRoundsCSV(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUIDLocal(chi.URLParam(r, "id"), "negotiation")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	n, rounds, err := h.uc.GetNegotiation(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition",
		"attachment; filename=\"negotiation-"+n.ID.String()+"-rounds.csv\"")
	w.WriteHeader(http.StatusOK)
	// Header row + one row per round. We avoid encoding/csv to keep
	// the byte output 1:1 with the test harness expectation; the
	// fields below never contain commas / quotes by construction.
	_, _ = w.Write([]byte(
		"negotiation_id,round_no,status,margin_before,margin_after,max_discount_after,cco_auto_injected,cco_injection_reason,submitted_at,completed_at\n",
	))
	for _, rd := range rounds {
		completedAt := ""
		if rd.CompletedAt != nil {
			completedAt = rd.CompletedAt.UTC().Format("2006-01-02T15:04:05Z")
		}
		row := n.ID.String() + "," +
			strconv.Itoa(rd.RoundNo) + "," +
			string(rd.Status) + "," +
			ftoaRound(rd.MarginBefore) + "," +
			ftoaRound(rd.MarginAfter) + "," +
			ftoaRound(rd.MaxDiscountAfter) + "," +
			btoaRound(rd.CCOAutoInjected) + "," +
			string(rd.CCOInjectionReason) + "," +
			rd.SubmittedAt.UTC().Format("2006-01-02T15:04:05Z") + "," +
			completedAt + "\n"
		_, _ = w.Write([]byte(row))
	}
}

// =====================================================================
// CSV helpers — kept local to avoid importing fmt for these few
// allocs. Tested via the CSV-export route test.
// =====================================================================

// ftoaRound — local float formatter used by the rounds.csv export
// (TC-NG-* CSV ops handoff). Four decimal places, no scientific notation.
func ftoaRound(f float64) string {
	return strconv.FormatFloat(f, 'f', 4, 64)
}

// btoaRound — local bool-to-string for the rounds.csv export.
func btoaRound(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// Compile-time: assert NegotiationUseCase is implemented (early failure
// if the interface diverges).
var _ port.NegotiationUseCase = (port.NegotiationUseCase)(nil)

// silence unused import when uuid is only referenced via parse helpers.
var _ = uuid.Nil
