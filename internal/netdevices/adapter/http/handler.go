// Package http is the driving adapter for the netdevices bounded
// context. Same conventions as partnership / reseller:
//   - One handler covers the full surface; tenant scoping (when needed
//     in a future wave) lives in the permission set, not in path
//     prefixes.
//   - DTOs live next to the handler they're used by.
//   - Auth chain: RequireAuth → RequirePermission per route.
package http

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/netdevices/domain"
	"github.com/ion-core/backend/internal/netdevices/port"
	"github.com/ion-core/backend/internal/netdevices/usecase"
	"github.com/ion-core/backend/pkg/auth"
	"github.com/ion-core/backend/pkg/errors"
	"github.com/ion-core/backend/pkg/httpserver"
)

// Handler exposes the full netdev HTTP surface.
type Handler struct {
	devices     *usecase.DeviceService
	firmware    *usecase.FirmwareService
	swaps       *usecase.SwapService
	rma         *usecase.RMAService
	health      *usecase.HealthService
	compliance  *usecase.ComplianceService
	verifier    *auth.Verifier
}

func NewHandler(
	devices *usecase.DeviceService,
	firmware *usecase.FirmwareService,
	swaps *usecase.SwapService,
	rma *usecase.RMAService,
	health *usecase.HealthService,
	compliance *usecase.ComplianceService,
	verifier *auth.Verifier,
) *Handler {
	return &Handler{
		devices: devices, firmware: firmware, swaps: swaps,
		rma: rma, health: health, compliance: compliance,
		verifier: verifier,
	}
}

// Mount wires every netdev route onto the supplied chi router.
//
// Routes:
//
//	# Devices
//	GET    /api/netdev/devices                                  [netdev.device.read]
//	POST   /api/netdev/devices                                  [netdev.device.write]
//	GET    /api/netdev/devices/{id}                             [netdev.device.read]
//	POST   /api/netdev/devices/{id}/allocate                    [netdev.device.write]
//	POST   /api/netdev/devices/{id}/commission                  [netdev.device.commission]
//	POST   /api/netdev/devices/{id}/decommission                [netdev.device.decommission]
//
//	# Firmware
//	POST   /api/netdev/firmware/versions                        [netdev.firmware.upgrade]
//	POST   /api/netdev/firmware/upgrade-jobs                    [netdev.firmware.upgrade]
//	POST   /api/netdev/firmware/upgrade-jobs/{id}/stage         [netdev.firmware.upgrade]
//	POST   /api/netdev/firmware/upgrade-jobs/{id}/start         [netdev.firmware.upgrade]
//	POST   /api/netdev/firmware/upgrade-jobs/{id}/complete      [netdev.firmware.upgrade]
//	POST   /api/netdev/firmware/upgrade-jobs/{id}/fail          [netdev.firmware.upgrade]
//
//	# Swaps
//	POST   /api/netdev/swaps                                    [netdev.swap.request]
//	POST   /api/netdev/swaps/{id}/approve                       [netdev.swap.approve]
//	POST   /api/netdev/swaps/{id}/stage                         [netdev.swap.approve]
//	POST   /api/netdev/swaps/{id}/assign-technician             [netdev.swap.approve]
//	POST   /api/netdev/swaps/{id}/complete                      [netdev.swap.execute]
//
//	# RMA
//	POST   /api/netdev/rma                                      [netdev.rma.write]
//	POST   /api/netdev/rma/{id}/ship                            [netdev.rma.write]
//	POST   /api/netdev/rma/{id}/receive                         [netdev.rma.write]
//	POST   /api/netdev/rma/{id}/close                           [netdev.rma.close]
//
//	# Health + Compliance
//	POST   /api/netdev/devices/{id}/health-snapshot             [netdev.health.read]
//	GET    /api/netdev/devices/{id}/health-history              [netdev.health.read]
//	POST   /api/netdev/compliance/runs                          [netdev.compliance.read]
//	GET    /api/netdev/compliance/runs/{id}                     [netdev.compliance.read]
func (h *Handler) Mount(r chi.Router) {
	r.Route("/api/netdev", func(r chi.Router) {
		r.Use(httpserver.RequireAuth(h.verifier))

		// Devices
		r.With(httpserver.RequirePermission("netdev.device.read")).
			Get("/devices", h.listDevices)
		r.With(httpserver.RequirePermission("netdev.device.write")).
			Post("/devices", h.registerDevice)
		r.With(httpserver.RequirePermission("netdev.device.read")).
			Get("/devices/{id}", h.getDevice)
		r.With(httpserver.RequirePermission("netdev.device.write")).
			Post("/devices/{id}/allocate", h.allocateDevice)
		r.With(httpserver.RequirePermission("netdev.device.commission")).
			Post("/devices/{id}/commission", h.commissionDevice)
		r.With(httpserver.RequirePermission("netdev.device.decommission")).
			Post("/devices/{id}/decommission", h.decommissionDevice)

		// Firmware
		r.With(httpserver.RequirePermission("netdev.firmware.upgrade")).
			Post("/firmware/versions", h.registerFirmwareVersion)
		r.With(httpserver.RequirePermission("netdev.firmware.upgrade")).
			Post("/firmware/upgrade-jobs", h.scheduleUpgrade)
		r.With(httpserver.RequirePermission("netdev.firmware.upgrade")).
			Post("/firmware/upgrade-jobs/{id}/stage", h.stageUpgrade)
		r.With(httpserver.RequirePermission("netdev.firmware.upgrade")).
			Post("/firmware/upgrade-jobs/{id}/start", h.startUpgrade)
		r.With(httpserver.RequirePermission("netdev.firmware.upgrade")).
			Post("/firmware/upgrade-jobs/{id}/complete", h.completeUpgrade)
		r.With(httpserver.RequirePermission("netdev.firmware.upgrade")).
			Post("/firmware/upgrade-jobs/{id}/fail", h.failUpgrade)

		// Swaps
		r.With(httpserver.RequirePermission("netdev.swap.request")).
			Post("/swaps", h.requestSwap)
		r.With(httpserver.RequirePermission("netdev.swap.approve")).
			Post("/swaps/{id}/approve", h.approveSwap)
		r.With(httpserver.RequirePermission("netdev.swap.approve")).
			Post("/swaps/{id}/stage", h.stageSwap)
		r.With(httpserver.RequirePermission("netdev.swap.approve")).
			Post("/swaps/{id}/assign-technician", h.assignTechnician)
		r.With(httpserver.RequirePermission("netdev.swap.execute")).
			Post("/swaps/{id}/complete", h.completeSwap)

		// RMA
		r.With(httpserver.RequirePermission("netdev.rma.write")).
			Post("/rma", h.openRMA)
		r.With(httpserver.RequirePermission("netdev.rma.write")).
			Post("/rma/{id}/ship", h.shipRMA)
		r.With(httpserver.RequirePermission("netdev.rma.write")).
			Post("/rma/{id}/receive", h.receiveRMA)
		r.With(httpserver.RequirePermission("netdev.rma.close")).
			Post("/rma/{id}/close", h.closeRMA)

		// Health + Compliance
		r.With(httpserver.RequirePermission("netdev.health.read")).
			Post("/devices/{id}/health-snapshot", h.recordHealthSnapshot)
		r.With(httpserver.RequirePermission("netdev.health.read")).
			Get("/devices/{id}/health-history", h.healthHistory)
		r.With(httpserver.RequirePermission("netdev.compliance.read")).
			Post("/compliance/runs", h.triggerCompliance)
		r.With(httpserver.RequirePermission("netdev.compliance.read")).
			Get("/compliance/runs/{id}", h.getCompliance)
	})
}

// ---------------------------------------------------------------------
// Devices
// ---------------------------------------------------------------------

func (h *Handler) listDevices(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page := httpserver.ParseIntDefault(q.Get("page"), 1)
	if page < 1 {
		page = 1
	}
	pageSize := httpserver.ParseIntDefault(q.Get("page_size"), 50)
	f := port.DeviceListFilter{
		Status:     q.Get("status"),
		Kind:       q.Get("kind"),
		SerialLike: q.Get("serial_like"),
		Limit:      pageSize,
		Offset:     (page - 1) * pageSize,
	}
	if s := q.Get("customer_id"); s != "" {
		u, err := parseUUID(s, "customer")
		if err != nil {
			writeErr(w, err)
			return
		}
		f.CustomerID = &u
	}
	if s := q.Get("warehouse_id"); s != "" {
		u, err := parseUUID(s, "warehouse")
		if err != nil {
			writeErr(w, err)
			return
		}
		f.WarehouseID = &u
	}
	items, total, err := h.devices.List(r.Context(), f)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]deviceDTO, 0, len(items))
	for _, d := range items {
		out = append(out, toDeviceDTO(d))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":     out,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	})
}

func (h *Handler) registerDevice(w http.ResponseWriter, r *http.Request) {
	var req registerDeviceRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	in := port.RegisterDeviceInput{
		SerialNo:     req.SerialNo,
		MACAddr:      req.MACAddr,
		AssetTag:     req.AssetTag,
		Kind:         domain.DeviceKind(req.Kind),
		Model:        req.Model,
		Manufacturer: req.Manufacturer,
	}
	if req.WarehouseID != nil && *req.WarehouseID != "" {
		u, err := parseUUID(*req.WarehouseID, "warehouse")
		if err != nil {
			writeErr(w, err)
			return
		}
		in.WarehouseID = &u
	}
	d, err := h.devices.RegisterDevice(r.Context(), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toDeviceDTO(*d))
}

func (h *Handler) getDevice(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "device")
	if err != nil {
		writeErr(w, err)
		return
	}
	d, err := h.devices.GetDevice(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toDeviceDTO(*d))
}

func (h *Handler) allocateDevice(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "device")
	if err != nil {
		writeErr(w, err)
		return
	}
	var req allocateDeviceRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	customerID, err := parseUUID(req.CustomerID, "customer")
	if err != nil {
		writeErr(w, err)
		return
	}
	in := port.AllocateDeviceInput{DeviceID: id, CustomerID: customerID}
	if req.ServiceLocationID != "" {
		u, err := parseUUID(req.ServiceLocationID, "service_location")
		if err != nil {
			writeErr(w, err)
			return
		}
		in.ServiceLocationID = u
	}
	d, err := h.devices.AllocateToCustomer(r.Context(), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toDeviceDTO(*d))
}

func (h *Handler) commissionDevice(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "device")
	if err != nil {
		writeErr(w, err)
		return
	}
	var req commissionDeviceRequest
	_ = httpserver.DecodeJSON(r, &req) // body optional
	in := port.CommissionDeviceInput{DeviceID: id, At: time.Now().UTC()}
	if req.TechnicianUserID != "" {
		u, err := parseUUID(req.TechnicianUserID, "technician")
		if err != nil {
			writeErr(w, err)
			return
		}
		in.TechnicianUserID = u
	}
	d, err := h.devices.Commission(r.Context(), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toDeviceDTO(*d))
}

func (h *Handler) decommissionDevice(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "device")
	if err != nil {
		writeErr(w, err)
		return
	}
	by := actorUserID(r.Context())
	d, err := h.devices.Decommission(r.Context(), id, by, time.Now().UTC())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toDeviceDTO(*d))
}

// ---------------------------------------------------------------------
// Firmware
// ---------------------------------------------------------------------

func (h *Handler) registerFirmwareVersion(w http.ResponseWriter, r *http.Request) {
	var req registerFirmwareRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	v, err := h.firmware.RegisterVersion(
		r.Context(),
		domain.DeviceKind(req.Kind),
		req.Model, req.Version, req.ReleaseNotes,
		req.IsRecommended, req.IsCritical,
	)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toFirmwareVersionDTO(*v))
}

func (h *Handler) scheduleUpgrade(w http.ResponseWriter, r *http.Request) {
	var req scheduleUpgradeRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	deviceID, err := parseUUID(req.DeviceID, "device")
	if err != nil {
		writeErr(w, err)
		return
	}
	in := port.ScheduleUpgradeInput{
		DeviceID:    deviceID,
		ScheduledAt: req.ScheduledAt,
		CreatedBy:   actorUserID(r.Context()),
	}
	if req.TargetFirmwareID != nil && *req.TargetFirmwareID != "" {
		u, err := parseUUID(*req.TargetFirmwareID, "firmware_version")
		if err != nil {
			writeErr(w, err)
			return
		}
		in.TargetFirmwareID = &u
	}
	j, err := h.firmware.ScheduleUpgrade(r.Context(), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toUpgradeJobDTO(*j))
}

func (h *Handler) stageUpgrade(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "firmware_upgrade_job")
	if err != nil {
		writeErr(w, err)
		return
	}
	j, err := h.firmware.StageUpgrade(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toUpgradeJobDTO(*j))
}

func (h *Handler) startUpgrade(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "firmware_upgrade_job")
	if err != nil {
		writeErr(w, err)
		return
	}
	var req startUpgradeRequest
	_ = httpserver.DecodeJSON(r, &req)
	j, err := h.firmware.MarkUpgradeStarted(r.Context(), id, req.PreviousFirmware)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toUpgradeJobDTO(*j))
}

func (h *Handler) completeUpgrade(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "firmware_upgrade_job")
	if err != nil {
		writeErr(w, err)
		return
	}
	j, err := h.firmware.MarkUpgradeSucceeded(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toUpgradeJobDTO(*j))
}

func (h *Handler) failUpgrade(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "firmware_upgrade_job")
	if err != nil {
		writeErr(w, err)
		return
	}
	var req failUpgradeRequest
	_ = httpserver.DecodeJSON(r, &req)
	j, err := h.firmware.MarkUpgradeFailed(r.Context(), id, req.ErrorMsg)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toUpgradeJobDTO(*j))
}

// ---------------------------------------------------------------------
// Swaps
// ---------------------------------------------------------------------

func (h *Handler) requestSwap(w http.ResponseWriter, r *http.Request) {
	var req requestSwapRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	customerID, err := parseUUID(req.CustomerID, "customer")
	if err != nil {
		writeErr(w, err)
		return
	}
	faultyID, err := parseUUID(req.FaultyDeviceID, "device")
	if err != nil {
		writeErr(w, err)
		return
	}
	in := port.RequestSwapInput{
		CustomerID:     customerID,
		FaultyDeviceID: faultyID,
		Reason:         req.Reason,
		RequestedBy:    actorUserID(r.Context()),
	}
	if req.FaultEventID != nil && *req.FaultEventID != "" {
		u, err := parseUUID(*req.FaultEventID, "fault_event")
		if err != nil {
			writeErr(w, err)
			return
		}
		in.FaultEventID = &u
	}
	s, err := h.swaps.RequestSwap(r.Context(), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toSwapDTO(*s))
}

func (h *Handler) approveSwap(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "swap")
	if err != nil {
		writeErr(w, err)
		return
	}
	by := uuid.Nil
	if a := actorUserID(r.Context()); a != nil {
		by = *a
	}
	if by == uuid.Nil {
		writeErr(w, errors.Unauthorized("auth.missing", "authentication required"))
		return
	}
	s, err := h.swaps.ApproveSwap(r.Context(), id, by)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSwapDTO(*s))
}

func (h *Handler) stageSwap(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "swap")
	if err != nil {
		writeErr(w, err)
		return
	}
	var req stageSwapRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	replacementID, err := parseUUID(req.ReplacementDeviceID, "device")
	if err != nil {
		writeErr(w, err)
		return
	}
	s, err := h.swaps.StageSwap(r.Context(), id, replacementID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSwapDTO(*s))
}

func (h *Handler) assignTechnician(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "swap")
	if err != nil {
		writeErr(w, err)
		return
	}
	var req assignTechnicianRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	techID, err := parseUUID(req.TechnicianUserID, "technician")
	if err != nil {
		writeErr(w, err)
		return
	}
	s, err := h.swaps.AssignTechnician(r.Context(), id, techID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSwapDTO(*s))
}

func (h *Handler) completeSwap(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "swap")
	if err != nil {
		writeErr(w, err)
		return
	}
	s, err := h.swaps.CompleteSwap(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSwapDTO(*s))
}

// ---------------------------------------------------------------------
// RMA
// ---------------------------------------------------------------------

func (h *Handler) openRMA(w http.ResponseWriter, r *http.Request) {
	var req openRMARequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	deviceID, err := parseUUID(req.DeviceID, "device")
	if err != nil {
		writeErr(w, err)
		return
	}
	rec, err := h.rma.OpenRMA(r.Context(), port.OpenRMAInput{
		DeviceID:  deviceID,
		Vendor:    req.Vendor,
		Reason:    req.Reason,
		CreatedBy: actorUserID(r.Context()),
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toRMADTO(*rec))
}

func (h *Handler) shipRMA(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "rma")
	if err != nil {
		writeErr(w, err)
		return
	}
	var req shipRMARequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	rec, err := h.rma.MarkShipped(r.Context(), id, req.VendorRMANo, time.Now().UTC())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toRMADTO(*rec))
}

func (h *Handler) receiveRMA(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "rma")
	if err != nil {
		writeErr(w, err)
		return
	}
	var req receiveRMARequest
	_ = httpserver.DecodeJSON(r, &req)
	rec, err := h.rma.MarkReceived(r.Context(), id, req.ReplacementSerial, time.Now().UTC())
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toRMADTO(*rec))
}

func (h *Handler) closeRMA(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "rma")
	if err != nil {
		writeErr(w, err)
		return
	}
	rec, err := h.rma.CloseRMA(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toRMADTO(*rec))
}

// ---------------------------------------------------------------------
// Health + compliance
// ---------------------------------------------------------------------

func (h *Handler) recordHealthSnapshot(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "device")
	if err != nil {
		writeErr(w, err)
		return
	}
	var req recordHealthRequest
	if err := httpserver.DecodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	in := port.RecordHealthInput{
		DeviceID:      id,
		SnappedAt:     req.SnappedAt,
		UptimeSeconds: req.UptimeSeconds,
		SignalDBM:     req.SignalDBM,
		PacketLossPct: req.PacketLossPct,
		CPUPct:        req.CPUPct,
		MemoryPct:     req.MemoryPct,
	}
	if len(req.RawPayload) > 0 {
		// Pass-through opaque JSON. We marshal back into bytes so the
		// repo can store it as jsonb.
		b, err := json.Marshal(req.RawPayload)
		if err == nil {
			in.RawPayload = b
		}
	}
	snap, err := h.health.RecordSnapshot(r.Context(), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toHealthSnapshotDTO(*snap))
}

func (h *Handler) healthHistory(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "device")
	if err != nil {
		writeErr(w, err)
		return
	}
	limit := httpserver.ParseIntDefault(r.URL.Query().Get("limit"), 50)
	items, err := h.health.History(r.Context(), id, limit)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]healthSnapshotDTO, 0, len(items))
	for _, s := range items {
		out = append(out, toHealthSnapshotDTO(s))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "total": len(out)})
}

func (h *Handler) triggerCompliance(w http.ResponseWriter, r *http.Request) {
	var req triggerComplianceRequest
	_ = httpserver.DecodeJSON(r, &req)
	scope := req.Scope
	if scope == "" {
		scope = "all"
	}
	run, err := h.compliance.RunScan(r.Context(), scope)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toComplianceRunDTO(*run))
}

func (h *Handler) getCompliance(w http.ResponseWriter, r *http.Request) {
	id, err := parseUUID(chi.URLParam(r, "id"), "compliance_run")
	if err != nil {
		writeErr(w, err)
		return
	}
	run, err := h.compliance.GetRun(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toComplianceRunDTO(*run))
}

// ---------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------

func actorUserID(ctx context.Context) *uuid.UUID {
	c := httpserver.ClaimsFromContext(ctx)
	if c == nil {
		return nil
	}
	id := c.UserID
	return &id
}

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

func writeJSON(w http.ResponseWriter, status int, body any) {
	httpserver.WriteJSON(w, status, body)
}

func writeErr(w http.ResponseWriter, err error) {
	httpserver.WriteError(w, err)
}
