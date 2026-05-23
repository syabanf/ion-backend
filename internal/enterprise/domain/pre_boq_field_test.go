package domain

import (
	"testing"

	derrors "github.com/ion-core/backend/pkg/errors"
)

// TestValidatePreBOQSnapshot — Wave 106 TC-OP-009 structured validator.
// Table-driven coverage of:
//   - happy path with all required fields present
//   - missing required field
//   - empty string in a required string field
//   - zero value in a required number field
//   - invalid JSON body
//   - empty snapshot
//   - optional field (required=false) missing → still passes
func TestValidatePreBOQSnapshot(t *testing.T) {
	fields := []PreBOQRequiredField{
		{FieldKey: "customer_name", FieldType: "string", Required: true},
		{FieldKey: "customer_email", FieldType: "email", Required: true},
		{FieldKey: "expected_capacity_mbps", FieldType: "number", Required: true},
		{FieldKey: "optional_note", FieldType: "string", Required: false},
	}
	tests := []struct {
		name        string
		snapshot    string
		wantCode    string // empty means expect no error
	}{
		{
			name:     "happy path all required present",
			snapshot: `{"customer_name":"Acme","customer_email":"x@y.z","expected_capacity_mbps":100}`,
			wantCode: "",
		},
		{
			name:     "missing string field",
			snapshot: `{"customer_email":"x@y.z","expected_capacity_mbps":100}`,
			wantCode: "opportunity.pre_boq_missing_required_fields",
		},
		{
			name:     "empty string in required field",
			snapshot: `{"customer_name":"","customer_email":"x@y.z","expected_capacity_mbps":100}`,
			wantCode: "opportunity.pre_boq_missing_required_fields",
		},
		{
			name:     "whitespace-only string treated as empty",
			snapshot: `{"customer_name":"   ","customer_email":"x@y.z","expected_capacity_mbps":100}`,
			wantCode: "opportunity.pre_boq_missing_required_fields",
		},
		{
			name:     "zero value in number field treated as missing",
			snapshot: `{"customer_name":"Acme","customer_email":"x@y.z","expected_capacity_mbps":0}`,
			wantCode: "opportunity.pre_boq_missing_required_fields",
		},
		{
			name:     "invalid JSON",
			snapshot: `{customer_name:"Acme"`,
			wantCode: "opportunity.pre_boq_invalid_json",
		},
		{
			name:     "empty snapshot",
			snapshot: ``,
			wantCode: "opportunity.pre_boq_empty",
		},
		{
			name:     "missing optional field still passes",
			snapshot: `{"customer_name":"Acme","customer_email":"x@y.z","expected_capacity_mbps":100}`,
			wantCode: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePreBOQSnapshot([]byte(tt.snapshot), fields)
			if tt.wantCode == "" {
				if err != nil {
					t.Fatalf("want nil error, got %v", err)
				}
				return
			}
			de := derrors.As(err)
			if de == nil {
				t.Fatalf("want typed Validation error %q, got %v", tt.wantCode, err)
			}
			if de.Code != tt.wantCode {
				t.Errorf("want code %q, got %q (%v)", tt.wantCode, de.Code, err)
			}
		})
	}
}
