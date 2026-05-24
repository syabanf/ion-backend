// Wave 128D — admin-triggered importer route.
//
// POST /api/cs/importer/run runs a single TicketImporterService.RunOnce
// pass synchronously and returns the ImportSummary as JSON. Idempotent
// — the underlying ON CONFLICT semantics guarantee a re-run is a
// cheap no-op.
//
// Mounted via the existing /api/cs route group (so RequireAuth +
// permission middleware behave the same as the rest of the surface).
// WithImporter is nil-safe; if the service isn't wired the route
// simply isn't attached and the cron path remains the only way to
// run the importer.
package http

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	csusecase "github.com/ion-core/backend/internal/cs/usecase"
	"github.com/ion-core/backend/pkg/httpserver"
)

// WithImporter wires the Wave 128D importer service.
func (h *Handler) WithImporter(svc *csusecase.TicketImporterService) *Handler {
	h.importer = svc
	return h
}

// MountWave128D attaches the admin-triggered importer route. Same
// pattern as MountWave126 — separate mount so cs-svc deployments that
// don't yet wire the importer aren't disturbed.
//
// The caller mounts after the main Mount() so the parent /api/cs
// chi.Router subgroup exists.
func (h *Handler) MountWave128D(r chi.Router) {
	if h.importer == nil {
		return
	}
	r.Route("/api/cs/importer", func(r chi.Router) {
		r.Use(httpserver.RequireAuth(h.verifier))
		r.With(httpserver.RequirePermission("cs.importer.run")).
			Post("/run", h.runImporter)
	})
}

func (h *Handler) runImporter(w http.ResponseWriter, r *http.Request) {
	if h.importer == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": "cs.importer.unavailable",
			"msg":   "importer service not wired",
		})
		return
	}
	summary, err := h.importer.RunOnce(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, summary)
}
