// Package port defines the interfaces between the usecase layer and the
// world outside it.
//
//   - "Driving" ports: things callers invoke ON the application (UseCase).
//   - "Driven"  ports: things the application invokes ON dependencies
//     (Repository, Hasher, TokenIssuer).
//
// Adapters (http, postgres) implement these. Tests substitute fakes.
package port

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/identity/domain"
)

// =====================================================================
// Driving ports
// =====================================================================

// LoginInput is the request shape for authentication.
type LoginInput struct {
	Email     string
	Password  string
	UserAgent string
	IP        string
}

// LoginOutput is what the usecase returns on a successful login.
type LoginOutput struct {
	AccessToken      string
	RefreshToken     string // plaintext — returned to client once
	RefreshExpiresAt time.Time
	User             domain.User
	Roles            []string
	Permissions      []string // resolved "module.action" keys for this user
}

// RefreshInput is the request shape for /auth/refresh.
type RefreshInput struct {
	RefreshToken string
	UserAgent    string
	IP           string
}

// LogoutInput revokes a single refresh token (the one the client holds).
type LogoutInput struct {
	RefreshToken string
}

// CreateUserInput is the request shape for provisioning a new user.
//
// SalesType and TechnicianGrade are optional and only meaningful when the
// user's role set includes sales_rep / technician — they're stored in the
// extension tables (PK=FK to users). Setting them for a user without the
// matching role is allowed; the value is just metadata until that role is
// granted later.
type CreateUserInput struct {
	EmployeeID      string
	FullName        string
	Email           string
	Phone           string
	Password        string
	BranchID        *uuid.UUID
	BranchLevel     *domain.BranchLevel
	ReportsToID     *uuid.UUID
	RoleNames       []string
	SalesType       *domain.SalesType
	TechnicianGrade *domain.TechnicianGrade
}

// UpdateUserInput patches a user's editable fields. Fields left nil are not
// touched. Password is NOT updatable here — that's a dedicated change-password
// flow with its own auth requirements.
type UpdateUserInput struct {
	ID              uuid.UUID
	EmployeeID      *string
	FullName        *string
	Phone           *string
	BranchID        *uuid.UUID
	BranchLevel     *domain.BranchLevel
	ReportsToID     *uuid.UUID
	ClearReportsTo  bool // pass true to set reports_to_id = NULL
	ClearBranch     bool // pass true to unassign branch
	SalesType       *domain.SalesType
	ClearSalesType  bool
	TechnicianGrade *domain.TechnicianGrade
	ClearTechGrade  bool
}

// MeOutput is what /auth/me returns — current user with roles and
// resolved permission keys for client-side gating.
type MeOutput struct {
	User        domain.User
	Roles       []string
	Permissions []string
}

// AssignRolesInput swaps a user's role set.
type AssignRolesInput struct {
	UserID    uuid.UUID
	RoleNames []string
}

// CreateRoleInput / UpdateRoleInput — role admin surface. Roles are
// organizational rows; permissions attached to them are managed via
// SetRolePermissions so the two operations have separate audit trails.
type CreateRoleInput struct {
	Name        string
	Description string
	CreatedBy   *uuid.UUID
}

type UpdateRoleInput struct {
	ID          uuid.UUID
	Name        string
	Description string
}

// SetRolePermissionsInput atomically replaces the role's permission set
// with the provided ids. Useful for the admin UI which lets the operator
// tick/untick permissions in a single multi-select pane.
type SetRolePermissionsInput struct {
	RoleID        uuid.UUID
	PermissionIDs []uuid.UUID
}

// UserListFilter narrows /users list queries.
type UserListFilter struct {
	Search   string // matches email or full_name (ILIKE)
	BranchID *uuid.UUID
	Role     string // role.name filter
	Active   *bool
	Limit    int
	Offset   int
}

// UserListItem is a slim projection for the users table — joined roles
// rendered as a comma-separated list at read time.
type UserListItem struct {
	User  domain.User
	Roles []string
}

// UserDetail is the full view returned by GetUserDetail — base user plus
// roles and the extension-table fields. Used by the admin edit-user dialog.
type UserDetail struct {
	User            domain.User
	Roles           []string
	SalesType       *domain.SalesType
	TechnicianGrade *domain.TechnicianGrade
}

// CreateBranchInput specifies the data needed to construct a branch.
type CreateBranchInput struct {
	Name     string
	Code     string
	Level    domain.BranchLevel
	ParentID *uuid.UUID
}

// UpdateBranchInput patches editable branch fields.
//
// Wave 68 — extended to cover the geo + per-branch operational config
// fields that previously could only be edited via direct SQL. The
// repo applies a sparse UPDATE — any pointer left nil means "don't
// touch". Clearing a field uses the *Clear booleans.
type UpdateBranchInput struct {
	ID     uuid.UUID
	Name   *string
	Active *bool

	// GeoShapeGeoJSON is the GeoJSON MultiPolygon (or Polygon —
	// auto-promoted by ST_Multi) that defines the branch's coverage
	// area. Set to "" + GeoShapeClear=true to remove the polygon.
	GeoShapeGeoJSON *string
	GeoShapeClear   *bool

	// ODPStrategy / CableDistance / WOAutoAssign — free-shape JSON
	// blobs that the branch repo persists into the matching jsonb
	// columns. The application layer reads them via type-tagged DTOs
	// in CRM + field flows.
	ODPStrategy     *string // raw JSON; "" + clear=true removes
	ODPStrategyClear *bool
	CableDistance     *string
	CableDistanceClear *bool
	WOAutoAssign     *string
	WOAutoAssignClear *bool

	// Per-branch SLA columns (PRD §9). Stored in minutes; nil leaves
	// the column unchanged. Use the *Clear booleans to NULL the column
	// (and let the inheritance chain resolve up the parent path).
	SLAAssignmentMinutes      *int
	SLAAssignmentMinutesClear *bool
	SLADispatchMinutes        *int
	SLADispatchMinutesClear   *bool
	SLAInstallMinutes         *int
	SLAInstallMinutesClear    *bool
}

// DashboardStats is the payload of /api/identity/dashboard/stats.
type DashboardStats struct {
	UsersTotal       int
	UsersActive      int
	BranchesTotal    int
	BranchesByLevel  map[string]int // {"regional":..,"area":..,"sub_area":..}
	RolesTotal       int
	PermissionsTotal int
	RecentAudits     []domain.AuditEntry
}

// UseCase is the contract the HTTP layer depends on.
// Implemented by usecase.Service.
type UseCase interface {
	// Auth
	Login(ctx context.Context, in LoginInput) (*LoginOutput, error)
	Refresh(ctx context.Context, in RefreshInput) (*LoginOutput, error)
	Logout(ctx context.Context, in LogoutInput) error
	Me(ctx context.Context, userID uuid.UUID) (*MeOutput, error)

	// User management
	CreateUser(ctx context.Context, in CreateUserInput) (*domain.User, error)
	UpdateUser(ctx context.Context, in UpdateUserInput) (*domain.User, error)
	GetUser(ctx context.Context, id uuid.UUID) (*domain.User, error)
	GetUserDetail(ctx context.Context, id uuid.UUID) (*UserDetail, error)
	ListUsers(ctx context.Context, f UserListFilter) ([]UserListItem, int, error)
	SetUserActive(ctx context.Context, id uuid.UUID, active bool) error

	// RBAC
	ListRoles(ctx context.Context) ([]domain.Role, error)
	GetRole(ctx context.Context, id uuid.UUID) (*domain.Role, error)
	CreateRole(ctx context.Context, in CreateRoleInput) (*domain.Role, error)
	UpdateRole(ctx context.Context, in UpdateRoleInput) (*domain.Role, error)
	DeleteRole(ctx context.Context, id uuid.UUID) error
	ListPermissions(ctx context.Context) ([]domain.Permission, error)
	PermissionsForRole(ctx context.Context, roleID uuid.UUID) ([]domain.Permission, error)
	SetRolePermissions(ctx context.Context, in SetRolePermissionsInput) error
	AssignRolesToUser(ctx context.Context, in AssignRolesInput) error

	// Branches
	ListBranches(ctx context.Context) ([]domain.Branch, error)
	CreateBranch(ctx context.Context, in CreateBranchInput) (*domain.Branch, error)
	UpdateBranch(ctx context.Context, in UpdateBranchInput) (*domain.Branch, error)
	// Wave 68 — config read for the branch editor.
	GetBranchConfig(ctx context.Context, id uuid.UUID) (*BranchConfigView, error)

	// Audit
	ListAuditEntries(ctx context.Context, f domain.AuditFilter) ([]domain.AuditEntry, int, error)

	// Platform config
	ListPlatformConfig(ctx context.Context) ([]domain.PlatformConfig, error)
	UpdatePlatformConfig(ctx context.Context, key, value string, updatedBy uuid.UUID) error

	// Dashboard
	GetDashboardStats(ctx context.Context) (*DashboardStats, error)

	// M5 r3 — Availability (HRIS stub)
	SetAvailability(ctx context.Context, in SetAvailabilityInput) error
	ListRoster(ctx context.Context, in RosterFilter) ([]RosterRow, error)
}

// =====================================================================
// M5 r3 — Availability
// =====================================================================

type SetAvailabilityInput struct {
	UserID    uuid.UUID
	Date      time.Time
	Status    domain.AvailabilityStatus
	Notes     string
	UpdatedBy uuid.UUID
}

type RosterFilter struct {
	Date     time.Time
	BranchID *uuid.UUID
	// Roles, when non-empty, restricts the roster to users with any of
	// these role names. Use ["technician"] for a Team Leader's view.
	Roles []string
}

type RosterRow struct {
	UserID     uuid.UUID
	FullName   string
	Email      string
	EmployeeID string
	BranchID   *uuid.UUID
	BranchCode string
	Status     domain.AvailabilityStatus
	Notes      string
	UpdatedAt  *time.Time
}

type AvailabilityRepository interface {
	Upsert(ctx context.Context, a *domain.UserAvailability) error
	ListRosterForDate(ctx context.Context, date time.Time, branchID *uuid.UUID, roleNames []string) ([]RosterRow, error)
}

// =====================================================================
// Driven ports
// =====================================================================

// UserRepository persists users. Implementations live in adapter/postgres.
type UserRepository interface {
	Create(ctx context.Context, u *domain.User, roleNames []string, salesType *domain.SalesType, techGrade *domain.TechnicianGrade) error
	Update(ctx context.Context, in UpdateUserInput) (*domain.User, error)
	FindByID(ctx context.Context, id uuid.UUID) (*domain.User, error)
	FindByEmail(ctx context.Context, email string) (*domain.User, error)
	GetSalesType(ctx context.Context, id uuid.UUID) (*domain.SalesType, error)
	GetTechnicianGrade(ctx context.Context, id uuid.UUID) (*domain.TechnicianGrade, error)
	RolesForUser(ctx context.Context, userID uuid.UUID) ([]string, error)
	PermissionsForUser(ctx context.Context, userID uuid.UUID) ([]string, error)
	ReplaceRolesForUser(ctx context.Context, userID uuid.UUID, roleNames []string) error
	List(ctx context.Context, f UserListFilter) ([]UserListItem, int, error)
	SetActive(ctx context.Context, id uuid.UUID, active bool) error
	CountActive(ctx context.Context) (int, int, error) // (active, total)
}

// BranchConfigView projects the geo + per-branch operational
// config off identity.branches into one read shape. The repo
// returns the GeoJSON as a string (already wrapped via
// ST_AsGeoJSON); the JSONB columns come back as strings too so
// the handler can pass them straight through.
type BranchConfigView struct {
	GeoShapeGeoJSON      *string
	ODPStrategy          *string
	CableDistance        *string
	WOAutoAssign         *string
	SLAAssignmentMinutes *int
	SLADispatchMinutes   *int
	SLAInstallMinutes    *int
}

// BranchRepository persists branches.
type BranchRepository interface {
	List(ctx context.Context) ([]domain.Branch, error)
	FindByID(ctx context.Context, id uuid.UUID) (*domain.Branch, error)
	Create(ctx context.Context, b *domain.Branch) error
	Update(ctx context.Context, b *domain.Branch) error
	// Wave 68 — apply geo + config patch in a single round-trip. The
	// input shape mirrors UpdateBranchInput's optional fields; pointer
	// nil = don't touch, *Clear=true = NULL the column.
	ApplyBranchPatch(ctx context.Context, in UpdateBranchInput) error
	// Wave 68 — config read for the editor.
	FindConfig(ctx context.Context, id uuid.UUID) (*BranchConfigView, error)
	CountByLevel(ctx context.Context) (map[string]int, error)
}

// AuditRepository — read-side; writes go through pkg/audit.Writer.
type AuditRepository interface {
	List(ctx context.Context, f domain.AuditFilter) ([]domain.AuditEntry, int, error)
}

// PlatformConfigRepository — CRUD-ish over the KV store.
type PlatformConfigRepository interface {
	List(ctx context.Context) ([]domain.PlatformConfig, error)
	Upsert(ctx context.Context, key, value string, updatedBy uuid.UUID) error
}

// RoleRepository — CRUD access to roles + permissions.
//
// Permissions themselves are seeded by migrations and not user-creatable —
// they correspond to handler-level permission checks, so adding a row
// here without a corresponding code change does nothing. Roles, by
// contrast, are organizational and admin-editable.
type RoleRepository interface {
	List(ctx context.Context) ([]domain.Role, error)
	FindByID(ctx context.Context, id uuid.UUID) (*domain.Role, error)
	FindByName(ctx context.Context, name string) (*domain.Role, error)
	Create(ctx context.Context, name, description string) (*domain.Role, error)
	Update(ctx context.Context, id uuid.UUID, name, description string) (*domain.Role, error)
	Delete(ctx context.Context, id uuid.UUID) error
	ListPermissions(ctx context.Context) ([]domain.Permission, error)
	PermissionsForRole(ctx context.Context, roleID uuid.UUID) ([]domain.Permission, error)
	// ReplacePermissions atomically replaces the role_permissions set
	// for the given role with the provided permission ids.
	ReplacePermissions(ctx context.Context, roleID uuid.UUID, permissionIDs []uuid.UUID) error
}

// RefreshTokenRepository — persistence for refresh grants.
type RefreshTokenRepository interface {
	// Store inserts a new refresh token row. token_hash must already be hashed.
	Store(ctx context.Context, t *domain.RefreshToken) error
	// FindActive returns the active token whose id matches AND whose hash
	// verifies against the plaintext secret. Returns NotFound otherwise.
	FindActive(ctx context.Context, id uuid.UUID, plaintextSecret string) (*domain.RefreshToken, error)
	// Revoke marks a token as revoked. Idempotent.
	Revoke(ctx context.Context, id uuid.UUID, replacedBy *uuid.UUID) error
	// RevokeAllForUser revokes every active token belonging to a user.
	// Used for "log out everywhere" and replay-attack containment.
	RevokeAllForUser(ctx context.Context, userID uuid.UUID) error
}

// PasswordHasher abstracts bcrypt (or any future hasher).
type PasswordHasher interface {
	Hash(plain string) (string, error)
	Compare(hash, plain string) error
}

// TokenIssuer signs JWT access tokens for authenticated users.
type TokenIssuer interface {
	Issue(userID uuid.UUID, email string, roles, permissions []string, branchID *uuid.UUID, branchLevel string) (string, error)
}
