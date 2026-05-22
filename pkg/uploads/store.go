// Package uploads provides a minimal photo upload pipeline shared by
// every bounded context. Round-3 stores file bytes on the local
// filesystem under a configurable base directory and persists metadata
// (GPS, EXIF taken_at, sha256) in shared.photo_uploads.
//
// Round-4 swaps the local-fs adapter for a MinIO/S3 adapter behind the
// same Storer interface — no service / handler changes.
package uploads

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/google/uuid"

	derrors "github.com/ion-core/backend/pkg/errors"
)

// Storer abstracts how upload bytes get persisted. Round-3 impl writes
// to a local directory; round-4 will satisfy the same interface with
// an S3 / MinIO client.
type Storer interface {
	// Put writes the bytes under `key` and returns an `object_url` the
	// frontend can use to retrieve them later. Caller supplies key as a
	// stable, collision-resistant identifier (UUID + extension).
	Put(ctx context.Context, key string, r io.Reader) (objectURL string, bytes int64, err error)

	// ReadCloser returns a reader for the previously-stored object.
	// Used by the protected serve endpoint.
	ReadCloser(ctx context.Context, key string) (io.ReadCloser, error)
}

// LocalStore is a Storer backed by a local directory. Each object_url
// is the relative key prefixed with `local://` so consumers can tell
// apart from cloud URLs that will arrive in round-4.
type LocalStore struct {
	root string
}

func NewLocalStore(root string) (*LocalStore, error) {
	if root == "" {
		return nil, errors.New("uploads: local store needs a non-empty root path")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("uploads: create root: %w", err)
	}
	return &LocalStore{root: root}, nil
}

func (s *LocalStore) Put(ctx context.Context, key string, r io.Reader) (string, int64, error) {
	// We don't honour ctx cancellation in the inner copy — round-3
	// uploads are small (≤ 10 MB) and run to completion in well under
	// a request timeout. Round-4's S3 client will respect ctx.
	abs := filepath.Join(s.root, key)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "", 0, fmt.Errorf("uploads: mkdir: %w", err)
	}
	f, err := os.Create(abs)
	if err != nil {
		return "", 0, fmt.Errorf("uploads: create: %w", err)
	}
	defer f.Close()
	n, err := io.Copy(f, r)
	if err != nil {
		return "", 0, fmt.Errorf("uploads: write: %w", err)
	}
	return "local://" + key, n, nil
}

func (s *LocalStore) ReadCloser(ctx context.Context, key string) (io.ReadCloser, error) {
	abs := filepath.Join(s.root, key)
	f, err := os.Open(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, derrors.NotFound("upload.not_found", "upload not found")
		}
		return nil, derrors.Wrap(derrors.KindInternal, "upload.read", "open file", err)
	}
	return f, nil
}

// NewKey builds a content-addressable-ish key with a UUID + extension.
// We don't hash-name yet (sha256 is computed during upload), so two
// identical photos uploaded by different techs get distinct keys.
// Dedup-by-hash is a round-4 polish.
func NewKey(ext string) string {
	id := uuid.New().String()
	if ext == "" {
		return "photos/" + id
	}
	return "photos/" + id + ext
}

// SHA256Hex returns the hex-encoded sha256 of a byte slice. We compute
// the hash *after* the upload so we don't need to buffer the whole file
// in memory before the write — instead the handler tees the body
// through a hasher while it's being copied. Helper here for callers
// that already have the bytes (e.g. small EXIF payloads).
func SHA256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
