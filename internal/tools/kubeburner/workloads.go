package kubeburner

import (
	"github.com/openshift-storage-cert/storage-cert-harness/internal/clustercheck"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/tools/common"
)

type snapshotScenario struct {
	SnapshotsPerVolume bool
	SingleSourceVolume bool
	AllowZeroSnapshots bool
	Capabilities       []clustercheck.Capability
	SnapshotCheckName  string
	SnapshotSkipReason string
}

var snapshotCRD = clustercheck.CRD("virtualmachinesnapshots.snapshot.kubevirt.io")

var snapshotScenarios = map[string]snapshotScenario{
	"TR-VIRT-010": {
		Capabilities: []clustercheck.Capability{
			clustercheck.KubeVirt,
			clustercheck.CDI,
			snapshotCRD,
		},
		SnapshotCheckName: "snapshot readiness",
	},
	"TR-VIRT-027": {
		SnapshotsPerVolume: true,
		SingleSourceVolume: true,
		AllowZeroSnapshots: true,
		Capabilities: []clustercheck.Capability{
			clustercheck.KubeVirt,
			clustercheck.CDI,
			snapshotCRD,
		},
		SnapshotCheckName:  "TR-VIRT-027 snapshot readiness",
		SnapshotSkipReason: "reduced mode creates the source volume without snapshots; the 250-snapshot SLA will fail",
	},
}

var preflightStage = common.Preflight[Params]{
	Tool:    core.ToolKubeBurner,
	Resolve: resolveParams,
	Workload: func(tr core.TestRequirement, _ Params) common.WorkloadRequirements {
		return common.WorkloadRequirements{Capabilities: snapshotScenarios[tr.ID].Capabilities}
	},
}

func snapshotChecks(rc *core.RunCtx, bag *core.Bag, resolved []common.ResolvedRequirement[Params]) []common.PreflightCheck {
	var checks []common.PreflightCheck
	storageClass := common.BackendStorageClass(rc)
	snapshotClass := ""
	if rc != nil && rc.Backend != nil {
		snapshotClass = rc.Backend.SnapshotClass
	}
	for i := range resolved {
		requirement := &resolved[i]
		bag.Set("params", requirement.Params)
		scenario, ok := snapshotScenarios[requirement.TR.ID]
		if !ok {
			continue
		}

		checks = append(checks, common.SnapshotSupportCheck(common.SnapshotSupport{
			Name:            scenario.SnapshotCheckName,
			StorageClass:    storageClass,
			SnapshotClass:   snapshotClass,
			VMSnapshot:      true,
			RequireVMClient: true,
			SnapshotCount:   requirement.Params.SnapshotCount,
			SkipReason:      scenario.SnapshotSkipReason,
			Record: func(selection clustercheck.SnapshotSelection) {
				bag.Set("snapshot_selection", selection)
			},
		}))
		checks = append(checks, common.VolumePopulatorCheck(func(registered bool) {
			requirement.Params.UsePopulator = registered
			bag.Set("params", requirement.Params)
		}))
	}
	return checks
}
