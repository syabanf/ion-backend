// M6 r2 HTTP handlers — policy, cycles, commissions, manual tick.
package http

import (
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/billing/port"
	"github.com/ion-core/backend/pkg/errors"
	"github.com/ion-core/backend/pkg/httpserver"
)

// DTOs (policyDTO, cycleDTO, commissionDTO, tickReportDTO, …) live in dto.go.

// =====================================================================
// Policy
// =====================================================================

func (h *Handler) getPolicy(w http.ResponseWriter, r *http.Request) {
	p, err := h.uc.GetPolicy(r.Context())
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toPolicyDTO(p))
}

func (h *Handler) updatePolicy(w http.ResponseWriter, r *http.Request) {
	var req updatePolicyRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	c := httpserver.ClaimsFromContext(r.Context())
	if c == nil {
		httpserver.WriteError(w, errors.Unauthorized("auth.missing", "authentication required"))
		return
	}
	p, err := h.uc.UpdatePolicy(r.Context(), port.UpdatePolicyInput{
		LateFeeGraceDays:            req.LateFeeGraceDays,
		LateFeeAmount:               req.LateFeeAmount,
		SuspendAfterDays:            req.SuspendAfterDays,
		TerminateAfterSuspendedDays: req.TerminateAfterSuspendedDays,
		NotifyCustomerDaysBefore:    req.NotifyCustomerDaysBefore,
		UpdatedBy:                   c.UserID,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toPolicyDTO(p))
}

// =====================================================================
// Billing cycles
// =====================================================================

func (h *Handler) listCycles(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := port.CycleFilter{
		Status: q.Get("status"),
		Limit:  parseIntDefault(q.Get("page_size"), 50),
	}
	if v := q.Get("customer_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpserver.WriteError(w, errors.Validation("cycle.customer_invalid", "customer_id is not a uuid"))
			return
		}
		f.CustomerID = &id
	}
	out, total, err := h.uc.ListBillingCycles(r.Context(), f)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	items := make([]cycleDTO, 0, len(out))
	for _, c := range out {
		items = append(items, toCycleDTO(c))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"items": items, "total": total,
	})
}

// =====================================================================
// Manual tick
// =====================================================================

func (h *Handler) runTick(w http.ResponseWriter, r *http.Request) {
	rep, err := h.uc.RunBillingTick(r.Context(), time.Now().UTC())
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, tickReportDTO{
		StartedAt:             rep.StartedAt.UTC().Format(time.RFC3339),
		CompletedAt:           rep.CompletedAt.UTC().Format(time.RFC3339),
		RecurringGenerated:    rep.RecurringGenerated,
		RecurringSkipped:      rep.RecurringSkipped,
		LateFeesApplied:       rep.LateFeesApplied,
		CustomersSuspended:    rep.CustomersSuspended,
		CustomersRestored:     rep.CustomersRestored,
		TerminationsTriggered: rep.TerminationsTriggered,
		Errors:                rep.Errors,
	})
}

// =====================================================================
// Commissions
// =====================================================================

func (h *Handler) listCommissions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := port.CommissionFilter{
		PartyType: q.Get("party_type"),
		Limit:     parseIntDefault(q.Get("page_size"), 100),
	}
	if v := q.Get("user_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpserver.WriteError(w, errors.Validation("commission.user_invalid", "user_id is not a uuid"))
			return
		}
		f.UserID = &id
	}
	if v := q.Get("branch_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpserver.WriteError(w, errors.Validation("commission.branch_invalid", "branch_id is not a uuid"))
			return
		}
		f.BranchID = &id
	}
	if v := q.Get("order_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpserver.WriteError(w, errors.Validation("commission.order_invalid", "order_id is not a uuid"))
			return
		}
		f.OrderID = &id
	}
	// 'mine=true' scopes to the caller's user_id — useful for sales reps.
	if q.Get("mine") == "true" {
		c := httpserver.ClaimsFromContext(r.Context())
		if c != nil {
			uid := c.UserID
			f.UserID = &uid
		}
	}
	out, err := h.uc.ListCommissions(r.Context(), f)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	items := make([]commissionDTO, 0, len(out))
	for _, c := range out {
		items = append(items, toCommissionDTO(c))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}
