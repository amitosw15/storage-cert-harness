package kubeburner

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

// snapshotCRD is the KubeVirt VirtualMachineSnapshot capability this tool needs.
var snapshotCRD = clustercheck.CRD("virtualmachinesnapshots.snapshot.kubevirt.io")

type preflight struct{}

func (preflight) Check(ctx context.Context, rc *core.RunCtx, bag *core.Bag, trs []core.TestRequirement) ([]core.Finding, error) {
	// Validate every selected TR's params, not just the first.
	for i := range trs {
		if err := resolveParams(trs[i : i+1]).validateFor(trs[i].ID); err != nil {
			return []core.Finding{{Level: "error", Message: err.Error()}}, nil
		}
	}
	p := resolveParams(trs)
	bag.Set("params", p)

	sc := ""
	if rc.Backend != nil {
		sc = rc.Backend.StorageClass
	}
	if sc == "" {
		return []core.Finding{{
			Level:   "error",
			Message: "kube-burner: storage_class required (pass --backends/--backend)",
		}}, nil
	}

	var findings []core.Finding
	findings = append(findings, core.Finding{
		Level:   "info",
		Message: fmt.Sprintf("kube-burner vm-snapshot replicas=%d snapshot_count=%d vm_image=%s", p.Replicas, p.SnapshotCount, p.VMImage),
	})
	if len(trs) == 1 && trs[0].ID == "TR-VIRT-027" && p.SnapshotCount == 0 {
		findings = append(findings, core.Finding{Level: "warn", Message: "TR-VIRT-027 reduced mode: creates the source volume without snapshots; the 250-snapshot SLA will fail"})
	}
	if _, err := exec.LookPath("kube-burner"); err != nil {
		findings = append(findings, core.Finding{Level: "error", Message: fmt.Sprintf("host execution needs %q on PATH: %v", "kube-burner", err)})
	} else {
		findings = append(findings, core.Finding{Level: "info", Message: "host binary found: kube-burner"})
	}
	if sc != "" {
		findings = append(findings, core.Finding{Level: "info", Message: "storage_class=" + sc})
	}
	for _, f := range findings {
		rc.Logger.Info(f.Message)
	}

	caps := []clustercheck.Capability{clustercheck.KubeVirt, clustercheck.CDI, snapshotCRD}
	findings = append(findings, clustercheck.Preflight(ctx, caps, sc)...)
	cli := clustercheck.KubeCLI()
	if cli == "" {
		return findings, nil
	}
	usePopulator, err := clustercheck.HasVolumePopulator(ctx, cli, "cdi.kubevirt.io", "VolumeImportSource")
	if err != nil {
		return append(findings, core.Finding{Level: "error", Message: fmt.Sprintf("kube-burner: checking CDI VolumeImportSource populator: %v", err)}), nil
	}
	p.UsePopulator = usePopulator
	bag.Set("params", p)
	if usePopulator {
		findings = append(findings, core.Finding{Level: "info", Message: "CDI VolumeImportSource populator registered; using populator imports"})
	} else {
		findings = append(findings, core.Finding{Level: "info", Message: "CDI VolumeImportSource populator not registered; using legacy CDI imports"})
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
		return fmt.Errorf("kube-burner: results dir: %w", err)
	}
	bag.Set("results_dir", resultsDir)
	rc.Logger.Info("kube-burner: provisioned results dir", "dir", resultsDir)
	return nil
}
