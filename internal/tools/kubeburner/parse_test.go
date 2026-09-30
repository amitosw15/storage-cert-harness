package kubeburner

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/core"
	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/plan"
	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/registry"
)

func readFixtures(t *testing.T, names ...string) map[string][]byte {
	t.Helper()
	base := filepath.Join("testdata", "vmsnapshot-results")
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

func TestRegistrationProvidesTR027(t *testing.T) {
	integration, ok := registry.Get("kube-burner")
	if !ok {
		t.Fatal("kube-burner integration is not registered")
	}
	if !slices.Contains(integration.Provides, "TR-VIRT-027") {
		t.Fatalf("provides %v, want TR-VIRT-027", integration.Provides)
	}
	if _, ok := integration.Evaluators["TR-VIRT-027"]; !ok {
		t.Fatal("TR-VIRT-027 evaluator is not registered")
	}
}

func metricAt(res core.TestResult, name, pct string) (float64, bool) {
	for _, m := range res.Metrics {
		if m.Name == name && m.Percentile == pct {
			return m.Value, true
		}
	}
	return 0, false
}

func TestParseResults_Golden(t *testing.T) {
	data := readFixtures(t,
		"jobSummary.json",
		"volumeSnapshotLatencyQuantilesMeasurement-vmsnapshot-snapshot.json",
		"volumeSnapshotLatencyMeasurement-vmsnapshot-snapshot.json")
	res, err := parseResults("TR-VIRT-010", data, 3)
	if err != nil {
		t.Fatalf("ParseResults: %v", err)
	}
	if res.Native != core.OutcomePass {
		t.Errorf("native=%s, want pass", res.Native)
	}
	want := map[string]float64{"p50": 1, "p95": 1, "p99": 1, "max": 1}
	for pct, v := range want {
		got, ok := metricAt(res, MetricSnapshotReadyTime, pct)
		if !ok {
			t.Errorf("missing metric %s@%s", MetricSnapshotReadyTime, pct)
			continue
		}
		if got != v {
			t.Errorf("%s@%s=%v, want %v", MetricSnapshotReadyTime, pct, got, v)
		}
	}
	if res.Checks["jobs_passed"] != core.OutcomePass {
		t.Errorf("jobs_passed=%s, want pass", res.Checks["jobs_passed"])
	}
	for _, want := range []core.Metric{
		{Name: MetricSnapshotRequestedCount, Value: 3, Unit: "count"},
		{Name: MetricSnapshotReadyCount, Value: 3, Unit: "count"},
		{Name: MetricSnapshotFailedCount, Value: 0, Unit: "count"},
		{Name: MetricSnapshotSuccessRate, Value: 100, Unit: "%"},
		{Name: MetricSnapshotBatchCompletionTime, Value: 1, Unit: "s"},
	} {
		if !slices.Contains(res.Metrics, want) {
			t.Errorf("missing metric %+v in %+v", want, res.Metrics)
		}
	}
}

func TestParseResults_TR027ReportsSnapshotsPerVolume(t *testing.T) {
	data := readFixtures(t,
		"jobSummary.json",
		"volumeSnapshotLatencyQuantilesMeasurement-vmsnapshot-snapshot.json",
		"volumeSnapshotLatencyMeasurement-vmsnapshot-snapshot.json")
	res, err := parseResults("TR-VIRT-027", data, 3)
	if err != nil {
		t.Fatalf("parseResults: %v", err)
	}
	if !slices.Contains(res.Metrics, core.Metric{Name: MetricSnapshotsPerVolume, Value: 3, Unit: "count"}) {
		t.Fatalf("missing snapshots-per-volume metric in %+v", res.Metrics)
	}
}

func TestParseResults_TR027QuantilesOnlyUsesPassingJobCount(t *testing.T) {
	data := readFixtures(t,
		"jobSummary.json",
		"volumeSnapshotLatencyQuantilesMeasurement-vmsnapshot-snapshot.json")
	res, err := parseResults("TR-VIRT-027", data, 3)
	if err != nil {
		t.Fatalf("parseResults: %v", err)
	}
	if res.Native != core.OutcomePass {
		t.Fatalf("native=%s, want pass", res.Native)
	}
	for _, want := range []core.Metric{
		{Name: MetricSnapshotReadyCount, Value: 3, Unit: "count"},
		{Name: MetricSnapshotFailedCount, Value: 0, Unit: "count"},
		{Name: MetricSnapshotSuccessRate, Value: 100, Unit: "%"},
		{Name: MetricSnapshotsPerVolume, Value: 3, Unit: "count"},
	} {
		if !slices.Contains(res.Metrics, want) {
			t.Errorf("missing metric %+v in %+v", want, res.Metrics)
		}
	}
}

func TestParseResults_NativeFail(t *testing.T) {
	data := readFixtures(t,
		"volumeSnapshotLatencyQuantilesMeasurement-vmsnapshot-snapshot.json",
		"volumeSnapshotLatencyMeasurement-vmsnapshot-snapshot.json")
	data["jobSummary.json"] = readFixtures(t, "jobSummary-fail.json")["jobSummary-fail.json"]
	res, err := parseResults("TR-VIRT-010", data, 5)
	if err != nil {
		t.Fatalf("ParseResults: %v", err)
	}
	if res.Native != core.OutcomeFail {
		t.Errorf("native=%s, want fail", res.Native)
	}
	for _, want := range []core.Metric{
		{Name: MetricSnapshotReadyCount, Value: 3, Unit: "count"},
		{Name: MetricSnapshotFailedCount, Value: 2, Unit: "count"},
		{Name: MetricSnapshotSuccessRate, Value: 60, Unit: "%"},
	} {
		if !slices.Contains(res.Metrics, want) {
			t.Errorf("missing metric %+v in %+v", want, res.Metrics)
		}
	}
}

func TestEvaluator_NativeFailureFailsBatchVerdict(t *testing.T) {
	data := readFixtures(t, "volumeSnapshotLatencyMeasurement-vmsnapshot-snapshot.json")
	data["jobSummary.json"] = []byte(`[
  {"metricName":"jobSummary","elapsedTime":1,"passed":true,"jobConfig":{"name":"vmsnapshot-provision"}},
  {"metricName":"jobSummary","elapsedTime":1,"passed":true,"jobConfig":{"name":"vmsnapshot-snapshot-1"}},
  {"metricName":"jobSummary","elapsedTime":1,"passed":false,"jobConfig":{"name":"vmsnapshot-snapshot-2"}}
]`)
	res, err := parseResults("TR-VIRT-010", data, 5)
	if err != nil {
		t.Fatalf("ParseResults: %v", err)
	}
	verdicts, err := snapshotEvaluator{}.Evaluate(context.Background(), core.TestRequirement{
		ID: "TR-VIRT-010",
		SLAs: []core.SLA{
			{Metric: MetricSnapshotReadyTime, Operator: "<=", Value: ptr(30), Unit: "s", Percentile: "p99"},
		},
	}, res)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(verdicts) != 2 || verdicts[0].Item != "native:jobs_passed" || verdicts[0].Outcome != core.OutcomeFail {
		t.Fatalf("got %+v, want failed batch verdict with SLA result", verdicts)
	}
}

func TestParseResults_MissingJobSummary(t *testing.T) {
	if _, err := ParseResults("TR-VIRT-010", map[string][]byte{}); err == nil {
		t.Fatal("expected error on missing jobSummary.json")
	}
}

func TestParseResults_RequiresSnapshotJobSummary(t *testing.T) {
	data := map[string][]byte{
		"jobSummary.json": []byte(`[{"metricName":"jobSummary","elapsedTime":1,"passed":true,"jobConfig":{"name":"vmsnapshot-provision"}}]`),
	}
	if _, err := parseResults("TR-VIRT-010", data, 1); err == nil {
		t.Fatal("expected error when snapshot job summary is missing")
	}
}

func TestParseResults_RequiresSnapshotElapsedTime(t *testing.T) {
	data := map[string][]byte{
		"jobSummary.json": []byte(`[{"metricName":"jobSummary","passed":true,"jobConfig":{"name":"vmsnapshot-snapshot-1"}}]`),
	}
	if _, err := parseResults("TR-VIRT-010", data, 1); err == nil {
		t.Fatal("expected error when snapshot elapsedTime is missing")
	}
}

func TestParseResults_RejectsMalformedSnapshotMeasurement(t *testing.T) {
	data := readFixtures(t, "jobSummary.json")
	data["volumeSnapshotLatencyMeasurement-bad.json"] = []byte(`{`)
	if _, err := parseResults("TR-VIRT-010", data, 1); err == nil || !strings.Contains(err.Error(), "volumeSnapshotLatencyMeasurement-bad.json") {
		t.Fatalf("got error %v, want malformed measurement filename", err)
	}
}

func TestParseResults_RejectsMoreReadySnapshotsThanRequested(t *testing.T) {
	data := readFixtures(t, "jobSummary.json")
	data["volumeSnapshotLatencyMeasurement-extra.json"] = []byte(`[
  {"vsName":"vmsnap-1","vsReadyLatency":1},
  {"vsName":"vmsnap-2","vsReadyLatency":1}
]`)
	if _, err := parseResults("TR-VIRT-010", data, 1); err == nil || !strings.Contains(err.Error(), "more than 1 requested") {
		t.Fatalf("got error %v, want extra ready snapshot error", err)
	}
}

func TestParseResults_AggregatesSnapshotBatchElapsedTime(t *testing.T) {
	data := map[string][]byte{
		"jobSummary.json": []byte(`[
  {"metricName":"jobSummary","elapsedTime":1,"passed":true,"jobConfig":{"name":"vmsnapshot-provision"}},
  {"metricName":"jobSummary","elapsedTime":1,"passed":true,"jobConfig":{"name":"vmsnapshot-snapshot-1"}},
  {"metricName":"jobSummary","elapsedTime":2,"passed":true,"jobConfig":{"name":"vmsnapshot-snapshot-2"}}
]`),
		"volumeSnapshotLatencyMeasurement-vmsnapshot-snapshot-1.json": []byte(`[
  {"vsName":"vmsnap-1-1","vsReadyLatency":1},
  {"vsName":"vmsnap-1-2","vsReadyLatency":1},
  {"vsName":"vmsnap-1-3","vsReadyLatency":1}
]`),
		"volumeSnapshotLatencyMeasurement-vmsnapshot-snapshot-2.json": []byte(`[
  {"vsName":"vmsnap-2-1","vsReadyLatency":1},
  {"vsName":"vmsnap-2-2","vsReadyLatency":1}
]`),
	}
	res, err := parseResults("TR-VIRT-010", data, 5)
	if err != nil {
		t.Fatalf("ParseResults: %v", err)
	}
	if elapsed, ok := metricAt(res, MetricSnapshotBatchCompletionTime, ""); !ok || elapsed != 3 {
		t.Errorf("snapshot batch completion time = %v, %t; want 3, true", elapsed, ok)
	}
	for _, want := range []core.Metric{
		{Name: MetricSnapshotReadyCount, Value: 5, Unit: "count"},
		{Name: MetricSnapshotFailedCount, Value: 0, Unit: "count"},
		{Name: MetricSnapshotSuccessRate, Value: 100, Unit: "%"},
	} {
		if !slices.Contains(res.Metrics, want) {
			t.Errorf("missing metric %+v in %+v", want, res.Metrics)
		}
	}
}

func TestParseResults_AggregatesSnapshotReadyQuantilesAcrossBatches(t *testing.T) {
	data := map[string][]byte{
		"jobSummary.json": []byte(`[
  {"metricName":"jobSummary","elapsedTime":1,"passed":true,"jobConfig":{"name":"vmsnapshot-snapshot-1"}},
  {"metricName":"jobSummary","elapsedTime":1,"passed":true,"jobConfig":{"name":"vmsnapshot-snapshot-2"}}
]`),
		"volumeSnapshotLatencyQuantilesMeasurement-vmsnapshot-snapshot-1.json": []byte(`[
  {"quantileName":"Ready","P50":1,"P95":1,"P99":1,"max":1}
]`),
		"volumeSnapshotLatencyQuantilesMeasurement-vmsnapshot-snapshot-2.json": []byte(`[
  {"quantileName":"Ready","P50":10,"P95":10,"P99":10,"max":10}
]`),
		"volumeSnapshotLatencyMeasurement-vmsnapshot-snapshot-1.json": []byte(`[
  {"vsName":"vmsnap-1-1","vsReadyLatency":1},
  {"vsName":"vmsnap-1-2","vsReadyLatency":1},
  {"vsName":"vmsnap-1-3","vsReadyLatency":1},
  {"vsName":"vmsnap-1-4","vsReadyLatency":1},
  {"vsName":"vmsnap-1-5","vsReadyLatency":1}
]`),
		"volumeSnapshotLatencyMeasurement-vmsnapshot-snapshot-2.json": []byte(`[
  {"vsName":"vmsnap-2-1","vsReadyLatency":10},
  {"vsName":"vmsnap-2-2","vsReadyLatency":10},
  {"vsName":"vmsnap-2-3","vsReadyLatency":10},
  {"vsName":"vmsnap-2-4","vsReadyLatency":10},
  {"vsName":"vmsnap-2-5","vsReadyLatency":10}
]`),
	}
	res, err := parseResults("TR-VIRT-010", data, 10)
	if err != nil {
		t.Fatalf("ParseResults: %v", err)
	}
	want := map[string]float64{"p50": 1, "p95": 10, "p99": 10, "max": 10}
	for percentile, value := range want {
		got, ok := metricAt(res, MetricSnapshotReadyTime, percentile)
		if !ok || got != value {
			t.Errorf("snapshot_ready_time@%s = %v, %t; want %v, true", percentile, got, ok, value)
		}
	}
	for _, percentile := range []string{"p50", "p95", "p99", "max"} {
		count := 0
		for _, metric := range res.Metrics {
			if metric.Name == MetricSnapshotReadyTime && metric.Percentile == percentile {
				count++
			}
		}
		if count != 1 {
			t.Errorf("snapshot_ready_time@%s count = %d, want 1", percentile, count)
		}
	}
}

func TestPreflight_RequiresStorageClass(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	rc := &core.RunCtx{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	findings, err := (preflight{}).Check(context.Background(), rc, core.NewBag(), []core.TestRequirement{{ID: "TR-VIRT-010", Params: map[string]any{"replicas": 1, "snapshot_count": 1}}})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(findings) != 8 || findings[1].Level != "error" || !strings.Contains(findings[1].Message, "storage_class required") {
		t.Fatalf("got %#v, want missing storage class error and all independent checks", findings)
	}
}

func TestParams_ReplicaAndSnapshotCounts(t *testing.T) {
	p := resolveParams([]core.TestRequirement{{Params: map[string]any{"replicas": 5, "snapshot_count": 3}}})
	if p.Replicas != 5 || p.SnapshotCount != 3 {
		t.Fatalf("params = %+v, want replicas=5 snapshot_count=3", p)
	}
	if err := p.validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	for _, p := range []Params{{Replicas: 0, SnapshotCount: 1}, {Replicas: 1, SnapshotCount: 0}, {Replicas: -1, SnapshotCount: 1}, {Replicas: 1, SnapshotCount: -1}} {
		if err := p.validate(); err == nil {
			t.Errorf("validate(%+v) succeeded, want error", p)
		}
	}
}

func TestParams_SnapshotCountDefaultsToReplicas(t *testing.T) {
	p := resolveParams([]core.TestRequirement{{Params: map[string]any{"replicas": 3}}})
	if p.Replicas != 3 || p.SnapshotCount != 3 || p.validate() != nil {
		t.Fatalf("replica default = %+v, want valid replicas=3 snapshot_count=3", p)
	}
}

func TestParams_TR027UsesCanonicalSnapshotNames(t *testing.T) {
	p := resolveParams([]core.TestRequirement{{Params: map[string]any{
		"replicas":       1,
		"snapshot_count": 250,
	}}})
	if p.Replicas != 1 || p.SnapshotCount != 250 || !p.snapshotCountSet {
		t.Fatalf("params = %+v, want replicas=1 snapshot_count=250", p)
	}
	if err := p.validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestParams_TR027RequiresSingleSourceVolume(t *testing.T) {
	p := resolveParams([]core.TestRequirement{{Params: map[string]any{
		"replicas":       2,
		"snapshot_count": 250,
	}}})
	if err := p.validateFor("TR-VIRT-027"); err == nil {
		t.Fatal("TR-VIRT-027 accepted replicas=2, want single-source rejection")
	}
}

func TestParams_TR027AllowsExplicitZeroSnapshotReducedMode(t *testing.T) {
	p := Params{Replicas: 1, SnapshotCount: 0}
	if err := p.validateFor("TR-VIRT-027"); err != nil {
		t.Fatalf("TR-VIRT-027 zero-snapshot mode rejected: %v", err)
	}
	if err := p.validateFor("TR-VIRT-010"); err == nil {
		t.Fatal("TR-VIRT-010 zero-snapshot mode succeeded, want error")
	}
}

func TestParseResults_TR027ZeroSnapshotReducedModeFailsSLAExplicitly(t *testing.T) {
	data := map[string][]byte{
		"jobSummary.json": []byte(`[{"metricName":"jobSummary","elapsedTime":1,"passed":true,"jobConfig":{"name":"vmsnapshot-provision"}}]`),
	}
	res, err := parseResults("TR-VIRT-027", data, 0)
	if err != nil {
		t.Fatalf("parseResults: %v", err)
	}
	if res.Native != core.OutcomeFail {
		t.Fatalf("native outcome = %q, want fail", res.Native)
	}
	if len(res.Metrics) != 1 || res.Metrics[0].Name != MetricSnapshotsPerVolume || res.Metrics[0].Value != 0 {
		t.Fatalf("metrics = %+v, want snapshots_per_volume=0", res.Metrics)
	}
}

func TestBuildEnv_UsesReplicas(t *testing.T) {
	env := buildEnv(Params{Replicas: 5, SnapshotCount: 3}, "example-storage")
	for _, want := range []string{"REPLICAS=5"} {
		if !slices.Contains(env, want) {
			t.Errorf("missing %q in %v", want, env)
		}
	}
}

func TestBuildRunCmd_UsesHostBinaryRegardlessOfEnvironment(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "kube-burner")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write kube-burner: %v", err)
	}
	t.Setenv("PATH", dir)
	cmd, err := buildRunCmd(context.Background(), &core.RunCtx{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, nil, dir, "TR-VIRT-010")
	if err != nil {
		t.Fatalf("buildRunCmd: %v", err)
	}
	if filepath.Base(cmd.Path) != "kube-burner" {
		t.Fatalf("command path = %q, want kube-burner", cmd.Path)
	}
	if got, want := strings.Join(cmd.Args, " "), binary+" init -c "+filepath.Join(dir, "TR-VIRT-010", "config.yaml"); got != want {
		t.Errorf("command args = %q, want %q", got, want)
	}
}

func TestTeardown_DeletesNamespaceAfterCancellation(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "args")
	t.Setenv("TEST_OUTPUT", output)
	if err := os.WriteFile(filepath.Join(dir, "oc"), []byte("#!/bin/sh\nprintf '%s' \"$*\" > \"$TEST_OUTPUT\"\n"), 0o755); err != nil {
		t.Fatalf("write oc: %v", err)
	}
	t.Setenv("PATH", dir)

	bag := core.NewBag()
	bag.Set("params", Params{NS: "storage-cert-smoke"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := (teardown{}).Teardown(ctx, &core.RunCtx{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, bag); err != nil {
		t.Fatalf("Teardown: %v", err)
	}
	got, err := os.ReadFile(output)
	if err != nil {
		t.Fatalf("read oc args: %v", err)
	}
	if string(got) != "delete ns storage-cert-smoke --wait=false --ignore-not-found" {
		t.Errorf("oc args = %q, want delete after cancellation", got)
	}
}

func TestSnapshotJobs_SplitsPartialFinalBatch(t *testing.T) {
	jobs := snapshotJobs(Params{Replicas: 2, SnapshotCount: 3, snapshotCountSet: true})
	for _, want := range []string{
		"name: vmsnapshot-snapshot-1",
		"name: vmsnapshot-snapshot-2",
		"replicas: 2",
		"replicas: 1",
		"snapshotBatch: 1",
		"snapshotBatch: 2",
		"waitWhenFinished: true",
	} {
		if !strings.Contains(jobs, want) {
			t.Errorf("missing %q in generated snapshot jobs:\n%s", want, jobs)
		}
	}
}

func TestSnapshotPlansUseReplicaAndSnapshotCounts(t *testing.T) {
	for _, tc := range []struct {
		name          string
		replicas      int
		snapshotCount int
	}{
		{name: "tr-virt-010-vm-snapshot-smoke.yaml", replicas: 2, snapshotCount: 3},
		{name: "tr-virt-010-vm-snapshot-scale.yaml", replicas: 25, snapshotCount: 50},
	} {
		p, err := plan.Load(filepath.Join("..", "..", "..", "plans", tc.name))
		if err != nil {
			t.Fatalf("load %s: %v", tc.name, err)
		}
		params := p.Overrides["TR-VIRT-010"]
		replicas, _ := core.AsInt(params["replicas"])
		snapshotCount, _ := core.AsInt(params["snapshot_count"])
		if replicas != tc.replicas || snapshotCount != tc.snapshotCount {
			t.Errorf("%s counts = replicas=%d snapshot_count=%d, want %d and %d", tc.name, replicas, snapshotCount, tc.replicas, tc.snapshotCount)
		}
		if err := resolveParams([]core.TestRequirement{{Params: params}}).validate(); err != nil {
			t.Errorf("%s has invalid counts: %v", tc.name, err)
		}
	}
}

func ptr(f float64) *float64 { return &f }

func TestEvaluator_GradesP99(t *testing.T) {
	res, err := parseResults("TR-VIRT-010", readFixtures(t,
		"jobSummary.json",
		"volumeSnapshotLatencyQuantilesMeasurement-vmsnapshot-snapshot.json",
		"volumeSnapshotLatencyMeasurement-vmsnapshot-snapshot.json"), 3)
	if err != nil {
		t.Fatalf("ParseResults: %v", err)
	}
	tr := core.TestRequirement{
		ID:             "TR-VIRT-010",
		AutomationTool: "kube-burner",
		SLAStatus:      core.StatusDefined,
		SLAs: []core.SLA{
			{Metric: MetricSnapshotReadyTime, Unit: "s", Percentile: "p99", MeasuredBy: []string{"kube-burner"}},
		},
		Bars: []core.SLA{
			{Metric: MetricSnapshotReadyTime, Operator: "<=", Value: ptr(30), Unit: "s", Percentile: "p99"},
		},
	}
	vs, err := snapshotEvaluator{}.Evaluate(context.Background(), tr, res)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(vs) != 2 {
		t.Fatalf("got %d verdicts, want 2", len(vs))
	}
	// 1ms p99 <= 30s → pass (unit-normalized).
	if vs[1].Outcome != core.OutcomePass {
		t.Errorf("verdict=%s (%s), want pass", vs[1].Outcome, vs[1].Reason)
	}
	if vs[0].Item != "native:jobs_passed" ||
		vs[0].Actual != "requested=3 ready=3 failed=0 success_rate=100% batch_completion_time=1s" {
		t.Errorf("batch verdict = %+v, want measured batch summary", vs[0])
	}
}

func TestEvaluator_NativeWhenNoSLA(t *testing.T) {
	res, _ := parseResults("TR-VIRT-010", readFixtures(t,
		"jobSummary.json",
		"volumeSnapshotLatencyQuantilesMeasurement-vmsnapshot-snapshot.json",
		"volumeSnapshotLatencyMeasurement-vmsnapshot-snapshot.json"), 3)
	tr := core.TestRequirement{ID: "TR-VIRT-010"}
	vs, err := snapshotEvaluator{}.Evaluate(context.Background(), tr, res)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(vs) != 1 || vs[0].Item != "native:jobs_passed" || vs[0].Outcome != core.OutcomePass {
		t.Errorf("got %+v, want single native:jobs_passed pass", vs)
	}
}

func TestEvaluator_SkipsUnmeasurableSLA(t *testing.T) {
	res, _ := parseResults("TR-VIRT-010", readFixtures(t,
		"jobSummary.json",
		"volumeSnapshotLatencyQuantilesMeasurement-vmsnapshot-snapshot.json",
		"volumeSnapshotLatencyMeasurement-vmsnapshot-snapshot.json"), 3)
	tr := core.TestRequirement{
		ID:        "TR-VIRT-010",
		SLAStatus: core.StatusDefined,
		SLAs: []core.SLA{
			{Metric: "iops_latency", Operator: "<=", Value: ptr(5), Unit: "ms", Percentile: "p99"},
		},
	}
	vs, err := snapshotEvaluator{}.Evaluate(context.Background(), tr, res)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if len(vs) != 2 || vs[0].Outcome != core.OutcomePass || vs[1].Outcome != core.OutcomeSkip {
		t.Errorf("got %+v, want batch pass and SLA skip", vs)
	}
}

func TestWriteConfig_EmbedsAssets(t *testing.T) {
	dir := t.TempDir()
	if _, err := writeConfig(dir, Params{Replicas: 2, SnapshotCount: 3, snapshotCountSet: true}); err != nil {
		t.Fatalf("writeConfig: %v", err)
	}
	for _, f := range []string{"config.yaml", "templates/vm.yaml", "templates/vmsnapshot.yaml", "templates/vmsnapshot-batch.yaml"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("missing embedded asset %s: %v", f, err)
		}
	}
	config, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(config), "replicas: {{.REPLICAS}}") || strings.Count(string(config), "name: vmsnapshot-snapshot-") != 2 || !strings.Contains(string(config), "replicas: 2") || !strings.Contains(string(config), "replicas: 1") || !strings.Contains(string(config), "snapshotBatch: 1") || !strings.Contains(string(config), "snapshotBatch: 2") {
		t.Errorf("config does not preserve replicas and use snapshot batches:\n%s", config)
	}
	template, err := os.ReadFile(filepath.Join(dir, "templates", "vmsnapshot-batch.yaml"))
	if err != nil {
		t.Fatalf("read snapshot template: %v", err)
	}
	if !strings.Contains(string(template), "name: vmsnap-{{.snapshotBatch}}-{{.Replica}}") || !strings.Contains(string(template), "name: vm-{{.Replica}}") {
		t.Errorf("snapshot template does not use unique batch names and matching VM names:\n%s", template)
	}
}

func TestWriteConfig_PreservesLegacySnapshotJob(t *testing.T) {
	dir := t.TempDir()
	p := resolveParams([]core.TestRequirement{{Params: map[string]any{"replicas": 2}}})
	if _, err := writeConfig(dir, p); err != nil {
		t.Fatalf("writeConfig: %v", err)
	}
	config, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(config), "name: vmsnapshot-snapshot\n") || !strings.Contains(string(config), "objectTemplate: templates/vmsnapshot.yaml") || !strings.Contains(string(config), "replicas: {{.REPLICAS}}") || strings.Contains(string(config), "snapshotBatch:") {
		t.Errorf("config does not preserve the legacy snapshot job:\n%s", config)
	}
	template, err := os.ReadFile(filepath.Join(dir, "templates", "vmsnapshot.yaml"))
	if err != nil {
		t.Fatalf("read snapshot template: %v", err)
	}
	if !strings.Contains(string(template), "name: vmsnap-{{.Replica}}") || strings.Contains(string(template), "snapshotBatch") {
		t.Errorf("template does not preserve legacy snapshot names:\n%s", template)
	}
}
