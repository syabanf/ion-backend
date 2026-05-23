// Package http is the driving adapter for the platform/schemas
// bounded context — translates HTTP into UseCase calls.
//
// All routes live under whatever prefix the host service mounts them
// on (identity-svc mounts at root; the gateway proxies /api/platform
// → identity-svc). All write routes require platform.schema.manage;
// read routes accept platform.schema.read. Override write/read live
// behind platform.schema_override.{read,manage} so an operator with
// read-only access to definitions can still be granted override edit
// rights without leaking schema editorial control.
package http

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/platform/domain"
	"github.com/ion-core/backend/internal/platform/port"
	"github.com/ion-core/backend/pkg/auth"
	"github.com/ion-core/backend/pkg/errors"
	"github.com/ion-core/backend/pkg/httpserver"
)

type Handler struct {
	uc       port.SchemaUseCase
	verifier *auth.Verifier
}

func NewHandler(uc port.SchemaUseCase, verifier *auth.Verifier) *Handler {
	return &Handler{uc: uc, verifier: verifier}
}

// Mount — route map (the gateway proxies /api/platform → this service,
// stripping the /api/platform prefix; downstream we see plain /schemas
// and /customer-schemas paths):
//
//	Schemas
//	  GET    /schemas                           [platform.schema.read]
//	  GET    /schemas/{id}                      [platform.schema.read]
//	  POST   /schemas                           [platform.schema.manage]
//	  PATCH  /schemas/{id}                      [platform.schema.manage]
//	  POST   /schemas/{id}/publish              [platform.schema.manage]
//	  POST   /schemas/{id}/supersede            [platform.schema.manage]
//
//	Per-customer overrides + resolution
//	  GET    /customer-schemas/{customer_id}    [platform.schema_override.read]
//	      Returns: resolved body for the requested kind (or all kinds when no kind filter).
//	  PUT    /customer-schemas/{customer_id}/{kind}    [platform.schema_override.manage]
//	  DELETE /customer-schemas/{customer_id}/{kind}    [platform.schema_override.manage]
func (h *Handler) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(httpserver.RequireAuth(h.verifier))

		// Schemas
		r.With(httpserver.RequirePermission("platform.schema.read")).
			Get("/schemas", h.listSchemas)
		r.With(httpserver.RequirePermission("platform.schema.read")).
			Get("/schemas/{id}", h.getSchema)
		r.With(httpserver.RequirePermission("platform.schema.manage")).
			Post("/schemas", h.createSchema)
		r.With(httpserver.RequirePermission("platform.schema.manage")).
			Patch("/schemas/{id}", h.updateDraftSchema)
		r.With(httpserver.RequirePermission("platform.schema.manage")).
			Post("/schemas/{id}/publish", h.publishSchema)
		r.With(httpserver.RequirePermission("platform.schema.manage")).
			Post("/schemas/{id}/supersede", h.supersedeSchema)

		// Overrides + resolved view
		r.With(httpserver.RequirePermission("platform.schema_override.read")).
			Get("/customer-schemas/{customer_id}", h.getCustomerSchemas)
		r.With(httpserver.RequirePermission("platform.schema_override.manage")).
			Put("/customer-schemas/{customer_id}/{kind}", h.upsertOverride)
		r.With(httpserver.RequirePermission("platform.schema_override.manage")).
			Delete("/customer-schemas/{customer_id}/{kind}", h.deleteOverride)

		// Wave 116 — Content validators
		//   POST /schemas/{id}/validate         [platform.schema.validate]
		//   GET  /schemas/{id}/validation       [platform.schema.read]
		//   POST /schemas/validate-all          [platform.schema.validate]
		//   GET  /schemas/by-kind/{kind}/active [platform.schema.read]
		r.With(httpserver.RequirePermission("platform.schema.validate")).
			Post("/schemas/{id}/validate", h.validateSchema)
		r.With(httpserver.RequirePermission("platform.schema.read")).
			Get("/schemas/{id}/validation", h.getLatestValidation)
		r.With(httpserver.RequirePermission("platform.schema.validate")).
			Post("/schemas/validate-all", h.validateAllSchemas)
		r.With(httpserver.RequirePermission("platform.schema.read")).
			Get("/schemas/by-kind/{kind}/active", h.listActiveByKind)
	})
}

// =====================================================================
// Schema handlers
// =====================================================================

func (h *Handler) listSchemas(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page := httpserver.ParseIntDefault(q.Get("page"), 1)
	if page < 1 {
		page = 1
	}
	pageSize := httpserver.ParseIntDefault(q.Get("page_size"), 100)
	f := port.SchemaListFilter{
		Status: q.Get("status"),
		Code:   q.Get("code"),
		Limit:  pageSize,
		Offset: (page - 1) * pageSize,
	}
	if k := q.Get("kind"); k != "" {
		kind, err := domain.ParseSchemaKind(k)
		if err != nil {
			httpserver.WriteError(w, err)
			return
		}
		f.Kind = kind
	}
	items, total, err := h.uc.ListSchemas(r.Context(), f)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]schemaDTO, 0, len(items))
	for _, s := range items {
		out = append(out, toSchemaDTO(s))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"items":     out,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

func (h *Handler) getSchema(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "schema")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	s, err := h.uc.GetSchema(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toSchemaDTO(*s))
}

func (h *Handler) createSchema(w http.ResponseWriter, r *http.Request) {
	var req createSchemaRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	kind, err := domain.ParseSchemaKind(req.Kind)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	in := port.CreateSchemaInput{
		Kind:        kind,
		Code:        req.Code,
		Name:        req.Name,
		Description: req.Description,
		Body:        json.RawMessage(req.Body),
		Notes:       req.Notes,
	}
	if uid := actorUserID(r.Context()); uid != nil {
		in.CreatedBy = uid
	}
	s, err := h.uc.CreateSchema(r.Context(), in)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toSchemaDTO(*s))
}

func (h *Handler) updateDraftSchema(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "schema")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var req updateSchemaRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	in := port.UpdateSchemaDraftInput{
		ID:          id,
		Name:        req.Name,
		Description: req.Description,
		Notes:       req.Notes,
	}
	if len(req.Body) > 0 {
		in.Body = json.RawMessage(req.Body)
	}
	s, err := h.uc.UpdateDraftSchema(r.Context(), in)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toSchemaDTO(*s))
}

func (h *Handler) publishSchema(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "schema")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	s, err := h.uc.PublishSchema(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toSchemaDTO(*s))
}

func (h *Handler) supersedeSchema(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "schema")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	s, err := h.uc.SupersedeSchema(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toSchemaDTO(*s))
}

// =====================================================================
// Override / resolution handlers
// =====================================================================

// getCustomerSchemas serves two shapes off the same URL based on the
// `kind` query parameter:
//
//   - kind=billing      → { schema, override, resolved }  (single object)
//   - kind missing      → { items: [{ kind, schema, override, resolved }, …] }
//
// The single-object shape is what billing/commission services hit at
// runtime. The list shape is for the admin Customer 360 surface.
func (h *Handler) getCustomerSchemas(w http.ResponseWriter, r *http.Request) {
	customerID, err := parseUUID(chi.URLParam(r, "customer_id"), "customer")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	if k := r.URL.Query().Get("kind"); k != "" {
		kind, err := domain.ParseSchemaKind(k)
		if err != nil {
			httpserver.WriteError(w, err)
			return
		}
		resolved, schema, override, err := h.uc.ResolveSchemaForCustomer(r.Context(), customerID, kind)
		if err != nil {
			httpserver.WriteError(w, err)
			return
		}
		httpserver.WriteJSON(w, http.StatusOK, resolvedDTO{
			Kind:     string(kind),
			Schema:   toSchemaDTOPtr(schema),
			Override: toOverrideDTOPtr(override),
			Resolved: json.RawMessage(resolved),
		})
		return
	}

	// No kind filter — return one entry per kind. Caller can render
	// a table of "live rule sets" for the customer.
	kinds := []domain.SchemaKind{
		domain.SchemaKindBilling,
		domain.SchemaKindCommission,
		domain.SchemaKindSuspension,
		domain.SchemaKindService,
	}
	out := make([]resolvedDTO, 0, len(kinds))
	for _, k := range kinds {
		resolved, schema, override, err := h.uc.ResolveSchemaForCustomer(r.Context(), customerID, k)
		if err != nil {
			if errors.IsNotFound(err) {
				// No DEFAULT for that kind yet — surface a null
				// entry so the FE can render "not configured".
				out = append(out, resolvedDTO{Kind: string(k)})
				continue
			}
			httpserver.WriteError(w, err)
			return
		}
		out = append(out, resolvedDTO{
			Kind:     string(k),
			Schema:   toSchemaDTOPtr(schema),
			Override: toOverrideDTOPtr(override),
			Resolved: json.RawMessage(resolved),
		})
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (h *Handler) upsertOverride(w http.ResponseWriter, r *http.Request) {
	customerID, err := parseUUID(chi.URLParam(r, "customer_id"), "customer")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	kind, err := domain.ParseSchemaKind(chi.URLParam(r, "kind"))
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	var req upsertOverrideRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	if req.SchemaCode == "" {
		httpserver.WriteError(w, errors.Validation(
			"schema_override.code_required",
			"schema_code is required",
		))
		return
	}
	in := port.UpsertOverrideInput{
		CustomerID: customerID,
		Kind:       kind,
		SchemaCode: req.SchemaCode,
		Patch:      json.RawMessage(req.Patch),
		Reason:     req.Reason,
	}
	if req.SchemaID != "" {
		u, perr := parseUUID(req.SchemaID, "schema_id")
		if perr != nil {
			httpserver.WriteError(w, perr)
			return
		}
		in.SchemaID = &u
	}
	if req.ValidFrom != "" {
		t, perr := time.Parse(time.RFC3339, req.ValidFrom)
		if perr != nil {
			httpserver.WriteError(w, errors.Validation(
				"schema_override.valid_from_invalid",
				"valid_from must be RFC 3339",
			))
			return
		}
		in.ValidFrom = &t
	}
	if req.ValidUntil != "" {
		t, perr := time.Parse(time.RFC3339, req.ValidUntil)
		if perr != nil {
			httpserver.WriteError(w, errors.Validation(
				"schema_override.valid_until_invalid",
				"valid_until must be RFC 3339",
			))
			return
		}
		in.ValidUntil = &t
	}
	if uid := actorUserID(r.Context()); uid != nil {
		in.CreatedBy = uid
	}
	o, err := h.uc.UpsertOverride(r.Context(), in)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toOverrideDTO(*o))
}

func (h *Handler) deleteOverride(w http.ResponseWriter, r *http.Request) {
	customerID, err := parseUUID(chi.URLParam(r, "customer_id"), "customer")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	kind, err := domain.ParseSchemaKind(chi.URLParam(r, "kind"))
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	if err := h.uc.DeleteOverride(r.Context(), customerID, kind); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// =====================================================================
// Helpers
// =====================================================================

func parseUUID(s, field string) (uuid.UUID, error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil, errors.Validation(
			field+".id_invalid",
			field+" is not a valid uuid",
		)
	}
	return id, nil
}

// actorUserID pulls the authenticated user's UUID from the request
// context. Returns nil when no claims are attached.
func actorUserID(ctx context.Context) *uuid.UUID {
	c := httpserver.ClaimsFromContext(ctx)
	if c == nil {
		return nil
	}
	id := c.UserID
	return &id
}

// =====================================================================
// Wave 116 — Validation handlers
// =====================================================================

// validateSchema triggers the per-kind content validator for the schema
// id, persists a row in platform.schema_validation_results, and returns
// the result envelope. 400-class if the schema's kind has no registered
// validator (we encode that as a successful "no validator" response so
// admin UIs render gracefully).
func (h *Handler) validateSchema(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "schema")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	res, err := h.uc.ValidateSchemaContent(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toValidationDTOFromResult(*res))
}

// getLatestValidation returns the most recent validation result for the
// schema, or 404 if it has never been validated.
func (h *Handler) getLatestValidation(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "schema")
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	row, err := h.uc.LatestValidation(r.Context(), id)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := validationDTO{
		IsValid:          row.IsValid,
		Errors:           row.Errors,
		Warnings:         row.Warnings,
		ValidatorVersion: row.ValidatorVersion,
		ValidatedAt:      rfc3339(row.ValidatedAt),
		TriggeredBy:      row.TriggeredBy,
	}
	if out.Errors == nil {
		out.Errors = []string{}
	}
	if out.Warnings == nil {
		out.Warnings = []string{}
	}
	httpserver.WriteJSON(w, http.StatusOK, out)
}

// validateAllSchemas triggers the sweep across every published row.
// Returns counts; the per-row results are persisted and surfaced via
// the get-latest-validation endpoint.
func (h *Handler) validateAllSchemas(w http.ResponseWriter, r *http.Request) {
	invalid, total, err := h.uc.ValidateAllPublishedSchemas(r.Context())
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, validateAllResponseDTO{
		Total:   total,
		Invalid: invalid,
	})
}

// listActiveByKind returns published schemas of `kind` whose latest
// validation_results row is is_valid=true. Used by the admin Schema
// Picker to filter out schemas that haven't been cleaned up yet.
func (h *Handler) listActiveByKind(w http.ResponseWriter, r *http.Request) {
	kind, err := domain.ParseSchemaKind(chi.URLParam(r, "kind"))
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	items, err := h.uc.ListActiveByKind(r.Context(), kind)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]schemaDTO, 0, len(items))
	for _, s := range items {
		out = append(out, toSchemaDTO(s))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}
