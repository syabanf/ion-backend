// Priority-followup endpoints layered on top of the main identity
// handler. These don't fit the existing UseCase port (push tokens +
// HRIS sync are infrastructure-y, not domain operations) so they
// take the pool directly, matching the phase2.go pattern.
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

		// Staff-side device token registration.
		r.With(httpserver.RequirePermission("platform.device_token.register")).
			Post("/device-token", h.registerStaffDeviceToken)

		// HRIS sync state — read for the roster banner, manual trigger
		// for ops admins.
		r.Get("/hris/sync-state", h.hrisSyncState)
		r.With(httpserver.RequirePermission("identity.availability.manage")).
			Post("/hris/sync-now", h.hrisSyncNow)
	})
}

// =============================================================================
// Staff push tokens
// =============================================================================

type staffTokenReq struct {
	Token    string `json:"token"`
	Platform string `json:"platform"` // ios | android | web
	App      string `json:"app"`      // tech | sales | staff_web
}

func (h *PriorityHandler) registerStaffDeviceToken(w http.ResponseWriter, r *http.Request) {
	claims := httpserver.ClaimsFromContext(r.Context())
	if claims == nil {
		writePriorityErr(w, http.StatusUnauthorized, "auth.missing", "no claims")
		return
	}
	var in staffTokenReq
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writePriorityErr(w, http.StatusBadRequest, "device.bad_body", err.Error())
		return
	}
	if in.Token == "" || in.Platform == "" || in.App == "" {
		writePriorityErr(w, http.StatusBadRequest, "device.missing_fields",
			"token, platform, app are required")
		return
	}
	switch in.Platform {
	case "ios", "android", "web":
	default:
		writePriorityErr(w, http.StatusBadRequest, "device.bad_platform",
			"platform must be ios|android|web")
		return
	}
	switch in.App {
	case "tech", "sales", "staff_web":
	default:
		writePriorityErr(w, http.StatusBadRequest, "device.bad_app",
			"app must be tech|sales|staff_web")
		return
	}
	if _, err := h.pool.Exec(r.Context(), `
		INSERT INTO platform.device_tokens
			(user_id, token, platform, app, last_seen_at)
		VALUES ($1, $2, $3, $4, NOW())
		ON CONFLICT (token) DO UPDATE
			SET user_id = EXCLUDED.user_id,
			    customer_id = NULL,
			    platform = EXCLUDED.platform,
			    app = EXCLUDED.app,
			    last_seen_at = NOW()
	`, claims.UserID, in.Token, in.Platform, in.App); err != nil {
		writePriorityErr(w, http.StatusInternalServerError, "device.upsert_failed", err.Error())
		return
	}
	writePriorityJSON(w, http.StatusCreated, map[string]any{"ok": true})
}

// =============================================================================
// HRIS sync — read state + manual trigger (no actual provider wired yet)
//
// The real adapter (Mekari/Gajiku/etc.) ships in a follow-up; this
// endpoint shape lets the web admin "Refresh HRIS" button work today
// against a stub that just bumps the last_run timestamp.
// =============================================================================

func (h *PriorityHandler) hrisSyncState(w http.ResponseWriter, r *http.Request) {
	rows, err := h.pool.Query(r.Context(), `
		SELECT provider, last_run_at, last_success_at,
		       COALESCE(last_error, ''), rows_synced
		FROM identity.hris_sync_state
		ORDER BY provider ASC
	`)
	if err != nil {
		writePriorityErr(w, http.StatusInternalServerError, "hris.query_failed", err.Error())
		return
	}
	defer rows.Close()
	type row struct {
		Provider      string     `json:"provider"`
		LastRunAt     *time.Time `json:"last_run_at,omitempty"`
		LastSuccessAt *time.Time `json:"last_success_at,omitempty"`
		LastError     string     `json:"last_error,omitempty"`
		RowsSynced    int        `json:"rows_synced"`
	}
	out := []row{}
	for rows.Next() {
		var x row
		if err := rows.Scan(&x.Provider, &x.LastRunAt, &x.LastSuccessAt,
			&x.LastError, &x.RowsSynced); err == nil {
			out = append(out, x)
		}
	}
	writePriorityJSON(w, http.StatusOK, map[string]any{"items": out})
}

type hrisSyncReq struct {
	Provider string `json:"provider,omitempty"`
}

func (h *PriorityHandler) hrisSyncNow(w http.ResponseWriter, r *http.Request) {
	var in hrisSyncReq
	_ = json.NewDecoder(r.Body).Decode(&in)
	if in.Provider == "" {
		in.Provider = "stub"
	}
	// Stub provider: succeeds immediately with 0 rows.
	// Real adapters will call the upstream API here, upsert into
	// identity.user_availability with source='hris', and report the
	// row count + any errors.
	now := time.Now()
	if _, err := h.pool.Exec(r.Context(), `
		INSERT INTO identity.hris_sync_state
			(id, provider, last_run_at, last_success_at, rows_synced)
		VALUES (gen_random_uuid(), $1, $2, $2, 0)
		ON CONFLICT (provider) DO UPDATE
			SET last_run_at = EXCLUDED.last_run_at,
			    last_success_at = EXCLUDED.last_success_at,
			    last_error = NULL,
			    rows_synced = 0,
			    updated_at = NOW()
	`, in.Provider, now); err != nil {
		writePriorityErr(w, http.StatusInternalServerError, "hris.upsert_failed", err.Error())
		return
	}
	writePriorityJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"provider":    in.Provider,
		"rows_synced": 0,
		"note":        "Stub provider — real HRIS adapter is not yet wired.",
	})
}

// =============================================================================
// Helpers
// =============================================================================

// Silence unused-import lint when this file is the only one in build.
var _ = uuid.Nil

func writePriorityJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writePriorityErr(w http.ResponseWriter, status int, code, msg string) {
	writePriorityJSON(w, status, map[string]any{
		"error": map[string]string{"code": code, "message": msg},
	})
}
