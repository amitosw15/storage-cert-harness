package kubeburner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/clustercheck"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/safefs"
)

// snapshotCRD is the KubeVirt VirtualMachineSnapshot capability this tool needs.
var snapshotCRD = clustercheck.CRD("virtualmachinesnapshots.snapshot.kubevirt.io")

type preflight struct{}

func (preflight) Check(ctx context.Context, rc *core.RunCtx, bag *core.Bag, trs []core.TestRequirement) ([]core.Finding, error) {
	var findings []core.Finding
	needsSnapshot := false
	// Validate every selected TR's params, not just the first.
	for i := range trs {
		params := resolveParams(trs[i : i+1])
		needsSnapshot = needsSnapshot || params.SnapshotCount > 0
		if err := params.validateFor(trs[i].ID); err != nil {
			findings = append(findings, core.Finding{Level: "error", Message: trs[i].ID + " parameters: " + err.Error()})
		} else {
			findings = append(findings, core.Finding{Level: "info", Message: trs[i].ID + " parameters valid"})
		}
	}
	p := resolveParams(trs)
	bag.Set("params", p)

	sc := ""
	if rc.Backend != nil {
		sc = rc.Backend.StorageClass
	}
	if sc == "" {
		findings = append(findings, core.Finding{
			Level:   "error",
			Message: "kube-burner: storage_class required (pass --backends/--backend)",
		})
	} else {
		findings = append(findings, core.Finding{Level: "info", Message: "storage_class=" + sc})
	}

	if len(trs) == 1 && trs[0].ID == "TR-VIRT-027" && p.SnapshotCount == 0 {
		findings = append(findings, core.Finding{Level: "skip", Message: "TR-VIRT-027 snapshot readiness: reduced mode creates the source volume without snapshots; the 250-snapshot SLA will fail"})
	}
	if _, err := exec.LookPath("kube-burner"); err != nil {
		findings = append(findings, core.Finding{Level: "error", Message: fmt.Sprintf("host execution needs %q on PATH: %v", "kube-burner", err)})
	} else {
		findings = append(findings, core.Finding{Level: "info", Message: "host binary found: kube-burner"})
	}
	caps := []clustercheck.Capability{clustercheck.KubeVirt, clustercheck.CDI, snapshotCRD}
	findings = append(findings, clustercheck.Preflight(ctx, caps, sc)...)
	if sc != "" && needsSnapshot {
		selection, err := clustercheck.ResolveSnapshot(ctx, sc, rc.Backend.SnapshotClass, "", true)
		if err != nil {
			findings = append(findings, core.Finding{Level: "error", Message: err.Error()})
		} else {
			bag.Set("snapshot_selection", selection)
			findings = append(findings, selection.Finding())
			// Provisioning this storage uses "wait --for=create".
			findings = append(findings, clustercheck.WaitForCreateSupported(ctx, ""))
		}
	}
	cli := clustercheck.KubeCLI()
	if cli == "" {
		return append(findings, core.Finding{Level: "skip", Message: "CDI VolumeImportSource populator: neither kubectl nor oc found on PATH"}), nil
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

func (provisioner) Provision(ctx context.Context, rc *core.RunCtx, bag *core.Bag, _ []core.TestRequirement) error {
	if selection, ok := core.GetAs[clustercheck.SnapshotSelection](bag, "snapshot_selection"); ok && rc.Backend != nil && rc.Backend.SnapshotClass != "" {
		// Pin the resolved class for the whole run through a private profile.
		created, err := selection.PrepareVMStorage(ctx)
		bag.Set("snapshot_storage_class", created)
		if err != nil {
			return fmt.Errorf("prepare snapshot storage: %w", err)
		}
	}
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
