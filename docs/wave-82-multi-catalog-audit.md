# Wave 82 — Multi-Catalog QA Audit & Execution Roadmap

**Date:** 2026-05-23
**Source catalogs (all `Belum` — not yet executed):**
1. `ION-Phase1-Broadband-Test-Cases-ID (2).xlsx` — 270 TCs
2. `ION-Phase1B-Broadband-Test-Cases-ID.xlsx` — 713 TCs
3. `ION-Phase1C-Broadband-Test-Cases-ID.xlsx` — 838 TCs
4. `ION-Phase1-Enterprise-Test-Cases-ID.xlsx` — 455 TCs

**Total unique TCs (de-duped after counting Phase 1 as regression in 1B/1C):** **~1500**

---

## Honest scope read

These are **forward-looking specification catalogs** — every TC marked "Belum" (not executed). The earlier Wave 74 audit was against `ION-Phase1-Broadband-Test-Cases-ID (2).xlsx` v2 where QA had run a fresh pass with 82.97% Lulus. That catalog is the **only one with execution verdicts**.

Phase 1B, 1C, and Phase 1 Enterprise are PRD-derived acceptance plans — they describe what the system **should** support once those phases ship. They do not reflect current code state. Translating them into running code is a 6–12 person-month program for a competent backend + frontend + mobile team.

This document does not pretend to close all 1500 TCs. It:

1. Aggregates the catalogs into module-level scope tables
2. Categorises each module as **Done** / **Partial** / **Foundationed** / **Not built** vs current code
3. Lays out a wave-by-wave roadmap (Waves 82–110+) with effort estimates
4. Pin-points the highest-leverage next steps

---

## Aggregate scorecard

| Catalog | Total | Lulus | Gagal | Blocked | Belum | Net new vs Phase 1 |
|---|---|---|---|---|---|---|
| Phase 1 Broadband v2 (audited xlsx v2 earlier) | 270 | 151 | 25 | 6 | 0 (88 N/A) | — baseline |
| Phase 1B Broadband | 713 | 0 | 0 | 0 | 713 | **+443 new** |
| Phase 1C Broadband | 838 | 0 | 0 | 0 | 838 | **+125 more** |
| Phase 1 Enterprise | 455 | 0 | 0 | 0 | 455 | **+455** entirely new module set |

> The xlsx_v2 column shows that an earlier QA pass reached 82.97% Lulus on Phase 1 broadband-scope. Waves 75-80 in this program closed ~28 additional domain-level TCs that QA flagged as Gagal/Blocked in xlsx_v2.

---

## Phase 1 Broadband status (recap)

Modules already audited in Wave 74 and substantially worked on in Waves 75-79. See `docs/wave-74-tc-audit.md` for per-TC granular table.

| Module | TCs | QA Lulus | Wave 75-79 closures | Remaining gap |
|---|---|---|---|---|
| Hirarki Cabang | 20 | 14 | TC-BR-013/14/15 (Wave 76 perms) — partial | Polygon PostGIS resolution still stub |
| Manajemen User | 19 | 16 | TC-USR-013/14/19 docs only | Org chart UI + circular reports_to CTE + user audit |
| Roles & Permissions | 17 | 0 (all skip) | Wave 76 NOC perm seeded | Branch-scope intersection middleware (TC-RBAC-015) |
| Schema System | 26 | 11 | Waves 77/78/79 — slot FKs + version lock + approval state machine | HTTP wiring for Submit/Approve/Reject (Wave 79b) |
| Katalog Produk | 35 | 18 | Wave 77 (slot FKs + GET/PATCH /products/{id}) | Admin UI for slot picker + audit emission on update |
| CRM — Tambah Lead | 23 | 7 | Wave 75/76 — status forward-only + lead_type + referrer | Wizard UI updates + territory auto-assign |
| Sales App | 24 | 20 | Wave 80 — Lead model + 6 tests | Wizard UI: lead_type selector + referrer dropdown |
| Customer App | 23 | 5 (most skip) | — | TC-CAP signature canvas + order tracker (Wave 84+) |
| Integrasi RADIUS | 21 | 15 | Wave 76 — NOC credential rotation endpoint | Real FreeRADIUS adapter (Wave 80b — defer) |
| Technician App — WO | 28 | 21 | — | Mobile/backend enum drift; offline queue; checklist via product schema |
| Team Lead & Pairing | 34 | 24 | Wave 76 — branch SLA | TL routing invocation in CreateWOFromOrder (Wave 81) |

---

## Phase 1B — Net-new modules (443 TCs)

Phase 1B re-runs all 270 Phase 1 TCs as regression suite, then adds these net-new modules grouped by domain.

### Billing & Finance (106 TCs)

| Module | TCs | Current code state | Effort estimate |
|---|---|---|---|
| Billing Schema | 12 | Schema kind exists; runtime resolver pinned in Wave 78 — needs structured fields | 1 session |
| OTC (One-Time Charge) | 18 | `billing.invoices` exists; OTC type enum (free/prepaid/postpaid) wired | 1 session |
| Recurring Billing | 12 | Cron exists (`internal/billing/cron`); needs schema-driven cycle/anchor/grace | 1-2 sessions |
| Add-On Billing | 8 | `crm.customer_addons` exists; no automatic billing-cycle merge yet | 1 session |
| Faktur Pajak DJP | 10 | **Not built** — needs PPN calculation + DJP e-Faktur XML format + tax_invoice_number column | **2-3 sessions** |
| Payment Handling | 12 | Manual payment recording exists; needs Xendit gateway integration | **2 sessions** |
| Suspension | 10 | Cron + state transitions; needs configurable trigger + throttle action (TC-RAD-010 pending) | 1 session |
| Reminder Schedule | 8 | **Not built** — needs cron + notifyx WA template send | 1-2 sessions |
| Late Fee | 4 | Domain field exists; calc not wired into dunning tick | 1 session |
| Commission Calculation | 6 | `crm.commissions` exists; per-product calc + payment-trigger event missing | 1-2 sessions |
| Financial Reporting | 6 | Some dashboards exist (revenue/aging); needs export + period selectors | 1 session |

**Billing/Finance subtotal: 14-19 sessions (~3-5 weeks)**

### Warehouse (167 TCs)

| Module | TCs | Current code state | Effort estimate |
|---|---|---|---|
| Item Type 1 (Serialized) | 10 | `warehouse.stock_items` with serial column exists | 1 session |
| Item Type 2 (Cable) | 6 | length-based tracking — partial | 1 session |
| Item Type 3 (Consumable) | 4 | quantity-based — exists | 0.5 session |
| Item Type 4 (Network Infra) | 6 | partial via asset tracking | 1 session |
| WO Material List (BOM) | 6 | not implemented; needs `wo_bom_items` table | 1-2 sessions |
| Dispatch Flow | 6 | `warehouse.dispatches` + UI exists | 0.5 session |
| Consumption Recording | 6 | tech app pickup + WO consumption — partial | 1 session |
| Device Return Flow | 8 | NOC verification of returned device — partial | 1 session |
| Asset Retrofit | 4 | **Not built** — needs serial swap + history audit | 1 session |
| Stock Threshold | 6 | alert exists; needs escalation cron | 1 session |
| Inter-Warehouse Transfer | 6 | `warehouse.transfers` exists | 0.5 session |
| Stock Opname | 6 | dashboard exists; needs reconciliation tool | 1 session |
| Sub-Warehouse (NOC-TL) | 10 | **Not built** — branch-level sub-warehouse model | 2-3 sessions |
| Item Coding & QR | 6 | QR generation + scan exists | 0.5 session |
| Network Device Lifecycle | 32 | `network.nodes` + status tracking partial | **3-4 sessions** |
| Item Category Management | 6 | `warehouse.item_categories` partial | 1 session |
| Asset Location Tracking | 8 | location field exists; needs history audit | 1 session |
| Manual Purchase Entry | 14 | **Not built** — PO + GR workflow | 2 sessions |
| Threshold Cascade | 8 | Sub→Area→Regional escalation — Wave 76 sla_watcher needs extension | 1 session |
| Stock Opname Tablet | 5 | **Not built** — mobile app for warehouse staff | 2-3 sessions |
| Batch Consumption Tracking | 4 | partial via WO consumption | 1 session |

**Warehouse subtotal: 22-28 sessions (~5-7 weeks)**

### Payment Service (34 TCs)

| Module | TCs | Current code state | Effort estimate |
|---|---|---|---|
| Payment Svc — Architecture | 6 | Internal payment recording exists; no separate microservice yet | **3-5 sessions** |
| Payment Svc — Routing | 10 | **Not built** — gateway selection rules | 1-2 sessions |
| Payment Svc — Webhook | 8 | webhook_deliveries cron exists; needs Xendit specifics | 1-2 sessions |
| Payment Svc — H2H Bank | 6 | **Not built** — VA + transfer reconciliation | **2-3 sessions** |
| Payment Svc — Refund | 4 | **Not built** | 1 session |

**Payment Svc subtotal: 8-13 sessions (~2-3 weeks)**

### Invoice Service (25 TCs)

| Module | TCs | Current code state | Effort estimate |
|---|---|---|---|
| Invoice Svc — Architecture | 5 | Invoice generation in-process; sufficient for Phase 1B | 1-2 sessions |
| Invoice Generation | 8 | Exists for OTC + recurring; needs DJP e-Faktur integration | 1-2 sessions |
| Invoice Monitoring (Customer) | 6 | Portal /invoices exists | 0.5 session |
| Invoice Monitoring (Dashboard) | 6 | Admin invoice list exists | 0.5 session |

**Invoice Svc subtotal: 3-5 sessions (~1 week)**

### Schema deep dives (68 TCs)

| Module | TCs | Current code state | Effort estimate |
|---|---|---|---|
| Schema Onboarding Deep | 18 | `crm.onboarding_schemas` exists | 1-2 sessions |
| Schema Billing Edge | 6 | covered by Wave 78 resolver tests | 0.5 session |
| Schema Service Deep | 15 | Wave 77 service_schema_id slot ready | 1 session |
| Schema Commission Deep | 25 | needs per-product commission split logic | 2-3 sessions |
| Schema Suspension Edge | 4 | covered by Wave 78 partial | 0.5 session |

**Schema deep subtotal: 5-7 sessions (~1-2 weeks)**

### NOC Monitoring (30 TCs)

| Module | TCs | Current code state | Effort estimate |
|---|---|---|---|
| NOC Service Monitoring | 10 | `internal/network` partial — needs uptime ping cron | 2-3 sessions |
| Fiber Attenuation Monitoring | 6 | **Not built** — needs OLT SNMP integration | **3-5 sessions** |
| Fault Impact Analysis | 6 | partial — `impact_repo` exists | 1-2 sessions |
| NOC Topology Views | 5 | Frontend live-map page exists | 1 session |
| NOC Alert WO | 3 | partial via maintenance WO | 1 session |

**NOC subtotal: 8-12 sessions (~2-3 weeks)**

### HRIS Integration (12 TCs)

Currently stubbed (`hris_sync_state` table from migration 0040). Needs real WhatsApp Business API + leave/availability bridge.

**HRIS subtotal: 2-3 sessions**

**Phase 1B grand total: ~62-87 sessions (~3-5 person-months)**

---

## Phase 1C — Net-new modules (125 TCs)

Phase 1C re-runs Phase 1 + 1B as regression, then adds these.

### Ops Tools (33 TCs)

| Module | TCs | Current code state | Effort estimate |
|---|---|---|---|
| Planned Maintenance | 10 | maintenance WO exists; needs schedule + opt-in/out + customer notif | 1-2 sessions |
| Maintenance Escalation | 4 | escalation chain partial (Wave 76 G3.2) | 0.5 session |
| Bulk Plan Change | 8 | **Not built** — admin bulk-edit tool | 1 session |
| Bulk ODP Migration | 6 | **Not built** | 1 session |
| Bulk WO Creation | 4 | **Not built** | 1 session |
| Operational Calendar | 6 | **Not built** — holiday + maintenance window calendar | 1 session |
| Internal Announcements | 5 | **Not built** — admin → staff broadcast | 1 session |
| Cross-Module SLA Ops View | 4 | SLA breaches page exists | 0.5 session |

**Ops subtotal: 6-8 sessions (~1-2 weeks)**

### CS Ticketing (78 TCs)

| Module | TCs | Current code state | Effort estimate |
|---|---|---|---|
| Ticket Types | 8 | `field.tickets` exists with type column | 0.5 session |
| Ticket Lifecycle | 10 | partial via existing CS tickets | 1 session |
| Ticket Channels | 6 | manual ticket creation exists; needs email + WA inbound | 2 sessions |
| Priority & SLA | 10 | SLA template binding exists (TC-WO-005) | 0.5 session |
| Team Assignment | 8 | CS agent + supervisor split exists (Wave 67) | 0.5 session |
| @Mentions | 3 | **Not built** | 0.5 session |
| WO from Ticket | 5 | partial — ticket → WO link exists | 0.5 session |
| Service Requests | 12 | partial via add-on/plan-change requests | 1 session |
| Communication | 6 | message thread exists | 0.5 session |
| CSAT | (counted in dashboards) | CSAT survey exists post-WO | 0.5 session |
| CS Dashboards | (in modules) | Supervisor + agent dashboards exist | 0.5 session |

**CS Ticketing subtotal: 7-9 sessions (~2 weeks)**

**Phase 1C net new total: ~13-17 sessions (~3-4 weeks)**

---

## Phase 1 Enterprise — Entirely new module set (455 TCs)

This is a **standalone CPQ (Configure-Price-Quote) system** with a B2B2C reseller chain. Almost no existing code — `internal/enterprise/` has skeleton work from earlier waves (templates, BOQ statuses, opportunity DTO) but the bulk is green-field.

### CPQ Core (144 TCs)

| Module | TCs | Current code state | Effort estimate |
|---|---|---|---|
| Pricebook | 12 | `enterprise.pricebooks` skeleton — needs version_pinning + auto-calc cost→sell | **2-3 sessions** |
| Opportunity | 11 | `enterprise.opportunities` exists; needs Cold/Warm/Hot stage SLA cron + Pre-BOQ snapshot | **2-3 sessions** |
| BOQ Core | 26 | `enterprise.boq_versions` exists; needs line snapshot + provider assignment + tax mode resolution | **4-5 sessions** |
| Negosiasi | 18 | Negotiation rounds exist but not wired into BOQ flow | **3 sessions** |
| Quotation | 12 | `enterprise.quotations` exists; needs PDF generation + customer signature | **2 sessions** |
| Customer PO | 10 | not implemented | 2 sessions |
| Intercompany PO | 39 | **Not built** — biggest gap; multi-company hierarchy + auto-PO between holdings | **6-8 sessions** |
| EWO Dual | 16 | EWO checklist templates exist; needs dual-WO (client + intercompany) tracking | **2-3 sessions** |

**CPQ subtotal: 23-29 sessions (~5-7 weeks)**

### Tax + Approvals + Provider (54 TCs)

| Module | TCs | Current code state | Effort estimate |
|---|---|---|---|
| Company Tax Profile | 12 | **Not built** — companies table needs PKP/NPWP/faktur_serial | 2 sessions |
| Approval BOQ | 12 | BOQ approval template exists (Wave 67) | 1-2 sessions |
| Provider & Vendor Input | 15 | vendor table partial | 2-3 sessions |

**Tax+Approval+Provider subtotal: 5-7 sessions (~1-2 weeks)**

### Field (Enterprise) (27 TCs)

| Module | TCs | Current code state | Effort estimate |
|---|---|---|---|
| TL Scheduling (Web) | 12 | Field TL Queue exists; needs EWO-specific scheduling | 1-2 sessions |
| Technician App (Mobile) | 15 | Tech app exists; needs EWO branch (separate from WO) | **3-4 sessions** |

**Field subtotal: 4-6 sessions (~1 week)**

### Finance (Enterprise) (36 TCs)

| Module | TCs | Current code state | Effort estimate |
|---|---|---|---|
| Finance Client AR | 18 | partial via billing module | 2-3 sessions |
| Finance Internal Vendor | 10 | **Not built** — intercompany AR/AP | 2 sessions |
| Wholesale Supply | 8 | **Not built** | 1-2 sessions |

**Finance subtotal: 5-7 sessions (~1-2 weeks)**

### Reseller Platform (22 TCs)

Entirely new B2B2C portal for resellers to manage their own customers.

| Module | TCs | Current code state | Effort estimate |
|---|---|---|---|
| Reseller Onboarding | 10 | not implemented | 2-3 sessions |
| Reseller Platform | 12 | not implemented | 3-4 sessions |

**Reseller subtotal: 5-7 sessions (~1-2 weeks)**

### Partnership (34 TCs)

| Module | TCs | Current code state | Effort estimate |
|---|---|---|---|
| Partnership Monthly Submission | 10 | not implemented | 2 sessions |
| Partnership Settlement | 8 | not implemented | 2 sessions |
| Monthly Compliance Check | 16 | not implemented | 2-3 sessions |

**Partnership subtotal: 6-7 sessions (~1-2 weeks)**

### Cross-cutting (153 TCs)

| Module | TCs | Current code state | Effort estimate |
|---|---|---|---|
| Audit Log | 8 | `identity.audit_logs` exists | 0.5 session |
| Notifikasi | 10 | notifyx exists | 0.5 session |
| State Machine | 56 | piecewise across modules | bundled with each module |
| RBAC & Field Masking | 32 | RBAC framework exists; field-level masking partial | 2-3 sessions |
| Edge Case & Concurrency | 35 | needs targeted tests per state machine | bundled |
| Non-Functional | 12 | load testing + perf benchmarks | 2-3 sessions |

**Cross-cutting subtotal: 5-7 sessions**

**Phase 1 Enterprise grand total: ~53-70 sessions (~3-4 person-months)**

---

## Realistic execution roadmap

### Tier 1 — Close out existing Wave 75-79 work (~2 sessions)

- **Wave 80b** — Lock-snapshot wiring + WO checklist via product schema (~2-3h) — closes TC-WO-011 + actually-runtime-pins TC-SCH-011/023/026 + TC-PRD-025
- **Wave 81** — Notifyx + audit emission + TL routing (~3h) — closes TC-USR-019, TC-PRD-013/028, TC-TLP-001/002/003/014/022/023, TC-CRM-005/011, TC-SCH-014

### Tier 2 — Phase 1B billing & finance (~14-19 sessions / 3-5 weeks)

Highest user-impact since billing drives revenue. Modules in priority order:
1. **Wave 82** — Billing Schema deep wiring (TC-Billing-Schema 12)
2. **Wave 83** — OTC + Recurring Billing (TC-OTC 18 + TC-Recurring 12)
3. **Wave 84** — Suspension + Reminder Schedule (10+8)
4. **Wave 85** — Late Fee + Commission Calc + Financial Reporting (4+6+6)
5. **Wave 86** — Add-On Billing + Faktur Pajak DJP (8+10) — DJP e-Faktur needs XML spec familiarity
6. **Wave 87** — Payment Handling + Xendit gateway (12)

### Tier 3 — Phase 1B warehouse (~22-28 sessions / 5-7 weeks)

Highest material-impact since warehouse drives field ops. Priority by dependency:
1. **Wave 88** — Item Types 1-4 + Coding/QR (10+6+4+6+6)
2. **Wave 89** — WO Material List (BOM) + Consumption Recording (6+6)
3. **Wave 90** — Device Return + Asset Retrofit + Location Tracking (8+4+8)
4. **Wave 91** — Network Device Lifecycle deep (32)
5. **Wave 92** — Sub-Warehouse + Threshold Cascade + Stock Opname Tablet (10+8+5)
6. **Wave 93** — Manual Purchase Entry + Inter-Warehouse Transfer (14+6)

### Tier 4 — Phase 1B platform services (~13-18 sessions / 3-4 weeks)

1. **Wave 94** — Payment Svc microservice architecture + Routing (6+10)
2. **Wave 95** — Payment Svc Webhook + H2H Bank + Refund (8+6+4)
3. **Wave 96** — Invoice Svc (5+8+6+6)
4. **Wave 97** — Schema deep dives (68 across 5 modules)
5. **Wave 98** — NOC Monitoring + Fiber Attenuation (30)
6. **Wave 99** — HRIS Integration (12)

### Tier 5 — Phase 1C operations (~13-17 sessions / 3-4 weeks)

1. **Wave 100** — Planned Maintenance + Escalation (14)
2. **Wave 101** — Bulk Ops (Plan/ODP/WO Creation) (18)
3. **Wave 102** — Operational Calendar + Internal Announcements + Cross-Module SLA (15)
4. **Wave 103-105** — CS Ticketing deep (78 across 11 modules)

### Tier 6 — Phase 1 Enterprise (~53-70 sessions / 3-4 months)

This is a full standalone CPQ project. Recommend a separate team track:

1. **Wave 106-110** — CPQ Core (144 TCs across 8 modules)
2. **Wave 111-113** — Tax + Approvals + Provider Input (54 TCs)
3. **Wave 114-115** — Field Enterprise + EWO Dual (27 + bundled)
4. **Wave 116-118** — Finance + Wholesale Supply (36)
5. **Wave 119-121** — Reseller Platform (22)
6. **Wave 122-124** — Partnership + Monthly Compliance (34)
7. **Wave 125-127** — Cross-cutting + non-functional (153)

### Cumulative time-to-100%

| Tier | Sessions | Calendar |
|---|---|---|
| 1 (close Tier C) | 2 | 1 week |
| 2 (Billing/Finance) | 14-19 | 3-5 weeks |
| 3 (Warehouse) | 22-28 | 5-7 weeks |
| 4 (Platform Svc) | 13-18 | 3-4 weeks |
| 5 (Operations + CS) | 13-17 | 3-4 weeks |
| 6 (Enterprise) | 53-70 | 13-17 weeks |
| **TOTAL** | **117-154 sessions** | **6-9 calendar months** |

(Assumes one focused dev session = ~3 hours, one engineer working solo. With a 3-person team running in parallel, divide calendar by ~2.5x → **2.5-4 months**.)

---

## Recommendation for next session

Realistic dev-cycle plan:

1. **This session** delivered Wave 75-80 (~30 backend TCs at domain level + mobile sync). Already committed.
2. **Next session** — Wave 80b + 81 (close out remaining Tier C, ~10 more TCs)
3. **Sessions 3-7** — Phase 1B Billing tier (highest revenue-impact)
4. **Sessions 8-14** — Phase 1B Warehouse tier
5. **Sessions 15-25** — Phase 1B Platform Services
6. **Sessions 26-30** — Phase 1C Operations + CS
7. **Sessions 31-90** — Phase 1 Enterprise (parallel team track recommended)

For each wave I'll generate:
- Migration (where new tables/columns needed)
- Domain entities + tests
- Repos + handlers + permissions
- Frontend pages (where applicable)
- Mobile updates (where applicable)
- Unit + integration tests

---

## What this document IS and IS NOT

**IS:** an honest, scoped, executable roadmap that captures every TC from all 4 catalogs in module-level granularity with effort estimates grounded in current code state. Sessions can pick up cold from this doc.

**IS NOT:** "all tests running 100%". That outcome requires shipping the code in Tiers 1-6, which is months of work. No single session can claim to have done that without lying about what was actually verified.

The real engineering question is **what slice of the 1500 TCs unlocks the most business value first**. My recommendation is Tier 2 (Billing/Finance) — that's what enables monetisation. After that, Warehouse + CS Ticketing close the operational loop. Enterprise CPQ is a standalone product and should be its own initiative.

When you say "next wave" I'll start Wave 80b. When you say "commit" I'll land this doc and proceed.
