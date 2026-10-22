package orchestrator

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/config"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/grader"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/registry"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/stages"
)

type sessionStub struct {
	trs         []core.TestRequirement
	events      []string
	failAt      string
	teardownErr error
	results     []core.TestResult
}

func TestFailedPipelineSkipsSharedSetupAndStillCleansSession(t *testing.T) {
	for _, failAt := range []string{"preflight", "setup"} {
		t.Run(failAt, func(t *testing.T) {
			tool, group := t.Name(), t.Name()+"-group"
			rec := &recorder{}
			session := &sessionStub{}
			if failAt == "preflight" {
				session.failAt = "preflight"
			}
			registry.Register(stages.ToolIntegration{
				Name: tool, SetupGroups: []string{group},
				NewSession: func() stages.Session { return session },
			})
			registry.RegisterSetup(recSetup{scope: stages.ScopeGroup, key: group, rec: rec, fail: true})
			trs := []core.TestRequirement{{ID: tool, AutomationTool: tool}}
			rc := &core.RunCtx{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
			report, err := Run(context.Background(), config.Config{}, trs, core.Provenance{}, "2.0", rc)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(session.events, []string{"preflight", "teardown"}) {
				t.Fatalf("session stages = %v", session.events)
			}
			wantSetup := 0
			if failAt == "setup" {
				wantSetup = 1
			}
			if n := rec.count("setup:group/" + group); n != wantSetup {
				t.Fatalf("setup calls = %d, want %d", n, wantSetup)
			}
			if summarizeTR(report.Verdicts, trs[0]) != core.OutcomeError {
				t.Fatalf("verdicts = %+v, want error", report.Verdicts)
			}
		})
	}
}

func TestCleanupErrorsPreserveResultsAndReachReports(t *testing.T) {
	for _, failure := range []string{"session", "shared", "run-and-cleanup"} {
		t.Run(failure, func(t *testing.T) {
			tool := t.Name()
			cleanupErr := fmt.Errorf("delete resources failed")
			session := &sessionStub{results: []core.TestResult{{TRID: tool, Checks: map[string]core.Outcome{"ready": core.OutcomePass}}}}
			if failure == "shared" {
				registry.RegisterSetup(recSetup{scope: stages.ScopeGroup, key: tool, rec: &recorder{}, teardownErr: cleanupErr})
			} else {
				session.teardownErr = cleanupErr
			}
			if failure == "run-and-cleanup" {
				session.failAt = "run"
			}
			registry.Register(stages.ToolIntegration{
				Name: tool, SetupGroups: []string{tool},
				NewSession: func() stages.Session { return session },
			})
			trs := []core.TestRequirement{{ID: tool, AutomationTool: tool, Checks: []core.Check{{ID: "ready", Kind: "capability"}}}}
			var checkpoint core.Report
			rc := &core.RunCtx{
				Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
				Checkpoint: func(r core.Report) { checkpoint = r },
			}
			report, err := Run(context.Background(), config.Config{}, trs, core.Provenance{}, "2.0", rc)
			if err != nil {
				t.Fatal(err)
			}
			want := core.OutcomePass
			if failure == "run-and-cleanup" {
				want = core.OutcomeError
			}
			if !slices.ContainsFunc(report.Verdicts, func(v core.Verdict) bool {
				return v.Item == "check:ready" && v.Outcome == want && (failure != "run-and-cleanup" || strings.Contains(v.Reason, "run failed"))
			}) {
				t.Fatalf("original verdict missing: %+v", report.Verdicts)
			}
			for _, r := range []core.Report{report, checkpoint} {
				if !slices.ContainsFunc(r.Verdicts, func(v core.Verdict) bool {
					return strings.HasPrefix(v.Item, "teardown") && v.Outcome == core.OutcomeError && strings.Contains(v.Reason, cleanupErr.Error())
				}) || len(r.Incidents) != 1 || !r.Incidents[0].BlocksCertification {
					t.Fatalf("cleanup error missing from report: %+v", r)
				}
				if len(r.Executions) != 1 || r.Executions[0].Outcome != core.OutcomeError {
					t.Fatalf("execution outcome = %+v, want error", r.Executions)
				}
			}
		})
	}
}

func (s *sessionStub) stage(name string) error {
	s.events = append(s.events, name)
	if s.failAt == name {
		return fmt.Errorf("%s failed", name)
	}
	return nil
}

func (s *sessionStub) Preflight(_ context.Context, _ *core.RunCtx, trs []core.TestRequirement) ([]core.Finding, error) {
	s.trs = trs
	err := s.stage("preflight")
	if s.failAt == "finding" {
		return []core.Finding{{Level: "error", Message: "prerequisite missing"}}, nil
	}
	return nil, err
}

func (s *sessionStub) Provision(context.Context, *core.RunCtx) error {
	return s.stage("provision")
}

func (s *sessionStub) Run(context.Context, *core.RunCtx) error {
	return s.stage("run")
}

func (s *sessionStub) Collect(context.Context, *core.RunCtx) (core.LogBundle, error) {
	return core.LogBundle{}, s.stage("collect")
}

func (s *sessionStub) Parse(context.Context, *core.RunCtx, core.LogBundle) ([]core.TestResult, error) {
	return s.results, s.stage("parse")
}

func (s *sessionStub) Teardown(context.Context, *core.RunCtx) error {
	if err := s.stage("teardown"); err != nil {
		return err
	}
	return s.teardownErr
}

func TestSessionLifecycleAndCleanup(t *testing.T) {
	allStages := []string{"preflight", "provision", "run", "collect", "parse", "teardown"}
	for _, failAt := range []string{"", "preflight", "finding", "provision", "run", "collect", "parse", "teardown"} {
		t.Run("failure="+failAt, func(t *testing.T) {
			var sessions []*sessionStub
			tool := t.Name()
			registry.Register(stages.ToolIntegration{Name: tool, NewSession: func() stages.Session {
				s := &sessionStub{failAt: failAt, results: []core.TestResult{{TRID: "SESSION-TR"}}}
				sessions = append(sessions, s)
				return s
			}})
			trs := []core.TestRequirement{{ID: "SESSION-TR", AutomationTool: tool, SLAs: []core.SLA{{Metric: "latency", Unit: "s"}}}}
			rc := &core.RunCtx{
				Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
				SetStage: func(string, []core.TestRequirement) {},
			}
			for range 2 {
				jobs, verdicts := prepareJobs(context.Background(), []job{{tool: tool, trs: trs}}, rc, nil)
				if len(jobs) > 0 {
					verdicts, _ = runJob(context.Background(), jobs[0].pipelines[0], grader.New())
				}
				if len(verdicts) == 0 || (summarizeTR(verdicts, trs[0]) == core.OutcomeError) != (failAt != "") {
					t.Fatalf("verdicts = %+v, want failure=%t", verdicts, failAt != "")
				}
			}
			if len(sessions) != 2 || sessions[0] == sessions[1] {
				t.Fatal("each job must create a fresh session")
			}
			want := allStages
			if failAt != "" && failAt != "teardown" {
				stage := failAt
				if stage == "finding" {
					stage = "preflight"
				}
				want = append(slices.Clone(allStages[:slices.Index(allStages, stage)+1]), "teardown")
			}
			for _, session := range sessions {
				if !slices.Equal(session.events, want) {
					t.Errorf("stages = %v, want %v", session.events, want)
				}
			}
		})
	}
}
