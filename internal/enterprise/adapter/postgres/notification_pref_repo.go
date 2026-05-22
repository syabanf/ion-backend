package postgres

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/enterprise/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type NotificationPrefRepository struct {
	pool *pgxpool.Pool
}

func NewNotificationPrefRepository(pool *pgxpool.Pool) *NotificationPrefRepository {
	return &NotificationPrefRepository{pool: pool}
}

var _ port.NotificationPrefRepository = (*NotificationPrefRepository)(nil)

func (r *NotificationPrefRepository) ListForUser(ctx context.Context, userID uuid.UUID) ([]port.NotificationPref, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT user_id, kind, enabled FROM enterprise.notification_preferences WHERE user_id = $1 ORDER BY kind`,
		userID,
	)
	if err != nil {
		return nil, derrors.Wrap(derrors.KindInternal, "db.notif_pref_list", "list", err)
	}
	defer rows.Close()
	out := []port.NotificationPref{}
	for rows.Next() {
		var p port.NotificationPref
		if err := rows.Scan(&p.UserID, &p.Kind, &p.Enabled); err != nil {
			return nil, derrors.Wrap(derrors.KindInternal, "db.notif_pref_scan", "scan", err)
		}
		out = append(out, p)
	}
	return out, nil
}

func (r *NotificationPrefRepository) Upsert(ctx context.Context, pref port.NotificationPref) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO enterprise.notification_preferences (user_id, kind, enabled)
		VALUES ($1, $2, $3)
		ON CONFLICT (user_id, kind) DO UPDATE SET enabled = EXCLUDED.enabled, updated_at = NOW()
	`, pref.UserID, pref.Kind, pref.Enabled)
	if err != nil {
		return mapDBError(err, "notification_preference", "upsert")
	}
	return nil
}

func (r *NotificationPrefRepository) Delete(ctx context.Context, userID uuid.UUID, kind string) error {
	_, err := r.pool.Exec(ctx,
		`DELETE FROM enterprise.notification_preferences WHERE user_id = $1 AND kind = $2`,
		userID, kind,
	)
	if err != nil {
		return mapDBError(err, "notification_preference", "delete")
	}
	return nil
}

// IsEnabled — default-on. Checks `*` (global mute) first, then any
// `module.*` wildcard whose prefix matches, then the exact kind.
// First muting match wins. If no row matches → enabled.
func (r *NotificationPrefRepository) IsEnabled(ctx context.Context, userID uuid.UUID, kind string) (bool, error) {
	// Pull all rows for the user — they're small (typically < 20) so
	// one query + in-memory check is cheaper than three round-trips.
	rows, err := r.pool.Query(ctx,
		`SELECT kind, enabled FROM enterprise.notification_preferences WHERE user_id = $1`,
		userID,
	)
	if err != nil {
		// On error, default-on so a flaky DB doesn't silently swallow notifications.
		return true, derrors.Wrap(derrors.KindInternal, "db.notif_pref_is_enabled", "query", err)
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var enabled bool
		if err := rows.Scan(&k, &enabled); err != nil {
			return true, err
		}
		switch {
		case k == "*":
			if !enabled {
				return false, nil
			}
		case strings.HasSuffix(k, ".*"):
			prefix := strings.TrimSuffix(k, ".*")
			if strings.HasPrefix(kind, prefix+".") && !enabled {
				return false, nil
			}
		case k == kind:
			return enabled, nil
		}
	}
	return true, nil
}
