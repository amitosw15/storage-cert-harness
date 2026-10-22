package virtbench

import (
	"strconv"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/tools/common"
)

type WorkloadName string

const (
	WorkloadDatasourceClone   WorkloadName = "datasource-clone"
	WorkloadBootStorm         WorkloadName = "datasource-clone --boot-storm"
	WorkloadDiskOps           WorkloadName = "disk-ops"
	WorkloadCloneAtScale      WorkloadName = "datasource-clone --num-disks 2"
	WorkloadDrain             WorkloadName = "vm-ops drain-nodes"
	WorkloadFIO               WorkloadName = "fio"
	WorkloadSingleNodeCeiling WorkloadName = "datasource-clone --single-node"
	WorkloadFailureRecovery   WorkloadName = "failure-recovery"
)

type workloadSpec struct {
	checks        func(*core.RunCtx, core.TestRequirement) []common.PreflightCheck
	snapshotClone bool
}

// scenarios contains the execution and result behavior for each test.
var scenarios = []Scenario{
	{
		AutomationTool: core.ToolVirtbench,
		ScenarioName:   WorkloadDatasourceClone,
		ProvidesTR:     "TR-VIRT-002",
		ResultFile:     SummaryFileName,
		NSPrefix:       "virtbench",
		BuildArgs:      datasourceCloneArgs(false, "virtbench"),
		Parse:          ParseSummary,
		// all SLAs (clone_duration / time_to_running / time_to_ping) are measured.
	},
	{
		AutomationTool: core.ToolVirtbench,
		ScenarioName:   WorkloadBootStorm,
		ProvidesTR:     "TR-VIRT-001",
		ResultFile:     BootStormFileName,
		NSPrefix:       "virtbench",
		BuildArgs:      datasourceCloneArgs(true, "virtbench"),
		Parse:          ParseBootStorm,
	},
	{
		AutomationTool: core.ToolVirtbench,
		ScenarioName:   WorkloadDiskOps,
		ProvidesTR:     "TR-STOR-001",
		ResultFile:     DiskOpsFileName,
		NSPrefix:       "disk-ops",
		BuildArgs:      diskOpsArgs("disk-ops"),
		Parse:          ParseDiskOps,
	},
	{
		// Clone VMs with a validated two-disk template; --num-disks alone only labels output.
		AutomationTool: core.ToolVirtbench,
		ScenarioName:   WorkloadCloneAtScale,
		ProvidesTR:     "TR-VIRT-018",
		ResultFile:     SummaryFileName,
		NSPrefix:       "virtbench",
		BuildArgs: func(rc *core.RunCtx, _ *session, tr core.TestRequirement, resultsRoot string) ([]string, error) {
			return cloneAtScaleArgs("virtbench")(rc, tr, resultsRoot)
		},
		Parse: ParseSummary,
		// Compute p99 from per-VM samples because the summary only carries avg/max/min.
		DetailFile:  DetailCloneFileName,
		DetailParse: func(data []byte) ([]core.Metric, error) { return ClonePercentiles(data, []string{"p99"}) },
	},
	{
		AutomationTool:   core.ToolVirtbench,
		ScenarioName:     WorkloadDrain,
		ProvidesTR:       "TR-VIRT-008",
		ResultFile:       DrainFileName,
		NSPrefix:         drainNSPrefix,
		BuildArgs:        drainArgs,
		Parse:            ParseDrain,
		Provision:        drainProvision,
		Teardown:         drainTeardown,
		TolerateRunError: true, // drain-nodes exits 1 on timeout; grade the FAIL from the log
	},
	{
		AutomationTool: core.ToolVirtbench,
		ScenarioName:   WorkloadFIO,
		ProvidesTR:     "TR-STOR-002",
		ResultFile:     FIOFileName,
		NSPrefix:       "fio-latency",
		BuildArgs:      fioArgs("fio-latency"),
		Run:            runFIO,
		Parse:          ParseFIO,
		Collect:        collectFIOResults,
	},
	{
		AutomationTool: core.ToolVirtbench,
		ScenarioName:   WorkloadSingleNodeCeiling,
		ProvidesTR:     "TR-VIRT-013",
		ResultFile:     SummaryFileName,
		NSPrefix:       singleNodeNSPrefix,
		BuildArgs: func(rc *core.RunCtx, _ *session, tr core.TestRequirement, resultsRoot string) ([]string, error) {
			return singleNodeCeilingArgs(singleNodeNSPrefix)(rc, tr, resultsRoot)
		},
		Parse:            ParseSingleNodeCeiling,
		DeriveMetrics:    singleNodeLunMetric,
		TolerateRunError: true, // virtbench exits 1 whenever any VM failed — expected here (singlenode.go)
	},
	{
		// Create VMs on the target node and monitor recovery after a node failure.
		AutomationTool: core.ToolVirtbench,
		ScenarioName:   WorkloadFailureRecovery,
		ProvidesTR:     "TR-VIRT-007",
		ResultFile:     FailureRecoveryFileName,
		NSPrefix:       "failure-recovery",
		BuildArgs:      failureRecoveryArgs("failure-recovery"),
		Parse:          ParseFailureRecovery,
		ProvisionVMs:   provisionFailureRecoveryVMs,
	},
}

var workloadSpecs = map[string]workloadSpec{
	"TR-VIRT-002": {snapshotClone: true},
	"TR-VIRT-001": {snapshotClone: true},
	"TR-STOR-001": {},
	"TR-VIRT-018": {checks: cloneAtScaleChecks, snapshotClone: true},
	"TR-VIRT-008": {checks: drainChecks},
	"TR-STOR-002": {checks: fioChecks},
	"TR-VIRT-013": {checks: singleNodeChecks, snapshotClone: true},
	"TR-VIRT-007": {checks: failureRecoveryChecks},
}

func workloadChecks(rc *core.RunCtx, s *session, trs []core.TestRequirement) []common.PreflightCheck {
	replayPath := replayDir(trs)
	replay := replayPath != ""
	checks := make([]common.PreflightCheck, 0, len(trs)*4+1)
	if replay {
		checks = append(checks, common.PathCheck(common.PathRequirement{Name: "virtbench replay results_dir", Path: replayPath}))
	}
	checks = append(checks, withReplay(common.BinaryCheck(common.BinaryRequirement{Name: string(core.ToolVirtbench), Workload: string(core.ToolVirtbench)}), replay))
	for _, tr := range trs {
		storage := storageClass(rc, tr)
		checks = append(checks, withReplay(common.RequiredValueCheck(tr.ID+" storage_class", storage), replay))
		spec, ok := workloadSpecs[tr.ID]
		if !ok {
			checks = append(checks, common.RequiredValueCheck(tr.ID+" workload declaration", ""))
			continue
		}
		if spec.checks != nil {
			for _, check := range spec.checks(rc, tr) {
				checks = append(checks, withReplay(check, replay))
			}
		}
		if spec.snapshotClone && usesSnapshot(tr) {
			checks = append(checks, withReplay(snapshotCloneBinding(rc, s, tr), replay))
		}
	}
	return checks
}

func cloneAtScaleChecks(_ *core.RunCtx, tr core.TestRequirement) []common.PreflightCheck {
	return []common.PreflightCheck{common.TemplateDiskCountCheck(tr.ID+" clone template", intParam(tr, "num_disks", cloneAtScaleDefaultDisks), cloneAtScaleDefaultDisks, strParamOr(tr, "vm_template", ""), templateDiskCount)}
}

func drainChecks(_ *core.RunCtx, tr core.TestRequirement) []common.PreflightCheck {
	return []common.PreflightCheck{
		common.BinaryCheck(common.BinaryRequirement{Name: "kubectl", Workload: tr.ID}),
		common.RequiredValueCheck(tr.ID+" target_node", strParamOr(tr, "target_node", "")),
		common.SchedulableWorkerCheck(common.WorkerRequirement{Name: tr.ID + " drain workers", Kubeconfig: strParamOr(tr, "kubeconfig", ""), MinWorkers: 2}),
	}
}

func fioChecks(_ *core.RunCtx, tr core.TestRequirement) []common.PreflightCheck {
	checks := []common.PreflightCheck{
		common.CommandCheck(common.CommandRequirement{Binary: string(core.ToolVirtbench), Args: []string{"fio", "--help"}, Workload: tr.ID}),
		common.IntegerBoundsCheck(tr.ID+" num_vms", intParam(tr, "num_vms", 0), 1, 0),
		common.EnumCheck(tr.ID+" profile", strParamOr(tr, "profile", ""), []string{"load-80"}, true),
	}
	if template := strParamOr(tr, "fio_vm_template", ""); template != "" {
		checks = append(checks, common.PathCheck(common.PathRequirement{Name: tr.ID + " fio_vm_template", Path: template, RejectDirectory: true}))
	}
	return checks
}

func singleNodeChecks(_ *core.RunCtx, tr core.TestRequirement) []common.PreflightCheck {
	checks := []common.PreflightCheck{
		common.IntegerBoundsCheck(tr.ID+" attempt_vms", intParam(tr, "attempt_vms", singleNodeDefaultCount), 1, 0),
		common.TemplateDiskCountCheck(tr.ID+" clone template", intParam(tr, "num_disks", 1), 1, strParamOr(tr, "vm_template", ""), templateDiskCount),
	}
	node := strParamOr(tr, "node_name", "")
	workerCheck := common.SchedulableWorkerCheck(common.WorkerRequirement{Name: tr.ID + " pinned worker", Kubeconfig: strParamOr(tr, "kubeconfig", ""), Node: node})
	workerCheck.Applies = func() (bool, string) { return node != "", "no node_name pin" }
	checks = append(checks, workerCheck)
	return checks
}

func failureRecoveryChecks(_ *core.RunCtx, tr core.TestRequirement) []common.PreflightCheck {
	mode := strParamOr(tr, "mode", "far-operator")
	checks := []common.PreflightCheck{
		common.IntegerBoundsCheck(tr.ID+" vm_count", intParam(tr, "vm_count", 3), 1, 0),
		common.EnumCheck(tr.ID+" failure-recovery mode", mode, []string{"monitor", "far-operator"}, false),
	}
	if mode == "monitor" {
		consent := "false"
		if boolParam(tr, "allow_monitor_mode") {
			consent = strconv.FormatBool(true)
		}
		checks = append(checks, common.EnumCheck(tr.ID+" allow_monitor_mode", consent, []string{"true"}, false))
	}
	if mode == "far-operator" {
		checks = append(checks, common.RequiredValueCheck(tr.ID+" far_config", strParamOr(tr, "far_config", "")))
	}
	return checks
}

func withReplay(check common.PreflightCheck, replay bool) common.PreflightCheck {
	if replay {
		check.Applies = func() (bool, string) { return false, "replay mode" }
	}
	return check
}
