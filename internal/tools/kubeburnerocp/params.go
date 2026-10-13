package kubeburnerocp

import (
	"fmt"
	"maps"
	"strconv"

	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/core"
)

const (
	WorkloadPVCDensity    = "pvc-density"
	WorkloadVirtMigration = "virt-migration"
	WorkloadVirtParallel  = "virt-parallel"
	resultsSubdir         = "kubeburner-results"
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
	if p.Workload == WorkloadVirtParallel {
		_, err := virtParallelParams(p.Raw)
		return err
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
		n := int(t)
		return n, int64(n) == t
	case float64:
		n := int(t)
		return n, float64(n) == t
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

// vms is the catalog count; initial_vms is its upstream CLI spelling. Never
// silently run a different population from the plan's certification count.
func virtParallelParams(raw map[string]any) (map[string]any, error) {
	p := maps.Clone(raw)
	if p == nil {
		p = map[string]any{}
	}
	if v, present := p["vms"]; present {
		n, ok := asInt(v)
		if !ok || n <= 0 {
			return nil, fmt.Errorf("virt-parallel: vms must be a positive integer")
		}
		if initial, present := p["initial_vms"]; present {
			m, valid := asInt(initial)
			if !valid || m != n {
				return nil, fmt.Errorf("virt-parallel: initial_vms must match the effective plan/catalog vms count")
			}
		}
		p["initial_vms"] = n
	}
	for key, def := range map[string]int{"initial_vms": 5, "increment": 0, "max_iterations": 1} {
		if _, ok := p[key]; !ok {
			p[key] = def
		}
		n, valid := asInt(p[key])
		if !valid || n < 0 || (key != "increment" && n == 0) {
			return nil, fmt.Errorf("virt-parallel: %s must be a valid bounded integer", key)
		}
	}
	for _, flag := range []string{"skip_migration_job", "skip_resize_job", "skip_restart_job", "skip_snapshot_job"} {
		if val, exists := p[flag]; exists {
			b, ok := asBool(val)
			if !ok || b {
				return nil, fmt.Errorf("virt-parallel: certification requires %s=false", flag)
			}
		}
	}
	return p, nil
}
