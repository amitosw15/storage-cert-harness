package kubeburnerocp

import (
	"github.com/openshift-storage-cert/storage-cert-harness/internal/clustercheck"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/tools/common"
)

// workloadSpec declares a workload CLI, result mapping, and prerequisites.
type workloadSpec struct {
	sub          string
	scFlag       string
	args         []argSpec
	requirements common.WorkloadRequirements
	mapResults   resultMapper
}

var workloadSpecs = map[string]workloadSpec{
	WorkloadPVCDensity: {
		sub:        "pvc-density",
		scFlag:     "--storage-class-name",
		mapResults: mapPVCDensityResults,
		args: []argSpec{
			{flag: "--iterations", param: "iterations", typ: argInt},
			{flag: "--claim-size", param: "claim_size", typ: argString},
			{flag: "--container-image", param: "container_image", typ: argString, optional: true},
		},
	},
	WorkloadVirtMigration: {
		sub:        "virt-migration",
		scFlag:     "--storage-class",
		mapResults: mapVirtMigrationResults,
		args: []argSpec{
			{flag: "--iterations", param: "iterations", typ: argInt},
			{flag: "--iteration-vms", param: "iteration_vms", typ: argInt},
			{flag: "--data-volume-count", param: "data_volume_count", typ: argInt, optional: true},
			{flag: "--migration-qps", param: "migration_qps", typ: argInt, optional: true},
			{flag: "--worker-node", param: "worker_node", typ: argString, optional: true},
		},
		requirements: common.WorkloadRequirements{
			Capabilities: []clustercheck.Capability{clustercheck.KubeVirt, clustercheck.CDI},
			Binaries:     []common.BinaryRequirement{{Name: "virtctl", Workload: WorkloadVirtMigration}},
		},
	},
	WorkloadVirtParallel: {
		sub:        "virt-parallel",
		scFlag:     "--storage-class",
		mapResults: mapVirtParallelResults,
		args: []argSpec{
			{flag: "--initial-vms", param: "initial_vms", typ: argInt, optional: true},
			{flag: "--increment", param: "increment", typ: argInt, optional: true},
			{flag: "--data-volume-count", param: "data_volume_count", typ: argInt, optional: true},
			{flag: "--max-iterations", param: "max_iterations", typ: argInt, optional: true},
			{flag: "--vm-image", param: "vm_image", typ: argString, optional: true},
			{flag: "--vm-cpu", param: "vm_cpu", typ: argString, optional: true},
			{flag: "--vm-memory", param: "vm_memory", typ: argString, optional: true},
			{flag: "--namespace", param: "namespace", typ: argString, optional: true},
			{flag: "--min-vol-size", param: "min_vol_size", typ: argInt, optional: true},
			{flag: "--min-vol-inc-size", param: "min_vol_inc_size", typ: argInt, optional: true},
			{flag: "--skip-migration-job", param: "skip_migration_job", typ: argBool, optional: true},
			{flag: "--skip-resize-job", param: "skip_resize_job", typ: argBool, optional: true},
			{flag: "--skip-restart-job", param: "skip_restart_job", typ: argBool, optional: true},
			{flag: "--skip-snapshot-job", param: "skip_snapshot_job", typ: argBool, optional: true},
		},
		requirements: common.WorkloadRequirements{
			Capabilities: []clustercheck.Capability{clustercheck.KubeVirt, clustercheck.CDI},
		},
	},
}

var preflightStage = common.Preflight[Params]{
	Tool:    core.ToolKubeBurnerOCP,
	Resolve: resolveParams,
	Requirements: common.WorkloadRequirements{
		Capabilities: []clustercheck.Capability{clustercheck.RequiredKubeCLI},
	},
	Workload: func(_ core.TestRequirement, params Params) common.WorkloadRequirements {
		return workloadSpecs[params.Workload].requirements
	},
}
