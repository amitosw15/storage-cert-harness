package virtbench

import (
	"context"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/tools/common"
)

func (s *session) Preflight(ctx context.Context, rc *core.RunCtx, trs []core.TestRequirement) ([]core.Finding, error) {
	s.trs = trs
	return common.RunPreflightChecks(ctx, workloadChecks(rc, s, trs)), nil
}
