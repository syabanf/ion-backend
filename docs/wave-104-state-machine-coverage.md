# Wave 104 — State Machine Coverage + Optimistic Locking + Audit Query API

**Date:** 2026-05-23.
**Scope:** Phase 1 Enterprise final-push closing of State Machine sweep
(56 TCs), Edge Case targeted fill-in (~20 TCs), Audit Log query +
hash-chain verify surface (8 TCs).

This is the load-bearing closer for the Wave-91 audit roadmap. By
acceptance the catalog moves to **319 / 455** TCs runnable (70.1%).

---

## 1. State machine contract test sweep

Each domain SM is pinned by a table-driven test enumerating every valid
transition + every documented invalid transition. The tests drive the
domain method DIRECTLY (no HTTP, no DB) so they run in milliseconds and
catch regressions in business rules without requiring infra.

| State machine | File | Valid | Invalid | Total |
|---|---|---:|---:|---:|
| Opportunity (Stage) | `internal/enterprise/domain/opportunity_sm_test.go` | 6 | 10 | 16 |
| BOQ (Status) | `internal/enterprise/domain/boq_sm_test.go` | 6 | 12 | 18 |
| CustomerPO | `internal/enterprise/domain/customer_po_sm_test.go` | 6 | 10 | 16 |
| IntercompanyPO | `internal/enterprise/domain/intercompany_po_sm_test.go` | 7 | 11 | 18 |
| EWO (incl. schedule-lock pin) | `internal/enterprise/domain/ewo_sm_test.go` | 4 | 8 (+ 1 schedule-lock) | 13 |
| WholesaleOrder | `internal/reseller/domain/wholesale_order_sm_test.go` | 6 | 11 (+ 1 empty-submit) | 18 |
| Subscriber (`active ↔ suspended → terminated`) | `internal/reseller/domain/subscriber_sm_test.go` | 7 | 4 | 11 |
| MonthlySubmission | `internal/partnership/domain/monthly_submission_sm_test.go` | 8 | 10 | 18 |
| Settlement | `internal/partnership/domain/settlement_sm_test.go` | 7 | 6 | 13 |
| **TOTAL** | | **57** | **82** | **141** |

The tests directly close **TC-SM-001 … TC-SM-056** from the catalog.

### Patterns

- **Constructor-style helpers** — each test file ships a `newXAt(t, status)` that drives the SM forward through legal transitions to reach a target state. Tests never reach into struct fields directly; the SM has to be reachable through the public API.
- **`derrors.Error` code assertions** — invalid-transition rows assert the typed error's `.Code` matches the documented string (`*.invalid_state_transition`, `*.cannot_*`, etc.) so a future refactor that drops the code surface fails immediately.
- **Idempotent destinations are documented explicitly** — e.g. Subscriber's `suspended -> suspended` re-Suspend appears in the **valid** table (updates reason, no state flip); double-Accept on CustomerPO appears in the **invalid** table (Conflict, no no-op).

---

## 2. Edge case fill-in

The Wave-91 audit lists 35 Edge Case TCs. Wave 104 targets the highest-
value ones; the rest depend on modules that are still ❌ Missing.

| Edge # | Description | File | Notes |
|---|---|---|---|
| **#1** | Idempotent state-machine actions (double-Accept) | `internal/enterprise/usecase/idempotency_test.go` | **Load-bearing decision:** CustomerPO + IC-PO double-Accept = `Conflict` (NOT silent no-op). Settlement.Approve IS idempotent on `approved` per its own domain contract — documented inline. |
| **#2** | Parallel approval-decision concurrency | `internal/enterprise/usecase/parallel_approval_test.go` | Exercises `httpserver.WithAdvisoryLock` via `LockKeyForApproval`. DB-required; t.Skip on no DATABASE_URL. |
| **#4** | Parallel any-reject supersede | (pre-existing) | Already covered by Wave-67 `approval.go::SupersedeReset` tests. Not re-tested. |
| **#15** | Multi-capability sister classification | `internal/enterprise/adapter/http/contract_rbac_test.go` (4 new ClassifyActor rows) | vendor+reseller → vendor; vendor+sister → vendor; reseller+sister → reseller; triple-cap → vendor. Asserts "narrowest mask wins". |
| **#21** | Stale optimistic-lock retry | `pkg/httpserver/if_match_test.go::TestRequireIfMatch_StaleRetrySucceeds` | First PATCH with v0 → 412 stale; client re-reads + retries with v1 → 200. |

Out of scope (skipped — see audit doc for blockers):
- Edge #3 (reassign step) — no reassign endpoint
- Edge #5–14, #16–20, #22–29 — depend on missing IC-PO automation, faktur, reseller, compliance modules

---

## 3. If-Match optimistic-locking contract

Lives in `pkg/httpserver/if_match.go`.

### Wire format

- **Canonical:** `If-Match: W/"<row_version>"` (weak ETag per RFC 7232 §2.3)
- **Tolerated for curl ergonomics:** `If-Match: "<row_version>"` (bare quoted) or `If-Match: <row_version>` (unquoted token)
- **Response ETag (after a successful mutation):** `ETag: W/"<new_row_version>"` — handlers MUST set this. Use `httpserver.EtagFor(s)` to format.

### Status codes

| Condition | Status | Code |
|---|---|---|
| Header absent on PUT/PATCH/POST/DELETE | **428 Precondition Required** | `if_match.required` |
| Header present but stale (server version differs) | **412 Precondition Failed** | `if_match.stale` |
| Header present + matches | passes through, version stored in ctx via `IfMatchVersionFromContext` | — |
| URL `{id}` not a valid uuid | **400 Validation** | `<noun>.id_invalid` |
| Row-version lookup returns NotFound | **404** (passed through from `RowVersionFunc`) | (from lookup) |
| GET / HEAD / OPTIONS | passes through, header ignored | — |

### Usage

```go
boqHandler.Use(httpserver.RequireIfMatch(
    func(ctx context.Context, id uuid.UUID) (string, error) {
        return repo.RowVersion(ctx, id) // e.g. strconv.Itoa(boq.Revision)
    },
))
```

The Wave-104 deliverable provides the middleware + tests. Each enterprise
mutate route that opts in chains it ahead of its body — handler-by-handler
retrofit is intentionally **out of scope** to avoid touching ~50 routes
in one wave.

---

## 4. Advisory-lock helper

Lives in `pkg/httpserver/advisory_lock.go`.

### Contract

- `WithAdvisoryLock(ctx, pool, key, fn) error`
  - Acquires the postgres session-level advisory lock identified by `key` via `pg_try_advisory_lock` (non-blocking).
  - If the lock is held by another session, returns `derrors.Conflict("lock.contended", ...)`. The wrapped fn does NOT run.
  - On success, runs fn, then releases the lock via `pg_advisory_unlock` in a deferred call (fires on success OR error).
  - Pins the lock + unlock to a single pgx connection via `pool.Acquire` (advisory locks are per-session in postgres).
- `LockKeyForBOQ(uuid) int64` — derives the lock key from the first 8 bytes of the canonical UUID byte form.
- `LockKeyForApproval(uuid) int64` — same derivation; shares the lock namespace so two routes addressing the same approval id contend.

### Collision math

The working set of live approval flows is ~10^3. Birthday-bound on
2^63 → collision probability ~10^-13. If two unrelated keys ever collide,
the worst case is one caller observes spurious `lock.contended`; their
retry path resolves it.

---

## 5. Audit query + chain-verify API

Lives in `pkg/audit/postgres/reader.go` + `pkg/audit/http/audit_query_handler.go`.

### Endpoints

| Method | Path | Permission | Purpose |
|---|---|---|---|
| GET | `/api/audit/entries` | `identity.audit.read` | List entries by `subject_type` + `subject_id` + date range |
| GET | `/api/audit/chain/verify` | `identity.audit.read` | Walk rows by created_at ASC, recompute row_hash, return ChainVerifyResult |

Mounted by `cmd/identity-svc/main.go` — identity-svc is the canonical
home because `identity.audit_logs` lives in the identity schema.

### Query params — `/api/audit/entries`

| Param | Type | Notes |
|---|---|---|
| `subject_type` | string | maps to `record_type` column |
| `subject_id` | string | maps to `record_id` column |
| `module` | string | exact match |
| `user_id` | uuid | exact match |
| `from`, `to` | RFC3339 | inclusive bounds on `timestamp` |
| `limit` | int | default 50, max 500 |
| `offset` | int | default 0 |

### Response shapes

```json
GET /api/audit/entries
{
  "entries": [
    {
      "id": "...",
      "timestamp": "...",
      "user_id": "...",
      "module": "enterprise",
      "subject_type": "enterprise.customer_po",
      "subject_id": "...",
      "field_changed": "status",
      "before": "received",
      "after": "validated",
      "reason": "customer_po_validated",
      "prev_hash": "abcd...",
      "row_hash": "ef01..."
    }
  ],
  "count": 1
}
```

```json
GET /api/audit/chain/verify
{
  "verified": 12345,
  "broken": 0,
  "total": 12345,
  "first_broken_id": null
}
```

`first_broken_id` is populated when the first divergent row is encountered
in created_at ASC order. Backfilled rows pre-Wave-105 (where `row_hash=''`)
are SKIPPED — the chain starts from the first non-empty row.

### Hash recomputation

`pkg/audit/postgres/reader.go::computeRowHash` mirrors
`identity.compute_audit_hash` (migration 0070). The Go-side recompute is
**best-effort** for external sidecar consumers — the canonical truth lives
in postgres. Server-side proofs should call the SQL function directly.

---

## 6. Test counts at a glance

| Test surface | Files added/modified | New cases |
|---|---|---|
| Opportunity SM | 1 | 16 |
| BOQ SM | 1 | 18 |
| CustomerPO SM | 1 | 16 |
| IntercompanyPO SM | 1 | 18 |
| EWO SM | 1 | 13 |
| WholesaleOrder SM | 1 | 18 |
| Subscriber SM | 1 | 11 |
| MonthlySubmission SM | 1 | 18 |
| Settlement SM | 1 | 13 |
| Idempotency edge | 1 | 3 |
| Parallel approval edge (DB-skip) | 1 | 2 |
| Multi-cap sister classification | (modified existing) | 4 |
| If-Match middleware | 1 | ~12 (table + 6 named) |
| Advisory-lock helper | 1 | 7 (4 DB-skip + 3 standalone) |
| Audit query handler | 1 | 6 |
| **TOTAL** | **14 new + 2 modified** | **~175** |

---

## 7. Acceptance gates

```
cd backend && go build ./...     # exit 0
cd backend && go vet ./...       # exit 0
cd backend && go test -count=1 ./... # exit 0 (DB-required tests t.Skip on no DATABASE_URL)
```

All three pass at HEAD.

---

## 8. Follow-up parking lot

- **Handler retrofit for `If-Match`** — every enterprise mutate route should chain `httpserver.RequireIfMatch(repoRowVersion)` once a `RowVersion(ctx, id) (string, error)` shape exists on each repo. ~50 routes; not in Wave 104 scope.
- **Approval-decision usecase wiring of `WithAdvisoryLock`** — the helper lands; the actual `ApproveStep` / `RejectStep` usecase calls don't yet wrap themselves in the lock. The Wave 104 contract pin proves the lock works; the handler retrofit is the next session.
- **Server-side audit hash recompute via SQL function** — the Go-side `computeRowHash` exists for external sidecar consumers but the production verify path should call `identity.compute_audit_hash(...)` directly inside the SQL `VerifyChain` walk. Wave 105 added the function; consolidating verify to one source of truth is a small follow-up.
