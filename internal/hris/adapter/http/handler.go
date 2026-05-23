// Package http is the HRIS bounded context's HTTP surface.
//
// Routes (all require auth + a per-route permission):
//
//	GET    /api/hris/employees                  [hris.employee.read]
//	POST   /api/hris/employees                  [hris.employee.write]    (upsert)
//	GET    /api/hris/employees/{employee_no}    [hris.employee.read]
//	POST   /api/hris/employees/{employee_no}/resign     [hris.employee.write]
//	POST   /api/hris/employees/{employee_no}/reinstate  [hris.employee.write]
//	GET    /api/hris/events                     [hris.event.read]
//	POST   /api/hris/events                     [hris.event.ingest]
//	POST   /api/hris/sync                       [hris.sync.run]
package http

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/hris/domain"
	"github.com/ion-core/backend/internal/hris/port"
	"github.com/ion-core/backend/internal/hris/usecase"
	"github.com/ion-core/backend/pkg/auth"
	derrors "github.com/ion-core/backend/pkg/errors"
	"github.com/ion-core/backend/pkg/httpserver"
)

// Handler mounts the HRIS routes onto a chi.Router.
type Handler struct {
	emp      *usecase.EmployeeService
	events   *usecase.EventService
	sync     *usecase.SyncService
	verifier *auth.Verifier
}

// NewHandler builds a Handler. Any of the three services may be nil — the
// corresponding routes will 503 with a clean error.
func NewHandler(emp *usecase.EmployeeService, events *usecase.EventService, sync *usecase.SyncService, verifier *auth.Verifier) *Handler {
	return &Handler{emp: emp, events: events, sync: sync, verifier: verifier}
}

// Mount registers all eight HRIS routes.
func (h *Handler) Mount(r chi.Router) {
	r.Route("/api/hris", func(r chi.Router) {
		r.Use(httpserver.RequireAuth(h.verifier))

		// Employees
		r.With(httpserver.RequirePermission("hris.employee.read")).
			Get("/employees", h.listEmployees)
		r.With(httpserver.RequirePermission("hris.employee.write")).
			Post("/employees", h.upsertEmployee)
		r.With(httpserver.RequirePermission("hris.employee.read")).
			Get("/employees/{employee_no}", h.getEmployee)
		r.With(httpserver.RequirePermission("hris.employee.write")).
			Post("/employees/{employee_no}/resign", h.resignEmployee)
		r.With(httpserver.RequirePermission("hris.employee.write")).
			Post("/employees/{employee_no}/reinstate", h.reinstateEmployee)

		// Events
		r.With(httpserver.RequirePermission("hris.event.read")).
			Get("/events", h.listEvents)
		r.With(httpserver.RequirePermission("hris.event.ingest")).
			Post("/events", h.ingestEvents)

		// Sync
		r.With(httpserver.RequirePermission("hris.sync.run")).
			Post("/sync", h.runSync)
	})
}

// =====================================================================
// Employees
// =====================================================================

type employeeDTO struct {
	ID                   string    `json:"id"`
	EmployeeNo           string    `json:"employee_no"`
	FullName             string    `json:"full_name"`
	Email                string    `json:"email,omitempty"`
	Phone                string    `json:"phone,omitempty"`
	Department           string    `json:"department,omitempty"`
	Position             string    `json:"position,omitempty"`
	ManagerEmployeeNo    string    `json:"manager_employee_no,omitempty"`
	HireDate             *string   `json:"hire_date,omitempty"`
	ResignDate           *string   `json:"resign_date,omitempty"`
	Status               string    `json:"status"`
	KYCCompleted         bool      `json:"kyc_completed"`
	NPWP                 string    `json:"npwp,omitempty"`
	BankAccountNo        string    `json:"bank_account_no,omitempty"`
	BranchID             *string   `json:"branch_id,omitempty"`
	RoleRecommendations  []string  `json:"role_recommendations,omitempty"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

func toEmployeeDTO(e domain.Employee) employeeDTO {
	dto := employeeDTO{
		ID:                e.ID.String(),
		EmployeeNo:        e.EmployeeNo,
		FullName:          e.FullName,
		Email:             e.Email,
		Phone:             e.Phone,
		Department:        e.Department,
		Position:          e.Position,
		ManagerEmployeeNo: e.ManagerEmployeeNo,
		Status:            string(e.Status),
		KYCCompleted:      e.KYCCompleted,
		NPWP:              e.NPWP,
		BankAccountNo:     e.BankAccountNo,
		RoleRecommendations: e.RoleRecommendations,
		CreatedAt:         e.CreatedAt,
		UpdatedAt:         e.UpdatedAt,
	}
	if e.HireDate != nil {
		s := e.HireDate.Format("2006-01-02")
		dto.HireDate = &s
	}
	if e.ResignDate != nil {
		s := e.ResignDate.Format("2006-01-02")
		dto.ResignDate = &s
	}
	if e.BranchID != nil {
		s := e.BranchID.String()
		dto.BranchID = &s
	}
	return dto
}

type upsertEmployeeRequest struct {
	EmployeeNo          string   `json:"employee_no"`
	FullName            string   `json:"full_name"`
	Email               string   `json:"email,omitempty"`
	Phone               string   `json:"phone,omitempty"`
	Department          string   `json:"department,omitempty"`
	Position            string   `json:"position,omitempty"`
	ManagerEmployeeNo   string   `json:"manager_employee_no,omitempty"`
	HireDate            string   `json:"hire_date,omitempty"`
	Status              string   `json:"status,omitempty"`
	KYCCompleted        bool     `json:"kyc_completed,omitempty"`
	NPWP                string   `json:"npwp,omitempty"`
	BankAccountNo       string   `json:"bank_account_no,omitempty"`
	BranchID            string   `json:"branch_id,omitempty"`
	RoleRecommendations []string `json:"role_recommendations,omitempty"`
}

func (h *Handler) listEmployees(w http.ResponseWriter, r *http.Request) {
	if h.emp == nil {
		writeErr(w, derrors.Wrap(derrors.KindUnavailable, "hris.employee.service_not_wired", "employee service is not configured", nil))
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	f := port.EmployeeFilter{
		Query:      q.Get("q"),
		Status:     domain.EmployeeStatus(q.Get("status")),
		Department: q.Get("department"),
		Limit:      limit,
		Offset:     offset,
	}
	if b := q.Get("branch_id"); b != "" {
		if u, err := uuid.Parse(b); err == nil {
			f.BranchID = &u
		}
	}
	items, total, err := h.emp.Search(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]employeeDTO, 0, len(items))
	for _, e := range items {
		out = append(out, toEmployeeDTO(e))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": out,
		"total": total,
	})
}

func (h *Handler) upsertEmployee(w http.ResponseWriter, r *http.Request) {
	if h.emp == nil {
		writeErr(w, derrors.Wrap(derrors.KindUnavailable, "hris.employee.service_not_wired", "employee service is not configured", nil))
		return
	}
	var req upsertEmployeeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, derrors.Validation("hris.bad_json", "invalid JSON body"))
		return
	}
	rec := port.EmployeeRecord{
		EmployeeNo:          req.EmployeeNo,
		FullName:            req.FullName,
		Email:               req.Email,
		Phone:               req.Phone,
		Department:          req.Department,
		Position:            req.Position,
		ManagerEmployeeNo:   req.ManagerEmployeeNo,
		Status:              domain.EmployeeStatus(req.Status),
		KYCCompleted:        req.KYCCompleted,
		NPWP:                req.NPWP,
		BankAccountNo:       req.BankAccountNo,
		RoleRecommendations: req.RoleRecommendations,
	}
	if req.HireDate != "" {
		if t, err := time.Parse("2006-01-02", req.HireDate); err == nil {
			rec.HireDate = &t
		}
	}
	if req.BranchID != "" {
		if u, err := uuid.Parse(req.BranchID); err == nil {
			rec.BranchID = &u
		}
	}
	e, err := h.emp.Upsert(r.Context(), rec)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toEmployeeDTO(*e))
}

func (h *Handler) getEmployee(w http.ResponseWriter, r *http.Request) {
	if h.emp == nil {
		writeErr(w, derrors.Wrap(derrors.KindUnavailable, "hris.employee.service_not_wired", "employee service is not configured", nil))
		return
	}
	employeeNo := chi.URLParam(r, "employee_no")
	e, err := h.emp.FindByEmployeeNo(r.Context(), employeeNo)
	if err != nil {
		writeErr(w, err)
		return
	}
	if e == nil {
		writeErr(w, derrors.NotFound("hris.employee_not_found", "employee not found"))
		return
	}
	writeJSON(w, http.StatusOK, toEmployeeDTO(*e))
}

type resignEmployeeRequest struct {
	ResignDate string `json:"resign_date"`
	Reason     string `json:"reason,omitempty"`
}

func (h *Handler) resignEmployee(w http.ResponseWriter, r *http.Request) {
	if h.emp == nil {
		writeErr(w, derrors.Wrap(derrors.KindUnavailable, "hris.employee.service_not_wired", "employee service is not configured", nil))
		return
	}
	employeeNo := chi.URLParam(r, "employee_no")
	var req resignEmployeeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, derrors.Validation("hris.bad_json", "invalid JSON body"))
		return
	}
	at := time.Now().UTC()
	if req.ResignDate != "" {
		if t, err := time.Parse("2006-01-02", req.ResignDate); err == nil {
			at = t
		}
	}
	e, err := h.emp.Resign(r.Context(), employeeNo, at, req.Reason)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toEmployeeDTO(*e))
}

func (h *Handler) reinstateEmployee(w http.ResponseWriter, r *http.Request) {
	if h.emp == nil {
		writeErr(w, derrors.Wrap(derrors.KindUnavailable, "hris.employee.service_not_wired", "employee service is not configured", nil))
		return
	}
	employeeNo := chi.URLParam(r, "employee_no")
	e, err := h.emp.Reinstate(r.Context(), employeeNo)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toEmployeeDTO(*e))
}

// =====================================================================
// Events
// =====================================================================

type eventDTO struct {
	ID              string         `json:"id"`
	EmployeeNo      string         `json:"employee_no"`
	Kind            string         `json:"kind"`
	Payload         map[string]any `json:"payload,omitempty"`
	OccurredAt      time.Time      `json:"occurred_at"`
	IngestedAt      time.Time      `json:"ingested_at"`
	Source          string         `json:"source"`
	Processed       bool           `json:"processed"`
	ProcessedAt     *time.Time     `json:"processed_at,omitempty"`
	ProcessingError string         `json:"processing_error,omitempty"`
}

func toEventDTO(e domain.EmployeeEvent) eventDTO {
	return eventDTO{
		ID:              e.ID.String(),
		EmployeeNo:      e.EmployeeNo,
		Kind:            string(e.Kind),
		Payload:         e.Payload,
		OccurredAt:      e.OccurredAt,
		IngestedAt:      e.IngestedAt,
		Source:          e.Source,
		Processed:       e.Processed,
		ProcessedAt:     e.ProcessedAt,
		ProcessingError: e.ProcessingError,
	}
}

type ingestEventRequest struct {
	EmployeeNo string         `json:"employee_no"`
	Kind       string         `json:"kind"`
	Payload    map[string]any `json:"payload,omitempty"`
	OccurredAt string         `json:"occurred_at"`
	Source     string         `json:"source,omitempty"`
}

func (h *Handler) listEvents(w http.ResponseWriter, r *http.Request) {
	if h.events == nil {
		writeErr(w, derrors.Wrap(derrors.KindUnavailable, "hris.event.service_not_wired", "event service is not configured", nil))
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	f := port.EventFilter{
		EmployeeNo: q.Get("employee_no"),
		Kind:       domain.EventKind(q.Get("kind")),
		Limit:      limit,
		Offset:     offset,
	}
	if p := q.Get("processed"); p != "" {
		b := p == "true"
		f.Processed = &b
	}
	items, total, err := h.events.ListEvents(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]eventDTO, 0, len(items))
	for _, e := range items {
		out = append(out, toEventDTO(e))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": out,
		"total": total,
	})
}

func (h *Handler) ingestEvents(w http.ResponseWriter, r *http.Request) {
	if h.events == nil {
		writeErr(w, derrors.Wrap(derrors.KindUnavailable, "hris.event.service_not_wired", "event service is not configured", nil))
		return
	}
	// Accept either a single event or an array.
	var reqs []ingestEventRequest
	body, err := readAll(r)
	if err != nil {
		writeErr(w, derrors.Validation("hris.bad_body", "could not read body"))
		return
	}
	if len(body) > 0 && body[0] == '[' {
		if err := json.Unmarshal(body, &reqs); err != nil {
			writeErr(w, derrors.Validation("hris.bad_json", "invalid JSON body"))
			return
		}
	} else {
		var single ingestEventRequest
		if err := json.Unmarshal(body, &single); err != nil {
			writeErr(w, derrors.Validation("hris.bad_json", "invalid JSON body"))
			return
		}
		reqs = []ingestEventRequest{single}
	}
	events := make([]*domain.EmployeeEvent, 0, len(reqs))
	for _, req := range reqs {
		occurred := time.Now().UTC()
		if req.OccurredAt != "" {
			if t, err := time.Parse(time.RFC3339, req.OccurredAt); err == nil {
				occurred = t
			}
		}
		ev, err := domain.NewEmployeeEvent(req.EmployeeNo, domain.EventKind(req.Kind), req.Payload, occurred, req.Source)
		if err != nil {
			writeErr(w, err)
			return
		}
		events = append(events, ev)
	}
	n, err := h.events.IngestEvents(r.Context(), events)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"inserted_new": n,
		"total":        len(events),
	})
}

// =====================================================================
// Sync
// =====================================================================

func (h *Handler) runSync(w http.ResponseWriter, r *http.Request) {
	if h.sync == nil {
		writeErr(w, derrors.Wrap(derrors.KindUnavailable, "hris.sync.service_not_wired", "sync service is not configured", nil))
		return
	}
	res, err := h.sync.RunFullSync(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"employees_upserted": res.EmployeesUpserted,
		"events_ingested":    res.EventsIngested,
		"events_processed":   res.EventsProcessed,
		"started_at":         res.StartedAt,
		"finished_at":        res.FinishedAt,
		"err":                res.Err,
	})
}

// =====================================================================
// helpers
// =====================================================================

func writeJSON(w http.ResponseWriter, status int, body any) {
	httpserver.WriteJSON(w, status, body)
}

func writeErr(w http.ResponseWriter, err error) {
	httpserver.WriteError(w, err)
}

func readAll(r *http.Request) ([]byte, error) {
	const maxBody = 1 << 20 // 1 MiB
	buf := make([]byte, 0, 1024)
	tmp := make([]byte, 4096)
	total := 0
	for {
		n, err := r.Body.Read(tmp)
		if n > 0 {
			total += n
			if total > maxBody {
				return nil, derrors.Validation("hris.body_too_large", "body too large")
			}
			buf = append(buf, tmp[:n]...)
		}
		if err != nil {
			if err.Error() == "EOF" || err.Error() == "unexpected EOF" {
				return buf, nil
			}
			// Hit terminal err — treat as end-of-stream if we got data.
			if total > 0 {
				return buf, nil
			}
			return nil, err
		}
	}
}

// Keep ctx import alive.
var _ = context.Background
