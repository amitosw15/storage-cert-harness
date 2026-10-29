package grader

import (
	"context"

	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/core"
	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/stages"
)

// NativeOrGraded grades catalog items while preserving native execution failures.
func NativeOrGraded(item, reason string) stages.Evaluator {
	return nativeOrGraded{item: item, reason: reason}
}

type nativeOrGraded struct {
	item   string
	reason string
}

func (n nativeOrGraded) Evaluate(ctx context.Context, tr core.TestRequirement, res core.TestResult) ([]core.Verdict, error) {
	out := res.Native
	reason := n.reason
	if out == "" {
		out = core.OutcomeSkip
		reason = "no native execution outcome provided"
	}
	native := core.Verdict{
		TR:      tr.ID,
		Item:    n.item,
		Outcome: out,
		Reason:  reason,
	}
	if len(tr.SLAs) > 0 || len(tr.Checks) > 0 {
		verdicts := New().GradeTR(ctx, tr, res, nil)
		return append(verdicts, native), nil
	}
	return []core.Verdict{native}, nil
}
