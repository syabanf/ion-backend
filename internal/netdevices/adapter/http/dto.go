package http

import (
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/netdevices/domain"
)

// =====================================================================
// DTO helpers
// =====================================================================

func rfc3339Ptr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}

func uuidPtrString(u *uuid.UUID) *string {
	if u == nil {
		return nil
	}
	s := u.String()
	return &s
}

// =====================================================================
// Device
// =====================================================================

type deviceDTO struct {
	ID                string  `json:"id"`
	SerialNo          string  `json:"serial_no"`
	MACAddr           string  `json:"mac_addr,omitempty"`
	AssetTag          string  `json:"asset_tag,omitempty"`
	Kind              string  `json:"kind"`
	Model             string  `json:"model,omitempty"`
	Manufacturer      string  `json:"manufacturer,omitempty"`
	FirmwareVersion   string  `json:"firmware_version,omitempty"`
	Status            string  `json:"status"`
	WarehouseID       *string `json:"warehouse_id,omitempty"`
	CustomerID        *string `json:"customer_id,omitempty"`
	ServiceLocationID *string `json:"service_location_id,omitempty"`
	IPAddress         string  `json:"ip_address,omitempty"`
	MgmtURI           string  `json:"mgmt_uri,omitempty"`
	LastSeenAt        *string `json:"last_seen_at,omitempty"`
	CommissionedAt    *string `json:"commissioned_at,omitempty"`
	DecommissionedAt  *string `json:"decommissioned_at,omitempty"`
	Notes             string  `json:"notes,omitempty"`
	CreatedAt         string  `json:"created_at"`
	UpdatedAt         string  `json:"updated_at"`
}

func toDeviceDTO(d domain.Device) deviceDTO {
	return deviceDTO{
		ID:                d.ID.String(),
		SerialNo:          d.SerialNo,
		MACAddr:           d.MACAddr,
		AssetTag:          d.AssetTag,
		Kind:              string(d.Kind),
		Model:             d.Model,
		Manufacturer:      d.Manufacturer,
		FirmwareVersion:   d.FirmwareVersion,
		Status:            string(d.Status),
		WarehouseID:       uuidPtrString(d.WarehouseID),
		CustomerID:        uuidPtrString(d.CustomerID),
		ServiceLocationID: uuidPtrString(d.ServiceLocation),
		IPAddress:         d.IPAddress,
		MgmtURI:           d.MgmtURI,
		LastSeenAt:        rfc3339Ptr(d.LastSeenAt),
		CommissionedAt:    rfc3339Ptr(d.CommissionedAt),
		DecommissionedAt:  rfc3339Ptr(d.DecommissionedAt),
		Notes:             d.Notes,
		CreatedAt:         d.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:         d.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// =====================================================================
// Firmware
// =====================================================================

type firmwareVersionDTO struct {
	ID             string  `json:"id"`
	Kind           string  `json:"kind"`
	Model          string  `json:"model"`
	Version        string  `json:"version"`
	ReleaseNotes   string  `json:"release_notes,omitempty"`
	IsRecommended  bool    `json:"is_recommended"`
	IsCritical     bool    `json:"is_critical"`
	ReleasedAt     *string `json:"released_at,omitempty"`
	SupportedUntil *string `json:"supported_until,omitempty"`
	CreatedAt      string  `json:"created_at"`
}

func toFirmwareVersionDTO(v domain.FirmwareVersion) firmwareVersionDTO {
	return firmwareVersionDTO{
		ID:             v.ID.String(),
		Kind:           string(v.Kind),
		Model:          v.Model,
		Version:        v.Version,
		ReleaseNotes:   v.ReleaseNotes,
		IsRecommended:  v.IsRecommended,
		IsCritical:     v.IsCritical,
		ReleasedAt:     rfc3339Ptr(v.ReleasedAt),
		SupportedUntil: rfc3339Ptr(v.SupportedUntil),
		CreatedAt:      v.CreatedAt.UTC().Format(time.RFC3339),
	}
}

type upgradeJobDTO struct {
	ID               string  `json:"id"`
	DeviceID         string  `json:"device_id"`
	TargetFirmwareID *string `json:"target_firmware_id,omitempty"`
	ScheduledAt      *string `json:"scheduled_at,omitempty"`
	StartedAt        *string `json:"started_at,omitempty"`
	CompletedAt      *string `json:"completed_at,omitempty"`
	Status           string  `json:"status"`
	RetryCount       int     `json:"retry_count"`
	MaxRetries       int     `json:"max_retries"`
	ErrorMsg         string  `json:"error_msg,omitempty"`
	PreviousFirmware string  `json:"previous_firmware,omitempty"`
	CreatedAt        string  `json:"created_at"`
	UpdatedAt        string  `json:"updated_at"`
}

func toUpgradeJobDTO(j domain.FirmwareUpgradeJob) upgradeJobDTO {
	return upgradeJobDTO{
		ID:               j.ID.String(),
		DeviceID:         j.DeviceID.String(),
		TargetFirmwareID: uuidPtrString(j.TargetFirmwareID),
		ScheduledAt:      rfc3339Ptr(j.ScheduledAt),
		StartedAt:        rfc3339Ptr(j.StartedAt),
		CompletedAt:      rfc3339Ptr(j.CompletedAt),
		Status:           string(j.Status),
		RetryCount:       j.RetryCount,
		MaxRetries:       j.MaxRetries,
		ErrorMsg:         j.ErrorMsg,
		PreviousFirmware: j.PreviousFirmware,
		CreatedAt:        j.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:        j.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// =====================================================================
// Swap
// =====================================================================

type swapDTO struct {
	ID                  string  `json:"id"`
	CustomerID          string  `json:"customer_id"`
	FaultyDeviceID      string  `json:"faulty_device_id"`
	ReplacementDeviceID *string `json:"replacement_device_id,omitempty"`
	Reason              string  `json:"reason,omitempty"`
	FaultEventID        *string `json:"fault_event_id,omitempty"`
	Status              string  `json:"status"`
	WOID                *string `json:"wo_id,omitempty"`
	TechnicianUserID    *string `json:"technician_user_id,omitempty"`
	SwapStartedAt       *string `json:"swap_started_at,omitempty"`
	SwapCompletedAt     *string `json:"swap_completed_at,omitempty"`
	RetrofitID          *string `json:"retrofit_id,omitempty"`
	RequestedBy         *string `json:"requested_by,omitempty"`
	ApprovedBy          *string `json:"approved_by,omitempty"`
	CreatedAt           string  `json:"created_at"`
	UpdatedAt           string  `json:"updated_at"`
}

func toSwapDTO(s domain.DeviceSwap) swapDTO {
	return swapDTO{
		ID:                  s.ID.String(),
		CustomerID:          s.CustomerID.String(),
		FaultyDeviceID:      s.FaultyDeviceID.String(),
		ReplacementDeviceID: uuidPtrString(s.ReplacementDeviceID),
		Reason:              s.Reason,
		FaultEventID:        uuidPtrString(s.FaultEventID),
		Status:              string(s.Status),
		WOID:                uuidPtrString(s.WOID),
		TechnicianUserID:    uuidPtrString(s.TechnicianUserID),
		SwapStartedAt:       rfc3339Ptr(s.SwapStartedAt),
		SwapCompletedAt:     rfc3339Ptr(s.SwapCompletedAt),
		RetrofitID:          uuidPtrString(s.RetrofitID),
		RequestedBy:         uuidPtrString(s.RequestedBy),
		ApprovedBy:          uuidPtrString(s.ApprovedBy),
		CreatedAt:           s.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:           s.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// =====================================================================
// RMA
// =====================================================================

type rmaDTO struct {
	ID                string  `json:"id"`
	DeviceID          string  `json:"device_id"`
	Vendor            string  `json:"vendor,omitempty"`
	VendorRMANo       string  `json:"vendor_rma_no,omitempty"`
	ReturnReason      string  `json:"return_reason,omitempty"`
	ShippedAt         *string `json:"shipped_at,omitempty"`
	ReceivedAt        *string `json:"received_at,omitempty"`
	ReplacementSerial string  `json:"replacement_serial,omitempty"`
	Status            string  `json:"status"`
	Notes             string  `json:"notes,omitempty"`
	CreatedAt         string  `json:"created_at"`
	UpdatedAt         string  `json:"updated_at"`
}

func toRMADTO(r domain.RMARecord) rmaDTO {
	return rmaDTO{
		ID:                r.ID.String(),
		DeviceID:          r.DeviceID.String(),
		Vendor:            r.Vendor,
		VendorRMANo:       r.VendorRMANo,
		ReturnReason:      r.ReturnReason,
		ShippedAt:         rfc3339Ptr(r.ShippedAt),
		ReceivedAt:        rfc3339Ptr(r.ReceivedAt),
		ReplacementSerial: r.ReplacementSerial,
		Status:            string(r.Status),
		Notes:             r.Notes,
		CreatedAt:         r.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:         r.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// =====================================================================
// Health snapshot
// =====================================================================

type healthSnapshotDTO struct {
	ID            string   `json:"id"`
	DeviceID      string   `json:"device_id"`
	SnappedAt     string   `json:"snapped_at"`
	UptimeSeconds *int64   `json:"uptime_seconds,omitempty"`
	SignalDBM     *float64 `json:"signal_dbm,omitempty"`
	PacketLossPct *float64 `json:"packet_loss_pct,omitempty"`
	CPUPct        *float64 `json:"cpu_pct,omitempty"`
	MemoryPct     *float64 `json:"memory_pct,omitempty"`
	Score         int      `json:"score"`
}

func toHealthSnapshotDTO(s domain.HealthSnapshot) healthSnapshotDTO {
	return healthSnapshotDTO{
		ID:            s.ID.String(),
		DeviceID:      s.DeviceID.String(),
		SnappedAt:     s.SnappedAt.UTC().Format(time.RFC3339),
		UptimeSeconds: s.UptimeSeconds,
		SignalDBM:     s.SignalDBM,
		PacketLossPct: s.PacketLossPct,
		CPUPct:        s.CPUPct,
		MemoryPct:     s.MemoryPct,
		Score:         domain.ComputeHealthScore(s),
	}
}

// =====================================================================
// Compliance
// =====================================================================

type complianceRunDTO struct {
	ID              string  `json:"id"`
	StartedAt       string  `json:"started_at"`
	FinishedAt      *string `json:"finished_at,omitempty"`
	Scope           string  `json:"scope"`
	TotalDevices    int     `json:"total_devices"`
	Compliant       int     `json:"compliant"`
	NonCompliant    int     `json:"non_compliant"`
	CriticalPending int     `json:"critical_pending"`
	Report          []byte  `json:"report,omitempty"`
}

func toComplianceRunDTO(r domain.FirmwareComplianceRun) complianceRunDTO {
	return complianceRunDTO{
		ID:              r.ID.String(),
		StartedAt:       r.StartedAt.UTC().Format(time.RFC3339),
		FinishedAt:      rfc3339Ptr(r.FinishedAt),
		Scope:           r.Scope,
		TotalDevices:    r.TotalDevices,
		Compliant:       r.Compliant,
		NonCompliant:    r.NonCompliant,
		CriticalPending: r.CriticalPending,
		Report:          r.ReportPayload,
	}
}

// =====================================================================
// Request bodies
// =====================================================================

type registerDeviceRequest struct {
	SerialNo     string  `json:"serial_no"`
	MACAddr      string  `json:"mac_addr"`
	AssetTag     string  `json:"asset_tag"`
	Kind         string  `json:"kind"`
	Model        string  `json:"model"`
	Manufacturer string  `json:"manufacturer"`
	WarehouseID  *string `json:"warehouse_id,omitempty"`
}

type allocateDeviceRequest struct {
	CustomerID        string `json:"customer_id"`
	ServiceLocationID string `json:"service_location_id"`
}

type commissionDeviceRequest struct {
	TechnicianUserID string `json:"technician_user_id"`
}

type registerFirmwareRequest struct {
	Kind          string `json:"kind"`
	Model         string `json:"model"`
	Version       string `json:"version"`
	ReleaseNotes  string `json:"release_notes"`
	IsRecommended bool   `json:"is_recommended"`
	IsCritical    bool   `json:"is_critical"`
}

type scheduleUpgradeRequest struct {
	DeviceID         string    `json:"device_id"`
	TargetFirmwareID *string   `json:"target_firmware_id,omitempty"`
	ScheduledAt      time.Time `json:"scheduled_at"`
}

type startUpgradeRequest struct {
	PreviousFirmware string `json:"previous_firmware"`
}

type failUpgradeRequest struct {
	ErrorMsg string `json:"error_msg"`
}

type requestSwapRequest struct {
	CustomerID     string  `json:"customer_id"`
	FaultyDeviceID string  `json:"faulty_device_id"`
	Reason         string  `json:"reason"`
	FaultEventID   *string `json:"fault_event_id,omitempty"`
}

type stageSwapRequest struct {
	ReplacementDeviceID string `json:"replacement_device_id"`
}

type assignTechnicianRequest struct {
	TechnicianUserID string `json:"technician_user_id"`
}

type openRMARequest struct {
	DeviceID string `json:"device_id"`
	Vendor   string `json:"vendor"`
	Reason   string `json:"reason"`
}

type shipRMARequest struct {
	VendorRMANo string `json:"vendor_rma_no"`
}

type receiveRMARequest struct {
	ReplacementSerial string `json:"replacement_serial"`
}

type recordHealthRequest struct {
	SnappedAt     time.Time      `json:"snapped_at"`
	UptimeSeconds *int64         `json:"uptime_seconds,omitempty"`
	SignalDBM     *float64       `json:"signal_dbm,omitempty"`
	PacketLossPct *float64       `json:"packet_loss_pct,omitempty"`
	CPUPct        *float64       `json:"cpu_pct,omitempty"`
	MemoryPct     *float64       `json:"memory_pct,omitempty"`
	RawPayload    map[string]any `json:"raw_payload,omitempty"`
}

type triggerComplianceRequest struct {
	Scope string `json:"scope"`
}
