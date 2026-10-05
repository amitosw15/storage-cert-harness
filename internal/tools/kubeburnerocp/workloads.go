package kubeburnerocp

import (
	"fmt"
	"strconv"

	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/clustercheck"
)

type argType int

const (
	argString argType = iota
	argInt
	argBool
)

// argSpec declares one CLI flag sourced from a plan param, with its expected type
// (like a cobra IntVar/StringVar/BoolVar, but for building an outbound CLI).
// Required unless optional; bool renders as --flag=value, others as --flag value.
type argSpec struct {
	flag     string
	param    string
	typ      argType
	optional bool
}

// workloadSpec is the per-workload contract: subcommand, the storage-class flag
// (value from the backend), typed flags, and cluster prerequisites. Adding a
// workload is a new entry here — no engine-code changes.
type workloadSpec struct {
	sub             string
	scFlag          string
	args            []argSpec
	prereqs         []clustercheck.Capability
	hostBins        []string
	mapResults      resultMapper
	pvcPerIteration int
	customConfig    bool
}

var workloadSpecs = map[string]workloadSpec{
	WorkloadPVCDensity: {
		sub:             "pvc-density",
		scFlag:          "--storage-class-name",
		mapResults:      mapPVCDensityResults,
		pvcPerIteration: 1,
		args: []argSpec{
			{flag: "--iterations", param: "iterations", typ: argInt},
			{flag: "--claim-size", param: "claim_size", typ: argString},
			{flag: "--container-image", param: "container_image", typ: argString, optional: true},
		},
	},
	WorkloadPVCDensityFourPVC: {
		mapResults:      mapFourPVCDensityResults,
		pvcPerIteration: 4,
		customConfig:    true,
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
		prereqs:  []clustercheck.Capability{clustercheck.KubeVirt, clustercheck.CDI},
		hostBins: []string{"virtctl"},
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
		prereqs: []clustercheck.Capability{clustercheck.KubeVirt, clustercheck.CDI},
	},
}

// globalArgs are optional kube-burner-ocp flags shared by every workload.
var globalArgs = []argSpec{
	{flag: "--qps", param: "qps", typ: argInt, optional: true},
	{flag: "--burst", param: "burst", typ: argInt, optional: true},
	{flag: "--gc", param: "gc", typ: argBool, optional: true},
	{flag: "--timeout", param: "timeout", typ: argString, optional: true},
}

// buildCLIArgs renders a workload's subcommand + typed flags from the plan params
// (a missing required param fails; a type mismatch fails), plus shared global
// flags and the backend storage class.
func buildCLIArgs(p Params, storageClass string) ([]string, error) {
	spec, ok := workloadSpecs[p.Workload]
	if !ok {
		return nil, fmt.Errorf("kube-burner-ocp: unknown workload %q", p.Workload)
	}
	args := []string{spec.sub}
	for _, set := range [][]argSpec{spec.args, globalArgs} {
		for _, a := range set {
			seg, err := renderArg(p.Raw, a)
			if err != nil {
				return nil, err
			}
			args = append(args, seg...)
		}
	}
	args = append(args, "--local-indexing")
	if spec.scFlag != "" {
		if storageClass == "" {
			return nil, fmt.Errorf("kube-burner-ocp: workload %q requires storage_class (pass --backends/--backend)", p.Workload)
		}
		args = append(args, spec.scFlag, storageClass)
	}
	return args, nil
}

// renderArg validates the param against its declared type and returns its CLI
// segment(s); nil when an optional param is absent.
func renderArg(raw map[string]any, a argSpec) ([]string, error) {
	v, present := raw[a.param]
	if !present {
		if a.optional {
			return nil, nil
		}
		return nil, fmt.Errorf("kube-burner-ocp: missing required param %q", a.param)
	}
	switch a.typ {
	case argInt:
		n, ok := asInt(v)
		if !ok {
			return nil, fmt.Errorf("kube-burner-ocp: param %q must be an integer", a.param)
		}
		return []string{a.flag, strconv.Itoa(n)}, nil
	case argBool:
		b, ok := asBool(v)
		if !ok {
			return nil, fmt.Errorf("kube-burner-ocp: param %q must be a boolean", a.param)
		}
		return []string{a.flag + "=" + strconv.FormatBool(b)}, nil
	default:
		s, ok := asString(v)
		if !ok || s == "" {
			return nil, fmt.Errorf("kube-burner-ocp: param %q must be a non-empty string", a.param)
		}
		return []string{a.flag, s}, nil
	}
}
