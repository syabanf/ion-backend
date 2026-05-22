// Resolver — the concrete read-only adapter that satisfies
// port.SchemaResolver for cross-module consumers.
//
// Why split this out: the operator-side SchemaUseCase in service.go
// already does the heavy lifting (override → published → DEFAULT
// fallback + shallow-merge). Cross-cutting consumers only need the
// final resolved JSON object as a Go map. Wrapping SchemaUseCase keeps
// that single source of truth — no duplicate resolution logic.
package usecase

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"

	"github.com/ion-core/backend/internal/platform/domain"
	"github.com/ion-core/backend/internal/platform/port"
	derrors "github.com/ion-core/backend/pkg/errors"
)

// Resolver wraps SchemaUseCase and adapts the response to the
// narrower SchemaResolver port. Allocates one map per call; this is
// fine for tick-cadence workloads (minutes apart) and keeps the
// surface free of json.RawMessage in downstream callers.
type Resolver struct {
	uc port.SchemaUseCase
}

// NewResolver constructs a SchemaResolver from any SchemaUseCase. The
// usual call site is cmd/<svc>/main.go where the use case is built
// against the shared pgx pool.
func NewResolver(uc port.SchemaUseCase) *Resolver {
	return &Resolver{uc: uc}
}

var _ port.SchemaResolver = (*Resolver)(nil)

// Resolve picks up override + published schema for (customer, kind) and
// returns the resolved JSON body as a map. customerID == uuid.Nil short-
// circuits to ResolveDefault — same semantic as the underlying use
// case, which falls through to the DEFAULT code when no override is
// found.
func (r *Resolver) Resolve(ctx context.Context, customerID uuid.UUID, kind string) (map[string]any, error) {
	if customerID == uuid.Nil {
		return r.ResolveDefault(ctx, kind)
	}
	k, err := domain.ParseSchemaKind(kind)
	if err != nil {
		return nil, err
	}
	raw, _, _, err := r.uc.ResolveSchemaForCustomer(ctx, customerID, k)
	if err != nil {
		return nil, err
	}
	return unmarshalBody(raw)
}

// ResolveDefault returns the kind's DEFAULT-code published version
// straight from the use case path; we re-use ResolveSchemaForCustomer
// with uuid.Nil so it traverses the same "no override → DEFAULT"
// fallback.
func (r *Resolver) ResolveDefault(ctx context.Context, kind string) (map[string]any, error) {
	k, err := domain.ParseSchemaKind(kind)
	if err != nil {
		return nil, err
	}
	// Passing uuid.Nil skips the override lookup (there can't be one)
	// and falls through to FindLatestPublished(kind, "DEFAULT").
	raw, _, _, err := r.uc.ResolveSchemaForCustomer(ctx, uuid.Nil, k)
	if err != nil {
		return nil, err
	}
	return unmarshalBody(raw)
}

// unmarshalBody turns a resolved JSON body into a generic map. Empty
// or all-whitespace bodies yield an empty map so callers can ranging
// over the result without nil-checking.
func unmarshalBody(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, derrors.Wrap(
			derrors.KindInternal,
			"schema.resolver_unmarshal",
			"failed to unmarshal resolved schema body",
			err,
		)
	}
	if out == nil {
		out = map[string]any{}
	}
	return out, nil
}
