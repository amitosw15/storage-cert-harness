package kubeburner

import (
	"context"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/clustercheck"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/stages"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/tools/common"
)

type session struct {
	resultsDir string
	executions []execution
}

type execution struct {
	Requirement          common.ResolvedRequirement[Params]
	Selection            clustercheck.SnapshotSelection
	SnapshotStorageClass string
	Subdir               string
	RunErr               string
}

var _ stages.Session = (*session)(nil)

func (s *session) Preflight(ctx context.Context, rc *core.RunCtx, trs []core.TestRequirement) ([]core.Finding, error) {
	preflight := preflightStage
	preflight.Checks = s.snapshotChecks
	_, findings := preflight.Check(ctx, rc, trs)
	return findings, nil
}
