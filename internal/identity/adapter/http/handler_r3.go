// M5 r3 — HRIS availability stub handlers.
package http

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/identity/domain"
	"github.com/ion-core/backend/internal/identity/port"
	"github.com/ion-core/backend/pkg/errors"
	"github.com/ion-core/backend/pkg/httpserver"
)

// DTOs (rosterRowDTO, setAvailabilityRequest) + the toRosterRowDTO
// helper live in dto.go.

// GET /availability/roster?date=YYYY-MM-DD&branch_id=…&role=technician&role=team_leader
//
// All three filters optional. role is repeated for OR. date defaults to today UTC.
func (h *Handler) listRoster(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := port.RosterFilter{Roles: q["role"]}
	if v := q.Get("date"); v != "" {
		t, err := time.Parse("2006-01-02", v)
		if err != nil {
			httpserver.WriteError(w, errors.Validation("availability.date_invalid", "date must be YYYY-MM-DD"))
			return
		}
		f.Date = t
	}
	if v := q.Get("branch_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpserver.WriteError(w, errors.Validation("availability.branch_invalid", "branch_id is not a uuid"))
			return
		}
		f.BranchID = &id
	}
	out, err := h.uc.ListRoster(r.Context(), f)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	items := make([]rosterRowDTO, 0, len(out))
	for _, row := range out {
		items = append(items, toRosterRowDTO(row))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) setAvailability(w http.ResponseWriter, r *http.Request) {
	userID, err := uuid.Parse(chi.URLParam(r, "user_id"))
	if err != nil {
		httpserver.WriteError(w, errors.Validation("availability.user_invalid", "user_id is not a uuid"))
		return
	}
	var req setAvailabilityRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	t, err := time.Parse("2006-01-02", req.Date)
	if err != nil {
		httpserver.WriteError(w, errors.Validation("availability.date_invalid", "date must be YYYY-MM-DD"))
		return
	}
	c := httpserver.ClaimsFromContext(r.Context())
	if c == nil {
		httpserver.WriteError(w, errors.Unauthorized("auth.missing", "authentication required"))
		return
	}
	if err := h.uc.SetAvailability(r.Context(), port.SetAvailabilityInput{
		UserID:    userID,
		Date:      t,
		Status:    domain.AvailabilityStatus(req.Status),
		Notes:     req.Notes,
		UpdatedBy: c.UserID,
	}); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"user_id": userID.String(),
		"date":    req.Date,
		"status":  req.Status,
		"notes":   req.Notes,
	})
}
