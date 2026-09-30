package virtbench

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/core"
	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/plan"
)

// update regenerates the golden file: go test ./internal/tools/virtbench -update
var update = flag.Bool("update", false, "update golden files")

// TestParseSummary is the golden-file parser test: feed the virtbench summary
// fixture, parse, compare normalized output to a golden file. No cluster or
// binary required (see decisions/0003, 0006).
func TestParseSummary(t *testing.T) {
	in, err := os.ReadFile(filepath.Join("testdata", SummaryFileName))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	got, err := ParseSummary(in, "TR-VIRT-002")
	if err != nil {
		t.Fatalf("ParseSummary: %v", err)
	}
	gotJSON, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	goldenPath := filepath.Join("testdata", "parse.golden.json")
	if *update {
		if err := os.WriteFile(goldenPath, append(gotJSON, '\n'), 0o644); err != nil {
			t.Fatalf("update golden: %v", err)
		}
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if strings.TrimSpace(string(gotJSON)) != strings.TrimSpace(string(want)) {
		t.Errorf("parsed output differs from golden:\n got: %s\nwant: %s", gotJSON, want)
	}
}

// TestParseSummaryMetricNamesMatchKB guards the contract that the emitted base
// metric names equal the KB SLA metric names for TR-VIRT-002 — otherwise the
// grader's findMetric can't match the measured value to its SLA.
func TestParseSummaryMetricNamesMatchKB(t *testing.T) {
	in, _ := os.ReadFile(filepath.Join("testdata", SummaryFileName))
	got, err := ParseSummary(in, "TR-VIRT-002")
	if err != nil {
		t.Fatalf("ParseSummary: %v", err)
	}
	names := map[string]bool{}
	for _, m := range got[0].Metrics {
		names[m.Name] = true
	}
	for _, want := range []string{"clone_duration", "time_to_running", "time_to_ping"} {
		if !names[want] {
			t.Errorf("missing SLA-scored metric %q; got %v", want, names)
		}
	}
	if got[0].Native != core.OutcomePass {
		t.Errorf("native = %q, want pass (fixture has 0 failed VMs)", got[0].Native)
	}
}

// goldenParse runs a pure parser against a fixture and compares to a golden file,
// regenerating with -update. It is the shared harness for every scenario parser.
func goldenParse(t *testing.T, parse func([]byte, string) ([]core.TestResult, error), fixture, golden, trID string) {
	t.Helper()
	in, err := os.ReadFile(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	got, err := parse(in, trID)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	gotJSON, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	goldenPath := filepath.Join("testdata", golden)
	if *update {
		if err := os.WriteFile(goldenPath, append(gotJSON, '\n'), 0o644); err != nil {
			t.Fatalf("update golden: %v", err)
		}
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if strings.TrimSpace(string(gotJSON)) != strings.TrimSpace(string(want)) {
		t.Errorf("parsed output differs from golden:\n got: %s\nwant: %s", gotJSON, want)
	}
}

func TestParseBootStorm(t *testing.T) {
	goldenParse(t, ParseBootStorm, BootStormFileName, "parse_bootstorm.golden.json", "TR-VIRT-001")
}

// TestParseBootStormEmitsVMBootTime guards the documented mapping vm_boot_time ←
// total_test_duration_sec (the whole-storm wall-clock the KB gate targets).
func TestParseBootStormEmitsVMBootTime(t *testing.T) {
	in, _ := os.ReadFile(filepath.Join("testdata", BootStormFileName))
	got, err := ParseBootStorm(in, "TR-VIRT-001")
	if err != nil {
		t.Fatalf("ParseBootStorm: %v", err)
	}
	var vmBoot, total *core.Metric
	for i := range got[0].Metrics {
		switch got[0].Metrics[i].Name {
		case "vm_boot_time":
			vmBoot = &got[0].Metrics[i]
		case "total_test_duration":
			total = &got[0].Metrics[i]
		}
	}
	if vmBoot == nil || total == nil {
		t.Fatalf("want both vm_boot_time and total_test_duration; got %+v", got[0].Metrics)
	}
	if vmBoot.Value != total.Value || vmBoot.Unit != "s" {
		t.Errorf("vm_boot_time = %v %s, want %v s", vmBoot.Value, vmBoot.Unit, total.Value)
	}
}

func TestParseDiskOps(t *testing.T) {
	goldenParse(t, ParseDiskOps, DiskOpsFileName, "parse_diskops.golden.json", "TR-STOR-001")
}

// TestParseDiskOpsMetricNamesMatchKB guards the pvc_attach_time mapping.
func TestParseDiskOpsMetricNamesMatchKB(t *testing.T) {
	in, _ := os.ReadFile(filepath.Join("testdata", DiskOpsFileName))
	got, err := ParseDiskOps(in, "TR-STOR-001")
	if err != nil {
		t.Fatalf("ParseDiskOps: %v", err)
	}
	names := map[string]bool{}
	for _, m := range got[0].Metrics {
		names[m.Name] = true
	}
	if !names["pvc_attach_time"] {
		t.Errorf("missing SLA-scored metric pvc_attach_time; got %v", names)
	}
}

func TestParseDiskOpsErrors(t *testing.T) {
	if _, err := ParseDiskOps(nil, "TR-STOR-001"); err == nil {
		t.Error("empty payload: want error, got nil")
	}
	if _, err := ParseDiskOps([]byte(`{"summary":{"total_vms":1}}`), "TR-STOR-001"); err == nil {
		t.Error("no hotplug section: want error, got nil")
	}
}

func TestParseFIO(t *testing.T) {
	goldenParse(t, ParseFIO, FIOFileName, "parse_fio.golden.json", "TR-STOR-002")
}

func TestParseFIOFleet(t *testing.T) {
	goldenParse(t, ParseFIO, "fio_fleet.json", "parse_fio_fleet.golden.json", "TR-STOR-002")
}

func TestParseFIOErrors(t *testing.T) {
	if _, err := ParseFIO(nil, "TR-STOR-002"); err == nil {
		t.Error("empty payload: want error, got nil")
	}
	if _, err := ParseFIO([]byte(`{"jobs":[]}`), "TR-STOR-002"); err == nil {
		t.Error("no jobs: want error, got nil")
	}
	if _, err := ParseFIO([]byte(`{"summary":{"total_vms":2,"successful":2,"failed":0},"results":[{"jobs":[]}]}`), "TR-STOR-002"); err == nil {
		t.Error("missing fleet result: want error, got nil")
	}
	got, err := ParseFIO([]byte(`{"summary":{"total_vms":2,"successful":1,"failed":1},"results":[{"jobs":[]}]}`), "TR-STOR-002")
	if err != nil {
		t.Fatalf("failed fleet: %v", err)
	}
	if got[0].Native != core.OutcomeFail {
		t.Errorf("failed fleet native = %q, want fail", got[0].Native)
	}
}

func TestCollectFIOResults(t *testing.T) {
	root := t.TempDir()
	raw, err := os.ReadFile(filepath.Join("testdata", FIOFileName))
	if err != nil {
		t.Fatalf("read raw fixture: %v", err)
	}
	for _, namespace := range []string{"fio-1", "fio-2"} {
		dir := filepath.Join(root, "per-vm-results", namespace)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("make result dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, FIOFileName), raw, 0o644); err != nil {
			t.Fatalf("write raw fixture: %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, FIOSummaryFileName), []byte(`{"total_vms":2,"successful":2,"failed":0}`), 0o644); err != nil {
		t.Fatalf("write summary: %v", err)
	}
	refs, data, err := collectFIOResults(root, false)
	if err != nil {
		t.Fatalf("collect FIO results: %v", err)
	}
	if len(refs) != 3 {
		t.Errorf("collected references = %d, want 3", len(refs))
	}
	got, err := ParseFIO(data, "TR-STOR-002")
	if err != nil {
		t.Fatalf("parse collected FIO results: %v", err)
	}
	if got[0].Native != core.OutcomePass {
		t.Errorf("collected FIO native = %q, want pass", got[0].Native)
	}
	singleRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(singleRoot, FIOFileName), raw, 0o644); err != nil {
		t.Fatalf("write one-VM raw fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(singleRoot, FIOSummaryFileName), []byte(`{"total_vms":1,"successful":1,"failed":0}`), 0o644); err != nil {
		t.Fatalf("write one-VM summary: %v", err)
	}
	refs, data, err = collectFIOResults(singleRoot, false)
	if err != nil {
		t.Fatalf("collect one-VM FIO result: %v", err)
	}
	if len(refs) != 2 {
		t.Errorf("one-VM references = %d, want 2", len(refs))
	}
	if _, err := ParseFIO(data, "TR-STOR-002"); err != nil {
		t.Fatalf("parse collected one-VM FIO result: %v", err)
	}
	refs, data, err = collectFIOResults(root, true)
	if err == nil {
		t.Error("fleet replay: want error, got nil")
	}
	if refs != nil || data != nil {
		t.Error("fleet replay: want no collected data")
	}
	replayRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(replayRoot, FIOFileName), raw, 0o644); err != nil {
		t.Fatalf("write replay fixture: %v", err)
	}
	refs, data, err = collectFIOResults(replayRoot, true)
	if err != nil {
		t.Fatalf("collect replay FIO result: %v", err)
	}
	if len(refs) != 1 {
		t.Errorf("replay references = %d, want 1", len(refs))
	}
	if _, err := ParseFIO(data, "TR-STOR-002"); err != nil {
		t.Fatalf("parse replay FIO result: %v", err)
	}
}

func TestPreflightChecksFIOCommand(t *testing.T) {
	dir := t.TempDir()
	binaryPath := filepath.Join(dir, binary)
	if err := os.WriteFile(binaryPath, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("write fake virtbench: %v", err)
	}
	t.Setenv("PATH", dir)
	findings, err := (preflight{sc: Scenario{CommandCheck: []string{"fio", "--help"}}}).Check(
		context.Background(), &core.RunCtx{}, nil, nil,
	)
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if len(findings) != 2 || findings[1].Level != "error" {
		t.Fatalf("findings = %+v, want FIO command error", findings)
	}
}

func TestPreflightUsesEmbeddedFIOP99Template(t *testing.T) {
	dir := t.TempDir()
	binaryPath := filepath.Join(dir, binary)
	if err := os.WriteFile(binaryPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake virtbench: %v", err)
	}
	t.Setenv("PATH", dir)
	findings, err := (preflight{sc: Scenario{Validate: fioValidate}}).Check(
		context.Background(), &core.RunCtx{}, nil, []core.TestRequirement{{ID: "TR-STOR-002", Params: map[string]any{"storage_class": "sc", "num_vms": 1}}},
	)
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if len(findings) != 3 {
		t.Fatalf("findings = %+v, want binary, storage class, and parameter checks", findings)
	}
	for _, f := range findings {
		if f.Level != "info" {
			t.Fatalf("findings = %+v, want all checks to pass", findings)
		}
	}
}

// TestScenarioEvaluatorNativeFail: a native failure (not every VM succeeded)
// fails the TR on its own line and short-circuits SLA grading — the one piece of
// virtbench-specific scoring that survives ADR-0013 (measurability is now data).
func TestScenarioEvaluatorNativeFail(t *testing.T) {
	ev := scenarioEvaluator{}
	tr := core.TestRequirement{ID: "TR-STOR-001", AutomationTool: "virtbench disk-ops",
		SLAs: []core.SLA{{Metric: "pvc_attach_time", MeasuredBy: []string{"virtbench disk-ops"}}}}
	res := core.TestResult{TRID: "TR-STOR-001", Native: core.OutcomeFail,
		Metrics: []core.Metric{{Name: "vms_failed", Value: 2}, {Name: "vms_total", Value: 10}}}
	got, err := ev.Evaluate(context.Background(), tr, res)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if len(got) != 1 || got[0].Item != "native:all_vms_succeeded" || got[0].Outcome != core.OutcomeFail {
		t.Fatalf("native failure should yield a single fail verdict, got %+v", got)
	}
}

func TestScenarioEvaluatorLabelsFIOFleetP99(t *testing.T) {
	limit := 5.0
	tr := core.TestRequirement{ID: "TR-STOR-002", AutomationTool: "virtbench fio", SLAStatus: core.StatusDefined, SLAs: []core.SLA{{
		Metric: "read_latency", Operator: "<=", Value: &limit, Unit: "ms", Percentile: "p99", MeasuredBy: []string{"virtbench fio"},
	}}}
	result := core.TestResult{TRID: tr.ID, Metrics: []core.Metric{
		{Name: "vms_total", Value: 2},
		{Name: "read_latency", Value: 3, Unit: "ms", Percentile: "p99"},
	}}
	verdicts, err := (scenarioEvaluator{sc: Scenario{AutomationTool: "virtbench fio"}}).Evaluate(context.Background(), tr, result)
	if err != nil {
		t.Fatalf("evaluate fleet FIO: %v", err)
	}
	if len(verdicts) != 1 || verdicts[0].Reason != "worst per-VM p99 across 2 VMs" {
		t.Errorf("fleet FIO verdicts = %+v, want worst-per-VM p99 label", verdicts)
	}
}

func TestParseSummaryErrors(t *testing.T) {
	if _, err := ParseSummary(nil, "TR-VIRT-002"); err == nil {
		t.Error("empty payload: want error, got nil")
	}
	if _, err := ParseSummary([]byte(`{"total_vms":0,"metrics":[]}`), "TR-VIRT-002"); err == nil {
		t.Error("zero VMs: want error, got nil")
	}
}

// TestBuildArgs verifies the CLI is assembled from params/backend only.
func TestBuildArgs(t *testing.T) {
	tr := core.TestRequirement{
		ID: "TR-VIRT-002",
		Params: map[string]any{
			"end":              100,
			"vm-name":          "ignored", // wrong key, ensure default used
			"vm_name":          "vb",
			"namespace_prefix": "vbench",
			"storage_class":    "ocs-storagecluster-ceph-rbd",
			"storage_driver":   "csi",
		},
	}
	got, err := datasourceCloneArgs(false, "virtbench")(&core.RunCtx{}, tr, "/work/res")
	if err != nil {
		t.Fatalf("buildArgs: %v", err)
	}
	want := []string{
		"datasource-clone",
		"--start", "1",
		"--end", "100",
		"--vm-name", "vb",
		"--namespace-prefix", "vbench",
		"--storage-class", "ocs-storagecluster-ceph-rbd",
		"--save-results",
		"--results-folder", "/work/res",
		"--storage-driver", "csi",
		"--cleanup", "--cleanup-on-failure", "--yes",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("buildArgs =\n  %v\nwant\n  %v", got, want)
	}
}

// TestBuildArgsVMNameDefault: with no vm_name param, the default MUST be the
// bundled template's VM name (rhel-9-vm) — otherwise virtbench's Running poll
// never matches the VM it created and the run hangs (the live-run bug).
func TestBuildArgsVMNameDefault(t *testing.T) {
	tr := core.TestRequirement{ID: "TR-VIRT-001", Params: map[string]any{"storage_class": "sc"}}
	got, err := datasourceCloneArgs(true, "virtbench")(&core.RunCtx{}, tr, "/work/res")
	if err != nil {
		t.Fatalf("buildArgs: %v", err)
	}
	if !strings.Contains(strings.Join(got, " "), "--vm-name rhel-9-vm") {
		t.Errorf("default vm-name must be rhel-9-vm; got %v", got)
	}
}

// TestBuildArgsBootStorm: the boot-storm scenario is the same subcommand plus the
// --boot-storm flag — a new table row, no engine change.
func TestBuildArgsBootStorm(t *testing.T) {
	tr := core.TestRequirement{ID: "TR-VIRT-001", Params: map[string]any{
		"end":           100,
		"storage_class": "ocs-storagecluster-ceph-rbd",
	}}
	got, err := datasourceCloneArgs(true, "virtbench")(&core.RunCtx{}, tr, "/work/res")
	if err != nil {
		t.Fatalf("buildArgs: %v", err)
	}
	joined := strings.Join(got, " ")
	if !strings.HasPrefix(joined, "datasource-clone ") {
		t.Errorf("boot storm must use datasource-clone subcommand; got %q", joined)
	}
	for _, want := range []string{"--boot-storm", "--results-folder /work/res", "--cleanup"} {
		if !strings.Contains(joined, want) {
			t.Errorf("boot storm args missing %q; got %q", want, joined)
		}
	}
}

// TestBuildArgsDiskOps: disk-ops is a different subcommand with its own flags
// (note --results-dir, not --results-folder).
func TestBuildArgsDiskOps(t *testing.T) {
	tr := core.TestRequirement{ID: "TR-STOR-001", Params: map[string]any{
		"end":           50,
		"storage_class": "ocs-storagecluster-ceph-rbd",
	}}
	got, err := diskOpsArgs("disk-ops")(&core.RunCtx{}, tr, "/work/res")
	if err != nil {
		t.Fatalf("buildArgs: %v", err)
	}
	joined := strings.Join(got, " ")
	if !strings.HasPrefix(joined, "disk-ops ") {
		t.Errorf("disk-ops subcommand expected; got %q", joined)
	}
	for _, want := range []string{"--operation hotplug", "--end 50", "--storage-class ocs-storagecluster-ceph-rbd", "--results-dir /work/res", "--create-vms"} {
		if !strings.Contains(joined, want) {
			t.Errorf("disk-ops args missing %q; got %q", want, joined)
		}
	}
}

func TestBuildArgsFIO(t *testing.T) {
	resultsDir := t.TempDir()
	tr := core.TestRequirement{ID: "TR-STOR-002", Params: map[string]any{
		"storage_class":   "ocs-storagecluster-ceph-rbd",
		"fio_vm_template": "assets/fio-vm-template.yaml",
		"num_vms":         1,
		"fio_bs":          "4k",
	}}
	got, err := fioArgs("fio-latency")(&core.RunCtx{}, tr, resultsDir)
	if err != nil {
		t.Fatalf("buildArgs: %v", err)
	}
	joined := strings.Join(got, " ")
	for _, want := range []string{
		"fio --action run-all",
		"--start 1 --end 1",
		"--vm-template ",
		"--fio-rw randrw --fio-bs 4k",
		"--fio-runtime 600",
		"--results-dir " + resultsDir,
		"--cleanup",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("fio args missing %q; got %q", want, joined)
		}
	}
	fleet := core.TestRequirement{ID: "TR-STOR-002", Params: map[string]any{
		"storage_class":   "ocs-storagecluster-ceph-rbd",
		"fio_vm_template": "assets/fio-vm-template.yaml",
		"num_vms":         40,
		"fio_bs":          "4k",
		"profile":         "load-80",
		"concurrency":     40,
	}}
	got, err = fioArgs("fio-latency")(&core.RunCtx{}, fleet, resultsDir)
	if err != nil {
		t.Fatalf("build fleet args: %v", err)
	}
	joined = strings.Join(got, " ")
	for _, want := range []string{
		"--start 1 --end 40",
		"--concurrency 40",
		"--fio-runtime 600",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("fleet fio args missing %q; got %q", want, joined)
		}
	}
	if _, err := fioArgs("fio-latency")(&core.RunCtx{}, core.TestRequirement{ID: "TR-STOR-002", Params: map[string]any{
		"storage_class": "sc", "num_vms": 0,
	}}, "/work/res"); err == nil {
		t.Error("zero FIO VM count: want error, got nil")
	}

	defaultResultsDir := t.TempDir()
	defaultTR := core.TestRequirement{ID: "TR-STOR-002", Params: map[string]any{
		"storage_class": "ocs-storagecluster-ceph-rbd",
		"num_vms":       1,
		"fio_bs":        "4k",
	}}
	got, err = fioArgs("fio-latency")(&core.RunCtx{}, defaultTR, defaultResultsDir)
	if err != nil {
		t.Fatalf("build args with embedded template: %v", err)
	}
	staged := filepath.Join(defaultResultsDir, fioVMTemplateFile)
	data, err := os.ReadFile(staged)
	if err != nil {
		t.Fatalf("read staged embedded template: %v", err)
	}
	if string(data) != fioVMTemplate {
		t.Fatal("staged embedded template differs from the compiled template")
	}
	if !strings.Contains(strings.Join(got, " "), "--vm-template "+staged) {
		t.Fatalf("FIO args do not use staged embedded template: %v", got)
	}
}

func TestFIOActionArgs(t *testing.T) {
	base := []string{"fio", "--action", "run-all", "--cleanup", "--start", "1"}
	for _, action := range []string{"deploy", "gather-results", "cleanup"} {
		got := strings.Join(fioActionArgs(base, action), " ")
		if got != "fio --action "+action+" --start 1" {
			t.Errorf("%s args = %q", action, got)
		}
	}
}

func TestFIOWaitAndCollectionSettings(t *testing.T) {
	tr := core.TestRequirement{Params: map[string]any{"fio_runtime": 600}}
	timeout, err := fioVMReadyTimeout(tr)
	if err != nil || timeout != 10*time.Minute {
		t.Errorf("default ready timeout = %s, %v; want 10m, nil", timeout, err)
	}
	retries, delay, err := fioCollectionSettings(tr)
	if err != nil || retries != 32 || delay != 20 {
		t.Errorf("collection settings = %d, %d, %v; want 32, 20, nil", retries, delay, err)
	}
	tr.Params["vm_ready_timeout"] = 1200
	tr.Params["fio_collect_retries"] = 40
	tr.Params["fio_collect_retry_delay"] = 15
	timeout, err = fioVMReadyTimeout(tr)
	if err != nil || timeout != 20*time.Minute {
		t.Errorf("configured ready timeout = %s, %v; want 20m, nil", timeout, err)
	}
	retries, delay, err = fioCollectionSettings(tr)
	if err != nil || retries != 40 || delay != 15 {
		t.Errorf("configured collection settings = %d, %d, %v; want 40, 15, nil", retries, delay, err)
	}
	tr.Params["vm_ready_timeout"] = 0
	if _, err := fioVMReadyTimeout(tr); err == nil {
		t.Error("zero VM ready timeout: want error")
	}
}

func TestFIOVMStates(t *testing.T) {
	wanted := map[string]struct{}{
		"fio-1/fio-vm": {},
		"fio-2/fio-vm": {},
	}
	data := []byte(`{"items":[
		{"metadata":{"namespace":"fio-1","name":"fio-vm"},"status":{"phase":"Running","interfaces":[{"ipAddress":"10.0.0.1"}]}},
		{"metadata":{"namespace":"fio-2","name":"fio-vm"},"status":{"phase":"Failed"}},
		{"metadata":{"namespace":"other","name":"fio-vm"},"status":{"phase":"Running","interfaces":[{"ipAddress":"10.0.0.2"}]}}
	]}`)
	ready, failed, err := fioVMStates(data, wanted)
	if err != nil || ready != 1 || failed != 1 {
		t.Errorf("VM states = ready=%d failed=%d err=%v; want 1, 1, nil", ready, failed, err)
	}
	noIP := []byte(`{"items":[{"metadata":{"namespace":"fio-1","name":"fio-vm"},"status":{"phase":"Running"}}]}`)
	ready, failed, err = fioVMStates(noIP, wanted)
	if err != nil || ready != 0 || failed != 0 {
		t.Errorf("running VM without IP = ready=%d failed=%d err=%v; want 0, 0, nil", ready, failed, err)
	}
}

func TestFIOUsesCustomRunnerOnly(t *testing.T) {
	for _, sc := range scenarios {
		if sc.AutomationTool == "virtbench fio" {
			if sc.Run == nil {
				t.Error("FIO custom runner is nil")
			}
			continue
		}
		if sc.Run != nil {
			t.Errorf("%s unexpectedly has a custom runner", sc.AutomationTool)
		}
	}
}

func TestFIOPlansUseConfiguredVMCount(t *testing.T) {
	for _, tc := range []struct {
		name         string
		vmCount      int
		concurrency  int
		runtime      int
		readyTimeout time.Duration
	}{
		{name: "virtbench-fio-live-smoke.yaml", vmCount: 1, runtime: 30, readyTimeout: 10 * time.Minute},
		{name: "virtbench-fio-concurrent.yaml", vmCount: 40, concurrency: 40, runtime: 600, readyTimeout: 20 * time.Minute},
	} {
		p, err := plan.Load(filepath.Join("..", "..", "..", "plans", tc.name))
		if err != nil {
			t.Fatalf("load %s: %v", tc.name, err)
		}
		params := p.Overrides["TR-STOR-002"]
		vmCount, _ := core.AsInt(params["num_vms"])
		concurrency, _ := core.AsInt(params["concurrency"])
		runtime, _ := core.AsInt(params["fio_runtime"])
		if vmCount != tc.vmCount || concurrency != tc.concurrency || runtime != tc.runtime {
			t.Errorf("%s settings = num_vms=%d concurrency=%d runtime=%d", tc.name, vmCount, concurrency, runtime)
		}
		readyTimeout, err := fioVMReadyTimeout(core.TestRequirement{Params: params})
		if err != nil || readyTimeout != tc.readyTimeout {
			t.Errorf("%s ready timeout = %s, %v; want %s, nil", tc.name, readyTimeout, err, tc.readyTimeout)
		}
	}
}

func TestBuildArgsRequiresStorageClass(t *testing.T) {
	if _, err := datasourceCloneArgs(false, "virtbench")(&core.RunCtx{}, core.TestRequirement{ID: "TR-VIRT-002"}, "/work/res"); err == nil {
		t.Error("no storage class: want error, got nil")
	}
	if _, err := diskOpsArgs("disk-ops")(&core.RunCtx{}, core.TestRequirement{ID: "TR-STOR-001"}, "/work/res"); err == nil {
		t.Error("disk-ops no storage class: want error, got nil")
	}
	if _, err := fioArgs("fio-latency")(&core.RunCtx{}, core.TestRequirement{ID: "TR-STOR-002"}, "/work/res"); err == nil {
		t.Error("fio no storage class: want error, got nil")
	}
}

func TestBuildArgsBackendStorageClassWins(t *testing.T) {
	tr := core.TestRequirement{ID: "TR-VIRT-002", Params: map[string]any{"storage_class": "from-param"}}
	rc := &core.RunCtx{Backend: &core.ResolvedBackend{StorageClass: "from-backend"}}
	got, err := datasourceCloneArgs(false, "virtbench")(rc, tr, "/work/res")
	if err != nil {
		t.Fatalf("buildArgs: %v", err)
	}
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "--storage-class from-backend") {
		t.Errorf("backend storage class should win; got %q", joined)
	}
}

func TestReplayDir(t *testing.T) {
	trs := []core.TestRequirement{
		{ID: "TR-VIRT-002", Params: map[string]any{"results_dir": "/tmp/x"}},
	}
	if d := replayDir(trs); d != "/tmp/x" {
		t.Errorf("replayDir = %q, want /tmp/x", d)
	}
	if d := replayDir([]core.TestRequirement{{ID: "TR-VIRT-002"}}); d != "" {
		t.Errorf("replayDir = %q, want empty", d)
	}
}

func TestStageTemplateRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	sourceDir := filepath.Join(root, "templates")
	resultsDir := filepath.Join(root, "results")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(resultsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(root, "outside.yaml")
	if err := os.WriteFile(secret, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	template := filepath.Join(sourceDir, "vm.yaml")
	if err := os.Symlink(secret, template); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if _, err := stageTemplate(resultsDir, template); err == nil {
		t.Fatal("stageTemplate accepted a symlink escaping the source directory")
	}
}
