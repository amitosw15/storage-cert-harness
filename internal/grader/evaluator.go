package grader

import (
	"context"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/stages"
)

// NativeOrGraded returns an Evaluator that grades a TR's SLAs/Checks with the
// standard grader when any are defined, and otherwise emits a single native
// verdict (using the given item label and reason). This is the common pattern for
// TRs whose KB SLA bars are still to-be-validated (sla: []). Shared across tool
// adapters so each one does not reimplement it.
func NativeOrGraded(item, reason string) stages.Evaluator {
	return nativeOrGraded{item: item, reason: reason}
}

type nativeOrGraded struct {
	item   string
	reason string
}

func (n nativeOrGraded) Evaluate(ctx context.Context, tr core.TestRequirement, res core.TestResult) ([]core.Verdict, error) {
	if len(tr.SLAs) > 0 || len(tr.Checks) > 0 {
		return New().GradeTR(ctx, tr, res, nil), nil
	}
	out := res.Native
	if out == "" {
		out = core.OutcomeError
	}
	return []core.Verdict{{
		TR:      tr.ID,
		Item:    n.item,
		Outcome: out,
		Reason:  n.reason,
	}}, nil
}
