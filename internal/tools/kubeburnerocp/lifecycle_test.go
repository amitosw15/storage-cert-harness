package kubeburnerocp

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/core"
	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/registry"
	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/stages"
)

const createEvidenceFile = "vmiLatencyMeasurement-virt-parallel-create-vms-0.json"
const migrationEvidenceFile = "vmimLatencyMeasurement-virt-parallel-migrate-vms-0.json"

func lifecycleFixture(t *testing.T) map[string][]byte {
	return readFixtures(t, "virt-parallel", "jobSummary.json", createEvidenceFile, migrationEvidenceFile)
}

func TestVirtParallelMigrationEvidence(t *testing.T) {
	for _, tc := range []struct {
		name string
		want core.Outcome
	}{
		{"complete", core.OutcomePass}, {"missing migration evidence", core.OutcomeError},
		{"empty migration evidence", core.OutcomeFail}, {"partial migration", core.OutcomeFail},
		{"repeated migration for one VM", core.OutcomeFail}, {"retries of all VMs", core.OutcomePass},
		{"foreign run", core.OutcomeError}, {"wrong VM", core.OutcomeError},
		{"missing success", core.OutcomeError}, {"malformed evidence", core.OutcomeError},
		{"missing create evidence", core.OutcomeError}, {"missing migration stage", core.OutcomeFail},
		{"skipped migration", core.OutcomeFail}, {"failed job", core.OutcomeFail},
		{"old creation-only golden", core.OutcomeError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := lifecycleFixture(t)
			var docs []lifecycleSample
			var jobs []jobSummary
			if err := json.Unmarshal(data[migrationEvidenceFile], &docs); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(data["jobSummary.json"], &jobs); err != nil {
				t.Fatal(err)
			}
			switch tc.name {
			case "partial migration":
				docs = docs[:1]
			case "empty migration evidence":
				docs = []lifecycleSample{}
			case "repeated migration for one VM":
				docs[1] = docs[0]
				docs[1].MigrationName = "retry"
			case "retries of all VMs":
				docs = append(docs, docs...)
			case "foreign run":
				docs[0].UUID = "another-run"
			case "wrong VM":
				docs[0].VMIName = "unrelated-vm"
			case "missing success":
				docs[0].SucceededLatency = nil
			case "missing migration stage":
				jobs = append(jobs[:4], jobs[5:]...)
			case "skipped migration":
				for i := range jobs {
					jobs[i].WorkloadFlags["skipMigrationJob"] = "true"
				}
			case "failed job":
				jobs[4].Passed = false
			}
			data[migrationEvidenceFile], _ = json.Marshal(docs)
			data["jobSummary.json"], _ = json.Marshal(jobs)
			switch tc.name {
			case "missing migration evidence":
				delete(data, migrationEvidenceFile)
			case "missing create evidence":
				delete(data, createEvidenceFile)
			case "malformed evidence":
				data[migrationEvidenceFile] = []byte("broken JSON")
			case "old creation-only golden":
				data["jobSummary.json"] = mustRead(t, "virt-parallel", "jobSummary-no-migration.json")
				delete(data, createEvidenceFile)
				delete(data, migrationEvidenceFile)
			}
			res, err := ParseResults(WorkloadVirtParallel, "TR-VIRT-019", data)
			if err != nil {
				t.Fatal(err)
			}
			// The fixture is a two-VM run, below the check's 20-VM scale, so a
			// clean result is reported as skip rather than as a passing
			// parallel-lifecycle-20.
			wantCheck := tc.want
			if tc.want == core.OutcomePass {
				wantCheck = core.OutcomeSkip
			}
			if res.Native != tc.want || res.Checks[virtParallelCheck] != wantCheck {
				t.Fatalf("native=%s checks=%v raw=%s", res.Native, res.Checks, res.Raw)
			}
		})
	}
}

func TestVirtParallelPassingDurationCannotMaskNoMigration(t *testing.T) {
	data := lifecycleFixture(t)
	data[migrationEvidenceFile] = []byte("[]")
	res, err := ParseResults(WorkloadVirtParallel, "TR-VIRT-019", data)
	if err != nil {
		t.Fatal(err)
	}
	gate := 1800.0
	tr := core.TestRequirement{ID: "TR-VIRT-019", AutomationTool: "kube-burner-ocp virt-parallel",
		Params: map[string]any{"vms": 2}, SLAs: []core.SLA{{Metric: virtParallelMetric, Unit: "s"}},
		Bars:   []core.SLA{{Metric: virtParallelMetric, Unit: "s", Operator: "<=", Value: &gate}},
		Checks: []core.Check{{ID: virtParallelCheck, Kind: core.CheckScenario}}}
	tool, _ := registry.Get("kube-burner-ocp")
	vs, err := tool.Evaluators[tr.ID].Evaluate(context.Background(), tr, res)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 3 || vs[0].Outcome != core.OutcomePass || vs[1].Outcome != core.OutcomeFail || vs[1].Actual != "0 distinct VM migrations" || vs[2].Item != "native:jobs_passed" || vs[2].Outcome != core.OutcomeFail {
		t.Fatalf("passing duration hid missing migration: %+v", vs)
	}
}

func TestVirtParallelPlanPopulation(t *testing.T) {
	res, err := ParseResults(WorkloadVirtParallel, "TR-VIRT-019", lifecycleFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		params map[string]any
		want   core.Outcome
	}{
		// A clean two-VM run is below the check's scale, hence skip, not pass.
		{map[string]any{"vms": 2}, core.OutcomeSkip},
		{map[string]any{"initial_vms": 2}, core.OutcomeSkip},
		{map[string]any{"vms": 20}, core.OutcomeError},
		{map[string]any{"vms": 2, "initial_vms": 3}, core.OutcomeError},
		{map[string]any{"vms": 2, "max_iterations": 2}, core.OutcomeError},
	} {
		vs, err := (virtParallelEvaluator{}).Evaluate(context.Background(), core.TestRequirement{ID: "TR-VIRT-019", Params: tc.params}, res)
		if err != nil || len(vs) != 2 || vs[0].Outcome != tc.want || vs[1].Item != "native:jobs_passed" || vs[1].Outcome != core.OutcomePass {
			t.Fatalf("params %v: %v %+v", tc.params, err, vs)
		}
	}
}

func TestVirtParallelCatalogCountCLI(t *testing.T) {
	args, err := buildCLIArgs(Params{Workload: WorkloadVirtParallel, Raw: map[string]any{"vms": 20}}, "storage-class")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(args, " "), "--initial-vms 20") {
		t.Fatalf("catalog count not used: %v", args)
	}
	for _, params := range []map[string]any{
		{"vms": 20, "initial_vms": 2}, {"vms": 2.5}, {"initial_vms": 0}, {"max_iterations": 0}, {"increment": -1},
	} {
		if _, err := buildCLIArgs(Params{Workload: WorkloadVirtParallel, Raw: params}, "storage-class"); err == nil {
			t.Fatalf("invalid population accepted: %v", params)
		}
	}
}

// Each iteration has its own jobSummary.json. The old basename-keyed collector
// replaced earlier iterations, including their failures, with the last file.
func TestCollectVirtParallelProgressiveIterations(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "all iterations", true: "earlier iteration failed"}[failed], func(t *testing.T) {
			dir := t.TempDir()
			for iteration := 0; iteration < 2; iteration++ {
				data := lifecycleFixture(t)
				var jobs []jobSummary
				if err := json.Unmarshal(data["jobSummary.json"], &jobs); err != nil {
					t.Fatal(err)
				}
				for i := range jobs {
					jobs[i].WorkloadFlags["maxIterations"] = "2"
					jobs[i].WorkloadFlags["increment"] = "1"
					if iteration == 1 {
						jobs[i].JobConfig.Name = strings.TrimSuffix(jobs[i].JobConfig.Name, "0") + "1"
					}
				}
				if failed && iteration == 0 {
					jobs[4].Passed = false
				}
				data["jobSummary.json"], _ = json.Marshal(jobs)
				for name, blob := range data {
					if name == "jobSummary.json" {
						continue
					}
					var docs []lifecycleSample
					if err := json.Unmarshal(blob, &docs); err != nil {
						t.Fatal(err)
					}
					if iteration == 1 {
						for i := range docs {
							docs[i].JobName = strings.TrimSuffix(docs[i].JobName, "0") + "1"
							docs[i].VMIName = strings.Replace(docs[i].VMIName, "-0-", "-1-", 1)
						}
						last := docs[1]
						last.VMIName = "virt-parallel-1-2"
						last.MigrationName = "migration-2"
						docs = append(docs, last)
					}
					data[name], _ = json.Marshal(docs)
				}
				iterDir := filepath.Join(dir, "virt-parallel", map[int]string{0: "iteration-0", 1: "iteration-1"}[iteration])
				if err := os.MkdirAll(iterDir, 0o755); err != nil {
					t.Fatal(err)
				}
				for name, blob := range data {
					if iteration == 1 {
						name = strings.Replace(name, "-vms-0.json", "-vms-1.json", 1)
					}
					if err := os.WriteFile(filepath.Join(iterDir, name), blob, 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			rc := &core.RunCtx{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
			bag := core.NewBag()
			bag.Set("kbo_runs", []kboRun{{TRID: "TR-VIRT-019", Workload: WorkloadVirtParallel, Subdir: dir}})
			logs, err := (collector{}).Collect(context.Background(), rc, bag, stages.RunHandle{})
			if err != nil {
				t.Fatal(err)
			}
			if len(logs.Data) != 6 {
				t.Fatalf("iteration evidence overwritten: %v", logs.Data)
			}
			results, err := (parser{}).Parse(context.Background(), rc, bag, logs)
			if err != nil {
				t.Fatal(err)
			}
			want := core.OutcomePass
			if failed {
				want = core.OutcomeFail
			}
			if len(results) != 1 || results[0].Native != want {
				t.Fatalf("%+v", results)
			}
			var raw struct {
				Lifecycle virtParallelEvidence `json:"lifecycle"`
			}
			if err := json.Unmarshal(results[0].Raw, &raw); err != nil {
				t.Fatal(err)
			}
			if raw.Lifecycle.Expected != 5 || raw.Lifecycle.Completed != 5 {
				t.Fatalf("wrong population: %+v", raw.Lifecycle)
			}
		})
	}
}

func TestVirtParallelRunErrorOverridesSuccessfulSummary(t *testing.T) {
	bag := core.NewBag()
	bag.Set("kbo_runs", []kboRun{{TRID: "TR-VIRT-019", Workload: WorkloadVirtParallel, RunErr: "exit status 1"}})
	data := map[string][]byte{}
	for name, blob := range lifecycleFixture(t) {
		data["TR-VIRT-019/"+name] = blob
	}
	results, err := (parser{}).Parse(context.Background(), nil, bag, core.LogBundle{Data: data})
	if err != nil || len(results) != 1 || results[0].Checks[virtParallelCheck] != core.OutcomeFail {
		t.Fatalf("%v %+v", err, results)
	}
}
