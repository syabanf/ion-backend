// Package http exposes the Wave 104 audit query + chain-verify
// endpoints. Mountable on any *-svc that has the audit reader wired.
//
// Routes:
//
//	GET /api/audit/entries        [identity.audit.read]
//	GET /api/audit/chain/verify   [identity.audit.read]
//
// The handler is intentionally framework-thin: it parses query params,
// invokes the reader, and writes JSON. No business logic — the Reader
// itself enforces the query contract.
package http

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	auditpg "github.com/ion-core/backend/pkg/audit/postgres"
	"github.com/ion-core/backend/pkg/auth"
	"github.com/ion-core/backend/pkg/errors"
	"github.com/ion-core/backend/pkg/httpserver"
)

// Handler is the audit query HTTP surface.
type Handler struct {
	reader   *auditpg.Reader
	verifier *auth.Verifier
}

func NewHandler(reader *auditpg.Reader, verifier *auth.Verifier) *Handler {
	return &Handler{reader: reader, verifier: verifier}
}

// MountAuditRoutes attaches the query + verify endpoints under
// /api/audit. Wraps each in RequireAuth + identity.audit.read so an
// unprivileged caller can't fish for audit events.
//
// The route prefix is `/api/audit` so the api-gateway can route /audit/*
// at the gateway tier without rewriting. Callers that mount this on a
// router already prefixed with `/api` should drop the `/api` half — the
// helper mounts at the supplied router's root.
func (h *Handler) MountAuditRoutes(r chi.Router) {
	r.Route("/api/audit", func(r chi.Router) {
		r.Use(httpserver.RequireAuth(h.verifier))
		r.With(httpserver.RequirePermission("identity.audit.read")).
			Get("/entries", h.listEntries)
		r.With(httpserver.RequirePermission("identity.audit.read")).
			Get("/chain/verify", h.verifyChain)
	})
}

// listEntries returns audit-log rows matching the query params.
// Accepts:
//   - subject_type
//   - subject_id
//   - module
//   - user_id (uuid)
//   - from, to (RFC3339)
//   - limit (default 50, max 500)
//   - offset
//
// Response shape: { "entries": [...], "count": N }
func (h *Handler) listEntries(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := auditpg.QueryFilter{
		SubjectType: q.Get("subject_type"),
		SubjectID:   q.Get("subject_id"),
		Module:      q.Get("module"),
		Limit:       httpserver.ParseIntDefault(q.Get("limit"), 50),
		Offset:      httpserver.ParseIntDefault(q.Get("offset"), 0),
	}
	if uidRaw := q.Get("user_id"); uidRaw != "" {
		uid, err := uuid.Parse(uidRaw)
		if err != nil {
			httpserver.WriteError(w, errors.Validation("audit.user_id_invalid",
				"user_id is not a valid uuid"))
			return
		}
		f.UserID = &uid
	}
	if fromRaw := q.Get("from"); fromRaw != "" {
		t, err := time.Parse(time.RFC3339, fromRaw)
		if err != nil {
			httpserver.WriteError(w, errors.Validation("audit.from_invalid",
				"from must be RFC3339"))
			return
		}
		f.From = &t
	}
	if toRaw := q.Get("to"); toRaw != "" {
		t, err := time.Parse(time.RFC3339, toRaw)
		if err != nil {
			httpserver.WriteError(w, errors.Validation("audit.to_invalid",
				"to must be RFC3339"))
			return
		}
		f.To = &t
	}

	if h.reader == nil {
		httpserver.WriteError(w, errors.Internal("audit.not_configured",
			"audit reader is not configured for this service"))
		return
	}
	entries, err := h.reader.Query(r.Context(), f)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"entries": entries,
		"count":   len(entries),
	})
}

// verifyChain walks the audit_logs table across the supplied window and
// recomputes each row_hash to detect tampering. The window is required
// — operators must pin a date range so the verify call is bounded.
//
// Accepts query params:
//   - from, to (RFC3339)
//
// Response shape: ChainVerifyResult JSON (verified / broken / total /
// first_broken_id).
func (h *Handler) verifyChain(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var from, to time.Time
	if fromRaw := q.Get("from"); fromRaw != "" {
		t, err := time.Parse(time.RFC3339, fromRaw)
		if err != nil {
			httpserver.WriteError(w, errors.Validation("audit.from_invalid",
				"from must be RFC3339"))
			return
		}
		from = t
	}
	if toRaw := q.Get("to"); toRaw != "" {
		t, err := time.Parse(time.RFC3339, toRaw)
		if err != nil {
			httpserver.WriteError(w, errors.Validation("audit.to_invalid",
				"to must be RFC3339"))
			return
		}
		to = t
	}

	if h.reader == nil {
		httpserver.WriteError(w, errors.Internal("audit.not_configured",
			"audit reader is not configured for this service"))
		return
	}
	result, err := h.reader.VerifyChain(r.Context(), from, to)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, result)
}

// stringOr returns a if non-empty, else b. Helper for query params.
//
//nolint:unused // reserved for future option-with-default handling
func stringOr(a, b string) string {
	if a == "" {
		return b
	}
	return a
}

// intOr returns a if positive, else b. Helper for query params.
//
//nolint:unused // reserved for future option-with-default handling
func intOr(a string, b int) int {
	if a == "" {
		return b
	}
	n, err := strconv.Atoi(a)
	if err != nil || n <= 0 {
		return b
	}
	return n
}
