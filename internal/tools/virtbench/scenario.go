package virtbench

import (
	"context"
	"fmt"
	"slices"

	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/core"
	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/grader"
	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/registry"
	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/stages"
)

// Scenario is the per-subcommand config that the generic engine (engine.go) is
// parameterized over. Everything that differs between virtbench scenarios lives
// here; everything that is the same lives in the engine. Adding a scenario is a
// new row in scenarios below — no engine edits (decisions/0008).
type Scenario struct {
	// AutomationTool is the KB automation_tool string verbatim; it is the registry
	// key so orchestrator's registry.Get(tr.AutomationTool) resolves this scenario.
	AutomationTool string
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
	BuildArgs func(rc *core.RunCtx, tr core.TestRequirement, resultsRoot string) ([]string, error)
	// Run optionally replaces the shared single-command runner for scenarios that
	// need a supported multi-command tool lifecycle. Nil uses the shared runner.
	Run func(ctx context.Context, rc *core.RunCtx, bag *core.Bag, trs []core.TestRequirement) (stages.RunHandle, error)
	// Parse turns the result file into normalized TestResults for the TR.
	Parse func(data []byte, trID string) ([]core.TestResult, error)

	// The fields below are optional hooks a scenario sets when it needs more than
	// the generic plumbing; every clone/disk-ops scenario leaves them zero.
	// drain-nodes uses them (decisions/0013).

	// Preflight runs extra scenario-specific checks after the generic ones (binary,
	// storage_class). Nil adds nothing.
	Preflight func(ctx context.Context, rc *core.RunCtx, trs []core.TestRequirement) []core.Finding
	// Provision runs after the generic provisioner made resultsRoot (never in
	// replay), for a scenario that must stage the cluster before Run — drain clones
	// the VMs onto the target here. It may stash bag state for Teardown. Nil skips.
	Provision func(ctx context.Context, rc *core.RunCtx, bag *core.Bag, tr core.TestRequirement, resultsRoot string) error
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
	// CommandCheck verifies that an installed virtbench supports the scenario before
	// the harness starts a live run. Nil means the shared binary check is sufficient.
	CommandCheck []string
	// Validate checks scenario-specific inputs before setup can create cluster resources.
	// Nil means the shared preflight checks are sufficient.
	Validate func(core.TestRequirement) error
	// DetailFile, when set, is a second (per-VM) result file the collector reads
	// best-effort so DetailParse can derive metrics the summary can't (e.g. a real
	// percentile from raw samples). Absence is not fatal.
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

// Result file names virtbench writes with --save-results (see the tool's
// save_results()). Each scenario has its own; SummaryFileName is kept exported for
// the existing datasource-clone golden test.
const (
	SummaryFileName    = "summary_vm_creation_results.json"
	BootStormFileName  = "summary_boot_storm_results.json"
	DiskOpsFileName    = "disk_ops_results.json"
	FIOFileName        = "fio_raw.json"
	FIOSummaryFileName = "summary_fio_benchmark.json"
	// DrainFileName is the log drain-nodes writes with --log-file; it is not JSON —
	// ParseDrain reads the timing and VMI distribution from the human log.
	DrainFileName = "drain.log"
	// DetailCloneFileName is the per-VM detailed results the datasource-clone
	// engine writes alongside the summary; it holds the raw clone_duration_sec
	// samples a percentile is computed from.
	DetailCloneFileName = "vm_creation_results.json"
)

// scenarios is the registry-of-scenarios. Each becomes one ToolIntegration.
var scenarios = []Scenario{
	{
		AutomationTool: "virtbench datasource-clone",
		ProvidesTR:     "TR-VIRT-002",
		ResultFile:     SummaryFileName,
		NSPrefix:       "virtbench",
		BuildArgs:      datasourceCloneArgs(false, "virtbench"),
		Parse:          ParseSummary,
		// all SLAs (clone_duration / time_to_running / time_to_ping) are measured.
	},
	{
		AutomationTool: "virtbench datasource-clone --boot-storm",
		ProvidesTR:     "TR-VIRT-001",
		ResultFile:     BootStormFileName,
		NSPrefix:       "virtbench",
		BuildArgs:      datasourceCloneArgs(true, "virtbench"),
		Parse:          ParseBootStorm,
	},
	{
		AutomationTool: "virtbench disk-ops",
		ProvidesTR:     "TR-STOR-001",
		ResultFile:     DiskOpsFileName,
		NSPrefix:       "disk-ops",
		BuildArgs:      diskOpsArgs("disk-ops"),
		Parse:          ParseDiskOps,
	},
	{
		// VDI Ready badge (TR-VIRT-018): clone VMs at scale with 2 disks each.
		// Same datasource-clone parser as TR-VIRT-002, but its own BuildArgs
		// (clone_at_scale.go): virtbench's --num-disks flag only labels output,
		// it never adds a disk, so a real 2-disk template is embedded by
		// default; a plan may swap in its own vm_template/num_disks, validated
		// against each other (cloneAtScaleValidate). Only gate: clone_duration.
		AutomationTool: "virtbench datasource-clone --num-disks 2",
		ProvidesTR:     "TR-VIRT-018",
		ResultFile:     SummaryFileName,
		NSPrefix:       "virtbench",
		BuildArgs:      cloneAtScaleArgs("virtbench"),
		Parse:          ParseSummary,
		Validate:       cloneAtScaleValidate,
		// TR-018's gate is p99 clone provisioning; compute it from the per-VM
		// samples (the summary carries only avg/max/min).
		DetailFile:  DetailCloneFileName,
		DetailParse: func(data []byte) ([]core.Metric, error) { return ClonePercentiles(data, []string{"p99"}) },
	},
	{
		AutomationTool:   "virtbench vm-ops drain-nodes",
		ProvidesTR:       "TR-VIRT-008",
		ResultFile:       DrainFileName,
		NSPrefix:         drainNSPrefix,
		BuildArgs:        drainArgs,
		Parse:            ParseDrain,
		Preflight:        drainPreflight,
		Provision:        drainProvision,
		Teardown:         drainTeardown,
		TolerateRunError: true, // drain-nodes exits 1 on timeout; grade the FAIL from the log
	},
	{
		AutomationTool: "virtbench fio",
		ProvidesTR:     "TR-STOR-002",
		ResultFile:     FIOFileName,
		NSPrefix:       "fio-latency",
		BuildArgs:      fioArgs("fio-latency"),
		Run:            runFIO,
		Parse:          ParseFIO,
		Collect:        collectFIOResults,
		CommandCheck:   []string{"fio", "--help"},
		Validate:       fioValidate,
	},
	{
		AutomationTool:   "virtbench datasource-clone --single-node",
		ProvidesTR:       "TR-VIRT-013",
		ResultFile:       SummaryFileName,
		NSPrefix:         singleNodeNSPrefix,
		BuildArgs:        singleNodeCeilingArgs(singleNodeNSPrefix),
		Parse:            ParseSingleNodeCeiling,
		Preflight:        singleNodeCeilingPreflight,
		Validate:         singleNodeCeilingValidate,
		DeriveMetrics:    singleNodeLunMetric,
		TolerateRunError: true, // virtbench exits 1 whenever any VM failed — expected here (singlenode.go)
	},
}

// setupGroup is the shared-setup group key every virtbench scenario belongs to.
// Its provider (sshPodSetup, setup.go) creates and cleans up the shared helper;
// individual provisioners recheck it in case a previous scenario evicted it.
const setupGroup = "virtbench"

func init() {
	registry.RegisterSetup(sharedSSHSetup)
	for _, sc := range scenarios {
		registry.Register(stages.ToolIntegration{
			Name:         sc.AutomationTool,
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
	if e.sc.AutomationTool == "virtbench fio" && vmCount(res, "vms_total") > 1 {
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
