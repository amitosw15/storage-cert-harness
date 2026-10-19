package kubeburner

import (
	"context"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/grader"
)

// snapshotEvaluator scores TR-VIRT-010: it emits the kube-burner native batch verdict, then defers SLA/check scoring to the generic grader (measured_by + when-scoped bars, ADR-0013). See decisions/0012.
type snapshotEvaluator struct{}

func (snapshotEvaluator) Evaluate(_ context.Context, tr core.TestRequirement, res core.TestResult) ([]core.Verdict, error) {
	batch := core.Verdict{
		TR:      tr.ID,
		Item:    "native:jobs_passed",
		Outcome: res.Native,
		Reason:  "kube-burner VM snapshot batch measurements",
		Actual:  snapshotBatchActual(res),
	}
	if batch.Outcome == "" {
		batch.Outcome = core.OutcomeError
	}
	if len(tr.SLAs) == 0 && len(tr.Checks) == 0 {
		return []core.Verdict{batch}, nil
	}

	// Measurability (which metrics kube-burner's volumeSnapshotLatency emits) is now
	// data — the catalog's measured_by — so the generic grader handles skips and
	// when-scoped bar selection (ADR-0013).
	out := []core.Verdict{batch}
	out = append(out, grader.GradeSLAs(tr, res)...)
	out = append(out, grader.GradeChecks(tr, res)...)
	return out, nil
}
