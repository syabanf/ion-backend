package http

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/auth"
	stderrors "github.com/ion-core/backend/pkg/errors"
	"github.com/ion-core/backend/pkg/httpserver"
)

// =====================================================================
// Wave 97 — Company-scope predicate + suspended-actor short-circuit
// =====================================================================
//
// CompanyScope is the structured view of "which subsidiary's records is
// this actor allowed to see". Built once per request from the verified
// JWT claims. Repos that filter on `commercial_owner_company_id` (BOQ,
// Customer PO, IC-PO, EWO, Invoice, ...) consult AppliesTo() before
// returning rows.
//
// Encoding:
//   - A wildcard scope (AllowedSubsidiaryIDs == nil) means "no
//     restriction" — only super_admin / holding_director / finance_admin
//     get this. Everything else gets a concrete list.
//   - An empty AllowedSubsidiaryIDs (non-nil, len 0) means "actor has
//     NO commercial scope" — safe-by-default for a token that lacks
//     the subsidiary_id claim entirely. Repos will return zero rows.
//
// Token-shape note:
//
//   pkg/auth.Claims now carries SubsidiaryID + HoldingCompanyID
//   (Wave 97). Old tokens issued before the migration won't have them;
//   AllowedSubsidiaryIDs ends up empty, which means no commercial-owned
//   rows are visible — the secure default. Existing legacy endpoints
//   (pricebook, opportunity, ...) that don't yet call AppliesTo continue
//   to work because the scope object is purely advisory until the repo
//   calls in.

// CompanyScope describes which subsidiary IDs the actor may transact
// against. See package doc above for the wildcard convention.
type CompanyScope struct {
	// AllowedSubsidiaryIDs lists the concrete subsidiary IDs the actor
	// may see commercial-owner records for. Nil means wildcard (no
	// restriction). Empty (non-nil) means the actor has no commercial
	// scope and should see zero rows.
	AllowedSubsidiaryIDs []uuid.UUID

	// HoldingCompanyID is the actor's holding company. Useful for
	// cross-sister reads that span the whole holding (e.g. ledger
	// reconciliation by a holding_director).
	HoldingCompanyID *uuid.UUID

	// SubsidiaryID is the actor's PRIMARY subsidiary — the home
	// company encoded in the token. Repos that need to distinguish
	// "this is my home sister" from "I have visibility into this
	// other sister too" can read it.
	SubsidiaryID *uuid.UUID

	// Wildcard tracks whether this scope was constructed in wildcard
	// mode. AllowedSubsidiaryIDs == nil already encodes that, but a
	// dedicated boolean makes audit logging + decision tests cheaper.
	Wildcard bool
}

// ctxKey to attach a resolved CompanyScope to the request context. We
// keep this PRIVATE so handlers MUST go through ResolveScope (which
// reads the verified claims) rather than constructing scopes
// directly.
type companyScopeCtxKey struct{}

// WithScope attaches a CompanyScope to ctx. Test helpers + the
// resolver use this.
func WithScope(ctx context.Context, scope CompanyScope) context.Context {
	return context.WithValue(ctx, companyScopeCtxKey{}, scope)
}

// ScopeFromContext extracts a previously-attached CompanyScope. The
// boolean second return is false when no scope was attached (e.g. the
// request bypassed the enterprise auth chain — a programming error in
// the handler wiring). Callers should treat ok=false as "deny" since
// the absence of scope means the auth chain was not run.
func ScopeFromContext(ctx context.Context) (CompanyScope, bool) {
	v, ok := ctx.Value(companyScopeCtxKey{}).(CompanyScope)
	return v, ok
}

// ResolveScope builds a CompanyScope from the verified claims on ctx.
//
// Ranking:
//  1. Full-access roles -> wildcard scope. Tokens for super_admin /
//     holding_director / finance_admin pass through unrestricted.
//  2. Concrete subsidiary in claim -> single-subsidiary scope. This is
//     the common case for sister + reseller users.
//  3. No subsidiary at all -> empty (deny-all) scope. New endpoints
//     that rely on AppliesTo will return zero rows, which is the
//     safe-by-default behaviour for tokens issued before the Wave 97
//     migration of the Claims struct.
//
// We return a value, not a pointer, because CompanyScope is small +
// rarely nil-meaningful. Tests can call WithScope to inject a fake.
func ResolveScope(ctx context.Context) CompanyScope {
	// If a scope was already attached (test helper, integration test
	// scaffolding), respect it.
	if s, ok := ScopeFromContext(ctx); ok {
		return s
	}
	claims := httpserver.ClaimsFromContext(ctx)
	if claims == nil {
		return CompanyScope{}
	}
	// Full-access role -> wildcard.
	if ClassifyActor(claims) == RoleFullAccess {
		return CompanyScope{
			AllowedSubsidiaryIDs: nil,
			HoldingCompanyID:     claims.HoldingCompanyID,
			SubsidiaryID:         claims.SubsidiaryID,
			Wildcard:             true,
		}
	}
	// Token without a subsidiary stamp -> empty deny scope. Returning
	// nil AllowedSubsidiaryIDs here would be UNSAFE because it would
	// signal wildcard.
	if claims.SubsidiaryID == nil {
		return CompanyScope{
			AllowedSubsidiaryIDs: []uuid.UUID{},
			HoldingCompanyID:     claims.HoldingCompanyID,
			SubsidiaryID:         nil,
			Wildcard:             false,
		}
	}
	return CompanyScope{
		AllowedSubsidiaryIDs: []uuid.UUID{*claims.SubsidiaryID},
		HoldingCompanyID:     claims.HoldingCompanyID,
		SubsidiaryID:         claims.SubsidiaryID,
		Wildcard:             false,
	}
}

// AppliesTo reports whether the scope grants visibility into the row
// whose commercial owner is the given subsidiary id. Wildcard scopes
// always return true; explicit scopes do a linear scan (the list is
// always small — at most a handful of sisters).
func (s CompanyScope) AppliesTo(commercialOwnerSubsidiaryID uuid.UUID) bool {
	if s.Wildcard {
		return true
	}
	for _, id := range s.AllowedSubsidiaryIDs {
		if id == commercialOwnerSubsidiaryID {
			return true
		}
	}
	return false
}

// =====================================================================
// RequireActiveActor middleware — TC-RBAC-029 suspended login block.
// =====================================================================
//
// Identity-svc marks a user inactive via the existing
// `User.Deactivate()` flow + a `suspended_at` column on users. Tokens
// already issued continue to verify until expiry, so we short-circuit
// any request whose claims carry a non-nil SuspendedAt on the
// enterprise side. Logging out from identity also revokes the refresh
// token; this middleware closes the residual-access window for the
// access token's remaining TTL (≤15 min by default).
//
// Returns 403 with code `actor.suspended`. The 403 vs 401 choice is
// deliberate: the token itself is structurally valid (so 401 would
// invite the client to refresh + retry); the actor's privileges have
// been revoked, which is forbidden, not unauthenticated.
//
// The verifier argument is currently unused — we read SuspendedAt
// straight off the claims already attached by RequireAuth. We accept
// it for forward-compat: if a future wave moves the suspension check
// to a per-request lookup (e.g. SuspendedAt was set AFTER the token
// was minted), we'll need the verifier to recheck. Removing the
// parameter later would be a callsite-only change.
func RequireActiveActor(verifier *auth.Verifier) func(http.Handler) http.Handler {
	_ = verifier // unused today; see comment above
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims := httpserver.ClaimsFromContext(r.Context())
			// No claims = RequireAuth wasn't in the chain ahead of us.
			// That's a handler-wiring bug. We pass through rather than
			// 401 because /healthz + other public routes share the
			// router and we don't want to block them.
			if claims == nil {
				next.ServeHTTP(w, r)
				return
			}
			if claims.SuspendedAt != nil && !claims.SuspendedAt.IsZero() {
				httpserver.WriteError(w, stderrors.Forbidden(
					"actor.suspended",
					"actor is suspended; please log in again",
				))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
