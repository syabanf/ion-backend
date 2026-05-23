package domain

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ion-core/backend/pkg/errors"
)

// PreBOQRequiredField is the admin-managed config row that drives the
// Wave 106 Pre-BOQ structured validator (TC-OP-009).
//
// Each row defines one key the Pre-BOQ snapshot must contain. The
// CompletePreBOQ usecase loads the active list and asserts that every
// row with Required=true has a non-empty value in the submitted JSON.
//
// Persisted to enterprise.pre_boq_required_fields by migration 0071.
// Seeded with 5 canonical entries (customer_name, customer_email,
// contact_phone, address_line, expected_capacity_mbps).
type PreBOQRequiredField struct {
	ID        uuid.UUID
	FieldKey  string
	Label     string
	FieldType string // 'string' | 'email' | 'number'
	Required  bool
	Position  int
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ValidatePreBOQSnapshot enforces the per-field requirements against a
// raw JSON snapshot. Returns nil on success, a typed Validation error
// listing the missing keys otherwise.
//
// Empty values count as "missing" — TC-OP-009 wants the operator to
// actually fill the form, not just send the key with an empty string.
// For FieldType='number' we also reject the value 0 as "unset" since
// expected_capacity_mbps=0 makes no sense in an enterprise quote.
func ValidatePreBOQSnapshot(snapshot []byte, fields []PreBOQRequiredField) error {
	if len(snapshot) == 0 {
		return errors.Validation(
			"opportunity.pre_boq_empty",
			"pre_boq snapshot must not be empty",
		)
	}
	var payload map[string]any
	if err := json.Unmarshal(snapshot, &payload); err != nil {
		return errors.Validation(
			"opportunity.pre_boq_invalid_json",
			"pre_boq snapshot must be a JSON object",
		)
	}
	missing := []string{}
	for _, f := range fields {
		if !f.Required {
			continue
		}
		v, ok := payload[f.FieldKey]
		if !ok {
			missing = append(missing, f.FieldKey)
			continue
		}
		switch f.FieldType {
		case "number":
			// Accept numeric values that are non-zero. JSON numbers
			// land as float64 in Go's untyped map.
			switch n := v.(type) {
			case float64:
				if n == 0 {
					missing = append(missing, f.FieldKey)
				}
			case int:
				if n == 0 {
					missing = append(missing, f.FieldKey)
				}
			default:
				missing = append(missing, f.FieldKey)
			}
		default:
			s, ok := v.(string)
			if !ok {
				missing = append(missing, f.FieldKey)
				continue
			}
			if strings.TrimSpace(s) == "" {
				missing = append(missing, f.FieldKey)
			}
		}
	}
	if len(missing) > 0 {
		return errors.Validation(
			"opportunity.pre_boq_missing_required_fields",
			"pre_boq snapshot is missing required fields: "+strings.Join(missing, ", "),
		)
	}
	return nil
}
