package orchestrator

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/config"
	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/registry"
	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/stages"

	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/core"
)

func TestResumeExecutionsSkipsOnlySuccessfulMatchingScenario(t *testing.T) {
	selected := []core.TestRequirement{
		{ID: "TR-A"},
		{ID: "TR-B"},
		{ID: "TR-C"},
		{ID: "TR-D"},
	}
	prior := core.Report{Executions: []core.RunExecution{
		{TR: "TR-A", Scenario: "load80", State: core.ExecutionCompleted, Outcome: core.OutcomePass},
		{TR: "TR-B", Scenario: "load80", State: core.ExecutionCompleted, Outcome: core.OutcomeFail},
		{TR: "TR-C", Scenario: "load80", State: core.ExecutionRunning},
		{TR: "TR-D", Scenario: "non-load80", State: core.ExecutionCompleted, Outcome: core.OutcomePass},
	}}
	got := ResumeExecutions(selected, prior, "load80")
	if len(got) != 2 || got[0].ID != "TR-C" || got[1].ID != "TR-D" {
		t.Fatalf("resumed selection = %+v, want C/D", got)
	}
	got = ResumeExecutionsWithOptions(selected, prior, "load80", true)
	if len(got) != 3 || got[0].ID != "TR-B" || got[1].ID != "TR-C" || got[2].ID != "TR-D" {
		t.Fatalf("retry-failed selection = %+v, want B/C/D", got)
	}
}

func TestResumeExecutionsSupportsLegacyVerdicts(t *testing.T) {
	selected := []core.TestRequirement{{ID: "TR-A"}, {ID: "TR-B"}}
	prior := core.Report{Verdicts: []core.Verdict{
		{TR: "TR-A", Scenario: "idle", Item: "check:a", Outcome: core.OutcomePass},
		{TR: "TR-A", Scenario: "idle", Item: "check:b", Outcome: core.OutcomeSkip},
		{TR: "TR-B", Scenario: "idle", Item: "check:a", Outcome: core.OutcomeError},
	}}
	got := ResumeExecutions(selected, prior, "idle")
	if len(got) != 0 {
		t.Fatalf("legacy resumed selection = %+v, want no completed executions", got)
	}
	got = ResumeExecutionsWithOptions(selected, prior, "idle", true)
	if len(got) != 1 || got[0].ID != "TR-B" {
		t.Fatalf("legacy retry-failed selection = %+v, want B", got)
	}
}

func TestResumeExecutionsRerunsAbortedExecutionDespiteCancellationVerdicts(t *testing.T) {
	selected := []core.TestRequirement{{ID: "TR-A"}, {ID: "TR-B"}}
	prior := core.Report{
		Executions: []core.RunExecution{
			{TR: "TR-A", Scenario: "idle", State: core.ExecutionAborted, Outcome: core.OutcomeError},
			{TR: "TR-B", Scenario: "idle", State: core.ExecutionCompleted, Outcome: core.OutcomePass},
		},
		Verdicts: []core.Verdict{
			{TR: "TR-A", Scenario: "idle", Item: "check:a", Outcome: core.OutcomeError},
			{TR: "TR-B", Scenario: "idle", Item: "check:a", Outcome: core.OutcomePass},
		},
	}
	got := ResumeExecutions(selected, prior, "idle")
	if len(got) != 1 || got[0].ID != "TR-A" {
		t.Fatalf("aborted resumed selection = %+v, want TR-A", got)
	}
}

// Run must execute exactly the TRs it is handed. The CLI narrows that list for
// --continue-from, so re-deriving it from the full catalog would re-run every
// execution the prior report had already completed — while the resume log
// still claimed they were skipped.
func TestRunExecutesOnlyTheGivenTRs(t *testing.T) {
	registry.Register(stages.ToolIntegration{Name: "resume-scope-test", Runner: countingRunner{}})
	all := []core.TestRequirement{
		{ID: "TR-RESUME-A", AutomationTool: "resume-scope-test"},
		{ID: "TR-RESUME-B", AutomationTool: "resume-scope-test"},
		{ID: "TR-RESUME-C", AutomationTool: "resume-scope-test"},
	}
	// What ResumeExecutionsWithOptions would hand back: only the outstanding one.
	outstanding := all[1:2]

	rc := &core.RunCtx{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	rep, err := Run(context.Background(), config.Config{}, outstanding, core.Provenance{}, "2.0", rc)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Executions) != 1 || rep.Executions[0].TR != "TR-RESUME-B" {
		t.Fatalf("executions = %+v, want only TR-RESUME-B", rep.Executions)
	}
}

type countingRunner struct{}

func (countingRunner) Run(_ context.Context, _ *core.RunCtx, _ *core.Bag, _ []core.TestRequirement) (stages.RunHandle, error) {
	return stages.RunHandle{}, nil
}
