// M6 r3 HTTP handlers — voluntary termination + referral rewards.
package http

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/billing/port"
	"github.com/ion-core/backend/pkg/errors"
	"github.com/ion-core/backend/pkg/httpserver"
)

// DTOs (terminationDTO, rewardDTO, requestTerminationRequest, …) live in dto.go.

// =====================================================================
// Termination requests
// =====================================================================

func (h *Handler) requestTermination(w http.ResponseWriter, r *http.Request) {
	var req requestTerminationRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	cid, err := uuid.Parse(req.CustomerID)
	if err != nil {
		httpserver.WriteError(w, errors.Validation("termination.customer_invalid", "customer_id is not a uuid"))
		return
	}
	c := httpserver.ClaimsFromContext(r.Context())
	if c == nil {
		httpserver.WriteError(w, errors.Unauthorized("auth.missing", "authentication required"))
		return
	}
	t, err := h.uc.RequestVoluntaryTermination(r.Context(), port.RequestTerminationInput{
		CustomerID:  cid,
		Reason:      req.Reason,
		RequestedBy: c.UserID,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toTerminationDTO(t))
}

func (h *Handler) cancelTermination(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "termination")
	if !ok {
		return
	}
	var req cancelTerminationRequest
	_ = httpserver.DecodeJSON(r, &req)
	c := httpserver.ClaimsFromContext(r.Context())
	if c == nil {
		httpserver.WriteError(w, errors.Unauthorized("auth.missing", "authentication required"))
		return
	}
	t, err := h.uc.CancelTerminationRequest(r.Context(), id, c.UserID, req.Reason)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toTerminationDTO(t))
}

func (h *Handler) listTerminations(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := port.TerminationRequestFilter{
		Kind:   q.Get("kind"),
		Status: q.Get("status"),
		Limit:  httpserver.ParseIntDefault(q.Get("page_size"), 50),
	}
	if v := q.Get("customer_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpserver.WriteError(w, errors.Validation("termination.customer_invalid", "customer_id is not a uuid"))
			return
		}
		f.CustomerID = &id
	}
	if v := q.Get("final_invoice_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpserver.WriteError(w, errors.Validation("termination.invoice_invalid", "final_invoice_id is not a uuid"))
			return
		}
		f.FinalInvoiceID = &id
	}
	out, total, err := h.uc.ListTerminationRequests(r.Context(), f)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	items := make([]terminationDTO, 0, len(out))
	for i := range out {
		items = append(items, toTerminationDTO(&out[i]))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "total": total})
}

func (h *Handler) getTermination(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "termination")
	if !ok {
		return
	}
	t, err := h.uc.GetTerminationRequest(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toTerminationDTO(t))
}

// =====================================================================
// Referral rewards
// =====================================================================

func (h *Handler) listReferralRewards(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := port.ReferralRewardFilter{
		Status: q.Get("status"),
		Limit:  httpserver.ParseIntDefault(q.Get("page_size"), 100),
	}
	if v := q.Get("referrer_customer_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpserver.WriteError(w, errors.Validation("referral.referrer_invalid", "referrer_customer_id is not a uuid"))
			return
		}
		f.ReferrerCustomerID = &id
	}
	out, err := h.uc.ListReferralRewards(r.Context(), f)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	items := make([]rewardDTO, 0, len(out))
	for _, x := range out {
		items = append(items, toRewardDTO(x))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}
