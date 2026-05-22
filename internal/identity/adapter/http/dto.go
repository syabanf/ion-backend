// Package http — DTOs for the identity adapter.
//
// All HTTP-layer request/response shapes for identity live in this
// file (auth, users, roles, permissions, branches, audit, platform
// config, availability roster). Conversion helpers `toXxxDTO` sit
// next to their target type so a change to the wire shape touches
// one file instead of three.
//
// Why DTOs are an adapter concern (not a domain concern):
//   - Domain types should stay framework-free; they shouldn't know
//     about JSON tags or HTTP versioning.
//   - The wire format can drift (rename, add field, deprecate) without
//     touching usecase or domain code.
//   - One file per bounded context keeps the surface easy to grep when
//     a contract question arises ("what does /api/identity/users
//     return?").
package http

import (
	"time"

	"github.com/ion-core/backend/internal/identity/domain"
	"github.com/ion-core/backend/internal/identity/port"
)

// =====================================================================
// Auth
// =====================================================================

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type tokenPairResponse struct {
	AccessToken      string   `json:"access_token"`
	RefreshToken     string   `json:"refresh_token"`
	RefreshExpiresAt string   `json:"refresh_expires_at"`
	User             userDTO  `json:"user"`
	Roles            []string `json:"roles"`
	Permissions      []string `json:"permissions"`
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

type logoutRequest struct {
	RefreshToken string `json:"refresh_token"`
}

type meResponse struct {
	User        userDTO  `json:"user"`
	Roles       []string `json:"roles"`
	Permissions []string `json:"permissions"`
}

// =====================================================================
// Users
// =====================================================================

type userDTO struct {
	ID          string  `json:"id"`
	EmployeeID  string  `json:"employee_id"`
	FullName    string  `json:"full_name"`
	Email       string  `json:"email"`
	Phone       string  `json:"phone"`
	BranchID    *string `json:"branch_id,omitempty"`
	BranchLevel *string `json:"branch_level,omitempty"`
	Active      bool    `json:"active"`
}

func toUserDTO(u domain.User) userDTO {
	var (
		branchID    *string
		branchLevel *string
	)
	if u.BranchID != nil {
		s := u.BranchID.String()
		branchID = &s
	}
	if u.BranchLevel != nil {
		s := string(*u.BranchLevel)
		branchLevel = &s
	}
	return userDTO{
		ID:          u.ID.String(),
		EmployeeID:  u.EmployeeID,
		FullName:    u.FullName,
		Email:       u.Email,
		Phone:       u.Phone,
		BranchID:    branchID,
		BranchLevel: branchLevel,
		Active:      u.Active,
	}
}

type createUserRequest struct {
	EmployeeID      string   `json:"employee_id"`
	FullName        string   `json:"full_name"`
	Email           string   `json:"email"`
	Phone           string   `json:"phone"`
	Password        string   `json:"password"`
	BranchID        *string  `json:"branch_id,omitempty"`
	BranchLevel     *string  `json:"branch_level,omitempty"`
	ReportsToID     *string  `json:"reports_to_id,omitempty"`
	Roles           []string `json:"roles"`
	SalesType       *string  `json:"sales_type,omitempty"`
	TechnicianGrade *string  `json:"technician_grade,omitempty"`
}

// updateUserRequest — partial update. Nil = leave alone. Use the `clear_*`
// booleans to explicitly NULL out a nullable field (branch, reports_to,
// sales_type, technician_grade). This shape avoids the "can't tell missing
// from null" ambiguity in JSON PATCH semantics without dragging in JSON-Patch
// machinery.
type updateUserRequest struct {
	EmployeeID     *string `json:"employee_id,omitempty"`
	FullName       *string `json:"full_name,omitempty"`
	Phone          *string `json:"phone,omitempty"`
	BranchID       *string `json:"branch_id,omitempty"`
	BranchLevel    *string `json:"branch_level,omitempty"`
	ClearBranch    bool    `json:"clear_branch,omitempty"`
	ReportsToID    *string `json:"reports_to_id,omitempty"`
	ClearReportsTo bool    `json:"clear_reports_to,omitempty"`
	SalesType       *string `json:"sales_type,omitempty"`
	ClearSalesType  bool    `json:"clear_sales_type,omitempty"`
	TechnicianGrade *string `json:"technician_grade,omitempty"`
	ClearTechGrade  bool    `json:"clear_technician_grade,omitempty"`
}

type userDetailResponse struct {
	User            userDTO  `json:"user"`
	Roles           []string `json:"roles"`
	SalesType       *string  `json:"sales_type,omitempty"`
	TechnicianGrade *string  `json:"technician_grade,omitempty"`
}

type assignRolesRequest struct {
	Roles []string `json:"roles"`
}

type userListItemDTO struct {
	User  userDTO  `json:"user"`
	Roles []string `json:"roles"`
}

type paginatedUsers struct {
	Items []userListItemDTO `json:"items"`
	Total int               `json:"total"`
	Page  int               `json:"page"`
	Size  int               `json:"page_size"`
}

// =====================================================================
// Roles + Permissions
// =====================================================================

type roleDTO struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type createRoleRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type setRolePermissionsRequest struct {
	PermissionIDs []string `json:"permission_ids"`
}

type permissionDTO struct {
	ID          string `json:"id"`
	Module      string `json:"module"`
	Action      string `json:"action"`
	Key         string `json:"key"`
	Description string `json:"description"`
}

func toPermissionDTOs(perms []domain.Permission) []permissionDTO {
	out := make([]permissionDTO, 0, len(perms))
	for _, p := range perms {
		out = append(out, permissionDTO{
			ID:          p.ID.String(),
			Module:      p.Module,
			Action:      p.Action,
			Key:         p.Key(),
			Description: p.Description,
		})
	}
	return out
}

// =====================================================================
// Dashboard
// =====================================================================

type dashboardStatsResponse struct {
	UsersTotal       int             `json:"users_total"`
	UsersActive      int             `json:"users_active"`
	BranchesTotal    int             `json:"branches_total"`
	BranchesByLevel  map[string]int  `json:"branches_by_level"`
	RolesTotal       int             `json:"roles_total"`
	PermissionsTotal int             `json:"permissions_total"`
	RecentAudits     []auditEntryDTO `json:"recent_audits"`
}

// =====================================================================
// Branches
// =====================================================================

type branchDTO struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Code      string  `json:"code"`
	Level     string  `json:"level"`
	ParentID  *string `json:"parent_id,omitempty"`
	Active    bool    `json:"active"`
	CreatedAt string  `json:"created_at"`
}

func toBranchDTO(b domain.Branch) branchDTO {
	var parentID *string
	if b.ParentID != nil {
		s := b.ParentID.String()
		parentID = &s
	}
	return branchDTO{
		ID:        b.ID.String(),
		Name:      b.Name,
		Code:      b.Code,
		Level:     string(b.Level),
		ParentID:  parentID,
		Active:    b.Active,
		CreatedAt: b.CreatedAt.UTC().Format(time.RFC3339),
	}
}

type createBranchRequest struct {
	Name     string  `json:"name"`
	Code     string  `json:"code"`
	Level    string  `json:"level"`
	ParentID *string `json:"parent_id,omitempty"`
}

// Wave 68 — updateBranchRequest grows to cover the geo + per-branch
// operational config fields. Each *Clear pair lets the caller
// explicitly NULL a column (useful for the SLA inheritance chain).
type updateBranchRequest struct {
	Name   *string `json:"name,omitempty"`
	Active *bool   `json:"active,omitempty"`

	GeoShapeGeoJSON *string `json:"geo_shape_geojson,omitempty"`
	GeoShapeClear   *bool   `json:"geo_shape_clear,omitempty"`

	ODPStrategy      *string `json:"odp_strategy,omitempty"`
	ODPStrategyClear *bool   `json:"odp_strategy_clear,omitempty"`

	CableDistance      *string `json:"cable_distance,omitempty"`
	CableDistanceClear *bool   `json:"cable_distance_clear,omitempty"`

	WOAutoAssign      *string `json:"wo_auto_assign,omitempty"`
	WOAutoAssignClear *bool   `json:"wo_auto_assign_clear,omitempty"`

	SLAAssignmentMinutes      *int  `json:"sla_assignment_minutes,omitempty"`
	SLAAssignmentMinutesClear *bool `json:"sla_assignment_minutes_clear,omitempty"`
	SLADispatchMinutes        *int  `json:"sla_dispatch_minutes,omitempty"`
	SLADispatchMinutesClear   *bool `json:"sla_dispatch_minutes_clear,omitempty"`
	SLAInstallMinutes         *int  `json:"sla_install_minutes,omitempty"`
	SLAInstallMinutesClear    *bool `json:"sla_install_minutes_clear,omitempty"`
}

// =====================================================================
// Audit
// =====================================================================

type auditEntryDTO struct {
	ID           string  `json:"id"`
	Timestamp    string  `json:"timestamp"`
	UserID       *string `json:"user_id,omitempty"`
	UserFullName string  `json:"user_full_name"`
	Module       string  `json:"module"`
	RecordType   string  `json:"record_type"`
	RecordID     string  `json:"record_id"`
	FieldChanged string  `json:"field_changed,omitempty"`
	Before       string  `json:"before,omitempty"`
	After        string  `json:"after,omitempty"`
	Description  string  `json:"description,omitempty"`
	Reason       string  `json:"reason,omitempty"`
}

func toAuditEntryDTOs(es []domain.AuditEntry) []auditEntryDTO {
	out := make([]auditEntryDTO, 0, len(es))
	for _, e := range es {
		var uid *string
		if e.UserID != nil {
			s := e.UserID.String()
			uid = &s
		}
		out = append(out, auditEntryDTO{
			ID:           e.ID.String(),
			Timestamp:    e.Timestamp.UTC().Format(time.RFC3339),
			UserID:       uid,
			UserFullName: e.UserFullName,
			Module:       e.Module,
			RecordType:   e.RecordType,
			RecordID:     e.RecordID,
			FieldChanged: e.FieldChanged,
			Before:       e.Before,
			After:        e.After,
			Description:  e.Description,
			Reason:       e.Reason,
		})
	}
	return out
}

type paginatedAudit struct {
	Items []auditEntryDTO `json:"items"`
	Total int             `json:"total"`
	Page  int             `json:"page"`
	Size  int             `json:"page_size"`
}

// =====================================================================
// Platform Config
// =====================================================================

type configDTO struct {
	Key       string  `json:"key"`
	Value     string  `json:"value"`
	UpdatedBy *string `json:"updated_by,omitempty"`
	UpdatedAt string  `json:"updated_at"`
}

type updateConfigRequest struct {
	Value string `json:"value"`
}

// =====================================================================
// Availability roster (M5 r3)
// =====================================================================

type rosterRowDTO struct {
	UserID     string  `json:"user_id"`
	FullName   string  `json:"full_name"`
	Email      string  `json:"email"`
	EmployeeID string  `json:"employee_id"`
	BranchID   *string `json:"branch_id,omitempty"`
	BranchCode string  `json:"branch_code,omitempty"`
	Status     string  `json:"status"`
	Notes      string  `json:"notes,omitempty"`
	UpdatedAt  *string `json:"updated_at,omitempty"`
}

func toRosterRowDTO(r port.RosterRow) rosterRowDTO {
	d := rosterRowDTO{
		UserID:     r.UserID.String(),
		FullName:   r.FullName,
		Email:      r.Email,
		EmployeeID: r.EmployeeID,
		BranchCode: r.BranchCode,
		Status:     string(r.Status),
		Notes:      r.Notes,
	}
	if r.BranchID != nil {
		s := r.BranchID.String()
		d.BranchID = &s
	}
	if r.UpdatedAt != nil {
		s := r.UpdatedAt.UTC().Format(time.RFC3339)
		d.UpdatedAt = &s
	}
	return d
}

type setAvailabilityRequest struct {
	Date   string `json:"date"`   // YYYY-MM-DD; required
	Status string `json:"status"` // available|leave|sick|training|off
	Notes  string `json:"notes,omitempty"`
}
