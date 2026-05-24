# CS Bounded Context Migration Notes

## Wave 123 — Foundation Laid (this wave)

The customer-service bounded context now has a canonical home at
`internal/cs/` with the hexagonal layout the rest of the codebase
uses (domain / port / usecase / adapter/postgres / adapter/http / cron).

### What's new

- `cs.*` schema (6 tables) — see `migrations/0082_wave123_cs_foundation.up.sql`
- 7-state ticket lifecycle (vs. the legacy 5-state in `field.tickets.status`)
- Workflow-oriented `ticket_type` enum (technical / billing / complaint /
  service_request / information) — distinct from `field.tickets.category`,
  which is symptom-oriented (no_internet / slow_speed / …)
- @Mentions parser + `cs.ticket_mentions` table
- Channel registry seeded with 8 channels (portal, whatsapp, phone, email,
  walkin, agent_internal, api, tech_app)
- New `cs-svc` binary at `cmd/cs-svc/main.go`

### What's intentionally NOT done

**The legacy CS code stays live and untouched in this wave.** Specifically:

- `internal/field/adapter/http/phase2.go` — agent CRUD on `field.tickets`
  remains the production path until a future importer wave runs.
- `internal/crm/adapter/http/portal_auth.go` — customer-portal CS submit
  routes remain on `field.tickets` for backwards compatibility.
- The legacy `field.tickets` rows are not migrated. Pre-Wave-123 tickets
  stay readable on the old path; new tickets created via the cs-svc
  routes land in `cs.tickets`.

The two stores are intentionally divergent until the importer lands:

| Concern              | `field.tickets`                            | `cs.tickets`                                       |
|----------------------|--------------------------------------------|----------------------------------------------------|
| Status enum          | 5 states (no `assigned`, no `pending_internal`) | 7 states                                           |
| Type taxonomy        | `category` (symptom-oriented)              | `ticket_type` (workflow-oriented)                  |
| Channel attribution  | None                                       | `opened_via` enum + `cs.ticket_channels` registry  |
| @Mentions            | None                                       | `cs.ticket_mentions`                               |
| Pause semantics      | None                                       | `pause_seconds` + `paused_since` (SLA-on-pause)    |
| Timeline             | `field.ticket_messages` (mixed)            | Append-only `cs.ticket_events` + `cs.ticket_comments` |

## Future waves

### Wave 124 — SLA Matrix + Service Request Linkage

Will EXTEND `cs.tickets` via migration 0083:

- ADD COLUMN `sla_due_at TIMESTAMPTZ` — snapshot from the matrix at ticket open
- ADD COLUMN `sla_template_id UUID` — FK to the new `cs.sla_templates` table
- ADD COLUMN `sla_breached BOOLEAN NOT NULL DEFAULT FALSE`
- ADD COLUMN `sla_breached_at TIMESTAMPTZ`

The SLA evaluator will consume `Ticket.EffectiveAge(now)` (already
implemented here) to compute breach. `Ticket.AddPauseDuration` is the
extension hook for the pause-on-pending semantics.

### Wave 128D — Backfill importer ✅ shipped

Status: **CLOSED in Wave 128D.** Runs daily-ish via `TicketImporterTick`
(every 4h) in `internal/cs/cron/cron.go` and ad-hoc via
`POST /api/cs/importer/run` (gated on `cs.importer.run`, granted to
`super_admin` + `operations_admin`).

What ships:

1. Migration `0087_wave128d_ticket_importer.up.sql` — adds
   `cs.tickets.legacy_id UUID` + partial unique index. NULL is allowed
   so existing cs-native rows keep working without backfill.
2. `internal/cs/usecase/importer.go` — `TicketImporterService.RunOnce`.
   Anti-join select from `field.tickets`, per-row INSERT with
   `ON CONFLICT (legacy_id) DO NOTHING`. Idempotent on re-run.
3. `internal/cs/adapter/postgres/importer_repo.go` — pgxpool-backed
   `LegacyTicketReader` + `CanonicalImporterWriter`.
4. `internal/cs/adapter/http/handler_wave128d.go` —
   `POST /api/cs/importer/run` returns the `ImportSummary` as JSON.
5. Wiring in `cmd/cs-svc/main.go`.

Mapping rules (workflow-oriented `ticket_type` from symptom-oriented
`category`):

| Legacy category (actual + prompt-rich set)                           | Canonical ticket_type |
|----------------------------------------------------------------------|-----------------------|
| `no_internet`, `slow_speed`, `frequent_drops`, `equipment_damage`,   | `technical`           |
| `intermittent`, `signal_quality`, `hardware_failure`, `other`        |                       |
| `billing_dispute`, `invoice_dispute`, `payment_issue`, `refund`      | `billing`             |
| `service_quality`, `complaint`, `escalation`                         | `complaint`           |
| `cancellation`, `plan_change`, `address_change`                      | `service_request`     |
| `status_inquiry`, `info_request`                                     | `information`         |
| _anything else_                                                      | `technical` (default + warn log) |

Status mapping (legacy 5 → canonical 7):

| Legacy             | Canonical           |
|--------------------|---------------------|
| `open`             | `open`              |
| `in_progress`      | `in_progress`       |
| `pending_customer` | `pending_customer`  |
| `resolved`         | `resolved`          |
| `closed`           | `closed`            |

The two canonical-only states (`assigned`, `pending_internal`) have no
inbound mapping — agents hand-tune post-import if needed.

`ticket_no` is derived as `IMP-<legacy_ticket_number>` so the origin is
obvious in agent UIs. `source_metadata` carries
`{legacy_ticket_number, legacy_id, import_wave}` for trace-back.

After the importer has drained the legacy backlog, the legacy write
paths (`internal/field/adapter/http/phase2.go` + the portal CS submit
in `internal/crm/adapter/http/portal_auth.go`) can be cut over to
`cs.tickets` in a follow-up wave; the importer continues running so
any straggler writes during the cut-over window are reconciled within
4h.

### Future wave — Frontend cutover

`frontend/src/app/(dashboard)/admin/cs-tickets/page.tsx` currently
talks to the field.tickets API. The cutover will redirect the same UI
to the new `/api/cs/tickets` routes, then delete the legacy routes
and the legacy code in `field/adapter/http/phase2.go`.
