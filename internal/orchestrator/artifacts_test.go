package orchestrator

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/config"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/registry"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/stages"
)

type artifactCollector struct{ workdir string }

func (a artifactCollector) Collect(_ context.Context, _ *core.RunCtx, _ *core.Bag, _ stages.RunHandle) (core.LogBundle, error) {
	return core.LogBundle{Refs: []string{
		filepath.Join(a.workdir, "virtbench/TR-ART-001/20261018-060401/2-disk/run/summary.json"),
		"/somewhere/else/outside-the-workdir.json",
		"handle-id-not-a-path",
	}}, nil
}

// A verdict should be traceable to the files it was graded from, and those
// paths are published evidence: they must be relative to the run's working
// directory so a report never carries the operator's home directory, and
// anything outside that directory is dropped rather than recorded.
func TestExecutionRecordsWorkdirRelativeArtifacts(t *testing.T) {
	workdir := t.TempDir()
	registry.Register(stages.ToolIntegration{Name: "artifact-recording-test", LogCollector: artifactCollector{workdir: workdir}})
	rc := &core.RunCtx{
		RunID:   "20261018-060401",
		WorkDir: workdir,
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	rep, err := Run(context.Background(), config.Config{}, []core.TestRequirement{{ID: "TR-ART-001", AutomationTool: "artifact-recording-test"}}, core.Provenance{}, "2.0", rc)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Executions) != 1 {
		t.Fatalf("executions = %+v", rep.Executions)
	}
	got := rep.Executions[0].Artifacts
	want := []string{"virtbench/TR-ART-001/20261018-060401/2-disk/run/summary.json"}
	if len(got) != len(want) || got[0] != want[0] {
		t.Errorf("artifacts = %v, want %v (relative, and nothing from outside the workdir)", got, want)
	}
}

type multiTRCollector struct{ workdir string }

func (m multiTRCollector) Collect(_ context.Context, _ *core.RunCtx, _ *core.Bag, _ stages.RunHandle) (core.LogBundle, error) {
	a := filepath.Join(m.workdir, "kubeburner-results/TR-ART-A/collected-metrics/jobSummary.json")
	b := filepath.Join(m.workdir, "kubeburner-results/TR-ART-B/collected-metrics/jobSummary.json")
	return core.LogBundle{
		Refs:     []string{a, b},
		RefsByTR: map[string][]string{"TR-ART-A": {a}, "TR-ART-B": {b}},
	}, nil
}

// A job can cover several TRs — every kube-burner-ocp test in a plan shares
// one. The collector's flat Refs mixes their files, so recording it wholesale
// would hand each execution the others' results: the same misattribution the
// per-execution results root exists to prevent, reintroduced one layer up.
func TestMultiTRJobAttributesArtifactsPerTR(t *testing.T) {
	workdir := t.TempDir()
	registry.Register(stages.ToolIntegration{Name: "multi-tr-artifact-test", LogCollector: multiTRCollector{workdir: workdir}})
	rc := &core.RunCtx{RunID: "20261018-060401", WorkDir: workdir, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	rep, err := Run(context.Background(), config.Config{}, []core.TestRequirement{
		{ID: "TR-ART-A", AutomationTool: "multi-tr-artifact-test"},
		{ID: "TR-ART-B", AutomationTool: "multi-tr-artifact-test"},
	}, core.Provenance{}, "2.0", rc)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range rep.Executions {
		if len(e.Artifacts) != 1 {
			t.Fatalf("%s got %d artifacts, want only its own: %v", e.TR, len(e.Artifacts), e.Artifacts)
		}
		if !strings.Contains(e.Artifacts[0], e.TR) {
			t.Errorf("%s was given another test's results: %s", e.TR, e.Artifacts[0])
		}
	}
}
