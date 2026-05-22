# internal/warehouse — Warehouse & Asset bounded context

**Status:** Skeleton (M3 — depends on M1, M2)

Phase 1 owns:
- 4 item types: serialized devices, cable (length), consumables (qty), network infra equipment
- Stock intake with `received_at` + `purchase_cost`
- FIFO/LIFO dispatch (per `platform_config.inventory_valuation_method`)
- WO dispatch flow (device QR + technician ID QR)
- Consumption recording (post-installation actuals)
- Device return flow (Option A: warehouse, Option B: re-use on next WO)
- Per-item per-warehouse thresholds + parent-branch escalation
- Stock opname (schedule + execution + discrepancy report + cable remnant decision)
- Inter-warehouse transfer

Copy the structure from `internal/identity/` when starting M3.
