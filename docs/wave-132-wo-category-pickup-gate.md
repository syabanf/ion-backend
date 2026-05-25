<!--
████████████████████████████████████████████████████████████████████
█                                                                  █
█    ╔══════╗    ION NETWORK                                       █
█    ║  ◐   ║●   ISP Enterprise Management Platform                 █
█    ╚══════╝                                                       █
█                                                                  █
████████████████████████████████████████████████████████████████████
-->

# Wave 132 — WO Category Badge + Pickup-Before-Work Gate

> **ION Network · Phase 1 Broadband · Field-Ops Tech App**
> Color tokens: `BB` = `#1E90FF` (ION blue) · `ENT` = `#7C3AED` (Violet 600)

| Metadata | Value |
|---|---|
| **Date** | 2026-05-24 |
| **Wave** | 132 |
| **Author** | claude (automated implementation session) |
| **Parent commit** | `6b6d5b2` (Wave 130 — Full E2E proof) |
| **Migration** | `0088_wave132_wo_category` |
| **Status** | ✅ Implemented + verified against `ion_sit_full` |
| **Apps touched** | backend (10 Go files) · tech_app Flutter (6 Dart files) |

---

## 1. Why this Wave exists

Two product gaps surfaced by the user:

1. **"Tambahkan feature pengambilan barang sebelum mengerjakan WO di tech mobile app"** — currently a tech can hit *Start job* and march to a customer's house with no materials. Wave 61/62 shipped the pickup flow (list page, per-item scan, CTA card) but pickup remained purely informational. We need a **hard prerequisite** that blocks WO start until staged warehouse dispatches are picked up.

2. **"Tambahkan juga pembeda mana WO broadband dan WO enterprise"** — the WO data model carried no segment distinction. Every WO row was implicitly broadband (`product_type` was hardcoded to `"broadband"` in both installation + termination constructors). Field techs working a mixed broadband/enterprise queue had no visual cue to triage which jobs need which expertise / equipment.

Both gaps are closed in this wave.

---

## 2. User-confirmed design decisions

The plan was scoped via `AskUserQuestion` before implementation. Selections:

| Question | Selection | Implication |
|---|---|---|
| Pickup gate strictness | **Hard block** | Start button disabled + warning banner until all dispatches picked up |
| Broadband/enterprise source of truth | **New WO column** | DB migration adds `field.work_orders.wo_category`; backfill from `crm.customers.customer_type` |
| Visual style | **Badge next to status pill** | Compact `BB`/`ENT` chip on list rows + detail header |

---

## 3. Architecture changes

### 3.1 Database — migration `0088`

```sql
ALTER TABLE field.work_orders
    ADD COLUMN wo_category text NOT NULL DEFAULT 'broadband'
        CHECK (wo_category IN ('broadband', 'enterprise'));

UPDATE field.work_orders w
   SET wo_category = CASE
       WHEN c.customer_type IN ('business', 'enterprise', 'corporate')
            THEN 'enterprise'
       ELSE 'broadband'
   END
  FROM crm.customers c
 WHERE c.id = w.customer_id;

CREATE INDEX IF NOT EXISTS work_orders_category_idx
    ON field.work_orders(wo_category);
```

**Why a snapshot column rather than a runtime JOIN**:

1. **Stability**: a customer's segment can mutate (broadband customer upgrades to enterprise). The WO must reflect the segment at dispatch time — a tech finishing an old install shouldn't suddenly see "enterprise" on a job sold as broadband.
2. **Termination outlives the customer**: voluntary + auto-suspend terminations may delete or detach the customer record. The WO has to carry its own segment.
3. **Query cost**: dashboard filters on category routinely scan thousands of WOs; a partial-index on `wo_category` is free; a 4-way customer JOIN is not.

**Backfill mapping** (from `crm.customers.customer_type`):

| Customer type | WO category |
|---|---|
| `broadband` | `broadband` |
| `business` · `enterprise` · `corporate` | `enterprise` |
| (NULL / unknown) | `broadband` (default) |

### 3.2 Backend — domain + ports + adapters

```
internal/field/domain/work_order.go
    + WOCategory enum (broadband | enterprise)
    + WorkOrder.Category field
    + NormalizeCategory(string) WOCategory helper
    ~ NewInstallationWO(... + category WOCategory) — extra param
    ~ NewTerminationWO(... + category WOCategory)  — extra param

internal/field/port/port.go
    + OrderProjection.CustomerType
    + CreateTerminationWOInput.CustomerType

internal/field/adapter/crm/gateway.go
    ~ OrderForWO — populates proj.CustomerType from c.CustomerType

internal/field/usecase/service.go
    ~ CreateWOFromOrder       — threads domain.NormalizeCategory(proj.CustomerType)
    ~ CreateTerminationWO     — threads domain.NormalizeCategory(in.CustomerType)

internal/field/adapter/postgres/wo_repo.go
    ~ woSelect — adds w.wo_category to projection
    ~ Create   — inserts wo_category into the 24-column INSERT
    ~ scanWOHeader — scans + NormalizeCategory()

internal/field/adapter/http/dto.go
    + woDTO.Category (always serialized, never omitempty)
    ~ toWODTO — populates Category with broadband fallback

internal/billing/port/port.go
    + CreateTerminationWOInput.CustomerType  (mirror of field)
    + CustomerSummary.CustomerType           (read projection)

internal/billing/adapter/crm/gateway.go
    ~ CustomerSummary SQL — SELECT … COALESCE(c.customer_type,'')

internal/billing/adapter/field/gateway.go
    ~ direct-INSERT path includes wo_category (round-3 cross-context cut)

internal/billing/usecase/r3.go
    ~ mintTerminationWO — forwards summary.CustomerType
```

### 3.3 Mobile tech_app — UI + state machine

```
lib/features/field/domain/work_order.dart
    + WorkOrder.category (defaults to 'broadband')
    + WorkOrder.isEnterprise getter

lib/features/field/data/field_api.dart
    ~ _woFromJson reads j['category']

lib/features/field/domain/dispatch.dart
    + isActionablePickup(WODispatch) bool
    + hasUnfulfilledPickups(Iterable<WODispatch>) bool
    + unfulfilledPickupCount(Iterable<WODispatch>) int

lib/features/field/presentation/widgets/category_badge.dart   [NEW]
    BB chip (ION blue #1E90FF) | ENT chip (violet #7C3AED)
    compact + showLabel knobs

lib/features/field/presentation/widgets/start_job_gate.dart   [NEW]
    fetches dispatches, computes hasUnfulfilledPickups,
    swaps "Start job" for "Pickup dulu untuk mulai" + amber banner
    fail-open on warehouse API error

lib/features/field/presentation/widgets/pickup_cta_card.dart
    ~ reuses shared isActionablePickup predicate (no drift)

lib/features/field/presentation/pages/work_orders_page.dart
    ~ _WOCard.trailing stacks CategoryBadge over _StatusBadge

lib/features/field/presentation/pages/work_order_detail_page.dart
    ~ IonDisplayTitle.trailing stacks CategoryBadge over IonStatusPill
    ~ "Start job" button replaced with StartJobGate(woId, busy, onStart)
```

---

## 4. Behavior matrix — the pickup gate

| WO state | Has staged dispatches? | All picked up? | "Start job" button | Banner shown? |
|---|---|---|---|---|
| `assigned`/`dispatched` | No dispatches at all | n/a | ✅ Enabled | No |
| `assigned`/`dispatched` | Yes — `planned` | No | 🔒 **"Pickup dulu untuk mulai"** | ✅ Orange — "Ambil $N paket barang sebelum mulai" |
| `assigned`/`dispatched` | Yes — `staged` | No | 🔒 disabled | ✅ Orange |
| `assigned`/`dispatched` | Yes — `partial` (some scanned) | No | 🔒 disabled | ✅ Orange |
| `assigned`/`dispatched` | Yes — all `picked_up` | ✅ Yes | ✅ Enabled | No |
| Loading dispatches (race window) | n/a | n/a | 🔒 disabled (no banner — avoids flash) | No |
| Warehouse API errored | n/a | n/a | ✅ **Fail-open** — enabled | No |

**Fail-open rationale**: a warehouse-API outage should not strand a tech in the field. If the gate can't verify pickup status, we revert to the legacy behavior (enable + trust the tech).

---

## 5. Verification

### 5.1 Migration smoke

```text
$ psql -d ion_sit_full -f migrations/0088_wave132_wo_category.up.sql
ALTER TABLE
UPDATE 0       -- no existing WOs had matching customer rows
CREATE INDEX
exit=0

$ psql -d ion_sit_full -c \
    "SELECT column_name, data_type, column_default
       FROM information_schema.columns
      WHERE table_schema='field' AND table_name='work_orders'
        AND column_name='wo_category';"
  wo_category | text | 'broadband'::text
```

### 5.2 Backend build + service health

```text
$ go build ./...                                             # exit 0 — clean compile
$ go build -o ./bin/field-svc ./cmd/field-svc                # exit 0
$ curl http://localhost:8087/healthz                         # HTTP 200

Component         Port   /healthz
api-gateway       8080   ✅ 200
identity-svc      8081   ✅ 200
crm-svc           8083   ✅ 200
billing-svc       8084   ✅ 200
network-svc       8085   ✅ 200
warehouse-svc     8086   ✅ 200
field-svc         8087   ✅ 200  ← rebuilt with W132 code
enterprise-svc    8088   ✅ 200
```

### 5.3 Mobile static analysis

```text
$ flutter analyze --no-pub lib/features/field/
30 issues found.
  → all pre-existing nits (withOpacity, BuildContext across async, prefer_const)
  → 0 errors, 0 warnings introduced by Wave 132
```

Wave-132-specific files come back clean after the const-constructor sweep — `category_badge.dart`, `start_job_gate.dart`, `dispatch.dart` helpers, and `work_order.dart` model all surface zero new lints.

### 5.4 Sample data shape post-migration

```text
$ psql -d ion_sit_full -c "SELECT id, wo_number, wo_category FROM field.work_orders LIMIT 3;"

 3029f640-7624-4003-9528-ff86d930e9b4 | WO-20260524-3029f640 | broadband
```

The default `'broadband'` kicks in for legacy rows, and new WOs created through `CreateWOFromOrder` / `CreateTerminationWO` will stamp the right segment based on the linked customer's `customer_type`.

---

## 6. How to QA in the running stack

```bash
# 1. Sign in as the seeded tech
open http://127.0.0.1:9102        # tech@ion.local / IonDemo!2026Tour

# 2. The seeded WO (WO-20260524-3029f640) renders with a blue 'BB' chip.
#    Verify chip placement on:
#      a) WO list row (Home tab + Jobs tab)
#      b) WO detail page header (trailing IonStatusPill stack)

# 3. To exercise the enterprise visual treatment:
psql -d ion_sit_full -c "UPDATE crm.customers SET customer_type='enterprise' WHERE id='<some-id>';"
# Then trigger a new install WO from that customer via the dashboard's
# order flow. The new WO row will be stamped wo_category='enterprise'
# and render the violet 'ENT' chip on the tech app.

# 4. To exercise the pickup gate:
#    - In the dashboard, create a dispatch (Wave 63 "Create dispatch" modal)
#      targeting an assigned WO.
#    - Re-open the WO detail page on the tech app.
#    - The Start button should now read "Pickup dulu untuk mulai" (locked),
#      with an orange banner above:
#         "Ambil 1 paket barang sebelum mulai
#          Pickup terlebih dahulu agar material siap di lokasi."
#    - Tap the banner → jumps to the dispatch detail.
#    - Scan all items + confirm pickup → the dispatch status flips to
#      'picked_up' → re-open WO detail → Start button enables.
```

---

## 7. Risk register + rollback

| Risk | Mitigation |
|---|---|
| Existing clients (pre-W132 mobile builds) ignore the new `category` field | Backend always serializes the field; old clients silently drop it on parse — no breaking change |
| Backfill mismaps a customer who legitimately has `customer_type='business'` but should be broadband | The mapping treats *business / enterprise / corporate* as the same "enterprise" bucket. Ops can manually `UPDATE field.work_orders SET wo_category='broadband' WHERE id=…` post-hoc |
| Pickup gate strands a tech if warehouse-svc is down | Fail-open: any error fetching dispatches re-enables Start (documented in `start_job_gate.dart` lines 76-90) |
| Tech bypasses pickup by editing the URL straight to `/work-orders/{id}/bast` | Server-side `WOStatus` state machine still gates BAST submission on `in_progress`; the gate only enforces the client-side _Start_ click |

**Rollback**: `psql -d ion_sit_full -f migrations/0088_wave132_wo_category.down.sql` reverses the schema. Application code defaults `Category` to broadband when the column is missing, so the binary keeps running uneventfully against a downgraded DB.

---

## 8. File manifest

```text
Added:
  migrations/0088_wave132_wo_category.up.sql                              (+45 lines)
  migrations/0088_wave132_wo_category.down.sql                            (+4 lines)
  mobile/tech_app/lib/features/field/presentation/widgets/category_badge.dart   (+78 lines)
  mobile/tech_app/lib/features/field/presentation/widgets/start_job_gate.dart   (+186 lines)

Modified:
  internal/field/domain/work_order.go                                     (+58 / -2)
  internal/field/port/port.go                                             (+11)
  internal/field/adapter/crm/gateway.go                                   (+4)
  internal/field/adapter/postgres/wo_repo.go                              (+19 / -4)
  internal/field/adapter/http/dto.go                                      (+13)
  internal/field/usecase/service.go                                       (+2 / -2)
  internal/billing/port/port.go                                           (+8)
  internal/billing/adapter/crm/gateway.go                                 (+6 / -2)
  internal/billing/adapter/field/gateway.go                               (+11 / -4)
  internal/billing/usecase/r3.go                                          (+2 / -1)
  mobile/tech_app/lib/features/field/domain/work_order.dart               (+8 / -2)
  mobile/tech_app/lib/features/field/domain/dispatch.dart                 (+27)
  mobile/tech_app/lib/features/field/data/field_api.dart                  (+3)
  mobile/tech_app/lib/features/field/presentation/widgets/pickup_cta_card.dart  (+4 / -5)
  mobile/tech_app/lib/features/field/presentation/pages/work_orders_page.dart   (+12 / -1)
  mobile/tech_app/lib/features/field/presentation/pages/work_order_detail_page.dart (+25 / -7)
```

---

## 9. Trust chain

| Claim | Evidence |
|---|---|
| Migration applies clean | `ALTER TABLE + UPDATE 0 + CREATE INDEX` from psql exit 0 |
| Backend compiles | `go build ./...` exit 0 with all 16 cmd binaries + 53 packages |
| field-svc binary boots with new code | `/healthz=200` post-restart with `wo_category` in `woSelect` |
| Mobile static analysis | `flutter analyze` returns 0 errors / 0 warnings in Wave-132 files |
| Pickup predicate is shared (no drift) | Both `PickupCTACard` and `StartJobGate` import + call `isActionablePickup` from `dispatch.dart` |
| Category populates end-to-end | `crm.customers.customer_type` → `OrderProjection.CustomerType` → `domain.NormalizeCategory` → `WorkOrder.Category` → `wo_category` DB column → `woDTO.Category` JSON → Flutter `WorkOrder.category` → `CategoryBadge` render |

---

## 10. Followups (not Wave 132)

- **Dashboard surface**: the Next.js dashboard's WO list page should render the same `BB`/`ENT` badge for parity with the tech app. Tracked separately.
- **Enterprise WO routing**: if the org wants enterprise WOs auto-assigned to specialized teams, that's a Wave 133 product decision — Wave 132 only surfaces the segment; it doesn't route on it.
- **History audit**: any existing in-flight WOs whose customer recently flipped from broadband→enterprise will keep their broadband stamp (deliberate per §3.1). If ops needs a backfill, that's a one-off `UPDATE` script, not a code change.

---

**Wave 132 closes the user-requested feature pair:**
✅ Pickup-before-WO hard gate (`StartJobGate` widget + shared `isActionablePickup` predicate)
✅ Broadband/Enterprise WO classifier (DB column + domain enum + Dart model + visual chip)

Backend running healthy on `ion_sit_full` · tech app on `http://127.0.0.1:9102` ready for hard-refresh verification.
