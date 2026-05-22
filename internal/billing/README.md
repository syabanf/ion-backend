# internal/billing — Billing & Finance bounded context

**Status:** Skeleton (M6 — depends on M1–M5)

Phase 1 owns:
- OTC invoicing (free/prepaid/postpaid — schema-driven)
- Faktur Pajak (DJP e-Faktur compliant)
- Xendit payment gateway integration
- Payment gate enforcement (NOC isn't notified until all invoices are paid)
- Recurring billing (anniversary model for broadband)
- Reminder execution (email + SMS + WhatsApp via Meta-approved templates)
- Late fees, broadband auto-suspension + restoration
- Auto-termination trigger (per Suspension Schema `termination_trigger` block)
- Commission calculation (5-party splits including cross-branch infra)
- Referral reward calculation + disbursement record
- Voluntary termination (lock-in penalty check, final invoice)

This is the highest-integration-density context. Don't start until M1–M5 are done.
