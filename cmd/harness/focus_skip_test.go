package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
)

func TestFocusSkipPreviewMatchesRun(t *testing.T) {
	dir := t.TempDir()
	catalogPath := filepath.Join(dir, "catalog.json")
	thresholdsPath := filepath.Join(dir, "thresholds.json")
	catalogData := `{"schema_version":"2.0","provenance":{"kb_git_commit":"fixture"},"test_requirements":[{"id":"EX-001","automation_tool":"example","sla_status":"defined","sla_count":1,"sla":[{"metric":"latency_ms","type":"latency","unit":"ms","percentile":"p99","measured_by":["example"]}]},{"id":"EX-003","automation_tool":"example","sla_status":"defined","sla_count":1,"sla":[{"metric":"read_throughput","type":"throughput","unit":"MiB/s","measured_by":["example"]}]}]}`
	thresholdsData := `{"schema_version":"2.0","provenance":{"kb_git_commit":"fixture"},"test_requirements":[{"id":"EX-001","sla":[{"metric":"latency_ms","type":"latency","operator":"<=","value":1,"unit":"ms","percentile":"p99"}]},{"id":"EX-003","sla":[{"metric":"read_throughput","type":"throughput","operator":">=","value":1000,"unit":"MiB/s"}]}]}`
	if err := os.WriteFile(catalogPath, []byte(catalogData), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(thresholdsPath, []byte(thresholdsData), 0o600); err != nil {
		t.Fatal(err)
	}
	planPath := filepath.Join(dir, "plan.yaml")
	plan := `schema_version: "1"
name: selected-executions
select:
  ids: [EX-001, EX-003]
variants:
  EX-001:
    - name: small
    - name: large
`
	if err := os.WriteFile(planPath, []byte(plan), 0o600); err != nil {
		t.Fatal(err)
	}
	base := []string{"run", "--catalog", catalogPath, "--thresholds", thresholdsPath, "--plan", planPath, "--focus", "EX-001", "--skip", "EX-001,EX-003", "--skip-env", "--no-attestations"}
	var preview bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&preview)
	cmd.SetArgs(append(append([]string{}, base...), "--inspect-plan"))
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Executions []planExecution `json:"executions"`
	}
	if err := json.Unmarshal(preview.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "output")
	cmd = newRootCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs(append(append([]string{}, base...), "--output", output))
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(output, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var report core.Report
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	var actual []planExecution
	for _, e := range report.Executions {
		actual = append(actual, planExecution{ID: e.TR, Tool: "example", Variant: e.Variant, Scenario: e.Scenario})
	}
	sort.Slice(result.Executions, func(i, j int) bool { return result.Executions[i].Variant < result.Executions[j].Variant })
	sort.Slice(actual, func(i, j int) bool { return actual[i].Variant < actual[j].Variant })
	if !reflect.DeepEqual(result.Executions, actual) {
		t.Fatalf("preview=%+v, run=%+v", result.Executions, actual)
	}
	resumeOut := filepath.Join(dir, "resume")
	cmd = newRootCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs(append(append([]string{}, base...), "--continue-from", filepath.Join(output, "report.json"), "--output", resumeOut))
	if err := cmd.Execute(); err != nil {
		t.Fatalf("completed continuation: %v", err)
	}
	data, err = os.ReadFile(filepath.Join(resumeOut, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var resumed core.Report
	if err := json.Unmarshal(data, &resumed); err != nil {
		t.Fatal(err)
	}
	if len(resumed.Executions) != len(report.Executions) || len(resumed.Verdicts) != len(report.Verdicts) {
		t.Fatalf("completed continuation changed results: executions=%v verdicts=%v", resumed.Executions, resumed.Verdicts)
	}
}
