# Wave 121E — Phase 1B Broadband Stub-Mode → Production-Mode Readiness

**Date:** 2026-05-23
**Status:** Phase 1B closeout — closes SIT gap #5 (real third-party flows entirely unverified).
**Audience:** Ops + SRE preparing to flip the stub-mode binary into production-mode against real DJP / Xendit / BCA / Midtrans / Stripe / SNMP / FCM / WhatsApp endpoints.

---

## 1. Headline

The Phase 1B Broadband binary today ships **eight third-party integrations as stubs**: every adapter satisfies its hexagonal port contract without making real network calls. Stub mode is the default in dev / CI / smoke databases because real credentials don't exist yet.

**What stub mode DOES validate:** contract shape, idempotency, signature dedup, error taxonomy mapping, downstream parser robustness, audit-log persistence, schema-driven retry windows. See §4.

**What stub mode does NOT validate:** real timeout / 5xx behaviour, real signature crypto, real schema drift, real upstream rate-limits. See §5.

**Wave 121E ships only tests + this doc.** No business `.go` files were modified. The deliverable is:

- 12 new `*_stub_determinism_test.go` files (one per stub adapter)
- 5 new `*_env_flag_test.go` files (one per env-toggled adapter)
- 1 new `test/e2e/cron_observability_smoke_test.go` (Wave 114 cron boot smoke)
- This readiness doc

The flip from "stub" to "production" is a **configuration change, not a code change** — each row in §2 lists the exact env vars that need to be set. Two integrations (HRIS, device-mgmt) currently have a **quietly-broken env flag** — flagged in §6 with remediation.

---

## 2. Inventory — stub-backed adapters

| # | Component | Env flag | Required env vars / secrets | Endpoints expected from real service | Test fixtures needed | Effort |
|---|-----------|----------|----------------------------|-------------------------------------|---------------------|:------:|
| 1 | **DJP e-Faktur** (Indonesian tax) | `DJP_ENABLED=true` | `DJP_BASE_URL`, `DJP_API_KEY`, `DJP_NPWP_CERTIFICATE_PATH` (future), `DJP_TIMEOUT_SECONDS` (default 30s) | `POST /api/faktur/issue`, `GET /api/faktur/{nomor_seri}/status` | Test NPWP (`01.234.567.8-901.000`), test subsidiary id, test invoice row | **L** — real cert chain + signing required |
| 2 | **Xendit payment** (aggregator) | `XENDIT_ENABLED=true` | `PAYMENT_XENDIT_SECRET`, `XENDIT_BASE_URL`, `XENDIT_API_KEY`, `XENDIT_CALLBACK_TOKEN` | `POST /v2/invoices`, `POST /payments/{id}/refund`, `GET /v2/invoices/{id}` | Test merchant id, test customer email, sandbox VA prefix | **M** |
| 3 | **BCA Host-to-Host** (corporate banking) | `BCA_H2H_ENABLED=true` | `BCA_SFTP_HOST`, `BCA_SFTP_USER`, `BCA_SFTP_PRIVATE_KEY_PATH`, `BCA_CORPORATE_ACCOUNT_NO` | SFTP drop directory, daily CSV / MT940 statement file | Test corporate account, sample MT940 fixture | **M** — SFTP cert + daily CSV format |
| 4 | **Midtrans payment** (aggregator) | `MIDTRANS_ENABLED=true` | `PAYMENT_MIDTRANS_SECRET`, `MIDTRANS_SERVER_KEY`, `MIDTRANS_CLIENT_KEY`, `MIDTRANS_BASE_URL` | `POST /charge`, `GET /status`, webhook notification handler | Test merchant id, sandbox `signature_key` | **M** |
| 5 | **Stripe payment** (international) | `STRIPE_ENABLED=true` (gateway row `is_active=FALSE` until flipped) | `STRIPE_API_KEY`, `STRIPE_WEBHOOK_SECRET` | `POST /v1/payment_intents`, `POST /v1/refunds`, webhook signature verify | International test customer; Indonesian rupiah currency mapping | **L** — international expansion gate |
| 6 | **NOC probes** (ICMP / iperf3 / SNMP) | `NOC_PROBES_ENABLED=true` | `NOC_ICMP_ALLOW_RAW_SOCKETS=true` (capability), `NOC_IPERF3_BIN_PATH`, `NOC_SNMP_COMMUNITY` | Direct device IPs / SNMP OIDs (no HTTP) | Test device IP, test OLT mac → port mapping | **L** — raw sockets + per-vendor SNMP OIDs |
| 7 | **Device management** (Mikrotik / Huawei OLT) | `DEVICE_MGMT_ENABLED=true` | `DEVICE_MGMT_VENDOR_SDK_PATH`, `DEVICE_MGMT_CREDENTIALS_VAULT_KEY` | Vendor SDK (NETCONF / proprietary REST), TFTP / SCP for firmware push | Test ONT serial, test firmware tarball | **L** — multi-vendor abstraction |
| 8 | **HRIS gateway** (employee sync) | `HRIS_GATEWAY_ENABLED=true` | `HRIS_BASE_URL`, `HRIS_API_TOKEN`, `HRIS_POLL_INTERVAL_SECONDS` | REST poll endpoint OR webhook receiver | Test employee numbers `EMP00001`–`EMP00003` (match migration seed) | **M** |
| 9 | **Push notifications** (FCM / APNS) | `NOTIFYX_PROVIDER=fcm` (or `apns`) | `FCM_SERVICE_ACCOUNT_JSON_PATH`, `APNS_AUTH_KEY_PATH`, `APNS_KEY_ID`, `APNS_TEAM_ID` | Firebase / Apple endpoints | Test device tokens registered in `platform.device_tokens` | **M** |
| 10 | **WhatsApp Business API** (reminders + OTP) | `NOTIFYX_PROVIDER=whatsapp` | `WHATSAPP_PHONE_NUMBER_ID`, `WHATSAPP_BUSINESS_TOKEN`, `WHATSAPP_TEMPLATE_NAMESPACE` | WhatsApp Cloud API endpoints | Approved message templates (utility + marketing) | **L** — template approval cycle |
| 11 | **Partnership PDF generation** | (no flag; swap constructor) | `PARTNERSHIP_PDF_ENGINE=gofpdf` (or `chromium-headless`), `PARTNERSHIP_PDF_FONT_PATH` | None (in-process) | Test settlement row, test agreement row | **S** — pure code swap |
| 12 | **Partnership evidence store** | (no flag; swap constructor) | `EVIDENCE_S3_BUCKET`, `EVIDENCE_S3_REGION`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `EVIDENCE_S3_KMS_KEY_ID` | AWS S3 endpoints | Test bucket (lifecycle: 7-year retention) | **S** |

**Effort key:** S = 1–3 days, M = 1 week, L = 2+ weeks.

---

## 3. Per-gateway integration guide

### 3.1 DJP e-Faktur (`internal/tax/adapter/djp/`)

**Files**: `client.go` (real HTTP client, already shipped behind `DJP_ENABLED`), `stub.go` (fallback).

**To wire production**:
1. Obtain DJP NPWP digital certificate from the operator's PT tax office.
2. Set in environment:
   ```
   DJP_ENABLED=true
   DJP_BASE_URL=https://api.pajak.go.id
   DJP_API_KEY=<bearer-from-DJP-onboarding-pack>
   DJP_NPWP_CERTIFICATE_PATH=/etc/ion/djp.p12
   ```
3. Restart `enterprise-svc`. Boot log should show `DJP gateway mode=real` (line 99 of `client.go`).
4. Smoke test: issue a test faktur against DJP staging — `nomor_seri` should be non-empty.
5. Watch `enterprise.faktur_pajak` table for `status='submitted'` rows.

**Known gaps**:
- Wave 101 ships `Authorization: Bearer` only. DJP may require XML signing — verify against DJP onboarding pack.
- No retry-on-503 logic; relies on `DJP_TIMEOUT_SECONDS`.

### 3.2 Xendit (`internal/payment/adapter/gateway/xendit_stub.go`)

**To wire production**:
1. Sign up at Xendit production dashboard, obtain `XENDIT_API_KEY` + `XENDIT_CALLBACK_TOKEN`.
2. Set `XENDIT_ENABLED=true` + the four secrets in §2.
3. **Code change required**: ship `xendit_client.go` alongside `xendit_stub.go`, with `NewXenditClient(cfg)` returning a real REST client. Update `registry.go::NewStubRegistry` to read the env flag and choose stub vs. client.
4. Update `payment_gateways` table: set `xendit.is_active=true`.
5. Smoke test: create an intent via `POST /api/payment/intents`, follow the returned `payment_url`, confirm webhook arrives at `/api/payment/webhooks/xendit`.

**Production tuning**:
- Xendit production uses **static `X-Callback-Token`** in headers, not HMAC. Stub uses HMAC for forward-compat — the real client must support both via a `XenditCallbackMode` config.

### 3.3 BCA Host-to-Host (`internal/payment/adapter/gateway/bca_h2h_stub.go`)

**To wire production**:
1. Provision SFTP user + RSA key with BCA corporate banking team.
2. Set the four `BCA_*` env vars in §2.
3. **Code change required**: implement `bca_sftp_client.go` (SFTP poller goroutine writing into `payment.h2h_bank_statements`). The stub's `ParseH2HStatement` CSV parser is forward-compat with the real MT940 format — wrap it in a `bca_mt940_parser.go`.
4. Schedule the poller to run daily at 08:00 WIB (after BCA's daily-batch cutoff).
5. Smoke test: drop a test MT940 file in the SFTP dir, verify `payment.h2h_bank_lines` get inserted + matched against open invoices.

### 3.4 Midtrans (`internal/payment/adapter/gateway/midtrans_stub.go`)

Same shape as Xendit. The stub uses HMAC-SHA256; Midtrans production uses **SHA-512** with a `signature_key`. Real client must:
- Compute `signature_key = sha512(order_id + status_code + gross_amount + server_key)`.
- Match against the inbound `X-Midtrans-Signature` header.

### 3.5 NOC probes (`internal/nocmon/adapter/probes/runners.go`)

**To wire production**:
1. Build the binary with raw-socket capability:
   ```
   sudo setcap cap_net_raw+ep ./nocmon-svc
   ```
2. Install iperf3 server on each region's NOC bridge.
3. Set `NOC_PROBES_ENABLED=true` + the three `NOC_*` env vars in §2.
4. **Code change required**: replace `RTTStub` with `*RealICMPRunner`, etc. The file's package doc calls this a "one-file delete" — `DefaultRunners()` becomes env-conditional.
5. Smoke test: deactivate the stub seed, watch `nocmon.service_health_samples` for real values within the documented ranges (5–200 ms RTT, 0–5% loss).

### 3.6 Device management (`internal/netdevices/adapter/mgmt/stub_client.go`)

**To wire production**:
1. Build the vendor abstraction. Most ION deployments use **Mikrotik (RouterOS API)** and **Huawei OLT (SNMP + GUI/CLI)** — different SDKs.
2. Set `DEVICE_MGMT_ENABLED=true` + vendor-specific creds.
3. **Code change required (currently a no-op flag — see §6)**: implement a `RealClient` that satisfies `port.DeviceMgmtClient` with `if env.Vendor == "mikrotik" { ... }` branches.
4. Wire via `cmd/netdevices-svc/main.go` (today line 87 logs a warning and falls back to stub).

### 3.7 HRIS gateway (`internal/hris/adapter/gateway/stub.go`)

**To wire production**:
1. Confirm the HRIS counterparty's wire format (REST? CSV poll? webhook receiver?).
2. Set `HRIS_GATEWAY_ENABLED=true` + `HRIS_BASE_URL` + `HRIS_API_TOKEN`.
3. **Code change required (currently a no-op flag — see §6)**: implement `RESTGateway` satisfying `port.HRISGateway`. Today `cmd/hris-svc/main.go` line 67 logs and silently falls back.
4. Verify daily poll cron picks up real `EmployeeEvent`s — the resign event should auto-trigger commission cessation via the Wave 118 bridge.

### 3.8 Push notifications (`pkg/notifyx/`)

**FCM (Android)**:
1. Create a Firebase project, download `service-account.json`.
2. Set `FCM_SERVICE_ACCOUNT_JSON_PATH=/etc/ion/fcm-sa.json`.
3. **Code change required**: implement `FCMPush` satisfying `notifyx.Push`. Wire via `dispatcher.WithProvider(fcmpush.New(...))`.

**WhatsApp Business API**:
1. Onboard the operator's phone number through WhatsApp Cloud API.
2. Get message templates approved (utility templates clear in 24h, marketing in 1–7 days).
3. Set the three `WHATSAPP_*` env vars in §2.
4. Implement `WhatsAppPush` — the OTP flow is the highest-leverage template to ship first.

### 3.9 Partnership PDF + evidence store (`internal/partnership/adapter/postgres/stubs.go`)

Both are **constructor swaps**, not env flags:

- **PDF**: replace `NewStubSettlementPDFGenerator()` with a `gofpdf` / `chromium-headless` impl. The `port.SettlementPDFGenerator` contract stays the same.
- **Evidence**: replace `NewLocalEvidenceStore(dir)` with `NewS3EvidenceStore(bucket, region, kmsKey)`. The `port.EvidenceStore` contract stays the same.

Both swaps are S-effort (1–3 days). The hash + URL shape pins (in `stubs_determinism_test.go`) protect against drift.

---

## 4. What stub-mode DOES validate today

The 12 `*_stub_determinism_test.go` files pin the following contract guarantees:

1. **Deterministic outputs** — same input → same response across N calls. Catches accidental `time.Now()` / `uuid.New()` leakage in the response path.
2. **Idempotent re-call semantics** — calling `CheckStatus` / `FetchEmployees` / `ParseWebhook` twice returns the same shape (no hidden in-stub state).
3. **Typed error contracts** — every error path returns `*pkg/errors.Error` with stable `Kind` + `Code` so the HTTP layer maps to a stable status code and the FE can react.
4. **Filename sanitization** — `LocalEvidenceStore` prevents `../../etc/passwd`-style path traversal.
5. **Hash determinism** — `sha256(content)` is the contract for both evidence + PDF stubs.
6. **Webhook envelope parsing** — Xendit + Midtrans webhook payloads round-trip through `ParseWebhook` without panic on any documented status string.
7. **HMAC constant-time compare** — `VerifySignature` exercises the same `hmac.Equal` path the production adapter will use.
8. **Stable seed correspondence** — HRIS stub's `EmployeeEvent.ID` is fixed (`...e003`) so re-poll idempotency works against the DB's UNIQUE constraint.
9. **Per-minute determinism (NOC)** — same probe id + same minute → same value, which exercises the cron's anti-flap logic.
10. **No outbound network** — DJP stub mode has a tripwire that fails the test if any HTTP request fires.
11. **Toggle observability** — `DJP_ENABLED=true` without `DJP_BASE_URL` surfaces a typed `*derrors.Error` rather than panicking or silently falling back.
12. **Boot wiring** — Wave 114 cron's 5 evaluators register on the cron `Runner` without panicking; each `Run*Tick` executes against a real Postgres schema (`ion_p1b_smoke`).

---

## 5. What stub-mode does NOT validate

These gaps are the load-bearing reason to flip individual integrations into production-mode for SIT testing:

1. **Real upstream timeout behaviour** — DJP can hang at TCP connect for 60s; Xendit's REST API has its own SLA. Stubs return immediately.
2. **Real 5xx + retry-on-failure** — production has to handle DJP's documented "rate-limited, retry in 10s" header. Stubs always succeed (or always fail with a stable code).
3. **Real signature crypto** — Xendit's static `X-Callback-Token` vs. our HMAC stub; Midtrans's SHA-512 vs. our SHA-256 stub.
4. **Schema drift** — DJP can add fields to its response without warning. Stubs unmarshal a fixed shape; the production parser may need a "tolerate unknown fields" mode.
5. **Real rate-limits** — Xendit production accepts 100 req/s; the stub has no limit. Bulk-create flows may need batching.
6. **Real webhook retry / out-of-order delivery** — Xendit retries failed webhooks up to 5 times over 24h. The `payment_webhooks` table dedup is exercised in stubs (idempotency on `(gateway_id, external_event_id)`), but the real timing distribution differs.
7. **Real SFTP credentials rotation** — BCA rotates the SFTP key on a 90-day cycle. Stub uses a static CSV.
8. **Real CSV → MT940 format change** — BCA can switch from CSV to MT940 mid-quarter. The stub's parser only handles CSV.
9. **Real SNMP OID schema** — vendor SDKs change OIDs between firmware versions. The NOC stubs return random values in a fixed range.
10. **Real device firmware push transport** — Mikrotik uses HTTPS, Huawei OLTs typically use TFTP. The stub no-ops.
11. **Real FCM rate-limits per-token** — FCM caps at ~600 msg/s/project. Stub has no cap.
12. **Real WhatsApp template versioning** — once a template is approved, mutations require re-approval. Stub bypasses.
13. **Real S3 multipart upload + retry** — large evidence blobs (>5MB) need multipart. LocalEvidenceStore does a single `os.WriteFile`.
14. **Real PDF binary format** — PDF/A compliance, embedded fonts, page-break logic. Stub returns plain text.
15. **Cross-context transactional semantics** — e.g., did the Xendit refund actually settle, and is the `payment_intent.status` flip in the same DB tx? Stub bypasses real-world race windows.

---

## 6. Findings — env flags that don't actually toggle anything

Two adapters today have **quietly-broken env flags** — flipping them at runtime produces a log message and no behavior change. These are real bugs to flag for ops:

### 6.1 `HRIS_GATEWAY_ENABLED` (cmd/hris-svc/main.go, line 67)

Current behaviour:
```go
var gateway hrisport.HRISGateway = hrisgateway.NewStubGateway()
if os.Getenv("HRIS_GATEWAY_ENABLED") == "true" {
    log.Info("HRIS_GATEWAY_ENABLED=true — production gateway not yet wired; falling back to stub")
}
```

The flag is read, an info log is emitted, then the stub is used anyway. An operator who flips this expecting a real HRIS poll will get zero behavior change.

**Recommended remediation:**
- If no real gateway exists yet, **error on flag** (refuse to boot) so misconfiguration is loud, OR
- Land the real `RESTGateway` impl and wire the conditional properly.

### 6.2 `DEVICE_MGMT_ENABLED` (cmd/netdevices-svc/main.go, line 87)

Same shape as 6.1 — flag is read, warning is logged, stub is used. The log uses `Warn` (slightly louder than HRIS's `Info`) but the behaviour is identical: a no-op flag.

**Recommended remediation:** same as 6.1.

### 6.3 `NOC_PROBES_ENABLED` (internal/nocmon/adapter/probes/runners.go, function `EnabledFromEnv`)

This one is half-wired: the parser exists, but `DefaultRunners()` always returns stubs. Comment on line 30 says "today every runner is a stub regardless". Less of a footgun than 6.1/6.2 because operators expect "TODO" in the package doc, but worth tracking.

---

## 7. Acceptance gates (run from `backend/`)

```bash
# Unit + adapter tests (stub determinism + env-flag toggle)
go build ./...
go vet ./...
go test -count=1 \
  ./internal/tax/... \
  ./internal/payment/... \
  ./internal/nocmon/... \
  ./internal/netdevices/... \
  ./internal/hris/... \
  ./internal/partnership/... \
  ./pkg/notifyx/...

# Cron observability boot smoke (requires ion_p1b_smoke reachable)
DATABASE_URL="postgres://syabanf@localhost:5432/ion_p1b_smoke?sslmode=disable" \
JWT_SECRET="01234567890123456789012345678901test_jwt_secret_for_local_smoke_only" \
JWT_ISSUER="ion-sit" \
go test -tags=e2e -count=1 ./test/e2e/cron_observability_smoke_test.go
```

If `ion_p1b_smoke` is not reachable, the e2e test calls `t.Skip` with an explanatory message — it does not fail.

---

## 8. Coordination with other Wave 121 sub-waves

- **Wave 121C** (backend e2e happy-paths): runs in parallel, touches `test/e2e/billing_orchestration_cron_observability_e2e_test.go` (distinct filename — no overlap).
- **Wave 121D** (Cypress frontend): entirely frontend — no overlap.
- **Wave 121E** (this one): `*_stub_determinism_test.go`, `*_env_flag_test.go`, `test/e2e/cron_observability_smoke_test.go`, this doc.

---

## 9. Next steps after Wave 121E

In rough order of operator-effort × leverage:

1. **Fix the no-op env flags** (§6) — either error on the flag or land the real adapter. ~½ day.
2. **Wire FCM push** (§3.8) — highest user-visible win for ~1 week. Unblocks every billing reminder + WO assignment notification.
3. **Wire WhatsApp OTP template** (§3.8) — kills the dev-OTP affordance (`PORTAL_DEV_OTP=true`) in production. ~1 week + template approval cycle.
4. **Wire Xendit production REST client** (§3.2) — required for any real revenue collection. ~1 week.
5. **Wire DJP real client end-to-end against staging** (§3.1) — Wave 101 shipped the scaffolding; the cert chain is the remaining unknown. ~2 weeks.
6. **Wire BCA H2H SFTP poller** (§3.3) — needs corporate banking onboarding. ~2 weeks.
7. **Wire NOC real probes** (§3.5) — needs raw-socket capability + iperf3 deployment per region. ~2 weeks.
8. **Wire device-mgmt vendor abstractions** (§3.6) — Mikrotik first (most-deployed), Huawei second. ~3 weeks total.
9. **Wire HRIS REST poll** (§3.7) — depends on the HRIS counterparty's API readiness.
10. **Swap partnership PDF + evidence store** (§3.9) — S-effort, defer until S3 bucket policy clears compliance review.

---

**End of Wave 121E readiness doc.**
