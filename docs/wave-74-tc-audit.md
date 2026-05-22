# Wave 74 — TC Audit (Aligned to Updated QA Catalog)

**Date:** 2026-05-23
**Source catalog:** `ION-Phase1-Broadband-Test-Cases-ID (2).xlsx` (updated QA pass after Wave 67-73 fixes)
**Prior pass rate (xlsx v1):** 34.1% — **Current pass rate (xlsx v2):** 82.97%

This document reconciles the QA team's latest verdicts with my Wave 74 read-only code-trace audit, surfacing every TC where QA's runtime result diverges from what my static analysis predicted.

## Aggregate scorecard (QA vs my code-trace)

| Module | Cases | QA: Lulus | QA: Gagal | QA: Blocked | QA: N/A | My PASS | My RISK | My GAP | My SKIP |
|---|---|---|---|---|---|---|---|---|---|
| Hirarki Cabang (TC-BR) | 20 | 14 | 0 | 0 | 6 | 5 | 7 | 2 | 6 |
| Manajemen User (TC-USR) | 19 | 16 | 0 | 0 | 3 | 7 | 6 | 3 | 3 |
| Roles & Permissions (TC-RBAC) | 17 | 0 | 0 | 0 | 17 | 9 | 4 | 4 | 0 |
| Schema System (TC-SCH) | 26 | 11 | 8 | 2 | 5 | 6 | 2 | 14 | 4 |
| Katalog Produk (TC-PRD) | 35 | 18 | 9 | 1 | 7 | 3 | 6 | 19 | 7 |
| CRM — Tambah Lead (TC-CRM) | 23 | 7 | 5 | 3 | 8 | 9 | 2 | 8 | 4 |
| Sales App (Mobile) (TC-SAP) | 24 | 20 | 0 | 0 | 4 | 6 | 6 | 8 | 4 |
| Customer App (Mobile) (TC-CAP) | 23 | 5 | 0 | 0 | 18 | 1 | 6 | 8 | 8 |
| Integrasi RADIUS (TC-RAD) | 21 | 15 | 1 | 0 | 5 | 6 | 3 | 10 | 2 |
| Technician App (TC-WO) | 28 | 21 | 1 | 0 | 6 | 9 | 3 | 11 | 5 |
| Team Lead & Pairing (TC-TLP) | 34 | 24 | 1 | 0 | 9 | 4 | 7 | 15 | 8 |
| **TOTAL** | **270** | **151** | **25** | **6** | **88** | **65** | **52** | **102** | **51** |

- **QA pass rate:** 151/182 in-scope = **83.0%**
- **Code-trace PASS rate:** 65/182 = **35.7%**
- **Delta:** QA reports 86 more passes than my code-trace predicted → see Discrepancy section below.

## Discrepancy summary

- **QA Lulus but code-trace says GAP/RISK:** 74 cases — my static audit missed a feature OR QA used mocked data.
- **QA Gagal/Blocked but code-trace says PASS:** 9 cases — runtime bug in correct-looking code.

### A. QA Lulus, but code-trace flagged as GAP

These TCs warrant a re-grep: either the feature shipped between my audit and QA's run, or my search-hints missed it.

| TC | QA note | My code-trace ref | My note |
|---|---|---|---|
| TC-BR-006 | (no note) | `backend/internal/crm/usecase/service.go:105` | CreateLead has no branch.active check |
| TC-BR-009 | (no note) | `backend/internal/field/adapter/branch/resolver.go:93` | ResolveAddress stub; geo_shape never read |
| TC-CAP-004 | (no note) | `mobile/customer_app/lib/features/services/buy_addon_page.dart:36` | Buy add-on lists all; no plan-compatibility filter |
| TC-CAP-005 | (no note) | `mobile/customer_app/lib/features/onboarding/self_order_page.dart:99` | Self-order has no KTP photo field; only re-upload post-onboarding |
| TC-CAP-008 | (no note) | `mobile/customer_app/lib/features/onboarding/self_order_page.dart:84` | No order summary screen; submit posts directly |
| TC-CRM-002 | Saat pembuatan sudah ada, tapi pipelinenya blm ada filter nya | `backend/migrations/0007_crm.up.sql:55` | No lead_type column on crm.leads; no broadband/enterprise selector |
| TC-CRM-003 | (no note) | `backend/internal/crm/usecase/service.go:242` | Lead has no lead_type; UpdateLead permits any mutation |
| TC-CRM-006 | (no note) | `backend/migrations/0007_crm.up.sql:90` | crm.leads.source CHECK only 5 sources; full 11 only in enterprise |
| TC-CRM-023 | (no note) | `backend/internal/crm/usecase/service.go:242` | MarkConverted stamps but UpdateLead permits flipping back; no locked schema_version_id |
| TC-PRD-001 | (no note) | `backend/internal/crm/domain/product.go:19` | CreateProductInput lacks customer_type + branch_availability |
| TC-PRD-003 | (no note) | `backend/internal/field/adapter/network/activation.go:58` | Bandwidth profile = ProductCode literal; no bandwidth_profile_id column |
| TC-PRD-005 | (no note) | `backend/internal/crm/domain/product.go:31` | NewProduct validates basics only; no branch availability field |
| TC-PRD-006 | masih muncul dan jika di klik {"error":"ERROR: invalid input syntax for type uui... | `backend/internal/crm/adapter/http/portal_priority.go:65` | publicCoverageCheck no sub-area-filtered product list |
| TC-PRD-009 | (no note) | `backend/migrations/0036_phase2_foundation.up.sql:30` | No compatible_plans table; no compatibility validation |
| TC-PRD-014 | belum singkron | `backend/migrations/0007_crm.up.sql:26` | onboarding_schemas keyed by (customer_type,product_type); no FK from products |
| TC-PRD-016 | (no note) | `backend/migrations/0032_internal_foundation.up.sql:85` | platform.schema_definitions has no product_id |
| TC-PRD-018 | (no note) | `backend/migrations/0032_internal_foundation.up.sql:85` | suspension kind exists but no product↔schema slot |
| TC-PRD-022 | (no note) | `backend/internal/crm/domain/product.go:19` | Product has no schema slots; no mix-and-match |
| TC-PRD-023 | Saat ini ada product aktif yg jika dicek ternyata belum dihubungkan dengan schem... | `backend/internal/crm/domain/product.go:31` | NewProduct: no required-schema-slot check |
| TC-PRD-025 | (no note) | `backend/internal/crm/adapter/postgres/customer_repo.go` | Customers store no locked schema version snapshot |
| TC-PRD-026 | (no note) | `backend/internal/platform/usecase/service.go:190` | New orders use override → DEFAULT; no per-product |
| TC-PRD-031 | (no note) | `backend/internal/platform/usecase/service.go:118` | SupersedeSchema has no in-use check |
| TC-RAD-005 | (no note) | `backend/migrations/0048_phase1a_closure.up.sql:38` | Partial index exists for janitor; no sweep job in code |
| TC-RAD-006 | Belum semua flow sesuai baru partial | `backend/internal/network/adapter/radius/local.go:7` | BandwidthProfileID = product.code text; no Radius profile created — stub |
| TC-RAD-009 | Belum semua flow sesuai baru partial | `backend/internal/network/adapter/radius/local.go:112` | Suspend flips status only; no session disconnect / 0 Mbps push |
| TC-RAD-010 | (no note) | `backend/internal/billing/usecase/r2.go:353` | Suspension schema only carries days; no action/throttle_kbps |
| TC-RAD-013 | Belum semua flow sesuai baru partial | `backend/internal/crm/adapter/http/portal_auth.go:1235` | buyAddon writes customer_addons row but never updates radius_accounts |
| TC-RAD-015 | Belum semua flow sesuai baru partial | `backend/internal/crm/adapter/http/phase2.go:819` | decidePlanChange sets applied_at; never updates radius_accounts |
| TC-RAD-016 | Belum semua flow sesuai baru partial | `backend/internal/network/adapter/radius/local.go:1` | LocalRadiusClient is DB-only stub; no FreeRADIUS connector |
| TC-RAD-021 | (no note) | `backend/internal/network/adapter/radius/local.go:148` | Only slog.Info; no identity.audit_logs row, no before/after |
| TC-SAP-004 | (no note) | `mobile/sales_app/lib/features/crm/presentation/pages/new_lead_wizard.dart:118` | KTP picker hardcoded to ImageSource.camera; no gallery |
| TC-SAP-005 | (no note) | `mobile/sales_app/lib/features/crm/presentation/widgets/coverage_map.dart:54` | Pin locked to GPS fix; map drag pans only |
| TC-SAP-014 | belum live tapi saat berhenti baru dia spill jaraknya - Cost concern | `mobile/sales_app/lib/features/crm/presentation/pages/new_lead_wizard.dart:105` | _runCoverage fires only on GPS capture; no live recompute |
| TC-SAP-015 | (no note) | `backend/internal/crm/adapter/http/handler.go:164` | listProducts only search+active; no sub_area scoping |
| TC-SAP-016 | (no note) | `mobile/sales_app/lib/features/crm/presentation/pages/new_lead_wizard.dart:688` | Step3 single plan only; add-on only in Phase2 |
| TC-SAP-017 | (no note) | `mobile/sales_app/lib/features/crm/data/crm_api.dart:87` | Mobile never sends sales_id + backend doesn't auto-stamp from claims |
| TC-SAP-020 | Tinggal filter tipe servis belum ada optionny | `mobile/sales_app/lib/features/crm/presentation/bloc/leads_bloc.dart:20` | Only status + free-text q filter |
| TC-SAP-023 | (no note) | `mobile/sales_app/lib/core/api/api_client.dart:11` | Only 401 refresh-retry; no offline queue |
| TC-SCH-001 | (no note) | `backend/internal/crm/domain/onboarding_schema.go:16` | Onboarding only has documents[]; no steps/automation/SLA |
| TC-SCH-003 | (no note) | `backend/migrations/0035_seed_default_schemas.up.sql:90` | Service schema body is {}; no SLA tier/uptime/bandwidth fields |
| TC-SCH-005 | (no note) | `backend/migrations/0035_seed_default_schemas.up.sql:79` | Only suspend_after_days + grace_minutes; no trigger/approval/throttle/restoration |
| TC-SCH-008 | (no note) | `backend/internal/platform/usecase/service.go:90` | Publish gated only on permission, not approvals |
| TC-SCH-009 | (no note) | `backend/internal/platform/domain/schema.go:71` | No rejected status; no Reject method |
| TC-SCH-013 | Open migration tools di klik belum mengarah kemana2 | `backend/internal/platform/adapter/http/handler.go:55` | No bulk migration endpoint or preview |
| TC-SCH-017 | (no note) | `frontend/src/app/(dashboard)/admin/schemas/[id]/page.tsx:209` | No sample-customer preview |
| TC-SCH-018 | (no note) | `backend/internal/platform/adapter/http/handler.go:55` | No clone route or usecase method |
| TC-TLP-001 | V | `backend/internal/field/usecase/service.go:145` | CreateWOFromOrder stamps branch_id but never calls teamLookup.FindTeamLeader |
| TC-TLP-002 | (no note) | `backend/internal/field/adapter/branch/resolver.go:118` | Recursive Sub→Area→Regional TL lookup implemented but service never invokes |
| TC-TLP-003 | (no note) | `backend/internal/field/adapter/branch/resolver.go:118` | Chain walks to Regional; service ignores teamLookup |
| TC-TLP-007 | (no note) | `backend/internal/field/usecase/sla_watcher.go:54` | runSLAScan logs only; no 80% notification |
| TC-TLP-008 | (no note) | `backend/internal/field/usecase/sla_watcher.go:66` | No notifyx dispatch to Ops Manager on breach |
| TC-TLP-011 | (no note) | `backend/internal/field/usecase/service.go:294` | Backend only validates lead≠observer; caller may pass lead_grade=junior |
| TC-TLP-014 | (no note) | `backend/internal/field/usecase/service.go:330` | UpsertPair persists but no notifyx call |
| TC-TLP-016 | (no note) | `backend/internal/field/usecase/sla_watcher.go:1` | No auto-assign job; suggestedPair not auto-applied |
| TC-TLP-017 | (no note) | `backend/internal/field/usecase/sla_watcher.go:1` | No code path auto-flips WO → Assigned |
| TC-TLP-019 | (no note) | `backend/internal/field/adapter/http/phase2_backlog.go:26` | requestCrossArea is manual; no auto-fallback |
| TC-TLP-020 | (no note) | `backend/internal/identity/adapter/postgres/branch_repo.go:138` | Toggle storage exists; no consumer reads it |
| TC-TLP-022 | belum muncul di audit, dan tolong modul audit bahasnya diperjelas | `backend/internal/field/usecase/service.go:289` | AssignTechnicians has no audit_log writes |
| TC-TLP-026 | (no note) | `frontend/src/app/(dashboard)/admin/cross-area/page.tsx:102` | Target-branch queue lists; no approve/decline action wired |
| TC-TLP-031 | data wilayah lain masih bisa di akses | `backend/internal/field/adapter/http/handler.go:90` | listWOs only filters by query-param branch_id; no JWT scope |
| TC-USR-013 | (no note) | `backend/internal/identity/usecase/service.go:289` | Only self-reference rejected; recursive CTE for cycle detection not implemented |
| TC-USR-014 | (no note) | `frontend/src/app/(dashboard)/admin/users/page.tsx:1` | No OrgChart/TreeView component |
| TC-USR-019 | (no note) | `backend/internal/identity/usecase/service.go:215` | CreateUser/Update/SetActive never call audit.Writer |
| TC-WO-001 | (no note) | `backend/internal/field/port/port.go:165` | WOListFilter lacks technician_id; no self-filter |
| TC-WO-007 | (no note) | `backend/internal/field/adapter/http/phase2.go:822` | markArrived only stamps arrived_at; no status transition |
| TC-WO-008 | (no note) | `backend/internal/field/adapter/network/radius_reader.go:33` | RadiusAccountView omits password by design |
| TC-WO-009 | (no note) | `mobile/tech_app/lib/features/field/presentation/widgets/ont_config_card.dart:68` | No password masking UI because backend never returns password |
| TC-WO-010 | (no note) | `backend/internal/field/usecase/service.go:548` | No ONT auth-failure flag, no pause-WO, no NOC notify |
| TC-WO-012 | (no note) | `backend/internal/field/usecase/service.go:468` | SubmitBAST checks required items; no min_photos or per-photo signature |
| TC-WO-015 | (no note) | `backend/internal/field/usecase/service.go:410` | AddResolutionItem appends fresh; no pre-population from template |
| TC-WO-016 | (no note) | `backend/internal/field/adapter/http/dto.go:260` | Mobile sends 'cable' vs backend 'cabling'; 'escalated' vs 'escalated_to_noc' |
| TC-WO-019 | abis submit ttd WO berhasil dan bisa blk ke menu awal / dashboard | `mobile/tech_app/lib/features/field/presentation/pages/bast_signoff_page.dart:121` | Mobile sends 'otp_remote' vs backend 'remote'; no SMS/email delivery |
| TC-WO-023 | (no note) | `mobile/tech_app/lib/features/field/presentation/pages/reschedule_page.dart:36` | Reschedule has reason + notes + date only; no photo; enum mismatch |
| TC-WO-028 | (no note) | `mobile/tech_app/lib/features/field/data/field_api.dart:84` | Direct HTTP via Dio; no offline queue; no sqflite/hive/isar |

### B. QA Gagal/Blocked, but code-trace flagged as PASS

These TCs are likely runtime bugs in correct-looking code — the code-trace missed a behavior issue.

| TC | QA result | QA note | My code-trace ref |
|---|---|---|---|
| TC-CRM-001 | Gagal | flow status lead: New → Active → Warm → Hot → Converted (atau → Lost / Potential), tambah nomer HP | `backend/internal/crm/domain/lead.go:116` |
| TC-CRM-005 | Gagal | (no note) | `backend/internal/crm/usecase/r2.go:20` |
| TC-CRM-010 | Blocked | Kalo dia tipenya dari referral customer, CS bisa mengetahui asal customer existing dari mana, bentuknya masih UUID | `backend/internal/crm/adapter/http/phase2.go:138` |
| TC-CRM-014 | Blocked | (no note) | `backend/internal/crm/adapter/http/phase2.go:215` |
| TC-PRD-034 | Gagal | (no note) | `backend/migrations/0048_phase1a_closure.up.sql:35` |
| TC-SCH-010 | Gagal | cara balik ke versi sebelumnya? ga harus ada customer untuk bisa di rollback | `backend/internal/platform/usecase/service.go:97` |
| TC-SCH-016 | Gagal | (no note) | `backend/internal/platform/domain/schema.go:309` |
| TC-SCH-025 | Blocked | (no note) | `backend/internal/platform/usecase/service.go:205` |
| TC-WO-011 | Gagal | Belum mengikuti Schema WO yang di bind pada produk | `backend/internal/field/adapter/postgres/checklist_repo.go:29` |

## Per-module aligned tables

Columns: QA result (from xlsx) · Code-trace verdict (from my prior audit) · Evidence path · One-line note.

### Hirarki Cabang (TC-BR)

| TC | QA Result | Code-trace | Evidence | Notes |
|---|---|---|---|---|
| TC-BR-001 | ✓ Lulus | ✓ PASS | `backend/internal/identity/adapter/postgres/branch_repo.go:62` | CreateBranch handles name/code/level=regional + geo_shape via UpdateBranch |
| TC-BR-002 | ✓ Lulus | ✓ PASS | `backend/internal/identity/usecase/service.go:341` | Area parent=regional enforced; SLA inheritance via resolver walks |
| TC-BR-003 | ✓ Lulus | ✓ PASS | `backend/internal/identity/usecase/service.go:398` | validParentLevel enforces sub_area parent=area<br>**QA:** Nyoba buat sub area di bawah area JakTim / Bekasi tidak bisa... |
| TC-BR-004 | ✓ Lulus | ✓ PASS | `backend/internal/identity/domain/branch.go:50` | NewBranch rejects sub_area without parent |
| TC-BR-005 | ✓ Lulus | ✓ PASS | `backend/migrations/0001_platform_core.up.sql:23` | parent_id FK ON DELETE RESTRICT; no Delete usecase |
| TC-BR-006 | ✓ Lulus | ✗ GAP | `backend/internal/crm/usecase/service.go:105` | CreateLead has no branch.active check |
| TC-BR-007 | ✓ Lulus | △ RISK | `backend/migrations/0001_platform_core.up.sql:21` | branches.code globally UNIQUE not per-parent |
| TC-BR-008 | ✓ Lulus | △ RISK | `frontend/src/app/(dashboard)/admin/branches/[id]/page.tsx:376` | Polygon editor is raw GeoJSON textarea; no visual preview<br>**QA:** Ada beberapa branch pas klik detail ga muncul daerah yg tela... |
| TC-BR-009 | ✓ Lulus | ✗ GAP | `backend/internal/field/adapter/branch/resolver.go:93` | ResolveAddress stub; geo_shape never read |
| TC-BR-010 | — N/A | — SKIP | `` | Skipped in catalog |
| TC-BR-011 | — N/A | — SKIP | `` | Skipped in catalog |
| TC-BR-012 | — N/A | — SKIP | `` | Skipped in catalog |
| TC-BR-013 | ✓ Lulus | △ RISK | `backend/internal/identity/domain/user.go:83` | AssignBranch accepts any user→any level; no role-restricted enforcement |
| TC-BR-014 | ✓ Lulus | △ RISK | `backend/internal/identity/domain/user.go:83` | Sales rep can be assigned any branch level |
| TC-BR-015 | ✓ Lulus | △ RISK | `backend/internal/identity/domain/user.go:83` | TL scope free assignment; no level constraint |
| TC-BR-016 | ✓ Lulus | △ RISK | `backend/internal/identity/adapter/postgres/branch_repo.go:127` | odp_strategy stored + read but no NOC-type restriction or recursive inheritance |
| TC-BR-017 | ✓ Lulus | △ RISK | `backend/internal/network/usecase/service.go:282` | CheckCoverage reads from global platform_config, not branch overrides<br>**QA:** semua branch masih dapat mengatur hal tsb, harusnya hanya sc... |
| TC-BR-018 | — N/A | — SKIP | `` | Skipped in catalog |
| TC-BR-019 | — N/A | — SKIP | `` | Skipped in catalog |
| TC-BR-020 | — N/A | — SKIP | `` | Skipped in catalog |

### Manajemen User (TC-USR)

| TC | QA Result | Code-trace | Evidence | Notes |
|---|---|---|---|---|
| TC-USR-001 | ✓ Lulus | ✓ PASS | `backend/internal/identity/usecase/service.go:215` | CreateUser handles all required fields + branch scope |
| TC-USR-002 | ✓ Lulus | ✓ PASS | `backend/internal/identity/usecase/service.go:216` | Pre-check FindByEmail + DB UNIQUE backstop |
| TC-USR-003 | ✓ Lulus | △ RISK | `backend/internal/identity/adapter/postgres/user_repo.go:480` | DB UNIQUE(employee_id) enforced but mapInsertError mislabels 23505 as email_taken |
| TC-USR-004 | ✓ Lulus | △ RISK | `backend/internal/identity/usecase/service.go:193` | /auth/me rejects !u.Active but SetUserActive doesn't RevokeAllForUser |
| TC-USR-005 | ✓ Lulus | ✓ PASS | `backend/internal/identity/adapter/postgres/user_repo.go:399` | Deactivate flips active only; roles + branch untouched |
| TC-USR-006 | — N/A | — SKIP | `` | HRIS bulk import is Phase 2 |
| TC-USR-007 | — N/A | — SKIP | `` | No HRIS import path |
| TC-USR-008 | — N/A | — SKIP | `` | hris_sync_state stubbed |
| TC-USR-009 | ✓ Lulus | ✓ PASS | `backend/internal/identity/domain/user.go:104` | SalesType enum validated + persisted |
| TC-USR-010 | ✓ Lulus | △ RISK | `backend/internal/crm/adapter/identity/sales_user_gateway.go:32` | CRM reads sales_type live per-lead; applies immediately not next-login |
| TC-USR-011 | ✓ Lulus | ✓ PASS | `backend/internal/identity/domain/user.go:122` | TechnicianGrade {senior,junior} validated + persisted |
| TC-USR-012 | ✓ Lulus | ✓ PASS | `backend/internal/identity/adapter/postgres/user_repo.go:148` | reports_to_user_id column + UpdateUser path |
| TC-USR-013 | ✓ Lulus | ✗ GAP | `backend/internal/identity/usecase/service.go:289` | Only self-reference rejected; recursive CTE for cycle detection not implemented |
| TC-USR-014 | ✓ Lulus | ✗ GAP | `frontend/src/app/(dashboard)/admin/users/page.tsx:1` | No OrgChart/TreeView component |
| TC-USR-015 | ✓ Lulus | ✓ PASS | `backend/internal/identity/adapter/postgres/user_repo.go:280` | PermissionsForUser DISTINCT unions across roles |
| TC-USR-016 | ✓ Lulus | △ RISK | `backend/internal/identity/adapter/postgres/token_issuer.go:28` | branch_id baked into JWT; refreshes only on new login |
| TC-USR-017 | ✓ Lulus | △ RISK | `backend/internal/identity/usecase/service.go:96` | Login rejects !Active but no per-platform routing |
| TC-USR-018 | ✓ Lulus | △ RISK | `mobile/tech_app/lib/auth/presentation/bloc/auth_bloc.dart:20` | Client-side gate only; no server-side block on web |
| TC-USR-019 | ✓ Lulus | ✗ GAP | `backend/internal/identity/usecase/service.go:215` | CreateUser/Update/SetActive never call audit.Writer |

### Roles & Permissions (TC-RBAC)

| TC | QA Result | Code-trace | Evidence | Notes |
|---|---|---|---|---|
| TC-RBAC-001 | — N/A | ✓ PASS | `backend/migrations/0001_platform_core.up.sql:162` | All 17 canonical roles seeded |
| TC-RBAC-002 | — N/A | △ RISK | `backend/internal/billing/adapter/http/handler_r2.go:115` | /api/billing/commissions has mine=true opt-in; no server-side own-scope |
| TC-RBAC-003 | — N/A | ✓ PASS | `backend/migrations/0047_master_data_seed.up.sql:335` | sales_manager has crm.lead.manage + crm.plan_change.decide |
| TC-RBAC-004 | — N/A | ✓ PASS | `backend/migrations/0047_master_data_seed.up.sql:367` | cs_agent: ticket.read + customer.read + invoice.read; no schema.manage |
| TC-RBAC-005 | — N/A | △ RISK | `backend/internal/field/usecase/service.go:280` | Team_leader has wo.assign but middleware doesn't intersect by branch |
| TC-RBAC-006 | — N/A | ✗ GAP | `backend/internal/field/adapter/http/phase2.go:73` | team_leader seed omits cross_area.request; outside-area not blocked |
| TC-RBAC-007 | — N/A | △ RISK | `backend/internal/field/adapter/http/handler.go:90` | listWOs filters only by query params; no auto-scope by claims.UserID |
| TC-RBAC-008 | — N/A | ✓ PASS | `backend/migrations/0047_master_data_seed.up.sql:398` | noc has bast.noc_verify + wo.create + maintenance.read + topology.read |
| TC-RBAC-009 | — N/A | ✓ PASS | `backend/migrations/0047_master_data_seed.up.sql:457` | finance_manager has termination.manage + schema_override.manage + policy.manage |
| TC-RBAC-010 | — N/A | ✓ PASS | `backend/migrations/0047_master_data_seed.up.sql:222` | ops_admin has identity/branch/config/role but NOT billing or schema_override |
| TC-RBAC-011 | — N/A | ✓ PASS | `backend/migrations/0047_master_data_seed.up.sql:211` | super_admin via CROSS JOIN full catalog |
| TC-RBAC-012 | — N/A | △ RISK | `frontend/src/app/(dashboard)/admin/roles/page.tsx:485` | Custom role grid is single action per key, no read/create/update/delete/approve columns |
| TC-RBAC-013 | — N/A | ✓ PASS | `backend/pkg/httpserver/middleware.go:108` | RequirePermission returns 403; e2e test confirms tech→403 on /crm/leads |
| TC-RBAC-014 | — N/A | ✗ GAP | `backend/internal/platform/adapter/http/handler.go:64` | Single platform.schema.manage gates all kinds; no per-kind approver config |
| TC-RBAC-015 | — N/A | ✗ GAP | `backend/pkg/auth/jwt.go:29` | claims.BranchID in JWT but no middleware intersects list queries |
| TC-RBAC-016 | — N/A | ✓ PASS | `mobile/tech_app/lib/features/field/data/field_api.dart:18` | Mobile calls same endpoints with Bearer JWT; same RequirePermission |
| TC-RBAC-017 | — N/A | ✗ GAP | `backend/pkg/httpserver/middleware.go:120` | 403 only in structured log; no audit_logs row for denials |

### Schema System (TC-SCH)

| TC | QA Result | Code-trace | Evidence | Notes |
|---|---|---|---|---|
| TC-SCH-001 | ✓ Lulus | ✗ GAP | `backend/internal/crm/domain/onboarding_schema.go:16` | Onboarding only has documents[]; no steps/automation/SLA |
| TC-SCH-002 | — N/A | — SKIP | `` | Skipped in catalog |
| TC-SCH-003 | ✓ Lulus | ✗ GAP | `backend/migrations/0035_seed_default_schemas.up.sql:90` | Service schema body is {}; no SLA tier/uptime/bandwidth fields |
| TC-SCH-004 | — N/A | — SKIP | `` | Skipped in catalog |
| TC-SCH-005 | ✓ Lulus | ✗ GAP | `backend/migrations/0035_seed_default_schemas.up.sql:79` | Only suspend_after_days + grace_minutes; no trigger/approval/throttle/restoration |
| TC-SCH-006 | ✓ Lulus | ✓ PASS | `backend/internal/platform/domain/schema.go:148` | Draft cannot publish without Publish()<br>**QA:** perlu validation fieldnya |
| TC-SCH-007 | ✗ Gagal | ✗ GAP | `backend/internal/platform/adapter/http/handler.go:55` | No /submit-for-approval route; lifecycle skips approval<br>**QA:** "error": "Bad Request: content-to-rule translation failed: u... |
| TC-SCH-008 | ✓ Lulus | ✗ GAP | `backend/internal/platform/usecase/service.go:90` | Publish gated only on permission, not approvals |
| TC-SCH-009 | ✓ Lulus | ✗ GAP | `backend/internal/platform/domain/schema.go:71` | No rejected status; no Reject method |
| TC-SCH-010 | ✗ Gagal | ✓ PASS | `backend/internal/platform/usecase/service.go:97` | Publish auto-supersedes prior; UNIQUE preserves history<br>**QA:** cara balik ke versi sebelumnya? ga harus ada customer untuk ... |
| TC-SCH-011 | ✗ Gagal | △ RISK | `backend/internal/platform/usecase/service.go:226` | Resolver picks latest published; v1.0 customers flip unless override pinned<br>**QA:** update versi berhasil, tapi tampilan data masih menggunakan ... |
| TC-SCH-012 | ✗ Gagal | ✗ GAP | `frontend/src/app/(dashboard)/admin/schemas/[id]/page.tsx:301` | Body is raw JSON textarea; no diff UI |
| TC-SCH-013 | ✓ Lulus | ✗ GAP | `backend/internal/platform/adapter/http/handler.go:55` | No bulk migration endpoint or preview<br>**QA:** Open migration tools di klik belum mengarah kemana2 |
| TC-SCH-014 | ✗ Gagal | ✗ GAP | `backend/internal/platform/usecase/service.go:90` | No bulk migration codepath + no audit emission<br>**QA:** di audit perlu penyesuaian keterangan changes before after |
| TC-SCH-015 | ✗ Gagal | △ RISK | `backend/internal/platform/usecase/service.go:226` | DEFAULT-code fallback works; no per-product schema set |
| TC-SCH-016 | ✗ Gagal | ✓ PASS | `backend/internal/platform/domain/schema.go:309` | ResolveForCustomer shallow-merges override.Patch over body |
| TC-SCH-017 | ✓ Lulus | ✗ GAP | `frontend/src/app/(dashboard)/admin/schemas/[id]/page.tsx:209` | No sample-customer preview |
| TC-SCH-018 | ✓ Lulus | ✗ GAP | `backend/internal/platform/adapter/http/handler.go:55` | No clone route or usecase method |
| TC-SCH-019 | — N/A | ✗ GAP | `backend/migrations/0011_crm_r2.up.sql:43` | customer_type is hard-coded CHECK enum; needs migration |
| TC-SCH-020 | ✓ Lulus | ✓ PASS | `backend/internal/crm/usecase/service.go:162` | CreateLead calls schemas.FindActive; seeds doc checklist |
| TC-SCH-021 | — N/A | — SKIP | `` | Skipped in catalog |
| TC-SCH-022 | ✓ Lulus | ✓ PASS | `backend/internal/platform/domain/schema.go:186` | UpdateDraft requires draft; resolver requires published |
| TC-SCH-023 | ✗ Gagal | ✗ GAP | `backend/internal/platform/usecase/service.go:217` | Resolver does FindLatestPublished unless override.SchemaID pinned |
| TC-SCH-024 | — N/A | — SKIP | `` | Skipped in catalog |
| TC-SCH-025 | ⊘ Blocked | ✓ PASS | `backend/internal/platform/usecase/service.go:205` | Lookup order: override → schema_id → schema_code → DEFAULT |
| TC-SCH-026 | ⊘ Blocked | ✗ GAP | `backend/internal/crm/usecase/service.go:364` | Order has no locked schema_version_id; billing reads latest at tick |

### Katalog Produk (TC-PRD)

| TC | QA Result | Code-trace | Evidence | Notes |
|---|---|---|---|---|
| TC-PRD-001 | ✓ Lulus | ✗ GAP | `backend/internal/crm/domain/product.go:19` | CreateProductInput lacks customer_type + branch_availability |
| TC-PRD-002 | ✓ Lulus | △ RISK | `backend/internal/crm/adapter/postgres/product_repo.go:33` | Column exists; CreateProductInput omits it; default 72h only on create |
| TC-PRD-003 | ✓ Lulus | ✗ GAP | `backend/internal/field/adapter/network/activation.go:58` | Bandwidth profile = ProductCode literal; no bandwidth_profile_id column |
| TC-PRD-004 | ✓ Lulus | ✓ PASS | `backend/internal/crm/adapter/http/portal_priority.go:163` | publicListProducts + ListProducts(ActiveOnly) filter active=TRUE |
| TC-PRD-005 | ✓ Lulus | ✗ GAP | `backend/internal/crm/domain/product.go:31` | NewProduct validates basics only; no branch availability field |
| TC-PRD-006 | ✓ Lulus | ✗ GAP | `backend/internal/crm/adapter/http/portal_priority.go:65` | publicCoverageCheck no sub-area-filtered product list<br>**QA:** masih muncul dan jika di klik {"error":"ERROR: invalid input... |
| TC-PRD-007 | ✓ Lulus | △ RISK | `backend/migrations/0036_phase2_foundation.up.sql:42` | Column + read endpoint exist; no admin CRUD |
| TC-PRD-008 | ✓ Lulus | △ RISK | `backend/migrations/0036_phase2_foundation.up.sql:45` | requires_install flag + read API; no admin CRUD |
| TC-PRD-009 | ✓ Lulus | ✗ GAP | `backend/migrations/0036_phase2_foundation.up.sql:30` | No compatible_plans table; no compatibility validation |
| TC-PRD-010 | — N/A | — SKIP | `` | Skipped in catalog |
| TC-PRD-011 | — N/A | — SKIP | `` | Skipped in catalog |
| TC-PRD-012 | — N/A | — SKIP | `` | Skipped in catalog |
| TC-PRD-013 | ⊘ Blocked | ✗ GAP | `backend/internal/crm/usecase/service.go:84` | CreateProduct + Update emit no audit |
| TC-PRD-014 | ✓ Lulus | ✗ GAP | `backend/migrations/0007_crm.up.sql:26` | onboarding_schemas keyed by (customer_type,product_type); no FK from products<br>**QA:** belum singkron |
| TC-PRD-015 | — N/A | — SKIP | `` | Skipped in catalog |
| TC-PRD-016 | ✓ Lulus | ✗ GAP | `backend/migrations/0032_internal_foundation.up.sql:85` | platform.schema_definitions has no product_id |
| TC-PRD-017 | — N/A | — SKIP | `` | Skipped in catalog |
| TC-PRD-018 | ✓ Lulus | ✗ GAP | `backend/migrations/0032_internal_foundation.up.sql:85` | suspension kind exists but no product↔schema slot |
| TC-PRD-019 | ✗ Gagal | △ RISK | `backend/internal/platform/usecase/service.go:223` | Fallback is to global DEFAULT, not customer-type default |
| TC-PRD-020 | ✓ Lulus | ✓ PASS | `backend/internal/platform/usecase/service.go:226` | FindLatestPublished filters status=published<br>**QA:** schema y sdh di publish tidak semua muncul di pilihan |
| TC-PRD-021 | ✗ Gagal | ✗ GAP | `backend/migrations/0032_internal_foundation.up.sql:85` | schema_definitions has no customer_type column<br>**QA:** masih muncul branch selain NOC |
| TC-PRD-022 | ✓ Lulus | ✗ GAP | `backend/internal/crm/domain/product.go:19` | Product has no schema slots; no mix-and-match |
| TC-PRD-023 | ✓ Lulus | ✗ GAP | `backend/internal/crm/domain/product.go:31` | NewProduct: no required-schema-slot check<br>**QA:** Saat ini ada product aktif yg jika dicek ternyata belum dihu... |
| TC-PRD-024 | ✗ Gagal | ✗ GAP | `backend/internal/platform/usecase/service.go:90` | PublishSchema has no approval workflow |
| TC-PRD-025 | ✓ Lulus | ✗ GAP | `backend/internal/crm/adapter/postgres/customer_repo.go` | Customers store no locked schema version snapshot |
| TC-PRD-026 | ✓ Lulus | ✗ GAP | `backend/internal/platform/usecase/service.go:190` | New orders use override → DEFAULT; no per-product |
| TC-PRD-027 | ✗ Gagal | ✗ GAP | `frontend/src/features/crm/api/crm.ts:7` | Product admin UI absent |
| TC-PRD-028 | ✗ Gagal | ✗ GAP | `backend/internal/platform/usecase/service.go:67` | UpdateDraftSchema/Publish emit no audit with before/after<br>**QA:** Setelah update produk, tidak masuk audit logs |
| TC-PRD-029 | ✗ Gagal | △ RISK | `backend/internal/platform/usecase/service.go:205` | Customer override works; product layer absent |
| TC-PRD-030 | ✗ Gagal | △ RISK | `backend/internal/platform/usecase/service.go:200` | Order is override → DEFAULT; product schema tier missing |
| TC-PRD-031 | ✓ Lulus | ✗ GAP | `backend/internal/platform/usecase/service.go:118` | SupersedeSchema has no in-use check |
| TC-PRD-032 | — N/A | — SKIP | `` | Skipped in catalog |
| TC-PRD-033 | — N/A | — SKIP | `` | Skipped in catalog |
| TC-PRD-034 | ✗ Gagal | ✓ PASS | `backend/migrations/0048_phase1a_closure.up.sql:35` | radius_accounts.temp_expires_at wired from product.temp_activation_window_hours |
| TC-PRD-035 | ✗ Gagal | ✗ GAP | `backend/internal/platform/usecase/service.go:190` | Suspension via customer override → DEFAULT only; no product-bound |

### CRM — Tambah Lead (TC-CRM)

| TC | QA Result | Code-trace | Evidence | Notes |
|---|---|---|---|---|
| TC-CRM-001 | ✗ Gagal | ✓ PASS | `backend/internal/crm/domain/lead.go:116` | NewLead validates full_name/phone/address<br>**QA:** flow status lead: New → Active → Warm → Hot → Converted (ata... |
| TC-CRM-002 | ✓ Lulus | ✗ GAP | `backend/migrations/0007_crm.up.sql:55` | No lead_type column on crm.leads; no broadband/enterprise selector<br>**QA:** Saat pembuatan sudah ada, tapi pipelinenya blm ada filter ny... |
| TC-CRM-003 | ✓ Lulus | ✗ GAP | `backend/internal/crm/usecase/service.go:242` | Lead has no lead_type; UpdateLead permits any mutation |
| TC-CRM-004 | — N/A | △ RISK | `backend/internal/crm/usecase/service.go:124` | Sales-type enforcement for broadband only; no inverse |
| TC-CRM-005 | ✗ Gagal | ✓ PASS | `backend/internal/crm/usecase/r2.go:20` | salesTypeMatchesBroadband accepts 'broadband' and 'both' |
| TC-CRM-006 | ✓ Lulus | ✗ GAP | `backend/migrations/0007_crm.up.sql:90` | crm.leads.source CHECK only 5 sources; full 11 only in enterprise |
| TC-CRM-007 | ✗ Gagal | ✗ GAP | `backend/internal/crm/domain/lead.go:67` | No referrer_customer_id field; no referrer linkage<br>**QA:** customer blocked/pending masih tertampil |
| TC-CRM-008 | ✗ Gagal | ✗ GAP | `frontend/src/features/crm/components/NewLeadModal.tsx:38` | No referrer dropdown in broadband modal<br>**QA:** customer suspend tapi muncul |
| TC-CRM-009 | ✓ Lulus | ✓ PASS | `backend/internal/crm/adapter/http/phase2.go:116` | POST /cs-referrals creates lead with source='cs_referral' |
| TC-CRM-010 | ⊘ Blocked | ✓ PASS | `backend/internal/crm/adapter/http/phase2.go:138` | cs_referral leads in crm.leads with status='new'<br>**QA:** Kalo dia tipenya dari referral customer, CS bisa mengetahui ... |
| TC-CRM-011 | ⊘ Blocked | ✗ GAP | `backend/internal/crm/usecase/service.go:105` | CreateLead has no territory-based auto-assign<br>**QA:** Buat sales account dengan office regional, dan polygon arean... |
| TC-CRM-012 | — N/A | ✓ PASS | `backend/internal/crm/usecase/service.go:129` | Rejects sales rep when SalesTypeFor returns 'enterprise'<br>**QA:** page convert lead ke customer tidak ketemu |
| TC-CRM-013 | ✗ Gagal | ✗ GAP | `backend/internal/crm/usecase/service.go:242` | UpdateLead accepts any Status mutation; hot→new and converted→new both pass<br>**QA:** saat pertama create langsung jadi potential. default assign ... |
| TC-CRM-014 | ⊘ Blocked | ✓ PASS | `backend/internal/crm/adapter/http/phase2.go:215` | GET /leads/overdue filters by updated_at; hardcoded 7-day |
| TC-CRM-015 | — N/A | △ RISK | `backend/internal/crm/adapter/http/handler.go:369` | Reassignment via PATCH; no lead.takeover permission gate<br>**QA:** lead blm bisa |
| TC-CRM-016 | ✓ Lulus | ✓ PASS | `backend/internal/crm/usecase/service.go:251` | LeadStatusLost requires non-empty reason in notes<br>**QA:** Masih bisa tanpa alasan |
| TC-CRM-017 | — N/A | ✓ PASS | `backend/internal/crm/usecase/service.go:160` | CreateLead loads active schema via schemas.FindActive<br>**QA:** belum bisa update schema, jadi schema belum bisa publish |
| TC-CRM-018 | ✓ Lulus | ✓ PASS | `backend/internal/crm/usecase/service.go:321` | ConvertLead returns docs_incomplete if required missing |
| TC-CRM-019 | — N/A | — SKIP | `` | Skipped in catalog |
| TC-CRM-020 | — N/A | — SKIP | `` | Skipped in catalog |
| TC-CRM-021 | — N/A | — SKIP | `` | Skipped in catalog |
| TC-CRM-022 | — N/A | — SKIP | `` | Skipped in catalog |
| TC-CRM-023 | ✓ Lulus | ✗ GAP | `backend/internal/crm/usecase/service.go:242` | MarkConverted stamps but UpdateLead permits flipping back; no locked schema_version_id |

### Sales App (Mobile) (TC-SAP)

| TC | QA Result | Code-trace | Evidence | Notes |
|---|---|---|---|---|
| TC-SAP-001 | ✓ Lulus | ✓ PASS | `mobile/sales_app/lib/auth/data/auth_api.dart:16` | POST /api/identity/auth/login with ION Core creds |
| TC-SAP-002 | ✓ Lulus | △ RISK | `mobile/sales_app/lib/features/crm/presentation/pages/leads_page.dart:278` | Quota/MTD/KPIs shown; no explicit recent-orders tile<br>**QA:** Tampilannya apakah gabisa dibuka buat lihat lbh detail? |
| TC-SAP-003 | ✓ Lulus | ✓ PASS | `mobile/sales_app/lib/features/crm/presentation/pages/new_lead_wizard.dart:117` | KTP + name + NIK + email + phone captured; OCR auto-fill |
| TC-SAP-004 | ✓ Lulus | ✗ GAP | `mobile/sales_app/lib/features/crm/presentation/pages/new_lead_wizard.dart:118` | KTP picker hardcoded to ImageSource.camera; no gallery |
| TC-SAP-005 | ✓ Lulus | ✗ GAP | `mobile/sales_app/lib/features/crm/presentation/widgets/coverage_map.dart:54` | Pin locked to GPS fix; map drag pans only |
| TC-SAP-006 | ✓ Lulus | △ RISK | `backend/internal/network/usecase/service.go:270` | Returns ODP+ports+excess; no explicit 3s SLA |
| TC-SAP-007 | ✓ Lulus | ✓ PASS | `mobile/sales_app/lib/features/crm/presentation/pages/new_lead_wizard.dart:185` | verdict=='no_coverage' blocks _canNext |
| TC-SAP-008 | ✓ Lulus | ✓ PASS | `backend/internal/network/usecase/service.go:283` | routeFactor from platform_config (1.3); cable = straight × factor |
| TC-SAP-009 | ✓ Lulus | ✓ PASS | `backend/internal/network/usecase/service.go:282` | max 210; verdict=covered when cable ≤ maxRun |
| TC-SAP-010 | ✓ Lulus | △ RISK | `backend/internal/crm/domain/lead.go:180` | Excess auto-sets LeadStatusPotential; no explicit Mark Potential choice |
| TC-SAP-011 | ✓ Lulus | ✓ PASS | `backend/internal/network/usecase/service.go:284` | excessCharge = (cable - maxRun) × cable_excess_price_per_meter<br>**QA:** Saat sudah buat cabang baru, dan pop, olt dan odp sudah di s... |
| TC-SAP-012 | ✓ Lulus | △ RISK | `backend/internal/crm/domain/lead.go:181` | Excess auto-sets potential; no rep-typed reason captured<br>**QA:** Branch id ganti dengan nama branchnya dan saat cek detail 40... |
| TC-SAP-013 | — N/A | — SKIP | `` | Skipped in catalog |
| TC-SAP-014 | ✓ Lulus | ✗ GAP | `mobile/sales_app/lib/features/crm/presentation/pages/new_lead_wizard.dart:105` | _runCoverage fires only on GPS capture; no live recompute<br>**QA:** belum live tapi saat berhenti baru dia spill jaraknya - Cost... |
| TC-SAP-015 | ✓ Lulus | ✗ GAP | `backend/internal/crm/adapter/http/handler.go:164` | listProducts only search+active; no sub_area scoping |
| TC-SAP-016 | ✓ Lulus | ✗ GAP | `mobile/sales_app/lib/features/crm/presentation/pages/new_lead_wizard.dart:688` | Step3 single plan only; add-on only in Phase2 |
| TC-SAP-017 | ✓ Lulus | ✗ GAP | `mobile/sales_app/lib/features/crm/data/crm_api.dart:87` | Mobile never sends sales_id + backend doesn't auto-stamp from claims |
| TC-SAP-018 | — N/A | — SKIP | `` | Skipped in catalog |
| TC-SAP-019 | ✓ Lulus | △ RISK | `mobile/sales_app/lib/features/crm/presentation/pages/leads_page.dart:831` | Vertical list grouped by status; not true kanban columns |
| TC-SAP-020 | ✓ Lulus | ✗ GAP | `mobile/sales_app/lib/features/crm/presentation/bloc/leads_bloc.dart:20` | Only status + free-text q filter<br>**QA:** Tinggal filter tipe servis belum ada optionny |
| TC-SAP-021 | — N/A | — SKIP | `` | Skipped in catalog |
| TC-SAP-022 | — N/A | — SKIP | `` | Skipped in catalog |
| TC-SAP-023 | ✓ Lulus | ✗ GAP | `mobile/sales_app/lib/core/api/api_client.dart:11` | Only 401 refresh-retry; no offline queue |
| TC-SAP-024 | ✓ Lulus | △ RISK | `mobile/sales_app/lib/push/push_notifier.dart:35` | Backend writes inbox; mobile FCM kill-switched (ION_PUSH_ENABLED=false)<br>**QA:** Nunggu notivication servis backand |

### Customer App (Mobile) (TC-CAP)

| TC | QA Result | Code-trace | Evidence | Notes |
|---|---|---|---|---|
| TC-CAP-001 | ✓ Lulus | △ RISK | `mobile/customer_app/lib/features/onboarding/coverage_check_page.dart:114` | No map/pin picker; user types lat/lng manually |
| TC-CAP-002 | — N/A | △ RISK | `backend/internal/crm/adapter/http/portal_priority.go:82` | Public coverage uses bbox+Haversine; bypasses ST_Contains<br>**QA:** Berhasil saat mencoba di laut |
| TC-CAP-003 | — N/A | ✗ GAP | `backend/internal/crm/adapter/http/portal_priority.go:158` | publicListProducts no sub-area / location filter<br>**QA:** bebas bisa memilih paket yg mana saja |
| TC-CAP-004 | ✓ Lulus | ✗ GAP | `mobile/customer_app/lib/features/services/buy_addon_page.dart:36` | Buy add-on lists all; no plan-compatibility filter |
| TC-CAP-005 | ✓ Lulus | ✗ GAP | `mobile/customer_app/lib/features/onboarding/self_order_page.dart:99` | Self-order has no KTP photo field; only re-upload post-onboarding |
| TC-CAP-006 | — N/A | △ RISK | `backend/internal/crm/adapter/http/portal_priority.go:317` | Cable distance from check sent in payload; not re-run server-side<br>**QA:** saat ini di sistem apk <180m > 265m, dan saat check ODP mela... |
| TC-CAP-007 | ✓ Lulus | ✓ PASS | `mobile/customer_app/lib/features/onboarding/self_order_page.dart:88` | Excess gates submit via _acceptExcess switch |
| TC-CAP-008 | ✓ Lulus | ✗ GAP | `mobile/customer_app/lib/features/onboarding/self_order_page.dart:84` | No order summary screen; submit posts directly |
| TC-CAP-009 | — N/A | — SKIP | `` | Skipped in catalog |
| TC-CAP-010 | — N/A | — SKIP | `` | Skipped in catalog |
| TC-CAP-011 | — N/A | ✗ GAP | `mobile/customer_app/lib/app/router.dart:49` | No order-tracker route or endpoint<br>**QA:** Tidak ada tampilan informasi progres pesanan |
| TC-CAP-012 | — N/A | — SKIP | `` | Skipped in catalog<br>**QA:** Tidak bisa submit tiket gangguan, hanya bisa ngisi data kete... |
| TC-CAP-013 | — N/A | △ RISK | `backend/internal/crm/adapter/http/portal_auth.go:441` | Backend exposes RADIUS state; no visible online/offline indicator<br>**QA:** Tidak ada tampilan informasi progres |
| TC-CAP-014 | — N/A | — SKIP | `` | Skipped in catalog |
| TC-CAP-015 | — N/A | — SKIP | `` | Skipped in catalog |
| TC-CAP-016 | — N/A | ✗ GAP | `backend/internal/crm/adapter/http/portal_auth.go:1276` | buyAddon sets active but no RADIUS policy push |
| TC-CAP-017 | — N/A | △ RISK | `backend/internal/crm/adapter/http/portal_auth.go:1298` | Install WO created unassigned; no area TL routing logic |
| TC-CAP-018 | — N/A | ✗ GAP | `backend/internal/crm/adapter/http/portal_auth.go:1166` | Plan change always pending; no auto-process for upgrades<br>**QA:** belum ada menu untuk ubah layanan |
| TC-CAP-019 | — N/A | — SKIP | `` | Skipped in catalog<br>**QA:** belum ada menu jika ingin berhenti atau terminated |
| TC-CAP-020 | — N/A | △ RISK | `mobile/customer_app/lib/features/services/tech_tracker_page.dart:58` | Polls every 15s; gated by has_active_wo; no explicit visit-window<br>**QA:** belum ada menu tracking lokasi teknisi |
| TC-CAP-021 | — N/A | ✗ GAP | `mobile/customer_app/lib/` | No on-device signature canvas or /portal/sign* endpoint<br>**QA:** belum ada fitur tanda tangan, lgsg submit |
| TC-CAP-022 | — N/A | — SKIP | `` | Skipped in catalog |
| TC-CAP-023 | — N/A | — SKIP | `` | Skipped in catalog |

### Integrasi RADIUS (TC-RAD)

| TC | QA Result | Code-trace | Evidence | Notes |
|---|---|---|---|---|
| TC-RAD-001 | ✓ Lulus | ✓ PASS | `backend/internal/field/adapter/network/activation.go:53` | Username = unique CustomerNumber; password = crypto/rand 16B hex |
| TC-RAD-002 | ✓ Lulus | △ RISK | `backend/internal/network/adapter/radius/local.go:87` | Password bcrypt-hashed (one-way), not encrypted |
| TC-RAD-003 | ✓ Lulus | ✓ PASS | `backend/internal/field/usecase/service.go:182` | Provision called at WO creation; inserts status='temporary'<br>**QA:** Belum semua flow sesuai baru partial |
| TC-RAD-004 | ✓ Lulus | ✓ PASS | `backend/internal/field/usecase/service.go:189` | proj.TempActivationWindowHrs drives temp_expires_at; default 72h<br>**QA:** Belum semua flow sesuai baru partial |
| TC-RAD-005 | ✓ Lulus | ✗ GAP | `backend/migrations/0048_phase1a_closure.up.sql:38` | Partial index exists for janitor; no sweep job in code |
| TC-RAD-006 | ✓ Lulus | ✗ GAP | `backend/internal/network/adapter/radius/local.go:7` | BandwidthProfileID = product.code text; no Radius profile created — stub<br>**QA:** Belum semua flow sesuai baru partial |
| TC-RAD-007 | — N/A | ✓ PASS | `backend/internal/field/usecase/service.go:684` | onInstallApproved calls ProvisionAndActivate → PromoteToPermanent<br>**QA:** Belum semua flow sesuai baru partial |
| TC-RAD-008 | ✓ Lulus | ✓ PASS | `backend/internal/billing/usecase/r2.go:442` | Dunning tick suspends past suspend-after-days |
| TC-RAD-009 | ✓ Lulus | ✗ GAP | `backend/internal/network/adapter/radius/local.go:112` | Suspend flips status only; no session disconnect / 0 Mbps push<br>**QA:** Belum semua flow sesuai baru partial |
| TC-RAD-010 | ✓ Lulus | ✗ GAP | `backend/internal/billing/usecase/r2.go:353` | Suspension schema only carries days; no action/throttle_kbps |
| TC-RAD-011 | — N/A | △ RISK | `backend/internal/billing/usecase/r2.go:484` | Restore flips status; no bandwidth_profile_id re-push |
| TC-RAD-012 | ✓ Lulus | ✓ PASS | `backend/internal/billing/usecase/r3.go:251` | DeactivateCustomer sets status='deactivated'; row retained |
| TC-RAD-013 | ✓ Lulus | ✗ GAP | `backend/internal/crm/adapter/http/portal_auth.go:1235` | buyAddon writes customer_addons row but never updates radius_accounts<br>**QA:** Belum semua flow sesuai baru partial |
| TC-RAD-014 | — N/A | ✗ GAP | `backend/internal/crm/adapter/http/portal_auth.go:1235` | No add-on removal endpoint touches RADIUS |
| TC-RAD-015 | ✓ Lulus | ✗ GAP | `backend/internal/crm/adapter/http/phase2.go:819` | decidePlanChange sets applied_at; never updates radius_accounts<br>**QA:** Belum semua flow sesuai baru partial |
| TC-RAD-016 | ✓ Lulus | ✗ GAP | `backend/internal/network/adapter/radius/local.go:1` | LocalRadiusClient is DB-only stub; no FreeRADIUS connector<br>**QA:** Belum semua flow sesuai baru partial |
| TC-RAD-017 | — N/A | — SKIP | `` | Skipped in catalog<br>**QA:** Belum semua flow sesuai baru partial |
| TC-RAD-018 | ✓ Lulus | △ RISK | `backend/internal/field/adapter/network/radius_reader.go:41` | Gates in_progress+assigned; RadiusAccountView omits password<br>**QA:** Belum semua flow sesuai baru partial |
| TC-RAD-019 | ✗ Gagal | ✗ GAP | `backend/internal/network/adapter/` | No credential-change endpoint; no NOC-only permission<br>**QA:** Trigger web untuk detail WO melakukan perubahan credentials |
| TC-RAD-020 | — N/A | — SKIP | `` | Skipped in catalog |
| TC-RAD-021 | ✓ Lulus | ✗ GAP | `backend/internal/network/adapter/radius/local.go:148` | Only slog.Info; no identity.audit_logs row, no before/after |

### Technician App (TC-WO)

| TC | QA Result | Code-trace | Evidence | Notes |
|---|---|---|---|---|
| TC-WO-001 | ✓ Lulus | ✗ GAP | `backend/internal/field/port/port.go:165` | WOListFilter lacks technician_id; no self-filter |
| TC-WO-002 | ✓ Lulus | △ RISK | `backend/internal/field/adapter/http/dto.go:31` | woDTO has no customer_name + no assigned_device fields |
| TC-WO-003 | — N/A | — SKIP | `` | Skipped in catalog |
| TC-WO-004 | — N/A | — SKIP | `` | Skipped in catalog |
| TC-WO-005 | ✓ Lulus | ✓ PASS | `backend/internal/field/adapter/http/phase2.go:799` | journey/start stamps timestamp; mobile opens Google Maps |
| TC-WO-006 | ✓ Lulus | ✓ PASS | `frontend/src/app/(dashboard)/field/live-map/page.tsx:47` | Live-map queries /api/field/tech-locations; mobile posts via GpsStreamer |
| TC-WO-007 | ✓ Lulus | ✗ GAP | `backend/internal/field/adapter/http/phase2.go:822` | markArrived only stamps arrived_at; no status transition |
| TC-WO-008 | ✓ Lulus | ✗ GAP | `backend/internal/field/adapter/network/radius_reader.go:33` | RadiusAccountView omits password by design |
| TC-WO-009 | ✓ Lulus | ✗ GAP | `mobile/tech_app/lib/features/field/presentation/widgets/ont_config_card.dart:68` | No password masking UI because backend never returns password |
| TC-WO-010 | ✓ Lulus | ✗ GAP | `backend/internal/field/usecase/service.go:548` | No ONT auth-failure flag, no pause-WO, no NOC notify |
| TC-WO-011 | ✗ Gagal | ✓ PASS | `backend/internal/field/adapter/postgres/checklist_repo.go:29` | FindTemplateFor loads dynamic template<br>**QA:** Belum mengikuti Schema WO yang di bind pada produk |
| TC-WO-012 | ✓ Lulus | ✗ GAP | `backend/internal/field/usecase/service.go:468` | SubmitBAST checks required items; no min_photos or per-photo signature |
| TC-WO-013 | ✓ Lulus | △ RISK | `mobile/tech_app/lib/features/field/data/field_api.dart:191` | Backend exposes photo_tag; mobile ChecklistItem drops it |
| TC-WO-014 | ✓ Lulus | ✓ PASS | `mobile/tech_app/lib/features/field/presentation/pages/checklist_response_page.dart:356` | _SpeedtestField pipe-encodes; backend parses |
| TC-WO-015 | ✓ Lulus | ✗ GAP | `backend/internal/field/usecase/service.go:410` | AddResolutionItem appends fresh; no pre-population from template |
| TC-WO-016 | ✓ Lulus | ✗ GAP | `backend/internal/field/adapter/http/dto.go:260` | Mobile sends 'cable' vs backend 'cabling'; 'escalated' vs 'escalated_to_noc' |
| TC-WO-017 | — N/A | — SKIP | `` | Skipped in catalog<br>**QA:** warehaouse modulenya belum ada |
| TC-WO-018 | ✓ Lulus | ✓ PASS | `mobile/tech_app/lib/features/field/presentation/pages/bast_signoff_page.dart:107` | Mobile uploads sig PNG + GPS; backend stamps fields |
| TC-WO-019 | ✓ Lulus | ✗ GAP | `mobile/tech_app/lib/features/field/presentation/pages/bast_signoff_page.dart:121` | Mobile sends 'otp_remote' vs backend 'remote'; no SMS/email delivery<br>**QA:** abis submit ttd WO berhasil dan bisa blk ke menu awal / dash... |
| TC-WO-020 | ✓ Lulus | ✓ PASS | `backend/internal/field/usecase/service.go:456` | SubmitBAST asserts WO status is in_progress; required-checklist gate |
| TC-WO-021 | ✓ Lulus | △ RISK | `backend/internal/field/usecase/service.go:492` | compiled_data has wo_number/customer/address/checklist/resolution; photos as URLs only |
| TC-WO-022 | ✓ Lulus | ✓ PASS | `backend/internal/field/usecase/service.go:537` | SubmitBAST flips WO to pending_noc_verification |
| TC-WO-023 | ✓ Lulus | ✗ GAP | `mobile/tech_app/lib/features/field/presentation/pages/reschedule_page.dart:36` | Reschedule has reason + notes + date only; no photo; enum mismatch |
| TC-WO-024 | ✓ Lulus | ✓ PASS | `backend/internal/field/usecase/r2.go:80` | RescheduleWO flips status to WOStatusRescheduled |
| TC-WO-025 | — N/A | ✓ PASS | `backend/internal/field/domain/work_order.go:182` | validTransitions enforces full chain<br>**QA:** NOC belum bisa approve atau verifikasi kerjaan WO setelah se... |
| TC-WO-026 | — N/A | — SKIP | `` | Skipped in catalog |
| TC-WO-027 | — N/A | — SKIP | `` | Skipped in catalog |
| TC-WO-028 | ✓ Lulus | ✗ GAP | `mobile/tech_app/lib/features/field/data/field_api.dart:84` | Direct HTTP via Dio; no offline queue; no sqflite/hive/isar |

### Team Lead & Pairing (TC-TLP)

| TC | QA Result | Code-trace | Evidence | Notes |
|---|---|---|---|---|
| TC-TLP-001 | ✓ Lulus | ✗ GAP | `backend/internal/field/usecase/service.go:145` | CreateWOFromOrder stamps branch_id but never calls teamLookup.FindTeamLeader<br>**QA:** V |
| TC-TLP-002 | ✓ Lulus | ✗ GAP | `backend/internal/field/adapter/branch/resolver.go:118` | Recursive Sub→Area→Regional TL lookup implemented but service never invokes |
| TC-TLP-003 | ✓ Lulus | ✗ GAP | `backend/internal/field/adapter/branch/resolver.go:118` | Chain walks to Regional; service ignores teamLookup |
| TC-TLP-004 | ✓ Lulus | △ RISK | `frontend/src/app/(dashboard)/field/work-orders/page.tsx:493` | Queue shows fields but no service_type filter; uses created_at |
| TC-TLP-005 | ✓ Lulus | △ RISK | `backend/internal/field/usecase/service.go:169` | sla_due_at uses install SLA only; no priority-aware variant |
| TC-TLP-006 | ✓ Lulus | △ RISK | `frontend/src/app/(dashboard)/field/work-orders/page.tsx:373` | Filters status/priority/date/branch text-match; no sub-area or service-type |
| TC-TLP-007 | ✓ Lulus | ✗ GAP | `backend/internal/field/usecase/sla_watcher.go:54` | runSLAScan logs only; no 80% notification |
| TC-TLP-008 | ✓ Lulus | ✗ GAP | `backend/internal/field/usecase/sla_watcher.go:66` | No notifyx dispatch to Ops Manager on breach |
| TC-TLP-009 | ✓ Lulus | ✓ PASS | `backend/internal/field/usecase/service.go:289` | AssignTechnicians upserts pair; modal forces senior+junior |
| TC-TLP-010 | — N/A | — SKIP | `` | Skipped in catalog<br>**QA:** V |
| TC-TLP-011 | ✓ Lulus | ✗ GAP | `backend/internal/field/usecase/service.go:294` | Backend only validates lead≠observer; caller may pass lead_grade=junior |
| TC-TLP-012 | — N/A | — SKIP | `` | Skipped in catalog |
| TC-TLP-013 | — N/A | — SKIP | `` | Skipped in catalog |
| TC-TLP-014 | ✓ Lulus | ✗ GAP | `backend/internal/field/usecase/service.go:330` | UpsertPair persists but no notifyx call |
| TC-TLP-015 | ✓ Lulus | △ RISK | `backend/internal/identity/adapter/postgres/branch_repo.go:138` | wo_auto_assign jsonb stored but never read; toggle cosmetic |
| TC-TLP-016 | ✓ Lulus | ✗ GAP | `backend/internal/field/usecase/sla_watcher.go:1` | No auto-assign job; suggestedPair not auto-applied |
| TC-TLP-017 | ✓ Lulus | ✗ GAP | `backend/internal/field/usecase/sla_watcher.go:1` | No code path auto-flips WO → Assigned |
| TC-TLP-018 | — N/A | △ RISK | `backend/internal/field/adapter/http/phase2_backlog.go:119` | suggestedPair excludes unavailable; not invoked from any auto path |
| TC-TLP-019 | ✓ Lulus | ✗ GAP | `backend/internal/field/adapter/http/phase2_backlog.go:26` | requestCrossArea is manual; no auto-fallback |
| TC-TLP-020 | ✓ Lulus | ✗ GAP | `backend/internal/identity/adapter/postgres/branch_repo.go:138` | Toggle storage exists; no consumer reads it |
| TC-TLP-021 | ✓ Lulus | △ RISK | `backend/internal/field/usecase/service.go:330` | UpsertPair wipes + writes new pair; no pre-dispatch guard |
| TC-TLP-022 | ✓ Lulus | ✗ GAP | `backend/internal/field/usecase/service.go:289` | AssignTechnicians has no audit_log writes<br>**QA:** belum muncul di audit, dan tolong modul audit bahasnya diper... |
| TC-TLP-023 | ✗ Gagal | ✗ GAP | `backend/internal/field/adapter/postgres/assignment_repo.go:24` | UpsertPair deletes+inserts with no notification |
| TC-TLP-024 | — N/A | — SKIP | `` | Skipped in catalog |
| TC-TLP-025 | ✓ Lulus | ✓ PASS | `backend/internal/field/adapter/http/phase2_backlog.go:26` | POST /work-orders/{id}/cross-area marks fields |
| TC-TLP-026 | ✓ Lulus | ✗ GAP | `frontend/src/app/(dashboard)/admin/cross-area/page.tsx:102` | Target-branch queue lists; no approve/decline action wired |
| TC-TLP-027 | ✓ Lulus | ✓ PASS | `backend/internal/field/usecase/service.go:279` | RouteToTeam sets IsCrossArea=true when branch differs |
| TC-TLP-028 | ✓ Lulus | ✓ PASS | `backend/internal/field/usecase/service.go:176` | New WOs land in WOStatusUnassigned; explicit AssignTechnicians needed |
| TC-TLP-029 | — N/A | — SKIP | `` | Skipped in catalog<br>**QA:** Belum lengkap |
| TC-TLP-030 | ✓ Lulus | △ RISK | `frontend/src/app/(dashboard)/field/` | No dedicated /field/team-leader route; scattered surfaces |
| TC-TLP-031 | ✓ Lulus | ✗ GAP | `backend/internal/field/adapter/http/handler.go:90` | listWOs only filters by query-param branch_id; no JWT scope<br>**QA:** data wilayah lain masih bisa di akses |
| TC-TLP-032 | — N/A | — SKIP | `` | Skipped in catalog |
| TC-TLP-033 | — N/A | — SKIP | `` | Skipped in catalog |
| TC-TLP-034 | — N/A | — SKIP | `` | Skipped in catalog |

## Confirmed remaining gaps (QA Gagal/Blocked) — fix queue

These 31 cases are real bugs in current build per QA's latest pass.

| TC | Priority | QA Result | QA Note | Code-trace |
|---|---|---|---|---|
| TC-CRM-001 | — | Gagal | flow status lead: New → Active → Warm → Hot → Converted (atau → Lost / Potential), tambah nomer HP | PASS @ `backend/internal/crm/domain/lead.go:116` |
| TC-CRM-005 | — | Gagal | (no note) | PASS @ `backend/internal/crm/usecase/r2.go:20` |
| TC-CRM-007 | — | Gagal | customer blocked/pending masih tertampil | GAP @ `backend/internal/crm/domain/lead.go:67` |
| TC-CRM-008 | — | Gagal | customer suspend tapi muncul | GAP @ `frontend/src/features/crm/components/NewLeadModal.tsx:38` |
| TC-CRM-010 | — | Blocked | Kalo dia tipenya dari referral customer, CS bisa mengetahui asal customer existing dari mana, bentuknya masih UUID | PASS @ `backend/internal/crm/adapter/http/phase2.go:138` |
| TC-CRM-011 | — | Blocked | Buat sales account dengan office regional, dan polygon areanya in range dengan ODP dan NOC, tetapi masih tidak bisa orde... | GAP @ `backend/internal/crm/usecase/service.go:105` |
| TC-CRM-013 | — | Gagal | saat pertama create langsung jadi potential. default assign new status lead | GAP @ `backend/internal/crm/usecase/service.go:242` |
| TC-CRM-014 | — | Blocked | (no note) | PASS @ `backend/internal/crm/adapter/http/phase2.go:215` |
| TC-PRD-013 | — | Blocked | (no note) | GAP @ `backend/internal/crm/usecase/service.go:84` |
| TC-PRD-019 | — | Gagal | (no note) | RISK @ `backend/internal/platform/usecase/service.go:223` |
| TC-PRD-021 | — | Gagal | masih muncul branch selain NOC | GAP @ `backend/migrations/0032_internal_foundation.up.sql:85` |
| TC-PRD-024 | — | Gagal | (no note) | GAP @ `backend/internal/platform/usecase/service.go:90` |
| TC-PRD-027 | — | Gagal | (no note) | GAP @ `frontend/src/features/crm/api/crm.ts:7` |
| TC-PRD-028 | — | Gagal | Setelah update produk, tidak masuk audit logs | GAP @ `backend/internal/platform/usecase/service.go:67` |
| TC-PRD-029 | — | Gagal | (no note) | RISK @ `backend/internal/platform/usecase/service.go:205` |
| TC-PRD-030 | — | Gagal | (no note) | RISK @ `backend/internal/platform/usecase/service.go:200` |
| TC-PRD-034 | — | Gagal | (no note) | PASS @ `backend/migrations/0048_phase1a_closure.up.sql:35` |
| TC-PRD-035 | — | Gagal | (no note) | GAP @ `backend/internal/platform/usecase/service.go:190` |
| TC-RAD-019 | — | Gagal | Trigger web untuk detail WO melakukan perubahan credentials | GAP @ `backend/internal/network/adapter/` |
| TC-SCH-007 | — | Gagal | "error": "Bad Request: content-to-rule translation failed: unsupported schema type: WORK_ORDER"  curl 'https://ion-broad... | GAP @ `backend/internal/platform/adapter/http/handler.go:55` |
| TC-SCH-010 | — | Gagal | cara balik ke versi sebelumnya? ga harus ada customer untuk bisa di rollback | PASS @ `backend/internal/platform/usecase/service.go:97` |
| TC-SCH-011 | — | Gagal | update versi berhasil, tapi tampilan data masih menggunakan versi terlama | RISK @ `backend/internal/platform/usecase/service.go:226` |
| TC-SCH-012 | — | Gagal | (no note) | GAP @ `frontend/src/app/(dashboard)/admin/schemas/[id]/page.tsx:301` |
| TC-SCH-014 | — | Gagal | di audit perlu penyesuaian keterangan changes before after | GAP @ `backend/internal/platform/usecase/service.go:90` |
| TC-SCH-015 | — | Gagal | (no note) | RISK @ `backend/internal/platform/usecase/service.go:226` |
| TC-SCH-016 | — | Gagal | (no note) | PASS @ `backend/internal/platform/domain/schema.go:309` |
| TC-SCH-023 | — | Gagal | (no note) | GAP @ `backend/internal/platform/usecase/service.go:217` |
| TC-SCH-025 | — | Blocked | (no note) | PASS @ `backend/internal/platform/usecase/service.go:205` |
| TC-SCH-026 | — | Blocked | (no note) | GAP @ `backend/internal/crm/usecase/service.go:364` |
| TC-TLP-023 | — | Gagal | (no note) | GAP @ `backend/internal/field/adapter/postgres/assignment_repo.go:24` |
| TC-WO-011 | — | Gagal | Belum mengikuti Schema WO yang di bind pada produk | PASS @ `backend/internal/field/adapter/postgres/checklist_repo.go:29` |
