// Wave 105 — Boundary-regression marker package.
//
// The nightly boundary-regression.yml workflow runs:
//
//	go test -count=1 -run 'TestBoundary' ./internal/...
//
// to pick up any test whose name starts with "TestBoundary" across
// the internal/ tree. This file is the registry of "yes, the nightly
// matters" — a canary test that always runs, plus a TODO list of
// landmine cases (TC-BQ-010 / TC-NEG-009 / TC-MC-008) that should
// migrate to TestBoundary* names once their owning tests exist.
//
// Why this lives under test/boundary rather than internal/: a marker
// test in test/boundary is intentionally outside internal/ so the
// nightly `./internal/...` pattern doesn't accidentally pick it up
// when ops only want it to scan domain tests. Run it via:
//
//	go test ./test/boundary/...
//
// (no build tag — this test is always available; only the nightly
// schedule decides whether it runs).
package boundary

import "testing"

// TestBoundaryRegressionCanary is the always-pass canary that
// confirms the nightly job's `-run TestBoundary` filter has at
// least one match. Without this, a nightly that found zero
// boundary tests would silently pass with "no tests to run."
//
// As real boundary cases land under internal/<context>/ with
// TestBoundary* names, this canary stops being load-bearing
// but stays as the existence proof.
func TestBoundaryRegressionCanary(t *testing.T) {
	t.Log("boundary-regression canary — see .github/workflows/boundary-regression.yml")
}

// Planned boundary cases (from wave-91 audit doc + Phase 1 catalog):
//
//   TestBoundary_BQ010_BOQ_LineCount_Limit       — TC-BQ-010
//   TestBoundary_NEG009_Negotiation_RoundCap     — TC-NEG-009
//   TestBoundary_MC008_MonthlyCompliance_Cutoff  — TC-MC-008
//
// These get added inside their respective internal/ contexts as
// the owning test suites mature. The nightly job picks them up
// automatically via the TestBoundary* prefix — no workflow edits
// needed.
