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

### Future wave — Backfill importer

A one-shot job will:

1. Read `field.tickets` ordered by `created_at`
2. Best-effort map `category` → `ticket_type` (e.g. `no_internet` →
   `technical`, `billing_dispute` → `billing`)
3. Insert into `cs.tickets` with `opened_via='portal'` for portal-origin
   rows, `opened_via='agent_internal'` for agent-on-behalf rows
4. Copy `field.ticket_messages` → `cs.ticket_comments` 1:1
5. Stamp `source_metadata.legacy_field_ticket_id` for traceback

After the importer runs, the legacy paths can be deprecated and removed
in a follow-up wave.

### Future wave — Frontend cutover

`frontend/src/app/(dashboard)/admin/cs-tickets/page.tsx` currently
talks to the field.tickets API. The cutover will redirect the same UI
to the new `/api/cs/tickets` routes, then delete the legacy routes
and the legacy code in `field/adapter/http/phase2.go`.
