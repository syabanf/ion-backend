// Package http is the HTTP surface for the NOC monitoring bounded
// context. Every route is gated by a permission key per the
// migration (nocmon.probe.read, nocmon.fault.acknowledge, etc.) so
// the noc_admin / noc_engineer / noc_viewer roles fall out of the
// JWT claims with no per-route code.
package http

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/nocmon/domain"
	"github.com/ion-core/backend/internal/nocmon/port"
	"github.com/ion-core/backend/pkg/auth"
	"github.com/ion-core/backend/pkg/errors"
	"github.com/ion-core/backend/pkg/httpserver"
)

// Handler bundles every NOC usecase + the auth verifier. Mount()
// registers the routes under /api/nocmon/*.
type Handler struct {
	probes   port.ProbeUseCase
	fiber    port.FiberUseCase
	faults   port.FaultUseCase
	topology port.TopologyUseCase
	alertWO  port.AlertWOUseCase
	verifier *auth.Verifier
}

func NewHandler(
	probes port.ProbeUseCase,
	fiber port.FiberUseCase,
	faults port.FaultUseCase,
	topology port.TopologyUseCase,
	alertWO port.AlertWOUseCase,
	verifier *auth.Verifier,
) *Handler {
	return &Handler{
		probes:   probes,
		fiber:    fiber,
		faults:   faults,
		topology: topology,
		alertWO:  alertWO,
		verifier: verifier,
	}
}

// Mount registers every route under /api/nocmon. Each route is
// guarded by a fine-grained permission key per the migration; the
// noc_* roles are pre-wired with the appropriate sets.
func (h *Handler) Mount(r chi.Router) {
	r.Route("/api/nocmon", func(r chi.Router) {
		r.Use(httpserver.RequireAuth(h.verifier))

		// Probes
		r.With(httpserver.RequirePermission("nocmon.probe.read")).Get("/probes", h.listProbes)
		r.With(httpserver.RequirePermission("nocmon.probe.write")).Post("/probes", h.createProbe)
		r.With(httpserver.RequirePermission("nocmon.probe.read")).Get("/probes/{id}", h.getProbe)
		r.With(httpserver.RequirePermission("nocmon.probe.read")).Get("/probes/{id}/samples", h.listSamples)
		r.With(httpserver.RequirePermission("nocmon.probe.write")).Post("/probes/{id}/sample", h.recordSample)
		r.With(httpserver.RequirePermission("nocmon.probe.write")).Post("/probes/{id}/deactivate", h.deactivateProbe)

		// Fiber
		r.With(httpserver.RequirePermission("nocmon.fiber.read")).Get("/fiber/links", h.listFiber)
		r.With(httpserver.RequirePermission("nocmon.fiber.read")).Get("/fiber/links/{id}", h.getFiber)
		r.With(httpserver.RequirePermission("nocmon.fiber.read")).Post("/fiber/links/{id}/attenuation", h.recordAttenuation)

		// Faults
		r.With(httpserver.RequirePermission("nocmon.fault.read")).Get("/faults", h.listFaults)
		r.With(httpserver.RequirePermission("nocmon.fault.write")).Post("/faults", h.openFault)
		r.With(httpserver.RequirePermission("nocmon.fault.read")).Get("/faults/{id}", h.getFault)
		r.With(httpserver.RequirePermission("nocmon.fault.acknowledge")).Post("/faults/{id}/acknowledge", h.ackFault)
		r.With(httpserver.RequirePermission("nocmon.fault.write")).Post("/faults/{id}/investigate", h.investigateFault)
		r.With(httpserver.RequirePermission("nocmon.fault.resolve")).Post("/faults/{id}/mitigate", h.mitigateFault)
		r.With(httpserver.RequirePermission("nocmon.fault.resolve")).Post("/faults/{id}/resolve", h.resolveFault)
		r.With(httpserver.RequirePermission("nocmon.alert.wo.create")).Post("/faults/{id}/create-wo", h.createWO)
		r.With(httpserver.RequirePermission("nocmon.fault.read")).Get("/faults/{id}/impact", h.listImpact)
		r.With(httpserver.RequirePermission("nocmon.fault.write")).Post("/faults/{id}/impact", h.linkImpact)

		// Topology
		r.With(httpserver.RequirePermission("nocmon.topology.read")).Get("/topology/{scope}/{id}", h.getTopology)
		r.With(httpserver.RequirePermission("nocmon.topology.read")).Post("/topology/{scope}/{id}/rebuild", h.rebuildTopology)
	})
}

// ---------------------------------------------------------------------
// DTOs — wire shapes for create/update inputs and response bodies.
// Kept inline to keep the handler file self-contained.
// ---------------------------------------------------------------------

type createProbeReq struct {
	CustomerID        string   `json:"customer_id"`
	PlanID            string   `json:"plan_id,omitempty"`
	Kind              string   `json:"probe_kind"`
	Target            string   `json:"probe_target,omitempty"`
	IntervalSeconds   int      `json:"interval_seconds,omitempty"`
	ThresholdWarn     *float64 `json:"threshold_warn,omitempty"`
	ThresholdCritical *float64 `json:"threshold_critical,omitempty"`
}

type recordSampleReq struct {
	Value     float64 `json:"value"`
	SampledAt string  `json:"sampled_at,omitempty"`
}

type openFaultReq struct {
	Kind       string `json:"kind"`
	Severity   string `json:"severity"`
	SourceID   string `json:"source_id,omitempty"`
	SourceKind string `json:"source_kind,omitempty"`
}

type attenuationReq struct {
	ValueDB    float64 `json:"value_db"`
	MeasuredAt string  `json:"measured_at,omitempty"`
	Source     string  `json:"source,omitempty"`
}

type mitigateReq struct {
	RootCause string `json:"root_cause"`
}

type linkImpactReq struct {
	CustomerID        string `json:"customer_id"`
	ImpactKind        string `json:"impact_kind"`
	ImpactStart       string `json:"impact_start,omitempty"`
	SLACreditEligible bool   `json:"sla_credit_eligible,omitempty"`
}

// ---------------------------------------------------------------------
// Probe handlers
// ---------------------------------------------------------------------

func (h *Handler) createProbe(w http.ResponseWriter, r *http.Request) {
	var req createProbeReq
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	custID, err := uuid.Parse(req.CustomerID)
	if err != nil {
		writeErr(w, errors.Validation("probe.customer_invalid", "customer_id is not a uuid"))
		return
	}
	in := port.CreateProbeInput{
		CustomerID:        custID,
		Kind:              domain.ProbeKind(req.Kind),
		Target:            req.Target,
		IntervalSeconds:   req.IntervalSeconds,
		ThresholdWarn:     req.ThresholdWarn,
		ThresholdCritical: req.ThresholdCritical,
	}
	if req.PlanID != "" {
		planID, perr := uuid.Parse(req.PlanID)
		if perr != nil {
			writeErr(w, errors.Validation("probe.plan_invalid", "plan_id is not a uuid"))
			return
		}
		in.PlanID = &planID
	}
	p, err := h.probes.CreateProbe(r.Context(), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, probeToDTO(*p))
}

func (h *Handler) getProbe(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "probe")
	if !ok {
		return
	}
	p, err := h.probes.GetProbe(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, probeToDTO(*p))
}

func (h *Handler) listProbes(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := port.ProbeListFilter{
		Kind:       domain.ProbeKind(q.Get("kind")),
		OnlyActive: q.Get("active") == "true",
		Limit:      httpserver.ParseIntDefault(q.Get("limit"), 50),
		Offset:     httpserver.ParseIntDefault(q.Get("offset"), 0) - 1,
	}
	if f.Offset < 0 {
		f.Offset = 0
	}
	if cid := q.Get("customer_id"); cid != "" {
		if id, err := uuid.Parse(cid); err == nil {
			f.CustomerID = &id
		}
	}
	items, total, err := h.probes.ListProbes(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	dtos := make([]any, 0, len(items))
	for _, p := range items {
		dtos = append(dtos, probeToDTO(p))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"data": dtos, "total": total,
	})
}

func (h *Handler) listSamples(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "probe")
	if !ok {
		return
	}
	q := r.URL.Query()
	f := port.SampleListFilter{
		ProbeID: id,
		Limit:   httpserver.ParseIntDefault(q.Get("limit"), 500),
	}
	if from := q.Get("from"); from != "" {
		if t, err := time.Parse(time.RFC3339, from); err == nil {
			f.From = &t
		}
	}
	if to := q.Get("to"); to != "" {
		if t, err := time.Parse(time.RFC3339, to); err == nil {
			f.To = &t
		}
	}
	items, err := h.probes.ListSamples(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	dtos := make([]any, 0, len(items))
	for _, s := range items {
		dtos = append(dtos, sampleToDTO(s))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"data": dtos})
}

func (h *Handler) recordSample(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "probe")
	if !ok {
		return
	}
	var req recordSampleReq
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	at := time.Now().UTC()
	if req.SampledAt != "" {
		if t, err := time.Parse(time.RFC3339, req.SampledAt); err == nil {
			at = t.UTC()
		}
	}
	s, err := h.probes.RecordSample(r.Context(), id, req.Value, at)
	if err != nil {
		writeErr(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, sampleToDTO(*s))
}

func (h *Handler) deactivateProbe(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "probe")
	if !ok {
		return
	}
	p, err := h.probes.DeactivateProbe(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, probeToDTO(*p))
}

// ---------------------------------------------------------------------
// Fiber handlers
// ---------------------------------------------------------------------

func (h *Handler) listFiber(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := port.FiberListFilter{
		Status: domain.FiberStatus(q.Get("status")),
		Limit:  httpserver.ParseIntDefault(q.Get("limit"), 50),
	}
	if cid := q.Get("customer_id"); cid != "" {
		if id, err := uuid.Parse(cid); err == nil {
			f.CustomerID = &id
		}
	}
	items, total, err := h.fiber.ListLinks(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	dtos := make([]any, 0, len(items))
	for _, l := range items {
		dtos = append(dtos, fiberToDTO(l))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"data": dtos, "total": total,
	})
}

func (h *Handler) getFiber(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "fiber")
	if !ok {
		return
	}
	l, err := h.fiber.GetLink(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, fiberToDTO(*l))
}

func (h *Handler) recordAttenuation(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "fiber")
	if !ok {
		return
	}
	var req attenuationReq
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	at := time.Now().UTC()
	if req.MeasuredAt != "" {
		if t, err := time.Parse(time.RFC3339, req.MeasuredAt); err == nil {
			at = t.UTC()
		}
	}
	source := req.Source
	if source == "" {
		source = "manual"
	}
	l, err := h.fiber.RecordAttenuation(r.Context(), id, req.ValueDB, at, source)
	if err != nil {
		writeErr(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, fiberToDTO(*l))
}

// ---------------------------------------------------------------------
// Fault handlers
// ---------------------------------------------------------------------

func (h *Handler) listFaults(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := port.FaultListFilter{
		Status:   domain.FaultStatus(q.Get("status")),
		Severity: domain.FaultSeverity(q.Get("severity")),
		Kind:     domain.FaultKind(q.Get("kind")),
		Limit:    httpserver.ParseIntDefault(q.Get("limit"), 50),
	}
	items, total, err := h.faults.ListFaults(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	dtos := make([]any, 0, len(items))
	for _, ev := range items {
		dtos = append(dtos, faultToDTO(ev))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{
		"data": dtos, "total": total,
	})
}

func (h *Handler) openFault(w http.ResponseWriter, r *http.Request) {
	var req openFaultReq
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	in := port.OpenFaultInput{
		Kind:       domain.FaultKind(req.Kind),
		Severity:   domain.FaultSeverity(req.Severity),
		SourceKind: req.SourceKind,
	}
	if req.SourceID != "" {
		if id, err := uuid.Parse(req.SourceID); err == nil {
			in.SourceID = &id
		}
	}
	f, err := h.faults.OpenFault(r.Context(), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, faultToDTO(*f))
}

func (h *Handler) getFault(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "fault")
	if !ok {
		return
	}
	f, err := h.faults.GetFault(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, faultToDTO(*f))
}

func (h *Handler) ackFault(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "fault")
	if !ok {
		return
	}
	by := userIDFromClaims(r)
	f, err := h.faults.AcknowledgeFault(r.Context(), id, by)
	if err != nil {
		writeErr(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, faultToDTO(*f))
}

func (h *Handler) investigateFault(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "fault")
	if !ok {
		return
	}
	by := userIDFromClaims(r)
	f, err := h.faults.InvestigateFault(r.Context(), id, by)
	if err != nil {
		writeErr(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, faultToDTO(*f))
}

func (h *Handler) mitigateFault(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "fault")
	if !ok {
		return
	}
	var req mitigateReq
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	by := userIDFromClaims(r)
	f, err := h.faults.MitigateFault(r.Context(), id, by, req.RootCause)
	if err != nil {
		writeErr(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, faultToDTO(*f))
}

func (h *Handler) resolveFault(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "fault")
	if !ok {
		return
	}
	by := userIDFromClaims(r)
	f, err := h.faults.ResolveFault(r.Context(), id, by)
	if err != nil {
		writeErr(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, faultToDTO(*f))
}

func (h *Handler) createWO(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "fault")
	if !ok {
		return
	}
	by := userIDFromClaims(r)
	f, err := h.alertWO.ConvertFaultToWO(r.Context(), id, by)
	if err != nil {
		writeErr(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, faultToDTO(*f))
}

func (h *Handler) listImpact(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "fault")
	if !ok {
		return
	}
	items, err := h.faults.ListImpact(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	dtos := make([]any, 0, len(items))
	for _, i := range items {
		dtos = append(dtos, impactToDTO(i))
	}
	httpserver.WriteJSON(w, http.StatusOK, map[string]any{"data": dtos})
}

func (h *Handler) linkImpact(w http.ResponseWriter, r *http.Request) {
	id, ok := httpserver.ParseUUIDParam(w, r, "id", "fault")
	if !ok {
		return
	}
	var req linkImpactReq
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	custID, err := uuid.Parse(req.CustomerID)
	if err != nil {
		writeErr(w, errors.Validation("impact.customer_invalid", "customer_id is not a uuid"))
		return
	}
	start := time.Now().UTC()
	if req.ImpactStart != "" {
		if t, perr := time.Parse(time.RFC3339, req.ImpactStart); perr == nil {
			start = t.UTC()
		}
	}
	in := port.LinkImpactInput{
		FaultEventID:      id,
		CustomerID:        custID,
		ImpactKind:        domain.ImpactKind(req.ImpactKind),
		ImpactStart:       start,
		SLACreditEligible: req.SLACreditEligible,
	}
	impact, err := h.faults.LinkImpact(r.Context(), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, impactToDTO(*impact))
}

// ---------------------------------------------------------------------
// Topology handlers
// ---------------------------------------------------------------------

func (h *Handler) getTopology(w http.ResponseWriter, r *http.Request) {
	scope, scopeID, ok := parseScope(w, r)
	if !ok {
		return
	}
	snap, err := h.topology.GetLatest(r.Context(), scope, scopeID)
	if err != nil {
		writeErr(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, topologyToDTO(*snap))
}

func (h *Handler) rebuildTopology(w http.ResponseWriter, r *http.Request) {
	scope, scopeID, ok := parseScope(w, r)
	if !ok {
		return
	}
	snap, err := h.topology.RebuildSnapshot(r.Context(), scope, scopeID)
	if err != nil {
		writeErr(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, topologyToDTO(*snap))
}

// parseScope reads the (scope, id) pair from the URL. The "regional"
// scope accepts the literal string "none" for the id parameter so the
// SPA can call /api/nocmon/topology/regional/none without inventing
// a fake uuid.
func parseScope(w http.ResponseWriter, r *http.Request) (domain.TopologyScope, *uuid.UUID, bool) {
	scopeRaw := strings.ToLower(chi.URLParam(r, "scope"))
	scope := domain.TopologyScope(scopeRaw)
	if !scope.Valid() {
		writeErr(w, errors.Validation("topology.scope_invalid", "scope must be regional/branch/sub_area/olt"))
		return "", nil, false
	}
	idRaw := chi.URLParam(r, "id")
	if scope == domain.TopologyScopeRegional && (idRaw == "" || idRaw == "none") {
		return scope, nil, true
	}
	id, err := uuid.Parse(idRaw)
	if err != nil {
		writeErr(w, errors.Validation("topology.id_invalid", "id is not a uuid"))
		return "", nil, false
	}
	return scope, &id, true
}

// ---------------------------------------------------------------------
// DTO mappers
// ---------------------------------------------------------------------

func probeToDTO(p domain.ServiceProbe) any {
	out := map[string]any{
		"id":                 p.ID,
		"customer_id":        p.CustomerID,
		"probe_kind":         string(p.ProbeKind),
		"probe_target":       p.ProbeTarget,
		"interval_seconds":   p.IntervalSeconds,
		"threshold_warn":     p.ThresholdWarn,
		"threshold_critical": p.ThresholdCritical,
		"is_active":          p.IsActive,
		"last_status":        string(p.LastStatus),
		"created_at":         httpserver.FormatRFC3339(p.CreatedAt),
		"updated_at":         httpserver.FormatRFC3339(p.UpdatedAt),
	}
	if p.PlanID != nil {
		out["plan_id"] = p.PlanID
	}
	if p.LastProbedAt != nil {
		out["last_probed_at"] = httpserver.FormatRFC3339(*p.LastProbedAt)
	}
	return out
}

func sampleToDTO(s domain.HealthSample) any {
	out := map[string]any{
		"id":         s.ID,
		"probe_id":   s.ProbeID,
		"sampled_at": httpserver.FormatRFC3339(s.SampledAt),
		"status":     string(s.Status),
	}
	if s.Value != nil {
		out["value"] = *s.Value
	}
	return out
}

func fiberToDTO(l domain.FiberLink) any {
	out := map[string]any{
		"id":                    l.ID,
		"onu_serial":            l.ONUSerial,
		"warn_threshold_db":     l.WarnThresholdDB,
		"critical_threshold_db": l.CriticalThresholdDB,
		"status":                string(l.Status),
		"created_at":            httpserver.FormatRFC3339(l.CreatedAt),
		"updated_at":            httpserver.FormatRFC3339(l.UpdatedAt),
	}
	if l.OLTPortID != nil {
		out["olt_port_id"] = l.OLTPortID
	}
	if l.CustomerID != nil {
		out["customer_id"] = l.CustomerID
	}
	if l.ExpectedLossDB != nil {
		out["expected_loss_db"] = *l.ExpectedLossDB
	}
	if l.LastMeasuredDB != nil {
		out["last_measured_db"] = *l.LastMeasuredDB
	}
	if l.LastMeasuredAt != nil {
		out["last_measured_at"] = httpserver.FormatRFC3339(*l.LastMeasuredAt)
	}
	return out
}

func faultToDTO(f domain.FaultEvent) any {
	out := map[string]any{
		"id":                    f.ID,
		"kind":                  string(f.Kind),
		"severity":              string(f.Severity),
		"status":                string(f.Status),
		"started_at":            httpserver.FormatRFC3339(f.StartedAt),
		"detected_at":           httpserver.FormatRFC3339(f.DetectedAt),
		"customer_impact_count": f.CustomerImpactCount,
		"root_cause":            f.RootCause,
		"source_kind":           f.SourceKind,
		"created_at":            httpserver.FormatRFC3339(f.CreatedAt),
		"updated_at":            httpserver.FormatRFC3339(f.UpdatedAt),
	}
	if f.SourceID != nil {
		out["source_id"] = f.SourceID
	}
	if f.AcknowledgedAt != nil {
		out["acknowledged_at"] = httpserver.FormatRFC3339(*f.AcknowledgedAt)
	}
	if f.AcknowledgedBy != nil {
		out["acknowledged_by"] = f.AcknowledgedBy
	}
	if f.ResolvedAt != nil {
		out["resolved_at"] = httpserver.FormatRFC3339(*f.ResolvedAt)
	}
	if f.ResolvedBy != nil {
		out["resolved_by"] = f.ResolvedBy
	}
	if f.TicketWOID != nil {
		out["ticket_wo_id"] = f.TicketWOID
	}
	return out
}

func impactToDTO(i domain.FaultImpact) any {
	out := map[string]any{
		"id":                  i.ID,
		"fault_event_id":      i.FaultEventID,
		"customer_id":         i.CustomerID,
		"impact_kind":         string(i.ImpactKind),
		"sla_credit_eligible": i.SLACreditEligible,
	}
	if i.ImpactStart != nil {
		out["impact_start"] = httpserver.FormatRFC3339(*i.ImpactStart)
	}
	if i.ImpactEnd != nil {
		out["impact_end"] = httpserver.FormatRFC3339(*i.ImpactEnd)
	}
	if i.NotifiedAt != nil {
		out["notified_at"] = httpserver.FormatRFC3339(*i.NotifiedAt)
	}
	return out
}

func topologyToDTO(s domain.TopologySnapshot) any {
	// Payload is raw JSON; pass through verbatim so the SPA can decode
	// it as needed without us re-encoding.
	var raw json.RawMessage = s.Payload
	out := map[string]any{
		"id":           s.ID,
		"scope":        string(s.Scope),
		"snapshot_at":  httpserver.FormatRFC3339(s.SnapshotAt),
		"node_count":   s.NodeCount,
		"edge_count":   s.EdgeCount,
		"generated_by": s.GeneratedBy,
		"payload":      raw,
	}
	if s.ScopeID != nil {
		out["scope_id"] = s.ScopeID
	}
	return out
}

// ---------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------

func userIDFromClaims(r *http.Request) uuid.UUID {
	c := httpserver.ClaimsFromContext(r.Context())
	if c == nil {
		return uuid.Nil
	}
	return c.UserID
}

func writeErr(w http.ResponseWriter, err error) {
	httpserver.WriteError(w, err)
}

// silence unused import on builds where strconv isn't needed (kept for
// future query param parsing).
var _ = strconv.Itoa
