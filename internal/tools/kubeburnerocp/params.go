package kubeburnerocp

import (
	"fmt"
	"strconv"

	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/core"
)

const (
	WorkloadPVCDensity        = "pvc-density"
	WorkloadPVCDensityFourPVC = "pvc-density-four-pvc-per-pod"
	WorkloadVirtMigration     = "virt-migration"
	WorkloadVirtParallel      = "virt-parallel"
	resultsSubdir             = "kubeburner-results"
)

// Params identifies the workload; per-flag values stay in Raw (the plan's TR
// params) and are type-checked as they render via workloadSpecs.
type Params struct {
	Workload string
	Raw      map[string]any
}

func resolveParams(trs []core.TestRequirement) Params {
	if len(trs) == 0 || trs[0].Params == nil {
		return Params{}
	}
	p := Params{Raw: trs[0].Params}
	if v, ok := asString(p.Raw["workload"]); ok {
		p.Workload = v
	}
	return p
}

func (p Params) validate() error {
	if p.Workload == "" {
		return fmt.Errorf("kube-burner-ocp: missing required param %q", "workload")
	}
	if _, ok := workloadSpecs[p.Workload]; !ok {
		return fmt.Errorf("kube-burner-ocp: unknown workload %q", p.Workload)
	}
	if p.Workload == WorkloadPVCDensityFourPVC {
		if _, _, _, err := fourPVCInputs(p.Raw); err != nil {
			return err
		}
	}
	return nil
}

func asString(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case fmt.Stringer:
		return t.String(), true
	default:
		return "", false
	}
}

func asInt(v any) (int, bool) {
	switch t := v.(type) {
	case int:
		return t, true
	case int64:
		return int(t), true
	case float64:
		return int(t), true
	case string:
		n, err := strconv.Atoi(t)
		return n, err == nil
	default:
		return 0, false
	}
}

func asBool(v any) (bool, bool) {
	switch t := v.(type) {
	case bool:
		return t, true
	case string:
		b, err := strconv.ParseBool(t)
		return b, err == nil
	default:
		return false, false
	}
}
