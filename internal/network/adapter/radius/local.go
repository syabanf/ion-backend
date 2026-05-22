// Package radius holds the network-svc-side RADIUS adapter implementations.
//
// LocalRadiusClient is the DB-backed stub used in Phase 1.
//
// PRD §13 makes clear that ION Radius is an external system. We don't run
// FreeRADIUS in dev (and don't have credentials to a shared one), so this
// adapter persists state transitions to network.radius_accounts and that's
// it. When the FreeRADIUS adapter lands, it implements the same RadiusClient
// interface; nothing else needs to change.
//
// The interface methods correspond 1:1 to the lifecycle in PRD:
//
//   Provision()         WO created          → TEMPORARY
//   PromoteToPermanent  NOC verified BAST   → PERMANENT_ACTIVE
//   Suspend             schema fired        → SUSPENDED
//   Restore             payment cleared     → PERMANENT_ACTIVE
//   Deactivate          termination         → DEACTIVATED
package radius

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/network/domain"
	"github.com/ion-core/backend/internal/network/port"
	derrors "github.com/ion-core/backend/pkg/errors"
	"github.com/ion-core/backend/pkg/auth"
)

type LocalRadiusClient struct {
	pool *pgxpool.Pool
	log  *slog.Logger
}

func NewLocalClient(pool *pgxpool.Pool, log *slog.Logger) *LocalRadiusClient {
	return &LocalRadiusClient{pool: pool, log: log.With("component", "radius_local")}
}

var _ port.RadiusClient = (*LocalRadiusClient)(nil)

// Provision creates a new RADIUS account in TEMPORARY state.
// Idempotent on customer_id — if an account already exists we return it
// (the onboarding flow should never call Provision twice for the same
// customer, but if it does we want a safe no-op).
func (c *LocalRadiusClient) Provision(ctx context.Context, in domain.ProvisionInput) (*domain.RadiusAccount, error) {
	if existing, err := c.Find(ctx, in.CustomerID); err == nil && existing != nil {
		c.log.Warn("radius.Provision called twice", "customer_id", in.CustomerID)
		return existing, nil
	}
	hash, err := auth.HashPassword(in.PasswordPlain)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "radius.hash", "hash password", err)
	}
	now := time.Now().UTC()
	id := uuid.New()
	_, err = c.pool.Exec(ctx, `
		INSERT INTO network.radius_accounts
			(id, customer_id, username, password_hash,
			 vlan_id, bandwidth_profile_id, status, temp_activated_at,
			 created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, 'temporary', $7, $8, $8)
	`, id, in.CustomerID, in.Username, hash, in.VLANID, in.BandwidthProfileID, now, now)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "radius.insert", "create radius account", err)
	}
	c.log.Info("radius account provisioned (TEMPORARY)",
		"customer_id", in.CustomerID, "username", in.Username)
	return c.Find(ctx, in.CustomerID)
}

func (c *LocalRadiusClient) PromoteToPermanent(ctx context.Context, customerID uuid.UUID) (*domain.RadiusAccount, error) {
	return c.transition(ctx, customerID, domain.RadiusStatusPermanentActive, "perm_activated_at")
}

func (c *LocalRadiusClient) Suspend(ctx context.Context, customerID uuid.UUID) (*domain.RadiusAccount, error) {
	return c.transition(ctx, customerID, domain.RadiusStatusSuspended, "suspended_at")
}

func (c *LocalRadiusClient) Restore(ctx context.Context, customerID uuid.UUID) (*domain.RadiusAccount, error) {
	// Restore = transition back to PERMANENT_ACTIVE; we don't bump perm_activated_at
	// because PRD treats restoration as a continuation of the existing service.
	return c.transition(ctx, customerID, domain.RadiusStatusPermanentActive, "")
}

func (c *LocalRadiusClient) Deactivate(ctx context.Context, customerID uuid.UUID) (*domain.RadiusAccount, error) {
	return c.transition(ctx, customerID, domain.RadiusStatusDeactivated, "")
}

// transition flips the status, optionally stamping a timestamp column.
// timestampCol="" means "don't stamp anything new".
func (c *LocalRadiusClient) transition(ctx context.Context, customerID uuid.UUID, to domain.RadiusStatus, timestampCol string) (*domain.RadiusAccount, error) {
	now := time.Now().UTC()
	stamp := ""
	if timestampCol != "" {
		stamp = ", " + timestampCol + " = $3"
	}
	q := `UPDATE network.radius_accounts SET status = $2` + stamp + ` WHERE customer_id = $1`

	args := []any{customerID, string(to)}
	if timestampCol != "" {
		args = append(args, now)
	}

	tag, err := c.pool.Exec(ctx, q, args...)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "radius.transition", "update status", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, derrors.NotFound("radius.not_found", "radius account not found")
	}
	c.log.Info("radius status transition", "customer_id", customerID, "to", string(to))
	return c.Find(ctx, customerID)
}

func (c *LocalRadiusClient) Find(ctx context.Context, customerID uuid.UUID) (*domain.RadiusAccount, error) {
	row := c.pool.QueryRow(ctx, `
		SELECT id, customer_id, username, password_hash,
		       vlan_id, COALESCE(bandwidth_profile_id, ''), ip_address,
		       status, temp_activated_at, perm_activated_at, suspended_at,
		       created_at, updated_at
		FROM network.radius_accounts
		WHERE customer_id = $1
	`, customerID)

	var (
		a       domain.RadiusAccount
		ip      *net.IP
		status  string
	)
	err := row.Scan(
		&a.ID, &a.CustomerID, &a.Username, &a.PasswordHash,
		&a.VLANID, &a.BandwidthProfileID, &ip, &status,
		&a.TempActivatedAt, &a.PermActivatedAt, &a.SuspendedAt,
		&a.CreatedAt, &a.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "radius.find", "find radius account", err)
	}
	a.Status = domain.RadiusStatus(status)
	if ip != nil {
		a.IPAddress = ip.String()
	}
	return &a, nil
}
