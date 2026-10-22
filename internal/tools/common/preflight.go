// Package common contains support shared by tool adapters.
package common

import (
	"context"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/clustercheck"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
)

// RequirementValidator checks whether one test requirement is valid for an adapter.
type RequirementValidator interface {
	Validate(core.TestRequirement) error
}

// ResolvedRequirement holds one TR's params and validation result.
type ResolvedRequirement[P RequirementValidator] struct {
	TR     core.TestRequirement
	Params P
	Err    error
}

// Preflight coordinates parameter resolution and workload-declared checks.
type Preflight[P RequirementValidator] struct {
	Tool    core.ToolName
	Resolve func(core.TestRequirement) P
	// Requirements apply to every workload of this adapter.
	Requirements WorkloadRequirements
	// Workload selects prerequisites using the resolved parameters of one TR.
	Workload func(core.TestRequirement, P) WorkloadRequirements
	// Checks optionally binds additional checks to adapter-specific run state.
	Checks func(*core.RunCtx, []ResolvedRequirement[P]) []PreflightCheck
}

// Check resolves each TR and executes the checks required by its workload.
func (p Preflight[P]) Check(ctx context.Context, rc *core.RunCtx, trs []core.TestRequirement) ([]ResolvedRequirement[P], []core.Finding) {
	resolved, findings := ResolveParams(trs, p.Resolve)
	checks := p.workloadChecks(rc, resolved)
	if p.Checks != nil {
		checks = append(checks, p.Checks(rc, resolved)...)
	}
	findings = append(findings, RunPreflightChecks(ctx, checks)...)
	return resolved, findings
}

func (p Preflight[P]) workloadChecks(rc *core.RunCtx, resolved []ResolvedRequirement[P]) []PreflightCheck {
	storageClass := BackendStorageClass(rc)
	checks := make([]PreflightCheck, 0, len(resolved)+1)
	for _, requirement := range resolved {
		checks = append(checks, ToolIdentityCheck(requirement.TR, p.Tool))
	}
	checks = append(checks, StorageClassCheck(rc), BinaryCheck(BinaryRequirement{Name: string(p.Tool)}))
	checks = append(checks, BuildRequirementChecks(p.Requirements.Binaries, p.Requirements.Capabilities, storageClass)...)
	checks = append(checks, CapabilityCheck(clustercheck.StorageClass(storageClass), storageClass))
	for _, requirement := range resolved {
		workload := p.Workload(requirement.TR, requirement.Params)
		checks = append(checks, BuildRequirementChecks(workload.Binaries, workload.Capabilities, storageClass)...)
	}
	return checks
}

// ResolveParams resolves and validates the parameters of every selected TR.
func ResolveParams[P RequirementValidator](trs []core.TestRequirement, resolve func(core.TestRequirement) P) ([]ResolvedRequirement[P], []core.Finding) {
	resolved := make([]ResolvedRequirement[P], 0, len(trs))
	findings := make([]core.Finding, 0, len(trs))
	for _, tr := range trs {
		params := resolve(tr)
		err := params.Validate(tr)
		resolved = append(resolved, ResolvedRequirement[P]{TR: tr, Params: params, Err: err})
		if err != nil {
			findings = append(findings, core.Finding{Level: "error", Message: tr.ID + " parameters: " + err.Error()})
		} else {
			findings = append(findings, core.Finding{Level: "info", Message: tr.ID + " parameters valid"})
		}
	}
	return resolved, findings
}
