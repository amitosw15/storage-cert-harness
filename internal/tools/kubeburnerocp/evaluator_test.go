package kubeburnerocp

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/registry"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/report"
)

func TestPVCDensityReportCount(t *testing.T) {
	for _, tc := range []struct {
		name       string
		requested  int
		reported   float64
		outcome    core.Outcome
		wantReason string
	}{
		{"override", 3, 3, core.OutcomePass, "all 3 requested PVCs"},
		{"default", 1000, 1000, core.OutcomePass, "all 1000 requested PVCs"},
		{"failed", 3, 3, core.OutcomeFail, "all 3 requested PVCs"},
		{"mismatch", 3, 5, core.OutcomeError, "all 3 requested PVCs"},
		{"missing count", 0, 3, core.OutcomeError, "all requested PVCs"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const original = "all 1000 PVCs reach Bound without provisioner backlog or failure"
			tr := core.TestRequirement{
				ID: "TR-STOR-006", Params: map[string]any{"iterations": tc.requested},
				Checks: []core.Check{{ID: pvcBoundCheck, Kind: "suite", Expectation: original}},
			}
			res := core.TestResult{
				Checks:  map[string]core.Outcome{pvcBoundCheck: tc.outcome},
				Metrics: []core.Metric{{Name: "pvc_requested_count", Value: tc.reported, Unit: "count"}},
			}
			tool, _ := registry.Get("kube-burner-ocp")
			verdicts, err := tool.Evaluators[tr.ID].Evaluate(context.Background(), tr, res)
			if err != nil {
				t.Fatal(err)
			}
			if len(verdicts) != 1 || verdicts[0].Outcome != tc.outcome || !strings.Contains(verdicts[0].Reason, tc.wantReason) {
				t.Fatalf("unexpected verdicts: %+v", verdicts)
			}
			v := verdicts[0]
			if tc.requested > 0 && v.Expected != fmt.Sprintf("%d requested PVCs", tc.requested) {
				t.Fatalf("unexpected expected count: %q", v.Expected)
			}
			if v.Actual != fmt.Sprintf("%g PVCs requested by tool", tc.reported) {
				t.Fatalf("unexpected actual count: %q", v.Actual)
			}
			if tr.Checks[0].Expectation != original {
				t.Fatal("evaluator mutated the catalog check")
			}
			r := report.Build("count-override", "2.0", core.Provenance{}, verdicts)
			for _, render := range []func(*bytes.Buffer) error{
				func(b *bytes.Buffer) error { return report.WriteMarkdown(b, r) },
				func(b *bytes.Buffer) error { return report.WriteJSON(b, r) },
			} {
				var b bytes.Buffer
				if err := render(&b); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(b.String(), tc.wantReason) || strings.Contains(b.String(), original) {
					t.Fatalf("report has incorrect count wording: %s", b.String())
				}
			}
		})
	}
}

func TestPVCDensityUncoveredScaleBlocksCertification(t *testing.T) {
	data := readFixtures(t, "pvc-density", "jobSummary.json", "pvcLatencyQuantilesMeasurement-pvc-density.json")
	result, err := ParseResults(WorkloadPVCDensity, "TR-STOR-006", data)
	if err != nil {
		t.Fatal(err)
	}
	gate := 30.0
	tr := core.TestRequirement{
		ID: "TR-STOR-006", Params: map[string]any{"iterations": 5},
		SLAs:   []core.SLA{{Metric: "pvc_bind_latency", Percentile: "p99", Unit: "s"}},
		Bars:   []core.SLA{{Metric: "pvc_bind_latency", Percentile: "p99", Unit: "s", Operator: "<=", Value: &gate, When: map[string]any{"iterations": 1000}}},
		Checks: []core.Check{{ID: pvcBoundCheck, Kind: "suite"}},
	}
	tool, _ := registry.Get("kube-burner-ocp")
	verdicts, err := tool.Evaluators[tr.ID].Evaluate(context.Background(), tr, result)
	if err != nil {
		t.Fatal(err)
	}
	if len(verdicts) != 2 || verdicts[0].Outcome != core.OutcomeSkip || !verdicts[0].BlocksCertification || verdicts[1].Outcome != core.OutcomePass {
		t.Fatalf("expected a blocking SLA skip and passing bound check: %+v", verdicts)
	}
	for i := range verdicts {
		verdicts[i].Level = 3
	}
	r := report.Build("uncovered-scale", "2.0", core.Provenance{}, verdicts)
	if r.Certifiable || r.Summary.CertifiedLevel != 0 {
		t.Fatalf("an uncovered scale must not earn certification: %+v", r)
	}
}
