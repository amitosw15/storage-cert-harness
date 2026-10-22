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

// Scenario is the per-subcommand config used by the generic engine; scenario rows live in workloads.go (decisions/0008).
type Scenario struct {
	// AutomationTool is the tool enum; ScenarioName selects this registered workload.
	AutomationTool core.ToolName
	ScenarioName   WorkloadName
	// ProvidesTR is the single TR id this scenario runs.
	ProvidesTR string
	// ResultFile is the JSON file virtbench writes for this scenario.
	ResultFile string
	// NSPrefix is the default namespace prefix for CLI arguments and cleanup.
	NSPrefix string
	// BuildArgs builds the CLI from parameters, backend config, and session state.
	BuildArgs func(rc *core.RunCtx, s *session, tr core.TestRequirement, resultsRoot string) ([]string, error)
	// Run optionally replaces the shared runner with a scenario's command sequence.
	Run func(ctx context.Context, rc *core.RunCtx, s *session, trs []core.TestRequirement) error
	// Parse turns the result file into normalized TestResults for the TR.
	Parse func(data []byte, trID string) ([]core.TestResult, error)
	// The fields below are optional hooks a scenario sets when it needs more than
	// the generic plumbing; every clone/disk-ops scenario leaves them zero.

	// Provision stages scenario resources after resultsRoot is ready, except in replay.
	Provision func(ctx context.Context, rc *core.RunCtx, s *session, tr core.TestRequirement, resultsRoot string) error
	// ProvisionVMs optionally creates scenario VMs before Run.
	ProvisionVMs func(ctx context.Context, rc *core.RunCtx, s *session, trs []core.TestRequirement) error
	// Teardown optionally adds cleanup before the shared namespace sweep.
	Teardown func(ctx context.Context, rc *core.RunCtx, s *session) error
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

func (s Scenario) IntegrationName() string {
	return string(s.AutomationTool) + " " + string(s.ScenarioName)
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

// setupGroup is the shared-setup group key every virtbench scenario belongs to.
// Its provider (sshPodSetup, setup.go) creates and cleans up the shared helper;
// individual provisioners recheck it in case a previous scenario evicted it.
const setupGroup = "virtbench"

func init() {
	registry.RegisterSetup(sharedSSHSetup)
	for _, sc := range scenarios {
		registry.Register(stages.ToolIntegration{
			Name:       sc.IntegrationName(),
			Image:      "", // no upstream image; phase-1 local-exec (decisions/0007)
			Provides:   []string{sc.ProvidesTR},
			NewSession: func() stages.Session { return &session{sc: sc} },

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
