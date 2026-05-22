# internal/field — Technician & Field bounded context

**Status:** Skeleton (M5 — depends on M1, M3, HRIS)

Phase 1 owns:
- WO routing (address → Sub Area → Team Leader, with escalation)
- WO assignment SLA + auto-pair on breach (Senior + Junior)
- Team Leader dashboard (web + mobile) + cross-area overflow
- HRIS read integration (≤15min sync) for technician availability
- Technical App (mobile) — WO list, QR scan, navigation, dynamic proof-of-work, resolution log, sign-off (on-site or OTP), BAST submit
- WO status lifecycle: Created → Unassigned → Assigned → Dispatched → In Progress → Pending NOC Verification → Completed
- BAST immutability (rejected BAST creates a new row)
- Reschedule flows (customer- and technician-initiated)

Copy the structure from `internal/identity/` when starting M5.
