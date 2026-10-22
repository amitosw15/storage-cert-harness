package virtbench

import (
	"context"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/tools/common"
)

type preflight struct{ sc Scenario }

func (preflight) Check(ctx context.Context, rc *core.RunCtx, bag *core.Bag, trs []core.TestRequirement) ([]core.Finding, error) {
	return common.RunPreflightChecks(ctx, workloadChecks(rc, bag, trs)), nil
}
