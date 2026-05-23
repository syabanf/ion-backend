// Wave 116 — Deep schema content validators.
//
// The generic schema engine (NewSchema / Publish / ResolveForCustomer) is
// content-agnostic — it stores body as JSONB and merges patches without
// peeking inside. That's correct at the engine level, but downstream code
// (billing tick, commission calc, onboarding wizard) has historically had
// to reach into `map[string]any` to fish out fields.
//
// This file ships a typed validator per content kind:
//
//	- OnboardingValidator  — required docs, OCR confidence floor, auto-approve gate
//	- BillingValidator     — cycle day window, currency, tax rules, prorate consistency
//	- ServiceValidator     — plan speed sanity, SLA tier, monotonic degradation thresholds
//	- CommissionValidator  — clawback window, split-percentage sum, ramp duration, flat-vs-pct
//	- SuspensionValidator  — warn → soft → hard monotonicity, channel allow-list, RADIUS SLA
//
// Each validator parses the jsonb body once, runs a set of cross-field
// business rules, and returns (errors, warnings). The ValidatorRegistry
// dispatches by string kind so the four new kinds in
// platform.schema_kinds (onboarding/reminder/late_fee/addon) can register
// without touching the platform.schema_kind ENUM.
//
// The publish gate uses errors-only: warnings are advisory and never
// block. Callers that want strict mode should check len(warnings) == 0
// themselves.
//
// Compatibility with Wave 114's billing domain policy structs
// (ReminderPolicy, LateFeePolicy, SuspensionPolicy,
// CommissionTriggerPolicy) is preserved: the validators here describe a
// SUPERSET of the fields those structs read, so any schema that passes
// validation will deserialize cleanly into the Wave 114 policy structs.
// See the per-validator NOTE comments for specific field mappings.

package domain

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// =====================================================================
// SchemaContentValidator — the interface every per-kind validator
// implements. Kept tiny on purpose — the registry just needs Kind() to
// dispatch and Validate() to run.
// =====================================================================

// SchemaContentValidator is the contract for typed validators. Errors
// block publish + flag is_valid=false; Warnings are advisory.
type SchemaContentValidator interface {
	// Kind returns the string kind this validator handles (matches
	// platform.schema_kinds.kind). Lowercase, snake_case.
	Kind() string

	// Validate parses content and returns (errors, warnings). Errors are
	// rule violations that block publish; warnings are advisory and only
	// surface in audit / admin UI.
	//
	// A nil error slice + non-nil warnings slice is a valid result.
	// Empty slices both ways = schema is fully clean.
	Validate(content json.RawMessage) (errors []string, warnings []string)
}

// =====================================================================
// ValidatorRegistry — kind → validator dispatch table.
// =====================================================================

// ValidatorRegistry holds the runtime mapping from kind to validator.
// Constructed once at service startup; thread-safe for reads (the
// registry is immutable after wiring).
type ValidatorRegistry struct {
	byKind map[string]SchemaContentValidator
}

// NewValidatorRegistry returns a registry pre-populated with the five
// Wave 116 validators (onboarding, billing, service, commission,
// suspension). Callers may register additional validators via Register
// before the registry is handed to the use-case service.
func NewValidatorRegistry() *ValidatorRegistry {
	r := &ValidatorRegistry{byKind: make(map[string]SchemaContentValidator)}
	r.Register(&OnboardingValidator{})
	r.Register(&BillingValidator{})
	r.Register(&ServiceValidator{})
	r.Register(&CommissionValidator{})
	r.Register(&SuspensionValidator{})
	return r
}

// Register adds a validator, replacing any prior registration for the
// same kind. The registry is not thread-safe for writes — call only
// during wiring.
func (r *ValidatorRegistry) Register(v SchemaContentValidator) {
	if v == nil {
		return
	}
	r.byKind[strings.ToLower(v.Kind())] = v
}

// For returns the validator for kind, or nil if none registered.
// Callers should treat nil as "no validation configured" — emit an
// info-level audit row but do not block publish.
func (r *ValidatorRegistry) For(kind string) SchemaContentValidator {
	if r == nil {
		return nil
	}
	return r.byKind[strings.ToLower(kind)]
}

// Kinds returns the sorted list of registered kinds. Useful for the
// nightly sweep and admin "what's installed?" surfaces.
func (r *ValidatorRegistry) Kinds() []string {
	if r == nil {
		return nil
	}
	out := make([]string, 0, len(r.byKind))
	for k := range r.byKind {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ValidationResult is the projection returned to the use-case caller.
// Mirrors the platform.schema_validation_results columns 1:1 so the
// repository write is a direct mapping.
type ValidationResult struct {
	IsValid          bool     `json:"is_valid"`
	Errors           []string `json:"errors"`
	Warnings         []string `json:"warnings"`
	ValidatorVersion string   `json:"validator_version"`
}

// Run dispatches to the validator for kind and packages the result.
// Returns a result with IsValid=true and empty slices when no validator
// is registered — schemas of unknown kinds aren't blocked from publish.
func (r *ValidatorRegistry) Run(kind string, content json.RawMessage) ValidationResult {
	v := r.For(kind)
	if v == nil {
		return ValidationResult{IsValid: true, ValidatorVersion: "v1.0"}
	}
	errs, warns := v.Validate(content)
	if errs == nil {
		errs = []string{}
	}
	if warns == nil {
		warns = []string{}
	}
	return ValidationResult{
		IsValid:          len(errs) == 0,
		Errors:           errs,
		Warnings:         warns,
		ValidatorVersion: "v1.0",
	}
}

// =====================================================================
// OnboardingValidator (kind="onboarding")
//
// Source: TC-SOB-001..018. Validates the onboarding wizard content used
// by lead conversion + sales mobile to know which docs to require and
// how strict the OCR auto-approve gate should be.
//
// NOTE on cross-wave compatibility: no Wave-114 policy struct reads
// onboarding schema content, so the only consumer is internal/crm's
// onboarding wizard.
// =====================================================================

type OnboardingValidator struct{}

func (OnboardingValidator) Kind() string { return "onboarding" }

// onboardingBody is the typed projection. Fields use pointers where the
// "missing vs zero" distinction matters for validation.
type onboardingBody struct {
	RequiredDocuments []struct {
		Code            string   `json:"code"`
		Label           string   `json:"label"`
		AllowedFormats  []string `json:"allowed_formats"`
		MaxSizeMB       *int     `json:"max_size_mb"`
	} `json:"required_documents"`
	MinOCRConfidence   *float64 `json:"min_ocr_confidence"`
	MaxDocSizeMB       *int     `json:"max_doc_size_mb"`
	SurveyQuestions    []struct {
		Code  string `json:"code"`
		Label string `json:"label"`
		Type  string `json:"type"`
	} `json:"survey_questions"`
	AutoApproveThresholds *struct {
		Enabled       bool     `json:"enabled"`
		MinConfidence *float64 `json:"min_confidence"`
	} `json:"auto_approve_thresholds"`
	TimelineSLAHours *int `json:"timeline_sla_hours"`
}

// allowedDocFormats is the closed set of file types the sales mobile +
// portal will accept for KTP/KK/NPWP uploads.
var allowedDocFormats = map[string]struct{}{
	"jpeg": {}, "jpg": {}, "png": {}, "pdf": {},
}

func (OnboardingValidator) Validate(content json.RawMessage) ([]string, []string) {
	var errs, warns []string
	var b onboardingBody
	if err := json.Unmarshal(content, &b); err != nil {
		return []string{fmt.Sprintf("onboarding.parse_failed: %v", err)}, nil
	}

	// Required fields.
	if b.MinOCRConfidence == nil {
		errs = append(errs, "onboarding.min_ocr_confidence_required")
	} else if *b.MinOCRConfidence < 0.50 || *b.MinOCRConfidence > 1.00 {
		errs = append(errs, fmt.Sprintf(
			"onboarding.min_ocr_confidence_out_of_range: got %.2f, want [0.50, 1.00]",
			*b.MinOCRConfidence,
		))
	}
	if b.MaxDocSizeMB == nil {
		errs = append(errs, "onboarding.max_doc_size_mb_required")
	} else if *b.MaxDocSizeMB < 1 || *b.MaxDocSizeMB > 50 {
		errs = append(errs, fmt.Sprintf(
			"onboarding.max_doc_size_mb_out_of_range: got %d, want [1, 50]",
			*b.MaxDocSizeMB,
		))
	}

	// At least one required document.
	if len(b.RequiredDocuments) == 0 {
		errs = append(errs, "onboarding.required_documents_empty: at least one document must be required")
	}

	// Per-document format allow-list. TC-SOB-001 (.exe → 422) lives here.
	for i, d := range b.RequiredDocuments {
		if strings.TrimSpace(d.Code) == "" {
			errs = append(errs, fmt.Sprintf("onboarding.required_documents[%d].code_required", i))
		}
		if len(d.AllowedFormats) == 0 {
			errs = append(errs, fmt.Sprintf("onboarding.required_documents[%d].allowed_formats_empty", i))
		}
		for _, f := range d.AllowedFormats {
			fl := strings.ToLower(strings.TrimSpace(f))
			if _, ok := allowedDocFormats[fl]; !ok {
				errs = append(errs, fmt.Sprintf(
					"onboarding.required_documents[%d].format_disallowed: %q (allow only jpeg/png/pdf)",
					i, f,
				))
			}
		}
	}

	// Cross-field: auto-approve confidence must be ≥ min OCR confidence.
	if b.AutoApproveThresholds != nil && b.AutoApproveThresholds.MinConfidence != nil &&
		b.MinOCRConfidence != nil {
		if *b.AutoApproveThresholds.MinConfidence < *b.MinOCRConfidence {
			errs = append(errs, fmt.Sprintf(
				"onboarding.auto_approve_threshold_too_low: min_confidence %.2f < min_ocr_confidence %.2f",
				*b.AutoApproveThresholds.MinConfidence, *b.MinOCRConfidence,
			))
		}
	}

	// Warning: auto-approve enabled w/o OCR-side config implies OCR module
	// must be wired downstream (TC-SOB-011/012).
	if b.AutoApproveThresholds != nil && b.AutoApproveThresholds.Enabled {
		if b.MinOCRConfidence == nil || *b.MinOCRConfidence < 0.80 {
			warns = append(warns, "onboarding.auto_approve_enabled_without_strong_ocr: OCR module must be configured downstream")
		}
	}

	if b.TimelineSLAHours != nil && *b.TimelineSLAHours <= 0 {
		errs = append(errs, "onboarding.timeline_sla_hours_must_be_positive")
	}

	return errs, warns
}

// =====================================================================
// BillingValidator (kind="billing")
//
// Source: TC-SBE-001..006. Validates the billing cycle config consumed
// by internal/billing/usecase/r3.go (cycle continuation) and the
// invoice cron.
//
// Wave 114 NOTE: ReminderPolicy + LateFeePolicy live under SEPARATE
// schema kinds in the registry (reminder / late_fee), so the billing
// validator does NOT enforce reminder fields here — Wave 114's
// schema_policy.go reads them from a different schema body. Drift risk:
// none, because the field namespaces don't overlap.
// =====================================================================

type BillingValidator struct{}

func (BillingValidator) Kind() string { return "billing" }

type billingBody struct {
	CycleDay         *int     `json:"cycle_day"`
	Currency         string   `json:"currency"`
	ProratePolicy    string   `json:"prorate_policy"`
	DeferPolicy      string   `json:"defer_policy"`
	TaxMode          string   `json:"tax_mode"`
	TaxPct           *float64 `json:"tax_pct"`
	LateFeeGraceDays *int     `json:"late_fee_grace_days"`
	MinChargeIDR     *int     `json:"min_charge_idr"`
}

var billingProratePolicies = map[string]struct{}{
	"full_period":          {},
	"prorated_daily":       {},
	"prorated_proportional": {},
}
var billingDeferPolicies = map[string]struct{}{
	"never":          {},
	"first_invoice":  {},
	"until_activated": {},
}
var billingTaxModes = map[string]struct{}{
	"inclusive": {},
	"exclusive": {},
	"none":      {},
}

// iso4217 is a small subset of ISO 4217 currencies that the platform
// supports in Phase 1 (IDR is the only live currency; USD/EUR are for
// reseller settlement). Keep tight rather than letting any 3-letter
// string through.
var iso4217 = map[string]struct{}{
	"IDR": {}, "USD": {}, "EUR": {}, "SGD": {}, "MYR": {}, "JPY": {}, "AUD": {},
}

func (BillingValidator) Validate(content json.RawMessage) ([]string, []string) {
	var errs, warns []string
	var b billingBody
	if err := json.Unmarshal(content, &b); err != nil {
		return []string{fmt.Sprintf("billing.parse_failed: %v", err)}, nil
	}

	if b.CycleDay == nil {
		errs = append(errs, "billing.cycle_day_required")
	} else if *b.CycleDay < 1 || *b.CycleDay > 28 {
		// Cap at 28 to avoid Feb-edge ambiguity (29/30/31 → which day in Feb?).
		// TC-SBE-004 (Anniversary Last-Day Month) — we don't allow this here;
		// callers that want last-day-of-month use a separate explicit flag
		// at the customer level.
		errs = append(errs, fmt.Sprintf(
			"billing.cycle_day_out_of_range: got %d, want [1, 28]",
			*b.CycleDay,
		))
	}

	currency := strings.ToUpper(strings.TrimSpace(b.Currency))
	if currency == "" {
		errs = append(errs, "billing.currency_required")
	} else if _, ok := iso4217[currency]; !ok {
		errs = append(errs, fmt.Sprintf("billing.currency_unsupported: %q (allow only ISO 4217 majors)", b.Currency))
	}

	if b.ProratePolicy == "" {
		errs = append(errs, "billing.prorate_policy_required")
	} else if _, ok := billingProratePolicies[strings.ToLower(b.ProratePolicy)]; !ok {
		errs = append(errs, fmt.Sprintf(
			"billing.prorate_policy_invalid: %q (allow: full_period | prorated_daily | prorated_proportional)",
			b.ProratePolicy,
		))
	}

	if b.DeferPolicy == "" {
		errs = append(errs, "billing.defer_policy_required")
	} else if _, ok := billingDeferPolicies[strings.ToLower(b.DeferPolicy)]; !ok {
		errs = append(errs, fmt.Sprintf(
			"billing.defer_policy_invalid: %q (allow: never | first_invoice | until_activated)",
			b.DeferPolicy,
		))
	}

	if b.TaxMode == "" {
		errs = append(errs, "billing.tax_mode_required")
	} else if _, ok := billingTaxModes[strings.ToLower(b.TaxMode)]; !ok {
		errs = append(errs, fmt.Sprintf(
			"billing.tax_mode_invalid: %q (allow: inclusive | exclusive | none)",
			b.TaxMode,
		))
	}

	if b.TaxPct == nil {
		errs = append(errs, "billing.tax_pct_required")
	} else if *b.TaxPct < 0 || *b.TaxPct > 0.50 {
		errs = append(errs, fmt.Sprintf(
			"billing.tax_pct_out_of_range: got %.2f, want [0.00, 0.50]",
			*b.TaxPct,
		))
	}

	if b.LateFeeGraceDays != nil && *b.LateFeeGraceDays < 0 {
		errs = append(errs, "billing.late_fee_grace_days_negative")
	}

	if b.MinChargeIDR != nil && *b.MinChargeIDR < 0 {
		errs = append(errs, "billing.min_charge_idr_negative")
	}

	// Cross-field: defer=until_activated + prorate=full_period is illegal —
	// holding the invoice till activation means the proration window is
	// known; charging the full period defeats that semantic.
	defer_ := strings.ToLower(b.DeferPolicy)
	prorate := strings.ToLower(b.ProratePolicy)
	if defer_ == "until_activated" && prorate == "full_period" {
		errs = append(errs, "billing.defer_policy_inconsistent_with_prorate: until_activated requires a prorated policy")
	}

	// Warning: tax_mode=none + tax_pct > 0 is suspicious.
	if strings.ToLower(b.TaxMode) == "none" && b.TaxPct != nil && *b.TaxPct > 0 {
		warns = append(warns, "billing.tax_mode_none_but_pct_positive: did you mean exclusive/inclusive?")
	}

	return errs, warns
}

// =====================================================================
// ServiceValidator (kind="service")
//
// Source: TC-SSD-001..015. Validates the per-plan service definition
// consumed by the RADIUS profile loader, ticket SLA evaluator, and CPQ.
// =====================================================================

type ServiceValidator struct{}

func (ServiceValidator) Kind() string { return "service" }

type serviceBody struct {
	PlanName          string   `json:"plan_name"`
	DownloadMbps      *int     `json:"download_mbps"`
	UploadMbps        *int     `json:"upload_mbps"`
	SLATier           string   `json:"sla_tier"`
	DataCapGB         *int     `json:"data_cap_gb"`
	QoSPriority       *int     `json:"qos_priority"`
	SupportsStaticIP  bool     `json:"supports_static_ip"`
	BundledServices   []string `json:"bundled_services"`
	RADIUSProfileTmpl string   `json:"radius_profile_template"`
	DegradationPolicy *struct {
		WarnThresholdPct  *int `json:"warn_threshold_pct"`
		SoftThrottlePct   *int `json:"soft_throttle_pct"`
		HardDisconnectPct *int `json:"hard_disconnect_pct"`
	} `json:"degradation_policy"`
}

var serviceSLATiers = map[string]struct{}{
	"bronze": {}, "silver": {}, "gold": {}, "platinum": {},
}

func (ServiceValidator) Validate(content json.RawMessage) ([]string, []string) {
	var errs, warns []string
	var b serviceBody
	if err := json.Unmarshal(content, &b); err != nil {
		return []string{fmt.Sprintf("service.parse_failed: %v", err)}, nil
	}

	if strings.TrimSpace(b.PlanName) == "" {
		errs = append(errs, "service.plan_name_required")
	}

	if b.DownloadMbps == nil {
		errs = append(errs, "service.download_mbps_required")
	} else if *b.DownloadMbps < 1 || *b.DownloadMbps > 10000 {
		errs = append(errs, fmt.Sprintf(
			"service.download_mbps_out_of_range: got %d, want [1, 10000]",
			*b.DownloadMbps,
		))
	}
	if b.UploadMbps == nil {
		errs = append(errs, "service.upload_mbps_required")
	} else if *b.UploadMbps < 1 {
		errs = append(errs, "service.upload_mbps_must_be_positive")
	}
	// upload ≤ download × 2 — catches typo where upload >> download (only
	// symmetric enterprise plans should approach this).
	if b.DownloadMbps != nil && b.UploadMbps != nil && *b.UploadMbps > *b.DownloadMbps*2 {
		errs = append(errs, fmt.Sprintf(
			"service.upload_too_high_vs_download: upload %d > download %d × 2",
			*b.UploadMbps, *b.DownloadMbps,
		))
	}

	if b.SLATier == "" {
		errs = append(errs, "service.sla_tier_required")
	} else if _, ok := serviceSLATiers[strings.ToLower(b.SLATier)]; !ok {
		errs = append(errs, fmt.Sprintf(
			"service.sla_tier_invalid: %q (allow: bronze | silver | gold | platinum)",
			b.SLATier,
		))
	}

	if b.QoSPriority == nil {
		errs = append(errs, "service.qos_priority_required")
	} else if *b.QoSPriority < 0 || *b.QoSPriority > 7 {
		errs = append(errs, fmt.Sprintf(
			"service.qos_priority_out_of_range: got %d, want [0, 7]",
			*b.QoSPriority,
		))
	}

	// data_cap_gb: typo guard — if set, must be > 10 (10 GB cap doesn't
	// exist; smallest realistic cap is 50 GB).
	if b.DataCapGB != nil && *b.DataCapGB > 0 && *b.DataCapGB <= 10 {
		errs = append(errs, fmt.Sprintf(
			"service.data_cap_gb_too_small: got %d, want > 10 (typo guard)",
			*b.DataCapGB,
		))
	}

	// Degradation policy monotonicity: warn ≤ soft ≤ hard, all 0..100.
	if b.DegradationPolicy == nil {
		errs = append(errs, "service.degradation_policy_required")
	} else {
		dp := b.DegradationPolicy
		var warn, soft, hard int
		var have bool = true
		if dp.WarnThresholdPct == nil || dp.SoftThrottlePct == nil || dp.HardDisconnectPct == nil {
			errs = append(errs, "service.degradation_policy_incomplete: warn/soft/hard all required")
			have = false
		} else {
			warn = *dp.WarnThresholdPct
			soft = *dp.SoftThrottlePct
			hard = *dp.HardDisconnectPct
		}
		if have {
			if warn < 0 || warn > 100 || soft < 0 || soft > 100 || hard < 0 || hard > 100 {
				errs = append(errs, "service.degradation_policy_out_of_range: thresholds must be in [0, 100]")
			}
			if !(warn <= soft && soft <= hard) {
				errs = append(errs, fmt.Sprintf(
					"service.degradation_policy_non_monotonic: warn %d ≤ soft %d ≤ hard %d required",
					warn, soft, hard,
				))
			}
		}
	}

	// Warning: gold/platinum w/o bundled services — high tier usually
	// ships bundled VPN / static IP / 24x7 NOC.
	t := strings.ToLower(b.SLATier)
	if (t == "gold" || t == "platinum") && len(b.BundledServices) == 0 {
		warns = append(warns, fmt.Sprintf(
			"service.high_tier_no_bundle: SLA tier %q usually ships bundled services", t,
		))
	}

	if strings.TrimSpace(b.RADIUSProfileTmpl) == "" {
		warns = append(warns, "service.radius_profile_template_empty: RADIUS provisioning will fail without this")
	}

	return errs, warns
}

// =====================================================================
// CommissionValidator (kind="commission")
//
// Source: TC-SCD-001..025. The largest validator — commission is the
// most rule-laden schema kind. Validates clawback window, split sum,
// flat-vs-pct consistency, ramp duration.
//
// Wave 114 NOTE: CommissionTriggerPolicy (billing/domain/commission_trigger.go)
// consumes a SUBSET of these fields:
//   - trigger_event  → CommissionTriggerKind
//   - clawback_days  → ClawbackDays
//   - percentage     → PercentageOfBasis (× 100; Wave 114 stores 0..100)
//   - flat_amount    → FlatAmount
//
// Drift risk: PercentageOfBasis scale. Wave 116 stores percentage as a
// fraction (0..1); Wave 114's CommissionTriggerPolicy.PercentageOfBasis
// is "percent × 100" (15 → 15%). The schema body convention used by
// Wave 114's existing tests is 0..100 too, so a schema authored for
// Wave 114 would FAIL Wave 116's "percentage ∈ [0.00, 1.00]" check.
//
// Decision: this validator accepts BOTH conventions, normalizing
// internally — if percentage > 1.0 we treat it as a percent-out-of-100
// and emit a warning suggesting the canonical fraction form. The Wave
// 114 policy struct continues to receive what its tests expect.
// =====================================================================

type CommissionValidator struct{}

func (CommissionValidator) Kind() string { return "commission" }

type commissionSplitRule struct {
	Role string  `json:"role"`
	Pct  float64 `json:"pct"`
}

type commissionBody struct {
	TriggerEvent          string                `json:"trigger_event"`
	RecipientRole         string                `json:"recipient_role"`
	BaseAmountBasis       string                `json:"base_amount_basis"`
	FlatAmount            *float64              `json:"flat_amount"`
	Percentage            *float64              `json:"percentage"`
	ClawbackDays          *int                  `json:"clawback_days"`
	SplitRules            []commissionSplitRule `json:"split_rules"`
	RampMonths            *int                  `json:"ramp_months"`
	CapIDR                *float64              `json:"cap_idr"`
	RequiresKYC           bool                  `json:"requires_kyc"`
	MinSubscriptionMonths *int                  `json:"min_subscription_months"`
}

var commissionTriggerEvents = map[string]struct{}{
	"on_paid":        {},
	"on_activated":   {},
	"on_anniversary": {},
	"manual":         {},
}
var commissionBases = map[string]struct{}{
	"flat":              {},
	"plan_pct":          {},
	"first_invoice_pct": {},
	"recurring_pct":     {},
}

func (CommissionValidator) Validate(content json.RawMessage) ([]string, []string) {
	var errs, warns []string
	var b commissionBody
	if err := json.Unmarshal(content, &b); err != nil {
		return []string{fmt.Sprintf("commission.parse_failed: %v", err)}, nil
	}

	if b.TriggerEvent == "" {
		errs = append(errs, "commission.trigger_event_required")
	} else if _, ok := commissionTriggerEvents[strings.ToLower(b.TriggerEvent)]; !ok {
		errs = append(errs, fmt.Sprintf(
			"commission.trigger_event_invalid: %q (allow: on_paid | on_activated | on_anniversary | manual)",
			b.TriggerEvent,
		))
	}

	if strings.TrimSpace(b.RecipientRole) == "" {
		errs = append(errs, "commission.recipient_role_required")
	}

	if b.BaseAmountBasis == "" {
		errs = append(errs, "commission.base_amount_basis_required")
	} else if _, ok := commissionBases[strings.ToLower(b.BaseAmountBasis)]; !ok {
		errs = append(errs, fmt.Sprintf(
			"commission.base_amount_basis_invalid: %q (allow: flat | plan_pct | first_invoice_pct | recurring_pct)",
			b.BaseAmountBasis,
		))
	}

	// Percentage range — accept BOTH fraction (0..1) and percent (0..100)
	// to bridge Wave 114's convention; warn on percent form.
	if b.Percentage != nil {
		p := *b.Percentage
		switch {
		case p < 0:
			errs = append(errs, "commission.percentage_negative")
		case p > 100:
			errs = append(errs, fmt.Sprintf(
				"commission.percentage_too_large: got %.2f, want ≤ 1.00 (fraction) or ≤ 100 (percent)",
				p,
			))
		case p > 1.0 && p <= 100:
			warns = append(warns, fmt.Sprintf(
				"commission.percentage_uses_percent_form: %.2f looks like a percent-out-of-100; canonical form is fraction (0..1)",
				p,
			))
		}
	}

	if b.ClawbackDays == nil {
		errs = append(errs, "commission.clawback_days_required")
	} else if *b.ClawbackDays < 0 || *b.ClawbackDays > 365 {
		errs = append(errs, fmt.Sprintf(
			"commission.clawback_days_out_of_range: got %d, want [0, 365]",
			*b.ClawbackDays,
		))
	}

	if b.RampMonths == nil {
		errs = append(errs, "commission.ramp_months_required")
	} else if *b.RampMonths < 0 || *b.RampMonths > 12 {
		errs = append(errs, fmt.Sprintf(
			"commission.ramp_months_out_of_range: got %d, want [0, 12]",
			*b.RampMonths,
		))
	}

	// split_rules: if non-empty, pct must sum to 1.00. Tolerance for
	// float drift is 0.005 (half a basis point).
	if len(b.SplitRules) > 0 {
		var sum float64
		for i, r := range b.SplitRules {
			if strings.TrimSpace(r.Role) == "" {
				errs = append(errs, fmt.Sprintf("commission.split_rules[%d].role_required", i))
			}
			if r.Pct < 0 || r.Pct > 1.0 {
				errs = append(errs, fmt.Sprintf(
					"commission.split_rules[%d].pct_out_of_range: got %.4f, want [0.00, 1.00]",
					i, r.Pct,
				))
			}
			sum += r.Pct
		}
		if abs(sum-1.0) > 0.005 {
			errs = append(errs, fmt.Sprintf(
				"commission.split_rules_sum_invalid: got %.4f, want 1.00 (within 0.005)",
				sum,
			))
		}
	}

	// flat_amount XOR percentage based on base_amount_basis:
	//   flat            → flat_amount required, percentage forbidden
	//   plan_pct | first_invoice_pct | recurring_pct → percentage required, flat forbidden
	switch strings.ToLower(b.BaseAmountBasis) {
	case "flat":
		if b.FlatAmount == nil {
			errs = append(errs, "commission.flat_amount_required_for_flat_basis")
		}
		if b.Percentage != nil && *b.Percentage > 0 {
			errs = append(errs, "commission.percentage_forbidden_for_flat_basis")
		}
	case "plan_pct", "first_invoice_pct", "recurring_pct":
		if b.Percentage == nil {
			errs = append(errs, "commission.percentage_required_for_pct_basis")
		}
		if b.FlatAmount != nil && *b.FlatAmount > 0 {
			errs = append(errs, "commission.flat_amount_forbidden_for_pct_basis")
		}
	}

	if b.FlatAmount != nil && *b.FlatAmount < 0 {
		errs = append(errs, "commission.flat_amount_negative")
	}
	if b.CapIDR != nil && *b.CapIDR < 0 {
		errs = append(errs, "commission.cap_idr_negative")
	}
	if b.MinSubscriptionMonths != nil && *b.MinSubscriptionMonths < 0 {
		errs = append(errs, "commission.min_subscription_months_negative")
	}

	// Warning: requires_kyc + on_paid trigger — commission may fire
	// before KYC, depending on activation order. Not an error (sometimes
	// intentional, e.g. residential pre-paid plans).
	if b.RequiresKYC && strings.ToLower(b.TriggerEvent) == "on_paid" {
		warns = append(warns, "commission.kyc_on_paid_race: requires_kyc=true with trigger=on_paid may fire commission before KYC completes")
	}

	return errs, warns
}

// =====================================================================
// SuspensionValidator (kind="suspension")
//
// Source: TC-SSE-001..004. Validates the dunning + RADIUS suspension
// cadence consumed by Wave 114's suspension scheduler.
//
// Wave 114 NOTE: SuspensionPolicy (billing/domain/suspension.go) reads
// soft_suspend_grace_days + hard_suspend_grace_days. The remaining
// fields (warn, RADIUS restore window, channels) are Wave-116 new.
// Drift risk: none — Wave 114 ignores fields it doesn't know about.
// =====================================================================

type SuspensionValidator struct{}

func (SuspensionValidator) Kind() string { return "suspension" }

type suspensionBody struct {
	WarnGraceDays                  *int     `json:"warn_grace_days"`
	SoftSuspendGraceDays           *int     `json:"soft_suspend_grace_days"`
	HardSuspendGraceDays           *int     `json:"hard_suspend_grace_days"`
	RequiresSupervisorForHard      bool     `json:"requires_supervisor_for_hard"`
	RADIUSRestoreWindowMin         *int     `json:"radius_restore_window_minutes"`
	NotificationChannels           []string `json:"notification_channels"`
	RestoreRequiresFullSettlement  bool     `json:"restore_requires_full_settlement"`
	PartialPaymentAdvances         bool     `json:"partial_payment_advances"`
}

var suspensionChannels = map[string]struct{}{
	"whatsapp": {}, "email": {}, "sms": {}, "push": {}, "inapp": {},
}

func (SuspensionValidator) Validate(content json.RawMessage) ([]string, []string) {
	var errs, warns []string
	var b suspensionBody
	if err := json.Unmarshal(content, &b); err != nil {
		return []string{fmt.Sprintf("suspension.parse_failed: %v", err)}, nil
	}

	requiredInt := func(name string, v *int, lo, hi int) (int, bool) {
		if v == nil {
			errs = append(errs, "suspension."+name+"_required")
			return 0, false
		}
		if *v < lo || *v > hi {
			errs = append(errs, fmt.Sprintf(
				"suspension.%s_out_of_range: got %d, want [%d, %d]", name, *v, lo, hi,
			))
			return *v, false
		}
		return *v, true
	}

	warn, warnOK := requiredInt("warn_grace_days", b.WarnGraceDays, 0, 90)
	soft, softOK := requiredInt("soft_suspend_grace_days", b.SoftSuspendGraceDays, 0, 90)
	hard, hardOK := requiredInt("hard_suspend_grace_days", b.HardSuspendGraceDays, 0, 90)

	// Monotonic: warn < soft < hard. Strict — equality means the dunning
	// path collapses (warn fires same day as suspend).
	if warnOK && softOK && hardOK {
		if !(warn < soft && soft < hard) {
			errs = append(errs, fmt.Sprintf(
				"suspension.grace_days_non_monotonic: warn %d < soft %d < hard %d required",
				warn, soft, hard,
			))
		}
	}

	// RADIUS restore window: 0..60. Anything higher violates the SLA
	// commitment for "service restored within an hour of full payment."
	if b.RADIUSRestoreWindowMin == nil {
		errs = append(errs, "suspension.radius_restore_window_minutes_required")
	} else if *b.RADIUSRestoreWindowMin < 0 || *b.RADIUSRestoreWindowMin > 60 {
		errs = append(errs, fmt.Sprintf(
			"suspension.radius_restore_window_minutes_out_of_range: got %d, want [0, 60]",
			*b.RADIUSRestoreWindowMin,
		))
	}

	if len(b.NotificationChannels) == 0 {
		errs = append(errs, "suspension.notification_channels_empty: at least one channel required")
	}
	for _, c := range b.NotificationChannels {
		cl := strings.ToLower(strings.TrimSpace(c))
		if _, ok := suspensionChannels[cl]; !ok {
			errs = append(errs, fmt.Sprintf(
				"suspension.notification_channel_disallowed: %q (allow: whatsapp | email | sms | push | inapp)",
				c,
			))
		}
	}

	// Warning: contradiction between partial_payment_advances=true and
	// restore_requires_full_settlement=true — these flags say opposite
	// things about how a partial payment is treated.
	if b.PartialPaymentAdvances && b.RestoreRequiresFullSettlement {
		warns = append(warns, "suspension.partial_advance_contradicts_full_settlement: partial_payment_advances=true with restore_requires_full_settlement=true is contradictory")
	}

	return errs, warns
}

// =====================================================================
// Helpers
// =====================================================================

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
