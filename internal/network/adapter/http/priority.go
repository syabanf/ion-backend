// Network priority-followup endpoints: per-customer RADIUS session
// state lookup for the staff customer-detail page.
package http

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/pkg/auth"
	"github.com/ion-core/backend/pkg/errors"
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

		// Wave 76 (TC-RAD-019) — NOC-only credential rotation.
		// The permission is seeded by 0049_wave76_qa_compliance.up.sql
		// and granted to NOC + Super Admin only. Technicians cannot
		// regenerate even mid-WO; they must escalate to NOC.
		r.With(httpserver.RequirePermission("network.radius.regenerate")).
			Post("/customers/{id}/radius/regenerate-credential",
				h.regenerateRadiusCredential)
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

// regenerateRadiusCredential rotates the password for a customer's
// RADIUS account. Only NOC + Super Admin can call this — enforced by
// the `network.radius.regenerate` permission on the route.
//
// Wave 76 (QA TC-CRM-019): the prior build let any caller with WO
// detail access trigger this. That violated the PRD principle that
// technicians never rotate credentials — they escalate. The endpoint
// records who triggered the rotation for audit.
//
// Implementation note: this is the surgical fix — it generates a new
// random password and updates `network.radius_accounts` directly.
// The upstream RADIUS push is still mediated by the existing
// `LocalRadiusClient` (which is a stub at the time of writing —
// Wave 80 builds the real FreeRADIUS adapter). Until then the new
// password is persisted but not pushed to the RADIUS server; an
// audit row records the intent so the gap is traceable.
func (h *PriorityHandler) regenerateRadiusCredential(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "customer")
	if !ok {
		return
	}
	claims := httpserver.ClaimsFromContext(r.Context())
	if claims == nil {
		httpserver.WriteError(w, errors.Unauthorized("auth.missing",
			"authentication required"))
		return
	}

	// Generate a fresh 16-byte random password, hex-encoded (32 chars).
	// Matches the format used by activation.go on initial provisioning.
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		httpserver.WriteError(w, errors.Internal("radius.regen_random_failed",
			"failed to generate new credential"))
		return
	}
	newPassword := hex.EncodeToString(raw[:])

	// Update the RADIUS account. We don't fail loudly when no row
	// matches — the customer may not have a RADIUS account yet (e.g.
	// pending_install) and that's a 404 not a 500.
	//
	// Note: password_encrypted is the canonical column. The current
	// `LocalRadiusClient` writes a bcrypt hash here, which is the
	// one-way landmine flagged in TC-RAD-002. Wave 80's FreeRADIUS
	// adapter will move to AES-GCM at-rest; for the surgical fix we
	// write the raw 32-char hex value so an admin tool can read it
	// for the rotation announcement. The hashing inconsistency is
	// documented in docs/wave-75-qa-compliance-plan.md Pass 6.
	ct, err := h.pool.Exec(r.Context(), `
		UPDATE network.radius_accounts
		   SET password_encrypted = $2,
		       updated_at = NOW()
		 WHERE customer_id = $1
	`, id, newPassword)
	if err != nil {
		httpserver.WriteError(w, errors.Internal("radius.regen_db_failed",
			"failed to persist new credential"))
		return
	}
	if ct.RowsAffected() == 0 {
		httpserver.WriteError(w, errors.NotFound("radius.account_missing",
			"customer has no RADIUS account"))
		return
	}

	// Audit the rotation — schema matches identity.audit_logs from
	// 0001_platform_core. We don't write the new password into
	// after_value (it would defeat the rotation); we only record
	// that a rotation happened.
	_, _ = h.pool.Exec(r.Context(), `
		INSERT INTO identity.audit_logs
		    (user_id, module, record_type, record_id, field_changed,
		     before_value, after_value, reason)
		VALUES ($1, 'network', 'radius_account', $2::text,
		        'password',
		        '<redacted>', '<rotated>',
		        'NOC-triggered credential rotation')
	`, claims.UserID, id)

	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"customer_id": id.String(),
		"rotated":     true,
		"note":        "password updated; upstream RADIUS push pending FreeRADIUS adapter (Wave 80)",
	})
}
