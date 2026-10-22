package kubeburnerocp

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/safefs"
)

func (s *session) Provision(_ context.Context, rc *core.RunCtx) error {
	if s.executions == nil {
		return fmt.Errorf("kube-burner-ocp: parameters missing (preflight first)")
	}
	dir := rc.WorkDir
	if dir == "" {
		dir = os.TempDir()
	}
	resultsDir := strings.TrimRight(dir, "/") + "/" + resultsSubdir
	if err := safefs.MkdirAll(resultsDir, 0o755); err != nil {
		return fmt.Errorf("kube-burner-ocp: results dir: %w", err)
	}
	s.resultsDir = resultsDir
	rc.Logger.Info("kube-burner-ocp: provisioned results dir", "dir", resultsDir)
	return nil
}
