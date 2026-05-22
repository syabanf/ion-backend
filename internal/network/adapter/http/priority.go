// Network priority-followup endpoints: per-customer RADIUS session
// state lookup for the staff customer-detail page.
package http

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/pkg/auth"
	"github.com/ion-core/backend/pkg/httpserver"
)

type PriorityHandler struct {
	pool     *pgxpool.Pool
	verifier *auth.Verifier
}

func NewPriorityHandler(pool *pgxpool.Pool, verifier *auth.Verifier) *PriorityHandler {
	return &PriorityHandler{pool: pool, verifier: verifier}
}

func (h *PriorityHandler) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(httpserver.RequireAuth(h.verifier))

		r.With(httpserver.RequirePermission("network.topology.read")).
			Get("/customers/{id}/radius-state", h.radiusStateByCustomer)
	})
}

func (h *PriorityHandler) radiusStateByCustomer(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	var (
		state      string = "unknown"
		lastSeenAt *time.Time
		username   string
	)
	_ = h.pool.QueryRow(r.Context(), `
		SELECT COALESCE(rs.state, 'unknown'),
		       rs.last_seen_at,
		       COALESCE(ra.username, '')
		FROM crm.customers c
		LEFT JOIN network.radius_accounts ra ON ra.customer_id = c.id
		LEFT JOIN network.radius_sessions rs ON rs.customer_id = c.id
		WHERE c.id = $1
		ORDER BY rs.last_seen_at DESC NULLS LAST
		LIMIT 1
	`, id).Scan(&state, &lastSeenAt, &username)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"state":        state,
		"last_seen_at": lastSeenAt,
		"username":     username,
	})
}
