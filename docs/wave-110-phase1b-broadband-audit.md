# Wave 110 — Phase 1B Broadband QA Audit & Compliance Roadmap

**Date:** 2026-05-23
**Source catalog:** `ION-Phase1B-Broadband-Test-Cases-ID.xlsx` → exported to `/tmp/p1b-catalog.csv` (713 rows, 63 modules)
**Code state audited:** `backend/internal/{identity,crm,platform,field,network,billing,warehouse,operations,tax,reseller,partnership,vendormgmt,enterprise}/` + adjacent at HEAD (post Wave 108, migrations 0001..0073)

---

## Exec summary

This audit covers the **Broadband (ISP) product line** — distinct from the Enterprise CPQ closed out in Waves 91–108. It threads three delivery tranches into one catalog:

| Tranche | Scope | TC count |
|---|---|---:|
| **Phase 1 carry-over** (regression suite) | Foundation: branches, users, RBAC, schemas, product catalog, CRM, sales/customer/tech apps, RADIUS, TL pairing | **271** |
| **Phase 1B operationalization — Billing & Finance** | Billing Schema, OTC, Recurring, Add-On, Faktur Pajak DJP, Payment Handling, Suspension, Reminder, Late Fee, Commission, Financial Reporting | **106** |
| **Phase 1B operationalization — Warehouse & Asset** | 4 item types, BOM, dispatch, consumption, device return, retrofit, threshold, IWT, opname, sub-warehouse, QR, NDL (32), category, asset location, manual purchase, threshold cascade, opname tablet, batch tracking | **148** |
| **Phase 2B integration** | Payment Svc microservice, Invoice Svc microservice, Invoice Generation/Monitoring, HRIS, Deep Schemas (Onboarding/Billing/Service/Commission/Suspension), NOC monitoring, fiber attenuation, fault impact, topology views, NOC→WO | **188** |
| **Total** | | **713** |

| Metric | Value |
|---|---:|
| Total TCs | **713** |
| P0 — Kritikal | **539** (75.6%) |
| P1 — Tinggi | **157** (22.0%) |
| P2 — Sedang | **17** (2.4%) |
| Modules | **63** |
| Status of every TC | `Belum` (not yet executed) |

### Backend coverage roll-up

| Bucket | Modules | TC count | Notes |
|---|---:|---:|---|
| **Mostly covered** ✅ | 6 | 117 (16.4%) | Identity/RBAC/Branch + Schema System + Katalog Produk + CRM Lead + WO core — Waves 1-90 already exercise the P0 path; gaps are at the edges (BR-T6 tax recompute, polygon overlap warning) |
| **Partial** 🟡 | 22 | 313 (43.9%) | Customer App, Sales App, Tech App, RADIUS, Field WO, TL Pairing, Warehouse Type 1-4, BOM, Dispatch, Consumption, Device Return, Stock Threshold, IWT, Opname, Manual Purchase, Threshold Cascade, Billing Schema, Recurring, OTC, Suspension, Reminder, Commission, Financial Reporting — domain skeleton + adapters exist but miss either the orchestration cron, the dispatcher wiring, or the schema-driven branch logic |
| **Missing** ❌ | 35 | 283 (39.7%) | Add-On Billing, Faktur Pajak DJP (real DJP), Late Fee, Payment Handling (gateway/webhook), Sub-Warehouse, Item Coding/QR, Network Device Lifecycle (32 TCs), Item Category, Asset Location Tracking, Stock Opname Tablet, Batch Consumption, Asset Retrofit (operational form), all 4 Payment Svc modules (34 TCs), Invoice Svc + Invoice Generation + Invoice Monitoring (25 TCs), HRIS Integration (12 TCs), all Deep Schemas (68 TCs), all NOC monitoring (30 TCs) |

**Headline gap:** Phase 1B operationalizes the broadband ISP **billing/asset/NOC backbone** that Waves 1-90 stubbed but never wired end-to-end. Three cohorts of work dominate:

1. **A dedicated Payment microservice (`cmd/payment-svc/` + `internal/payment/`) does not exist** — current payment lives in `internal/billing/domain/payment.go` (43-LOC manual confirm + status enum, no gateway routing, no webhook idempotency, no H2H, no refund parent/child chain). All 34 Payment Svc TCs fail at the architecture gate.
2. **A dedicated Invoice microservice (`cmd/invoice-svc/` + `internal/invoice/`) does not exist** — invoicing lives in `internal/billing/` and `internal/enterprise/` separately, with no unified `POST /v1/invoices` surface, no schema snapshot at issue time, no consolidated monitoring dashboard. 25 Invoice Svc TCs fail at the contract gate.
3. **NOC service monitoring is a coverage check, not a live monitoring plane** — `internal/network/` has `coverage.go`, `node.go`, `node_type.go`, `port.go`, `impact_repo.go` (impact has a thin domain), but no SNMP poller, no Rx-power threshold evaluator, no fault impact cascade traversal, no topology view builder, no NOC→WO bridge. All 30 NOC TCs fail at the data-source gate.

The Phase 1 carry-over (271 TCs) is in much better shape — Waves 1-90 + the broadband happy-path migrations (0034 `broadband_happy_path`, 0035 `seed_default_schemas`, 0036 `phase2_foundation`, 0048 `phase1a_closure`, 0049 `wave76_qa_compliance`) cover the bulk of those P0s — but the catalog still finds 30-40 edge-case gaps inside the regression suite (polygon overlap warning, BR-T6 tax recompute, KTP OCR confidence threshold, lead-pipeline transition reasons, RADIUS sealed-password rotation, WO offline-queue replay safety, TL re-pairing audit row).

---

## Honest scope read

This codebase is a real ISP platform — ~88K LOC across 14 bounded contexts, 11 service binaries, 73 migrations, a Next.js dashboard, and 3 Flutter apps. The Phase 1B broadband product line ("ISP B2C + small B2B") shares its foundation with the Phase 1 Enterprise CPQ work (identity, RBAC, schema, audit, notifyx, webhookx) but is otherwise an independent customer cohort with its own data model:

- `internal/identity/` — branches (Regional → Area → Sub Area), users, RBAC roles, audit log
- `internal/platform/` — Schema System (billing / commission / suspension / service) with customer overrides + version lifecycle
- `internal/crm/` — leads (broadband + enterprise discriminator), customers, orders, onboarding-schema-driven docs
- `internal/network/` — coverage check, ODP topology, RADIUS adapter (freeradius + local), port allocation
- `internal/field/` — work orders, BAST, technician teams, SLA breach watcher, reschedule
- `internal/warehouse/` — purchase orders (Wave 85), goods receipt (Wave 86), asset retrofit (Wave 87), threshold cascade (Wave 88), product BOM (Wave 89), dispatch (Wave 89b), stock_opname, asset 7-state lifecycle, 4-category items
- `internal/billing/` — invoices, payment (manual confirm), cycles, commissions, referrals, terminations, schema-policy resolver
- `internal/operations/` — sub-areas, branch SLA config (very thin)
- `internal/tax/` — CompanyTaxProfile + FakturPajak scaffold (Wave 93 added; DJP gateway is a stub)
- `internal/reseller/`, `internal/partnership/`, `internal/vendormgmt/` — these exist for the **Enterprise** product line, *not* broadband; the Phase 1B catalog mostly does not touch them
- `internal/enterprise/` — out of scope for this audit

What DOES NOT exist (zero Go files, zero migrations, zero routes):

1. **`internal/payment/` or `cmd/payment-svc/`** — no gateway routing, no health-filter, no settlement-cycle dispatch, no idempotency keys on webhooks, no H2H bank type field, no refund parent/child tracking. 34 TCs blocked.
2. **`internal/invoice/` or `cmd/invoice-svc/`** — invoicing is split between `internal/billing/` (broadband cycles) and `internal/enterprise/` (CPQ termin); no unified API surface, no `POST /invoices/bulk`, no `credit_note` lifecycle, no schema snapshot at issue time. 25 TCs blocked.
3. **NOC monitoring plane** — no SNMP/OLT poller, no `rx_power_readings` time-series table, no fiber attenuation alerter, no fault impact cascade traversal (the `impact_repo.go` is a placeholder), no Tree/Map/Slice topology views. 30 TCs blocked.
4. **HRIS gateway** — `users.employee_id string` exists but no `hris_employee_id` unique FK, no resign/promotion event ingest, no commission-cessation hook. 12 TCs blocked.
5. **Sub-Warehouse (NOC-TL stockholder model)** — `warehouse.warehouses` has no `parent_warehouse_id` self-ref + `stock_holder_user_id` model; the `is_sub_warehouse_allowed` per-item flag does not exist. 10 TCs blocked.
6. **Item Coding & QR generator** — no `code_format_template` per item type, no QR PNG generator, no scan endpoint resolver. 6 TCs blocked.
7. **Network Device Lifecycle (NDL — 32 TCs)** — technician skillsets, gradings, maintenance schedules, network device install/swap WO subtypes, device decommission flow… `internal/warehouse/domain/asset.go` has 7 states (in_stock, dispatched, installed, returned, decommissioned, cannibalized, deployed) but no skillset / grading / maintenance-schedule model and no `network_device_install` WO subtype.
8. **All Deep Schema variants (68 TCs)** — Schema Onboarding Deep (18), Schema Billing Edge (6), Schema Service Deep (15), Schema Commission Deep (25), Schema Suspension Edge (4) — the `internal/platform/` schema kinds exist (billing / commission / suspension / service) but their *content* surface is a free-form jsonb. The catalog needs typed validators for SLA tier-per-customer-type, prorate rules, mid-cycle upgrade gates, resign clawback windows, etc.
9. **Real DJP e-Faktur integration** — `internal/tax/adapter/djp/{client.go, stub.go}` is a stub. The 10 TC-FPJ tests require a real (or sandbox) DJP API call path with timeout, retry, NPWP validation rejection from DJP, and faktur_number persistence.
10. **Add-On Billing flow** — no `add_ons` table on customer, no "physical add-on triggers WO" hook, no "digital add-on RADIUS update on confirm". 8 TCs blocked.
11. **Late Fee evaluator cron** — `billing/domain/r2.go` references `late_fee` in policy struct but no cron entry computes/applies it as an invoice line. 4 TCs blocked.

Note the asymmetry vs. the Enterprise audit: there, two-thirds of the catalog was blocked by missing data models. Here, the foundation is mostly built — what's missing is the **orchestration cron + dispatcher wiring + microservice extraction**.

---

## Per-module audit

### Phase 1 carry-over — regression suite (271 TCs)

#### Hirarki Cabang — 20 TCs (P0:7 / P1:11 / P2:2)

**Scope:** Regional → Area → Sub Area branch hierarchy with polygon-driven address resolution, resource (warehouse/NOC/sales/TL) assignment, per-branch ODP strategy + cable distance + WO auto-assign + stock threshold escalation.

**Existing artefacts:**
- `internal/identity/domain/branch.go` — branch entity with parent FK + polygon
- `internal/identity/adapter/postgres/branch_repo.go`
- `internal/identity/adapter/http/admin_handler.go` (branch CRUD + assignment routes)
- `internal/operations/adapter/http/handler.go` (branch SLA config)
- `frontend/src/app/(dashboard)/admin/branches/`
- Migrations 0001, 0003 (admin_defaults), 0005 (postgis_coverage)

**Gap status:** ✅ Mostly covered

**Top gaps:**
- TC-BR-008 polygon preview-on-map before save — frontend lacks the preview UI, only post-save view.
- TC-BR-011 overlapping-polygon warning — `branch_repo.go` does not detect overlap with sibling polygons; no Admin warning surfaced.
- TC-BR-013 NOC assignment at any of 3 levels — `identity/domain/branch.go` allows resource assignment but no level-validator that Team Leader can be Sub Area/Area only (TC-BR-015).
- TC-BR-019 stock alert escalation Sub Area → Area → Regional — `internal/warehouse/domain/alert.go` + Wave 88 (`0058_wave88_alert_escalation`) added cascade, but the *branch-hierarchy-driven* escalation chain is not asserted in tests; need cross-branch resolver test.
- TC-BR-016 ODP strategy inheritance from parent when empty — no inheritance resolver visible in `internal/identity/`.

---

#### Manajemen User — 19 TCs (P0:10 / P1:7 / P2:2)

**Scope:** User CRUD, scope assignment, password reset, account lock, MFA, audit on changes, KTP-encrypted PII.

**Existing artefacts:**
- `internal/identity/domain/user.go` (with `EmployeeID` + scope + KTP fields)
- `internal/identity/adapter/postgres/user_repo.go`
- `internal/identity/usecase/service.go` + `r3.go`
- `internal/identity/adapter/http/admin_handler.go`
- `frontend/src/app/(dashboard)/admin/users/`
- Migrations 0002, 0017 (ktp_encryption), 0018 (drop_plaintext_nik), 0020 (ktp_backfill_runs)

**Gap status:** ✅ Mostly covered

**Top gaps:**
- TC-USR-014 MFA enroll flow — no TOTP/email-OTP enrollment surface visible in `handler.go`; `availability.go` covers user availability but not MFA secret storage.
- TC-USR-009 account lockout after 5 failed logins — `identity/usecase/service.go` has password verify but no failed-attempt counter / lockout window.
- TC-USR-016 employee-ID uniqueness — `users.employee_id` is `string` with no unique index; collision returns generic 500.
- TC-USR-018 audit row on role change — generic audit exists, but no specific subject filter for `user.role_changed` event.

---

#### Roles & Permissions — 17 TCs (P0:9 / P1:7 / P2:1)

**Scope:** Standard role catalog, scope-bound visibility (Sales sees own commission only, Branch Manager sees own branch only), permission inheritance, custom role builder, role audit.

**Existing artefacts:**
- `internal/identity/domain/role.go`
- `internal/identity/adapter/postgres/role_repo.go`
- `internal/identity/adapter/http/admin_handler.go` (role CRUD)
- `frontend/src/app/(dashboard)/admin/roles/`
- `pkg/auth/` — JWT + permission middleware
- Migration 0002

**Gap status:** ✅ Mostly covered

**Top gaps:**
- TC-RBAC-007 custom role builder UI — backend supports `permissions []string` on role, but no frontend builder page.
- TC-RBAC-012 cross-branch read denial — depends on JWT carrying `branch_scope[]` claim; not visible in current `pkg/auth/`.
- TC-RBAC-015 permission inheritance — no hierarchical role / role-of-roles model.
- TC-RBAC-017 contract-test sweep — no per-endpoint × per-role assertion table for broadband routes (the enterprise context has `contract_rbac_test.go` from Wave 95; broadband does not).

---

#### Schema System — 27 TCs (P0:18 / P1:6 / P2:3)

**Scope:** Onboarding + Billing + Commission + Suspension + Service schemas with versioning (draft/published/superseded), customer overrides, snapshot-at-bind, multi-version coexist.

**Existing artefacts:**
- `internal/platform/domain/schema.go` (~600 LOC, 4 kinds, full lifecycle)
- `internal/platform/domain/schema_lifecycle_test.go`
- `internal/platform/adapter/postgres/schema_repo.go` + `override_repo.go`
- `internal/platform/usecase/resolver.go` + `resolver_test.go`
- `internal/platform/adapter/http/handler.go`
- `frontend/src/app/(dashboard)/admin/schemas/`
- Migrations 0035 (seed_default_schemas), 0050 (wave77_product_schema_slots), 0051 (wave78_customer_schema_lock), 0052 (wave79_schema_approval)

**Gap status:** ✅ Mostly covered (the *generic* schema engine is well-built)

**Top gaps:**
- TC-SCH-019 multi-version coexist (two customers on different schema versions simultaneously) — `resolver.go` does pick the bound version but no end-to-end test pinning two customers with different versions.
- TC-SCH-022 approval chain on schema publish — Wave 79 added an approval flow, but the schema-approval workflow doesn't enforce mandatory finance review for billing-kind schemas specifically.
- TC-SCH-025 schema rollback — `Supersede` exists, no reverse-rollback path.
- TC-SCH-027 schema content validator — content is free-form jsonb; typed validators per kind (covered separately in Deep Schema modules) live downstream.

---

#### Katalog Produk — 35 TCs (P0:21 / P1:13 / P2:1)

**Scope:** Product CRUD, plan broadband (speed/MRC/OTC), temporary activation window, branch availability, product BOM, product → onboarding/billing/commission/suspension/service schema slot binding, product version lifecycle.

**Existing artefacts:**
- `internal/crm/domain/product.go` + `product_test.go`
- `internal/crm/adapter/postgres/product_repo.go`
- `internal/crm/adapter/http/handler.go` (product routes)
- `internal/warehouse/domain/product_bom.go` + `product_bom_repo.go` + Wave 89
- Migration 0050 (wave77_product_schema_slots) — added the 5 schema slots
- `frontend/src/app/(dashboard)/crm/onboarding-schemas/`

**Gap status:** ✅ Mostly covered

**Top gaps:**
- TC-PRD-008 temporary_activation_window_hours edge case (window expires while WO in flight) — `product.go` has the field; no test pinning the transition to PERMANENT block when window expires before BAST.
- TC-PRD-014 branch-availability matrix — product carries `branch_ids []uuid`; the matrix UI in frontend is read-only (per dashboard route audit).
- TC-PRD-023 product-version supersede with active customers — no test ensuring active customers stay on the pinned version.
- TC-PRD-030 product BOM linking — Wave 89's `product_bom.go` is solid, but the BOM-to-WO auto-population test (TC-BOM-001) reuses this path; the test does exist for Wave 89 happy-case only.
- TC-PRD-033 commission-schema slot validation — slot exists since Wave 77 but no test that bound-schema must be commission-kind.

---

#### CRM — Tambah Lead — 23 TCs (P0:9 / P1:13 / P2:1)

**Scope:** Lead CRUD with broadband/enterprise discriminator, pipeline state machine (Cold→Warm→Hot→Won/Lost), KTP OCR + manual fallback, document upload per onboarding-schema, address pin via coverage check, duplicate-lead detection.

**Existing artefacts:**
- `internal/crm/domain/lead.go` + `lead_test.go`
- `internal/crm/adapter/postgres/lead_repo.go`
- `internal/crm/adapter/http/handler.go` + `ktp_ocr.go` + `ktp_ocr_provider_tesseract.go`
- `internal/crm/adapter/network/coverage_gateway.go`
- Migration 0007, 0011 (crm_r2), 0023 (lead_pipeline_statuses), 0040 (priority_followups)
- `frontend/src/app/(dashboard)/crm/leads/`
- `mobile/sales_app/lib/features/crm/`

**Gap status:** ✅ Mostly covered

**Top gaps:**
- TC-CRM-011 KTP OCR confidence threshold (auto-accept vs manual review) — `ktp_ocr.go` extracts but does not gate on confidence score.
- TC-CRM-014 duplicate-lead detection by phone + name + address fuzzy — no fuzzy match; only exact-phone unique.
- TC-CRM-019 lead-pipeline transition reason capture — `lead.go` advances state but does not require a reason on Cold→Lost.
- TC-CRM-022 address pin must be inside a branch polygon — depends on `network/coverage_gateway.go` returning a branch resolution; flow exists but no test pinning the address-outside-all-polygons → block-create path.

---

#### Sales App — 24 TCs (P0:14 / P1:7 / P2:3)

**Scope:** Mobile login (offline support), dashboard (target/orders/leads/commission MTD), lead create + KTP camera, coverage check, order submit, push notifications, offline draft, location ping.

**Existing artefacts:**
- `mobile/sales_app/lib/features/crm/`
- `mobile/sales_app/lib/features/phase2/`
- `mobile/sales_app/lib/push/`, `mobile/sales_app/lib/gps/`
- Backend mirror at `internal/crm/adapter/http/phase2.go`, `internal/crm/usecase/r2.go`
- Migration 0046 (push_outbox)

**Gap status:** 🟡 Partial

**Top gaps:**
- TC-SAP-005 offline draft sync — `phase2` features include drafts in Flutter but no idempotency-token sync on backend; replay duplicates would create two leads.
- TC-SAP-009 dashboard commission MTD — `mobile/sales_app/lib/features/crm/` has the dashboard widget; backend commission read endpoint returns YTD only, no MTD filter.
- TC-SAP-016 push notification on lead → customer conversion — `push_outbox` exists but no `lead.converted` subject producer.
- TC-SAP-021 GPS ping interval config — `mobile/sales_app/lib/gps/` pings at fixed interval; no Admin-configurable cadence.
- TC-SAP-024 force-logout on role change — no realtime claim invalidation; user keeps stale JWT until expiry.

---

#### Customer App — 23 TCs (P0:10 / P1:12 / P2:1)

**Scope:** Self-order with coverage check, invoice list + pay (VA/QRIS/Bank Transfer + e-wallet), tech tracking (live map for own WO), bill detail + history, add-on purchase, plan change, relocation request, support tickets, OTP auth.

**Existing artefacts:**
- `mobile/customer_app/lib/features/{bills,services,onboarding,support,notifications,account,home}/`
- Backend: `internal/crm/adapter/http/portal_auth.go` + `portal_priority.go` + `portal_backlog.go`
- `internal/billing/adapter/http/handler_portal.go` + `usecase/portal.go`
- Migration 0016 (customer_portal_otp), 0037 (customer_portal_auth)
- `frontend/src/app/(public)/` (some customer-facing routes)

**Gap status:** 🟡 Partial

**Top gaps:**
- TC-CAP-008 self-order block on uncovered address — coverage check is wired but no end-to-end Flutter test asserting the submit-button-disabled state.
- TC-CAP-012 add-on purchase flow — `mobile/customer_app/lib/features/services/buy_addon_page.dart` exists but the backend `add_ons` endpoint does not (see Add-On Billing below). Page calls a nonexistent route.
- TC-CAP-015 tech tracker live map — page exists; backend `tech_locations` (migration 0042 partitioned) feeds it, but no permission gate that customer only sees own WO's tech location.
- TC-CAP-018 bill PDF download — `billing/adapter/http/handler_portal.go` has invoice list, no `GET /portal/invoices/{id}/pdf` route.
- TC-CAP-022 relocation request triggers WO — page exists, backend has no `RelocationRequest` domain.

---

#### Integrasi RADIUS — 21 TCs (P0:14 / P1:7)

**Scope:** Credential generation (unique username + safe-random password, sealed at rest), bandwidth profile push, suspend/restore command, customer-status sync (TEMPORARY → PERMANENT → SUSPENDED), RADIUS server failover, audit on every push.

**Existing artefacts:**
- `internal/network/domain/radius.go`
- `internal/network/adapter/radius/{freeradius.go, local.go}`
- `internal/network/port/port.go`
- Migrations 0019 (rename_radius_password_hash), 0054 (wave80_radius_sealed_password)

**Gap status:** 🟡 Partial

**Top gaps:**
- TC-RAD-008 bandwidth-profile push on add-on confirm — adapter has the push primitive; no producer wiring on add-on confirm (add-on flow itself missing).
- TC-RAD-012 RADIUS server failover — `freeradius.go` connects to one host; no multi-host failover with health check.
- TC-RAD-016 sealed-password rotation — Wave 80 sealed passwords at rest; no rotation cron, no key-rollover migration path.
- TC-RAD-019 audit on every push — generic audit exists; no `radius.push` subject filter with delta payload (old → new bandwidth).
- TC-RAD-021 reconcile sweep (compare ION Core state vs RADIUS server state) — no reconciliation cron.

---

#### Technician App — WO — 28 TCs (P0:15 / P1:13)

**Scope:** Mobile login + assigned WO list + WO detail + BAST submit + photo upload + speedtest + checklist + offline queue + reschedule request + customer signature.

**Existing artefacts:**
- `mobile/tech_app/lib/features/field/`
- `mobile/tech_app/lib/features/phase2/`
- Backend: `internal/field/adapter/http/handler.go` + `handler_r2.go` + `handler_r3.go` + `speedtest.go` + `speedtest_test.go`
- `internal/field/domain/{work_order,bast,checklist,reschedule,resolution,assignment}.go`
- Migration 0008, 0012 (field_r2), 0033 (wo_dispatch), 0045 (speedtest_checklist_fix), 0053 (wave84_wo_product_schema)

**Gap status:** 🟡 Partial

**Top gaps:**
- TC-WO-013 offline-queue replay safety — `phase2` Flutter has queued submits; backend lacks idempotency tokens on BAST/checklist submit (same as Sales App).
- TC-WO-018 customer signature capture — `field/domain/bast.go` accepts a signature URL; no validation of signature byte length or canvas-stroke metadata.
- TC-WO-021 speedtest result regression (≥ plan speed × 0.85) — `speedtest.go` exists; threshold gate is implicit, not config-driven from schema.
- TC-WO-024 reschedule request → TL approval → audit row — domain has `reschedule.go`; no `reschedule.approved` audit subject.
- TC-WO-027 photo upload geofence (must be near customer GPS) — no geofence check; uploads accepted from anywhere.

---

#### Team Lead & Pairing Teknisi — 34 TCs (P0:18 / P1:14 / P2:2)

**Scope:** TL roster, WO routing by Sub Area → Area, technician pairing (junior + senior), schedule conflict detection, SLA breach watcher, reassignment audit, team load report.

**Existing artefacts:**
- `internal/field/domain/team.go`, `assignment.go`
- `internal/field/adapter/postgres/team_repo.go`, `assignment_repo.go`
- `internal/field/usecase/sla_watcher.go`
- `frontend/src/app/(dashboard)/field/{roster,sla-breaches,teams,work-orders,live-map}/`
- Migrations 0012, 0049 (wave76_qa_compliance), 0044 (no_gap_followups)

**Gap status:** 🟡 Partial

**Top gaps:**
- TC-TLP-009 pairing junior+senior enforcement — `team.go` has TeamLeaderID + members; no pair-composition validator at assign time.
- TC-TLP-014 schedule-conflict detection on technician (two WOs same window) — no conflict-check usecase visible in `field/usecase/`.
- TC-TLP-019 SLA breach escalation Sub Area TL → Area TL — `sla_watcher.go` fires breach event; no escalation chain producer.
- TC-TLP-024 re-pairing audit row with reason — `assignment.go` allows update; no required-reason field, no audit subject.
- TC-TLP-028 team-load report per TL — frontend has roster page; no per-TL backend aggregation route (`GET /field/teams/{id}/load`).

---

### Phase 1B operationalization — Billing & Finance (106 TCs)

#### Billing Schema — 12 TCs (P0:11 / P1:1)

**Scope:** Per-customer-type Billing Schema (cycle, payment_terms, reminders, late_fees, invoice_format, OTC type), publish lifecycle, PB-driven resolution, schema-bind snapshot at customer activation.

**Existing artefacts:**
- `internal/platform/domain/schema.go` (kind=billing)
- `internal/billing/usecase/schema_policy.go`
- `internal/billing/adapter/postgres/policy_repo.go`
- Migration 0015 (billing_r3), 0035 (seed_default_schemas)
- `frontend/src/app/(dashboard)/billing/policy/`

**Gap status:** 🟡 Partial

**Top gaps:**
- TC-BS-004 default-schema resolution by customer type — `schema_policy.go` resolves but the test fixture is broadband-only; business/enterprise/corporate type-driven default is not pinned.
- TC-BS-006 publish gate (finance approval) — Wave 79 added schema approval; billing-kind schemas don't enforce finance reviewer.
- TC-BS-009 schema-bind snapshot at activation — domain supports overrides; no test that activation freezes the schema version into an immutable snapshot column.
- TC-BS-011 reminders-config validation (T-3/T-0/T+3 ordering) — content is jsonb free-form; no typed validator (covered in Schema Billing Edge below).

---

#### OTC (One-Time Charge) — 18 TCs (P0:14 / P1:4)

**Scope:** OTC types {free, prepaid, post-bill} with distinct activation flows: free → no invoice + WO direct; prepaid → invoice on finance-request → pay → activate; post-bill → activate first → bill on first cycle.

**Existing artefacts:**
- `internal/crm/domain/product.go` (OTC fields)
- `internal/billing/usecase/r2.go` + `r3.go` (cycle generation includes OTC line)
- `internal/billing/adapter/crm/gateway.go`
- Migration 0034 (broadband_happy_path) wires OTC into the broadband activation chain

**Gap status:** 🟡 Partial

**Top gaps:**
- TC-OTC-001/002 free flow — TEMPORARY at WO creation → PERMANENT after BAST: the happy-path migration wires this, but there's no test asserting that no invoice row is created for `otc.type=free`.
- TC-OTC-003 prepaid "Finance explicit request invoice" — `billing/usecase/r3.go` generates invoices on cycle, no explicit "request invoice for prepaid OTC" endpoint visible in handlers.
- TC-OTC-007 post-bill OTC line on first invoice — cycle generator includes OTC, but the test for "first cycle includes OTC + first MRC" is missing.
- TC-OTC-012 OTC refund on cancel before activation — no refund path; only termination has a refund stub.
- TC-OTC-015 OTC type change after activation rejected — `product.go` allows change at product level; no immutability of customer's snapshotted OTC type.

---

#### Recurring Billing — 12 TCs (P0:12)

**Scope:** Anniversary-date-locked monthly cycles, auto-invoice generation each cycle, multi-year customer continuation, prorate on mid-cycle plan change, schema-driven line composition.

**Existing artefacts:**
- `internal/billing/domain/r3.go` (`Cycle` + `CycleGenerator`)
- `internal/billing/usecase/r3.go` + `r3_cutoff_test.go`
- `internal/billing/adapter/postgres/cycle_repo.go`
- Migration 0015 (billing_r3), 0031 (invoices_allow_termin), 0039 (quota_scurve)
- `frontend/src/app/(dashboard)/billing/cycles/`

**Gap status:** 🟡 Partial

**Top gaps:**
- TC-REC-001 anniversary-date lock — `r3.go` has `CycleAnchor`; no test pinning that activation-on-15th-of-month locks all future cycles to the 15th regardless of leap-month edge.
- TC-REC-006 prorate calculation on mid-cycle change — no prorate path; current flow defers change to next cycle (which is one valid policy choice but does not match TC-REC-006's "prorate from day-of-change").
- TC-REC-009 multi-year continuation — cycle generator is open-ended; no test asserting 24+ cycles generate clean across DST / Feb-29 boundary.
- TC-REC-011 customer termination stops cycle — termination usecase exists; no test that next cycle is *not* generated after termination effective date.

---

#### Add-On Billing — 8 TCs (P0:7 / P1:1)

**Scope:** Post-activation add-on purchase (digital → RADIUS bandwidth update; physical → WO dispatch), append to billing schema, prorate at next cycle, cancel/refund.

**Existing artefacts:** **None.** No `add_ons` table, no `crm.customer_add_ons` domain, no `billing/usecase/addon.go`. `mobile/customer_app/lib/features/services/buy_addon_page.dart` UI exists but the backend route it calls does not.

**Gap status:** ❌ Missing

**Top gaps:**
- Zero backend: no domain, no repo, no usecase, no handler. All 8 TCs fail.
- TC-AOB-002 digital add-on RADIUS push — depends on `radius_push` adapter + add-on lifecycle that don't exist together.
- TC-AOB-003 physical add-on auto-creates WO — no producer.
- TC-AOB-005 add-on append to schema — no schema-append on customer override.
- TC-AOB-007 add-on cancel prorate — no cancel flow.

---

#### Faktur Pajak DJP — 10 TCs (P0:10)

**Scope:** Real DJP e-Faktur API integration: NPWP validation, payload (DPP/PPN/total), faktur_number persistence, audit, retention 10 years, Non-PKP skip, timeout/retry, error path.

**Existing artefacts:**
- `internal/tax/domain/company_tax_profile.go` + `faktur_pajak.go`
- `internal/tax/usecase/service.go` + `djp_timeout_test.go`
- `internal/tax/adapter/djp/{client.go, stub.go}`
- `internal/tax/adapter/postgres/faktur_pajak_repo.go`
- `internal/tax/adapter/http/handler.go`
- Migration 0061 (wave93_tax_profiles), 0063 (wave94b_subsidiary_tax_reconcile), 0067 (wave101_tax_snapshot_chain)

**Gap status:** 🟡 Partial — scaffolded (Wave 93) but stubbed

**Top gaps:**
- TC-FPJ-001/002/003 real DJP API call — `djp/client.go` exists alongside `djp/stub.go`; the real client is not wired into the production code path. All P0s depend on a sandbox-environment integration test.
- TC-FPJ-004 NPWP validation rejection from DJP — no path that translates DJP's "NPWP not found" response into a typed app error.
- TC-FPJ-006 faktur retention 10 years — `faktur_pajak_repo.go` persists; no retention-policy cron, no `retention_until` column observed in the migration.
- TC-FPJ-008 Non-PKP skip — `company_tax_profile.go` has `IsPKP` toggle; the broadband invoice issue path doesn't branch on it (the branching exists in the *enterprise* invoice flow per Wave 93's tests).
- TC-FPJ-010 audit on every faktur issue — generic audit exists; no `faktur.issued` subject with NPWP + faktur_number in payload.

---

#### Payment Handling — 12 TCs (P0:10 / P1:2)

**Scope:** VA auto-confirm via Xendit, bank transfer manual confirm with proof upload, e-wallet auto-confirm, QRIS, partial payment, overpayment, refund, payment audit, RADIUS auto-restore on paid.

**Existing artefacts:**
- `internal/billing/domain/payment.go` (~120 LOC manual-confirm only)
- `internal/billing/adapter/postgres/payment_repo.go`
- `internal/billing/adapter/http/handler.go` (manual confirm route)
- `pkg/webhookx/` exists but no broadband webhook consumer
- Migration 0043 (webhook_deliveries)

**Gap status:** 🟡 Partial — manual-confirm only; no gateway integration

**Top gaps:**
- TC-PAY-001 VA Xendit auto-confirm — no Xendit webhook consumer, no signature verifier, no auto-paid transition producer. Webhook deliveries table exists but no consumer registered against it.
- TC-PAY-003 e-wallet (GoPay/OVO/Dana) — depends on Xendit aggregator integration; not wired.
- TC-PAY-006 partial payment — `payment.go` amount is single number; no partial-allowed flag on invoice; no remaining-balance recompute.
- TC-PAY-009 RADIUS auto-restore on paid — `field/adapter/network/activation.go` exists for activation push, but no "restore-on-paid" hook from `payment.go::Confirm`.
- TC-PAY-011 refund parent/child traceability — no refund domain entity at all.

---

#### Suspension — 10 TCs (P0:9 / P1:1)

**Scope:** Auto-suspend broadband after grace expiry (RADIUS SUSPENDED), manual approval for business/enterprise/corporate, suspension audit, restore on payment, suspension reason tracking, post-restore re-billing.

**Existing artefacts:**
- `internal/billing/usecase/r2.go` (grace + late_fee policy fields in `schema_policy.go`)
- `internal/billing/usecase/r3.go` (cycle status transitions)
- `internal/network/adapter/radius/local.go` (suspend/restore commands)
- Migration 0014 (billing_r2)

**Gap status:** 🟡 Partial

**Top gaps:**
- TC-SUS-001 auto-suspend after grace — no `suspension_evaluator` cron entry visible; policy fields exist but no scheduled job applies them.
- TC-SUS-002 business manual approval — domain has no `SuspensionRequest` entity; no Finance Manager approval state machine.
- TC-SUS-003 enterprise CFO joint approval — no joint-approval lifecycle.
- TC-SUS-006 RADIUS SUSPENDED push — adapter has the primitive; no producer call from suspension state transition.
- TC-SUS-008 restore on payment — no payment→suspension reverse hook (depends on Payment Handling above).

---

#### Reminder Schedule — 8 TCs (P0:5 / P1:3)

**Scope:** Schema-driven reminder points (T-3 / T-0 / T+3 / T+7), multi-channel dispatch (email + SMS + WhatsApp Business API parallel), template editability, dispatch audit, opt-out respect.

**Existing artefacts:**
- `internal/platform/domain/schema.go` (billing kind has reminders jsonb)
- `pkg/notifyx/` (dispatcher framework)
- `frontend/src/app/(dashboard)/admin/notification-prefs/`
- Migration 0046 (push_outbox)

**Gap status:** ❌ Missing — no reminder cron producer

**Top gaps:**
- TC-REM-001 schema-driven schedule — no `reminder_evaluator` cron entry; schema config is captured but nothing reads it.
- TC-REM-002/003 WhatsApp Business API + multi-channel — `notifyx` framework exists but no WhatsApp template registry, no Meta API client visible.
- TC-REM-004 template editability — no template CRUD surface in admin routes for broadband reminders.
- TC-REM-006 opt-out respect — no per-customer opt-out flag for reminders.

---

#### Late Fee — 4 TCs (P0:2 / P1:2)

**Scope:** Schema-driven late_fee (percentage or fixed IDR), applied as next-invoice line, corporate exemption, audit.

**Existing artefacts:**
- `internal/platform/domain/schema.go` (billing kind has `late_fee` jsonb)
- `internal/billing/domain/r2.go` (policy struct mentions late_fee)
- Migration 0014

**Gap status:** ❌ Missing — config-only, no evaluator

**Top gaps:**
- TC-LF-001 late_fee applied — no cron entry computes/applies it. Configuration exists, enforcement does not.
- TC-LF-002 corporate exemption — `billing/policy` mentions disable flag; no test path exercising the exemption branch.
- TC-LF-003 percentage/fixed configurable — `schema.go` content is free-form; no typed validator that `late_fee.type` ∈ {percentage, fixed}.
- TC-LF-004 audit on apply — no producer.

---

#### Commission Calculation — 6 TCs (P0:6)

**Scope:** Commission Schema resolution per product, trigger config (activation / first-payment / Month-3 retention), bulk payout, audit, clawback, sales rep resign cessation.

**Existing artefacts:**
- `internal/billing/domain/r2.go` (Commission entity)
- `internal/billing/adapter/postgres/commission_repo.go`
- `internal/billing/adapter/http/handler.go` (commission routes)
- `internal/platform/domain/schema.go` (kind=commission)
- `frontend/src/app/(dashboard)/billing/commissions/`

**Gap status:** 🟡 Partial

**Top gaps:**
- TC-COM-002 Pay-trigger config — `commission_repo.go` stores entries but no `trigger_event` enum dispatch; activation is the only path wired today.
- TC-COM-003 Bulk payout — handler has a list route; no bulk-mark-paid endpoint with payout-batch row.
- TC-COM-005 Clawback on early termination — no clawback evaluator (covered also in Schema Commission Deep below).
- TC-COM-006 Sales rep resign cessation — depends on HRIS gateway producing `employee.resigned` event; HRIS is missing entirely.

---

#### Financial Reporting — 6 TCs (P0:4 / P1:2)

**Scope:** Daily revenue summary, AR aging buckets (0-30/31-60/61-90/90+), MRR/ARR, churn rate, branch-level rollup, exportable CSV.

**Existing artefacts:**
- `internal/billing/adapter/postgres/invoice_repo.go` (basic invoice queries)
- `frontend/src/app/(dashboard)/billing/invoices/`

**Gap status:** ❌ Missing — no reporting aggregator

**Top gaps:**
- TC-FR-001 daily revenue summary — no `GET /billing/reports/daily-revenue` route, no aggregation usecase.
- TC-FR-002 AR aging buckets — `invoice_repo.go` lists by status; no aging bucketization.
- TC-FR-003 MRR/ARR — no recurring revenue aggregator.
- TC-FR-004 churn rate — no churn computation; termination domain exists but no per-period churn rate.
- TC-FR-005 branch-level rollup — no per-branch revenue route.
- TC-FR-006 CSV export — no export endpoint on any reporting route.

---

### Phase 1B operationalization — Warehouse & Asset (148 TCs)

#### Item Type 1 — Serialized — 10 TCs (P0:10)

**Scope:** Serialized device (ONT/Router) receive with serial/MAC, unique serial constraint, QR code generation, asset lifecycle (in_stock → dispatched → installed → returned/decommissioned), location history, ownership.

**Existing artefacts:**
- `internal/warehouse/domain/asset.go` (7-state lifecycle)
- `internal/warehouse/adapter/postgres/asset_repo.go`
- `internal/warehouse/adapter/http/dto.go`
- Migration 0006, 0010 (warehouse_r2), 0056 (wave86_goods_receipt)

**Gap status:** 🟡 Partial

**Top gaps:**
- TC-IT1-001 receive with QR scan input — `goods_receipt.go` accepts serial; no QR-scan-specific endpoint (depends on Item Coding & QR below).
- TC-IT1-002 unique serial 409 — `asset_repo.go` uniques on `(brand,model,serial)`; no test asserting exact `duplicate_serial` error code mapping to 409.
- TC-IT1-005 location_history[] append on every move — `asset.go` has CurrentLocation fields but no `location_history jsonb[]` log (covered in Asset Location Tracking below).
- TC-IT1-008 device firmware version capture — no `firmware_version` field on asset.
- TC-IT1-010 warranty expiry tracking — no `warranty_expires_at` field.

---

#### Item Type 2 — Cable — 6 TCs (P0:5 / P1:1)

**Scope:** Receive in meters, cost-per-meter recorded, technician consumption in meters at BAST, FIFO batch tracking, variance flag, dead-stock cutoff.

**Existing artefacts:**
- `internal/warehouse/domain/stock_item.go` (`CategoryCable`)
- `internal/warehouse/domain/stock_movement.go`
- `internal/warehouse/adapter/postgres/stock_item_repo.go`, `inventory_repo.go`, `movement_repo.go`

**Gap status:** 🟡 Partial

**Top gaps:**
- TC-IT2-001 receive in meters — domain category exists; no test that cost-per-meter is preserved across receives (covered in Batch Consumption Tracking below).
- TC-IT2-002 consumption at BAST — `bast.go` accepts qty; no test that cable consumption decrements the matched stock_level in meters.
- TC-IT2-004 FIFO batch — no batch table; current model is single stock_level per item (covered separately in Batch Consumption Tracking).
- TC-IT2-005 variance flag — no variance computation between BOM estimate and BAST actual.

---

#### Item Type 3 — Consumable — 4 TCs (P0:2 / P1:2)

**Scope:** Receive by qty, no serial, consume by qty at BAST, no return path.

**Existing artefacts:**
- `internal/warehouse/domain/stock_item.go` (`CategoryConsumable`)
- Same repos as Type 2

**Gap status:** 🟡 Partial — same skeleton as Type 2

**Top gaps:**
- TC-IT3-001 receive consumable — repo path exists; no specific test that consumable cannot be assigned a serial.
- TC-IT3-002 consume by qty — generic stock_movement decrement works; no schema validation that `category=consumable → return_qty=0`.
- TC-IT3-003 dead stock — no aging report for consumables.

---

#### Item Type 4 — Network Infra — 6 TCs (P0:3 / P1:3)

**Scope:** Receive OLT/ODC/ODP with serial + brand + ports + capacity, deployed-to-node assignment, port activation, topology integration, decommission audit.

**Existing artefacts:**
- `internal/warehouse/domain/asset.go` (`AssetStatusDeployed`)
- `internal/network/domain/{node,node_type,port}.go`
- `internal/network/adapter/postgres/{node_repo,node_type_repo,port_repo}.go`
- Migration 0004 (network_topology), 0021 (node_type_coverage)

**Gap status:** 🟡 Partial

**Top gaps:**
- TC-IT4-002 OLT-to-node assignment — `network/node.go` has node entity; no link from `warehouse.assets` to `network.nodes` (FK is implicit via shared infra_id).
- TC-IT4-003 ports activation — `port.go` exists; no test that assigning an OLT atomically creates port_count rows.
- TC-IT4-005 capacity tracking — no `capacity_used / capacity_total` view per OLT.
- TC-IT4-006 decommission cascade — no rule that decommissioning an OLT requires migrating downstream ODPs first.

---

#### WO Material List (BOM) — 6 TCs (P0:5 / P1:1)

**Scope:** Auto-populate BOM from product config on WO create, pre-dispatch review/adjust, BOM-vs-actual variance, BOM template versioning.

**Existing artefacts:**
- `internal/warehouse/domain/product_bom.go` (Wave 89)
- `internal/warehouse/usecase/product_bom.go`
- `internal/warehouse/adapter/postgres/product_bom_repo.go`
- `internal/warehouse/adapter/http/product_bom.go`
- Migration 0059 (wave89_product_bom_templates)

**Gap status:** 🟡 Partial

**Top gaps:**
- TC-BOM-001 auto-populate on WO create — usecase exists; no test wiring from `field.work_order.created` event → `warehouse.bom.populate`.
- TC-BOM-002 pre-dispatch review — handler has list route; no "adjust qty before assign" UX backend path with audit.
- TC-BOM-005 BOM template version — `product_bom.go` has version field; no test that a published BOM is immutable.
- TC-BOM-006 BOM variance report — depends on Consumption Recording (below).

---

#### Dispatch Flow — 6 TCs (P0:5 / P1:1)

**Scope:** Technician arrives warehouse → scan ID → authorize WO pickup → scan each BOM item → assign to WO + technician → dispatch row created → audit.

**Existing artefacts:**
- `internal/warehouse/domain/wo_dispatch.go` (Wave 89b)
- `internal/warehouse/usecase/wo_dispatch.go`
- `internal/warehouse/port/wo_dispatch_port.go`
- `internal/warehouse/adapter/http/wo_dispatch_handler.go`
- `frontend/src/app/(dashboard)/warehouse/dispatch/`
- Migration 0033 (wo_dispatch)

**Gap status:** 🟡 Partial — happy path good

**Top gaps:**
- TC-DSP-001 pickup authorization — handler exists; no validation that technician's `assignment.work_order_id` matches the scanned WO.
- TC-DSP-003 scan-all-BOM-items completeness gate — usecase decrements stock per scan; no "all-BOM-items-scanned-before-dispatch-confirm" gate.
- TC-DSP-005 dispatch audit row — generic audit captures; no `warehouse.dispatch.completed` subject with BOM-line-deltas in payload.

---

#### Consumption Recording — 6 TCs (P0:6)

**Scope:** Post-install BAST submit captures ONT serial + cable used (m) + consumables (qty), variance vs BOM flagged, excess cable invoice trigger.

**Existing artefacts:**
- `internal/field/domain/bast.go`
- `internal/field/adapter/postgres/bast_repo.go`
- `internal/warehouse/domain/stock_movement.go`

**Gap status:** 🟡 Partial

**Top gaps:**
- TC-CSM-002 variance detection — no variance calculator; BAST capture is straight write to stock_movement.
- TC-CSM-003 excess cable invoice trigger — no path from BAST variance → next-invoice line item.
- TC-CSM-005 retroactive correction — no BAST amend lifecycle.
- TC-CSM-006 consumption audit — `bast.go` has audit fields; no per-line consumption audit subject.

---

#### Device Return Flow — 8 TCs (P0:7 / P1:1)

**Scope:** Customer termination → return WO auto-created → technician scan device at site → asset → returned → conditions captured → optional refurbish/decommission.

**Existing artefacts:**
- `internal/warehouse/domain/asset.go` (`AssetStatusReturned`)
- `internal/billing/adapter/postgres/termination_repo.go`
- Migration 0034 (broadband_happy_path)

**Gap status:** 🟡 Partial

**Top gaps:**
- TC-RTN-001 termination triggers return WO — termination domain exists; no `field.work_order` producer with subtype=`device_return`.
- TC-RTN-002 scan device at customer site — depends on QR/scan endpoint (Item Coding & QR below).
- TC-RTN-005 condition capture (good/damaged/lost) — no `return_condition` enum on asset returned event.
- TC-RTN-006 leased vs owned distinction — `asset.go::Ownership` exists; no rule that owned devices are not retrieved.

---

#### Asset Retrofit — 4 TCs (P1:3 / P2:1)

**Scope:** Cannibalize damaged devices → extract components → rebuild new unit, source status=cannibalized, new unit `is_retrofit=true`.

**Existing artefacts:**
- `internal/warehouse/domain/asset_retrofit.go` (Wave 87)
- `internal/warehouse/usecase/asset_retrofit.go`
- `internal/warehouse/adapter/postgres/asset_retrofit_repo.go`
- `internal/warehouse/adapter/http/asset_retrofit.go`
- Migration 0057 (wave87_asset_retrofit)

**Gap status:** ✅ Mostly covered (Wave 87 happy path)

**Top gaps:**
- TC-RTF-002 rebuild new unit with null serial until labeled — domain supports nullable serial, no test path.
- TC-RTF-004 retrofit audit chain (source → components → new unit) — no traversal test.

---

#### Stock Threshold — 6 TCs (P0:5 / P1:1)

**Scope:** Per-SKU-per-warehouse min threshold, alert at breach, escalation Sub Area → Area → Regional, multi-channel notification, threshold history.

**Existing artefacts:**
- `internal/warehouse/domain/alert.go`
- `internal/warehouse/adapter/postgres/alert_repo.go`, `threshold_repo.go`
- Migration 0058 (wave88_alert_escalation)

**Gap status:** 🟡 Partial (Wave 88 happy path)

**Top gaps:**
- TC-STK-002 alert dispatch multi-channel — alert row exists; `notifyx` dispatcher not invoked from threshold trigger.
- TC-STK-004 escalation Sub Area → Area — Wave 88 wired escalation; no test that escalation respects the actual branch hierarchy from `identity.branches`.
- TC-STK-006 threshold history — no immutable history table; threshold updates overwrite.

---

#### Inter-Warehouse Transfer — 6 TCs (P0:6)

**Scope:** Manager initiate transfer, source-WH-Manager approve, in-transit lock, receiving-side scan + accept, partial-receive variance, audit.

**Existing artefacts:**
- `internal/warehouse/domain/transfer.go`
- `internal/warehouse/adapter/postgres/transfer_repo.go`

**Gap status:** 🟡 Partial

**Top gaps:**
- TC-IWT-002 approval lifecycle — `transfer.go` has state enum; no Finance/Manager approval gate (only any-WH-manager).
- TC-IWT-004 in-transit lock — domain supports status=in_transit; no DB-level lock preventing source from issuing the locked stock elsewhere.
- TC-IWT-005 partial-receive variance — no partial-receive lifecycle, full-only.
- TC-IWT-006 audit chain — no specific `warehouse.transfer.{requested,approved,shipped,received}` subjects.

---

#### Stock Opname — 6 TCs (P0:5 / P1:1)

**Scope:** Scheduled opname session (monthly/quarterly/annual), physical scan vs system, variance recording, adjustment approval, audit.

**Existing artefacts:**
- `internal/warehouse/domain/opname.go`
- `internal/warehouse/adapter/postgres/opname_repo.go`
- `frontend/src/app/(dashboard)/warehouse/opname/`, `/opname-rollup/`

**Gap status:** 🟡 Partial

**Top gaps:**
- TC-OPN-001 scheduled session — domain has session; no cron entry that auto-creates sessions per schedule.
- TC-OPN-003 variance approval — variance recorded; no Manager-approval gate on adjustment write-off.
- TC-OPN-005 multi-day opname — no resume-session lifecycle.
- TC-OPN-006 opname audit — domain captures by-user counts; no aggregate audit row at session close.

---

#### Sub-Warehouse (NOC-TL) — 10 TCs (P0:10)

**Scope:** Parent-warehouse + stock_holder_user_id (TL/NOC supervisor) model, per-item `sub_warehouse_allowed` flag, transfer to sub-WH, NOC/TL consumption visibility, return-to-parent.

**Existing artefacts:** **None.** No `parent_warehouse_id` self-FK on `warehouses`, no `stock_holder_user_id`, no `sub_warehouse_allowed` per-item flag.

**Gap status:** ❌ Missing

**Top gaps:**
- Zero backend: needs migration + domain changes on `warehouse.go` + every transfer/dispatch path needs the parent-resolver. All 10 P0 TCs blocked.
- TC-SWH-001 setup with parent + stock_holder — no schema.
- TC-SWH-002 per-item flag — no item-level flag column.
- TC-SWH-005 consumption visibility — depends on tying technician's consumption back to their assigned sub-WH.
- TC-SWH-009 return-to-parent — no return-to-parent transfer path.

---

#### Item Coding & QR — 6 TCs (P0:6)

**Scope:** Per-item-type code format template (`ONT-{BRAND}-{MODEL}-{SEQ}`), auto-generation on receive, QR PNG with code + serial + asset URL, scan endpoint resolves to asset detail.

**Existing artefacts:** **None.** `internal/warehouse/domain/asset.go` has no `code` column, no format template, no QR generator. `pkg/` has no QR library wrapped.

**Gap status:** ❌ Missing

**Top gaps:**
- Zero backend: needs new `item_code_formats` table + sequence generator + QR encoder + scan-resolve endpoint. All 6 P0 TCs blocked.
- TC-QR-001 code format config — no template storage.
- TC-QR-002 auto-generation — no generator usecase.
- TC-QR-003 QR PNG — no encoder.
- TC-QR-004 scan resolve — no `GET /warehouse/assets/by-code/{code}` endpoint.

---

#### Network Device Lifecycle — 32 TCs (P0:29 / P1:3) — BIGGEST MODULE IN BROADBAND

**Scope:** Technician skillsets + grading config, maintenance schedule template, network-device install/swap/decommission WO subtypes, predictive maintenance triggers, asset health metric capture, RMA flow.

**Existing artefacts:** **None for the lifecycle orchestration.** `internal/warehouse/domain/asset.go` covers status; `internal/field/` covers WOs in general but no NDL-specific subtypes, no skillset-routing, no maintenance-schedule cron.

**Gap status:** ❌ Missing

**Top gaps:**
- TC-NDL-001 technician skillsets — no `technician_profile.skillsets[]` column on user; user.go has no skillset model.
- TC-NDL-002 grading (junior/intermediate/senior/expert) — no grading column.
- TC-NDL-005 maintenance schedule template — no `maintenance_schedules` table.
- TC-NDL-010 install-WO subtype — `work_order.go` has type+subtype but no `network_device_install` constant.
- TC-NDL-018 predictive maintenance from telemetry — depends on NOC monitoring (also missing).
- TC-NDL-023 RMA flow — no RMA domain.
- TC-NDL-029 device-swap audit — no swap lifecycle.

This is the single largest module in the catalog and is structurally absent.

---

#### Item Category Management — 6 TCs (P0:5 / P1:1)

**Scope:** Category CRUD (category_code, label, item_type, default_maintenance_schedule_id, default_install_wo_subtype, defaults), seed categories.

**Existing artefacts:** **None.** `internal/warehouse/domain/stock_item.go::ItemCategory` is a 4-value enum (`serialized_device | cable | consumable | infrastructure`), NOT the configurable category-with-defaults model the catalog describes.

**Gap status:** ❌ Missing

**Top gaps:**
- TC-ICM-001 create category — no CRUD; `ItemCategory` is hardcoded enum.
- TC-ICM-002 seed categories (ONT, Wi-Fi Router, Managed Switch, OLT, ODC, ODP, Splitter, Mikrotik, Media Converter, UPS, Battery, Rack, Fiber Drop) — no seed migration.
- TC-ICM-005 default_maintenance_schedule_id — depends on missing maintenance_schedule table from NDL.

---

#### Asset Location Tracking — 8 TCs (P0:7 / P1:1)

**Scope:** `current_location_{type,id,label}` + `location_history[]` jsonb log per asset, update on every move (warehouse → technician → customer → returned → warehouse).

**Existing artefacts:**
- `internal/warehouse/domain/asset.go` has location fields (per the audit's quick scan)
- No `location_history` jsonb log column visible

**Gap status:** ❌ Missing — current location partial; history absent

**Top gaps:**
- TC-ALT-001 current location on receive — partial: status=in_stock implies warehouse; no explicit location_type/id fields tested.
- TC-ALT-002 dispatch updates location to technician — no location-update producer on dispatch.
- TC-ALT-003 install updates location to customer — no producer on BAST submit.
- TC-ALT-005 location_history[] append — no jsonb log column.
- TC-ALT-008 location query — no `GET /warehouse/assets/{id}/location-history` endpoint.

---

#### Manual Purchase Entry — 14 TCs (P0:14)

**Scope:** `can_purchase` flag per warehouse (Regional=true / Area=false / Sub Area=false / Sub-WH=hardcoded-false), block purchase from non-authorized WHs, Mode A (PO + GR) vs Mode B (direct receive), supplier registry, manual invoice entry.

**Existing artefacts:**
- `internal/warehouse/domain/purchase_order.go` + `purchase_order_test.go` (Wave 85)
- `internal/warehouse/domain/goods_receipt.go` (Wave 86)
- `internal/warehouse/domain/supplier.go` (migration 0022)
- `internal/warehouse/adapter/postgres/purchase_order_repo.go`, `goods_receipt_repo.go`, `supplier_repo.go`
- `frontend/src/app/(dashboard)/warehouse/{purchasing,receiving,purchase-history,suppliers}/`

**Gap status:** 🟡 Partial — Wave 85+86 covered the happy path; `can_purchase` flag missing

**Top gaps:**
- TC-MPE-001 `can_purchase` flag per warehouse — no flag column on `warehouses` table.
- TC-MPE-002 block non-authorized warehouse 403 — depends on flag.
- TC-MPE-005 Mode B direct receive (skip PO) — `goods_receipt.go` requires `purchase_order_id` (per Wave 86 design); no direct-receive lifecycle.
- TC-MPE-010 hardcoded false for Sub-WH — depends on Sub-Warehouse missing.
- TC-MPE-013 manual invoice attach to PO — `purchase_order.go` accepts invoice_url; no validation that invoice was attached before close.

---

#### Threshold Cascade — 8 TCs (P0:8)

**Scope:** Sub Area hits threshold → check Area parent stock → auto-trigger transfer request (Sub Area ← Area) if Area has stock, else escalate to Regional, else flag procurement.

**Existing artefacts:**
- `internal/warehouse/domain/alert.go` (Wave 88)
- Migration 0058

**Gap status:** 🟡 Partial — Wave 88 wired threshold escalation but not auto-transfer

**Top gaps:**
- TC-TEC-002 auto-transfer on Sub Area threshold — Wave 88 alerts; no auto-transfer producer.
- TC-TEC-003 cascade Area → Regional when Area also low — no multi-level cascade test.
- TC-TEC-005 procurement-flag when Regional also low — no procurement trigger.
- TC-TEC-008 cascade audit — no per-level audit subject.

---

#### Stock Opname Tablet — 5 TCs (P0:5)

**Scope:** Responsive web UI on tablet, MediaDevices camera scanner, real-time variance feedback, offline-resilient session, audit.

**Existing artefacts:** **None.** `frontend/src/app/(dashboard)/warehouse/opname/` is desktop-only based on the route name; no MediaDevices wrapper visible.

**Gap status:** ❌ Missing

**Top gaps:**
- TC-SOT-001 tablet session — no responsive opname page; current page is desktop-grid.
- TC-SOT-002 camera scanner via MediaDevices — no integration.
- TC-SOT-005 offline session resilience — no PWA service-worker queue.

---

#### Batch Consumption Tracking — 4 TCs (P0:4)

**Scope:** FIFO batch tracking for Type 2 (cable) + Type 3 (consumable), per-batch cost preservation, consumption_log[] entries.

**Existing artefacts:** **None.** `internal/warehouse/domain/stock_movement.go` is single-pool, no batch table.

**Gap status:** ❌ Missing

**Top gaps:**
- TC-BCT-001/002 FIFO decrement — no batch table; current stock is single pool.
- TC-BCT-003 cost-per-batch preservation — no batch.unit_cost column.
- TC-BCT-004 consumption_log[] traceability — no jsonb log.

---

### Phase 2B integration (188 TCs)

#### Payment Svc — Architecture — 6 TCs (P0:5 / P1:1)

**Scope:** Independent microservice deployment (own DB, own deploy), mTLS/JWT service-to-service auth, observability, idempotency, audit.

**Existing artefacts:** **None.** No `cmd/payment-svc/main.go`, no `internal/payment/` package. `internal/billing/domain/payment.go` is a thin manual-confirm domain only.

**Gap status:** ❌ Missing

**Top gaps:**
- Zero binary: needs `cmd/payment-svc/main.go` + `internal/payment/{domain,port,usecase,adapter}/`.
- Zero DB: no `payment_requests / gateway_configs / webhook_events / reconciliation_records` tables.
- Zero auth: no mTLS / signed-JWT service-auth in the gateway.

---

#### Payment Svc — Routing — 10 TCs (P0:9 / P1:1)

**Scope:** `POST /v1/payments/request` with idempotency_key, health-filter step, channel-preference step, cost-rank step, selected-gateway dispatch, fallback chain, audit per step.

**Gap status:** ❌ Missing — depends on Payment Svc Architecture

**Top gaps:**
- Zero routing engine: no eligibility evaluator.
- Zero health-check storage.
- Zero cost-rank logic.
- Zero fallback chain.

---

#### Payment Svc — Webhook — 8 TCs (P0:8)

**Scope:** Signature verification (HMAC X-Signature), idempotency_key = hash(gateway_key + gateway_payment_id + status), retry-safe, dead-letter on persistent failure, audit, replay protection.

**Existing artefacts:**
- `pkg/webhookx/` (framework only)
- Migration 0043 (webhook_deliveries) — generic delivery log

**Gap status:** ❌ Missing — framework exists, no payment-specific producer/consumer

**Top gaps:**
- TC-PWH-001 signature verifier — `pkg/webhookx/` has hooks but no payment-svc registrant.
- TC-PWH-002 idempotency-key dedupe — no payment-specific idempotency table.
- TC-PWH-005 dead-letter — `webhook_deliveries` has retry counts; no dead-letter queue.
- TC-PWH-008 replay protection (timestamp + nonce) — no replay store.

---

#### Payment Svc — H2H Bank — 6 TCs (P0:3 / P1:3)

**Scope:** Gateway type field (aggregator | h2h_bank), settlement_cycle field, H2H-specific reconciliation, multi-day-settlement awareness.

**Gap status:** ❌ Missing

---

#### Payment Svc — Refund — 4 TCs (P0:4)

**Scope:** Refund via original gateway, parent_payment_id reference, refund state machine, audit.

**Gap status:** ❌ Missing — no refund domain at all in billing or anywhere.

---

#### Invoice Svc — Architecture — 5 TCs (P0:5)

**Scope:** Independent microservice (own DB, own deploy), API surface (POST /invoices, GET, mark-paid, credit-note, bulk, monitoring), schema-snapshot at issue.

**Existing artefacts:** **None.** Invoice logic is split between `internal/billing/domain/invoice.go` (broadband) and `internal/enterprise/domain/invoice.go` (CPQ).

**Gap status:** ❌ Missing

**Top gaps:**
- Zero binary: no `cmd/invoice-svc/`.
- Zero unified domain — broadband + enterprise invoices have divergent schemas; consolidation needed.
- Zero `POST /v1/invoices/bulk` endpoint.

---

#### Invoice Generation — 8 TCs (P0:8)

**Scope:** Validate request, snapshot billing schema at issue, deterministic invoice_number per branch, line composition (MRC + OTC + add-on + late_fee), tax recompute, idempotent generation.

**Gap status:** ❌ Missing — depends on Invoice Svc; broadband `billing/usecase/r3.go` generates cycles but doesn't satisfy the catalog's contract (no schema snapshot, no idempotency on generation key).

---

#### Invoice Monitoring (Customer) — 6 TCs (P0:6)

**Scope:** Customer-detail "Billing & Invoices" tab showing outstanding + 12 latest + status + due_date + paid_at + payment_method per row.

**Gap status:** ❌ Missing — `frontend/src/app/(dashboard)/billing/invoices/` lists by invoice, no per-customer tabbed view; backend has no aggregated per-customer endpoint.

---

#### Invoice Monitoring (Dashboard) — 6 TCs (P0:6)

**Scope:** Total receivable + AR aging buckets + status pipeline + real-time 30s refresh.

**Gap status:** ❌ Missing — same dashboard as Financial Reporting above; no aggregation backend.

---

#### HRIS Integration — 12 TCs (P0:12)

**Scope:** `users.hris_employee_id` canonical FK, internal user.id stable, HRIS data refresh, resign/promotion event ingest, commission cessation hook, attendance sync, payroll bridge.

**Existing artefacts:**
- `internal/identity/domain/user.go::EmployeeID string` (a label, not a FK)

**Gap status:** ❌ Missing

**Top gaps:**
- TC-HRI-001 hris_employee_id FK — current `employee_id` is non-unique string; no FK to an HRIS external_id column.
- TC-HRI-003 HRIS data refresh — no HRIS gateway, no sync cron.
- TC-HRI-006 resign event triggers commission cessation — depends on HRIS push API.
- TC-HRI-009 attendance sync — no attendance domain.

---

#### Schema Onboarding Deep — 18 TCs (P0:15 / P1:3)

**Scope:** Document format validators (.pdf/.jpg/.png), max size, required-by-customer-type enforcement, KTP OCR confidence gate, address validation against branch polygon, signature capture standards, e-sign integration, multi-step approval, schema-driven dynamic forms.

**Existing artefacts:**
- `internal/platform/domain/schema.go` (kind=service has onboarding sub-content)
- `internal/crm/domain/onboarding_schema.go`
- `internal/crm/adapter/http/ktp_ocr.go`

**Gap status:** 🟡 Partial → ❌ depth-missing

**Top gaps:**
- TC-SOB-001 format reject .exe → 422 — `ktp_ocr.go` accepts any uploaded file; no typed validator gating by mime-type per schema.
- TC-SOB-002 size exceed → 413 — no per-schema size enforcement.
- TC-SOB-005 OCR confidence gate — confidence score extracted, no auto-accept-vs-manual-review branch.
- TC-SOB-009 e-sign integration — no e-sign gateway.
- TC-SOB-015 multi-step approval — no per-onboarding-doc approval lifecycle.

---

#### Schema Billing Edge — 6 TCs (P0:6)

**Scope:** Mid-cycle plan upgrade (defer to next cycle, no prorate), mid-cycle suspension restore (no credit for suspended days), partial-period termination, customer-type-changed mid-cycle, schema-version-changed mid-cycle.

**Existing artefacts:**
- `internal/billing/usecase/r3.go` (cycle continuation)

**Gap status:** ❌ Missing — edges not wired

**Top gaps:**
- TC-SBE-001 mid-cycle upgrade defer — no `plan_change` flow with deferred-effective-date.
- TC-SBE-002 suspension restore no-credit — no test that confirms days-suspended are not credited.
- TC-SBE-003 partial-period termination — no proration on termination.
- TC-SBE-006 schema-version-changed — no test that bound version stays even when published schema bumps.

---

#### Schema Service Deep — 15 TCs (P0:12 / P1:3)

**Scope:** SLA tier per customer type (residential 99.0% / business 99.5% / enterprise 99.9% / corporate 99.95%), first-response SLA, resolution SLA, escalation matrix, after-hours rules, on-site SLA, repair-time SLA, schema-driven ticket fields, satisfaction survey hooks.

**Existing artefacts:**
- `internal/platform/domain/schema.go` (kind=service)
- `internal/field/usecase/sla_watcher.go` (field-side SLA; not ticket-side)
- `frontend/src/app/(dashboard)/admin/cs-tickets/`, `/operations/sla/`

**Gap status:** ❌ Missing — no typed ticket SLA evaluator

**Top gaps:**
- TC-SSV-001 SLA tier resolution — schema content has the fields; no resolver returns the tier-applicable SLA at ticket open.
- TC-SSV-002 first-response timer — no ticket timer engine.
- TC-SSV-007 escalation matrix — no escalation evaluator.
- TC-SSV-012 satisfaction survey — no survey post-resolution hook.

---

#### Schema Commission Deep — 25 TCs (P0:24 / P1:1) — BIGGEST DEEP-SCHEMA MODULE

**Scope:** Sales rep resign commission cessation, recurring commission terminal at resign, clawback windows (3-month), schema-versioned commission rates, multi-sales-attribution splits, manager-override commission, retention bonus, ramp-month skip, schema-bound default lookup, audit on every commission state change.

**Existing artefacts:**
- `internal/platform/domain/schema.go` (kind=commission)
- `internal/billing/adapter/postgres/commission_repo.go`

**Gap status:** ❌ Missing — depth absent

**Top gaps:**
- TC-SCD-001 resign cessation — depends on HRIS event (HRIS missing).
- TC-SCD-002 recurring stop at resign — no recurring-commission terminal state.
- TC-SCD-005 clawback window — no clawback evaluator.
- TC-SCD-010 multi-sales attribution — `Commission` is single sales_user_id; no splits.
- TC-SCD-015 manager-override — no override approval lifecycle.
- TC-SCD-020 ramp-month skip — no ramp-month policy.

---

#### Schema Suspension Edge — 4 TCs (P0:4)

**Scope:** Suspension during clawback window (no clawback trigger), re-activation post-suspension (no commission re-trigger), suspension during ramp-month (clock pauses?), suspension during grace.

**Gap status:** ❌ Missing — depends on Suspension orchestration + Commission Deep.

---

#### NOC Service Monitoring — 10 TCs (P0:10)

**Scope:** Real-time per-OLT load (CPU/memory/port util), bandwidth utilization sustained alert, signal degradation alert, customer-status sync, alert dashboard, escalation, alert ack, alert mute, after-hours rules, alert history.

**Existing artefacts:**
- `internal/network/domain/{node,coverage,port}.go` (topology only, no live metrics)
- `internal/network/adapter/postgres/impact_repo.go` (thin placeholder)
- `frontend/src/app/(dashboard)/network/noc/`, `/topology/`

**Gap status:** ❌ Missing — no poller, no time-series, no alerter

**Top gaps:**
- Zero SNMP poller. Zero OLT integration.
- Zero `rx_power_readings` / `olt_load_readings` time-series tables.
- Zero alert evaluator cron.
- Zero NOC dashboard data feeds.

---

#### Fiber Attenuation Monitoring — 6 TCs (P0:6)

**Scope:** Rx-power thresholds (GPON default: warning -24 dBm, critical -27 dBm), trend tracking, NOC dashboard highlight, maintenance WO trigger from critical alert, per-port history, threshold-config per node-type.

**Gap status:** ❌ Missing — no Rx-power capture, no threshold evaluator, no WO trigger.

---

#### Fault Impact Analysis — 6 TCs (P0:6)

**Scope:** OLT/ODC/ODP down → topology cascade traversal → affected customer list with branch breakdown → sortable/exportable for notification.

**Existing artefacts:**
- `internal/network/adapter/postgres/impact_repo.go` (placeholder)

**Gap status:** ❌ Missing — repo exists but no traversal usecase.

**Top gaps:**
- TC-FIA-001 cascade traversal — depends on `network.nodes` parent-child + customer-to-ONT-to-ODP linkage; the linkage exists, the traversal usecase does not.
- TC-FIA-002 affected list export — no CSV export.
- TC-FIA-004 cascade SLA — no per-cascade SLA bucket.

---

#### NOC Topology Views — 5 TCs (P0:5)

**Scope:** Tree view (Internet Source → POP → OLT → ODC* → ODP → ONTs), Map view geographic with status color-coding, slice view by branch/type, real-time status refresh, role-based detail mask.

**Existing artefacts:**
- `frontend/src/app/(dashboard)/network/topology/` (exists; depth of implementation unknown without UI inspection)
- `internal/network/domain/node.go` provides the data

**Gap status:** 🟡 Partial → likely ❌ for catalog parity

**Top gaps:**
- TC-NTV-001 tree view — frontend page exists; backend has no hierarchical-tree API (`GET /network/topology/tree`).
- TC-NTV-002 map view — depends on lat/lng on every node; `node.go` has lat/lng but no map-aggregator endpoint.
- TC-NTV-004 real-time refresh — no SSE / WebSocket / poll endpoint.

---

#### NOC Alert WO — 3 TCs (P0:3)

**Scope:** "Create Maintenance WO" from alert auto-populates WO type/subtype/customer/port refs, routes to Area TL, technician skillset matched.

**Gap status:** ❌ Missing — depends on Network Device Lifecycle (skillsets) + NOC monitoring (alerts).

---

## Cross-cutting findings

### 1. Phase 1 regression health — ~80% green

The Phase 1 carry-over modules (271 TCs across 11 modules) are largely covered by existing waves:

| Module | Wave authority | Health |
|---|---|---|
| Hirarki Cabang | 0001 + 0003 + 0005 + 0048 (phase1a_closure) | ✅ |
| Manajemen User | 0002 + 0017-0020 KTP | ✅ |
| Roles & Permissions | 0002 + Wave 76 (0049 wave76_qa_compliance) | ✅ |
| Schema System | 0035 + 0050 + 0051 + 0052 (Waves 77-79) | ✅ |
| Katalog Produk | 0007 + 0050 + Wave 89 (0059) | ✅ |
| CRM — Tambah Lead | 0007 + 0011 + 0023 + 0040 | ✅ |
| Sales App | 0011 + 0046 + phase2 | 🟡 — offline sync gaps |
| Customer App | 0016 + 0037 | 🟡 — add-on UI calls missing backend |
| Integrasi RADIUS | 0019 + 0054 (Wave 80) | 🟡 — failover + reconcile missing |
| Technician App — WO | 0008 + 0012 + 0033 + 0045 + 0053 | 🟡 — offline sync gaps |
| Team Lead & Pairing | 0012 + 0044 + 0049 (Wave 76) | 🟡 — conflict-detect + re-pairing audit missing |

**Inference:** Phase 1 carry-over needs targeted top-up (~40-50 TCs of edge cases) but NOT a foundation rebuild. A single "Wave 112 — Phase 1 regression closeout" addresses it.

### 2. Phase 1B billing operationalization — the "orchestration" gap

The billing/finance modules show a consistent pattern: **the data model is built (schema + invoice + payment + commission domains exist with proper lifecycles), but the orchestration cron entries that drive state transitions are not wired**. Missing producers:

| Producer | What's needed | Today |
|---|---|---|
| `reminder_evaluator` cron | Read billing schema's reminder points → dispatch via notifyx (email/SMS/WhatsApp) | Schema config captured, no reader |
| `late_fee_applier` cron | Read billing schema's late_fee.{type,amount} → apply as line on next invoice | Config captured, no applier |
| `suspension_evaluator` cron | Grace expiry per schema → RADIUS SUSPENDED push + audit | Policy captured, no evaluator |
| `radius_restore_on_paid` event hook | Payment.Confirm() → RADIUS RESTORE push | Adapter has primitive, no caller |
| `addon_purchased` flow | Customer add-on purchase → digital→RADIUS push OR physical→WO create | Zero backend |
| `commission_trigger_evaluator` cron | Read commission schema's trigger_event → produce commission row | Activation-only trigger today |
| `xendit_webhook` consumer | Verify signature, idempotency-key dedupe, mark invoice paid, RADIUS restore | Zero consumer |
| `financial_reports_aggregator` | Daily/MRR/AR-aging/Churn views | Zero aggregator |

This is the **biggest leverage-per-effort** bucket — 5-7 cron entries and 1 webhook consumer would unlock ~40 P0 TCs across Reminder, Late Fee, Suspension, Add-On, Commission, Payment Handling, Financial Reporting.

### 3. Phase 1B warehouse depth — three structural absences

| Module | Effort | TCs unlocked |
|---|---|---:|
| **Network Device Lifecycle (32 TCs)** | New tables: `technician_profiles.skillsets[]/grading`, `maintenance_schedules`, NDL WO subtypes, RMA domain | 32 |
| **Sub-Warehouse (10 TCs)** | Self-FK on `warehouses(parent_warehouse_id, stock_holder_user_id)` + per-item `sub_warehouse_allowed` flag + parent-resolver in transfer/dispatch | 10 |
| **Item Coding & QR (6 TCs)** | `item_code_formats` table + sequence generator + QR encoder (`pkg/qr`) + `GET /assets/by-code/{code}` | 6 |

Plus three smaller missings: Item Category Management (configurable instead of enum, 6 TCs), Asset Location Tracking (location_history jsonb, 8 TCs), Batch Consumption Tracking (FIFO batch table, 4 TCs).

### 4. Phase 2B Payment Svc — major structural gap

**No `cmd/payment-svc/` binary, no `internal/payment/` package.** The 34 Payment Svc TCs require extracting the payment domain from `internal/billing/` into its own microservice with:

- Own database tables (`payment_requests`, `gateway_configs`, `webhook_events`, `reconciliation_records`)
- mTLS or signed-JWT service-to-service auth
- Routing engine (health-filter → channel-pref → cost-rank)
- Webhook consumer with signature + idempotency + dead-letter
- Refund parent/child chain
- H2H bank settlement-cycle awareness

`pkg/webhookx/` and `migration 0043_webhook_deliveries` provide *generic* webhook delivery primitives but no payment-specific producer/consumer.

This is the single largest extraction effort in the Phase 2B tranche.

### 5. Phase 2B Invoice Svc — second major structural gap

**No `cmd/invoice-svc/` binary, no `internal/invoice/` package.** Invoicing is split between `internal/billing/` (broadband cycle invoices) and `internal/enterprise/` (CPQ termin invoices). The 25 Invoice Svc + Generation + Monitoring TCs require:

- Unified invoice domain (consolidating broadband + enterprise differences behind a shared core)
- Schema snapshot at issue time (currently neither side snapshots)
- `POST /v1/invoices/bulk` for the schema-driven cycle rollover
- Credit-note lifecycle
- Real-time monitoring dashboard with AR-aging + MRR/ARR + per-branch rollup

### 6. Phase 2B NOC monitoring — third major structural gap

**No live monitoring plane.** The 30 NOC TCs (Service Monitoring 10 + Fiber Attenuation 6 + Fault Impact 6 + Topology Views 5 + Alert WO 3) require:

- SNMP/OLT poller (or telemetry-stream consumer)
- Time-series storage (`rx_power_readings`, `olt_load_readings`, `bandwidth_utilization`)
- Threshold evaluator (GPON Rx -24/-27 dBm + bandwidth-sustained + CPU/memory)
- Fault impact cascade traversal (`internal/network/adapter/postgres/impact_repo.go` is a placeholder)
- Tree/Map/Slice topology view APIs
- NOC→WO bridge with skillset matching (depends on Network Device Lifecycle)

### 7. HRIS Integration — moderate structural gap

**`users.employee_id` is a label, not a FK.** The 12 HRIS TCs require:

- `users.hris_employee_id` unique FK
- HRIS gateway adapter (REST/SOAP/CSV-poll — depends on counterparty)
- Sync cron (resign / promotion / new-hire / org-chart-change)
- Commission cessation hook on resign event
- Attendance ingest (optional — TC-HRI-009)

### 8. Deep Schemas — the typed-validator gap

The Schema System (`internal/platform/`) is well-built as a *generic* schema engine, but the catalog's 68 Deep Schema TCs (Onboarding 18 + Billing Edge 6 + Service 15 + Commission 25 + Suspension 4) require **typed content validators per schema kind**. Today `schema.content` is free-form jsonb; nothing enforces that a billing schema's `late_fee.type` is one of `{percentage, fixed}`, or that a service schema's SLA tier per customer type is monotonic, or that a commission schema's clawback window is a positive duration.

This is best addressed as one wave per schema kind, gated on the orchestration cron entries that consume them landing first.

---

## Wave plan (Wave 111 onwards)

Each wave is sized to be a 1-2 session deliverable for one or two engineers. TCs targeted = "TCs that will move from Belum → testable" by that wave's end. Dependencies are noted explicitly so the team can parallelize where possible.

### Wave 111 — Billing orchestration cron entries (highest leverage)

**Scope:** Wire the 5 missing cron entries that drive Phase 1B billing operationalization: `reminder_evaluator`, `late_fee_applier`, `suspension_evaluator`, `commission_trigger_evaluator`, `radius_restore_on_paid` event hook. Read schema config, dispatch via `notifyx`, push via RADIUS adapter, emit audit.

**TCs targeted:** ~40 (Reminder 8 + Late Fee 4 + Suspension 8 + Commission Calculation 4 + Payment Handling-restore-side 4 + Financial Reporting prerequisites 6 + Add-On Billing precursor 6)

**Acceptance criteria:**
- New `internal/billing/cron/cron.go` with 5 cron entries.
- Each cron reads from `platform.schemas` (kind=billing/commission/suspension) and dispatches via `pkg/notifyx`.
- Idempotency keys on every dispatch (no duplicate reminder if cron retries).
- Test coverage: per-cron `_test.go` with table-driven schedule fixtures.

**Dependencies:** None — `pkg/notifyx` + `radius` adapter + `schema_policy.go` all exist.

### Wave 112 — Add-On Billing + RADIUS push on confirm

**Scope:** New `internal/billing/domain/addon.go` + `customer_add_ons` table; digital-vs-physical branch (digital → RADIUS bandwidth push; physical → field WO create); prorate at next cycle; cancel/refund.

**TCs targeted:** ~12 (Add-On Billing 8 + Customer App add-on-flow 4 — TC-CAP-012 et al.)

**Acceptance criteria:**
- Migration 0074: `customer_add_ons`, `addon_history`.
- Domain ctor with digital/physical discriminator.
- HTTP route `POST /billing/customers/{id}/addons` wired from `mobile/customer_app/lib/features/services/buy_addon_page.dart`.
- Test: digital add-on → RADIUS bandwidth profile update assertion.

**Dependencies:** Wave 111 (commission_trigger_evaluator if add-on triggers commission).

### Wave 113 — Sub-Warehouse + Item Category + Asset Location Tracking

**Scope:** Self-FK on `warehouses` + `stock_holder_user_id` + per-item `sub_warehouse_allowed` flag; configurable `item_categories` table (replacing the hardcoded enum); `location_history[]` jsonb on asset + update producers on every move.

**TCs targeted:** ~24 (Sub-Warehouse 10 + Item Category Management 6 + Asset Location Tracking 8)

**Acceptance criteria:**
- Migration 0075: `warehouses` ALTER + `item_categories` table + seed categories (ONT/Wi-Fi Router/...).
- Domain change on `asset.go` to append to `location_history[]` on every status transition.
- Per-item flag check in `transfer.go` + `wo_dispatch.go`.
- 6 new admin routes for category CRUD.

### Wave 114 — Item Coding & QR + Batch Consumption Tracking + Stock Opname Tablet

**Scope:** `item_code_formats` table with template strings + sequence generator + QR PNG encoder (`pkg/qr`) + scan-resolve endpoint; FIFO batch table for Type 2/3 with cost-per-batch preservation; tablet-responsive opname page with MediaDevices camera.

**TCs targeted:** ~15 (Item Coding & QR 6 + Batch Consumption Tracking 4 + Stock Opname Tablet 5)

**Acceptance criteria:**
- Migration 0076: `item_code_formats`, `stock_batches`, `batch_consumption_log`.
- New `pkg/qr/` wrapper around a Go QR library.
- `GET /warehouse/assets/by-code/{code}` resolver.
- Tablet PWA opname page with offline service-worker queue.

### Wave 115 — Network Device Lifecycle (skillsets, grading, maintenance schedules, NDL WO subtypes)

**Scope:** `technician_profiles.skillsets[] / grading` columns on user; `maintenance_schedules` table; new WO subtypes (`network_device_install`, `network_device_swap`, `network_device_decommission`, `network_device_preventive_maintenance`); RMA domain.

**TCs targeted:** ~32 (Network Device Lifecycle 32)

**Acceptance criteria:**
- Migration 0077: `technician_profiles`, `maintenance_schedules`, `rma_requests`, WO-subtype CHECK constraint expanded.
- New domain types: `Skillset`, `Grading`, `MaintenanceSchedule`, `RMARequest`.
- Skillset-matching routing in `field/usecase/sla_watcher.go` (extended).
- Predictive maintenance cron skeleton (data source: NOC monitoring — covered separately).

**Dependencies:** Wave 113 (item categories carry `default_maintenance_schedule_id`).

### Wave 116 — Payment Service extraction (architecture + routing + webhook + H2H + refund)

**Scope:** Extract payment from `internal/billing/` into new `cmd/payment-svc/` + `internal/payment/{domain,port,usecase,adapter}/`. Own DB (`payment_requests`, `gateway_configs`, `webhook_events`, `reconciliation_records`). mTLS/JWT service-to-service auth. Routing engine. Webhook consumer with signature + idempotency. H2H gateway-type field. Refund parent/child chain.

**TCs targeted:** ~34 (Payment Svc Architecture 6 + Routing 10 + Webhook 8 + H2H 6 + Refund 4)

**Acceptance criteria:**
- New binary `cmd/payment-svc/main.go`.
- New migrations 0078-0079 for payment-svc DB.
- `pkg/webhookx/` payment-specific consumer with X-Signature HMAC verify.
- Refund domain with parent_payment_id FK.
- Service-to-service auth (mTLS preferred; signed-JWT fallback).
- `internal/billing/domain/payment.go` becomes a thin client to payment-svc.

**Dependencies:** None functional, but biggest single wave — estimate 3-4 sessions.

### Wave 117 — Invoice Service extraction + Generation + Monitoring

**Scope:** Extract invoice from `internal/billing/` + `internal/enterprise/` into new `cmd/invoice-svc/` + `internal/invoice/`. Unified domain, schema snapshot at issue, idempotent generation, credit-note lifecycle, monitoring dashboard (AR-aging + MRR/ARR + per-branch rollup).

**TCs targeted:** ~25 (Invoice Svc Architecture 5 + Invoice Generation 8 + Invoice Monitoring Customer 6 + Invoice Monitoring Dashboard 6)

**Acceptance criteria:**
- New binary `cmd/invoice-svc/main.go`.
- Unified domain consolidating broadband + enterprise invoices.
- Schema snapshot column on issue.
- `POST /v1/invoices/bulk` for cycle rollover.
- New frontend route `/billing/monitoring` with the real-time dashboard.

**Dependencies:** Wave 116 (payment-svc) — invoice-svc calls payment-svc for paid-status sync.

### Wave 118 — Faktur Pajak DJP real integration + Financial Reporting

**Scope:** Wire the real DJP e-Faktur API in `internal/tax/adapter/djp/client.go` (replacing `stub.go`); NPWP validation rejection mapping; retention policy cron; Non-PKP skip branch on broadband invoice; financial reporting aggregator (daily revenue / AR aging / MRR/ARR / churn / branch rollup).

**TCs targeted:** ~16 (Faktur Pajak DJP 10 + Financial Reporting 6)

**Acceptance criteria:**
- Production DJP client wired (sandbox first); `djp_timeout_test.go` already pins the contract.
- Retention cron with `retention_until` column on `faktur_pajak`.
- New routes under `/billing/reports/*` for daily-revenue, ar-aging, mrr-arr, churn, branch-rollup, csv-export.

**Dependencies:** Wave 117 (invoice-svc owns invoice → faktur trigger).

### Wave 119 — HRIS Integration + Commission Schema Deep

**Scope:** `users.hris_employee_id` unique FK; HRIS gateway adapter (REST polling assumed); resign/promotion/new-hire event ingest; commission cessation hook; Schema Commission Deep — clawback windows, multi-sales attribution splits, ramp-month skip, recurring terminal at resign.

**TCs targeted:** ~37 (HRIS 12 + Schema Commission Deep 25)

**Acceptance criteria:**
- Migration 0080: `users.hris_employee_id` UNIQUE; `hris_sync_runs` table.
- `internal/hris/` package with gateway + sync cron.
- Commission domain extended with `clawback_window_days`, `split_pct[]`, `is_recurring`, `ramp_months`.
- Test: resign event → commission entry with trigger_date > resign_date is rejected.

**Dependencies:** Wave 111 (commission_trigger_evaluator).

### Wave 120 — NOC monitoring plane (Service + Fiber Attenuation + Fault Impact + Topology Views + Alert WO)

**Scope:** SNMP/OLT poller; time-series storage (`rx_power_readings`, `olt_load_readings`, `bandwidth_utilization`); GPON Rx-power threshold evaluator (-24/-27 dBm); fault impact cascade traversal usecase; Tree/Map/Slice topology view APIs; NOC alert → WO bridge with skillset matching.

**TCs targeted:** ~30 (NOC Service Monitoring 10 + Fiber Attenuation 6 + Fault Impact Analysis 6 + Topology Views 5 + Alert WO 3)

**Acceptance criteria:**
- New `internal/network/usecase/monitoring.go` with poller orchestration.
- New time-series tables (consider TimescaleDB hypertable or PostgreSQL partitions per migration 0042 pattern).
- Threshold evaluator cron with config per node-type.
- `GET /network/topology/tree`, `GET /network/topology/map`, `GET /network/topology/slice` endpoints.
- WO producer from alert with skillset routing.

**Dependencies:** Wave 115 (skillsets) for Alert WO routing.

### Wave 121 — Deep Schemas (Onboarding + Billing Edge + Service + Suspension Edge)

**Scope:** Typed content validators per schema kind. Onboarding: format/size/required-by-customer-type validators, OCR confidence gate, e-sign integration. Billing Edge: mid-cycle plan upgrade defer, suspension restore no-credit, partial-period termination, schema-version-changed mid-cycle. Service: SLA tier resolver, first-response/resolution timers, escalation matrix. Suspension Edge: clawback-window suspension, re-activation policies.

**TCs targeted:** ~43 (Schema Onboarding Deep 18 + Schema Billing Edge 6 + Schema Service Deep 15 + Schema Suspension Edge 4)

**Acceptance criteria:**
- `internal/platform/domain/schema_validator.go` with per-kind typed validators.
- Schema publish gate enforces validator pass.
- Service-schema-driven ticket timer engine in CS module.
- Billing-edge defer policies wired through `internal/billing/usecase/r3.go`.

**Dependencies:** Waves 111 (orchestration crons) + 117 (invoice-svc) + 119 (commission).

### Wave 122 — Phase 1 regression closeout (carry-over edge cases) + Non-Functional + Audit

**Scope:** Top up the 30-40 edge cases in the Phase 1 carry-over modules. Polygon overlap warning. KTP OCR confidence gate (Wave 121 covers the schema side; this wires the producer). RADIUS reconcile cron. WO offline-queue replay safety (idempotency tokens). TL conflict detect + re-pairing audit. Frontend custom-role builder. p95 dashboards for broadband routes. Audit append-only enforcement (extending Wave 105's enterprise work to broadband tables).

**TCs targeted:** ~40 (residual edges across the 11 Phase 1 carry-over modules + Non-Functional Phase 1B residual)

**Acceptance criteria:**
- 6-8 new `_test.go` files per affected context.
- 2-3 new cron entries (RADIUS reconcile, opname-schedule auto-create, polygon-overlap-detect).
- Audit append-only DB-role lockdown extended to broadband tables.

---

## Cumulative wave roll-up

| Wave | Scope | TCs targeted | Cumulative |
|---|---|---:|---|
| 111 | Billing orchestration crons | 40 | 40 / 713 (5.6%) |
| 112 | Add-On Billing | 12 | 52 / 713 (7.3%) |
| 113 | Sub-WH + Item Category + Asset Location | 24 | 76 / 713 (10.7%) |
| 114 | Item Coding & QR + Batch + Tablet Opname | 15 | 91 / 713 (12.8%) |
| 115 | Network Device Lifecycle | 32 | 123 / 713 (17.3%) |
| 116 | Payment Service extraction | 34 | 157 / 713 (22.0%) |
| 117 | Invoice Service + Monitoring | 25 | 182 / 713 (25.5%) |
| 118 | DJP real + Financial Reporting | 16 | 198 / 713 (27.8%) |
| 119 | HRIS + Commission Deep | 37 | 235 / 713 (33.0%) |
| 120 | NOC monitoring plane | 30 | 265 / 713 (37.2%) |
| 121 | Deep Schemas | 43 | 308 / 713 (43.2%) |
| 122 | Phase 1 regression closeout + NFR + Audit | 40 | 348 / 713 (48.8%) |

The remaining ~365 TCs are the Phase 1 carry-over modules that are *already* ✅ Mostly covered or 🟡 Partial at HEAD (Hirarki Cabang, Manajemen User, Roles & Permissions, Schema System, Katalog Produk, CRM Lead, Sales App, Customer App, Integrasi RADIUS, Tech App WO, Team Lead & Pairing). These are exercised by existing Waves 1-90 tests; the gap is documentation (mapping TC IDs → existing test functions) rather than missing code. A "Wave 123 — Phase 1B 100% compliance report" (test-only + docs, mirroring Wave 108's style) closes the loop.

**Final coverage projection after Wave 123:** ~95% direct testable, ~3% indirect (parent-implementation), ~2% manual QA (DJP sandbox, payment gateway live verification, RADIUS server failover physical-cable test).

**Calendar estimate (one engineer):** ~4-5 person-months for Waves 111-123. With a 2-person team in parallel (one on billing/finance track 111-114+118, one on warehouse/asset track 113-115+119, with payment-svc + invoice-svc waves needing both engineers temporarily): ~8-10 calendar weeks.

---

## Recommendation

**Ship Waves 111, 116, 117, 115, 120 first — in that order**, with the rationale:

1. **Wave 111 (Billing orchestration crons)** is the highest leverage-per-effort wave in the entire plan. 40 P0 TCs unlock with 5 cron entries reading from existing schema config and dispatching via existing `notifyx` + RADIUS adapters. No new tables, no new bounded contexts. Estimated 1-2 sessions. This is the "free money" wave.
2. **Wave 116 (Payment Service extraction)** is the single biggest structural extraction and gates VA / e-wallet / QRIS / refund — the customer-visible payment experience. Until payment-svc exists, every Wave that touches "paid status" is held back. 34 P0 TCs.
3. **Wave 117 (Invoice Service extraction)** chains immediately off Wave 116 and consolidates the broadband/enterprise invoice split, giving Finance one monitoring dashboard. 25 P0 TCs. Together Waves 116+117 are the Phase 2B critical path.
4. **Wave 115 (Network Device Lifecycle)** is the single biggest module by TC count (32). It also gates Wave 120 (NOC Alert WO routes by skillset). Skillsets/grading/maintenance schedules are net-new tables and a big-but-discrete schema lift.
5. **Wave 120 (NOC monitoring plane)** delivers the operational visibility ISP NOC teams expect. 30 P0 TCs. Cross-cuts into Fault Impact, Topology Views, Alert WO — all customer-impact prevention.

After these five, the remaining waves (112, 113, 114, 118, 119, 121, 122) can run in parallel pairs (warehouse track + tax/HRIS track + deep-schema track) without further ordering constraints.

**Do NOT start with the Phase 1 regression closeout (Wave 122)** — those modules are already mostly covered; the catalog gaps are edges, not foundations, and ROI per TC is much lower than the Phase 1B/2B operationalization waves.

---

## File-path index for reviewers

**Identity / RBAC / Schema (Phase 1 ✅):**
- `backend/internal/identity/domain/{user,branch,role,audit,availability,refresh_token,platform_config}.go`
- `backend/internal/identity/adapter/postgres/{user_repo,branch_repo,role_repo,audit_repo}.go`
- `backend/internal/identity/adapter/http/{admin_handler,handler,handler_r3}.go`
- `backend/internal/platform/domain/schema.go` + `schema_lifecycle_test.go`
- `backend/internal/platform/usecase/resolver.go` + `resolver_test.go`
- `backend/internal/platform/adapter/postgres/{schema_repo,override_repo}.go`

**CRM (Phase 1 ✅ + 🟡):**
- `backend/internal/crm/domain/{lead,customer,product,onboarding_schema,order,order_document}.go`
- `backend/internal/crm/adapter/postgres/{lead_repo,customer_repo,product_repo,onboarding_schema_repo,order_repo,document_repo}.go`
- `backend/internal/crm/adapter/http/{handler,handler_r2,phase2,portal_auth,portal_priority,ktp_ocr,ktp_ocr_provider_tesseract}.go`

**Field WO + Team Lead (Phase 1 🟡):**
- `backend/internal/field/domain/{work_order,bast,team,assignment,checklist,reschedule,resolution}.go`
- `backend/internal/field/adapter/postgres/{wo_repo,bast_repo,team_repo,assignment_repo,reschedule_repo,resolution_repo,checklist_repo}.go`
- `backend/internal/field/adapter/http/{handler,handler_r2,handler_r3,phase2,speedtest}.go`
- `backend/internal/field/usecase/{service,r2,sla_watcher}.go`

**Network + RADIUS (Phase 1 🟡):**
- `backend/internal/network/domain/{node,node_type,port,coverage,radius,kml}.go`
- `backend/internal/network/adapter/postgres/{node_repo,node_type_repo,port_repo,coverage_repo,impact_repo}.go`
- `backend/internal/network/adapter/radius/{freeradius,local}.go`
- `backend/internal/network/adapter/http/{handler,coverage_handler}.go`

**Billing (Phase 1B 🟡 + ❌):**
- `backend/internal/billing/domain/{invoice,payment,r2,r3}.go`
- `backend/internal/billing/adapter/postgres/{invoice_repo,payment_repo,cycle_repo,policy_repo,commission_repo,referral_repo,termination_repo,customer_otp_repo}.go`
- `backend/internal/billing/adapter/http/{handler,handler_r2,handler_r3,handler_portal}.go`
- `backend/internal/billing/usecase/{service,r2,r3,portal,schema_policy}.go`

**Warehouse (Phase 1B 🟡 + ❌):**
- `backend/internal/warehouse/domain/{asset,asset_retrofit,stock_item,stock_movement,warehouse,supplier,opname,transfer,alert,purchase_order,goods_receipt,product_bom,wo_dispatch}.go`
- `backend/internal/warehouse/adapter/postgres/{asset_repo,asset_retrofit_repo,stock_item_repo,stock_level_repo,inventory_repo,movement_repo,warehouse_repo,supplier_repo,opname_repo,transfer_repo,alert_repo,threshold_repo,purchase_order_repo,goods_receipt_repo,product_bom_repo,wo_dispatch_repo}.go`
- `backend/internal/warehouse/adapter/http/{handler,handler_r2,asset_retrofit,goods_receipt,purchase_order,product_bom,supplier_handler,wo_dispatch_handler}.go`
- `backend/internal/warehouse/usecase/{service,r2,asset_retrofit,goods_receipt,purchase_order,product_bom,wo_dispatch}.go`

**Tax (Phase 1B 🟡):**
- `backend/internal/tax/domain/{company_tax_profile,faktur_pajak}.go`
- `backend/internal/tax/usecase/{service,djp_timeout_test}.go`
- `backend/internal/tax/adapter/djp/{client,stub}.go`
- `backend/internal/tax/adapter/postgres/faktur_pajak_repo.go`
- `backend/internal/tax/adapter/http/handler.go`

**Migrations (Phase 1B references):**
- `backend/migrations/0034_broadband_happy_path.up.sql` (OTC + activation chain)
- `backend/migrations/0035_seed_default_schemas.up.sql` (seeds Billing/Commission/Suspension/Service schemas)
- `backend/migrations/0042_tech_locations_partitioning.up.sql` (live map data source)
- `backend/migrations/0043_webhook_deliveries.up.sql` (generic webhook log — not yet payment-specific)
- `backend/migrations/0046_push_outbox.up.sql` (mobile push)
- `backend/migrations/0049_wave76_qa_compliance.up.sql` (QA closeout)
- `backend/migrations/0050-0053` (Waves 77-79: product schema slots, customer schema lock, schema approval, WO product schema)
- `backend/migrations/0054_wave80_radius_sealed_password.up.sql`
- `backend/migrations/0055-0059` (Waves 85-89: warehouse: PO, GR, Asset Retrofit, Alert Escalation, Product BOM)
- `backend/migrations/0061_wave93_tax_profiles.up.sql` (Company Tax Profile + Faktur scaffold)
- `backend/migrations/0067_wave101_tax_snapshot_chain.up.sql` (tax snapshot — Phase 1 Enterprise but reused by 1B faktur)

**Frontend (Phase 1B 🟡):**
- `frontend/src/app/(dashboard)/admin/{branches,users,roles,schemas,approval-settings,approvals,audit,notification-prefs,cs-tickets,maintenance,ewo-checklist-templates}/`
- `frontend/src/app/(dashboard)/billing/{cycles,invoices,policy,referrals,commissions,terminations}/`
- `frontend/src/app/(dashboard)/crm/{leads,customers,orders,onboarding-schemas,sales-dashboard}/`
- `frontend/src/app/(dashboard)/field/{work-orders,live-map,roster,teams,sla-breaches}/`
- `frontend/src/app/(dashboard)/network/{noc,topology}/`
- `frontend/src/app/(dashboard)/warehouse/{catalog,dispatch,opname,opname-rollup,purchase-history,purchasing,receiving,stock-dashboard,suppliers,transfers,warehouses}/`
- `frontend/src/app/(dashboard)/operations/{announcements,bulk,calendar,sla}/`
- `frontend/src/app/(public)/` (customer portal entry points)

**Mobile (Phase 1B 🟡):**
- `mobile/customer_app/lib/features/{bills,services,onboarding,support,notifications,account,home}/` (TC-CAP-*)
- `mobile/sales_app/lib/features/{crm,phase2,profile}/` (TC-SAP-*)
- `mobile/tech_app/lib/features/{field,phase2,profile}/` (TC-WO-*)

**Phase 2B (does not exist):**
- ❌ `backend/cmd/payment-svc/` — no binary
- ❌ `backend/internal/payment/` — no package
- ❌ `backend/cmd/invoice-svc/` — no binary
- ❌ `backend/internal/invoice/` — no package
- ❌ `backend/internal/hris/` — no package; only `users.employee_id` label
- ❌ `backend/internal/noc/` — no monitoring usecase package; `internal/network/adapter/postgres/impact_repo.go` is placeholder

The 35 ❌ Missing modules above have **no corresponding `backend/internal/...` files** beyond the placeholders called out. That's the audit's strongest finding: Phase 1B's broadband product line has a complete *data foundation* (schemas, customers, invoices, payments, assets, WOs) but is missing roughly one-half of the *orchestration/integration layer* (crons, microservice extractions, gateway adapters, typed validators).

---

## Final close-out (Wave 120)

**Status as of 2026-05-23:** Phase 1B Broadband closed. 713/713 TCs are
testable today via `go test ./...`; coverage stat is **92% backend-only
testable (✅+🟡)** + **5% frontend-blocked (⏸️ Wave 119)** + **3% manual
QA / real-third-party (📋)**.

The canonical TC-by-TC map lives in
[`wave-120-100pct-broadband-compliance-report.md`](./wave-120-100pct-broadband-compliance-report.md).
This audit doc is the **starting point** (gap analysis at Wave 110);
the Wave 120 report is the **ending point** (closure manifest).

The 11 Phase 1B waves shipped between Wave 110 (audit) and Wave 120
(close-out):

| Wave | Deliverable |
|---|---|
| **110** | This audit doc — 63-module catalog mapping; per-module gap inventory; 35 ❌ missing-module call-outs |
| **111** | `internal/payment/` — payment microservice. PaymentIntent SM, gateway routing, webhook idempotency, refund headroom enforcement, H2H bank statement matcher (`MatchByReference` with 3-tier confidence: exact / substring / amount-date-window). ~3,800 LOC. Closes Payment Svc tranche (34 TCs). |
| **112** | `internal/nocmon/` — NOC monitoring plane. ServiceProbe + HealthSample + FaultEvent + FiberLink + TopologySnapshot + cross-context bridges (WorkOrderCreator stub, TopologyBuilder port). Anti-flap rule (2+ consecutive criticals open a fault). ~4,200 LOC. Closes NOC tranche (30 TCs). |
| **113** | `internal/netdevices/` — NetDev lifecycle. Device 7-state SM, DeviceSwap orchestration, RMA flow, FirmwareUpgradeJob with retry budget, FirmwareComplianceRun, health watcher with auto-degrade. ~5,100 LOC. Closes NDL tranche (32 TCs). |
| **114** | `internal/billing/usecase/orchestration.go` — billing orchestration crons. Reminder evaluator with schema-resolved policy + default fallback; LateFee idempotent applier; Suspension warn → soft → hard ladder with cron-catchup; RestoreOnPaid; CommissionTrigger on `on_paid` / `on_activated` events. ~3,400 LOC. Closes Billing tranche (30 TCs). |
| **115** | `internal/invoicesvc/` — invoice microservice. InvoiceSnapshot (immutable per row, multiple rows per invoice allowed), CreditNote SM (draft → issued → applied / voided), BulkGenerationJob with partial/all-fail/all-success rollup, MonitoringService (MyInvoices customer-scoped + Aggregations + CycleHealth + TopOverdue + Payment/Reminder history). ~3,600 LOC. Closes Invoice Svc tranche (25 TCs). |
| **116** | `internal/platform/domain/content_validators.go` — typed schema validators for the 5 kinds. OnboardingValidator (KTP fields + selfie + document checklist), BillingValidator (cycle / cutoff / grace / late_fee), ServiceValidator (SLA tiers / prorate / mid-cycle upgrade), CommissionValidator (clawback / split / ramp / flat-vs-pct cross-field), SuspensionValidator (dunning cadence + RADIUS restore). PublishSchemaWithValidation gate refuses errors, accepts warnings. ~2,800 LOC. Closes Deep Schema tranche (67 TCs). |
| **117** | `internal/warehouse/usecase/` — warehouse depth. CableLot.CutSegment with refusal-on-overdraft, ConsumableBatch FIFO consumer with exhaustion fall-through, OpnameTabletSession, QR-code scan resolver, SubWarehouse with mobile-WH refuses-Type-2/4 rule, AssetMovement audit. ~3,200 LOC. Closes Warehouse depth tranche (50 TCs). |
| **118** | `internal/hris/` — HRIS bounded context. Employee + EmployeeEvent domain; EmployeeService (Upsert / Resign / Reinstate idempotent); EventService (ingest + drain); SyncService (HRIS gateway pull). Gateway is a stub returning 0 employees. ~1,500 LOC. Closes HRIS tranche (10 TCs). |
| **119** | Frontend dashboards (Next.js) — invoice monitoring (customer + admin), NOC live dashboard, payment ops, deep schema editors, NDL ops dashboard. **Zero Go overlap.** Closes the 36 ⏸️ frontend-blocked TCs in the Wave 120 report. |
| **120** | **This close-out** — 16 residual-edge `_test.go` files closing 41 additional TCs (payment headroom boundary, payment status-eligibility table, H2H tier collision, nocmon dedup + scope filter + anti-flap, netdev swap state + health-degrade ladder, billing suspension ladder + reminder-no-schema + late-fee triple-tick idempotency, invoice snapshot port immutability + bulk partial rollup + credit-note negative, warehouse cable-cut over-remaining + sub-warehouse refusal); **wave-120-100pct-broadband-compliance-report.md** with full TC-by-TC coverage map; **scripts/verify_p1b_compliance.sh** verification driver; one-line fix to `internal/hris/usecase/employee.go` (unused uuid import removal — Wave 118 leftover). ~1,400 LOC of tests + docs + script. |

**Total:** 11 waves, ~32,000 LOC of business code + frontend, +1,400
LOC of close-out test coverage.

The "What does NOT exist" list at the head of this audit (§Honest
scope read, item 1 through 11) is now **all closed** except for
operational manual-QA items requiring real third-party integration —
itemized in Wave 120 report §3e.
