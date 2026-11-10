package virtbench

import (
	"context"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"strings"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/safefs"
)

// --- Preflight ---------------------------------------------------------------

type preflight struct{ sc Scenario }

func (p preflight) Check(ctx context.Context, rc *core.RunCtx, bag *core.Bag, trs []core.TestRequirement) ([]core.Finding, error) {
	var findings []core.Finding
	// Replay grades pre-collected results — no binary or cluster needed.
	dir := replayDir(trs)
	if dir != "" {
		if _, err := os.Stat(dir); err != nil {
			findings = append(findings, core.Finding{Level: "error", Message: fmt.Sprintf("virtbench: replay results_dir %q not accessible: %v", dir, err)})
		} else {
			findings = append(findings, core.Finding{Level: "info", Message: "virtbench: replay results_dir accessible: " + dir})
		}
	}

	if dir != "" {
		findings = append(findings, core.Finding{Level: "skip", Message: "virtbench CLI on PATH: replay mode"})
		if len(p.sc.CommandCheck) > 0 {
			findings = append(findings, core.Finding{Level: "skip", Message: "virtbench command " + p.sc.CommandCheck[0] + ": replay mode"})
		}
	} else if _, err := exec.LookPath(binary); err != nil {
		findings = append(findings, core.Finding{Level: "error", Message: "virtbench CLI not found on PATH (install per https://portworx.github.io/kubevirt-benchmark/install/, or use replay mode via the results_dir param)"})
		if len(p.sc.CommandCheck) > 0 {
			findings = append(findings, core.Finding{Level: "skip", Message: "virtbench command " + p.sc.CommandCheck[0] + ": virtbench CLI not found on PATH"})
		}
	} else {
		findings = append(findings, core.Finding{Level: "info", Message: "virtbench CLI found on PATH"})
		if len(p.sc.CommandCheck) > 0 {
			cmd := exec.CommandContext(ctx, binary, p.sc.CommandCheck...) // #nosec G204 -- command checks are declared by built-in scenarios.
			if output, err := cmd.CombinedOutput(); err != nil {
				findings = append(findings, core.Finding{Level: "error", Message: fmt.Sprintf("virtbench: required command %q unavailable: %v: %s", p.sc.CommandCheck[0], err, strings.TrimSpace(string(output)))})
			} else {
				findings = append(findings, core.Finding{Level: "info", Message: "virtbench command available: " + p.sc.CommandCheck[0]})
			}
		}
	}
	for _, tr := range trs {
		if dir == "" {
			refs, err := datasourcePreflight(ctx, rc, tr)
			if err != nil {
				findings = append(findings, core.Finding{Level: "error", Message: tr.ID + " datasource: " + err.Error()})
			} else {
				for _, ref := range refs {
					findings = append(findings, core.Finding{Level: "info", Message: fmt.Sprintf("%s datasource=%s/%s", tr.ID, ref.Namespace, ref.Name)})
				}
			}
		}

		if dir == "" && storageClass(rc, tr) != "" {
			findings = append(findings, vmStoragePreflight(ctx, rc, tr)...)
		}

		if dir == "" && usesSnapshot(tr) && strings.HasPrefix(p.sc.AutomationTool, "virtbench datasource-clone") {
			clone, err := snapshotClonePreflight(ctx, rc, tr)
			if err != nil {
				findings = append(findings, core.Finding{Level: "error", Message: tr.ID + " snapshot clone: " + err.Error()})
			} else {
				bag.Set("snapshot_clone", clone)
				findings = append(findings, clone.Selection.Finding())
			}
		}
		if dir != "" {
			findings = append(findings, core.Finding{Level: "skip", Message: tr.ID + " storage_class: replay mode"})
		} else if sc := storageClass(rc, tr); sc == "" && !p.sc.StorageClassOptional {
			findings = append(findings, core.Finding{Level: "error", Message: fmt.Sprintf("virtbench: %s has no storage_class (select a --backend or set the TR 'storage_class' param)", tr.ID)})
		} else {
			findings = append(findings, core.Finding{Level: "info", Message: tr.ID + " storage_class=" + sc})
		}
		if p.sc.Validate != nil {
			validationTR := tr
			validationTR.Params = maps.Clone(tr.Params)
			if validationTR.Params == nil {
				validationTR.Params = map[string]any{}
			}
			validationTR.Params["storage_class"] = storageClass(rc, tr)
			if dir != "" {
				findings = append(findings, core.Finding{Level: "skip", Message: tr.ID + " scenario parameters: replay mode"})
			} else if err := p.sc.Validate(validationTR); err != nil {
				findings = append(findings, core.Finding{Level: "error", Message: tr.ID + " scenario parameters: " + err.Error()})
			} else {
				findings = append(findings, core.Finding{Level: "info", Message: tr.ID + " scenario parameters valid"})
			}
		}
	}
	if p.sc.Preflight != nil {
		findings = append(findings, p.sc.Preflight(ctx, rc, trs)...)
	}
	return findings, nil
}

// --- Provision ---------------------------------------------------------------

type provisioner struct{ sc Scenario }

func (p provisioner) Provision(ctx context.Context, rc *core.RunCtx, bag *core.Bag, trs []core.TestRequirement) error {
	if len(trs) > 0 {
		bag.Set("tr_id", trs[0].ID)
		bag.Set("ns_prefix", strParamOr(trs[0], "namespace_prefix", p.sc.NSPrefix))
		bag.Set("kubeconfig", strParamOr(trs[0], "kubeconfig", ""))
	}
	if dir := replayDir(trs); dir != "" {
		bag.Set("replay", true)
		bag.Set("results_root", dir)
		rc.Logger.Info("virtbench: replay mode", "results_dir", dir)
		return nil
	}
	// A preceding test (notably node drain) may have evicted the shared helper.
	// Recheck before scenario provisioning, which can itself invoke virtbench.
	if err := sharedSSHSetup.Setup(ctx, rc, trs); err != nil {
		return fmt.Errorf("virtbench: ensure ssh helper pod: %w", err)
	}
	// Every execution gets its own root. Several scenarios write the same
	// filenames (datasource-clone writes vm_creation_results.json whether it
	// runs for TR-VIRT-001, -002, -013, -018 or the drain provisioning step),
	// so a root shared across TRs lets Collect pick up another test's results.
	var trID, variant string
	if len(trs) > 0 {
		trID, variant = trs[0].ID, trs[0].Variant
	}
	root := rc.ExecutionDir(resultsSubdir, trID, variant)
	if err := safefs.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("virtbench: create results dir: %w", err)
	}
	bag.Set("replay", false)
	bag.Set("results_root", root)
	rc.Logger.Debug("virtbench: provisioned results dir", "results_root", root)
	// A scenario that must stage the cluster before Run does it here (drain clones
	// the VMs onto the target); the generic scenarios have no extra provisioning.
	if p.sc.Provision != nil && len(trs) > 0 {
		if err := p.sc.Provision(ctx, rc, bag, trs[0], root); err != nil {
			return err
		}
	}
	if p.sc.ProvisionVMs != nil {
		if err := p.sc.ProvisionVMs(ctx, rc, bag, trs); err != nil {
			return err
		}
	}
	return nil
}
