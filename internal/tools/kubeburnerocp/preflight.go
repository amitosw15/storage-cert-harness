package kubeburnerocp

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/safefs"
)

type preflight struct{}

func (preflight) Check(ctx context.Context, rc *core.RunCtx, bag *core.Bag, trs []core.TestRequirement) ([]core.Finding, error) {
	_, findings := preflightStage.Check(ctx, rc, trs)
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
