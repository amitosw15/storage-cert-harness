package kubeburnerocp

import (
	"context"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/stages"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/tools/common"
)

type session struct {
	resultsDir string
	executions []execution
}

type execution struct {
	Requirement common.ResolvedRequirement[Params]
	Subdir      string
	RunErr      string
	Skipped     string
}

var _ stages.Session = (*session)(nil)

func (s *session) Preflight(ctx context.Context, rc *core.RunCtx, trs []core.TestRequirement) ([]core.Finding, error) {
	resolved, findings := preflightStage.Check(ctx, rc, trs)
	s.executions = make([]execution, len(resolved))
	for i, requirement := range resolved {
		s.executions[i].Requirement = requirement
	}
	return findings, nil
}
