package kubeburnerocp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/clustercheck"
	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/core"
	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/safefs"
)

type preflight struct{}

func (preflight) Check(ctx context.Context, rc *core.RunCtx, bag *core.Bag, trs []core.TestRequirement) ([]core.Finding, error) {
	p := resolveParams(trs)
	bag.Set("params", p)

	var findings []core.Finding
	for _, tr := range trs {
		params := resolveParams([]core.TestRequirement{tr})
		if err := params.validate(); err != nil {
			findings = append(findings, core.Finding{Level: "error", Message: tr.ID + " parameters: " + err.Error()})
			findings = append(findings, core.Finding{Level: "skip", Message: tr.ID + " workload prerequisites: cannot select checks without a valid workload"})
		} else {
			findings = append(findings, core.Finding{Level: "info", Message: tr.ID + " workload=" + params.Workload})
		}
	}

	if rc.Backend != nil && rc.Backend.StorageClass != "" {
		findings = append(findings, core.Finding{Level: "info", Message: "storage_class=" + rc.Backend.StorageClass})
	}

	seenHost := map[string]bool{}
	for _, bin := range []string{"kube-burner-ocp"} {
		if _, err := exec.LookPath(bin); err != nil {
			findings = append(findings, core.Finding{Level: "error",
				Message: fmt.Sprintf("host execution needs %q on PATH: %v", bin, err)})
		} else {
			findings = append(findings, core.Finding{Level: "info", Message: "host binary found: " + bin})
		}
	}
	for _, tr := range trs {
		trParams := resolveParams([]core.TestRequirement{tr})
		for _, bin := range workloadSpecs[trParams.Workload].hostBins {
			if seenHost[bin] {
				continue
			}
			seenHost[bin] = true
			if _, err := exec.LookPath(bin); err != nil {
				findings = append(findings, core.Finding{Level: "error",
					Message: fmt.Sprintf("workload %s needs %q on PATH: %v", trParams.Workload, bin, err)})
			} else {
				findings = append(findings, core.Finding{Level: "info", Message: "host binary found: " + bin})
			}
		}
	}

	cli := clustercheck.KubeCLI()

	// Run the union of every selected workload's cluster prerequisites, once each.
	seen := map[string]bool{}
	for _, tr := range trs {
		for _, c := range workloadSpecs[resolveParams([]core.TestRequirement{tr}).Workload].prereqs {
			if seen[c.Name] {
				continue
			}
			seen[c.Name] = true
			if cli == "" {
				findings = append(findings, core.Finding{Level: "skip", Message: c.Name + ": neither kubectl nor oc found on PATH"})
			} else {
				findings = append(findings, c.Check(ctx, cli))
			}
		}
	}

	return findings, nil
}

type provisioner struct{}

func (provisioner) Provision(_ context.Context, rc *core.RunCtx, bag *core.Bag, _ []core.TestRequirement) error {
	dir := rc.WorkDir
	if dir == "" {
		dir = os.TempDir()
	}
	resultsDir := strings.TrimRight(dir, "/") + "/" + resultsSubdir
	if err := safefs.MkdirAll(resultsDir, 0o755); err != nil {
		return fmt.Errorf("kube-burner-ocp: results dir: %w", err)
	}
	bag.Set("results_dir", resultsDir)
	rc.Logger.Info("kube-burner-ocp: provisioned results dir", "dir", resultsDir)
	return nil
}
