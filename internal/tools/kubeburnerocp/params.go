package kubeburnerocp

import (
	"fmt"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/tools/common"
)

const (
	WorkloadPVCDensity    = "pvc-density"
	WorkloadVirtMigration = "virt-migration"
	WorkloadVirtParallel  = "virt-parallel"
	resultsSubdir         = "kubeburner-results"
)

// Params identifies a workload and retains its raw parameters for typed CLI rendering.
type Params struct {
	Workload string
	Raw      map[string]any
}

var _ common.RequirementValidator = Params{}

func resolveParams(tr core.TestRequirement) Params {
	if tr.Params == nil {
		return Params{}
	}
	p := Params{Raw: tr.Params}
	if v, ok := core.AsString(p.Raw["workload"]); ok {
		p.Workload = v
	}
	return p
}

func (p Params) validate() error {
	if p.Workload == "" {
		return fmt.Errorf("kube-burner-ocp: missing required param %q", "workload")
	}
	spec, ok := workloadSpecs[p.Workload]
	if !ok {
		return fmt.Errorf("kube-burner-ocp: unknown workload %q", p.Workload)
	}
	// Validate against the same flag specifications used by the runner.
	for _, set := range [][]argSpec{spec.args, globalArgs} {
		for _, a := range set {
			if _, err := renderArg(p.Raw, a); err != nil {
				return err
			}
		}
	}
	return nil
}

// Validate checks the workload and its required arguments for one test requirement.
func (p Params) Validate(_ core.TestRequirement) error {
	return p.validate()
}
