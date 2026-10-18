// Package grader scores a TR's TestResult into one Verdict per scorable item
// (each sla and each check), applying the KB v2.0 scoring rules. A
// ToolIntegration may supply a custom per-TR Evaluator for TRs whose scoring it
// owns; otherwise the built-in rules apply. See decisions/0006.
package grader

import (
	"context"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/stages"
)

// Grader applies the built-in v2.0 scoring rules.
type Grader struct{}

// New returns a Grader.
func New() *Grader { return &Grader{} }

// GradeTR scores every scorable item in a TR. If toolEvaluators has a custom
// evaluator for this TR id, it fully owns the scoring; otherwise the built-in
// sla/checks rules apply.
func (g *Grader) GradeTR(
	ctx context.Context,
	tr core.TestRequirement,
	res core.TestResult,
	toolEvaluators map[string]stages.Evaluator,
) []core.Verdict {
	if ev, ok := toolEvaluators[tr.ID]; ok && ev != nil {
		vs, err := ev.Evaluate(ctx, tr, res)
		if err != nil {
			return []core.Verdict{{TR: tr.ID, Item: "custom", Outcome: core.OutcomeError, Reason: err.Error()}}
		}
		for i := range vs {
			if vs[i].TR == "" {
				vs[i].TR = tr.ID
			}
		}
		return vs
	}

	var out []core.Verdict
	out = append(out, gradeSLAs(tr, res)...)
	out = append(out, gradeChecks(tr, res)...)
	return out
}
