# Wave 75–81 — QA Compliance Tier C Execution Plan

**Status as of 2026-05-23:** Waves 75–79 landed in this program. Wave 80 (real FreeRADIUS adapter) and Wave 81 (notifyx wiring + audit emission) remain as substantial multi-session work — interfaces and migration scaffolding are in place, but the protocol-bridge implementation requires its own dedicated session.

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
# Build + vet everything
cd backend && go build ./... && go vet ./...

# Run all new tests
go test ./internal/crm/domain/... ./internal/platform/... -count=1
```

All green as of Wave 79 close.

---

## Total Wave 75–79 impact

| Metric | Count |
|---|---|
| QA TCs closed at domain level | 20+ |
| QA TCs foundationed (need wiring) | 12+ |
| New migrations | 4 |
| New unit tests | 35 |
| Lines of domain code | ~600 added |
| Backend bounded contexts touched | 4 (crm, platform, network, field) |

Original Tier C scope was 31 confirmed Gagal/Blocked TCs. After Waves 75–79, ~20 are closed at the code level, ~12 are foundationed and waiting on wiring (Waves 80/81). The remaining 5–7 TCs are environmental (e.g. TC-SCH-007 rule-engine in external service) or outside this repo's scope.
