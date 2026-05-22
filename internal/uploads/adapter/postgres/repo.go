package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ion-core/backend/internal/uploads/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

var _ port.Repository = (*Repository)(nil)

func (r *Repository) Create(ctx context.Context, u *port.PhotoUpload) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO shared.photo_uploads
		  (id, object_url, content_type, bytes, gps_lat, gps_lng, gps_accuracy_m,
		   taken_at, sha256, uploaded_by, uploaded_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
	`,
		u.ID, u.ObjectURL, u.ContentType, u.Bytes, u.GPSLat, u.GPSLng, u.GPSAccuracyM,
		u.TakenAt, nullable(u.SHA256), u.UploadedBy, u.UploadedAt,
	)
	if err != nil {
		return derrors.Wrap(derrors.KindInternal, "upload.create", "save upload metadata", err)
	}
	return nil
}

func (r *Repository) FindByObjectURL(ctx context.Context, objectURL string) (*port.PhotoUpload, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, object_url, content_type, bytes,
		       gps_lat, gps_lng, gps_accuracy_m, taken_at, COALESCE(sha256,''),
		       uploaded_by, uploaded_at
		FROM shared.photo_uploads
		WHERE object_url = $1
		LIMIT 1
	`, objectURL)
	var u port.PhotoUpload
	if err := row.Scan(&u.ID, &u.ObjectURL, &u.ContentType, &u.Bytes,
		&u.GPSLat, &u.GPSLng, &u.GPSAccuracyM, &u.TakenAt, &u.SHA256,
		&u.UploadedBy, &u.UploadedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, derrors.NotFound("upload.not_found", "upload metadata not found")
		}
		return nil, derrors.Wrap(derrors.KindInternal, "upload.find", "find upload", err)
	}
	return &u, nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
