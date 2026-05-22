// Resolver — the minimal cross-module client surface for the platform
// Schema System v1.
//
// Background. Schema System v1 ships a full operator surface (list /
// create / publish / supersede / override). Downstream modules — billing
// tick, commission calc, suspension scheduler — only need ONE thing: a
// function that, given a customer + kind, returns the resolved JSON body
// they should evaluate.
//
// Exposing the full SchemaUseCase to billing-svc / crm-svc would couple
// those services to operator-side concerns (CreateSchemaInput, draft
// lifecycle, etc.) that they have no business knowing about. SchemaResolver
// is the narrow read-only port: a stable, type-light contract that
// the cross-cutting consumers depend on.
//
// The concrete implementation in usecase.NewResolver wraps SchemaUseCase;
// when we ever split platform out into its own service the same
// interface gets an HTTP-backed implementation without any caller
// having to change.
package port

import (
	"context"

	"github.com/google/uuid"
)

// SchemaResolver is the read-only port consumed by modules outside the
// platform bounded context.
//
// Both methods return the resolved JSON object as a Go map so callers
// can pluck keys without dragging json.RawMessage / encoding/json into
// every billing usecase. NotFound errors flow through unwrapped — the
// derrors.IsNotFound helper still works at the call site.
type SchemaResolver interface {
	// Resolve returns the schema body for a customer + kind, with any
	// customer-level override patch shallow-merged over the published
	// base. The customerID may be uuid.Nil — in that case the resolver
	// falls through to the kind's DEFAULT-code published version (same
	// semantic as ResolveDefault).
	Resolve(ctx context.Context, customerID uuid.UUID, kind string) (map[string]any, error)

	// ResolveDefault returns the kind's DEFAULT-code published version.
	// Used by tick-style flows that don't have a customer in scope
	// until the loop iterates (e.g. policy lookup for the late-fee
	// pass over all overdue invoices).
	ResolveDefault(ctx context.Context, kind string) (map[string]any, error)
}
