package kubeburnerocp

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/tools/common"
)

func writePVCJobSummary(t *testing.T, dir string) {
	t.Helper()
	src := filepath.Join("testdata", "pvc-density-results", "jobSummary.json")
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	metricsDir := filepath.Join(dir, "collected-metrics-test")
	if err := os.MkdirAll(metricsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(metricsDir, "jobSummary.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRun_EmptyTRs(t *testing.T) {
	rc := &core.RunCtx{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := (&session{}).Run(context.Background(), rc); err == nil {
		t.Error("expected error for 0 TRs")
	}
}

func TestSessionRunsAndParsesDuplicateWorkloads(t *testing.T) {
	binDir := t.TempDir()
	for _, name := range []string{"kube-burner-ocp", "kubectl"} {
		if err := os.WriteFile(filepath.Join(binDir, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", binDir)
	rc := &core.RunCtx{
		WorkDir: t.TempDir(),
		Backend: &core.ResolvedBackend{StorageClass: "test-sc"},
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	job := &session{}
	trs := []core.TestRequirement{
		{ID: "TR-STOR-006", Params: map[string]any{"workload": WorkloadPVCDensity, "iterations": 5, "claim_size": "256Mi"}},
		{ID: "TR-STOR-006-COPY", Params: map[string]any{"workload": WorkloadPVCDensity, "iterations": 5, "claim_size": "256Mi"}},
	}
	findings, err := job.Preflight(context.Background(), rc, trs)
	if err != nil {
		t.Fatal(err)
	}
	for _, finding := range findings {
		if finding.Level == "error" {
			t.Fatalf("preflight: %s", finding.Message)
		}
	}
	if err := job.Provision(context.Background(), rc); err != nil {
		t.Fatal(err)
	}
	writePVCJobSummary(t, filepath.Join(job.resultsDir, trs[0].ID))
	if err := job.Run(context.Background(), rc); err != nil {
		t.Fatal(err)
	}
	if len(job.executions) != 2 || job.executions[0].Subdir == "" || job.executions[1].Skipped == "" {
		t.Fatalf("executions = %+v, want first workload run and duplicate skipped", job.executions)
	}
	logs, err := job.Collect(context.Background(), rc)
	if err != nil {
		t.Fatal(err)
	}
	results, err := job.Parse(context.Background(), rc, logs)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].TRID != trs[0].ID || results[0].Native != core.OutcomePass || results[1].TRID != trs[1].ID || results[1].Native != core.OutcomeSkip {
		t.Fatalf("results = %+v, want attributed pass and duplicate skip", results)
	}
}

func TestCollectParse_SingleTR(t *testing.T) {
	rc := &core.RunCtx{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	dir := t.TempDir()
	writePVCJobSummary(t, dir)

	job := &session{executions: []execution{pvcExecution("TR-STOR-006-0", 5, dir)}}

	logs, err := job.Collect(context.Background(), rc)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	results, err := job.Parse(context.Background(), rc, logs)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(results) != 1 || results[0].Native != core.OutcomePass {
		t.Fatalf("want 1 pass, got %#v", results)
	}
}

func TestParse_DuplicateWorkloadSkipped(t *testing.T) {
	rc := &core.RunCtx{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	dir := t.TempDir()
	writePVCJobSummary(t, dir)

	duplicate := pvcExecution("TR-STOR-006-1", 5, "")
	duplicate.Skipped = "duplicate workload in job; only the first runs (shared namespace)"
	job := &session{executions: []execution{pvcExecution("TR-STOR-006-0", 5, dir), duplicate}}

	logs, err := job.Collect(context.Background(), rc)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	results, err := job.Parse(context.Background(), rc, logs)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("want 2 results, got %d", len(results))
	}
	var pass, skip int
	for _, r := range results {
		switch r.Native {
		case core.OutcomePass:
			pass++
		case core.OutcomeSkip:
			skip++
		}
	}
	if pass != 1 || skip != 1 {
		t.Errorf("want 1 pass + 1 skip, got %d pass / %d skip", pass, skip)
	}
}

func TestParsePVCDensityIterationCount(t *testing.T) {
	for _, tc := range []struct {
		name       string
		iterations int
		want       core.Outcome
	}{
		{"matching", 5, core.OutcomePass},
		{"mismatch", 6, core.OutcomeError},
		{"missing", 0, core.OutcomeError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			job := &session{executions: []execution{pvcExecution("TR-STOR-006", tc.iterations, "")}}
			logs := core.LogBundle{Data: map[string][]byte{"TR-STOR-006/jobSummary.json": mustRead(t, "pvc-density", "jobSummary.json")}}
			results, err := job.Parse(context.Background(), nil, logs)
			if err != nil {
				t.Fatal(err)
			}
			if len(results) != 1 || results[0].Checks[pvcBoundCheck] != tc.want || results[0].Native != tc.want {
				t.Fatalf("unexpected parsed result: %+v", results)
			}
		})
	}
}

func pvcExecution(trID string, iterations int, dir string) execution {
	return execution{
		Requirement: common.ResolvedRequirement[Params]{
			TR:     core.TestRequirement{ID: trID},
			Params: Params{Workload: WorkloadPVCDensity, Raw: map[string]any{"iterations": iterations}},
		},
		Subdir: dir,
	}
}
