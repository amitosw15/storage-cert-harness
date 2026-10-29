package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/catalog"
	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/core"
	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/registry"
	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/report"
	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/stages"
	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/tools/kubeburnerocp"
)

type lifecycleReplayParser struct{}

func (lifecycleReplayParser) Parse(context.Context, *core.RunCtx, *core.Bag, core.LogBundle) ([]core.TestResult, error) {
	data, err := os.ReadFile("../../internal/tools/kubeburnerocp/testdata/virt-parallel-results/jobSummary-timeout.json")
	if err != nil {
		return nil, err
	}
	res, err := kubeburnerocp.ParseResults(kubeburnerocp.WorkloadVirtParallel, "TR-VIRT-019", map[string][]byte{"jobSummary.json": data})
	if err != nil {
		return nil, err
	}
	return []core.TestResult{res}, nil
}

func TestRunLifecycleTimeoutReturnsCertificationFailure(t *testing.T) {
	integration, ok := registry.ForTRID("TR-VIRT-019")
	if !ok {
		t.Fatal("missing lifecycle adapter")
	}
	const replayTool = "test-lifecycle-timeout-replay"
	registry.Register(stages.ToolIntegration{Name: replayTool, ResultParser: lifecycleReplayParser{}, Evaluators: integration.Evaluators})
	cat, err := catalog.Load("../../catalog.json")
	if err != nil {
		t.Fatal(err)
	}
	for i := range cat.TestRequirements {
		tr := &cat.TestRequirements[i]
		if tr.ID == "TR-VIRT-019" {
			tr.AutomationTool = replayTool
			for j := range tr.SLA {
				tr.SLA[j].MeasuredBy = []string{replayTool}
			}
		}
	}
	root := t.TempDir()
	catalogPath := filepath.Join(root, "catalog.json")
	data, err := json.Marshal(cat)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalogPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "output")
	var terminal bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&terminal)
	cmd.SetErr(&terminal)
	cmd.SetArgs([]string{"run", "--catalog", catalogPath, "--thresholds", "../../thresholds.json", "--id", "TR-VIRT-019", "--workdir", filepath.Join(root, "work"), "--output", output, "--skip-env", "--no-attestations"})
	err = cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "certification failed:") {
		t.Fatalf("CLI did not return certification failure: %v\n%s", err, terminal.String())
	}
	rep, err := report.ReadJSON(filepath.Join(output, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Certifiable || rep.Summary.CertifiedLevel != 0 || (rep.Summary.Fail+rep.Summary.Error) == 0 || rep.Status != core.RunStatusComplete {
		t.Fatalf("unexpected timeout report: %+v", rep)
	}
	found := false
	for _, verdict := range rep.Verdicts {
		if verdict.Item == "sla:lifecycle_completion_time" {
			found = true
		}
	}
	if !found {
		t.Fatal("missing lifecycle completion SLA verdict")
	}
}
