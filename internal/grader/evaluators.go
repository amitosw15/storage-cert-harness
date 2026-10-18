package grader

import (
	"fmt"
	"slices"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
)

// gradeSLAs scores each catalog SLA descriptor of a TR against the thresholds
// bars, fully generically (ADR-0013). For each descriptor:
//
//  1. active tool ∉ measured_by      → skip ("tool cannot measure metric").
//  2. select the bar whose when ⊆ run params; >1 candidate is a data error.
//  3. no matching bar                → skip, report-only (value surfaced, never fails).
//  4. bar present, metric measured   → unit-normalized compare → pass/fail.
//
// Precedence: measured_by skip > when-matched compare > report-only. There is no
// tool-specific measurability code here; measured_by (catalog) drives every skip.
func gradeSLAs(tr core.TestRequirement, res core.TestResult) []core.Verdict {
	out := make([]core.Verdict, 0, len(tr.SLAs))
	for _, d := range tr.SLAs {
		item := slaItem(d)

		if !measures(d.MeasuredBy, tr.AutomationTool) {
			out = append(out, core.Verdict{TR: tr.ID, Item: item, Outcome: core.OutcomeSkip,
				Reason: fmt.Sprintf("tool cannot measure metric: %q not in measured_by", tr.AutomationTool)})
			continue
		}

		bars := selectBars(tr.Bars, d.Metric, d.Percentile, tr.Params)
		if len(bars) > 1 {
			out = append(out, core.Verdict{TR: tr.ID, Item: item, Outcome: core.OutcomeError,
				Reason: fmt.Sprintf("data error: %d threshold bars match %q for the run params — bars must be disjoint by when",
					len(bars), metricLabel(d.Metric, d.Percentile))})
			continue
		}

		m := findMetric(res.Metrics, d.Metric, d.Percentile)

		if len(bars) == 0 || bars[0].Value == nil {
			v := core.Verdict{TR: tr.ID, Item: item, Outcome: core.OutcomeSkip,
				Reason: "report-only: no threshold bar for these run params"}
			if m != nil {
				v.Actual = fmt.Sprintf("%g %s", m.Value, unitOr(m.Unit, d.Unit))
			}
			out = append(out, v)
			continue
		}

		if m == nil {
			out = append(out, core.Verdict{TR: tr.ID, Item: item, Outcome: core.OutcomeError,
				Reason: fmt.Sprintf("missing required metric %q for graded SLA", metricLabel(d.Metric, d.Percentile))})
			continue
		}

		bar := bars[0]
		pass, incompatible := compareWithUnits(m.Value, m.Unit, bar.Operator, *bar.Value, bar.Unit)
		if incompatible {
			out = append(out, core.Verdict{
				TR:       tr.ID,
				Item:     item,
				Outcome:  core.OutcomeError,
				Expected: fmt.Sprintf("%s %g %s", bar.Operator, *bar.Value, bar.Unit),
				Actual:   fmt.Sprintf("%g %s", m.Value, unitOr(m.Unit, bar.Unit)),
				Reason:   fmt.Sprintf("incompatible units: measured %q cannot be compared to sla %q", m.Unit, bar.Unit),
			})
			continue
		}
		outcome := core.OutcomeFail
		if pass {
			outcome = core.OutcomePass
		}
		out = append(out, core.Verdict{
			TR:       tr.ID,
			Item:     item,
			Outcome:  outcome,
			Expected: fmt.Sprintf("%s %g %s", bar.Operator, *bar.Value, bar.Unit),
			Actual:   fmt.Sprintf("%g %s", m.Value, unitOr(m.Unit, bar.Unit)),
			Reason:   bar.Condition,
		})
	}
	return out
}

// gradeChecks scores each check in a TR by dispatching on kind. A
// to-be-validated check or a manual check is not auto-scorable → skip.
func gradeChecks(tr core.TestRequirement, res core.TestResult) []core.Verdict {
	out := make([]core.Verdict, 0, len(tr.Checks))
	for _, c := range tr.Checks {
		item := "check:" + c.ID
		switch {
		case c.Status == core.StatusToBeValidated:
			out = append(out, core.Verdict{TR: tr.ID, Item: item, Outcome: core.OutcomeSkip,
				Reason: reasonWithExpectation("check to-be-validated: no automation yet", c.Expectation)})
			continue
		case c.Kind == core.CheckManual:
			out = append(out, core.Verdict{TR: tr.ID, Item: item, Outcome: core.OutcomeSkip,
				Reason: reasonWithExpectation("manual check: requires human sign-off", c.Expectation)})
			continue
		}
		native, ok := res.Checks[c.ID]
		if !ok {
			out = append(out, core.Verdict{TR: tr.ID, Item: item, Outcome: core.OutcomeError,
				Reason: reasonWithExpectation(fmt.Sprintf("missing required outcome for %s check", c.Kind), c.Expectation)})
			continue
		}
		outcome := native
		switch native {
		case core.OutcomePass, core.OutcomeFail, core.OutcomeSkip, core.OutcomeError:
		default:
			outcome = core.OutcomeError
		}
		out = append(out, core.Verdict{TR: tr.ID, Item: item, Outcome: outcome,
			Reason: reasonWithExpectation(fmt.Sprintf("%s check", c.Kind), c.Expectation)})
	}
	return out
}

// measures reports whether the active tool produces this metric. A nil
// measured_by (no automation_tool to default from) is permissive; an explicit
// empty list means nothing measures it yet → not measured.
func measures(measuredBy []string, tool string) bool {
	if measuredBy == nil {
		return true
	}
	return slices.Contains(measuredBy, tool)
}

// selectBars returns the bars for (metric, percentile) whose when ⊆ run params.
// At most one should match (disjoint-when invariant); the caller treats >1 as a
// data error.
func selectBars(bars []core.SLA, metric, percentile string, params map[string]any) []core.SLA {
	var out []core.SLA
	for _, b := range bars {
		if b.Metric != metric || b.Percentile != percentile {
			continue
		}
		if whenSubset(b.When, params) {
			out = append(out, b)
		}
	}
	return out
}

// whenSubset reports whether every key in when equals the run's resolved param.
// Empty/nil when always matches.
func whenSubset(when, params map[string]any) bool {
	for k, v := range when {
		pv, ok := params[k]
		if !ok || !paramEqual(v, pv) {
			return false
		}
	}
	return true
}

// paramEqual compares two loosely-typed param values, coercing numerics (JSON
// float64 vs a plan's int) before falling back to string comparison.
func paramEqual(a, b any) bool {
	if af, ok := toFloat(a); ok {
		bf, ok := toFloat(b)
		return ok && af == bf
	}
	return fmt.Sprint(a) == fmt.Sprint(b)
}

func toFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case int32:
		return float64(t), true
	}
	return 0, false
}

// findMetric matches by name, and by percentile too when the sla specifies one.
func findMetric(metrics []core.Metric, name, percentile string) *core.Metric {
	for i := range metrics {
		if metrics[i].Name != name {
			continue
		}
		if percentile != "" && metrics[i].Percentile != percentile {
			continue
		}
		return &metrics[i]
	}
	return nil
}

func slaItem(s core.SLA) string {
	return "sla:" + metricLabel(s.Metric, s.Percentile)
}

func metricLabel(metric, percentile string) string {
	if percentile != "" {
		return metric + "@" + percentile
	}
	return metric
}

func unitOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func reasonWithExpectation(prefix, expectation string) string {
	if expectation == "" {
		return prefix
	}
	return prefix + ": " + expectation
}

// GradeSLAs and GradeChecks expose the built-in ADR-0013 scoring rules so a
// tool's custom stages.Evaluator can reuse them (e.g. after emitting a native
// batch verdict) instead of re-implementing scoring, unit normalization, or the
// measured_by / when-scoped bar selection.
func GradeSLAs(tr core.TestRequirement, res core.TestResult) []core.Verdict {
	return gradeSLAs(tr, res)
}

// GradeChecks scores a TR's checks with the built-in rules. See GradeSLAs.
func GradeChecks(tr core.TestRequirement, res core.TestResult) []core.Verdict {
	return gradeChecks(tr, res)
}

// SLAItem returns the Verdict.Item string the built-in rules use for an SLA, so a
// custom Evaluator's skip/error verdicts match the built-in items exactly.
func SLAItem(s core.SLA) string { return slaItem(s) }

func compare(actual float64, op string, want float64) bool {
	switch op {
	case "<=":
		return actual <= want
	case "<":
		return actual < want
	case ">=":
		return actual >= want
	case ">":
		return actual > want
	case "==":
		return actual == want
	default:
		return false
	}
}
