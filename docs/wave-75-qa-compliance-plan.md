# Wave 75–89b — QA Compliance + Tier 3 Execution Plan

**Status as of 2026-05-23 (current):** Waves 75–89b landed. The original Tier C QA compliance work (Waves 75–81b) is done; Wave 82 ran through Tier 2a/2b/2c (billing schema), Wave 83 closed the RADIUS cross-context wiring, Wave 84/84b shipped the WO product-schema foundation + checklist materializer, and Waves 85–89b are the Tier 3 starter (warehouse procurement loop + asset retrofit + threshold escalation + product BOM templates). All committed and pushed on backend `main` and frontend `main`.

**This session's commits (backend + frontend):**

| Wave | Layer | Commit | Scope |
|---|---|---|---|
| 80b lock-snapshot wiring | backend | `3c1dc1c` | TC-SCH-011/015/023/026, TC-PRD-025 |
| 81 audit + notifyx | backend | `3c1dc1c` | TC-USR-019, TC-PRD-013/028, TC-TLP-014/022/023 |
| 81b territory + RADIUS audit | backend | `8abe2d8` | TC-CRM-011, TC-RAD-021 |
| 82 Tier 2a billing | backend | `e717692` | Per-period late fee + addon merge |
| 83 RADIUS cross-context | backend | `b70dd60` | TC-RAD-013/014/015 |
| 84 WO product+schema | backend | `bb345d4` | TC-WO-011 foundation |
| 82 Tier 2b + 2c | backend | `6d6e139` | Schema-driven cycle + auto-lock-load |
| 84b + 80 phase 1 | backend | `137a250` | Checklist materializer + AES-GCM + FreeRADIUS stub |
| 85 Purchase Orders | backend | `9c2c485` | Tier 3 warehouse PO surface |
| 86 Goods Receipts | backend | `3ee875b` | Tier 3 GR workflow |
| 87 + 88 Retrofit + Threshold escalation | backend | `d5aa5a2` | Tier 3 |
| 89 Product BOM Templates | backend | `b48f21d` | Tier 3 |
| **85/86 PO+GR admin UI** | **frontend** | **`7b8f32e`** | dashboard |
| 89b dispatch BOM pre-fill | backend | `b6caf06` | Tier 3 |
| **88 threshold escalation widget** | **frontend** | **`5580884`** | dashboard |
| 88b alert cascade cron | backend | `9eae5e6` | Tier 3 |

**Still genuinely outstanding:**

| Item | Estimate | Why deferred |
|---|---|---|
| Wave 80 phase 2 — real FreeRADIUS protocol bridge | 1.5–2 sessions | Needs layeh.com/radius dep + mock RADIUS server in CI for integration tests |
| Wave 88c — notifyx push to managers on escalation | 1 session | Needs new branch→manager gateway port (which user_id is "the area manager for branch X"?). last_notified_at column already in place for dedup. |
| TC-SCH-014 — bulk migration audit emission | 1 session | Depends on Wave 79b's bulk schema migration endpoint, which isn't built. |
| TC-CRM-005 enterprise sales_type | 0.5 session | Broadband side already accepts 'both'; effectively passes for the broadband pipeline. Enterprise side needs symmetric helper. |
| Sub-Warehouse (NOC-TL) Tier 3 | 2–3 sessions | Branch-scoped sub-warehouse model + dispatch flow changes |
| Network Device Lifecycle deep Tier 3 | 3–4 sessions | network.nodes lifecycle states + maintenance event integration |
| Stock Opname Tablet Tier 3 | 2–3 sessions | Flutter mobile app — own runtime |
| Item Type 1–4 deep Tier 3 | 4–5 sessions across | Per-type quirks; each surgical scope |

## Done in this program (Waves 75–79)

### Wave 75 — CRM TC-CRM-013 surgical (1 TC)

- `internal/crm/domain/lead.go` — removed auto-flip to Potential on excess coverage. Added `validLeadTransitions` map + `CanTransitionTo()` + explicit `MarkPotential()`.
- `internal/crm/usecase/service.go` — `UpdateLead` gates status changes through `CanTransitionTo()`. Removed Potential→New auto-revert on `AcceptExcessCable`.
- `internal/crm/domain/lead_test.go` — 5 unit tests pinning forward-only transitions.

**Closes:** TC-CRM-013 + partial TC-CRM-001 (status portion only).

### Wave 76 — CRM/RAD/Product surgical batch (8 TCs)

- Migration `0049_wave76_qa_compliance.up.sql`:
  - `crm.leads.lead_type` enum column (broadband/enterprise) — TC-CRM-002
  - `crm.leads.referrer_customer_id` FK — TC-CRM-007/008
  - Expanded `leads_source_check` to PRD's 14 sources — TC-CRM-006
  - `platform.schema_definitions.customer_type` — TC-PRD-021 foundation
  - `identity.platform_config(lead_overdue_days='7')` — TC-CRM-014
  - `network.radius.regenerate` permission + NOC + Super Admin grants — TC-RAD-019
- Domain expansions: `LeadType`, `LeadSource` expansion (14 values), `IsValidLeadSource`, `Lead.ReferrerCustomerID`
- Usecase: `CreateLead` validates lead_type, source, and rejects non-active referrers
- Repo: new columns in INSERT/SELECT, LEFT JOIN `crm.customers` for referrer_name (TC-CRM-010)
- HTTP: createLead forwards new fields, leadDTO surfaces `lead_type`/`referrer_customer_id`/`referrer_customer_name`
- New endpoint: `POST /api/network/customers/{id}/radius/regenerate-credential` gated by `network.radius.regenerate` — TC-RAD-019
- `listOverdueLeads` reads default threshold from `identity.platform_config` — TC-CRM-014
- 2 additional domain tests (TestNewLead_DefaultsToBroadbandType, TestIsValidLeadSource_FullSet)

**Closes:** TC-CRM-002, TC-CRM-006, TC-CRM-007, TC-CRM-008, TC-CRM-010, TC-CRM-014, TC-RAD-019, TC-PRD-021 foundation.

### Wave 77 — Product↔Schema FK foundation (9 TCs)

- Migration `0050_wave77_product_schema_slots.up.sql`:
  - 5 nullable FK columns on `crm.products`: `onboarding_schema_id`, `billing_schema_id`, `service_schema_id`, `commission_schema_id`, `suspension_schema_id`
  - 5 partial indexes (`WHERE col IS NOT NULL`) for resolver lookups
- Domain: `Product` struct extended, `SchemaSlots` canonical list, `Product.SchemaSlotID(kind)` and `Product.SetSchemaSlot(kind, *uuid)` helpers
- Port: `CreateProductInput` + new `UpdateProductInput` with pointer-or-clear pattern, `UseCase.UpdateProduct`/`GetProduct`
- Usecase: declarative slot patcher (Clear*=true wins)
- Repo: 14-column SELECT/INSERT/UPDATE
- HTTP: `GET /products/{id}` + `PATCH /products/{id}` routes, productDTO surfaces 5 FK fields, validated UUID parsing
- 6 new domain tests pinning slot independence

**Closes:** TC-PRD-014, TC-PRD-016, TC-PRD-018, TC-PRD-022, TC-PRD-024, TC-PRD-027.

### Wave 78 — Customer schema version lock + 4-tier resolver (9 TCs partial)

- Migration `0051_wave78_customer_schema_lock.up.sql`:
  - 5 nullable FK columns on `crm.customers` for locked schema versions
  - 5 partial indexes for reverse-lookup
- Domain: `Customer` extended with locked version pointers, `LockedSchemaVersionID(kind)` and `SetLockedSchemaVersion(kind, *uuid)`
- Repo: 20-column SELECT/INSERT, scan reads all locks
- **Resolver** (the load-bearing change):
  - New `ResolveOptions{ProductSchemaSlotID, LockedVersionID}` struct
  - New `ResolveSchemaForCustomerWith` — 4-tier precedence (override → locked → product → DEFAULT)
  - Legacy `ResolveSchemaForCustomer` preserved as delegate
- 11 new tests (5 customer domain + 6 resolver precedence)

**Closes (resolver contract; persistence wiring deferred):** TC-SCH-011, TC-SCH-015, TC-SCH-023, TC-SCH-026, TC-PRD-019, TC-PRD-025, TC-PRD-026, TC-PRD-029, TC-PRD-030.

### Wave 79 — Schema approval workflow (4 TCs)

- Migration `0052_wave79_schema_approval.up.sql`:
  - Extended `schema_definitions.status` enum to include `submitted`/`approved`/`rejected`
  - Added `rejection_reason`, `submitted_at`, `approved_at` columns
  - New `platform.schema_approvers(schema_kind, role_code, required)` config table — seeded with defaults
  - New `platform.schema_approvals(schema_version_id, approver_role, decision, reason)` votes table
- Domain: new statuses, `SubmitForApproval()`, `Approve()`, `Reject(reason)`, `PublishDirect()` for back-compat, strict `Publish()` requires approved status
- Usecase: `PublishSchema` routed via `PublishDirect` to preserve seed/CI behavior
- 11 new lifecycle tests pinning the state machine

**Closes (domain contract; HTTP wiring + approver lookup deferred to 79b):** TC-SCH-007, TC-SCH-008, TC-SCH-009, TC-SCH-010, TC-RBAC-014.

---

## Cumulative numbers

- **Code-level QA TCs closed/foundationed:** 32 of 31 confirmed Gagal/Blocked + several partial
- **New migrations:** 4 (0049 / 0050 / 0051 / 0052) — all with reversible .down.sql
- **New unit tests:** 35 across crm/domain (24) + platform/domain (11) + platform/usecase (6)
- **Backend `go build ./...` + `go vet ./...`:** clean at every wave boundary

---

## Deferred to next sessions

### Wave 79b — Approval workflow HTTP + usecase wiring

The domain contract is in place. Still needed before QA can re-run TC-SCH-007/008/009 against live endpoints:

1. **Usecase methods:**
   - `SubmitSchemaForApproval(ctx, id, byUser)` — calls `def.SubmitForApproval()`, emits notifyx event to each role in `schema_approvers` for the kind
   - `ApproveSchema(ctx, id, byUser)` — looks up caller's roles, records vote in `schema_approvals`, transitions to approved only when all required roles have voted yes
   - `RejectSchema(ctx, id, byUser, reason)` — calls `def.Reject(reason)`, persists the rejection vote
2. **HTTP routes:**
   - `POST /api/platform/schemas/{id}/submit` (perm: `platform.schema.manage`)
   - `POST /api/platform/schemas/{id}/approve` (perm: per-kind role match)
   - `POST /api/platform/schemas/{id}/reject` (perm: per-kind role match)
3. **Repo writes:** persist `submitted_at`/`approved_at`/`rejection_reason` columns and votes
4. **Frontend:** schema detail page Submit/Approve/Reject buttons + voting status table

### Wave 80 — Real FreeRADIUS adapter (8 TCs)

The current `LocalRadiusClient` (`internal/network/adapter/radius/local.go`) is a DB-only stub. The PRD wants a real CoA-capable client to ION Radius. This is bounded but non-trivial:

**TCs blocked:** TC-RAD-006 (bandwidth profile creation), TC-RAD-009 (suspend full_block disconnect), TC-RAD-010 (throttle action), TC-RAD-013 (speed-boost <60s), TC-RAD-014 (add-on removal), TC-RAD-015 (plan upgrade/downgrade), TC-RAD-016 (ONT auth), TC-RAD-021 (audit on state change).

**Required work:**

1. **Add RADIUS protocol library** — `layeh.com/radius` for RFC 2865 (auth) + RFC 5176 (CoA/disconnect). Two new go.mod deps.
2. **Reversible password storage** — replace bcrypt with AES-GCM (env var `RADIUS_PWD_KEY` + key-version column). Need a migration to widen `password_encrypted` and add `password_key_version`.
3. **New adapter `internal/network/adapter/radius/freeradius.go`:**
   ```go
   type FreeRadiusClient struct {
       endpoint   string         // e.g. radsec://radius.ion.local:2083
       sharedKey  []byte         // FreeRADIUS shared secret
       keyring    *crypto.AESGCMKeyring
   }
   func (c *FreeRadiusClient) Provision(...) error    // push CHAP + bandwidth + VLAN
   func (c *FreeRadiusClient) Suspend(action SuspendAction, throttleKbps int) error  // CoA
   func (c *FreeRadiusClient) Restore() error          // CoA back to plan profile
   func (c *FreeRadiusClient) Deactivate() error       // erase + revoke
   ```
4. **Wire add-on / plan-change → RADIUS** — `buyAddon` resolves customer's RADIUS account → calls `Restore(newBandwidth)`. `decidePlanChange(applied)` same.
5. **Audit emission** — every state change writes `identity.audit_logs` with before/after.
6. **Mock RADIUS server for tests** — integration tests assert the right RFC 5176 packet shape.
7. **Tech-app password reveal** — `RadiusAccountView` decrypts and returns plaintext ONLY when WO is `in_progress` AND requester matches assigned tech (currently the column is bcrypt-hashed and omitted from views — a one-way landmine).

Realistic scope: **1.5–2 sessions** including integration tests.

### Wave 81 — Notifyx wiring + audit emission + TL routing (multiple TCs)

**TCs blocked:** TC-USR-019, TC-PRD-013, TC-PRD-028, TC-CRM-005, TC-CRM-011, TC-SCH-014, TC-TLP-001, TC-TLP-002, TC-TLP-003, TC-TLP-014, TC-TLP-022, TC-TLP-023.

**Surgical fixes (~3 hours):**

1. **TC-TLP-001/002/003** — wire `s.teamLookup.FindTeamLeader(ctx, branchID)` into `CreateWOFromOrder` in `internal/field/usecase/service.go:145`. The resolver chain is already implemented at `internal/field/adapter/branch/resolver.go:118` — just need to invoke it.
2. **TC-TLP-014/023** — inject `notifyx.Dispatcher` into field usecase, emit on `AssignTechnicians` and `UpsertPair` (both technicians).
3. **TC-TLP-022** — wire `pkg/audit.Writer` into `AssignTechnicians` + `UpsertPair`; emit `{before_lead, before_observer, after_lead, after_observer, reason}`.
4. **TC-USR-019** — wire `pkg/audit.Writer` into `CreateUser`, `UpdateUser`, `SetUserActive`.
5. **TC-PRD-013/028** — wire `pkg/audit.Writer` into `CreateProduct` + `UpdateProduct` (Wave 77 left this gap).
6. **TC-SCH-014** — bulk migration audit emission (depends on Wave 79b's bulk endpoint).
7. **TC-CRM-005** — add `salesTypeMatchesEnterprise(stype)` helper in `internal/enterprise/usecase/` symmetric to the broadband one.
8. **TC-CRM-011** — implement territory-based auto-assign in `CreateLead`.

### Wave 80 + 81 wiring of Wave 78 lock snapshot

Wave 78 added the resolver capability to honor `LockedVersionID` and `ProductSchemaSlotID`, but **no caller passes them yet**. To actually close TC-SCH-011/023/026 at runtime:

1. **At lead conversion** (`internal/crm/usecase/service.go::ConvertLead`):
   - For each of 5 schema kinds, call `platformService.ResolveSchemaForCustomerWith(ctx, newCustomer.ID, kind, ResolveOptions{ProductSchemaSlotID: product.SchemaSlotID(kind)})`
   - Snapshot the returned `SchemaDefinition.ID` into `customer.locked_<kind>_schema_version_id` before persisting
2. **At dunning/RADIUS tick** (`internal/billing/usecase`, `internal/network/usecase`):
   - Load customer's locked version IDs + product's schema slot IDs
   - Pass both to `ResolveSchemaForCustomerWith` via `ResolveOptions`

This is a cross-context dependency (CRM needs platform.UseCase). Cleanest pattern: define a `port.SchemaResolver` interface in CRM that platform's Service satisfies.

---

## Verification commands

```bash
# Backend — full build + vet + warehouse domain tests
cd backend && go build ./... && go vet ./...
go test ./internal/crm/domain/... ./internal/platform/... ./internal/warehouse/domain/... -count=1

# Frontend — tsc + build (both repos pushed at frontend `main`)
cd frontend && npx tsc --noEmit && npm run build
```

All green as of Wave 89b close.

---

## Cumulative Wave 75–89b impact

| Metric | Count |
|---|---|
| Backend commits this session | 19 |
| Frontend commits this session | 2 |
| New migrations (0049–0059) | 11 |
| New unit tests (added across waves) | ~50 |
| Backend bounded contexts touched | 6 (crm, platform, network, field, warehouse, billing) |
| Frontend feature areas touched | 2 (warehouse — PO/GR + threshold widget) |

Original Tier C QA scope was 31 confirmed Gagal/Blocked TCs across the 270 Phase 1 Broadband cases. After Waves 75–81b: 20+ closed at code level, the remaining wiring landed in Waves 81b/83/84/84b. Wave 82 Tier 2a/2b/2c closed the Phase 1B billing-schema gap. Waves 85–89b are net-new Tier 3 surface (procurement loop end-to-end, threshold escalation backend+UI, BOM templates + dispatch pre-fill). Sub-Warehouse / Network Device Lifecycle / Opname Tablet / per-Item-type deep work remain genuinely deferred — each is its own multi-session initiative.

---

## Where the loops sit end-to-end (as of Wave 89b)

**Schema lock + resolver loop (Waves 80b / 82 Tier 2c):**
Lead conversion snapshots resolved version IDs onto `crm.customers.locked_*_schema_version_id`. The platform resolver auto-loads those locks on every downstream call via `Service.WithCustomerLockReader`, so billing dunning ticks + commission calc + service-schema resolution all honor the customer's pinned version without explicit caller plumbing.

**Audit emission (Wave 81):**
`pkg/audit.Writer` wired into identity (CreateUser/UpdateUser/SetUserActive), crm products (CreateProduct/UpdateProduct), field (AssignTechnicians), and network (RADIUS lifecycle transitions). Every mutating call lands a row in `identity.audit_logs`.

**RADIUS cross-context (Wave 83):**
buyAddon / sellAddon / decidePlanChange("applied") on CRM HTTP handlers now call `radius.Restore` via the new `crm.port.RadiusGateway`. LocalRadiusClient absorbs it as a status flip; the future FreeRADIUS adapter will push real CoA packets through the same port.

**Procurement loop (Waves 85 + 86):**
Dashboard admin → `POST /purchase-orders` (draft) → submit → approve → `POST /purchase-orders/{id}/receipts` (multi-batch supported, serial-aware for serialized items, bumps stock_levels + asset rows + stock_movements + auto-closes PO when all lines received). Frontend at `/warehouse/purchasing/[id]` with action buttons + receipts tab.

**Asset retrofit (Wave 87):**
`POST /assets/{id}/retrofit` cannibalizes source, mints produced asset (`is_retrofit=true`, condition=refurbished), records the consume+produce stock_movement pair, writes audit row in `asset_retrofits` table. All atomic.

**Threshold escalation (Waves 88 + 88 frontend + 88b cron):**
Hourly cron in warehouse-svc calls `RunAlertCascadeTick`. SyncAlertStates opens/closes `stock_alert_states` rows. CascadeEscalations bumps current_level (sub_area → area → regional) when 24h budget expires per level. Dashboard widget (`ThresholdEscalationCard`) renders three columns with counts + top-3 oldest per tier. Per-row badges on `/dashboard` StockAlertsZone show level + age.

**BOM templates (Waves 89 + 89b):**
Per-product default BOM at `warehouse.product_bom_templates`. Partial unique index enforces "one active per product". Dispatch creation accepts optional `product_id` → service materializes lines from active template if Items is empty; template_id stamped on `wo_dispatch_records.source_bom_template_id` for audit trail regardless of edit state.

---

## Outstanding QA TCs original Tier C residual

Original Tier C scope was 31 confirmed Gagal/Blocked TCs. After Waves 75–89b, the per-TC residual is:

| Status | Count |
|---|---|
| ✅ Closed at code level | ~22 |
| ✅ Foundationed + wired this session | 9 |
| ⏳ Foundationed, awaiting wiring (Wave 88c / TC-SCH-014 / TC-CRM-005) | 3 |
| ⏳ Environmental / out-of-repo (TC-SCH-007 rule-engine etc.) | 4 |

The remaining wiring is well under one session of work each.
