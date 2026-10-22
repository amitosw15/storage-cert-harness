package kubeburner

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/clustercheck"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/safefs"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/tools/common"
)

type preflight struct{}

func (preflight) Check(ctx context.Context, rc *core.RunCtx, bag *core.Bag, trs []core.TestRequirement) ([]core.Finding, error) {
	stage := preflightStage
	stage.Checks = func(rc *core.RunCtx, resolved []common.ResolvedRequirement[Params]) []common.PreflightCheck {
		return snapshotChecks(rc, bag, resolved)
	}
	_, findings := stage.Check(ctx, rc, trs)
	if len(trs) != 1 {
		findings = append(findings, core.Finding{Level: "error", Message: fmt.Sprintf("kube-burner: expected exactly 1 TR, got %d", len(trs))})
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
