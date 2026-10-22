package kubeburner

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/safefs"
)

func (s *session) Provision(ctx context.Context, rc *core.RunCtx) error {
	runs := s.executions
	if runs == nil {
		return fmt.Errorf("kube-burner: parameters missing (preflight first)")
	}
	if rc.Backend != nil && rc.Backend.SnapshotClass != "" {
		for i := range runs {
			if runs[i].Selection.SnapshotClass == "" {
				continue
			}
			created, err := runs[i].Selection.PrepareVMStorage(ctx)
			runs[i].SnapshotStorageClass = created
			if err != nil {
				return fmt.Errorf("%s: prepare snapshot storage: %w", runs[i].Requirement.TR.ID, err)
			}
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
	s.resultsDir = resultsDir
	rc.Logger.Info("kube-burner: provisioned results dir", "dir", resultsDir)
	return nil
}
