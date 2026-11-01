package kubeburnerocp

import (
	"context"
	"fmt"
	"slices"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/stages"
)

type pvcDensityEvaluator struct {
	stages.Evaluator
}

func (e pvcDensityEvaluator) Evaluate(ctx context.Context, tr core.TestRequirement, res core.TestResult) ([]core.Verdict, error) {
	count, _ := asInt(tr.Params["iterations"])
	expectation := "all requested PVCs reach Bound without provisioner backlog or failure"
	if count > 0 {
		expectation = fmt.Sprintf("all %d requested PVCs reach Bound without provisioner backlog or failure", count)
	}
	tr.Checks = slices.Clone(tr.Checks)
	for i := range tr.Checks {
		if tr.Checks[i].ID == pvcBoundCheck {
			tr.Checks[i].Expectation = expectation
		}
	}
	verdicts, err := e.Evaluator.Evaluate(ctx, tr, res)
	if err != nil {
		return nil, err
	}
	for i := range verdicts {
		if verdicts[i].Item != "check:"+pvcBoundCheck {
			continue
		}
		if count > 0 {
			verdicts[i].Expected = fmt.Sprintf("%d requested PVCs", count)
		}
		for _, m := range res.Metrics {
			if m.Name == "pvc_requested_count" {
				verdicts[i].Actual = fmt.Sprintf("%g PVCs requested by tool", m.Value)
				break
			}
		}
	}
	return verdicts, nil
}
