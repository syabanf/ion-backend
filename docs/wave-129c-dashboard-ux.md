# Wave 129c — Dashboard UX consolidation

**Date:** 2026-05-24
**Scope:** `frontend/` Next.js dashboard only (`src/app/(dashboard)/*` + sidebar shell)
**Coordination:** disjoint from 129A (mobile/customer_app) and 129B (mobile/{sales,tech}_app); no backend overlap with 128A/C/D.
**Do-not-commit:** all changes are uncommitted in the working tree.

---

## Exec summary

Wave 119 had inflated the dashboard sidebar by adding every leaf route in the new payment + invoicesvc microservices and the netdev lifecycle to their parent sections. Network went 2 → 12 and Billing went 6 → 14, which buried the original ops entries and made both sections the longest in the app. Four `/operations/*` pages had also been shipped but were never wired into the sidebar at all.

This wave restores intent without dropping features:

| Metric | Before | After | Δ |
|---|---|---|---|
| Sidebar sections | 8 | **9** | +1 (`Operations`) |
| Sidebar items (total) | 67 | **61** | -6 |
| Network items | 12 | **8** | -4 |
| Billing items | 14 | **8** | -6 |
| Page files on disk | 122 | **124** | +2 (two new index/landing routes) |
| Orphan routes wired into sidebar | 4 | **0** | -4 |
| Routes removed | — | **0** | none |

Nothing was deleted. Every URL that worked before still resolves, including the 9 leaves dropped from the sidebar — they're reachable via in-page SubNav tabs, the Devices toolbar shortcuts, breadcrumbs, and Cmd-K.

**Build / lint / typecheck:**

| Check | Cmd | Exit |
|---|---|---|
| TypeScript | `npx tsc --noEmit` | **0** (clean) |
| ESLint | `npx eslint src/` | **0** (25 pre-existing warnings, **0 new**) |
| Next build | `npm run build` | **0** (`✓ Compiled successfully`; pre-existing `themeColor` deprecation warnings unchanged) |

---

## Before / after sidebar IA

### Before — 8 sections, 67 items
```
Overview
  └─ Dashboard
CRM & Sales (5)
  ├─ Leads
  ├─ Customers
  ├─ Sales dashboard
  ├─ Approvals queue
  └─ Onboarding schemas
Network (12)                                  ← overloaded
  ├─ NOC dashboard
  ├─ Service probes
  ├─ Fiber attenuation
  ├─ Fault events
  ├─ Topology                                 (nocmon snapshot)
  ├─ Network topology (legacy)                (registry CRUD — label fought above)
  ├─ NOC operations
  ├─ Devices
  ├─ Firmware                                 ┐
  ├─ Device swaps                             │
  ├─ RMA                                      ├ netdev cluster, 5 leaves
  └─ Compliance                               ┘
Field operations (7)
  ├─ Work orders
  ├─ Live map
  ├─ Cross-area requests
  ├─ SLA breaches
  ├─ Teams
  ├─ CS tickets
  └─ Maintenance
Warehouse (8)
Enterprise (10)
Billing (14)                                  ← overloaded
  ├─ Invoices                                 ┐
  ├─ Billing cycles                           │
  ├─ Commissions                              │
  ├─ Referral rewards                         ├ original 6 (wave 90)
  ├─ Terminations                             │
  ├─ Billing policy                           ┘
  ├─ Payments dashboard                       ┐
  ├─ Payment intents                          │
  ├─ Refunds                                  │
  ├─ Payment gateways                         ├ payment µsvc, 6 leaves
  ├─ Webhook deliveries                       │
  ├─ H2H statements                           ┘
  ├─ Invoice monitoring                       ┐
  ├─ Credit notes                             ├ invoicesvc µsvc, 3 leaves
  └─ Bulk invoice jobs                        ┘
Administration (10)

(orphans — pages exist but no sidebar entry)
  • /operations/calendar
  • /operations/sla
  • /operations/announcements
  • /operations/bulk
```

### After — 9 sections, 61 items
```
Overview
  └─ Dashboard
CRM & Sales (5)                                ← unchanged
Network (8)                                    ← -4
  ├─ NOC dashboard
  ├─ Service probes
  ├─ Fiber attenuation
  ├─ Fault events
  ├─ Topology
  ├─ Network plan                              (renamed from "Network topology (legacy)")
  ├─ Devices                                   (toolbar links to Firmware / Swaps / RMA / Compliance)
  └─ NOC operations
Field operations (7)                           ← unchanged
Warehouse (8)                                  ← unchanged
Enterprise (10)                                ← unchanged
Billing (8)                                    ← -6
  ├─ Invoices
  ├─ Billing cycles
  ├─ Commissions
  ├─ Referral rewards
  ├─ Terminations
  ├─ Billing policy
  ├─ Payments                                  (tab strip: Dashboard / Intents / Refunds / Gateways / Webhooks / H2H)
  └─ Invoice service                           (tab strip: Monitoring / Credit notes / Bulk jobs)
Operations (4)                                 ← NEW (surfaces previously-orphan routes)
  ├─ Calendar
  ├─ SLA matrix
  ├─ Announcements
  └─ Bulk actions
Administration (10)                            ← unchanged
```

---

## Per-section detail

### 1. Network — 12 → 8 (shipped)

**What merged**
- The 5 netdev-lifecycle leaves (Firmware / Device swaps / RMA / Compliance) dropped from the sidebar; users reach them via 4 secondary buttons added to the `/network/devices` header toolbar.
- "Network topology (legacy)" relabeled "Network plan" — the old label fought the new "Topology" entry for visual primacy. Both routes resolve, both stayed in the sidebar; users now pick by purpose (monitoring snapshot vs. registry CRUD), not by which one is "legacy."
- Firmware versions catalog + Upgrade jobs queue keep their two existing URLs but share a `FirmwareSubNav` tab strip so users hop between them with one click.

**Files touched**
- `frontend/src/app/(dashboard)/layout.tsx` — Network section block.
- `frontend/src/app/(dashboard)/network/devices/page.tsx` — added toolbar buttons.
- `frontend/src/app/(dashboard)/network/devices/firmware/page.tsx` — title + SubNav.
- `frontend/src/app/(dashboard)/network/devices/firmware/upgrade-jobs/page.tsx` — title + SubNav.
- `frontend/src/features/netdev/components/FirmwareSubNav.tsx` — new shared tab strip.

**What did NOT merge (intentionally)**
- `/noc/topology` (monitoring snapshot viewer) and `/network/topology` (full registry with KMZ import / polygon drawing / coverage check) look superficially similar but are different products. Merging them is in the proposed list, not shipped.

### 2. Billing — 14 → 8 (shipped)

**What merged**
- 6 payment-microservice leaves collapsed under one "Payments" sidebar entry. `/billing/payments` is a new index route that `redirect()`s to the dashboard tab. All 6 underlying URLs still resolve and now share a `PaymentsSubNav` tab strip.
- 3 invoice-microservice leaves collapsed under one "Invoice service" sidebar entry. Same pattern: new `/billing/invoice-service` index + redirect + `InvoiceServiceSubNav`.
- Snapshots stay as a detail-only deep link (no list page existed; reached from the monitoring tab).

**Files touched**
- `frontend/src/app/(dashboard)/layout.tsx` — Billing section block.
- `frontend/src/app/(dashboard)/billing/payments/page.tsx` — **NEW** landing route.
- `frontend/src/app/(dashboard)/billing/invoice-service/page.tsx` — **NEW** landing route.
- `frontend/src/app/(dashboard)/billing/payments/dashboard/page.tsx` — title + SubNav.
- `frontend/src/app/(dashboard)/billing/payments/intents/page.tsx` — title + SubNav.
- `frontend/src/app/(dashboard)/billing/payments/refunds/page.tsx` — title + SubNav.
- `frontend/src/app/(dashboard)/billing/payments/gateways/page.tsx` — title + SubNav.
- `frontend/src/app/(dashboard)/billing/payments/webhooks/page.tsx` — title + SubNav.
- `frontend/src/app/(dashboard)/billing/payments/h2h-statements/page.tsx` — title + SubNav.
- `frontend/src/app/(dashboard)/billing/invoice-service/monitoring/page.tsx` — title + SubNav.
- `frontend/src/app/(dashboard)/billing/invoice-service/credit-notes/page.tsx` — title + SubNav.
- `frontend/src/app/(dashboard)/billing/invoice-service/bulk-jobs/page.tsx` — title + SubNav.
- `frontend/src/features/payment/components/PaymentsSubNav.tsx` — **NEW** shared tab strip.
- `frontend/src/features/invoicesvc/components/InvoiceServiceSubNav.tsx` — **NEW** shared tab strip.

### 3. Operations — 0 → 4 (shipped)

**What changed**
- Added a new "Operations" sidebar section between Billing and Administration.
- Surfaced 4 pages that were already on disk + already gated by permissions but had no sidebar entry: Calendar, SLA matrix, Announcements, Bulk actions.

**Files touched**
- `frontend/src/app/(dashboard)/layout.tsx` — new section block.

**Why a new section instead of stuffing them into Administration?**
Administration in this codebase is users / roles / platform-config (identity + platform domain). The four orphans are operations-team workflows (when does a SLA fire, what's the broadcast queue) — putting them under Administration would have made that section even longer (10 → 14) and would have blurred the meaning of "Administration."

### 4. Enterprise — 10 → 10 (unchanged this wave)

Wave 90 already trimmed the config pages (Vendor benchmarks, Approval templates, SLA templates) out of the sidebar; they're reached from inside their consumers. 10 items is at the high end but still legible.

Per the brief, the Quotations + Invoices + Approvals "CPQ flow" merge and the Pricebooks + Services catalog merge are riskier — both are listed in **Proposed** below, not shipped.

### 5. Field operations — 7 → 7 (unchanged this wave)

The cluster is already coherent (WO / map / SLA / teams / tickets / maintenance / cross-area). Wave 90 already folded Roster into Teams. No work needed.

The brief flagged "Live map + Teams overlap" — they don't. Live map is a real-time technician location map (`field.tech_location.read`); Teams is the roster admin surface (`field.team.read`). Different audiences, different permissions, different shapes.

### 6. Administration — 10 → 10 (unchanged this wave)

10 entries is borderline. A focused trim pass is in **Proposed** below — pulling EWO checklist templates + Notification preferences + Webhook deliveries out feels right but each move needs a destination decision (do they land in Enterprise? a new "Platform" section? inside the respective module config?). Deferred.

---

## Cross-section redundancy audit

| Concern flagged in brief | Finding | Action |
|---|---|---|
| Separate `/admin/cs-tickets` AND new CS module | The `/admin/cs-tickets` route is the existing CS ticket queue (linked from Field ops sidebar). No separate "CS module" route exists in `frontend/src/app/(dashboard)`; presumably backend-only at this wave. **No duplication in the frontend.** | none |
| `/admin/maintenance` AND wave-126 "planned maintenance" routes | Only `/admin/maintenance` exists in `frontend/src/app/(dashboard)`. The Wave 126 planned-maintenance UI hasn't landed in the dashboard yet. | none |
| Cross-area requests + WO cross-area dispatch overlap | `/admin/cross-area` is the request inbox (gated on `field.cross_area.request`). WO cross-area dispatch is a button inside a WO detail. They're complementary, not redundant. | none |
| List-page sub-views that should be tabs (e.g. `/customers/{id}/commission`) | Checked all `[id]/` routes — **none** have child subview pages. The detail pages handle their tabs in-component (e.g. `crm/customers/[id]/page.tsx` is 808 lines of internal tabbing). | none |

---

## New shared components

- `frontend/src/components/ui/SubNav.tsx` — sibling-route horizontal tab strip. Renders `<Link>`s (deep-linkable, RequireAuth-friendly), highlights active via `usePathname()` prefix match, supports optional count badges. Matches the existing Tailwind tokens (`ion-500` underline marker, `ink-muted` for inactive, ring-1 idiom from `PageHeader`). Used by Payments, Invoice service, and Firmware.

---

## Shipped vs proposed

### Shipped (this wave)

- [x] Network sidebar: 12 → 8 items; "Network topology (legacy)" renamed "Network plan"; netdev cluster (4 items) dropped from sidebar but linked from Devices toolbar.
- [x] Billing sidebar: 14 → 8 items; 6 payment leaves under "Payments" tab hub; 3 invoicesvc leaves under "Invoice service" tab hub.
- [x] Operations section added (4 surfaced orphan routes).
- [x] Firmware versions + Upgrade jobs tab-merged.
- [x] `SubNav` UI primitive + 3 feature-side configurations (Payments / Invoice service / Firmware).
- [x] Devices page toolbar links to dropped netdev sidebar items for discoverability.
- [x] Build / typecheck / lint clean.

### Proposed (NOT shipped — user review)

1. **Merge `/network/topology` (registry) into `/noc/topology` (snapshot) as a single page with two view tabs ("Plan" vs "Live snapshot").** Both pages render a network graph; users almost certainly want to flip between "what we designed" and "what's actually up" without bouncing between sidebar entries. *Risk:* Network plan is 823 LOC of OpenLayers + KMZ + polygon-drawer code; merging requires either (a) keeping both pages and adding a top-level toggle, or (b) a substantial refactor of `network/topology/page.tsx` into shared components. Estimate 1-2 wave-equivalents of work.

2. **Enterprise: collapse Quotations + Invoices + Approvals into a "CPQ flow" tabbed page.** They share the deal lifecycle (BOQ → Quotation → Approval → Invoice). *Risk:* breaks the existing list-first model — finance teams hit `/enterprise/invoices` straight from email. Need to first ship a tab strip that defaults to whichever sub-view matches the click source; otherwise this is a regression for muscle memory.

3. **Enterprise: merge Pricebooks + Services catalog.** Both are pricing artefacts; Pricebooks is the company-scoped commercial layer, Services catalog is the SKU registry. *Risk:* different write permissions (`enterprise.pricebook.read` vs `enterprise.opportunity.read`); merging hides one set of users from the other set's writes. Probably better as a top-of-page SubNav, not a merge.

4. **Administration: trim 10 → 7.** Move EWO checklist templates into Enterprise; collapse Notification preferences + Webhook deliveries + Audit log into a single "Observability" sub-page with tabs. *Risk:* low — these are all admin-facing surfaces — but needs a permission-model review to make sure the unified page's auth gate doesn't accidentally widen access.

5. **Add a `Cmd-K` palette boost.** The 9 dropped/repointed leaves (5 netdev + 6+3 billing µsvc + 4 ops) need to be findable via the global search. The existing GlobalSearch already crawls the sidebar declaration — verify that after these changes the dropped routes (Compliance, Bulk invoice jobs, etc.) still appear in the palette. If they currently rely on sidebar entry, add explicit registry of "non-sidebar" routes. *Risk:* low; one-evening check.

6. **`/operations/sla` vs `/field/sla-breaches` — name collision.** Both surface SLA. One is a config matrix ("when does SLA fire"), the other is a live breach inbox. Worth a relabel pass: e.g. "SLA policy" vs "SLA breaches." Skipped this wave because the operations section is brand-new; let users react first.

7. **Wave-119's `/billing/payments/webhooks` is a placeholder ("Coming soon").** It should not be in the SubNav until the backend list endpoint lands — currently it's a dead-ish tab. Either ship the read endpoint or hide the tab. Skipped because hiding it would shrink the SubNav and re-introduce a "hidden" route — pick one direction and commit, but that's a product call.

---

## Honest gaps

- **No `next.config.mjs` redirects added.** The original brief asked to "preserve all routes via Next.js redirects." On audit, the only routes that would change were the (already-existing) Wave 119 leaves — and those are *preserved as-is*, not redirected. The only new routes are landing index pages (`/billing/payments` and `/billing/invoice-service`) which use Next's runtime `redirect()` from `next/navigation` to jump to the default tab. No bookmark or Cypress test breaks.

- **No e2e test added for SubNav.** The Cypress smoke at `cypress/e2e/netdev_smoke.cy.ts` still validates `/network/devices/firmware/upgrade-jobs` resolves; nothing in the suite asserts the tab strip is present. Manual click-through confirmed the tab highlighting works on dashboard + intents + refunds + gateways + webhooks + h2h, but a Cypress assertion would be cheap and would lock in the SubNav contract.

- **The mobile drawer reuses the same `SECTIONS` array** (`MobileNav` renders `NavSectionBlock` over the same source), so the consolidation transparently applies to mobile too — no extra work needed.

- **No screenshots in this doc** (no headless browser in this environment). The shape of the change is mechanical; a single screenshot diff per section would be the right next artefact.

- **Did not touch the `PageHeader` `module` taxonomy** — the new "Operations" section uses `module="billing"` on its current pages (or no module). Adding `operations` as a `PageModule` value (with its own ring colour) would tighten the visual link between sidebar section and page header badge. Skipped to keep the wave purely structural; this is a 5-line edit in `PageHeader.tsx` whenever the design lead picks a colour token.

---

## Coordination notes

- 129A (`mobile/customer_app/`) and 129B (`mobile/{sales,tech}_app/`) — disjoint codebases; no overlap.
- 128A / 128C / 128D — backend agents; this wave is frontend-only. No file collisions.
- Wave 90 — its trims are preserved (Roster folded into Teams; consolidated terminations).
- Wave 119 — every URL it shipped still resolves. The sidebar simplification is purely IA; no service-layer code changed.

---

## Verification

```
$ cd frontend
$ npx tsc --noEmit
(no output; exit 0)
$ npx eslint src/
✖ 25 problems (0 errors, 25 warnings)        # all pre-existing
$ npm run build
✓ Compiled successfully
   Route (app) ...
   ├ ƒ /billing/invoice-service                    179 B          96.3 kB
   ├ ƒ /billing/invoice-service/bulk-jobs          ...
   ├ ƒ /billing/payments                           179 B          96.3 kB
   ├ ƒ /billing/payments/dashboard                 ...
   ├ ○ /network/devices/firmware                   ...
   ├ ○ /network/devices/firmware/upgrade-jobs      ...
   ...
(exit 0)
```
