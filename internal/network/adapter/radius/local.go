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
	"github.com/ion-core/backend/pkg/audit"
	"github.com/ion-core/backend/pkg/auth"
	"github.com/ion-core/backend/pkg/cryptutil"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type LocalRadiusClient struct {
	pool   *pgxpool.Pool
	log    *slog.Logger
	auditW audit.Writer // Wave 81 (TC-RAD-021) — defaults to Nop; mutating lifecycle methods emit through it.
	// Wave 80 phase 1 — optional sealer. When wired, Provision writes
	// the AES-GCM-sealed plaintext alongside the bcrypt hash so phase
	// 2's FreeRadiusClient can later open it for CHAP. Without the
	// sealer this client keeps writing bcrypt-only as before.
	sealer        *cryptutil.Sealer
	sealerKeyVer  int
}

func NewLocalClient(pool *pgxpool.Pool, log *slog.Logger) *LocalRadiusClient {
	return &LocalRadiusClient{
		pool:   pool,
		log:    log.With("component", "radius_local"),
		auditW: audit.Nop{},
	}
}

// WithSealer wires the AES-GCM sealer used to encrypt the plaintext
// password for later FreeRADIUS CHAP. keyVersion is the integer
// stamped into password_key_version so the keyring can rotate keys
// without losing the ability to open older rows. Nil sealer is a
// no-op — clients without a sealer wired keep writing bcrypt-only.
func (c *LocalRadiusClient) WithSealer(s *cryptutil.Sealer, keyVersion int) *LocalRadiusClient {
	if s != nil {
		c.sealer = s
		if keyVersion <= 0 {
			keyVersion = 1
		}
		c.sealerKeyVer = keyVersion
	}
	return c
}

// WithAudit attaches the audit writer. Wave 81 (TC-RAD-021) — every
// state change (Provision / Promote / Suspend / Restore / Deactivate)
// emits a row through this writer so the operations audit trail
// captures who/when across the RADIUS lifecycle.
func (c *LocalRadiusClient) WithAudit(w audit.Writer) *LocalRadiusClient {
	if w != nil {
		c.auditW = w
	}
	return c
}

var _ port.RadiusClient = (*LocalRadiusClient)(nil)

// Provision creates a new RADIUS account in TEMPORARY state.
// Idempotent on customer_id — if an account already exists we refresh
// its `temp_expires_at` deadline (so a re-scheduled WO extends the
// grace window) and return the row. The PRD §13 lifecycle treats
// re-provision as a no-op-with-deadline-refresh rather than a duplicate.
//
// WindowHours = 0 falls back to the system default (72h) so callers
// that don't know the product's Service Schema still get sane behavior.
func (c *LocalRadiusClient) Provision(ctx context.Context, in domain.ProvisionInput) (*domain.RadiusAccount, error) {
	window := in.WindowHours
	if window <= 0 {
		window = 72
	}
	now := time.Now().UTC()
	expires := now.Add(time.Duration(window) * time.Hour)

	if existing, err := c.Find(ctx, in.CustomerID); err == nil && existing != nil {
		// Refresh deadline; do not flip back from PERMANENT/SUSPENDED.
		// This is the WO-rescheduled path: same customer, same RADIUS,
		// later expiry.
		if existing.Status == domain.RadiusStatusTemporary {
			_, err := c.pool.Exec(ctx, `
				UPDATE network.radius_accounts
				   SET temp_expires_at = $2, updated_at = $3
				 WHERE customer_id = $1
			`, in.CustomerID, expires, now)
			if err != nil {
				return nil, derrors.Wrap(derrors.KindInternal, "radius.refresh_expiry",
					"refresh temp expiry", err)
			}
			c.log.Info("radius temp expiry refreshed",
				"customer_id", in.CustomerID, "window_hours", window)
			return c.Find(ctx, in.CustomerID)
		}
		c.log.Warn("radius.Provision skipped — account not in TEMPORARY",
			"customer_id", in.CustomerID, "status", string(existing.Status))
		return existing, nil
	}

	hash, err := auth.HashPassword(in.PasswordPlain)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "radius.hash", "hash password", err)
	}
	// Wave 80 phase 1 — dual-write the AES-GCM sealed plaintext when
	// a sealer is wired so phase 2's FreeRADIUS CHAP path can later
	// open it. Both bcrypt (legacy verification) and sealed (forward
	// path) land atomically on the same row.
	var (
		sealed     []byte
		keyVersion *int
	)
	if c.sealer != nil && in.PasswordPlain != "" {
		s, serr := c.sealer.Seal(in.PasswordPlain)
		if serr != nil {
			return nil, derrors.Wrap(derrors.KindInternal,
				"radius.seal", "seal radius password", serr)
		}
		sealed = s
		v := c.sealerKeyVer
		keyVersion = &v
	}
	id := uuid.New()
	_, err = c.pool.Exec(ctx, `
		INSERT INTO network.radius_accounts
			(id, customer_id, username, password_hash, password_sealed,
			 password_key_version,
			 vlan_id, bandwidth_profile_id, status,
			 temp_activated_at, temp_expires_at,
			 created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'temporary', $9, $10, $11, $11)
	`, id, in.CustomerID, in.Username, hash, sealed, keyVersion,
		in.VLANID, in.BandwidthProfileID, now, expires, now)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "radius.insert", "create radius account", err)
	}
	c.log.Info("radius account provisioned (TEMPORARY)",
		"customer_id", in.CustomerID, "username", in.Username, "window_hours", window)
	// Wave 81 (TC-RAD-021) — provisioning crosses a security
	// boundary (network access granted), so we always audit even
	// for the happy path. UserID is left nil here because the
	// adapter can't see the actor; HTTP handlers that wrap this
	// can layer their own audit row with the actor when needed.
	audit.SafeWrite(ctx, c.auditW, audit.Entry{
		Module:     "network",
		RecordType: "radius_account",
		RecordID:   in.CustomerID.String(),
		After:      "status=temporary username=" + in.Username,
		Reason:     "radius_provisioned",
	})
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
	// Wave 81 (TC-RAD-021) — every lifecycle transition is auditable.
	// FieldChanged="status" lets the admin viewer collapse multiple
	// rows into a single "status timeline" widget per account.
	audit.SafeWrite(ctx, c.auditW, audit.Entry{
		Module:       "network",
		RecordType:   "radius_account",
		RecordID:     customerID.String(),
		FieldChanged: "status",
		After:        string(to),
		Reason:       "radius_status_transition",
	})
	return c.Find(ctx, customerID)
}

func (c *LocalRadiusClient) Find(ctx context.Context, customerID uuid.UUID) (*domain.RadiusAccount, error) {
	row := c.pool.QueryRow(ctx, `
		SELECT id, customer_id, username, password_hash,
		       vlan_id, COALESCE(bandwidth_profile_id, ''), ip_address,
		       status, temp_activated_at, temp_expires_at,
		       perm_activated_at, suspended_at,
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
		&a.TempActivatedAt, &a.TempExpiresAt,
		&a.PermActivatedAt, &a.SuspendedAt,
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
