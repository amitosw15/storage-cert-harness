package grader

import (
	"context"
	"testing"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
)

func TestCompareWithUnits(t *testing.T) {
	cases := []struct {
		name         string
		mVal         float64
		mUnit        string
		op           string
		wVal         float64
		wUnit        string
		wantPass     bool
		wantIncompat bool
	}{
		// The bug this fixes: a tool emits seconds, the gate is in minutes/hours.
		{"480s under 10min", 480, "s", "<", 10, "min", true, false},
		{"720s not under 10min", 720, "s", "<", 10, "min", false, false},
		{"3600s under 1h boundary", 3600, "s", "<", 1, "h", false, false},
		{"3600s at-most 1h boundary", 3600, "s", "<=", 1, "h", true, false},
		{"15ms at-most 0.015s", 15, "ms", "<=", 0.015, "s", true, false},
		{"same unit ms", 0.8, "ms", "<=", 2, "ms", true, false},
		// ratio dimension.
		{"99% above 95%", 99, "%", ">", 95, "%", true, false},
		// incompatible: both known, different dimensions.
		{"seconds vs iops", 5, "s", "<", 100, "iops", false, true},
		// unknown/empty unit → raw fallback (preserves pre-unit behavior).
		{"raw fallback both empty", 5, "", ">=", 10, "", false, false},
		{"raw fallback unknown unit", 5000, "widgets", ">=", 1000, "widgets", true, false},
		{"one side empty falls back to raw", 480, "s", "<", 600, "", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pass, incompat := compareWithUnits(tc.mVal, tc.mUnit, tc.op, tc.wVal, tc.wUnit)
			if incompat != tc.wantIncompat {
				t.Fatalf("incompatible = %v, want %v", incompat, tc.wantIncompat)
			}
			if !incompat && pass != tc.wantPass {
				t.Errorf("pass = %v, want %v", pass, tc.wantPass)
			}
		})
	}
}

// TestGradeSLAUnitNormalization exercises the fix through the full grader: a
// seconds measurement graded against a minutes gate must pass, and the report
// keeps the original units.
func TestGradeSLAUnitNormalization(t *testing.T) {
	g := New()
	tr := core.TestRequirement{
		ID: "TR-1", SLAStatus: core.StatusDefined,
		SLAs: []core.SLA{{Metric: "vm_boot_time", Type: "duration", Unit: "s"}},
		Bars: []core.SLA{{Metric: "vm_boot_time", Operator: "<", Value: f64(2), Unit: "min"}},
	}
	res := core.TestResult{TRID: "TR-1", Metrics: []core.Metric{{Name: "vm_boot_time", Value: 90, Unit: "s"}}}
	vs := g.GradeTR(context.Background(), tr, res, nil)
	if len(vs) != 1 {
		t.Fatalf("got %d verdicts, want 1", len(vs))
	}
	if vs[0].Outcome != core.OutcomePass {
		t.Errorf("outcome = %s, want pass (90 s < 2 min)", vs[0].Outcome)
	}
	if vs[0].Actual != "90 s" {
		t.Errorf("Actual = %q, want original units %q", vs[0].Actual, "90 s")
	}
	if vs[0].Expected != "< 2 min" {
		t.Errorf("Expected = %q, want %q", vs[0].Expected, "< 2 min")
	}
}

// TestGradeSLAIncompatibleUnitsErrors: a gate whose unit can't be reconciled with
// the measured unit is an error, not a bogus pass/fail.
func TestGradeSLAIncompatibleUnitsErrors(t *testing.T) {
	g := New()
	tr := core.TestRequirement{
		ID: "TR-1", SLAStatus: core.StatusDefined,
		SLAs: []core.SLA{{Metric: "x", Unit: "iops"}},
		Bars: []core.SLA{{Metric: "x", Operator: "<", Value: f64(99), Unit: "iops"}},
	}
	res := core.TestResult{TRID: "TR-1", Metrics: []core.Metric{{Name: "x", Value: 5, Unit: "s"}}}
	vs := g.GradeTR(context.Background(), tr, res, nil)
	if len(vs) != 1 || vs[0].Outcome != core.OutcomeError {
		t.Fatalf("want single error verdict, got %+v", vs)
	}
}
