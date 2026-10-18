package grader

import (
	"context"
	"testing"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
)

func f64(v float64) *float64 { return &v }

func outcomes(vs []core.Verdict) map[string]core.Outcome {
	m := map[string]core.Outcome{}
	for _, v := range vs {
		m[v.Item] = v.Outcome
	}
	return m
}

func verdictByItem(vs []core.Verdict, item string) *core.Verdict {
	for i := range vs {
		if vs[i].Item == item {
			return &vs[i]
		}
	}
	return nil
}

// tool is the active automation tool for these tests; descriptors list it in
// measured_by so they are not skipped for measurability.
const tool = "fio"

func TestGradeSLAPassAndFail(t *testing.T) {
	g := New()
	tr := core.TestRequirement{
		ID: "TR-1", AutomationTool: tool,
		SLAs: []core.SLA{
			{Metric: "latency_ms", Unit: "ms", Percentile: "p99", MeasuredBy: []string{tool}},
			{Metric: "iops", MeasuredBy: []string{tool}},
		},
		Bars: []core.SLA{
			{Metric: "latency_ms", Operator: "<=", Value: f64(2), Unit: "ms", Percentile: "p99"},
			{Metric: "iops", Operator: ">=", Value: f64(90)},
		},
	}
	res := core.TestResult{TRID: "TR-1", Metrics: []core.Metric{
		{Name: "latency_ms", Value: 1, Unit: "ms", Percentile: "p99"},
		{Name: "iops", Value: 50},
	}}
	got := outcomes(g.GradeTR(context.Background(), tr, res, nil))
	if got["sla:latency_ms@p99"] != core.OutcomePass {
		t.Errorf("latency should pass, got %s", got["sla:latency_ms@p99"])
	}
	if got["sla:iops"] != core.OutcomeFail {
		t.Errorf("iops should fail, got %s", got["sla:iops"])
	}
}

// A descriptor whose active tool is not in measured_by skips cleanly.
func TestGradeSLAMeasuredBySkip(t *testing.T) {
	g := New()
	tr := core.TestRequirement{ID: "TR-1", AutomationTool: tool,
		SLAs: []core.SLA{{Metric: "p99_latency", Percentile: "p99", MeasuredBy: []string{}}}}
	res := core.TestResult{TRID: "TR-1", Metrics: []core.Metric{{Name: "p99_latency", Value: 1, Percentile: "p99"}}}
	got := outcomes(g.GradeTR(context.Background(), tr, res, nil))
	if got["sla:p99_latency@p99"] != core.OutcomeSkip {
		t.Errorf("empty measured_by should skip, got %s", got["sla:p99_latency@p99"])
	}
}

// A measured metric with a descriptor but no matching bar is report-only: skip,
// never pass/fail, and it carries the measured value.
func TestGradeSLAReportOnly(t *testing.T) {
	g := New()
	tr := core.TestRequirement{ID: "TR-1", AutomationTool: tool,
		SLAs: []core.SLA{{Metric: "tput", Unit: "MBps", MeasuredBy: []string{tool}}}}
	res := core.TestResult{TRID: "TR-1", Metrics: []core.Metric{{Name: "tput", Value: 42, Unit: "MBps"}}}
	vs := g.GradeTR(context.Background(), tr, res, nil)
	v := verdictByItem(vs, "sla:tput")
	if v == nil || v.Outcome != core.OutcomeSkip {
		t.Fatalf("no-bar metric should be a report-only skip, got %+v", vs)
	}
	if v.Actual != "42 MBps" {
		t.Errorf("report-only skip should carry the measured value, got %q", v.Actual)
	}
}

func TestGradeSLAMissingMetricErrors(t *testing.T) {
	g := New()
	tr := core.TestRequirement{ID: "TR-1", AutomationTool: tool,
		SLAs: []core.SLA{{Metric: "latency_ms", MeasuredBy: []string{tool}}},
		Bars: []core.SLA{{Metric: "latency_ms", Operator: "<=", Value: f64(2)}}}
	vs := g.GradeTR(context.Background(), tr, core.TestResult{TRID: "TR-1"}, nil)
	v := verdictByItem(vs, "sla:latency_ms")
	if v == nil || v.Outcome != core.OutcomeError {
		t.Fatalf("missing metric for a graded SLA should error, got %+v", vs)
	}
	if v.Reason != "missing required metric \"latency_ms\" for graded SLA" {
		t.Errorf("unexpected missing metric reason: %q", v.Reason)
	}
}

// when ⊆ params selection: the two-scale case. At num_vms:10 the small-scale bar
// grades and the large-scale bar is inert; at num_vms:20 the large-scale bar
// grades; at an unmapped scale nothing matches → report-only.
func TestGradeSLAWhenScopedTwoScale(t *testing.T) {
	g := New()
	descriptors := []core.SLA{{Metric: "vm_boot_time", Unit: "s", MeasuredBy: []string{tool}}}
	bars := []core.SLA{
		{Metric: "vm_boot_time", Operator: "<=", Value: f64(6), Unit: "s", When: map[string]any{"num_vms": 10}},
		{Metric: "vm_boot_time", Operator: "<=", Value: f64(36), Unit: "s", When: map[string]any{"num_vms": 20}},
	}
	res := core.TestResult{TRID: "TR-1", Metrics: []core.Metric{{Name: "vm_boot_time", Value: 9, Unit: "s"}}}
	// 9s at num_vms:10 fails the small-scale bar (<=6s).
	tr10 := core.TestRequirement{ID: "TR-1", AutomationTool: tool, SLAs: descriptors, Bars: bars,
		Params: map[string]any{"num_vms": 10}}
	if outcomes(g.GradeTR(context.Background(), tr10, res, nil))["sla:vm_boot_time"] != core.OutcomeFail {
		t.Error("9s at num_vms:10 should fail the small-scale bar")
	}
	// The same 9s at num_vms:20 passes the large-scale bar (<=36s).
	tr20 := tr10
	tr20.Params = map[string]any{"num_vms": 20}
	if outcomes(g.GradeTR(context.Background(), tr20, res, nil))["sla:vm_boot_time"] != core.OutcomePass {
		t.Error("9s at num_vms:20 should pass the large-scale bar")
	}
	// At an unmapped scale, no bar matches → report-only skip.
	trNone := tr10
	trNone.Params = map[string]any{"num_vms": 15}
	if outcomes(g.GradeTR(context.Background(), trNone, res, nil))["sla:vm_boot_time"] != core.OutcomeSkip {
		t.Error("unmapped scale should be report-only skip")
	}
}

// An empty/absent when always applies.
func TestGradeSLAEmptyWhenAlwaysApplies(t *testing.T) {
	g := New()
	tr := core.TestRequirement{ID: "TR-1", AutomationTool: tool,
		SLAs:   []core.SLA{{Metric: "dur", Unit: "s", MeasuredBy: []string{tool}}},
		Bars:   []core.SLA{{Metric: "dur", Operator: "<", Value: f64(10), Unit: "s"}},
		Params: map[string]any{"num_vms": 10}}
	res := core.TestResult{TRID: "TR-1", Metrics: []core.Metric{{Name: "dur", Value: 5, Unit: "s"}}}
	if outcomes(g.GradeTR(context.Background(), tr, res, nil))["sla:dur"] != core.OutcomePass {
		t.Error("bar with no when should always apply")
	}
}

// Overlapping when for one metric is a data error (disjoint-when invariant).
func TestGradeSLADisjointWhenViolation(t *testing.T) {
	g := New()
	tr := core.TestRequirement{ID: "TR-1", AutomationTool: tool,
		SLAs: []core.SLA{{Metric: "dur", Unit: "s", MeasuredBy: []string{tool}}},
		Bars: []core.SLA{
			{Metric: "dur", Operator: "<", Value: f64(10), Unit: "s", When: map[string]any{"num_vms": 10}},
			{Metric: "dur", Operator: "<", Value: f64(20), Unit: "s"}, // also matches (empty when)
		},
		Params: map[string]any{"num_vms": 10}}
	res := core.TestResult{TRID: "TR-1", Metrics: []core.Metric{{Name: "dur", Value: 5, Unit: "s"}}}
	if outcomes(g.GradeTR(context.Background(), tr, res, nil))["sla:dur"] != core.OutcomeError {
		t.Error("two matching bars for one metric should error (disjoint-when violation)")
	}
}

func TestGradeChecks(t *testing.T) {
	g := New()
	tr := core.TestRequirement{
		ID: "TR-1",
		Checks: []core.Check{
			{ID: "cap", Kind: core.CheckCapability, Status: core.StatusDefined},
			{ID: "man", Kind: core.CheckManual, Status: core.StatusDefined},
			{ID: "tbv", Kind: core.CheckCapability, Status: core.StatusToBeValidated},
		},
	}
	res := core.TestResult{TRID: "TR-1", Checks: map[string]core.Outcome{"cap": core.OutcomePass}}
	got := outcomes(g.GradeTR(context.Background(), tr, res, nil))
	if got["check:cap"] != core.OutcomePass {
		t.Errorf("capability check should pass, got %s", got["check:cap"])
	}
	if got["check:man"] != core.OutcomeSkip {
		t.Errorf("manual check should skip, got %s", got["check:man"])
	}
	if got["check:tbv"] != core.OutcomeSkip {
		t.Errorf("to-be-validated check should skip, got %s", got["check:tbv"])
	}
}

func TestGradeCheckMissingOutcomeErrors(t *testing.T) {
	g := New()
	tr := core.TestRequirement{ID: "TR-1",
		Checks: []core.Check{{ID: "cap", Kind: core.CheckCapability, Status: core.StatusDefined}}}
	vs := g.GradeTR(context.Background(), tr, core.TestResult{TRID: "TR-1"}, nil)
	v := verdictByItem(vs, "check:cap")
	if v == nil || v.Outcome != core.OutcomeError {
		t.Fatalf("missing check outcome should error, got %+v", vs)
	}
	if v.Reason != "missing required outcome for capability check" {
		t.Errorf("unexpected missing check reason: %q", v.Reason)
	}
}
