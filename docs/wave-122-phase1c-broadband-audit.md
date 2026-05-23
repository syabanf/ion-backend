# Wave 122 — Phase 1C Broadband QA Audit & Compliance Roadmap

**Date:** 2026-05-23
**Source catalog:** `ION-Phase1C-Broadband-Test-Cases-ID.xlsx` → exported to `/tmp/p1c-catalog.csv` (838 rows, 82 modules)
**Builds on:** Waves 110–120 (Phase 1B audit + closeout, 713 TCs to 100% testable) and Wave 121E (production-wiring readiness).
**Code state audited:** `backend/internal/{identity,crm,platform,field,network,billing,warehouse,operations,tax,reseller,partnership,vendormgmt,enterprise,payment,nocmon,netdevices,invoicesvc,hris}/` + adjacent at HEAD (post Wave 121E, migrations 0001..0081).
**Distinct from:** Wave 110 (Phase 1B Broadband audit, 713 TCs across 63 modules). Phase 1C **layers 125 net-new TCs across 19 net-new modules on top of the 713 carry-over** — total 838.

---

## Exec summary

This audit covers the **Phase 1C Broadband enhancement tranche** — the third delivery slice of the broadband product line after the foundation (Phase 1, Waves 1-90) and the operationalization (Phase 1B, Waves 110-120). The Phase 1C scope is **deliberately narrow**: 19 new modules at the operations / customer-service layer that build on the now-complete data foundation.

| Tranche | Scope | TC count |
|---|---|---:|
| **Phase 1 + Phase 1B carry-over** (regression suite — already closed by Waves 1-120) | All 63 modules from `wave-110` catalog: foundation + billing + warehouse + payment-svc + invoice-svc + NOC + NDL + HRIS + Deep Schemas | **713** |
| **Phase 1C — Operations enhancements** | Planned Maintenance, Maintenance Escalation, Bulk Plan Change, Bulk ODP Migration, Bulk WO Creation, Operational Calendar, Internal Announcements, Cross-Module SLA Ops View | **47** |
| **Phase 1C — Customer Service module (entirely new)** | Ticket Types, Ticket Lifecycle, Ticket Channels, Priority & SLA, Team Assignment, @Mentions, WO from Ticket, Service Requests, Communication, CSAT, CS Dashboards | **78** |
| **Total** | | **838** |

| Metric | Value |
|---|---:|
| Total TCs | **838** |
| P0 — Kritikal | **648** (77.3%) |
| P1 — Tinggi | **173** (20.6%) |
| P2 — Sedang | **17** (2.0%) |
| Modules | **82** |
| **Net-new Phase 1C TCs** | **125** (109 P0 + 16 P1 + 0 P2) |
| Net-new modules | **19** |
| Carry-over coverage at HEAD | **100% testable** per Wave 120 closeout |
| Phase 1C coverage at HEAD | **~25% (skeleton only)** — see §3 |

### Phase 1C coverage roll-up

| Bucket | Modules | TC count | Notes |
|---|---:|---:|---|
| **Mostly covered** ✅ | 3 | 18 (14%) | Bulk Plan Change / Bulk ODP Migration / Bulk WO Creation (all 3 share the `operations.bulk_operations` table + handler shipped in Wave 71 / migration 0048; happy-path CRUD works; per-kind executor + approval-threshold + dry-run gates are partial — see §3) |
| **Partial** 🟡 | 8 | 50 (40%) | Planned Maintenance (schema in 0036 + handler in `field/phase2.go`), Internal Announcements (table + handler in 0048), Operational Calendar (cross-module read in `operations/handler.go`), Cross-Module SLA Ops View (counters in `operations.slaDashboard`), Ticket Types (5-category enum + WO bridge), Ticket Lifecycle (5-state SM + CSAT score persistence), WO from Ticket (`equipment_damage` auto-WO already wired), Service Requests (plan-change / relocation / addon-buy / termination portal routes all exist) |
| **Missing** ❌ | 8 | 57 (46%) | Maintenance Escalation (war-room hook exists; overrun-detect + auto-flag missing), Ticket Channels (no `channel` column; portal-only today), Priority & SLA (no per-customer-type × per-ticket-type matrix; no `first_response_at`; no pause-on-pending), Team Assignment (no auto-route / round-robin / queue), @Mentions (zero — no comment-layer mention parser), Communication (no WhatsApp template registry; no per-customer notification preference), CSAT (score persisted but no aggregation / low-score alert / per-agent report), CS Dashboards (no agent queue view / supervisor team-SLA view / escalation queue) |

**Headline gap:** Phase 1C is **deceptively small in TC count** (125 / 838 = 15%) but operationally significant because it adds the **Customer Service module — 78 TCs across 11 sub-modules** — as a first-class concern. The data foundation has been quietly accumulating since Wave 36 (`phase2_foundation` migration created `field.tickets` + `field.maintenance_events` + `crm.product_addons` + `crm.plan_change_requests` + `crm.customer_relocations`), Wave 38 added `field.ticket_messages` with internal-note + attachments, and Wave 71 (migration 0048) added the entire `operations` schema. **The skeletons are there. What's missing is the cross-cutting CS logic that elevates them from a ticketing table to a Customer Service module.**

Three structural absences dominate:

1. **No Customer Service bounded context (`internal/cs/`).** CS logic is scattered: ticket CRUD lives in `internal/field/adapter/http/phase2.go` next to maintenance, customer-facing flows live in `internal/crm/adapter/http/portal_auth.go`. There's no service-level SLA evaluator, no ticket assignment usecase, no mention parser, no CSAT aggregator. To deliver the 78 CS TCs cleanly, either: (a) extract a `internal/cs/` package and migrate the existing handler + portal routes into it, or (b) extend `internal/field/` with explicit `cs_*` sub-packages. The audit recommends (a) — see §4.1.

2. **Maintenance Window vs CS Service Request are different workflows.** `field.maintenance_events` covers **operator-initiated outages** (fiber upgrade, OLT swap, planned cuts) with affected-customer auto-detection from network topology. `crm.plan_change_requests` + `customer_relocations` + buy-addon flows cover **customer-initiated service requests**. The Phase 1C catalog treats CS Service Requests (12 TCs) as a **ticket sub-type backed by the existing CRM flow tables** — meaning when a customer submits "I want to upgrade my plan", a `field.tickets` row + a `crm.plan_change_requests` row are created atomically. No new domain is needed; the linkage is.

3. **Bulk Ops infrastructure is generic and reusable, but the per-kind executors are stubs.** `operations.bulk_operations` (Wave 71, migration 0048) has a state machine (draft → previewed → approved → completed), approval gates, audit hooks, and a `payload jsonb` for per-kind config. The Wave 71 `executeBulkOp` handler is currently a no-op marker — it updates status but doesn't actually apply the per-row changes. Phase 1C's 18 Bulk TCs need: (a) `plan_change` executor (updates `crm.customers.plan_id` + emits to RADIUS), (b) `odp_migration` executor (updates `network.ports.assigned_customer_id` + RADIUS profile path), (c) `wo_create` executor (atomic batch insert into `field.work_orders`). The framework is right; the executors are missing.

---

## Carry-over health check (713 TCs)

Per `wave-120-100pct-broadband-compliance-report.md` (Wave 120 closeout, 2026-05-23):

| Source | Count | Status |
|---|---:|---|
| Direct backend tests (`✅`) | **657** | 100% testable via `go test ./...` |
| Frontend dashboards (`⏸️ Wave 119 closed`) | **36** | All 36 frontend-blocked TCs were closed by the Wave 119 dashboard delivery (Next.js: invoice monitoring, NOC live dashboard, payment ops, deep schema editors, NDL ops). Frontend tests are visual + flow-driven via Wave 119 routes. |
| Manual QA / third-party integration (`📋`) | **20** | DJP sandbox-environment integration (3 TCs — TC-FPJ-001/002/003), payment gateway live verification (5 TCs across Xendit / Midtrans / Stripe / BCA H2H), RADIUS server failover physical-cable test (TC-RAD-012), sealed-password rotation runbook (TC-RAD-016), SNMP polling against real OLT (~6 TCs in NOC Service Monitoring), real OLT firmware push (~2 TCs in NDL), WhatsApp template approval (~3 TCs in Reminder Schedule). |
| **Total testable** | **713 / 713 (100%)** | Per Wave 120 §3a module table |

**Drift since Wave 120:** None observed. Migration count at HEAD is **0081** (= `0081_wave118_hris.up.sql`). No business `.go` files changed between Wave 120 closeout and Wave 122 audit. Wave 121E shipped only test files + the production-wiring-readiness doc; it did not regress any Wave 120 coverage.

**One real-world flip that affects Wave 122 planning:** Wave 121E flagged that the **HRIS gateway env flag is quietly broken** — `HRIS_GATEWAY_ENABLED` is read by `internal/hris/usecase/sync.go` but the stub adapter is still wired in `cmd/hris-svc/main.go` even when the flag is set. This is a real-third-party integration concern for Phase 1C only insofar as **Phase 1C @Mentions and Team Assignment route to identity users**, and stale HRIS data could route a mention to a resigned user. The audit recommends Phase 1C wave plan addresses this in Wave 127 (CS Dashboards + Polish) along with the HRIS sync remediation.

The carry-over health is **green** — no Phase 1C wave needs to fix the foundation.

---

## Per-module audit of the 19 NEW Phase 1C modules

### Operations enhancements — 47 TCs

#### Planned Maintenance — 10 TCs (P0:10)

**Scope (ID):** Ops Admin membuat maintenance event (fiber_upgrade / olt / odp / backbone / config); auto-detect affected customers downstream dari affected_nodes; approval threshold per impact size (NOC Manager + Ops Admin joint approval kalau >100 customers, Ops Admin solo kalau <100); customer notification ≥24h sebelum start (broadband) / ≥72h (enterprise/corporate); maintenance WO auto-spawn per affected node; status transitions planned → dispatched → in_progress → completed.

**Existing backend artefacts:**
- `internal/field/adapter/http/phase2.go::listMaintenanceEvents / getMaintenanceEvent / createMaintenanceEvent / dispatchMaintenanceEvent` (handler shipped at Wave 36)
- `field.maintenance_events` table (migration 0036) — `event_kind ∈ {preventive, corrective, planned_outage}`, status SM, branch_id, assigned_team_id
- `field.maintenance_event_nodes` (migration 0036) — per-event node touchpoints
- `field.work_orders.maintenance_event_id` soft FK + `maintenance_subtype` column (migration 0036)
- `internal/field/adapter/http/phase2.go::dispatchMaintenanceEvent` already spawns maintenance WOs per affected node (line ~423 of phase2.go)

**Existing frontend artefacts:**
- `frontend/src/app/(dashboard)/admin/maintenance/page.tsx` (360 LOC) — list + detail (Wave 67/D7)
- `frontend/src/app/(dashboard)/operations/calendar/page.tsx` (148 LOC) — unified calendar including maintenance

**Existing mobile artefacts:** `mobile/tech_app/lib/features/field/` exercises dispatch + maintenance WOs

**Gap status:** 🟡 Partial — happy path good; PRD §6.3 approval-threshold + customer-notification-lead-time + affected_customers materialization absent

**Top gaps:**
- TC-PM-002 auto-detect affected_customers from network topology cascade — `maintenance_event_nodes` captures nodes but no usecase walks `network.ports` → `crm.customers` to populate an `affected_customers` materialized list at event-create time.
- TC-PM-003 approval threshold (>100 customers → NOC Manager + Ops Admin joint) — `maintenance_events.status` jumps planned → dispatched with no approval gate.
- TC-PM-004 customer notification ≥24h (broadband) — no notification cron reading `scheduled_start - 24h` and dispatching via `notifyx`.
- TC-PM-005 enterprise/corporate ≥72h — no per-customer-type lead-time override.
- TC-PM-009 reschedule notification — no re-notify when `scheduled_start` is updated.

---

#### Maintenance Escalation — 4 TCs (P0:3 / P1:1)

**Scope (ID):** Maintenance in_progress past scheduled_end + tolerance (default 30 min) → auto-flag overrun, alert Ops + NOC Manager; manual escalate to War Room → incident auto-created dengan inherits affected_areas + affected_nodes + affected_customers + timeline; Phase 1C limited to escalation hook (full War Room UI is Phase 2C).

**Existing backend artefacts:**
- `internal/operations/adapter/http/handler.go::escalateMaintenance` (Wave 71) — accepts `reason` + optional `war_room_incident_id`, stamps `escalated_to_war_room_at` + `escalation_reason` columns on `field.maintenance_events`
- `field.maintenance_events.{escalated_to_war_room_at, war_room_incident_id, escalation_reason}` columns (migration 0048)

**Existing frontend artefacts:** Escalate-button surfaced in `admin/maintenance/page.tsx` (Wave 67)

**Gap status:** ❌ Missing the **overrun-detect cron** — manual escalation works, automatic doesn't

**Top gaps:**
- TC-MES-001 overrun auto-detect cron — no scheduled job reading `WHERE scheduled_end + interval '30 min' < NOW() AND status='in_progress'` → emit `maintenance.overrun` alert.
- TC-MES-003 incident inherits event data — `escalateMaintenance` only persists `war_room_incident_id` as a soft pointer; no payload-snapshot of affected_areas / nodes / customers / timeline at escalate time.
- TC-MES-004 schema-ready Phase 1 confirmation — needs a documented test that the schema accepts the inherited fields without the Phase 2C war-room domain present.

---

#### Bulk Plan Change — 8 TCs (P0:8)

**Scope (ID):** Ops define bulk plan change (source plan → target plan, customer filter by branch/cohort); preview menampilkan affected count + MRC delta + revenue impact + per-branch breakdown; upgrade → Finance Manager approval; downgrade → Finance Manager + Management joint approval; execute atomic per customer (billing schema + RADIUS profile + next cycle), failure isolation (one failure tidak gagalkan batch); audit per customer; idempotent re-run safe; rollback path per customer.

**Existing backend artefacts:**
- `internal/operations/adapter/http/handler.go::listBulkOps / createBulkOp / previewBulkOp / approveBulkOp / executeBulkOp` (Wave 71)
- `operations.bulk_operations` table (migration 0048) — state SM `draft → previewed → approved → completed`, `op_kind ∈ {plan_change, odp_migration, wo_create}`, `payload jsonb`, `preview_summary jsonb`, `execution_journal jsonb`

**Existing frontend artefacts:**
- `frontend/src/app/(dashboard)/operations/bulk/page.tsx` (282 LOC) — list + create + preview + approve UI

**Gap status:** 🟡 Partial — framework complete; **per-kind executor is a no-op marker**

**Top gaps:**
- TC-BPC-005 execute atomic per customer — `executeBulkOp` updates status to 'completed' without actually applying changes. Needs a `plan_change_executor` usecase that iterates `payload.customer_ids`, updates `crm.customers.plan_id`, pushes RADIUS profile via `internal/network/adapter/radius/`, emits per-customer audit, and accumulates results into `execution_journal`.
- TC-BPC-003/004 upgrade vs downgrade approval threshold — `approveBulkOp` accepts any user with `operations.bulk.approve`; no joint-approval lifecycle (needs 2-of-2 approvers for downgrade).
- TC-BPC-002 preview revenue-impact computation — `previewSummary` returns `affected_count` + `to_product_id` only; no MRC delta calculation across the impacted customers.
- TC-BPC-006 failure isolation — current executor has no per-customer retry budget; one failure would taint the whole batch row.
- TC-BPC-008 rollback path per customer — no `execution_journal` rollback semantics.

---

#### Bulk ODP Migration — 6 TCs (P0:6)

**Scope (ID):** Ops define ODP-X → ODP-Y migration; capacity validation (ODP-Y available ports >= affected customers); per-customer reassign (RADIUS profile path updated, no service interruption); flag field_work_required → auto-create Maintenance WO per customer (fiber re-splice); audit per migration with source ODP/port → target ODP/port; idempotent re-run safe.

**Existing backend artefacts:**
- Same `operations.bulk_operations` framework as Bulk Plan Change with `op_kind='odp_migration'`
- `network.ports` table (migration 0004) + `network.nodes` — has port-to-customer linkage
- `field.work_orders.maintenance_subtype = 'odp_migration'` (would need adding to CHECK constraint; current values per migration 0036 do not enumerate it)

**Existing frontend artefacts:** Reuses `operations/bulk/page.tsx`

**Gap status:** ❌ Missing — same framework as Plan Change, but the ODP-specific capacity validator + RADIUS path update don't exist

**Top gaps:**
- TC-BOM-002 capacity validation — `previewSummary` for `odp_migration` returns `to_odp_id` + `affected_count` only; no `network.ports` lookup that asserts available ports >= affected count.
- TC-BOM-003 per-customer port reassign atomicity — no executor walks `network.ports`, updates `assigned_customer_id`, pushes new RADIUS profile path.
- TC-BOM-004 field_work_required → auto Maintenance WO — no flag interpretation; no WO producer.
- TC-BOM-005 audit per customer with source/target port — no audit subject `network.port.reassigned` with both pairs in payload.

---

#### Bulk WO Creation — 4 TCs (P0:3 / P1:1)

**Scope (ID):** Ops upload CSV of customer IDs + WO type → bulk WO creation; per-row validation (customer exists, eligible for WO type, e.g. termination only for active customers); concurrent WO block (customer with open WO + bulk for same customer → flagged, not duplicated); dashboard track progress per Team Leader.

**Existing backend artefacts:**
- Same framework with `op_kind='wo_create'`, `payload.customer_ids[] + wo_kind`

**Existing frontend artefacts:** Reuses `operations/bulk/page.tsx`

**Gap status:** ❌ Missing the executor + per-row validator

**Top gaps:**
- TC-BWO-001 mass WO from customer list — `executeBulkOp` for `wo_create` is a no-op; no atomic batch insert into `field.work_orders`.
- TC-BWO-002 per-row validation (customer eligible for WO type) — no validator in `previewSummary`.
- TC-BWO-003 concurrent WO block — no `WHERE NOT EXISTS (SELECT 1 FROM field.work_orders WHERE customer_id = $1 AND status NOT IN ('completed','cancelled'))` guard.
- TC-BWO-004 per-Team-Leader progress dashboard — `execution_journal` returns nothing today.

---

#### Operational Calendar — 6 TCs (P0:4 / P1:2)

**Scope (ID):** Unified calendar view (maintenance + bulk + announcement) filterable by branch / status; auto-add on event approval; conflict detection on same-region maintenance overlap; date range views (day/week/month/quarter); iCal export → Google/Outlook sync; permission-scoped (Ops Admin sees all; Branch Manager sees own branch only).

**Existing backend artefacts:**
- `internal/operations/adapter/http/handler.go::calendarFeed` (Wave 71) — unified read across `field.maintenance_events` + `operations.bulk_operations` + `operations.internal_announcements`

**Existing frontend artefacts:**
- `frontend/src/app/(dashboard)/operations/calendar/page.tsx` (148 LOC) — day/week/month view

**Gap status:** 🟡 Partial — happy-path calendar feed works; filter / conflict / iCal export missing

**Top gaps:**
- TC-OPC-003 conflict detection on overlap — no overlap-detect logic in `createMaintenanceEvent`; no warning surface.
- TC-OPC-005 iCal export — no `GET /operations/calendar.ics` route; no RFC 5545 encoder.
- TC-OPC-006 permission-scoped view — `calendarFeed` returns all events; no per-branch claim filter applied.
- TC-OPC-004 date range navigation — frontend page accepts the view but backend `calendarFeed` does not accept `?from=&to=` query params; returns last 200 maintenance + 200 bulk + 100 announcements ordered by created_at.

---

#### Internal Announcements — 5 TCs (P0:4 / P1:1)

**Scope (ID):** Ops Admin create announcement (title, body, urgency info/important/urgent, target all_staff / role-specific / branch-specific); multi-channel delivery (in-app + email; urgent also SMS) within 60s; urgent requires acknowledge (per §11 Q3); audit with actor + target + dispatch count + read receipts; use cases (outage internal, policy update, on-call assignment).

**Existing backend artefacts:**
- `internal/operations/adapter/http/handler.go::listAnnouncements / createAnnouncement` (Wave 71)
- `operations.internal_announcements` table (migration 0048) — `severity ∈ {info, warning, critical}`, `targeting jsonb`, `channels jsonb` (default `["push"]`), `scheduled_at`, `sent_at`, `sent_count`

**Existing frontend artefacts:**
- `frontend/src/app/(dashboard)/operations/announcements/page.tsx` (223 LOC) — create + list

**Gap status:** 🟡 Partial — create + list shipped; **dispatcher + read-receipt tracking missing**

**Top gaps:**
- TC-ANN-002 multi-channel dispatch within 60s — handler inserts row with `sent_at=NULL`; no `notifyx`-driven dispatcher cron consumes the row.
- TC-ANN-003 urgent acknowledgement — no `announcement_receipts` table; no acknowledge endpoint.
- TC-ANN-005 use case templates — no template registry; every announcement is free-text.
- TC-ANN-001 severity mismatch — backend CHECK uses `info|warning|critical` (per migration 0048) but the PRD scope says `info|important|urgent`. The frontend page uses `info|important|urgent`, so the labels diverge silently. Needs a CHECK realignment + migration.

---

#### Cross-Module SLA Ops View — 4 TCs (P0:2 / P1:2)

**Scope (ID):** Live health dashboard (network uptime % + ticket SLA % + WO completion % + billing collection %); cross-module SLA breach aggregation (tickets near breach + WO overdue + maintenance overrun + AR aging); drill-down to source module; CSV/PDF export.

**Existing backend artefacts:**
- `internal/operations/adapter/http/handler.go::slaDashboard` (Wave 71) — returns 5 counters (WO assignment breaches, WO install breaches, NOC verify backlog, maintenance overdue, announcements pending)

**Existing frontend artefacts:**
- `frontend/src/app/(dashboard)/operations/sla/page.tsx` (127 LOC) — SLA counters dashboard

**Gap status:** 🟡 Partial — counters shipped; **billing AR aging + ticket SLA + network uptime not aggregated**

**Top gaps:**
- TC-CSM-001 ticket SLA compliance % — `slaDashboard` doesn't include `field.tickets WHERE sla_resolve_due < NOW() AND status NOT IN ('resolved','closed')`.
- TC-CSM-001 billing collection rate — no `invoicesvc` aggregator surfaced into ops dashboard.
- TC-CSM-002 SLA breach aggregation by tier — current counters are flat; no near-breach (75% threshold) vs already-breached split.
- TC-CSM-003 drill-down — no per-counter route returning the underlying rows (e.g. `GET /operations/sla/ticket-breaches`).
- TC-CSM-004 CSV/PDF export — no export endpoint.

---

### Customer Service module — 78 TCs (entirely new module class)

#### Ticket Types — 8 TCs (P0:8)

**Scope (ID):** CS agent create per-type tickets (Technical Issue, Billing Dispute, Complaint with complaint_type sub-classifier, Service Request, Information); Admin add new complaint type from Administration UI with default priority + SLA; system auto-set default routing per type (Technical → CS Agent → escalate NOC; Billing → CS Agent → escalate Finance; Complaint → CS Supervisor); ticket type immutable once created.

**Existing backend artefacts:**
- `field.tickets.category ∈ {no_internet, slow_speed, frequent_drops, equipment_damage, billing_dispute, other}` (migration 0036). **Note:** the catalog's type taxonomy (Technical Issue / Billing Dispute / Complaint / Service Request / Information) does NOT match the migration's category enum. The migration is product-symptom oriented (no_internet, slow_speed); the catalog is workflow oriented.
- `internal/field/adapter/http/phase2.go::createTicket` (allows arbitrary category from the enum)
- `internal/crm/adapter/http/portal_auth.go::createTicket` (customer-facing variant)

**Existing frontend artefacts:**
- `frontend/src/app/(dashboard)/admin/cs-tickets/page.tsx` (604 LOC, Wave 67/D7) — agent + supervisor mode split; uses the symptom-category taxonomy

**Existing mobile artefacts:**
- `mobile/customer_app/lib/features/support/new_ticket_page.dart` + `ticket_detail_page.dart`

**Gap status:** ❌ Missing the workflow-type taxonomy + admin-managed complaint subtypes

**Top gaps:**
- TC-TKT-003 complaint with sub-type — no `complaint_type` column on `field.tickets`; no enum.
- TC-TKT-004 Admin add new complaint type — no `field.complaint_types` table; no CRUD UI.
- TC-TKT-001/002/005 per-type default routing — no `field.ticket_type_routing` table mapping type → default assignee role.
- TC-TKT-006 type immutability after create — no DB-level guard.
- TC-TKT-007 PRD §6.1 taxonomy mismatch — recommend a migration that expands `category` (or renames it `symptom_category`) and adds a separate `ticket_type ∈ {technical, billing, complaint, service_request, information}` column. Currently impossible to satisfy both the catalog and the migration without schema work.

---

#### Ticket Lifecycle — 10 TCs (P0:10)

**Scope (ID):** SM: open → assigned → in_progress → pending_customer | pending_internal → resolved → closed (with reopen path from customer within Q4 max-reopens window); assign / start-work events recorded; first_response_at captured at first agent action (SLA stop clock for first response); pending_customer pauses resolution SLA + auto-reminder after 24h; pending_internal logs which department; max reopens before locked (e.g. 3, per §7.2 Q4).

**Existing backend artefacts:**
- `field.tickets.status ∈ {open, in_progress, pending_customer, resolved, closed}` (migration 0036) — note: **missing `assigned`** and **missing `pending_internal`** states.
- `field.tickets.{resolved_at, closed_at, sla_response_due, sla_resolve_due}` columns
- `internal/field/adapter/http/phase2.go::patchTicket` — accepts `status`, `assigned_to`, `wo_id`, `csat_score`, `csat_comment`; no `first_response_at` capture, no pause-on-pending semantics
- `internal/field/adapter/http/phase2.go::createTicket` — sets SLA snapshot based on a hardcoded 3-tier priority (high 1h/4h; medium 4h/24h; low 24h/72h)

**Existing frontend artefacts:** `admin/cs-tickets/page.tsx` shows status pills + transitions

**Gap status:** ❌ Missing — **the 5-state SM in the migration does not cover the 7-state SM in the catalog**

**Top gaps:**
- TC-TL-001 full SM with `assigned` + `pending_internal` states — migration 0036 CHECK constraint must be extended.
- TC-TL-003 first_response_at — no column on `field.tickets`; no producer in `patchTicket`.
- TC-TL-004 SLA pause on pending_customer — no `sla_paused_at` / `sla_paused_total_seconds` columns; no resume-on-state-change logic.
- TC-TL-005 pending_internal logs department — no `pending_internal_dept` column or audit subject.
- TC-TL-008 reopen counter + lock — no `reopens_count` column on `field.tickets`; no max-reopens constraint.
- TC-TL-010 timeline_entries — no per-status-change history table (Wave 38 added `ticket_messages` but not `ticket_status_history`).

---

#### Ticket Channels — 6 TCs (P0:4 / P1:2)

**Scope (ID):** Portal app submission (channel=portal, customer auto-id from session); WhatsApp manual ticket (CS agent on behalf with full conversation thread imported); email-to-ticket auto-parse from support@ + customer match-by-email; phone call logging + optional call_recording link; channel distribution report (% per channel + trend + per ticket type).

**Existing backend artefacts:**
- `internal/crm/adapter/http/portal_auth.go::createTicket` — portal channel implicit (caller is the customer)
- `internal/field/adapter/http/phase2.go::createTicket` — agent-on-behalf path

**Gap status:** ❌ Missing — no `channel` column; portal-only today

**Top gaps:**
- TC-CHN-001/002/003/004 channel attribution — no `field.tickets.channel ∈ {portal, whatsapp, email, phone, in_person}` column.
- TC-CHN-002 WhatsApp conversation import — no WhatsApp adapter; no conversation thread persistence.
- TC-CHN-003 email-to-ticket parser — no `cmd/email-ingest-svc/`; no IMAP / SES consumer.
- TC-CHN-004 phone call logging — no `call_recording_url` field on ticket or ticket_message.
- TC-CHN-005 channel distribution report — no aggregation route.

---

#### Priority & SLA — 10 TCs (P0:10)

**Scope (ID):** Per (customer type × ticket type) SLA matrix (residential technical 4h first response; enterprise corporate 1h; etc.); first_response timer + resolution timer (running continuously with pauses); pause on pending_customer; 75%-of-budget breach warning to agent + supervisor; breached SLA flagged dashboard; SLA snapshot at ticket open immutable; matrix editable per admin; per-customer-type after-hours rules; manual SLA override with audit.

**Existing backend artefacts:**
- `field.tickets.{sla_response_due, sla_resolve_due}` snapshots at create (per `phase2.go::createTicket`)
- Hardcoded 3-tier matrix inside `createTicket` (high/medium/low → 1h/4h, 4h/24h, 24h/72h)

**Gap status:** ❌ Missing — **no customer-type × ticket-type matrix; no admin CRUD; no first-response timer; no pause semantics**

**Top gaps:**
- TC-SLA-001 SLA matrix resolution — needs a `field.ticket_sla_matrix` table keyed on `(customer_type, ticket_type, priority) → (first_response_minutes, resolution_minutes)` + resolver at ticket-open.
- TC-SLA-002 first_response timer — no `first_response_at` column; no producer.
- TC-SLA-004 pause on pending_customer — covered in Ticket Lifecycle gaps.
- TC-SLA-005 75% breach warning — no cron evaluator; no `notifyx` producer.
- TC-SLA-008 SLA snapshot immutability — current snapshot is mutable via `patchTicket` (no guard).
- TC-SLA-009 admin CRUD matrix — no admin UI; no backend handler.
- TC-SLA-010 manual SLA override audit — no audit subject `ticket.sla.override` with old/new diff.

---

#### Team Assignment — 8 TCs (P0:7 / P1:1)

**Scope (ID):** Auto-route by ticket type (Technical → CS Agent → escalate NOC; Billing → Finance; Complaint → CS Supervisor); manual assignment with audit; round-robin default for unassigned to online agents balanced by workload; self-assign from queue; reassign with reason; team workload balancer (no agent overload); agent online/offline status; queue overflow → supervisor alert.

**Existing backend artefacts:**
- `field.tickets.assigned_to UUID` + `patchTicket` PATCH route allows assignment
- `identity.users` has role/availability columns (Wave 76 `availability.go` per `internal/identity/`)

**Gap status:** ❌ Missing — **assignment is manual-only; no auto-route, no round-robin, no workload balance, no online/offline gating**

**Top gaps:**
- TC-ASN-001 auto-route by ticket-type — depends on Ticket Types gap (no `ticket_type` column) + a `field.ticket_type_routing` table.
- TC-ASN-003 round-robin to online agents — no `agents_online` view; no workload-balance algorithm.
- TC-ASN-004 self-assign from queue — patch supports it; no permission gate that an agent can only self-assign from their team's queue.
- TC-ASN-005 reassign with reason — no `reassignment_reason` field; no audit subject.
- TC-ASN-008 queue overflow alert — no threshold-driven supervisor alert.

---

#### @Mentions — 3 TCs (P0:1 / P1:2)

**Scope (ID):** Agent types `@noc_engineer` in comment → mentioned user receives notification (in-app + email) with link + mention context; @username autocomplete with valid users only; invalid mention saved as text without notification.

**Existing backend artefacts:** **None.** No mention parser in `field.ticket_messages.body`. No `field.mentions` table.

**Existing frontend artefacts:** None visible in `admin/cs-tickets/page.tsx`

**Gap status:** ❌ Missing — entirely net-new

**Top gaps:**
- TC-MEN-001 mention parser on message create — needs `field.ticket_messages` post-save trigger that extracts `@[\w_]+` tokens, resolves to `identity.users.username`, persists a `field.ticket_mentions(message_id, mentioned_user_id, ticket_id)` row, dispatches a `notifyx` push.
- TC-MEN-002 autocomplete with valid users — needs `GET /identity/users/by-username?prefix=` endpoint (read-only, scope-restricted).
- TC-MEN-003 mention notification — depends on `notifyx`; the in-app + email channels exist as primitives in `pkg/notifyx/`, just no producer.

---

#### WO from Ticket — 5 TCs (P0:5)

**Scope (ID):** Technical Issue ticket → "Create Maintenance WO" button → WO auto-created with customer + issue context, routed to Field/TL; WO inherits customer info + address + issue description + agent notes + photos + priority; ticket-WO link bidirectional (status sync WO complete → ticket auto-update + agent notif); Complaint with termination_dispute → Termination WO + asset return scheduled; ticket auto-update on WO complete.

**Existing backend artefacts:**
- `field.tickets.wo_id` + `field.work_orders.ticket_id` (bidirectional FK, migration 0036)
- `internal/field/adapter/http/phase2.go::createTicket` already auto-spawns a maintenance WO atomically when `category=equipment_damage` (the closest existing equivalent)
- `internal/field/adapter/http/phase2.go::patchTicket` accepts `wo_id` PATCH — manual linkage exists

**Gap status:** 🟡 Partial — `equipment_damage` auto-spawn is hardcoded; needs to generalize to a "Create WO from Ticket" button across ticket types

**Top gaps:**
- TC-WOT-001 "Create Maintenance WO" button + auto-context — no `POST /tickets/{id}/spawn-wo` route; current spawn is on-create-only and category-specific.
- TC-WOT-002 inherit photos/attachments — `ticket_messages.attachments_url[]` is per-message; no copy to `work_orders.attachments` on spawn.
- TC-WOT-003 status sync WO complete → ticket update — no event handler from `work_order.completed` → `ticket.status='resolved'` (and the corresponding agent notification).
- TC-WOT-004 Termination WO from Complaint — depends on Ticket Types `complaint_type='termination_dispute'` (which doesn't exist yet).
- TC-WOT-005 auto-update notify — no `notifyx` producer on WO completion to ticket-assigned agent.

---

#### Service Requests — 12 TCs (P0:12)

**Scope (ID):** Customer-self-service plan upgrade (auto-approve or agent-process) → next cycle prorate or wait; downgrade requires Sales Manager approval (per §17 Q2), no proration current cycle, apply next cycle; add-on purchase (static IP, speed boost) → billing + RADIUS update per schema; add-on removal → next cycle, RADIUS reverted if digital; termination → final invoice settlement + asset return WO; address change → relocation request → feasibility check + new WO; identity update; phone/email change; communication preference change; billing day change; payment method change; consent management (marketing opt-in/out); paperless billing toggle.

**Existing backend artefacts:**
- `internal/crm/adapter/http/portal_auth.go::requestPlanChange / requestRelocation / buyAddon / requestTermination` (Wave 38 + 41)
- `crm.plan_change_requests` table (migration 0036)
- `crm.customer_relocations` table (migration 0036)
- `crm.product_addons` + `crm.customer_addons` (migration 0036)
- `internal/billing/usecase/orchestration.go` (Wave 114) — has commission_trigger + restore_on_paid hooks; add-on integration partial

**Existing frontend artefacts:** Customer mobile app pages exist for all 4 flows

**Gap status:** 🟡 Partial — **the 4 primary flows are wired (plan-change / relocation / addon-buy / termination); the 8 secondary flows (identity / phone / email / preference / billing-day / payment-method / consent / paperless) need a ticket → service-request link**

**Top gaps:**
- TC-SR-001 plan upgrade with auto-approve vs agent-process branch — `plan_change_requests.status` SM has `pending → approved → applied`; no auto-approve gate (currently always agent-process).
- TC-SR-002 downgrade Sales Manager approval — no role-specific approval gate; current handler accepts approval from any user with the permission.
- TC-SR-006 address change feasibility check — `customer_relocations` accepts new address; no PostGIS coverage check.
- TC-SR-007 identity update (KTP re-upload) — `portal_auth.go::portalKTPUpload` exists, but no link to a service-request ticket for audit.
- TC-SR-009 communication preference change — no `crm.customer_comm_preferences` table.
- TC-SR-010 billing day change — no per-customer billing-day-of-month override.
- TC-SR-012 paperless billing toggle — no flag on customer.
- TC-SR-* ticket linkage — all 12 SR flows should atomically create a `field.tickets` row of `ticket_type=service_request` for visibility; currently they create only the per-flow table row.

---

#### Communication — 6 TCs (P0:5 / P1:1)

**Scope (ID):** Customer outbound multi-channel (email + SMS + WhatsApp per customer preference) within 60s of trigger; internal notifications (agent assigned, SLA approaching, escalation needed); template management (customer-facing formal Bahasa + internal technical) — runtime apply with dynamic fields; WhatsApp templates pre-approved per Meta (no free-form); customer preference (opt-out SMS, WhatsApp-only) honored.

**Existing backend artefacts:**
- `pkg/notifyx/` (framework — Wave 110 baseline)
- `crm.customer_notifications` table (migration 0041) — in-app inbox
- `crm.device_tokens` (migration 0046) for push
- WhatsApp adapter stubs flagged in Wave 121E §2 row 10

**Gap status:** ❌ Missing — **framework + inbox exist; multi-channel dispatcher + template registry + preference resolver missing**

**Top gaps:**
- TC-COM2-001 multi-channel parallel dispatch — `notifyx` has push only wired today; no SMS / WhatsApp / email producers connected to events.
- TC-COM2-003 template registry — no `platform.notification_templates` table; current dispatch is per-call free-text.
- TC-COM2-004 WhatsApp pre-approved templates — depends on Meta API + template namespace from Wave 121E §3.10.
- TC-COM2-005 customer preference — no `crm.customer_comm_preferences(email_opt_in, sms_opt_in, whatsapp_opt_in, push_opt_in)` row.

---

#### CSAT — 4 TCs (P0:2 / P1:2)

**Scope (ID):** Ticket resolved → CSAT survey dispatched to customer (1-5 star + optional comment) via app/email link; aggregation per agent / per ticket type / per period (average + distribution + comments digest); CSAT ≤2 → automatic alert to supervisor for review + customer follow-up; monthly CSAT report (overall trend + per-agent breakdown + comments digest + improvement areas).

**Existing backend artefacts:**
- `field.tickets.{csat_score, csat_comment}` columns (migration 0036)
- `internal/crm/adapter/http/portal_auth.go::submitCSAT` route — customer submits score + comment
- `internal/field/adapter/http/phase2.go::patchTicket` accepts CSAT fields

**Existing frontend artefacts:** Customer app submit form exists (path-4 confetti was on CSAT per memory)

**Gap status:** 🟡 Partial — **score persistence works; survey dispatch + aggregation + low-score alert + monthly report missing**

**Top gaps:**
- TC-CST-001 survey dispatch on resolve — no producer wiring `ticket.resolved` event → `notifyx` survey dispatch.
- TC-CST-002 aggregation — no `GET /admin/cs/csat/aggregate?dim=agent|type|period` route.
- TC-CST-003 low-score alert — no cron / event listener evaluating `csat_score <= 2`.
- TC-CST-004 monthly report — no report generator.

---

#### CS Dashboards — 6 TCs (P0:5 / P1:1)

**Scope (ID):** Agent queue view (assigned + unassigned + pending + recent closed); workload indicator (active ticket count + SLA at risk + daily resolution); supervisor team SLA overview (team % compliance + breach count + escalations pending + agent workload distribution); escalation queue (from agents needing supervisor input + max-reopen tickets + breached tickets — prioritized); agent performance report (resolved + avg resolution time + CSAT + SLA compliance per agent); team alert (breach trending, workload imbalance).

**Existing backend artefacts:**
- `admin/cs-tickets/page.tsx` (Wave 67) already has agent + supervisor mode split, but the aggregations are computed client-side from a flat ticket list

**Gap status:** ❌ Missing — **dashboards exist as UI; the backend aggregation routes do not**

**Top gaps:**
- TC-CSD-001 agent queue view — no `GET /cs/agent/{id}/dashboard` aggregation route.
- TC-CSD-003 team SLA overview — no `GET /cs/team/{id}/sla-summary` route.
- TC-CSD-004 escalation queue — no `GET /cs/escalations/pending` route (current page reads flat tickets and filters in JS).
- TC-CSD-005 agent performance report — no per-agent aggregator route.

---

## Cross-cutting findings

### 1. There is no Customer Service bounded context (`internal/cs/`)

CS code is split across:
- `internal/field/adapter/http/phase2.go` — agent-side ticket CRUD + maintenance events (same handler!)
- `internal/crm/adapter/http/portal_auth.go` — customer-side ticket portal routes + service-request flows (plan-change / relocation / addon / termination)
- `internal/billing/` — billing dispute side-effects (no direct ticket touch)

**Recommendation:** **Phase 1C should extract `internal/cs/` as a new bounded context.** The 78 CS TCs cluster naturally:

- `internal/cs/domain/` — `Ticket`, `TicketMessage`, `TicketStatusHistory`, `SlaMatrix`, `TicketMention`, `CsatSurvey`, `ServiceRequest`, `Assignment`
- `internal/cs/usecase/` — `Lifecycle`, `Routing`, `SlaEvaluator` (cron), `MentionParser`, `CsatAggregator`, `ServiceRequestExecutor`
- `internal/cs/port/` — interfaces to `network` (RADIUS for service requests), `field` (WO creation), `billing` (commission/refund hooks), `notifyx`
- `internal/cs/adapter/http/` — `agent_handler.go` (Internal Web), `portal_handler.go` (move from `crm`)
- `internal/cs/adapter/postgres/` — repositories over `field.tickets` + `field.ticket_messages` + new tables

The existing `field.tickets` + `field.ticket_messages` + `field.ticket_attachments` tables stay (DB-level renames are expensive and pointless); only the Go code re-homes.

This extraction also makes the Phase 1C wave sequence clean: the structural split is **its own wave** (Wave 123), then four content-delivery waves layer behind it (Waves 124-127).

### 2. Maintenance Window vs CS Service Request are different workflows — but they share a sub-shape

| Aspect | `field.maintenance_events` (Phase 1B) | CS Service Request (Phase 1C) |
|---|---|---|
| Initiated by | Operator (Ops Admin / NOC Manager) | Customer (portal / WhatsApp / phone) |
| Affects | Multiple customers (downstream of a network node) | One customer |
| Approval | Joint (NOC Manager + Ops Admin when >100 customers) | Per-type (Sales Manager for downgrade, Finance for billing) |
| Notification lead-time | ≥24h broadband / ≥72h enterprise | Real-time confirmation |
| WO spawn | One per affected node (network-scoped WO) | Sometimes one per request (customer-scoped WO) |
| Audit subject | `maintenance.*` | `service_request.*` |

Phase 1C catalog correctly treats them as separate but **both end up creating a `field.tickets` row** for visibility (Maintenance ticket type for staff awareness; Service Request ticket type for customer-initiated changes). They share:
- The mentions surface (any internal staff can be @-mentioned)
- The notification surface (`notifyx` multi-channel)
- The SLA surface (resolution timer)

The audit recommends **the SLA evaluator usecase and the mention parser are written once in `internal/cs/usecase/`** and consumed by both flows — not duplicated.

### 3. Bulk Ops infrastructure (Wave 71) is generic and reusable — per-kind executors are the gap

The `operations.bulk_operations` framework is **architecturally sound**:
- One table, polymorphic by `op_kind` over `payload jsonb`
- 4-state SM (`draft → previewed → approved → completed`) gated by 4 separate permissions
- `preview_summary` + `execution_journal` jsonb columns for audit
- Handler is a single 700-LOC `internal/operations/adapter/http/handler.go` (Wave 71)

But the **executor is a no-op** today (just flips status to `completed`). The 3 op_kinds in Phase 1C all need real executors:

| Op kind | Lines of executor logic estimate | Touches |
|---|---|---|
| `plan_change` | ~200 LOC | `crm.customers.plan_id` + `internal/network/adapter/radius/` push + `billing.cycles` next-cycle bump |
| `odp_migration` | ~250 LOC | `network.ports.assigned_customer_id` + RADIUS profile path + Maintenance WO conditional spawn |
| `wo_create` | ~150 LOC | Atomic batch insert into `field.work_orders` + dedup against open WOs |

**Total bulk-ops executor work:** ~600 LOC across one wave. The framework is **right for the catalog**; what's missing is **per-row idempotent writers + failure isolation + a real `execution_journal` accumulator**.

### 4. Operational Calendar already overlaps with `field/sla-breaches` + `field/maintenance` UI

The Wave 71 `operations.calendarFeed` already cross-joins maintenance + bulk + announcements into one event stream. The existing `frontend/src/app/(dashboard)/operations/calendar/page.tsx` (148 LOC) consumes it. The 6 calendar TCs need:
- iCal export endpoint (RFC 5545; ~80 LOC)
- Per-branch claim filter (~20 LOC in calendarFeed)
- Conflict detection on `scheduled_start` overlap (~40 LOC pre-insert check in `createMaintenanceEvent`)
- Date range query params on calendarFeed (~30 LOC)

**Effort:** ~170 LOC + 1 frontend update. **No new tables.** This is a **small wave** and the lowest-effort wave in the Phase 1C plan.

### 5. Internal Announcements — notifyx already handles this, dispatcher cron is missing

`operations.internal_announcements` (Wave 71) stores announcements with `targeting jsonb` + `channels jsonb` + `scheduled_at` + `sent_at`. `pkg/notifyx/` has the multi-channel framework. The missing piece is a **dispatcher cron** that:
1. Reads `WHERE sent_at IS NULL AND (scheduled_at IS NULL OR scheduled_at <= NOW())`
2. Resolves `targeting` (all_staff / role:cs_agent / branch:jakarta) to user IDs
3. Dispatches via `notifyx` per channel from `channels jsonb`
4. Marks `sent_at = NOW()`, persists `sent_count`
5. For urgent severity, creates pending receipts in `operations.announcement_receipts` (new table) for acknowledge tracking

**Plus:** the **severity enum mismatch** (migration 0048 uses `info|warning|critical`; frontend uses `info|important|urgent`; PRD uses `info|important|urgent`) — this is a real bug that affects every announcement. Fix: migration to rename + a thin compatibility shim in the create handler.

**Effort:** ~150 LOC dispatcher + ~80 LOC ack tracking + 1 migration. Modest. Folds into the same wave as the Operational Calendar polish.

### 6. @Mentions — comment layer exists at `field.ticket_messages`; mention extraction is net-new

`field.ticket_messages` (Wave 38) has `body TEXT` + `is_internal_note BOOLEAN`. Mentions live as `@username` tokens in `body`. The work is:
- A **parser** (regex on `@[\w._-]+`) called from `postTicketMessageAgent`
- A **resolver** (`identity.users.username → identity.users.id`)
- A **new table** `field.ticket_mentions(id, ticket_id, message_id, mentioned_user_id, mentioned_at, notification_dispatched_at)`
- A **notifyx producer** firing in-app push + email to the mentioned user

**Effort:** ~180 LOC + 1 migration. Standalone. **Recommended as the smallest single deliverable Phase 1C wave** — useful as a confidence-build before the larger CS extraction.

### 7. SLA / Priority — Phase 1B has `internal/platform/domain/service_validator.go` (Wave 116) for service-schema deep validators; CS reuses or needs its own

Wave 116 (`internal/platform/domain/service_validator.go`) shipped a typed validator for the **service schema kind** that asserts SLA tiers per customer type (residential 99.0% / business 99.5% / enterprise 99.9% / corporate 99.95%). This is for **infrastructure-level SLA** (uptime).

The Phase 1C **Priority & SLA module (10 TCs)** is for **ticket-level SLA** (first-response time + resolution time). The two are different but should share the customer-type lookup. Specifically:
- Wave 116's validator answers: "given customer type, what's the uptime SLA?"
- Phase 1C needs: "given (customer_type, ticket_type, priority), what's the first-response + resolution SLA?"

**Recommendation:** **Add a second typed validator** in `internal/platform/domain/cs_sla_validator.go` (Wave 116 sibling) that reads from a service-kind schema's `cs_ticket_sla` sub-content. This reuses the publish-schema-with-validation gate (Wave 116) and the customer-type resolver. Add a `field.ticket_sla_matrix` table for runtime caching.

**Effort:** ~300 LOC validator + ~180 LOC matrix usecase + 1 migration. Medium wave.

### 8. CSAT — score persists; aggregation + low-score alert + monthly report are the gaps

`field.tickets.{csat_score, csat_comment}` exist (migration 0036). `portal_auth.go::submitCSAT` accepts customer submits. The 4 CSAT TCs need:
- A **survey dispatcher cron** (or `ticket.resolved` event listener) that pushes the survey link via `notifyx` after a configurable delay (default 24h)
- An **aggregation route** `GET /admin/cs/csat/aggregate?dim={agent|type|period}` returning rolling-window averages + distribution histograms
- A **low-score alert** trigger (`csat_score <= 2` → supervisor inbox + optional customer-callback task)
- A **monthly report generator** (re-uses the aggregation route + adds comments digest)

**Effort:** ~250 LOC aggregator + ~120 LOC survey dispatcher + ~80 LOC low-score alert + 0 migrations (all reads). Modest.

---

## Wave plan (Wave 123 onwards)

Each wave is sized to be a 1-3 session deliverable for one or two engineers. "TCs targeted" = TCs that will move from "untested" → "testable" by the wave's end. Dependencies are noted so the team can parallelize.

### Wave 123 — `internal/cs/` extraction + Ticket Types + Ticket Lifecycle SM extension

**Scope:** Extract Customer Service into its own bounded context: domain types, port interfaces, postgres adapter over existing `field.tickets` + `field.ticket_messages` tables (no rename — keep DB stable). Extend ticket SM with `assigned` + `pending_internal` states. Add `ticket_type ∈ {technical, billing, complaint, service_request, information}` column. Add `complaint_type` column with admin CRUD. Add `first_response_at`, `pending_internal_dept`, `reopens_count`, `sla_paused_at`, `sla_paused_total_seconds` columns. Add `field.ticket_status_history` table. Add `field.complaint_types` admin-managed table.

**TCs targeted:** ~26 (Ticket Types 8 + Ticket Lifecycle 10 + WO from Ticket 5 generalization + 3 from §3 alignments)

**Acceptance criteria:**
- New `internal/cs/` package (domain + usecase + port + adapter + http).
- `internal/field/adapter/http/phase2.go` ticket routes move to `internal/cs/adapter/http/agent_handler.go` (CRM portal routes stay where they are — they're customer-facing).
- Migration 0082 with new columns + tables.
- Backward-compat: `category` enum stays (renamed `symptom_category` in code, kept as `category` in DB) — no breaking change for in-flight tickets.
- Test: full SM walk (open → assigned → in_progress → pending_customer → resolved → closed → reopened) with first_response_at + SLA pause accounting.

**Dependencies:** None — this is the foundation wave.

**File-scope discipline:**
- Touches: `internal/cs/**`, `internal/field/adapter/http/phase2.go` (delete ticket routes, keep maintenance), `migrations/0082_*`.
- Does NOT touch: `internal/crm/adapter/http/portal_auth.go` (Wave 124 owns portal extraction), Bulk Ops, Maintenance.

---

### Wave 124 — Priority & SLA Matrix + CS Service Requests linkage

**Scope:** `field.ticket_sla_matrix` table keyed on `(customer_type, ticket_type, priority)` with first_response_minutes + resolution_minutes. Admin CRUD UI. `cs_sla_validator.go` in `internal/platform/domain/` (sibling to Wave 116 `service_validator.go`). SLA snapshot at ticket open (immutable). Pause-on-pending semantics. First-response timer (started at `open`, stopped at first agent reply or status change). 75%-of-budget breach warning (cron). Manual SLA override audit. Atomic ticket creation on customer-initiated Service Requests (plan-change / relocation / addon / termination flows from `portal_auth.go` ALSO create a `field.tickets` row of type=service_request).

**TCs targeted:** ~22 (Priority & SLA 10 + Service Requests 12)

**Acceptance criteria:**
- Migration 0083 with `ticket_sla_matrix` + seed data per customer type + ticket type.
- `internal/cs/usecase/sla_evaluator.go` cron entry (75% breach + breach evaluator).
- `internal/cs/adapter/http/sla_matrix_handler.go` for admin CRUD.
- Service Request flows in `portal_auth.go` updated to create the ticket row atomically.
- `internal/platform/domain/cs_sla_validator.go` for schema-driven matrix validation.

**Dependencies:** Wave 123 (CS bounded context).

---

### Wave 125 — Bulk Ops Executors (Plan Change + ODP Migration + WO Creation)

**Scope:** Per-kind executor in `internal/operations/usecase/`. `plan_change_executor.go` (~200 LOC): iterate `payload.customer_ids`, update `crm.customers.plan_id`, push RADIUS profile, bump next billing cycle, audit per customer. `odp_migration_executor.go` (~250 LOC): capacity check ODP-Y, atomic port reassign, RADIUS path update, optional Maintenance WO spawn for fiber re-splice. `wo_create_executor.go` (~150 LOC): per-row eligibility check + dedup against open WOs + atomic batch insert. Joint-approval lifecycle for downgrade (2-of-2 approvers). Failure isolation + per-customer retry budget + accurate `execution_journal` accumulation.

**TCs targeted:** ~18 (Bulk Plan Change 8 + Bulk ODP Migration 6 + Bulk WO Creation 4)

**Acceptance criteria:**
- 3 new executor files under `internal/operations/usecase/`.
- Migration 0084: `operations.bulk_operations` gains `joint_approvals jsonb` + `retry_budget int` + downgrade requires both Finance Manager + Management role approvers.
- `field.work_orders.maintenance_subtype` CHECK constraint extended with `odp_migration`.
- Failure isolation test: 5/10 succeed → status='partial' + journal records both successes and failures.
- Idempotency test: re-running an executed bulk op is a no-op.

**Dependencies:** None — operations.bulk_operations framework already exists. Can run **in parallel** with Wave 123.

---

### Wave 126 — Planned Maintenance Approval + Notification + Overrun + War Room data inheritance

**Scope:** Affected-customers materialization at maintenance-event-create time (walk `network.ports` downstream of `maintenance_event_nodes`). Approval threshold cron (>100 customers → require NOC Manager + Ops Admin joint). Customer notification cron with per-customer-type lead-time (broadband 24h / enterprise 72h). Overrun-detect cron (in_progress past scheduled_end + tolerance). War Room inherit payload snapshot at escalate time. Reschedule re-notification. Severity enum realignment migration (`info|warning|critical` → `info|important|urgent` per PRD). Internal Announcements dispatcher cron + acknowledge tracking + receipts table.

**TCs targeted:** ~19 (Planned Maintenance 10 + Maintenance Escalation 4 + Internal Announcements 5)

**Acceptance criteria:**
- New `internal/operations/usecase/maintenance_cron.go` with 3 cron entries (approval-gate, notify-lead-time, overrun-detect).
- Migration 0085: `field.maintenance_events.affected_customers_snapshot jsonb`, `operations.announcement_receipts(announcement_id, user_id, acknowledged_at)`, severity enum rename.
- War Room escalate payload snapshot persists `affected_customers_snapshot` + `timeline_entries` jsonb at the moment of escalation.
- Per-customer-type lead-time resolver (24h vs 72h) reads from customer record.

**Dependencies:** Wave 124 (notifyx multi-channel from CS communication).

---

### Wave 127 — Ticket Channels + @Mentions + Communication + Operational Calendar polish + Cross-Module SLA + CSAT aggregator + CS Dashboards

**Scope:** `field.tickets.channel ∈ {portal, whatsapp, email, phone, in_person}` column + per-channel ingest paths. Mentions: parser at `postTicketMessageAgent`, `field.ticket_mentions` table, `GET /identity/users/by-username` autocomplete, notifyx in-app + email producer. Communication: `crm.customer_comm_preferences` table + notifyx multi-channel + preference-honoring dispatcher; `platform.notification_templates` registry + WhatsApp pre-approved template adapter. Operational Calendar polish: per-branch claim filter, iCal export endpoint, conflict-detect on overlap, date range query params. Cross-Module SLA: extend `slaDashboard` with ticket SLA % + billing collection % + drill-down routes + CSV export. CSAT: aggregation route + survey dispatcher cron + low-score alert + monthly report. CS Dashboards: agent queue + supervisor team SLA + escalation queue + agent performance backend routes.

**TCs targeted:** ~40 (Ticket Channels 6 + @Mentions 3 + Communication 6 + Operational Calendar 6 + Cross-Module SLA Ops View 4 + CSAT 4 + CS Dashboards 6 + Team Assignment 8 — round-robin + auto-route + online/offline + reassign-with-reason)

**Acceptance criteria:**
- Migration 0086: `tickets.channel`, `ticket_mentions`, `customer_comm_preferences`, `notification_templates`, `ticket_type_routing`.
- New `internal/cs/usecase/mention_parser.go`, `internal/cs/usecase/router.go`, `internal/cs/usecase/csat_aggregator.go`.
- New iCal export endpoint with RFC 5545 encoder.
- WhatsApp template adapter (production-mode behind `WHATSAPP_BUSINESS_TOKEN`; stub mode for CI).
- CS Dashboards aggregation routes (`GET /cs/agent/{id}/dashboard`, `GET /cs/team/{id}/sla-summary`, `GET /cs/escalations/pending`).

**Dependencies:** Wave 123 (CS bounded context) + Wave 124 (SLA matrix).

---

## Cumulative wave roll-up

| Wave | Scope | NEW TCs targeted | Cumulative NEW | Cumulative TOTAL (713 + new) |
|---|---|---:|---:|---|
| 123 | `internal/cs/` extraction + Ticket Types + Lifecycle SM | 26 | 26 / 125 (21%) | 739 / 838 (88.2%) |
| 124 | Priority & SLA Matrix + Service Requests linkage | 22 | 48 / 125 (38%) | 761 / 838 (90.8%) |
| 125 | Bulk Ops Executors (plan change + ODP migration + WO create) | 18 | 66 / 125 (53%) | 779 / 838 (93.0%) |
| 126 | Planned Maintenance approval/notify/overrun + Internal Announcements dispatcher | 19 | 85 / 125 (68%) | 798 / 838 (95.2%) |
| 127 | Channels + Mentions + Communication + Calendar polish + Cross-SLA + CSAT + CS Dashboards + Team Assignment | 40 | 125 / 125 (100%) | 838 / 838 (100%) |

The remaining 0 TCs in Phase 1C are zero after Wave 127. **Final coverage projection after Wave 127:** ~95% direct testable (matches the Wave 120 ratio), ~3% manual QA (WhatsApp template approval cycle, real SMS gateway, IMAP email-to-ticket end-to-end), ~2% indirect (parent-flow coverage).

**Calendar estimate (one engineer):** ~3-4 person-months for Waves 123-127. With a 2-person team in parallel:
- Engineer A: 123 (CS extraction) → 124 (SLA matrix) → 126 (maintenance) — sequential
- Engineer B: 125 (bulk executors) → 127 polish — sequential

Total: **~6-8 calendar weeks** with 2 engineers; **~12-14 calendar weeks** with 1 engineer.

---

## Recommendation

**Ship Waves 123, 124, 125 first — in that order**, with the rationale:

1. **Wave 123 (`internal/cs/` extraction + Ticket Types + Lifecycle SM)** is the **foundational unblocker**. Until CS has its own bounded context with the corrected 7-state SM, every subsequent CS wave fights against the symptom-oriented `field.tickets.category` enum that doesn't match the catalog's workflow-type taxonomy. 26 TCs unlock with this single structural correction. Estimated 2-3 sessions.

2. **Wave 124 (Priority & SLA Matrix + Service Request linkage)** is the **highest-leverage business wave**. The SLA matrix is the single most-cited concept across the 78 CS TCs (10 dedicated TCs + 8 cross-referenced from Lifecycle / Team Assignment / Dashboards). Wiring service-request flows atomically through `field.tickets` also unblocks the CS Dashboards (which need a count of "service requests in flight"). 22 TCs.

3. **Wave 125 (Bulk Ops Executors)** is the **fast-ROI operational wave**. The framework already exists from Wave 71 — the missing executors are pure code, ~600 LOC across 3 well-bounded executors. 18 TCs from a framework that ships entirely on the existing `operations.bulk_operations` table. No new migrations besides one ALTER for `joint_approvals` and `retry_budget`. Can run in **parallel** with Wave 123/124 (different bounded contexts, zero overlap).

After these three, Waves 126 (maintenance refinement) and 127 (channel / mentions / communication / dashboards polish) can run in parallel as the **two final closeout waves** before a Wave 128 compliance-report mirroring Wave 120's pattern.

**Do NOT start with Wave 127 (the polish wave)** — without Wave 123's CS bounded context, the polish work is a guess against the wrong domain shape and will need rework.

**Do NOT block on Wave 121E's HRIS env-flag remediation** — that's a Phase 1B operational concern; for Phase 1C, the stub-mode HRIS data is sufficient for @Mentions autocomplete (users come from `identity.users`, not directly from HRIS).

---

## File-path index for reviewers

**Operations (Phase 1C-relevant; Wave 71 / migration 0048 origin):**
- `backend/internal/operations/adapter/http/handler.go` — Bulk Ops + Announcements + Calendar feed + Cross-Module SLA + War Room escalate
- `backend/internal/operations/usecase/polygon_overlap.go` + `tl_conflict.go` — existing usecases (carried-over)
- `backend/migrations/0048_phase1a_closure.up.sql` — `operations.bulk_operations`, `operations.internal_announcements`, `field.maintenance_events` war-room columns

**Field — tickets + maintenance (Phase 1C-relevant skeleton; Wave 36 / 38 / 41 origin):**
- `backend/internal/field/adapter/http/phase2.go` — ticket CRUD + maintenance CRUD (1100+ LOC; **Wave 123 will split this**)
- `backend/internal/field/adapter/http/phase2_backlog.go` — backlog helpers
- `backend/migrations/0036_phase2_foundation.up.sql` — `field.tickets`, `field.maintenance_events`, `field.maintenance_event_nodes`, `crm.product_addons`, `crm.customer_addons`, `crm.plan_change_requests`, `crm.customer_relocations`
- `backend/migrations/0038_gap_closures.up.sql` — `field.ticket_messages` with `is_internal_note` + `attachments_url[]`
- `backend/migrations/0041_p1_p2_followups.up.sql` — `crm.customer_notifications` inbox

**CRM portal — customer-side CS surface (Phase 1C-relevant; Wave 36 + 38 origin):**
- `backend/internal/crm/adapter/http/portal_auth.go` — `myTickets / ticketDetail / ticketMessages / postTicketMessage / submitCSAT / createTicket / requestPlanChange / requestRelocation / buyAddon / requestTermination`
- `backend/internal/crm/adapter/http/portal_priority.go` — priority insertion
- `backend/internal/crm/adapter/http/portal_backlog.go` — backlog helpers

**Frontend (Phase 1C-relevant; Wave 67/D7 + Wave 71 origin):**
- `frontend/src/app/(dashboard)/admin/cs-tickets/page.tsx` (604 LOC) — Agent + Supervisor mode split
- `frontend/src/app/(dashboard)/admin/maintenance/page.tsx` (360 LOC) — Maintenance list + detail + dispatch
- `frontend/src/app/(dashboard)/operations/bulk/page.tsx` (282 LOC) — Bulk Ops UI
- `frontend/src/app/(dashboard)/operations/announcements/page.tsx` (223 LOC) — Internal announcements
- `frontend/src/app/(dashboard)/operations/calendar/page.tsx` (148 LOC) — Unified calendar
- `frontend/src/app/(dashboard)/operations/sla/page.tsx` (127 LOC) — Cross-Module SLA dashboard

**Mobile (Phase 1C-relevant):**
- `mobile/customer_app/lib/features/support/new_ticket_page.dart`, `ticket_detail_page.dart` — customer ticket flows
- (CSAT submit form already in customer_app per memory file `project_phase1_broadband_plan.md`)

**Platform / Schema validators (Wave 116 — Phase 1C reuses):**
- `backend/internal/platform/domain/service_validator.go` — service-schema typed validator (Wave 124 will sibling-add `cs_sla_validator.go`)
- `backend/internal/platform/domain/billing_validator.go`, `onboarding_validator.go`, `commission_validator.go`, `suspension_validator.go` — Wave 116 family

**Notifyx + Channels (Phase 1C builds on):**
- `backend/pkg/notifyx/` — multi-channel dispatcher framework
- `backend/migrations/0046_push_outbox.up.sql` — push outbox table
- `backend/migrations/0043_webhook_deliveries.up.sql` — webhook delivery log

**Does NOT exist (Phase 1C deliverables):**
- ❌ `backend/internal/cs/` — entire bounded context (Wave 123)
- ❌ `backend/internal/cs/usecase/sla_evaluator.go` — first-response + resolution timer (Wave 124)
- ❌ `backend/internal/cs/usecase/mention_parser.go` — @mention extractor (Wave 127)
- ❌ `backend/internal/cs/usecase/csat_aggregator.go` — CSAT per-agent + per-period (Wave 127)
- ❌ `backend/internal/operations/usecase/{plan_change_executor,odp_migration_executor,wo_create_executor}.go` — Bulk Ops executors (Wave 125)
- ❌ `backend/internal/operations/usecase/maintenance_cron.go` — approval-gate + notify-lead-time + overrun-detect (Wave 126)
- ❌ `backend/internal/operations/usecase/announcement_dispatcher.go` — multi-channel + ack tracking (Wave 126)
- ❌ `field.ticket_sla_matrix`, `field.ticket_mentions`, `field.complaint_types`, `field.ticket_type_routing`, `field.ticket_status_history`, `crm.customer_comm_preferences`, `platform.notification_templates`, `operations.announcement_receipts` — net-new tables across Waves 123-127

The audit's strongest finding: **the Phase 1C catalog overwhelmingly works against tables and handlers that already exist in the codebase** (Wave 36 / 38 / 41 / 48 / 71 each shipped 1-2 of the structural pieces). What's missing is the **CS-shaped composition layer** — the bounded context that turns scattered ticket/maintenance/bulk primitives into a coherent Customer Service module. The 5-wave plan above ships that composition layer in a sequential foundation (123 → 124) + parallel-track polish (125 || 126 || 127) shape.
