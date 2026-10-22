package orchestrator

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"testing"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/grader"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/registry"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/stages"
)

type sessionStub struct {
	trs     []core.TestRequirement
	events  []string
	failAt  string
	results []core.TestResult
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
	return s.stage("teardown")
}

func TestSessionLifecycleAndCleanup(t *testing.T) {
	allStages := []string{"preflight", "provision", "run", "collect", "parse", "teardown"}
	for _, failAt := range []string{"", "preflight", "finding", "provision", "run", "collect", "parse"} {
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
				verdicts, _ := runJob(context.Background(), job{tool: tool, trs: trs}, rc, grader.New())
				if len(verdicts) == 0 || (verdicts[0].Outcome == core.OutcomeError) != (failAt != "") {
					t.Fatalf("verdicts = %+v, want failure=%t", verdicts, failAt != "")
				}
			}
			if len(sessions) != 2 || sessions[0] == sessions[1] {
				t.Fatal("each job must create a fresh session")
			}
			want := allStages
			if failAt != "" {
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
