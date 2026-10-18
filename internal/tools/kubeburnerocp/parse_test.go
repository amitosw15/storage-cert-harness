package kubeburnerocp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/core"
	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/registry"
)

func readFixtures(t *testing.T, workload string, names ...string) map[string][]byte {
	t.Helper()
	base := filepath.Join("testdata", workload+"-results")
	data := map[string][]byte{}
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(base, n))
		if err != nil {
			t.Fatalf("read %s: %v", n, err)
		}
		data[n] = b
	}
	return data
}

func p99s(res core.TestResult) map[string]float64 {
	out := map[string]float64{}
	for _, m := range res.Metrics {
		if m.Percentile == "p99" {
			out[m.Name] = m.Value
		}
	}
	return out
}

func TestParseResults_PVCDensity_Golden(t *testing.T) {
	data := readFixtures(t, "pvc-density",
		"jobSummary.json",
		"pvcLatencyQuantilesMeasurement-pvc-density.json",
		"podLatencyQuantilesMeasurement-pvc-density.json")
	res, err := ParseResults(WorkloadPVCDensity, "TR-STOR-006", data)
	if err != nil {
		t.Fatalf("ParseResults: %v", err)
	}
	if res.Native != core.OutcomePass {
		t.Errorf("native=%s, want pass", res.Native)
	}
	want := map[string]float64{"pvcLatency_Bound": 1000, "podLatency_Ready": 1000}
	got := p99s(res)
	for name, v := range want {
		if got[name] != v {
			t.Errorf("%s p99=%v, want %v", name, got[name], v)
		}
	}
}

func TestParseResults_PVCDensity_NativeFail(t *testing.T) {
	data := readFixtures(t, "pvc-density",
		"pvcLatencyQuantilesMeasurement-pvc-density.json",
		"podLatencyQuantilesMeasurement-pvc-density.json")
	data["jobSummary.json"] = mustRead(t, "pvc-density", "jobSummary-fail.json")
	res, err := ParseResults(WorkloadPVCDensity, "TR-STOR-006", data)
	if err != nil {
		t.Fatalf("ParseResults: %v", err)
	}
	if res.Native != core.OutcomeFail {
		t.Errorf("native=%s, want fail", res.Native)
	}
}

func TestParseResults_VirtParallel_Golden(t *testing.T) {
	data := readFixtures(t, "virt-parallel",
		"jobSummary.json",
		"vmiLatencyQuantilesMeasurement-virt-parallel-create-vms-0.json")
	res, err := ParseResults(WorkloadVirtParallel, "TR-VIRT-019", data)
	if err != nil {
		t.Fatalf("ParseResults: %v", err)
	}
	if res.Native != core.OutcomePass {
		t.Errorf("native=%s, want pass", res.Native)
	}
	if res.Checks[virtParallelCheck] != core.OutcomeSkip {
		t.Fatalf("%s=%v, want skip for a two-VM smoke run", virtParallelCheck, res.Checks[virtParallelCheck])
	}
	want := map[string]float64{"vmiLatency_VMReady": 10}
	got := p99s(res)
	for name, v := range want {
		if got[name] != v {
			t.Errorf("%s p99=%v, want %v", name, got[name], v)
		}
	}
	var lifecycle float64
	for _, m := range res.Metrics {
		if m.Name == virtParallelMetric {
			lifecycle = m.Value
		}
	}
	if lifecycle != 128 {
		t.Errorf("%s=%v, want 128", virtParallelMetric, lifecycle)
	}
}

func TestParseResults_VirtParallelFailedGolden(t *testing.T) {
	data := map[string][]byte{"jobSummary.json": mustRead(t, "virt-parallel", "jobSummary-fail.json")}
	res, err := ParseResults(WorkloadVirtParallel, "TR-VIRT-019", data)
	if err != nil {
		t.Fatalf("ParseResults: %v", err)
	}
	if res.Native != core.OutcomeFail {
		t.Fatalf("failed result = native %q checks %+v", res.Native, res.Checks)
	}
	if res.Checks[virtParallelCheck] != core.OutcomeSkip {
		t.Fatalf("%s=%v, want skip for a two-VM smoke run", virtParallelCheck, res.Checks[virtParallelCheck])
	}
	if got := res.Metrics[0].Value; got != 128 {
		t.Fatalf("lifecycle metric = %v, want 128", got)
	}
}

func TestParseResults_VirtParallelFullScaleCheck(t *testing.T) {
	data := readFixtures(t, "virt-parallel", "jobSummary.json")
	var summaries []jobSummary
	if err := json.Unmarshal(data["jobSummary.json"], &summaries); err != nil {
		t.Fatal(err)
	}
	for i := range summaries {
		summaries[i].WorkloadFlags["initialVms"] = "20"
	}
	data["jobSummary.json"], _ = json.Marshal(summaries)

	res, err := ParseResults(WorkloadVirtParallel, "TR-VIRT-019", data)
	if err != nil {
		t.Fatalf("ParseResults: %v", err)
	}
	if res.Checks[virtParallelCheck] != core.OutcomePass {
		t.Fatalf("%s=%s, want pass", virtParallelCheck, res.Checks[virtParallelCheck])
	}
}

func mustRead(t *testing.T, workload, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", workload+"-results", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return b
}

// synthetic gates and scale values exercise grading, not certification limits.
func TestPVCDensityCatalogGrading(t *testing.T) {
	data := readFixtures(t, "pvc-density", "jobSummary.json", "pvcLatencyQuantilesMeasurement-pvc-density.json")
	// fake unit-test gate in seconds.
	gate := 2.0
	tr := core.TestRequirement{ID: "TR-STOR-006", AutomationTool: "kube-burner-ocp pvc-density",
		// fixture workload size and illustrative catalog default.
		Params: map[string]any{"iterations": 5}, ParamSpec: map[string]core.ParamSpec{"iterations": {Default: 10}},
		SLAs: []core.SLA{{Metric: "pvc_bind_latency", Percentile: "p99", Unit: "s"}},
		// fake gate applicability matches the fixture.
		Bars:   []core.SLA{{Metric: "pvc_bind_latency", Percentile: "p99", Unit: "s", Operator: "<=", Value: &gate, When: map[string]any{"iterations": 5}}},
		Checks: []core.Check{{ID: pvcBoundCheck, Kind: "suite"}}}
	for _, tc := range []struct {
		name               string
		gate               float64
		failedJob          bool
		wantSLA, wantCheck core.Outcome
	}{
		// synthetic pass/fail boundaries around the fixture's one-second latency.
		{"pass", 2, false, core.OutcomePass, core.OutcomePass},
		// synthetic fail boundary.
		{"latency failure", 0.5, false, core.OutcomeFail, core.OutcomePass},
		// synthetic successful latency with failed workload.
		{"job failure", 2, true, core.OutcomePass, core.OutcomeFail},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gate = tc.gate
			data["jobSummary.json"] = mustRead(t, "pvc-density", "jobSummary.json")
			if tc.failedJob {
				data["jobSummary.json"] = mustRead(t, "pvc-density", "jobSummary-fail.json")
			}
			res, err := ParseResults(WorkloadPVCDensity, tr.ID, data)
			if err != nil {
				t.Fatal(err)
			}
			tool, _ := registry.Get("kube-burner-ocp")
			vs, err := tool.Evaluators[tr.ID].Evaluate(context.Background(), tr, res)
			if err != nil {
				t.Fatal(err)
			}
			if vs[0].Outcome != tc.wantSLA || vs[1].Outcome != tc.wantCheck {
				t.Fatalf("unexpected verdicts: %+v", vs)
			}
		})
	}
}

func TestPVCDensityRequiresVerifiedWait(t *testing.T) {
	data := readFixtures(t, "pvc-density", "jobSummary.json")
	var summaries []jobSummary
	if err := json.Unmarshal(data["jobSummary.json"], &summaries); err != nil {
		t.Fatal(err)
	}
	summaries[0].JobConfig.WaitWhenFinished = false
	data["jobSummary.json"], _ = json.Marshal(summaries)
	res, err := ParseResults(WorkloadPVCDensity, "TR-STOR-006", data)
	if err != nil {
		t.Fatal(err)
	}
	if res.Checks[pvcBoundCheck] != core.OutcomeFail {
		t.Fatal("unchecked readiness must not pass")
	}
}
