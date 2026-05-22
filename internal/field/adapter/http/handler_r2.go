// Round-2 HTTP handlers: reschedule, OTP verify, SLA queue.
package http

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/field/domain"
	"github.com/ion-core/backend/internal/field/port"
	"github.com/ion-core/backend/pkg/errors"
	"github.com/ion-core/backend/pkg/httpserver"
)

// DTOs (rescheduleRequest, rescheduleDTO, verifyOTPRequest) live in dto.go.

// =====================================================================
// Reschedule
// =====================================================================

func (h *Handler) rescheduleWO(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpserver.WriteError(w, errors.Validation("wo.id_invalid", "id is not a uuid"))
		return
	}
	var req rescheduleRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	when, err := time.Parse(time.RFC3339, req.NewDate)
	if err != nil {
		httpserver.WriteError(w, errors.Validation("wo.date_invalid", "new_date must be RFC3339"))
		return
	}
	c := httpserver.ClaimsFromContext(r.Context())
	if c == nil {
		httpserver.WriteError(w, errors.Unauthorized("auth.missing", "authentication required"))
		return
	}
	d, err := h.uc.RescheduleWO(r.Context(), port.RescheduleWOInput{
		WOID:          id,
		Reason:        domain.RescheduleReason(req.Reason),
		Notes:         req.Notes,
		NewDate:       when,
		RescheduledBy: c.UserID,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toWODetailDTO(*d))
}

func (h *Handler) listReschedules(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpserver.WriteError(w, errors.Validation("wo.id_invalid", "id is not a uuid"))
		return
	}
	out, err := h.uc.ListRescheduleHistory(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	items := make([]rescheduleDTO, 0, len(out))
	for _, r := range out {
		items = append(items, toRescheduleDTO(r))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

// =====================================================================
// OTP verify
// =====================================================================

func (h *Handler) verifyBASTOTP(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpserver.WriteError(w, errors.Validation("bast.id_invalid", "id is not a uuid"))
		return
	}
	var req verifyOTPRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	if len(req.Code) != 6 {
		httpserver.WriteError(w, errors.Validation("bast.otp_format", "code must be 6 digits"))
		return
	}
	b, err := h.uc.VerifyBASTOTP(r.Context(), port.VerifyOTPInput{
		BASTID: id,
		Code:   req.Code,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toBASTDTO(b))
}

// =====================================================================
// SLA-breach queue
// =====================================================================

func (h *Handler) listSLABreaches(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := parseIntDefault(q.Get("page_size"), 50)
	page := parseIntDefault(q.Get("page"), 1)
	out, total, err := h.uc.ListSLABreaches(r.Context(), limit, (page-1)*limit)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	items := make([]woDTO, 0, len(out))
	for _, d := range out {
		items = append(items, toWODTO(d))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"items": items, "total": total, "page": page, "page_size": limit,
	})
}
