// Package virtbench
// TR-VIRT-018 ("VM Cloning at Scale", 2 disks per VM) rides the generic
// engine like every other datasource-clone scenario, but needs its own
// BuildArgs: virtbench's --num-disks flag only labels output, it never adds
// a disk to the VM (upstream measure-vm-creation-time.py uses it purely for
// the results-directory name and reporting) — the real disk count comes
// entirely from the vm_template. So it embeds a real 2-disk default template
// and validates any plan-supplied vm_template/num_disks against each other
// (via templateDiskCount, shared with TR-VIRT-013's single-node ceiling —
// singlenode.go), the same way that scenario keeps num_disks honest for its
// own max_luns_per_node metric.
package virtbench

import (
	"fmt"
	"path/filepath"
	"strconv"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/safefs"
)

const (
	twoDiskVMTemplateFile = "rhel9-vm-2disk.yaml"
	// cloneAtScaleDefaultDisks is both the num_disks default and the built-in
	// template's real disk count (asserted by
	// TestCloneAtScaleArgsStagedTemplateHasTwoDataVolumes).
	cloneAtScaleDefaultDisks = 2
)

// cloneAtScaleArgs is TR-VIRT-018's BuildArgs: the same shape as
// datasourceCloneArgs (start/end, storage class, namespace-prefix,
// save-results, cleanup), plus a vm_template/num_disks pair that
// cloneAtScaleValidate has already confirmed agree (default: the embedded
// 2-disk template, num_disks 2).
func cloneAtScaleArgs(nsPrefix string) func(rc *core.RunCtx, tr core.TestRequirement, resultsRoot string) ([]string, error) {
	return func(rc *core.RunCtx, tr core.TestRequirement, resultsRoot string) ([]string, error) {
		sc := storageClass(rc, tr)
		if sc == "" {
			return nil, fmt.Errorf("virtbench: %s: storage_class required (backend or 'storage_class' param)", tr.ID)
		}
		staged, err := stageCloneAtScaleTemplate(resultsRoot, tr)
		if err != nil {
			return nil, fmt.Errorf("virtbench: %s: %w", tr.ID, err)
		}
		disks := intParam(tr, "num_disks", cloneAtScaleDefaultDisks)
		start := intParam(tr, "start", 1)
		end := intParam(tr, "iteration_clones", 100)

		args := clusterArgs(tr)
		args = append(args, "datasource-clone",
			"--start", strconv.Itoa(start),
			"--end", strconv.Itoa(end),
			"--vm-name", defaultVMName,
			"--namespace-prefix", strParamOr(tr, "namespace_prefix", nsPrefix),
			"--storage-class", sc,
			"--vm-template", staged,
			"--num-disks", strconv.Itoa(disks),
			"--save-results",
			"--results-folder", resultsRoot,
			"--cleanup", "--cleanup-on-failure", "--yes",
		)
		return args, nil
	}
}

// stageCloneAtScaleTemplate stages a plan-supplied vm_template (reusing the
// shared stageTemplate, engine.go — the same helper TR-VIRT-013's single-node
// ceiling uses for the same reason) or, absent one, the embedded default.
func stageCloneAtScaleTemplate(resultsRoot string, tr core.TestRequirement) (string, error) {
	if tmpl := strParamOr(tr, "vm_template", ""); tmpl != "" {
		return stageTemplate(resultsRoot, tmpl)
	}
	dst := filepath.Join(resultsRoot, twoDiskVMTemplateFile)
	if err := safefs.WriteFile(dst, []byte(twoDiskVMTemplate), 0o600); err != nil {
		return "", fmt.Errorf("stage embedded 2-disk vm template: %w", err)
	}
	return filepath.Abs(dst)
}

// cloneAtScaleValidate checks num_disks and vm_template agree before setup
// creates cluster resources — same rule and reason as TR-VIRT-013's
// singleNodeCeilingValidate (singlenode.go), reusing its templateDiskCount:
// a mismatch would silently under- or over-report what was actually cloned.
func cloneAtScaleValidate(tr core.TestRequirement) error {
	disks := intParam(tr, "num_disks", cloneAtScaleDefaultDisks)
	if disks < 1 {
		return fmt.Errorf("virtbench: %s: num_disks must be >= 1", tr.ID)
	}
	tmpl := strParamOr(tr, "vm_template", "")
	if tmpl == "" {
		if disks != cloneAtScaleDefaultDisks {
			return fmt.Errorf("virtbench: %s: num_disks=%d requires a vm_template that defines that many disks (the built-in default template defines %d)", tr.ID, disks, cloneAtScaleDefaultDisks)
		}
		return nil
	}
	got, err := templateDiskCount(tmpl)
	if err != nil {
		return fmt.Errorf("virtbench: %s: cannot count disks in vm_template %q: %w", tr.ID, tmpl, err)
	}
	if got != disks {
		return fmt.Errorf("virtbench: %s: num_disks=%d but vm_template %q defines %d disk(s); they must match", tr.ID, disks, tmpl, got)
	}
	return nil
}
