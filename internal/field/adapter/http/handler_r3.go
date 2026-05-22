// Round-3 HTTP handlers: ONT config.
package http

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
	"github.com/ion-core/backend/pkg/httpserver"
)

// DTOs (ontConfigDTO) live in dto.go.

// getONTConfig returns the RADIUS account projection for the WO's
// customer. Only available while the WO is `in_progress`; otherwise the
// usecase returns 403. Password is deliberately omitted.
func (h *Handler) getONTConfig(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "wo")
	if !ok {
		return
	}
	var caller uuid.UUID
	if c := httpserver.ClaimsFromContext(r.Context()); c != nil {
		caller = c.UserID
	}
	v, err := h.uc.GetONTConfig(r.Context(), id, caller)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	if v == nil {
		httpserver.WriteError(w, errors.NotFound("ont.not_provisioned",
			"no RADIUS account exists for this customer yet"))
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, ontConfigDTO{
		Username:           v.Username,
		BandwidthProfileID: v.BandwidthProfileID,
		VLANID:             v.VLANID,
		IPAddress:          v.IPAddress,
		Status:             v.Status,
	})
}
