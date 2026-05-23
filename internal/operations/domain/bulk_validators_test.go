package domain

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

// =====================================================================
// Plan change validator
// =====================================================================

type stubPCVal struct {
	planExists  map[uuid.UUID]bool
	custPlan    map[uuid.UUID]*uuid.UUID
	custStatus  map[uuid.UUID]string
	errOnLookup error
}

func (s *stubPCVal) PlanExists(_ context.Context, id uuid.UUID) (bool, error) {
	if s.errOnLookup != nil {
		return false, s.errOnLookup
	}
	return s.planExists[id], nil
}

func (s *stubPCVal) CustomerCurrentPlan(_ context.Context, c uuid.UUID) (*uuid.UUID, string, error) {
	return s.custPlan[c], s.custStatus[c], nil
}

func TestValidatePlanChangeItem(t *testing.T) {
	customer := uuid.New()
	planA := uuid.New()
	planB := uuid.New()

	tests := []struct {
		name   string
		stub   *stubPCVal
		item   BulkPlanChangeItem
		want   string
		wantSk bool
	}{
		{
			name: "happy",
			stub: &stubPCVal{
				planExists: map[uuid.UUID]bool{planB: true},
				custPlan:   map[uuid.UUID]*uuid.UUID{customer: &planA},
				custStatus: map[uuid.UUID]string{customer: "active"},
			},
			item: BulkPlanChangeItem{CustomerID: customer, TargetPlanID: planB},
			want: "",
		},
		{
			name: "plan_not_found",
			stub: &stubPCVal{
				planExists: map[uuid.UUID]bool{},
				custStatus: map[uuid.UUID]string{customer: "active"},
			},
			item: BulkPlanChangeItem{CustomerID: customer, TargetPlanID: planB},
			want: "target_plan_not_found",
		},
		{
			name: "customer_terminated",
			stub: &stubPCVal{
				planExists: map[uuid.UUID]bool{planB: true},
				custStatus: map[uuid.UUID]string{customer: "terminated"},
			},
			item: BulkPlanChangeItem{CustomerID: customer, TargetPlanID: planB},
			want: "customer_terminated",
		},
		{
			name: "no_op_skip",
			stub: &stubPCVal{
				planExists: map[uuid.UUID]bool{planB: true},
				custPlan:   map[uuid.UUID]*uuid.UUID{customer: &planB},
				custStatus: map[uuid.UUID]string{customer: "active"},
			},
			item:   BulkPlanChangeItem{CustomerID: customer, TargetPlanID: planB},
			want:   "customer_already_on_target_plan",
			wantSk: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reason, skip, err := ValidatePlanChangeItem(context.Background(), tc.stub, &tc.item)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if reason != tc.want {
				t.Errorf("reason: want %q, got %q", tc.want, reason)
			}
			if skip != tc.wantSk {
				t.Errorf("skip: want %v, got %v", tc.wantSk, skip)
			}
		})
	}
}

// =====================================================================
// ODP migration validator
// =====================================================================

type stubOMVal struct {
	capacityOf map[uuid.UUID]bool
	overlap    bool
	errCap     error
}

func (s *stubOMVal) PortHasCapacity(_ context.Context, id uuid.UUID) (bool, error) {
	if s.errCap != nil {
		return false, s.errCap
	}
	return s.capacityOf[id], nil
}
func (s *stubOMVal) WindowOverlapsMaintenance(_ context.Context, _ uuid.UUID, _, _ time.Time) (bool, error) {
	return s.overlap, nil
}

func TestValidateODPMigrationItem(t *testing.T) {
	port := uuid.New()
	now := time.Now().UTC()
	end := now.Add(2 * time.Hour)

	tests := []struct {
		name string
		stub *stubOMVal
		item BulkODPMigrationItem
		want string
	}{
		{
			name: "happy",
			stub: &stubOMVal{capacityOf: map[uuid.UUID]bool{port: true}},
			item: BulkODPMigrationItem{ToOLTPortID: port},
			want: "",
		},
		{
			name: "no_capacity",
			stub: &stubOMVal{capacityOf: map[uuid.UUID]bool{}},
			item: BulkODPMigrationItem{ToOLTPortID: port},
			want: "destination_port_no_capacity",
		},
		{
			name: "window_overlaps",
			stub: &stubOMVal{capacityOf: map[uuid.UUID]bool{port: true}, overlap: true},
			item: BulkODPMigrationItem{
				ToOLTPortID:          port,
				ScheduledWindowStart: &now,
				ScheduledWindowEnd:   &end,
			},
			want: "window_overlaps_maintenance",
		},
		{
			name: "capacity_lookup_err",
			stub: &stubOMVal{errCap: errors.New("boom")},
			item: BulkODPMigrationItem{ToOLTPortID: port},
			want: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reason, err := ValidateODPMigrationItem(context.Background(), tc.stub, &tc.item)
			if tc.name == "capacity_lookup_err" {
				if err == nil {
					t.Fatalf("want err")
				}
				return
			}
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if reason != tc.want {
				t.Errorf("reason: want %q, got %q", tc.want, reason)
			}
		})
	}
}

// =====================================================================
// WO creation validator
// =====================================================================

type stubWCVal struct {
	templateExists bool
	openWOByType   map[string]bool
}

func (s *stubWCVal) WOTemplateExists(_ context.Context, _ *uuid.UUID) (bool, error) {
	return s.templateExists, nil
}
func (s *stubWCVal) CustomerHasOpenWOOfType(_ context.Context, _ uuid.UUID, woType string) (bool, error) {
	return s.openWOByType[woType], nil
}

func TestValidateWOCreationItem(t *testing.T) {
	tests := []struct {
		name    string
		stub    *stubWCVal
		item    BulkWOCreationItem
		want    string
		wantDup bool
	}{
		{
			name: "happy",
			stub: &stubWCVal{templateExists: true},
			item: BulkWOCreationItem{WOType: "maintenance"},
			want: "",
		},
		{
			name: "template_missing",
			stub: &stubWCVal{templateExists: false},
			item: BulkWOCreationItem{WOType: "maintenance"},
			want: "wo_template_not_found",
		},
		{
			name: "duplicate",
			stub: &stubWCVal{
				templateExists: true,
				openWOByType:   map[string]bool{"maintenance": true},
			},
			item:    BulkWOCreationItem{WOType: "maintenance"},
			want:    "customer_has_open_wo_of_type",
			wantDup: true,
		},
		{
			name: "no_type_skips_open_check",
			stub: &stubWCVal{templateExists: true, openWOByType: map[string]bool{"any": true}},
			item: BulkWOCreationItem{WOType: ""},
			want: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reason, dup, err := ValidateWOCreationItem(context.Background(), tc.stub, &tc.item)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if reason != tc.want {
				t.Errorf("reason: want %q, got %q", tc.want, reason)
			}
			if dup != tc.wantDup {
				t.Errorf("dup: want %v, got %v", tc.wantDup, dup)
			}
		})
	}
}
