// M6 r3 customer-portal handlers — unauthenticated endpoints for
// self-service voluntary termination via OTP.
//
// Public route surface:
//
//	POST /portal/termination/request   — mint an OTP for (customer_number, phone)
//	POST /portal/termination/confirm   — verify OTP + reason, fire the termination
//
// Both are per-IP rate limited and don't require a JWT. The OTP itself
// is the auth gate: minted server-side, hashed in the DB, plaintext
// delivered out-of-band (round-3: log only; round-4: WhatsApp/SMS).
package http

import (
	"net/http"

	"github.com/ion-core/backend/internal/billing/port"
	"github.com/ion-core/backend/pkg/errors"
	"github.com/ion-core/backend/pkg/httpserver"
)

// DTOs (portalRequestOTPRequest, portalConfirmRequest, …) live in dto.go.

func (h *Handler) portalRequestOTP(w http.ResponseWriter, r *http.Request) {
	var req portalRequestOTPRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out, err := h.uc.RequestTerminationOTP(r.Context(), port.PortalRequestTerminationOTPInput{
		CustomerNumber: req.CustomerNumber,
		Phone:          req.Phone,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusAccepted, portalRequestOTPResponse{
		ExpiresAt: out.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z"),
		DevOTP:    out.DevOTP,
	})
}

func (h *Handler) portalConfirm(w http.ResponseWriter, r *http.Request) {
	var req portalConfirmRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	if req.CustomerNumber == "" || req.OTP == "" {
		httpserver.WriteError(w, errors.Validation("portal.input",
			"customer_number and otp are required"))
		return
	}
	out, err := h.uc.ConfirmTermination(r.Context(), port.PortalConfirmTerminationInput{
		CustomerNumber: req.CustomerNumber,
		OTP:            req.OTP,
		Reason:         req.Reason,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, portalConfirmResponse{
		TerminationID: out.TerminationID.String(),
		Status:        out.Status,
	})
}
