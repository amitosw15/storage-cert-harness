package virtbench

import (
	"context"
	"fmt"
	"slices"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/grader"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/registry"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/stages"
)

// Scenario is the per-subcommand config that the generic engine (engine.go) is
// parameterized over. Everything that differs between virtbench scenarios lives
// here; everything that is the same lives in the engine. Adding a scenario is a
// new row in scenarios below — no engine edits (decisions/0008).
type Scenario struct {
	// AutomationTool is the catalog tool identity; ScenarioName selects its workload.
	AutomationTool core.ToolName
	ScenarioName   WorkloadName
	// ProvidesTR is the single TR id this scenario runs.
	ProvidesTR string
	// ResultFile is the JSON file virtbench writes for this scenario.
	ResultFile string
	// NSPrefix is the default namespace prefix virtbench uses for this scenario's
	// test namespaces (overridable per-TR via the "namespace_prefix" param). Used
	// both to build the CLI and by Teardown's leftover-namespace sweep.
	NSPrefix string
	// BuildArgs assembles the CLI from params/backend only (never a threshold).
	// resultsRoot is the harness workdir virtbench must write results into, passed
	// via the scenario's results flag so the collector can find the output.
	BuildArgs func(rc *core.RunCtx, bag *core.Bag, tr core.TestRequirement, resultsRoot string) ([]string, error)
	// Run optionally replaces the shared single-command runner for scenarios that
	// need a supported multi-command tool lifecycle. Nil uses the shared runner.
	Run func(ctx context.Context, rc *core.RunCtx, bag *core.Bag, trs []core.TestRequirement) (stages.RunHandle, error)
	// Parse turns the result file into normalized TestResults for the TR.
	Parse func(data []byte, trID string) ([]core.TestResult, error)
	// Provision runs after the generic provisioner made resultsRoot (never in
	// replay), for a scenario that must stage the cluster before Run — drain clones
	// the VMs onto the target here. It may stash bag state for Teardown. Nil skips.
	Provision func(ctx context.Context, rc *core.RunCtx, bag *core.Bag, tr core.TestRequirement, resultsRoot string) error
	// ProvisionVMs, when non-nil, is called by the Provisioner after the results
	// dir is ready and before virtbench runs — for scenarios that need to create
	// VMs on the cluster (e.g. failure-recovery places VMs with node affinity).
	ProvisionVMs func(ctx context.Context, rc *core.RunCtx, bag *core.Bag, trs []core.TestRequirement) error
	// Teardown adds scenario-specific cleanup that the generic engine runs in
	// addition to (before) its namespace sweep, on the same fresh bounded context —
	// drain uncordons the workers it cordoned. Nil adds nothing.
	Teardown func(ctx context.Context, rc *core.RunCtx, bag *core.Bag) error
	// TolerateRunError keeps a non-zero exit from the CLI a graded result, not a
	// pipeline error — drain-nodes exits 1 on a timeout, which is a FAIL to score
	// from the log, not a crash.
	TolerateRunError bool
	// Collect optionally replaces the default single-file collector for scenarios
	// whose result consists of several files.
	Collect func(root string, replay bool) (refs []string, data []byte, err error)
	// DetailFile, when set, is a second (per-VM) result file the collector reads
	// so DetailParse can derive metrics the summary can't (e.g. a real
	// percentile from raw samples). Without DetailParse it is supplementary and
	// absence is not fatal; with DetailParse the scenario is graded from it, so
	// a file that cannot be found — or named unambiguously — fails the
	// execution rather than quietly dropping the metric it gates.
	DetailFile string
	// DetailParse turns DetailFile's bytes into extra metrics appended to the
	// TR's result. Only called when DetailFile was collected.
	DetailParse func(data []byte) ([]core.Metric, error)
	// DeriveMetrics computes metrics the parser can't, because they depend on TR
	// params the parser never sees (it gets only result bytes). The default
	// evaluator appends them before grading — single-node's max_luns_per_node is
	// successful VMs × num_disks. Nil adds nothing.
	DeriveMetrics func(tr core.TestRequirement, res core.TestResult) []core.Metric
}

// resultsSubdir is this tool's directory under the run's working directory;
// each execution gets its own subtree beneath it (core.RunCtx.ExecutionDir).
const resultsSubdir = "virtbench"

// Result file names virtbench writes with --save-results (see the tool's
// save_results()). Each scenario has its own; SummaryFileName is kept exported for
// the existing datasource-clone golden test.
const (
	SummaryFileName         = "summary_vm_creation_results.json"
	BootStormFileName       = "summary_boot_storm_results.json"
	DiskOpsFileName         = "disk_ops_results.json"
	FIOFileName             = "fio_raw.json"
	FIOSummaryFileName      = "summary_fio_benchmark.json"
	FailureRecoveryFileName = "summary_failure_recovery_results.json"
	// DrainFileName is the log drain-nodes writes with --log-file; it is not JSON —
	// ParseDrain reads the timing and VMI distribution from the human log.
	DrainFileName = "drain.log"
	// DetailCloneFileName is the per-VM detailed results the datasource-clone
	// engine writes alongside the summary; it holds the raw clone_duration_sec
	// samples a percentile is computed from.
	DetailCloneFileName = "vm_creation_results.json"
)

// setupGroup identifies the helper shared by every VirtBench scenario.
const setupGroup = "virtbench"

func init() {
	registry.RegisterSetup(sharedSSHSetup)
	for _, sc := range scenarios {
		registry.Register(stages.ToolIntegration{
			Name:         sc.IntegrationName(),
			Image:        "", // no upstream image; phase-1 local-exec (decisions/0007)
			Provides:     []string{sc.ProvidesTR},
			Preflight:    preflight{sc},
			Provisioner:  provisioner{sc},
			Runner:       runner{sc},
			LogCollector: collector{sc},
			ResultParser: parser{sc},
			Teardown:     teardown{sc},

			// virtbench drives a whole cluster's storage/virt stack at scale; never
			// overlap it with another job.
			ParallelSafe:      false,
			ExclusivityGroups: []string{"cluster"},

			// All virtbench scenarios share the SSH helper and its cleanup.
			SetupGroups: []string{setupGroup},

			Evaluators: map[string]stages.Evaluator{
				sc.ProvidesTR: scenarioEvaluator{sc},
			},
		})
	}
}

// scenarioEvaluator adds the one piece of virtbench-specific scoring the generic
// grader can't know: a run passes only if every VM succeeded, so a native failure
// fails the TR on its own line and skips SLA grading (an average over an
// incomplete run is meaningless). Measurability is data — the catalog's
// measured_by — so the generic grader
// (grader.GradeSLAs) handles skips and when-scoped bar selection (ADR-0013).
type scenarioEvaluator struct{ sc Scenario }

func (e scenarioEvaluator) Evaluate(_ context.Context, tr core.TestRequirement, res core.TestResult) ([]core.Verdict, error) {
	if e.sc.DeriveMetrics != nil {
		res.Metrics = append(slices.Clone(res.Metrics), e.sc.DeriveMetrics(tr, res)...)
	}
	if res.Native == core.OutcomeFail {
		return []core.Verdict{{
			TR:      tr.ID,
			Item:    "native:all_vms_succeeded",
			Outcome: core.OutcomeFail,
			Reason:  fmt.Sprintf("run incomplete: %d of %d VMs failed", vmCount(res, "vms_failed"), vmCount(res, "vms_total")),
		}}, nil
	}
	graded := grader.GradeSLAs(tr, res)
	if e.sc.ScenarioName == WorkloadFIO && vmCount(res, "vms_total") > 1 {
		for i := range graded {
			if graded[i].Item == "sla:read_latency@p99" || graded[i].Item == "sla:write_latency@p99" {
				graded[i].Reason = fmt.Sprintf("worst per-VM p99 across %d VMs", vmCount(res, "vms_total"))
			}
		}
	}
	var out []core.Verdict
	out = append(out, graded...)
	out = append(out, grader.GradeChecks(tr, res)...)
	return out, nil
}

// vmCount reads a run-level count the summary parser attaches (vms_failed /
// vms_total), returning 0 when absent.
func vmCount(res core.TestResult, name string) int {
	for _, m := range res.Metrics {
		if m.Name == name {
			return int(m.Value)
		}
	}
	return 0
}

// IntegrationName is the registry key for this tool scenario.
func (s Scenario) IntegrationName() string {
	return string(s.AutomationTool) + " " + string(s.ScenarioName)
}
