// Wave 125 — HTTP routes for the BulkExecutorService.
//
// These routes live alongside the Wave 71 handler (`handler.go`) but
// operate on the new `operations.bulk_jobs` table + the per-kind item
// tables. The Wave 71 surface (legacy `operations.bulk_operations`) is
// untouched.
//
// Routes mounted by ExecutorHandler.Mount:
//
//	POST /api/operations/bulk/plan-change    — CSV upload + dry_run
//	POST /api/operations/bulk/odp-migration  — CSV upload + dry_run
//	POST /api/operations/bulk/wo-creation    — CSV upload + dry_run
//	POST /api/operations/bulk/jobs/{id}/run     — run / resume
//	POST /api/operations/bulk/jobs/{id}/cancel
//	GET  /api/operations/bulk/jobs              — list (filter kind/status)
//	GET  /api/operations/bulk/jobs/{id}         — detail
//	GET  /api/operations/bulk/jobs/{id}/items   — paginated items
package http

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/operations/domain"
	"github.com/ion-core/backend/internal/operations/port"
	"github.com/ion-core/backend/internal/operations/usecase"
	"github.com/ion-core/backend/pkg/auth"
	derrors "github.com/ion-core/backend/pkg/errors"
	"github.com/ion-core/backend/pkg/httpserver"
)

// ExecutorHandler exposes the Bulk Executor surface.
type ExecutorHandler struct {
	verifier *auth.Verifier
	exec     *usecase.BulkExecutorService
	importer *usecase.BulkCSVImporter
	jobs     port.BulkJobRepository
	bpcs     port.BulkPlanChangeItemRepository
	boms     port.BulkODPMigrationItemRepository
	bwos     port.BulkWOCreationItemRepository
}

// ExecutorHandlerDeps — group constructor input.
type ExecutorHandlerDeps struct {
	Verifier *auth.Verifier
	Exec     *usecase.BulkExecutorService
	Importer *usecase.BulkCSVImporter
	Jobs     port.BulkJobRepository
	BPCItems port.BulkPlanChangeItemRepository
	BOMItems port.BulkODPMigrationItemRepository
	BWOItems port.BulkWOCreationItemRepository
}

func NewExecutorHandler(d ExecutorHandlerDeps) *ExecutorHandler {
	return &ExecutorHandler{
		verifier: d.Verifier,
		exec:     d.Exec,
		importer: d.Importer,
		jobs:     d.Jobs,
		bpcs:     d.BPCItems,
		boms:     d.BOMItems,
		bwos:     d.BWOItems,
	}
}

// Mount registers the bulk executor routes. The api-gateway strips
// `/api/operations` so we mount at `/bulk/...`.
func (h *ExecutorHandler) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(httpserver.RequireAuth(h.verifier))

		// CSV import — one route per kind.
		r.With(httpserver.RequirePermission("operations.bulk_plan_change.run")).
			Post("/bulk/plan-change", h.uploadPlanChange)
		r.With(httpserver.RequirePermission("operations.bulk_odp_migration.run")).
			Post("/bulk/odp-migration", h.uploadODPMigration)
		r.With(httpserver.RequirePermission("operations.bulk_wo_creation.run")).
			Post("/bulk/wo-creation", h.uploadWOCreation)

		// Job lifecycle.
		r.With(httpserver.RequirePermission("operations.bulk.run")).
			Post("/bulk/jobs/{id}/run", h.runJob)
		r.With(httpserver.RequirePermission("operations.bulk.cancel")).
			Post("/bulk/jobs/{id}/cancel", h.cancelJob)

		// Read.
		r.With(httpserver.RequirePermission("operations.bulk.read")).
			Get("/bulk/jobs", h.listJobs)
		r.With(httpserver.RequirePermission("operations.bulk.read")).
			Get("/bulk/jobs/{id}", h.getJob)
		r.With(httpserver.RequirePermission("operations.bulk.read")).
			Get("/bulk/jobs/{id}/items", h.listItems)
	})
}

// =====================================================================
// CSV upload handlers
// =====================================================================

const maxCSVBytes = 16 << 20 // 16 MiB — bounded so a misclicked drag-drop can't OOM

func (h *ExecutorHandler) uploadPlanChange(w http.ResponseWriter, r *http.Request) {
	dryRun, file, ferr := h.openMultipartCSV(r)
	if ferr != nil {
		httpserver.WriteError(w, ferr)
		return
	}
	defer file.Close()
	creator := h.actorID(r.Context())
	sum, err := h.importer.ImportPlanChangeCSV(r.Context(), file, dryRun, creator)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, sum)
}

func (h *ExecutorHandler) uploadODPMigration(w http.ResponseWriter, r *http.Request) {
	dryRun, file, ferr := h.openMultipartCSV(r)
	if ferr != nil {
		httpserver.WriteError(w, ferr)
		return
	}
	defer file.Close()
	creator := h.actorID(r.Context())
	sum, err := h.importer.ImportODPMigrationCSV(r.Context(), file, dryRun, creator)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, sum)
}

func (h *ExecutorHandler) uploadWOCreation(w http.ResponseWriter, r *http.Request) {
	dryRun, file, ferr := h.openMultipartCSV(r)
	if ferr != nil {
		httpserver.WriteError(w, ferr)
		return
	}
	defer file.Close()
	creator := h.actorID(r.Context())
	sum, err := h.importer.ImportWOCreationCSV(r.Context(), file, dryRun, creator)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, sum)
}

// =====================================================================
// Lifecycle handlers
// =====================================================================

func (h *ExecutorHandler) runJob(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpserver.WriteError(w, derrors.Validation("bulk.id_invalid", "id is not a uuid"))
		return
	}
	// Look up the job to pick the right kind-specific run path.
	job, err := h.jobs.FindByID(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	if job == nil {
		httpserver.WriteError(w, derrors.NotFound("bulk.not_found", "bulk job not found"))
		return
	}
	var out *domain.BulkJob
	switch job.Kind {
	case domain.BulkJobPlanChange:
		out, err = h.exec.RunBulkPlanChange(r.Context(), id)
	case domain.BulkJobODPMigration:
		out, err = h.exec.RunBulkODPMigration(r.Context(), id)
	case domain.BulkJobWOCreation:
		out, err = h.exec.RunBulkWOCreation(r.Context(), id)
	default:
		httpserver.WriteError(w, derrors.Validation("bulk.kind_unsupported",
			"executor not implemented for this kind"))
		return
	}
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, jobToDTO(out))
}

func (h *ExecutorHandler) cancelJob(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpserver.WriteError(w, derrors.Validation("bulk.id_invalid", "id is not a uuid"))
		return
	}
	out, err := h.exec.CancelJob(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, jobToDTO(out))
}

// =====================================================================
// Read handlers
// =====================================================================

func (h *ExecutorHandler) listJobs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := port.BulkJobFilter{
		Kind:   q.Get("kind"),
		Status: q.Get("status"),
	}
	f.Limit, _ = strconv.Atoi(q.Get("limit"))
	f.Offset, _ = strconv.Atoi(q.Get("offset"))
	items, total, err := h.jobs.List(r.Context(), f)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]bulkJobDTO, 0, len(items))
	for i := range items {
		out = append(out, jobToDTO(&items[i]))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"items": out,
		"total": total,
	})
}

func (h *ExecutorHandler) getJob(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpserver.WriteError(w, derrors.Validation("bulk.id_invalid", "id is not a uuid"))
		return
	}
	job, err := h.jobs.FindByID(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	if job == nil {
		httpserver.WriteError(w, derrors.NotFound("bulk.not_found", "bulk job not found"))
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, jobToDTO(job))
}

func (h *ExecutorHandler) listItems(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpserver.WriteError(w, derrors.Validation("bulk.id_invalid", "id is not a uuid"))
		return
	}
	job, err := h.jobs.FindByID(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	if job == nil {
		httpserver.WriteError(w, derrors.NotFound("bulk.not_found", "bulk job not found"))
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if limit <= 0 {
		limit = 100
	}
	switch job.Kind {
	case domain.BulkJobPlanChange:
		items, err := h.bpcs.ListByJob(r.Context(), id, limit, offset)
		if err != nil {
			httpserver.WriteError(w, err)
			return
		}
		httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "kind": "plan_change"})
	case domain.BulkJobODPMigration:
		items, err := h.boms.ListByJob(r.Context(), id, limit, offset)
		if err != nil {
			httpserver.WriteError(w, err)
			return
		}
		httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "kind": "odp_migration"})
	case domain.BulkJobWOCreation:
		items, err := h.bwos.ListByJob(r.Context(), id, limit, offset)
		if err != nil {
			httpserver.WriteError(w, err)
			return
		}
		httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "kind": "wo_creation"})
	default:
		httpserver.WriteError(w, derrors.Validation("bulk.kind_unsupported",
			"item list not implemented for this kind"))
	}
}

// =====================================================================
// Helpers
// =====================================================================

type bulkJobDTO struct {
	ID              string          `json:"id"`
	Kind            string          `json:"kind"`
	Status          string          `json:"status"`
	TotalItems      int             `json:"total_items"`
	ProcessedItems  int             `json:"processed_items"`
	SucceededItems  int             `json:"succeeded_items"`
	FailedItems     int             `json:"failed_items"`
	SkippedItems    int             `json:"skipped_items"`
	StartedAt       *time.Time      `json:"started_at,omitempty"`
	CompletedAt     *time.Time      `json:"completed_at,omitempty"`
	ErrorSummary    json.RawMessage `json:"error_summary,omitempty"`
	DryRun          bool            `json:"dry_run"`
	CreatedBy       string          `json:"created_by,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

func jobToDTO(j *domain.BulkJob) bulkJobDTO {
	d := bulkJobDTO{
		ID:             j.ID.String(),
		Kind:           string(j.Kind),
		Status:         string(j.Status),
		TotalItems:     j.TotalItems,
		ProcessedItems: j.ProcessedItems,
		SucceededItems: j.SucceededItems,
		FailedItems:    j.FailedItems,
		SkippedItems:   j.SkippedItems,
		StartedAt:      j.StartedAt,
		CompletedAt:    j.CompletedAt,
		DryRun:         j.DryRun,
		CreatedAt:      j.CreatedAt,
		UpdatedAt:      j.UpdatedAt,
	}
	if j.CreatedBy != nil {
		d.CreatedBy = j.CreatedBy.String()
	}
	if j.ErrorSummary != nil {
		if b, err := json.Marshal(j.ErrorSummary); err == nil {
			d.ErrorSummary = b
		}
	}
	return d
}

// openMultipartCSV pulls the CSV file from a multipart form. Accepts a
// `dry_run` form field for the executor switch.
func (h *ExecutorHandler) openMultipartCSV(r *http.Request) (dryRun bool, file multipartCloser, err error) {
	if err := r.ParseMultipartForm(maxCSVBytes); err != nil {
		return false, nil, derrors.Validation("bulk.multipart_invalid",
			"could not parse multipart form")
	}
	f, _, fherr := r.FormFile("file")
	if fherr != nil {
		return false, nil, derrors.Validation("bulk.file_missing",
			"form field 'file' is required")
	}
	dryRun = r.FormValue("dry_run") == "true" || r.FormValue("dry_run") == "1"
	return dryRun, f, nil
}

// multipartCloser is the minimum interface we use from the multipart
// file — it's io.Reader + io.Closer. Keeping it narrow lets tests pass
// raw bytes.
type multipartCloser interface {
	Read(p []byte) (int, error)
	Close() error
}

func (h *ExecutorHandler) actorID(ctx context.Context) *uuid.UUID {
	c := httpserver.ClaimsFromContext(ctx)
	if c == nil {
		return nil
	}
	id := c.UserID
	return &id
}
