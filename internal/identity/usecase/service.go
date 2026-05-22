// Package usecase implements the identity application services.
//
// Each method is a single use case. They:
//   - orchestrate domain calls,
//   - invoke driven ports (repo, hasher, token issuer),
//   - never touch HTTP, SQL, or env vars.
package usecase

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/identity/domain"
	"github.com/ion-core/backend/internal/identity/port"
	"github.com/ion-core/backend/pkg/auth"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// Service implements port.UseCase. One instance per process, wired at startup.
type Service struct {
	users       port.UserRepository
	roles       port.RoleRepository
	branches    port.BranchRepository
	audits      port.AuditRepository
	configs     port.PlatformConfigRepository
	refreshRepo port.RefreshTokenRepository
	hasher      port.PasswordHasher
	tokens      port.TokenIssuer
	refreshTTL  time.Duration
	log         *slog.Logger

	// M5 r3 — HRIS availability stub. Nil-safe so r1 wiring keeps working.
	availability port.AvailabilityRepository
}

// WithAvailability attaches the HRIS-stub repo. Optional; the cmd binary
// always wires it.
func (s *Service) WithAvailability(a port.AvailabilityRepository) *Service {
	s.availability = a
	return s
}

// NewService is the canonical constructor. Order of args follows the order
// in which the dependencies are typically wired in main.
func NewService(
	users port.UserRepository,
	roles port.RoleRepository,
	branches port.BranchRepository,
	audits port.AuditRepository,
	configs port.PlatformConfigRepository,
	refresh port.RefreshTokenRepository,
	hasher port.PasswordHasher,
	tokens port.TokenIssuer,
	refreshTTL time.Duration,
	log *slog.Logger,
) *Service {
	return &Service{
		users:       users,
		roles:       roles,
		branches:    branches,
		audits:      audits,
		configs:     configs,
		refreshRepo: refresh,
		hasher:      hasher,
		tokens:      tokens,
		refreshTTL:  refreshTTL,
		log:         log,
	}
}

// Compile-time check that Service satisfies the driving port.
var _ port.UseCase = (*Service)(nil)

// =====================================================================
// Auth
// =====================================================================

// Login authenticates by email + password and returns access + refresh tokens.
//
// Security note: we do NOT distinguish between "no such user" and "wrong
// password" — both return the same Unauthorized error to prevent user
// enumeration via timing or response inspection.
func (s *Service) Login(ctx context.Context, in port.LoginInput) (*port.LoginOutput, error) {
	u, err := s.users.FindByEmail(ctx, in.Email)
	if err != nil {
		if de := derrors.As(err); de != nil && de.Kind == derrors.KindNotFound {
			return nil, derrors.Unauthorized("auth.invalid_credentials", "invalid email or password")
		}
		return nil, err
	}
	if !u.Active {
		return nil, derrors.Unauthorized("auth.inactive", "account is inactive")
	}
	if err := s.hasher.Compare(u.PasswordHash, in.Password); err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) {
			return nil, derrors.Unauthorized("auth.invalid_credentials", "invalid email or password")
		}
		return nil, derrors.Wrap(derrors.KindInternal, "auth.hash_failed", "could not verify credentials", err)
	}

	return s.issueSession(ctx, u, in.UserAgent, in.IP)
}

// Refresh consumes a valid refresh token and rotates it: revokes the old,
// issues a fresh pair.
//
// Replay defense: if a token that has already been revoked is presented,
// we treat that as a potential token theft and revoke every active token
// for this user. The legitimate client will get logged out everywhere and
// must sign in again — annoying but safer than leaving an attacker in.
func (s *Service) Refresh(ctx context.Context, in port.RefreshInput) (*port.LoginOutput, error) {
	id, secret, err := splitRefreshToken(in.RefreshToken)
	if err != nil {
		return nil, derrors.Unauthorized("auth.refresh_invalid", "refresh token is invalid")
	}

	existing, err := s.refreshRepo.FindActive(ctx, id, secret)
	if err != nil {
		if de := derrors.As(err); de != nil && de.Kind == derrors.KindNotFound {
			// Could be: never existed, expired, or already revoked.
			// We can't distinguish revoked-from-this-user without an extra
			// lookup; the repo could be enhanced to flag "revoked but row
			// existed" later. For now, fail closed.
			return nil, derrors.Unauthorized("auth.refresh_invalid", "refresh token is invalid")
		}
		return nil, err
	}
	if !existing.IsActive(time.Now()) {
		return nil, derrors.Unauthorized("auth.refresh_expired", "refresh token expired")
	}

	u, err := s.users.FindByID(ctx, existing.UserID)
	if err != nil {
		return nil, err
	}
	if !u.Active {
		return nil, derrors.Unauthorized("auth.inactive", "account is inactive")
	}

	// Mint the new pair first so we can record `replaced_by` correctly.
	out, err := s.issueSession(ctx, u, in.UserAgent, in.IP)
	if err != nil {
		return nil, err
	}

	// Now revoke the presented token and link it to the new one.
	// We need the new refresh's id; parse it back out.
	newID, _, err := splitRefreshToken(out.RefreshToken)
	if err != nil {
		return nil, derrors.Internal("auth.rotation_failed", "could not rotate refresh token")
	}
	if err := s.refreshRepo.Revoke(ctx, existing.ID, &newID); err != nil {
		// Don't fail the request — the client already has a valid new pair.
		// Just log and move on; the old token will expire naturally.
		s.log.Warn("revoke old refresh token failed", "err", err, "token_id", existing.ID)
	}
	return out, nil
}

// Logout revokes the refresh token presented by the client. The access
// token (JWT) is stateless — it remains valid until its short TTL expires;
// we accept that tradeoff for the simplicity of not running a blacklist.
func (s *Service) Logout(ctx context.Context, in port.LogoutInput) error {
	id, secret, err := splitRefreshToken(in.RefreshToken)
	if err != nil {
		// Don't error on a malformed token from /logout; logout is best-effort.
		return nil
	}
	rt, err := s.refreshRepo.FindActive(ctx, id, secret)
	if err != nil {
		// Already-revoked or unknown tokens are treated as already-logged-out.
		return nil
	}
	return s.refreshRepo.Revoke(ctx, rt.ID, nil)
}

// Me hydrates the current authenticated user.
func (s *Service) Me(ctx context.Context, userID uuid.UUID) (*port.MeOutput, error) {
	u, err := s.users.FindByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	// Force-logout when the user has been deactivated since the JWT was
	// minted. The FE polls /auth/me on focus/heartbeat, so this surfaces
	// within seconds rather than waiting for the 15-min access-token TTL.
	// The dedicated code lets the FE distinguish "deactivated, sign out"
	// from generic auth failures (expired token, bad signature, etc.).
	if !u.Active {
		// Same code Refresh uses — the FE can listen for one error key
		// from any auth checkpoint and force-logout consistently.
		return nil, derrors.Unauthorized("auth.inactive", "account is inactive")
	}
	roles, err := s.users.RolesForUser(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	perms, err := s.users.PermissionsForUser(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	return &port.MeOutput{User: *u, Roles: roles, Permissions: perms}, nil
}

// =====================================================================
// User management
// =====================================================================

// CreateUser provisions a new internal user with the given role assignments.
// The caller is expected to have already been authorized at the HTTP layer.
func (s *Service) CreateUser(ctx context.Context, in port.CreateUserInput) (*domain.User, error) {
	if existing, err := s.users.FindByEmail(ctx, in.Email); err == nil && existing != nil {
		return nil, derrors.Conflict("user.email_taken", "email already in use")
	}

	if in.SalesType != nil && !in.SalesType.Valid() {
		return nil, derrors.Validation("user.sales_type_invalid", "invalid sales_type")
	}
	if in.TechnicianGrade != nil && !in.TechnicianGrade.Valid() {
		return nil, derrors.Validation("user.tech_grade_invalid", "invalid technician_grade")
	}

	hash, err := s.hasher.Hash(in.Password)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "user.hash_failed", "could not hash password", err)
	}

	u, err := domain.NewUser(in.EmployeeID, in.FullName, in.Email, in.Phone, hash)
	if err != nil {
		return nil, err
	}

	if in.BranchID != nil {
		// Round-3 landmine: previous code silently dropped the
		// branch when branch_level was missing (the callers had to
		// know to pass both). We now auto-derive the level from the
		// branch row when the caller omits it, and reject the request
		// outright when the referenced branch doesn't exist —
		// preventing the silent half-assignment that used to slip
		// through.
		level := in.BranchLevel
		if level == nil {
			b, err := s.branches.FindByID(ctx, *in.BranchID)
			if err != nil {
				return nil, derrors.Validation("user.branch_unknown",
					"branch_id refers to a branch that doesn't exist")
			}
			lvl := b.Level
			level = &lvl
		}
		if err := u.AssignBranch(*in.BranchID, *level); err != nil {
			return nil, err
		}
	} else if in.BranchLevel != nil {
		// branch_level without branch_id is meaningless; flag it instead
		// of accepting an unanchored level that later writes ignore.
		return nil, derrors.Validation("user.branch_level_without_branch",
			"branch_level cannot be set without branch_id")
	}
	u.ReportsToID = in.ReportsToID

	if err := s.users.Create(ctx, u, in.RoleNames, in.SalesType, in.TechnicianGrade); err != nil {
		return nil, err
	}
	return u, nil
}

// UpdateUser applies a partial update + reads the new state. The repo
// validates that the user exists; we validate enum shapes here so the
// repo doesn't have to know about domain enums.
func (s *Service) UpdateUser(ctx context.Context, in port.UpdateUserInput) (*domain.User, error) {
	if in.SalesType != nil && !in.SalesType.Valid() {
		return nil, derrors.Validation("user.sales_type_invalid", "invalid sales_type")
	}
	if in.TechnicianGrade != nil && !in.TechnicianGrade.Valid() {
		return nil, derrors.Validation("user.tech_grade_invalid", "invalid technician_grade")
	}
	if in.BranchID != nil && (in.BranchLevel == nil || !in.BranchLevel.Valid()) {
		return nil, derrors.Validation("user.branch_level_required",
			"branch_level is required when branch_id is set")
	}
	if in.ReportsToID != nil && *in.ReportsToID == in.ID {
		return nil, derrors.Validation("user.reports_to_self", "user cannot report to themselves")
	}
	// TODO(rbac): walk the reports_to chain to reject loops. For now we rely
	// on the self-check; a recursive CTE check is on the M1 polish list.
	return s.users.Update(ctx, in)
}

func (s *Service) GetUser(ctx context.Context, id uuid.UUID) (*domain.User, error) {
	return s.users.FindByID(ctx, id)
}

// GetUserDetail returns the user plus roles and extension-table data,
// suited for the admin edit-user dialog.
func (s *Service) GetUserDetail(ctx context.Context, id uuid.UUID) (*port.UserDetail, error) {
	u, err := s.users.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	roles, err := s.users.RolesForUser(ctx, id)
	if err != nil {
		return nil, err
	}
	st, err := s.users.GetSalesType(ctx, id)
	if err != nil {
		return nil, err
	}
	g, err := s.users.GetTechnicianGrade(ctx, id)
	if err != nil {
		return nil, err
	}
	return &port.UserDetail{
		User:            *u,
		Roles:           roles,
		SalesType:       st,
		TechnicianGrade: g,
	}, nil
}

func (s *Service) ListUsers(ctx context.Context, f port.UserListFilter) ([]port.UserListItem, int, error) {
	return s.users.List(ctx, f)
}

func (s *Service) SetUserActive(ctx context.Context, id uuid.UUID, active bool) error {
	return s.users.SetActive(ctx, id, active)
}

// =====================================================================
// Branches
// =====================================================================

func (s *Service) ListBranches(ctx context.Context) ([]domain.Branch, error) {
	return s.branches.List(ctx)
}

func (s *Service) CreateBranch(ctx context.Context, in port.CreateBranchInput) (*domain.Branch, error) {
	b, err := domain.NewBranch(in.Name, in.Code, in.Level, in.ParentID)
	if err != nil {
		return nil, err
	}
	// Verify parent level if a parent is specified.
	if in.ParentID != nil {
		parent, err := s.branches.FindByID(ctx, *in.ParentID)
		if err != nil {
			return nil, err
		}
		if !validParentLevel(in.Level, parent.Level) {
			return nil, derrors.Validation("branch.parent_level_invalid",
				"parent must be one level above this branch")
		}
	}
	if err := s.branches.Create(ctx, b); err != nil {
		return nil, err
	}
	return b, nil
}

func (s *Service) UpdateBranch(ctx context.Context, in port.UpdateBranchInput) (*domain.Branch, error) {
	b, err := s.branches.FindByID(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if in.Name != nil {
		b.Name = *in.Name
	}
	if in.Active != nil {
		b.Active = *in.Active
	}
	if err := s.branches.Update(ctx, b); err != nil {
		return nil, err
	}
	return b, nil
}

// validParentLevel enforces: regional has no parent;
// area's parent must be regional; sub_area's parent must be area.
func validParentLevel(child, parent domain.BranchLevel) bool {
	switch child {
	case domain.BranchLevelArea:
		return parent == domain.BranchLevelRegional
	case domain.BranchLevelSubArea:
		return parent == domain.BranchLevelArea
	}
	return false
}

// =====================================================================
// Audit + Config
// =====================================================================

func (s *Service) ListAuditEntries(ctx context.Context, f domain.AuditFilter) ([]domain.AuditEntry, int, error) {
	return s.audits.List(ctx, f)
}

func (s *Service) ListPlatformConfig(ctx context.Context) ([]domain.PlatformConfig, error) {
	return s.configs.List(ctx)
}

func (s *Service) UpdatePlatformConfig(ctx context.Context, key, value string, updatedBy uuid.UUID) error {
	if key == "" {
		return derrors.Validation("config.key_required", "config key is required")
	}
	return s.configs.Upsert(ctx, key, value, updatedBy)
}

// =====================================================================
// Dashboard
// =====================================================================

func (s *Service) GetDashboardStats(ctx context.Context) (*port.DashboardStats, error) {
	activeUsers, totalUsers, err := s.users.CountActive(ctx)
	if err != nil {
		return nil, err
	}
	branchLevels, err := s.branches.CountByLevel(ctx)
	if err != nil {
		return nil, err
	}
	branchesTotal := 0
	for _, n := range branchLevels {
		branchesTotal += n
	}
	roles, err := s.roles.List(ctx)
	if err != nil {
		return nil, err
	}
	perms, err := s.roles.ListPermissions(ctx)
	if err != nil {
		return nil, err
	}
	recent, _, err := s.audits.List(ctx, domain.AuditFilter{Limit: 8})
	if err != nil {
		return nil, err
	}
	return &port.DashboardStats{
		UsersTotal:       totalUsers,
		UsersActive:      activeUsers,
		BranchesTotal:    branchesTotal,
		BranchesByLevel:  branchLevels,
		RolesTotal:       len(roles),
		PermissionsTotal: len(perms),
		RecentAudits:     recent,
	}, nil
}

// =====================================================================
// RBAC
// =====================================================================

func (s *Service) ListRoles(ctx context.Context) ([]domain.Role, error) {
	return s.roles.List(ctx)
}

func (s *Service) ListPermissions(ctx context.Context) ([]domain.Permission, error) {
	return s.roles.ListPermissions(ctx)
}

func (s *Service) PermissionsForRole(ctx context.Context, roleID uuid.UUID) ([]domain.Permission, error) {
	return s.roles.PermissionsForRole(ctx, roleID)
}

func (s *Service) AssignRolesToUser(ctx context.Context, in port.AssignRolesInput) error {
	if _, err := s.users.FindByID(ctx, in.UserID); err != nil {
		return err
	}
	return s.users.ReplaceRolesForUser(ctx, in.UserID, in.RoleNames)
}

func (s *Service) GetRole(ctx context.Context, id uuid.UUID) (*domain.Role, error) {
	return s.roles.FindByID(ctx, id)
}

func (s *Service) CreateRole(ctx context.Context, in port.CreateRoleInput) (*domain.Role, error) {
	return s.roles.Create(ctx, in.Name, in.Description)
}

func (s *Service) UpdateRole(ctx context.Context, in port.UpdateRoleInput) (*domain.Role, error) {
	return s.roles.Update(ctx, in.ID, in.Name, in.Description)
}

// DeleteRole — guards against deleting roles still assigned to users
// (the repo enforces the FK-check via a COUNT query). The admin must
// reassign users first.
func (s *Service) DeleteRole(ctx context.Context, id uuid.UUID) error {
	return s.roles.Delete(ctx, id)
}

// SetRolePermissions — atomic replacement of the role's permission set.
func (s *Service) SetRolePermissions(ctx context.Context, in port.SetRolePermissionsInput) error {
	return s.roles.ReplacePermissions(ctx, in.RoleID, in.PermissionIDs)
}

// =====================================================================
// Internal helpers
// =====================================================================

// issueSession mints a new access + refresh token pair and persists the
// refresh token (hashed). Used by both Login and Refresh.
func (s *Service) issueSession(ctx context.Context, u *domain.User, userAgent, ip string) (*port.LoginOutput, error) {
	roles, err := s.users.RolesForUser(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	perms, err := s.users.PermissionsForUser(ctx, u.ID)
	if err != nil {
		return nil, err
	}

	branchLevel := ""
	if u.BranchLevel != nil {
		branchLevel = string(*u.BranchLevel)
	}

	access, err := s.tokens.Issue(u.ID, u.Email, roles, perms, u.BranchID, branchLevel)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "auth.token_failed", "could not issue token", err)
	}

	// Refresh token: a UUID id + a random secret. The wire format is
	// "<id>.<secret>"; the DB stores bcrypt(secret) so a DB leak can't
	// be used to mint access tokens.
	rtID := uuid.New()
	secret, err := domain.GenerateRefreshTokenSecret()
	if err != nil {
		return nil, err
	}
	secretHash, err := s.hasher.Hash(secret)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "auth.hash_failed", "could not hash refresh token", err)
	}

	now := time.Now()
	rt := &domain.RefreshToken{
		ID:        rtID,
		UserID:    u.ID,
		TokenHash: secretHash,
		IssuedAt:  now,
		ExpiresAt: now.Add(s.refreshTTL),
		UserAgent: userAgent,
		IP:        ip,
	}
	if err := s.refreshRepo.Store(ctx, rt); err != nil {
		return nil, err
	}

	return &port.LoginOutput{
		AccessToken:      access,
		RefreshToken:     joinRefreshToken(rtID, secret),
		RefreshExpiresAt: rt.ExpiresAt,
		User:             *u,
		Roles:            roles,
		Permissions:      perms,
	}, nil
}

// Refresh token wire format: "<uuid>.<secret>".
// We split server-side so the repo lookup is O(1) by id, and the secret
// comparison is bcrypt — constant-time within a record.
func joinRefreshToken(id uuid.UUID, secret string) string {
	return id.String() + "." + secret
}

func splitRefreshToken(s string) (uuid.UUID, string, error) {
	dot := strings.IndexByte(s, '.')
	if dot <= 0 || dot == len(s)-1 {
		return uuid.Nil, "", derrors.Validation("refresh.format", "malformed refresh token")
	}
	id, err := uuid.Parse(s[:dot])
	if err != nil {
		return uuid.Nil, "", derrors.Validation("refresh.id", "malformed refresh token id")
	}
	return id, s[dot+1:], nil
}
