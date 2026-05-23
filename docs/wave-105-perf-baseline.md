# Wave 105 — Performance Baseline + Audit Hash Chain

This wave wires three things end-to-end:

1. **Prometheus HTTP middleware** on every `*-svc` binary plus the
   api-gateway, recording `http_request_duration_seconds`,
   `http_requests_total`, `http_requests_in_flight`.
2. **Performance test harness** under `test/perf/` exercising the
   100k-line BOQ hydrate + recompute SLOs and the 500-concurrent
   list-read SLO.
3. **Audit append-only + hash chain** at the DB layer
   (migration `0070_wave105_audit_append_only.up.sql`).

Targets ~12 TCs (`TC-NFR-001..012`) from the Phase 1 Enterprise
audit catalog.

---

## SLO thresholds

| Test                                        | Metric        | Threshold |
| ------------------------------------------- | ------------- | --------- |
| `TestEnterpriseBOQ_100kLines_P95` — GET     | p95 latency   | < 2s      |
| `TestEnterpriseBOQ_100kLines_P95` — recompute | p95 latency | < 5s      |
| `TestEnterpriseBOQ_ConcurrentList_P99`      | 5xx / DB err  | 0         |
| `TestEnterpriseBOQ_ConcurrentList_P99`      | p99 latency   | < 1s      |

If the GH-hosted CI runner falls outside these numbers, treat that as
**regression first**, not "the runner is slow". The thresholds were
chosen against the audit doc's NFR table, and the CI fixture isolates
the perf test from other concurrent jobs by running on the
`boundary-regression.yml` nightly cron.

---

## Running locally

Requires a postgres reachable on `DATABASE_URL` with migrations
applied + the `seed-demo` reference data loaded:

```bash
cd backend
go run ./cmd/seed-demo
go test -tags=perf -count=1 -timeout=20m ./test/perf/...
```

When `DATABASE_URL` is empty the tests `t.Skip` cleanly — they never
fail just because the env isn't wired. This matches the convention
already in place for the existing perf tests
(`bulk_invoice_test.go`, `coverage_test.go`, `gps_streaming_test.go`).

---

## Prometheus metrics

Each service exposes `/metrics` on the same port as `/healthz`. Labels
are intentionally low-cardinality:

- `service`     — the binary's name (`enterprise-svc`, `crm-svc`, …)
- `route`       — the chi route pattern, e.g. `/api/enterprise/boqs/{id}`
                  (NOT the concrete URL — that would explode cardinality
                   per BOQ id)
- `method`      — `GET`, `POST`, etc.
- `status`      — HTTP status as a string (`200`, `404`, `500`, …)

Histogram buckets: `[0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5,
5, 10]` seconds. Chosen to cover the realistic spectrum of an internal
HTTP service (5ms cache hits → 10s timeout ceiling).

**Dashboards** (ops-prom-grafana repo, not in-tree): the existing
"Service Health" dashboard already groups by `service` + `route`, so
no Grafana migration is needed — just refresh the panels once the
metrics start flowing.

---

## Audit hash chain — contract

After migration `0070_wave105_audit_append_only.up.sql`:

- `identity.audit_logs` carries `prev_hash TEXT NOT NULL DEFAULT ''`
  and `row_hash TEXT NOT NULL DEFAULT ''`.
- A `BEFORE INSERT` trigger fills both:
  - `prev_hash` ← latest existing row's `row_hash`, or `''` if this
    is the first row.
  - `row_hash`  ← `compute_audit_hash(id, prev_hash, payload)`
- `compute_audit_hash(id, prev_hash, payload)` is
  `sha256-hex(prev_hash || '|' || payload::text)`, marked IMMUTABLE.
- `payload` is built inside the trigger from the row's columns —
  `user_id`, `module`, `record_type`, `record_id`, `field_changed`,
  `before_value`, `after_value`, `reason`, `timestamp`. The build
  is a `jsonb_build_object` so column order doesn't matter (JSON
  semantics).

### Contract for verifiers

> Given any row in `identity.audit_logs`, its `row_hash` MUST equal
> `compute_audit_hash(id, prev_hash, payload)` where `payload` is
> the canonical jsonb_build_object of the columns listed above.
> If `row_hash` doesn't match, **the chain is broken** — either the
> row was tampered with, or `prev_hash` was set to something other
> than the prior row's `row_hash`.

To verify the whole chain (auditor tool, not in the app):

```sql
SELECT id, row_hash,
       identity.compute_audit_hash(id, prev_hash,
           jsonb_build_object(
               'user_id',       user_id,
               'module',        module,
               'record_type',   record_type,
               'record_id',     record_id,
               'field_changed', field_changed,
               'before_value',  before_value,
               'after_value',   after_value,
               'reason',        reason,
               'timestamp',     timestamp
           )) AS recomputed,
       row_hash = identity.compute_audit_hash(...) AS chain_ok
FROM identity.audit_logs
ORDER BY timestamp;
```

### Append-only enforcement

A second trigger, `enforce_audit_append_only`, raises EXCEPTION
`42501` on any `BEFORE UPDATE OR DELETE` against `identity.audit_logs`.
This is the load-bearing protection — the role-level `REVOKE UPDATE,
DELETE ON identity.audit_logs FROM PUBLIC` is defense in depth but
no-ops when the app role is also the postgres superuser (as in CI
+ local dev).

### Existing pre-Wave-105 rows

The migration leaves existing rows with `prev_hash=''` and
`row_hash=''` rather than back-filling. Back-filling would require
choosing a serialization order across millions of historical rows;
the chain starts from **new inserts forward**. Pre-Wave-105 rows
are explicitly out of the chain's scope — they pre-date the
contract.

---

## CI surface

Two workflows touch this area:

- `ci.yml` (existing) — runs the regular `go test ./...` which
  excludes `-tags=perf`, so the perf tests don't slow PRs down.
- `boundary-regression.yml` (new, this wave) — daily cron at 02:00 UTC,
  runs the perf suite + every `TestBoundary*` test across `internal/`
  + `test/boundary/`. Posts a status check; does NOT block PRs
  (only fires on `schedule` / `workflow_dispatch`).

When the daily cron starts failing, the workflow's logs are the
first place to look — the perf test logs every individual latency
sample plus the computed p95/p99 so it's straightforward to tell
"a single regression" from "everything got slow."
