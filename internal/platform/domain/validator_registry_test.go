// Wave 116 — ValidatorRegistry tests.

package domain

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestValidatorRegistry_DefaultsRegistered(t *testing.T) {
	r := NewValidatorRegistry()
	got := r.Kinds()
	want := []string{"billing", "commission", "onboarding", "service", "suspension"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("default kinds mismatch: got %v want %v", got, want)
	}
}

func TestValidatorRegistry_For_UnknownReturnsNil(t *testing.T) {
	r := NewValidatorRegistry()
	if r.For("addon") != nil {
		t.Fatal("expected nil for unregistered kind")
	}
}

func TestValidatorRegistry_Run_UnknownPasses(t *testing.T) {
	r := NewValidatorRegistry()
	out := r.Run("addon", json.RawMessage(`{}`))
	if !out.IsValid || len(out.Errors) != 0 {
		t.Fatalf("unknown kind should be valid by default: %+v", out)
	}
}

func TestValidatorRegistry_Run_OnboardingValid(t *testing.T) {
	r := NewValidatorRegistry()
	out := r.Run("onboarding", json.RawMessage(`{
		"required_documents": [{"code":"ktp","allowed_formats":["png"]}],
		"min_ocr_confidence": 0.80,
		"max_doc_size_mb": 10
	}`))
	if !out.IsValid {
		t.Fatalf("expected valid; got errors=%v", out.Errors)
	}
}

func TestValidatorRegistry_Run_BillingInvalid(t *testing.T) {
	r := NewValidatorRegistry()
	out := r.Run("billing", json.RawMessage(`{}`))
	if out.IsValid {
		t.Fatal("expected invalid (missing required fields)")
	}
	if len(out.Errors) == 0 {
		t.Fatal("expected errors")
	}
}

// Custom validator override is supported via Register.
type fakeValidator struct{ k string }

func (f fakeValidator) Kind() string { return f.k }
func (f fakeValidator) Validate(_ json.RawMessage) ([]string, []string) {
	return nil, []string{"hello"}
}

func TestValidatorRegistry_Register_Overrides(t *testing.T) {
	r := NewValidatorRegistry()
	r.Register(fakeValidator{k: "service"})
	out := r.Run("service", json.RawMessage(`{}`))
	if !out.IsValid || len(out.Warnings) != 1 || out.Warnings[0] != "hello" {
		t.Fatalf("register override didn't take effect: %+v", out)
	}
}

func TestValidatorRegistry_NilSafe(t *testing.T) {
	var r *ValidatorRegistry
	if r.For("anything") != nil {
		t.Fatal("nil registry should return nil")
	}
	if r.Kinds() != nil {
		t.Fatal("nil registry should return nil kinds")
	}
}
