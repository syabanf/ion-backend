// Package http — DTOs for the field adapter.
//
// All HTTP-layer request/response shapes for the field bounded
// context live in this file (work orders, assignments, checklist,
// resolution, BAST, teams, reschedule, OTP, SLA, ONT config).
// Conversion helpers `toXxxDTO` sit next to their target type so a
// change to the wire shape touches one file instead of three.
//
// Why DTOs are an adapter concern (not a domain concern):
//   - Domain types should stay framework-free; they shouldn't know
//     about JSON tags or HTTP versioning.
//   - The wire format can drift (rename, add field, deprecate) without
//     touching usecase or domain code.
//   - One file per bounded context keeps the surface easy to grep when
//     a contract question arises ("what does /api/field/work-orders
//     return?").
package http

import (
	"encoding/json"

	"github.com/ion-core/backend/internal/field/domain"
	"github.com/ion-core/backend/internal/field/port"
	"github.com/ion-core/backend/pkg/httpserver"
)

// =====================================================================
// Work orders
// =====================================================================

type woDTO struct {
	ID                 string  `json:"id"`
	WONumber           string  `json:"wo_number"`
	OrderID            *string `json:"order_id,omitempty"`
	CustomerID         string  `json:"customer_id"`
	WOType             string  `json:"wo_type"`
	ProductType        string  `json:"product_type"`
	MaintenanceSubtype string  `json:"maintenance_subtype,omitempty"`
	Address            string  `json:"address"`
	BranchID           *string `json:"branch_id,omitempty"`
	BranchName         string  `json:"branch_name,omitempty"`
	BranchCode         string  `json:"branch_code,omitempty"`
	Priority           string  `json:"priority"`
	Status             string  `json:"status"`
	ScheduledDate      *string `json:"scheduled_date,omitempty"`
	SLADueAt           *string `json:"sla_due_at,omitempty"`
	TeamID             *string `json:"team_id,omitempty"`
	TeamName           string  `json:"team_name,omitempty"`
	TeamLeaderID       *string `json:"team_leader_id,omitempty"`
	TeamLeaderName     string  `json:"team_leader_name,omitempty"`
	IsEmergency        bool    `json:"is_emergency"`
	IsCrossArea        bool    `json:"is_cross_area"`
	Notes              string  `json:"notes,omitempty"`
	CreatedAt          string  `json:"created_at"`

	// Wave 84 (TC-WO-011) — the customer's product + the pinned
	// service-schema version at WO creation. Both omitempty so
	// existing clients without these fields keep working.
	ProductID       *string `json:"product_id,omitempty"`
	ServiceSchemaID *string `json:"service_schema_id,omitempty"`
}

func toWODTO(d port.WODetail) woDTO {
	w := d.WO
	out := woDTO{
		ID: w.ID.String(), WONumber: w.WONumber,
		CustomerID: w.CustomerID.String(),
		WOType:     string(w.WOType), ProductType: w.ProductType,
		MaintenanceSubtype: w.MaintenanceSubtype,
		Address:            w.Address,
		Priority:           string(w.Priority),
		Status:             string(w.Status),
		BranchName:         d.BranchName,
		BranchCode:         d.BranchCode,
		TeamName:           d.TeamName,
		TeamLeaderName:     d.TeamLeaderName,
		IsEmergency:        w.IsEmergency,
		IsCrossArea:        w.IsCrossArea,
		Notes:              w.Notes,
		CreatedAt:          httpserver.FormatRFC3339(w.CreatedAt),
	}
	if w.OrderID != nil {
		s := w.OrderID.String()
		out.OrderID = &s
	}
	if w.BranchID != nil {
		s := w.BranchID.String()
		out.BranchID = &s
	}
	if w.ScheduledDate != nil {
		s := httpserver.FormatRFC3339(*w.ScheduledDate)
		out.ScheduledDate = &s
	}
	if w.SLADueAt != nil {
		s := httpserver.FormatRFC3339(*w.SLADueAt)
		out.SLADueAt = &s
	}
	if w.TeamID != nil {
		s := w.TeamID.String()
		out.TeamID = &s
	}
	if w.TeamLeaderID != nil {
		s := w.TeamLeaderID.String()
		out.TeamLeaderID = &s
	}
	if w.ProductID != nil {
		s := w.ProductID.String()
		out.ProductID = &s
	}
	if w.ServiceSchemaID != nil {
		s := w.ServiceSchemaID.String()
		out.ServiceSchemaID = &s
	}
	return out
}

type woDetailDTO struct {
	woDTO
	Assignments        []assignmentDTO        `json:"assignments,omitempty"`
	ChecklistItems     []checklistItemDTO     `json:"checklist_items,omitempty"`
	ChecklistResponses []checklistResponseDTO `json:"checklist_responses,omitempty"`
	ResolutionItems    []resolutionItemDTO    `json:"resolution_items,omitempty"`
	ActiveBAST         *bastDTO               `json:"active_bast,omitempty"`
	ChecklistMinPhotos int                    `json:"checklist_min_photos,omitempty"`
}

func toWODetailDTO(d port.WODetail) woDetailDTO {
	out := woDetailDTO{woDTO: toWODTO(d)}
	for _, a := range d.Assignments {
		out.Assignments = append(out.Assignments, assignmentDTO{
			ID:              a.Assignment.ID.String(),
			TechnicianID:    a.Assignment.TechnicianID.String(),
			TechnicianName:  a.TechnicianName,
			TechnicianEmail: a.TechnicianEmail,
			Grade:           string(a.Assignment.Grade),
			WORole:          string(a.Assignment.WORole),
			AssignedAt:      httpserver.FormatRFC3339(a.Assignment.AssignedAt),
		})
	}
	for _, it := range d.ChecklistItems {
		out.ChecklistItems = append(out.ChecklistItems, checklistItemDTO{
			ID: it.ID.String(), ItemOrder: it.ItemOrder, ItemType: string(it.ItemType),
			Label: it.Label, Required: it.Required, PhotoTag: it.PhotoTag,
			GPSRequired: it.GPSRequired, MinAccuracyMeters: it.MinAccuracyMeters,
		})
	}
	if d.ChecklistTemplate != nil {
		out.ChecklistMinPhotos = d.ChecklistTemplate.MinPhotosRequired
	}
	for _, r := range d.ChecklistResponses {
		out.ChecklistResponses = append(out.ChecklistResponses, checklistResponseDTO{
			ID: r.ID.String(), TemplateItemID: r.TemplateItemID.String(),
			ResponseText: r.ResponseText, FileURL: r.FileURL,
			GPSLat: r.GPSLat, GPSLng: r.GPSLng, GPSAccuracyM: r.GPSAccuracyM,
			SubmittedAt: httpserver.FormatRFC3339(r.SubmittedAt),
		})
	}
	for _, ri := range d.ResolutionItems {
		out.ResolutionItems = append(out.ResolutionItems, resolutionItemDTO{
			ID: ri.ID.String(), ItemOrder: ri.ItemOrder, ItemLabel: ri.ItemLabel,
			Category: string(ri.Category), Finding: ri.Finding,
			ActionTaken: ri.ActionTaken, ResolutionStatus: string(ri.ResolutionStatus),
			TimeSpentMinutes: ri.TimeSpentMinutes,
			LoggedAt:         httpserver.FormatRFC3339(ri.LoggedAt),
		})
	}
	if d.ActiveBAST != nil {
		b := d.ActiveBAST
		bd := &bastDTO{
			ID: b.ID.String(), WOID: b.WOID.String(), CustomerID: b.CustomerID.String(),
			SignOffMode:    string(b.SignOffMode),
			CustomerSigURL: b.CustomerSigURL,
			OTPUsed:        b.OTPUsed,
			SignOffAt:      httpserver.FormatRFC3339(b.SignOffAt),
			SignOffGPSLat:  b.SignOffGPSLat,
			SignOffGPSLng:  b.SignOffGPSLng,
			SubmittedAt:    httpserver.FormatRFC3339(b.SubmittedAt),
			NOCStatus:      string(b.NOCStatus),
			NOCNotes:       b.NOCNotes,
		}
		if b.NOCVerifiedAt != nil {
			s := httpserver.FormatRFC3339(*b.NOCVerifiedAt)
			bd.NOCVerifiedAt = &s
		}
		if len(b.CompiledData) > 0 {
			bd.CompiledData = json.RawMessage(b.CompiledData)
		}
		out.ActiveBAST = bd
	}
	return out
}

type createWORequest struct {
	OrderID       string  `json:"order_id"`
	ScheduledDate *string `json:"scheduled_date,omitempty"`
	Priority      string  `json:"priority,omitempty"`
	Notes         string  `json:"notes,omitempty"`
}

type routeRequest struct {
	TeamID string `json:"team_id"`
}

type statusRequest struct {
	Status string `json:"status"`
	Notes  string `json:"notes,omitempty"`
}

// =====================================================================
// Assignments
// =====================================================================

type assignmentDTO struct {
	ID              string `json:"id"`
	TechnicianID    string `json:"technician_id"`
	TechnicianName  string `json:"technician_name"`
	TechnicianEmail string `json:"technician_email,omitempty"`
	Grade           string `json:"grade"`
	WORole          string `json:"wo_role"`
	AssignedAt      string `json:"assigned_at"`
}

type assignRequest struct {
	LeadID        string  `json:"lead_id"`
	LeadGrade     string  `json:"lead_grade,omitempty"`
	ObserverID    *string `json:"observer_id,omitempty"`
	ObserverGrade *string `json:"observer_grade,omitempty"`
}

// =====================================================================
// Checklist
// =====================================================================

type checklistItemDTO struct {
	ID                string `json:"id"`
	ItemOrder         int    `json:"item_order"`
	ItemType          string `json:"item_type"`
	Label             string `json:"label"`
	Required          bool   `json:"required"`
	PhotoTag          string `json:"photo_tag,omitempty"`
	GPSRequired       bool   `json:"gps_required"`
	MinAccuracyMeters *int   `json:"min_accuracy_meters,omitempty"`
}

type checklistResponseDTO struct {
	ID             string   `json:"id"`
	TemplateItemID string   `json:"template_item_id"`
	ResponseText   string   `json:"response_text,omitempty"`
	FileURL        string   `json:"file_url,omitempty"`
	GPSLat         *float64 `json:"gps_lat,omitempty"`
	GPSLng         *float64 `json:"gps_lng,omitempty"`
	GPSAccuracyM   *float64 `json:"gps_accuracy_m,omitempty"`
	SubmittedAt    string   `json:"submitted_at"`
}

type checklistRequest struct {
	TemplateItemID string   `json:"template_item_id"`
	ResponseText   string   `json:"response_text,omitempty"`
	FileURL        string   `json:"file_url,omitempty"`
	GPSLat         *float64 `json:"gps_lat,omitempty"`
	GPSLng         *float64 `json:"gps_lng,omitempty"`
	GPSAccuracyM   *float64 `json:"gps_accuracy_m,omitempty"`
}

// =====================================================================
// Resolution
// =====================================================================

type resolutionItemDTO struct {
	ID               string `json:"id"`
	ItemOrder        int    `json:"item_order"`
	ItemLabel        string `json:"item_label"`
	Category         string `json:"category"`
	Finding          string `json:"finding,omitempty"`
	ActionTaken      string `json:"action_taken,omitempty"`
	ResolutionStatus string `json:"resolution_status"`
	TimeSpentMinutes *int   `json:"time_spent_minutes,omitempty"`
	LoggedAt         string `json:"logged_at"`
}

type resolutionRequest struct {
	ItemLabel        string `json:"item_label"`
	Category         string `json:"category"`
	Finding          string `json:"finding,omitempty"`
	ActionTaken      string `json:"action_taken,omitempty"`
	ResolutionStatus string `json:"resolution_status"`
	TimeSpentMinutes *int   `json:"time_spent_minutes,omitempty"`
}

// =====================================================================
// BAST
// =====================================================================

type bastDTO struct {
	ID               string          `json:"id"`
	WOID             string          `json:"wo_id"`
	CustomerID       string          `json:"customer_id"`
	CompiledData     json.RawMessage `json:"compiled_data,omitempty"`
	SignOffMode      string          `json:"sign_off_mode"`
	CustomerSigURL   string          `json:"customer_sig_url,omitempty"`
	OTPUsed          bool            `json:"otp_used"`
	OTPVerifiedAt    *string         `json:"otp_verified_at,omitempty"`
	OTPPlaintextOnce string          `json:"otp_plaintext_once,omitempty"`
	SignOffAt        string          `json:"sign_off_at"`
	SignOffGPSLat    *float64        `json:"sign_off_gps_lat,omitempty"`
	SignOffGPSLng    *float64        `json:"sign_off_gps_lng,omitempty"`
	SubmittedAt      string          `json:"submitted_at"`
	NOCStatus        string          `json:"noc_status"`
	NOCVerifiedAt    *string         `json:"noc_verified_at,omitempty"`
	NOCNotes         string          `json:"noc_notes,omitempty"`
}

func toBASTDTO(b *domain.BAST) bastDTO {
	bd := bastDTO{
		ID:               b.ID.String(),
		WOID:             b.WOID.String(),
		CustomerID:       b.CustomerID.String(),
		SignOffMode:      string(b.SignOffMode),
		CustomerSigURL:   b.CustomerSigURL,
		OTPUsed:          b.OTPUsed,
		OTPPlaintextOnce: b.OTPPlaintextOnce,
		SignOffAt:        httpserver.FormatRFC3339(b.SignOffAt),
		SignOffGPSLat:    b.SignOffGPSLat,
		SignOffGPSLng:    b.SignOffGPSLng,
		SubmittedAt:      httpserver.FormatRFC3339(b.SubmittedAt),
		NOCStatus:        string(b.NOCStatus),
		NOCNotes:         b.NOCNotes,
	}
	if b.OTPVerifiedAt != nil {
		s := httpserver.FormatRFC3339(*b.OTPVerifiedAt)
		bd.OTPVerifiedAt = &s
	}
	if b.NOCVerifiedAt != nil {
		s := httpserver.FormatRFC3339(*b.NOCVerifiedAt)
		bd.NOCVerifiedAt = &s
	}
	if len(b.CompiledData) > 0 {
		bd.CompiledData = json.RawMessage(b.CompiledData)
	}
	return bd
}

type bastRequest struct {
	SignOffMode    string   `json:"sign_off_mode"`
	CustomerSigURL string   `json:"customer_sig_url,omitempty"`
	OTPUsed        bool     `json:"otp_used,omitempty"`
	GPSLat         *float64 `json:"gps_lat,omitempty"`
	GPSLng         *float64 `json:"gps_lng,omitempty"`
}

type verifyBASTRequest struct {
	Decision string `json:"decision"`
	Notes    string `json:"notes,omitempty"`
}

type verifyOTPRequest struct {
	Code string `json:"code"`
}

// =====================================================================
// Teams
// =====================================================================

type teamDTO struct {
	ID             string  `json:"id"`
	Code           string  `json:"code"`
	Name           string  `json:"name"`
	BranchID       string  `json:"branch_id"`
	BranchName     string  `json:"branch_name,omitempty"`
	BranchCode     string  `json:"branch_code,omitempty"`
	TeamLeaderID   *string `json:"team_leader_id,omitempty"`
	TeamLeaderName string  `json:"team_leader_name,omitempty"`
	MemberCount    int     `json:"member_count"`
	Active         bool    `json:"active"`
}

func toTeamDTO(v port.TeamView) teamDTO {
	d := teamDTO{
		ID:             v.Team.ID.String(),
		Code:           v.Team.Code,
		Name:           v.Team.Name,
		BranchID:       v.Team.BranchID.String(),
		BranchName:     v.BranchName,
		BranchCode:     v.BranchCode,
		TeamLeaderName: v.TeamLeaderName,
		MemberCount:    v.MemberCount,
		Active:         v.Team.Active,
	}
	if v.Team.TeamLeaderID != nil {
		s := v.Team.TeamLeaderID.String()
		d.TeamLeaderID = &s
	}
	return d
}

type teamMemberDTO struct {
	ID     string `json:"id"`
	UserID string `json:"user_id"`
	Name   string `json:"name,omitempty"`
	Email  string `json:"email,omitempty"`
	Grade  string `json:"grade"`
	Active bool   `json:"active"`
}

func toTeamMemberDTO(v port.TeamMemberView) teamMemberDTO {
	return teamMemberDTO{
		ID: v.Member.ID.String(), UserID: v.Member.UserID.String(),
		Name: v.Name, Email: v.Email, Grade: string(v.Member.Grade), Active: v.Member.Active,
	}
}

type createTeamRequest struct {
	Code         string  `json:"code"`
	Name         string  `json:"name"`
	BranchID     string  `json:"branch_id"`
	TeamLeaderID *string `json:"team_leader_id,omitempty"`
}

type addMemberRequest struct {
	UserID string `json:"user_id"`
	Grade  string `json:"grade"`
}

// =====================================================================
// Reschedule
// =====================================================================

type rescheduleDTO struct {
	ID            string  `json:"id"`
	WOID          string  `json:"wo_id"`
	Reason        string  `json:"reason"`
	Notes         string  `json:"notes,omitempty"`
	OriginalDate  *string `json:"original_date,omitempty"`
	NewDate       *string `json:"new_date,omitempty"`
	RescheduledBy *string `json:"rescheduled_by,omitempty"`
	CreatedAt     string  `json:"created_at"`
}

func toRescheduleDTO(r domain.Reschedule) rescheduleDTO {
	d := rescheduleDTO{
		ID:        r.ID.String(),
		WOID:      r.WOID.String(),
		Reason:    string(r.Reason),
		Notes:     r.Notes,
		CreatedAt: httpserver.FormatRFC3339(r.CreatedAt),
	}
	if r.OriginalDate != nil {
		s := httpserver.FormatRFC3339(*r.OriginalDate)
		d.OriginalDate = &s
	}
	if r.NewDate != nil {
		s := httpserver.FormatRFC3339(*r.NewDate)
		d.NewDate = &s
	}
	if r.RescheduledBy != nil {
		s := r.RescheduledBy.String()
		d.RescheduledBy = &s
	}
	return d
}

type rescheduleRequest struct {
	Reason  string `json:"reason"`
	Notes   string `json:"notes,omitempty"`
	NewDate string `json:"new_date"` // RFC3339
}

// =====================================================================
// ONT config (M5 r3)
// =====================================================================

type ontConfigDTO struct {
	Username           string `json:"username"`
	BandwidthProfileID string `json:"bandwidth_profile_id,omitempty"`
	VLANID             *int   `json:"vlan_id,omitempty"`
	IPAddress          string `json:"ip_address,omitempty"`
	Status             string `json:"status"`
}
