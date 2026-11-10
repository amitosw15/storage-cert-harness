package kubeburnerocp

import (
	"testing"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
)

func TestWorkloadSelection(t *testing.T) {
	cases := []struct {
		id     string
		params map[string]any
		want   string
		valid  bool
	}{
		{"TR-STOR-006", map[string]any{"iterations": 5, "claim_size": "1Gi"}, WorkloadPVCDensity, true},
		{"TR-VIRT-004", map[string]any{"iterations": 1, "iteration_vms": 2}, WorkloadVirtMigration, true},
		{"TR-VIRT-019", nil, WorkloadVirtParallel, true},
		{"TR-VIRT-019", map[string]any{"workload": WorkloadVirtParallel}, WorkloadVirtParallel, true},
		{"TR-STOR-006", map[string]any{"workload": WorkloadVirtParallel}, WorkloadPVCDensity, false},
		{"TR-STOR-006", map[string]any{"workload": "unknown"}, WorkloadPVCDensity, false},
		{"TR-STOR-006", map[string]any{"workload": ""}, WorkloadPVCDensity, false},
		{"TR-UNKNOWN", nil, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.id+"/"+tc.want, func(t *testing.T) {
			p := resolveParams([]core.TestRequirement{{ID: tc.id, Params: tc.params}})
			if p.Workload != tc.want {
				t.Fatalf("workload = %q, want %q", p.Workload, tc.want)
			}
			if err := p.validate(); (err == nil) != tc.valid {
				t.Fatalf("validate error = %v, want valid %v", err, tc.valid)
			}
		})
	}
}
