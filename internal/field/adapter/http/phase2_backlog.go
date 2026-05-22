// P1+P2 backlog handlers attached to Phase2Handler. Routes are
// mounted from phase2.go; the implementations live here to keep
// the per-file size sane.
package http

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/httpserver"
)

// =============================================================================
// Cross-area dispatch
// =============================================================================

type crossAreaReq struct {
	TargetBranchID string `json:"target_branch_id"`
	Reason         string `json:"reason"`
}

func (h *Phase2Handler) requestCrossArea(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeFieldErr(w, http.StatusBadRequest, err)
		return
	}
	var in crossAreaReq
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeFieldErr(w, http.StatusBadRequest, err)
		return
	}
	tb, err := uuid.Parse(in.TargetBranchID)
	if err != nil {
		writeFieldErr(w, http.StatusBadRequest, fieldErrMsg{"target_branch_id required"})
		return
	}
	if _, err := h.pool.Exec(r.Context(), `
		UPDATE field.work_orders
		SET is_cross_area = TRUE,
		    cross_area_target_branch_id = $1,
		    cross_area_reason = NULLIF($2,''),
		    cross_area_requested_at = NOW(),
		    updated_at = NOW()
		WHERE id = $3
	`, tb, in.Reason, id); err != nil {
		writeFieldErr(w, http.StatusInternalServerError, err)
		return
	}
	writeFieldJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Phase2Handler) listCrossAreaPending(w http.ResponseWriter, r *http.Request) {
	rows, err := h.pool.Query(r.Context(), `
		SELECT w.id, w.wo_number, w.customer_id, COALESCE(c.full_name, ''),
		       w.address, w.cross_area_target_branch_id,
		       COALESCE(b.name, ''),
		       w.cross_area_reason, w.cross_area_requested_at
		FROM field.work_orders w
		LEFT JOIN crm.customers c ON c.id = w.customer_id
		LEFT JOIN identity.branches b ON b.id = w.cross_area_target_branch_id
		WHERE w.is_cross_area = TRUE
		  AND w.status IN ('unassigned','assigned')
		ORDER BY w.cross_area_requested_at DESC NULLS LAST
		LIMIT 100
	`)
	if err != nil {
		writeFieldErr(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()
	type row struct {
		WoID           string     `json:"wo_id"`
		WoNumber       string     `json:"wo_number"`
		CustomerID     string     `json:"customer_id"`
		CustomerName   string     `json:"customer_name"`
		Address        string     `json:"address"`
		TargetBranchID string     `json:"target_branch_id"`
		TargetBranch   string     `json:"target_branch_name"`
		Reason         *string    `json:"reason,omitempty"`
		RequestedAt    *time.Time `json:"requested_at,omitempty"`
	}
	out := []row{}
	for rows.Next() {
		var x row
		var reason *string
		if err := rows.Scan(&x.WoID, &x.WoNumber, &x.CustomerID, &x.CustomerName,
			&x.Address, &x.TargetBranchID, &x.TargetBranch, &reason, &x.RequestedAt); err == nil {
			x.Reason = reason
			out = append(out, x)
		}
	}
	writeFieldJSON(w, http.StatusOK, map[string]any{"items": out})
}

// =============================================================================
// Auto-pair suggestion
//
// Returns the cheapest available senior+junior pair from the WO's
// branch — "available" = on shift today and not already booked on
// another in_progress WO at the same scheduled window.
// =============================================================================

func (h *Phase2Handler) suggestedPair(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeFieldErr(w, http.StatusBadRequest, err)
		return
	}
	// Branch of the WO.
	var branchID *uuid.UUID
	_ = h.pool.QueryRow(r.Context(),
		`SELECT branch_id FROM field.work_orders WHERE id = $1`, id).Scan(&branchID)

	pickPair := func(role string) (string, string, error) {
		var uid, name string
		err := h.pool.QueryRow(r.Context(), `
			SELECT u.id, u.full_name
			FROM identity.users u
			JOIN identity.technician_profiles tp ON tp.user_id = u.id
			LEFT JOIN identity.user_availability ua
			       ON ua.user_id = u.id AND ua.date = CURRENT_DATE
			WHERE tp.grade = $1
			  AND u.active = TRUE
			  AND ($2::uuid IS NULL OR u.branch_id = $2)
			  AND COALESCE(ua.status, 'available') = 'available'
			  AND NOT EXISTS (
			    -- The assignments table is field.wo_assignments (per migration
			    -- 0008). An older draft of this handler referenced
			    -- field.assignments which silently swallowed every error and
			    -- made the suggestion endpoint return {} regardless of pool
			    -- state. Fixed in Wave 59.
			    SELECT 1 FROM field.wo_assignments a
			    JOIN field.work_orders w ON w.id = a.wo_id
			    WHERE a.technician_id = u.id
			      AND w.status IN ('assigned','dispatched','in_progress')
			  )
			ORDER BY u.full_name ASC
			LIMIT 1
		`, role, branchID).Scan(&uid, &name)
		return uid, name, err
	}

	type suggest struct {
		UserID   string `json:"user_id"`
		FullName string `json:"full_name"`
	}
	type resp struct {
		Lead     *suggest `json:"lead_senior,omitempty"`
		Observer *suggest `json:"observer_junior,omitempty"`
	}
	out := resp{}
	if uid, name, err := pickPair("senior"); err == nil {
		out.Lead = &suggest{UserID: uid, FullName: name}
	}
	if uid, name, err := pickPair("junior"); err == nil {
		out.Observer = &suggest{UserID: uid, FullName: name}
	}
	writeFieldJSON(w, http.StatusOK, out)
}

// =============================================================================
// Mid-schedule priority insertion
// =============================================================================

type priorityInsertReq struct {
	TechUserID string `json:"tech_user_id"`
	Reason     string `json:"reason"`
}

func (h *Phase2Handler) priorityInsertWO(w http.ResponseWriter, r *http.Request) {
	woID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeFieldErr(w, http.StatusBadRequest, err)
		return
	}
	var in priorityInsertReq
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeFieldErr(w, http.StatusBadRequest, err)
		return
	}
	techID, err := uuid.Parse(in.TechUserID)
	if err != nil {
		writeFieldErr(w, http.StatusBadRequest, fieldErrMsg{"tech_user_id required"})
		return
	}
	claims := httpserver.ClaimsFromContext(r.Context())
	if claims == nil {
		writeFieldErr(w, http.StatusUnauthorized, fieldErrMsg{"no claims"})
		return
	}
	id := uuid.New()
	if _, err := h.pool.Exec(r.Context(), `
		INSERT INTO field.priority_insertions
			(id, wo_id, inserted_by, tech_user_id, reason)
		VALUES ($1, $2, $3, $4, $5)
	`, id, woID, claims.UserID, techID, in.Reason); err != nil {
		writeFieldErr(w, http.StatusInternalServerError, err)
		return
	}
	writeFieldJSON(w, http.StatusCreated, map[string]any{
		"id":     id.String(),
		"wo_id":  woID.String(),
		"tech_id": techID.String(),
	})
}

func (h *Phase2Handler) myPriorityInsertions(w http.ResponseWriter, r *http.Request) {
	claims := httpserver.ClaimsFromContext(r.Context())
	if claims == nil {
		writeFieldErr(w, http.StatusUnauthorized, fieldErrMsg{"no claims"})
		return
	}
	rows, err := h.pool.Query(r.Context(), `
		SELECT pi.id, pi.wo_id, w.wo_number, COALESCE(w.address, ''),
		       pi.reason, pi.accepted, pi.created_at
		FROM field.priority_insertions pi
		LEFT JOIN field.work_orders w ON w.id = pi.wo_id
		WHERE pi.tech_user_id = $1
		  AND pi.accepted IS NULL
		ORDER BY pi.created_at DESC LIMIT 20
	`, claims.UserID)
	if err != nil {
		writeFieldErr(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()
	type row struct {
		ID        string    `json:"id"`
		WoID      string    `json:"wo_id"`
		WoNumber  string    `json:"wo_number"`
		Address   string    `json:"address"`
		Reason    string    `json:"reason"`
		Accepted  *bool     `json:"accepted,omitempty"`
		CreatedAt time.Time `json:"created_at"`
	}
	out := []row{}
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.ID, &x.WoID, &x.WoNumber, &x.Address, &x.Reason,
			&x.Accepted, &x.CreatedAt); err == nil {
			out = append(out, x)
		}
	}
	writeFieldJSON(w, http.StatusOK, map[string]any{"items": out})
}

type respondPriorityReq struct {
	Accepted bool `json:"accepted"`
}

func (h *Phase2Handler) respondPriorityInsertion(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeFieldErr(w, http.StatusBadRequest, err)
		return
	}
	var in respondPriorityReq
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeFieldErr(w, http.StatusBadRequest, err)
		return
	}
	claims := httpserver.ClaimsFromContext(r.Context())
	if claims == nil {
		writeFieldErr(w, http.StatusUnauthorized, fieldErrMsg{"no claims"})
		return
	}
	if _, err := h.pool.Exec(r.Context(), `
		UPDATE field.priority_insertions
		SET accepted = $1, accepted_at = NOW()
		WHERE id = $2 AND tech_user_id = $3
	`, in.Accepted, id, claims.UserID); err != nil {
		writeFieldErr(w, http.StatusInternalServerError, err)
		return
	}
	writeFieldJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// =============================================================================
// Ticket attachments — list endpoint (POST already in postTicketMessage)
// =============================================================================

func (h *Phase2Handler) listTicketAttachments(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeFieldErr(w, http.StatusBadRequest, err)
		return
	}
	rows, err := h.pool.Query(r.Context(), `
		SELECT m.id, m.author_kind, m.attachments, m.created_at
		FROM field.ticket_messages m
		WHERE m.ticket_id = $1
		  AND jsonb_array_length(m.attachments) > 0
		ORDER BY m.created_at DESC
	`, id)
	if err != nil {
		writeFieldErr(w, http.StatusInternalServerError, err)
		return
	}
	defer rows.Close()
	type row struct {
		MessageID   string          `json:"message_id"`
		AuthorKind  string          `json:"author_kind"`
		Attachments json.RawMessage `json:"attachments"`
		CreatedAt   time.Time       `json:"created_at"`
	}
	out := []row{}
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.MessageID, &x.AuthorKind, &x.Attachments, &x.CreatedAt); err == nil {
			out = append(out, x)
		}
	}
	writeFieldJSON(w, http.StatusOK, map[string]any{"items": out})
}
