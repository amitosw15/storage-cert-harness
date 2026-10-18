package kubeburnerocp

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/stages"
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
	if _, err := (runner{}).Run(context.Background(), rc, core.NewBag(), nil); err == nil {
		t.Error("expected error for 0 TRs")
	}
}

func TestCollectParse_SingleTR(t *testing.T) {
	rc := &core.RunCtx{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	dir := t.TempDir()
	writePVCJobSummary(t, dir)

	bag := core.NewBag()
	bag.Set("kbo_runs", []kboRun{{TRID: "TR-STOR-006-0", Workload: WorkloadPVCDensity, Iterations: 5, Subdir: dir}})

	logs, err := (collector{}).Collect(context.Background(), rc, bag, stages.RunHandle{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	results, err := (parser{}).Parse(context.Background(), rc, bag, logs)
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

	bag := core.NewBag()
	bag.Set("kbo_runs", []kboRun{
		{TRID: "TR-STOR-006-0", Workload: WorkloadPVCDensity, Iterations: 5, Subdir: dir},
		{TRID: "TR-STOR-006-1", Workload: WorkloadPVCDensity, Skipped: "duplicate workload in job; only the first runs (shared namespace)"},
	})

	logs, err := (collector{}).Collect(context.Background(), rc, bag, stages.RunHandle{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	results, err := (parser{}).Parse(context.Background(), rc, bag, logs)
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
			bag := core.NewBag()
			bag.Set("kbo_runs", []kboRun{{TRID: "TR-STOR-006", Workload: WorkloadPVCDensity, Iterations: tc.iterations}})
			logs := core.LogBundle{Data: map[string][]byte{"TR-STOR-006/jobSummary.json": mustRead(t, "pvc-density", "jobSummary.json")}}
			results, err := (parser{}).Parse(context.Background(), nil, bag, logs)
			if err != nil {
				t.Fatal(err)
			}
			if len(results) != 1 || results[0].Checks[pvcBoundCheck] != tc.want || results[0].Native != tc.want {
				t.Fatalf("unexpected parsed result: %+v", results)
			}
		})
	}
}
