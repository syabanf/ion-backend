# ion-backend

The Go backend that powers the ION Network ISP management platform.
Eight micro-services behind an API gateway, sharing one PostgreSQL
cluster and one schema lineage of 47+ migrations.

Part of a 5-repo system:

| Repo | What it is |
|---|---|
| **ion-backend** (this) | Go services, migrations, e2e suite |
| [ion-frontend](https://github.com/syabanf/ion-frontend) | Next.js admin dashboard |
| [ion-customer-app](https://github.com/syabanf/ion-customer-app) | Flutter customer portal |
| [ion-sales-app](https://github.com/syabanf/ion-sales-app) | Flutter sales-rep app |
| [ion-tech-app](https://github.com/syabanf/ion-tech-app) | Flutter technician app |

---

## Tech stack

| Layer | Choice | Why |
|---|---|---|
| Language | **Go 1.22** | Compile-time safety + fast cold start, fits the per-service deployment story |
| HTTP | **go-chi/chi v5** | Lightweight router with middleware composition, no framework lock-in |
| DB | **PostgreSQL 16** (postgis/postgis image for geo) | Single source of truth; PostGIS for coverage polygons |
| DB driver | **jackc/pgx v5** + `pgxpool` | Native PG protocol, faster than database/sql |
| Auth | **JWT** (HS256), per-service `RequireAuth` + `RequirePermission` middleware | Stateless, gateway-validated, scoped permissions |
| Migrations | Plain `*.up.sql` / `*.down.sql` in `migrations/` | Reviewable, replayable, no runtime ORM magic |
| Logging | `log/slog` (structured) | Stdlib, JSON output by default |
| Tests | `go test` for unit, `-tags=e2e` for integration, `-tags=perf` for load | Tag-gated so PR CI stays fast |
| Lint | `golangci-lint` (errcheck + staticcheck + unused + ineffassign) | Strict, baseline-triaged |

### Services

| Service | Port (default) | Domain |
|---|---|---|
| `api-gateway` | 8080 | Routing + CORS, no DB |
| `identity-svc` | 8081 | Users, roles, permissions, branches, audit log, platform config |
| `crm-svc` | 8083 | Leads, customers, products, portal OTP + tickets |
| `billing-svc` | 8084 | Invoices, payments, cycles, terminations, webhook deliveries |
| `network-svc` | 8085 | Nodes, ODPs, topology, RADIUS accounts |
| `warehouse-svc` | 8086 | Catalog, intake, opname, WO dispatch |
| `field-svc` | 8087 | Work orders, BAST, checklists, journey, priority insertion |
| `enterprise-svc` | 8088 | Phase-2 CPQ (BOQ, RFQ, EWO, projects) |

Route table lives in `cmd/api-gateway/main.go`. The gateway strips the
`/api/<bounded-context>` prefix on the way to the downstream service.

---

## Quick start

### Prerequisites

- Go 1.22+
- Docker (for the bundled Postgres) **or** a local PG 16 + PostGIS
- `psql` client
- `make` (optional — most workflows use plain `go test`)

### Boot Postgres

```bash
docker compose up -d postgres
# Or against a local pg:
# createdb ion_core && psql ion_core -c "CREATE EXTENSION postgis;"
```

Default credentials in `docker-compose.yml`:
- user: `ion`
- pass: `ion_dev_password`
- db: `ion_core`
- port: `5432`

### Run migrations

```bash
export DATABASE_URL="postgres://ion:ion_dev_password@localhost:5432/ion_core?sslmode=disable"
for f in migrations/*.up.sql; do
  psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f "$f"
done
```

Migration `0047` is the production master-data seed (RBAC permissions
+ roles + role_permissions + default HQ branch + BB-* products +
approval/EWO templates). Idempotent — safe to re-run.

### Seed a demo user set (12 accounts, one per role)

```bash
go run ./cmd/seed-demo
# Shared password: IonDemo!2026Tour
# Emails: ops@, sales@, tech@, noc@, ... @ion.local
```

### Boot the stack

```bash
export JWT_SECRET="dev-secret-do-not-use-in-prod-32+chars"
export JWT_ISSUER="ion-core-dev"
export KTP_ENC_KEY=$(openssl rand -hex 32)
export CRM_PORTAL_OTP_DEMO=true   # surfaces OTP in API responses for local dev
export PORTAL_DEV_OTP=true

# In separate terminals (or use a process manager):
go run ./cmd/identity-svc
go run ./cmd/crm-svc
go run ./cmd/billing-svc
go run ./cmd/network-svc
go run ./cmd/warehouse-svc
go run ./cmd/field-svc
go run ./cmd/enterprise-svc
go run ./cmd/api-gateway
```

Visit `http://localhost:8080/healthz` to confirm the gateway responds.
Each downstream service exposes its own `/healthz`.

### Log in

```bash
curl -X POST http://localhost:8080/api/identity/auth/login \
  -H "content-type: application/json" \
  -d '{"email":"ops@ion.local","password":"IonDemo!2026Tour"}'
```

Returns `{"access_token": "...", ...}`. Pass that as
`Authorization: Bearer <token>` on subsequent calls.

---

## Project structure

```
backend/
├── cmd/                          # One main.go per binary
│   ├── api-gateway/              # Reverse proxy + auth
│   ├── identity-svc/             # Users, roles, branches, audit
│   ├── crm-svc/                  # Leads, customers, portal
│   ├── billing-svc/              # Invoices, payments, cycles
│   ├── network-svc/              # Nodes, ODPs, RADIUS
│   ├── warehouse-svc/            # Catalog, dispatch, opname
│   ├── field-svc/                # WOs, BAST, checklists
│   ├── enterprise-svc/           # CPQ (BOQ, RFQ, EWO)
│   ├── seed-demo/                # 12-user demo seed
│   ├── seed-referrals/           # CS referral demo data
│   └── seed-checklists/          # WO checklist templates
├── internal/
│   ├── identity/                 # Hexagonal: domain/port/adapter/usecase
│   ├── crm/                      #   …same per bounded context
│   ├── billing/
│   ├── network/
│   ├── warehouse/
│   ├── field/
│   └── enterprise/
├── pkg/                          # Shared, importable
│   ├── auth/                     # JWT issue/verify, password hash (bcrypt)
│   ├── httpserver/               # chi setup, RequireAuth, error envelopes
│   ├── notifyx/                  # Outbox dispatcher (FCM/APNS/WA stubs)
│   ├── webhookx/                 # Webhook outbox + retry
│   ├── ratelimit/                # IP-scoped rate limiter
│   └── config/                   # env-var loader
├── migrations/                   # 0001 → 0047 (.up.sql + .down.sql)
├── test/
│   ├── e2e/                      # //go:build e2e — full HTTP integration
│   ├── perf/                     # //go:build perf — load tests
│   ├── rbac/                     # role permission audit
│   └── skip-baseline.txt         # CI-enforced t.Skip count ceiling
├── docs/openapi.yaml             # OpenAPI 3.1 (strict drift check in CI)
└── docker-compose.yml            # Postgres for local dev
```

### Hexagonal layout

Each `internal/<ctx>/` follows:

```
internal/crm/
├── domain/        # Entities + business rules; no I/O
├── port/          # Repository + usecase interfaces; no impl
├── usecase/       # Pure business orchestration (Service struct)
└── adapter/
    ├── http/      # chi handlers, DTOs
    └── postgres/  # pgxpool repos implementing the ports
```

Test order: unit-test domain + usecase against mocks (`mocktail`-style
no — just hand-rolled fakes implementing the port interfaces); HTTP
adapter tests live under `test/e2e/`.

---

## Testing

| Suite | Command | Speed |
|---|---|---|
| Unit | `go test ./...` | ~10s |
| E2E (needs Postgres + booted stack) | `go test -tags=e2e ./test/e2e -v -timeout=10m` | ~3-5min |
| Cron workers | `go test -tags=e2e ./internal/enterprise/cron -v` | ~2min |
| Notifyx outbox | `go test -tags=e2e ./pkg/notifyx -v` | ~1min |
| Performance | `go test -tags=perf ./test/perf -v` | ~5min |
| Migration smoke | `for f in migrations/*.up.sql; do psql $DATABASE_URL -f $f; done` | ~20s |

The **34 E2E test files** under `test/e2e/` walk the full Phase 1 +
Phase 2 surface — broadband happy path, voluntary + auto termination,
suspension → restoration, plan upgrade, BAST rejection, warehouse
opname + dispatch, audit log capture, cross-surface mobile↔dashboard,
and more.

### Skip baseline ratchet

`test/skip-baseline.txt` pins the maximum number of `t.Skip` calls.
Adding new skips without bumping the file fails CI — keeps coverage
from rotting silently.

---

## CI

`.github/workflows/ci.yml` runs on every PR + main push:

- PR: backend unit + golangci-lint, build all binaries, migration smoke
  with master-data assertions, boot smoke (all 8 services + /healthz),
  E2E (light leg) + Cypress functional smoke
- Main: full bash all-e2e.sh + schema-e2e.sh, cron worker e2e, notifyx
  e2e

---

## Where this fits

The backend serves three mobile apps and one dashboard:

```
                          ┌─ ion-frontend (Next.js dashboard, port 3000)
                          │
  ion-backend (gateway     ├─ ion-customer-app (Flutter portal, port 9100)
  on :8080) ──── /api/* ──┤
                          ├─ ion-sales-app (Flutter sales, port 9101)
                          │
                          └─ ion-tech-app (Flutter technician, port 9102)
```

All four clients hit `/api/*` via the gateway, plus the customer portal
hits `/portal/*` (no prefix-strip — same shape on gateway + crm-svc).

---

## Contributing

- Branch from `main`, open a PR
- CI must be green; new `t.Skip` requires `test/skip-baseline.txt` bump
- Migrations: add a numbered pair (`0048_x.up.sql` + `0048_x.down.sql`)
  — both must exist, and the migration-smoke CI step round-trips
  up → down → up
- Permissions: dashboard call sites grep `Can permission=` and
  `RequireAuth permission=` — keep those in lockstep with
  `internal/identity/.../seed*.sql` + the Wave 47 master-data migration
