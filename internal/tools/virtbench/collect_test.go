package virtbench

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/core"
	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/stages"
)

// Virtbench nests its output under <n>-disk/<timestamp>_<prefix>_<range>/, and
// every datasource-clone scenario writes the same two filenames. These fixtures
// reproduce a single-node run (1-disk, slow clones) and a clone-at-scale run
// (2-disk, fast clones) so a collector that reaches the wrong one is visible in
// the parsed numbers rather than only in the path.
const (
	singleNodeDir = "1-disk/20261018-120233_virtbench-single-node_1-100"
	atScaleDir    = "2-disk/20261018-121211_virtbench_1-100"

	singleNodeSummary = `{"total_vms":3,"successful":3,"failed":0,"total_test_duration_sec":390.89,
	  "metrics":[{"metric":"clone_duration_sec","avg":5.78,"max":137.37,"min":1.0,"count":3}]}`
	singleNodeDetail = `[{"clone_duration_sec":1.0},{"clone_duration_sec":121.63},{"clone_duration_sec":137.37}]`

	atScaleSummary = `{"total_vms":2,"successful":2,"failed":0,"total_test_duration_sec":209.09,
	  "metrics":[{"metric":"clone_duration_sec","avg":3.05,"max":29.49,"min":1.0,"count":2}]}`
	atScaleDetail = `[{"clone_duration_sec":1.0},{"clone_duration_sec":29.49}]`
)

func writeResults(t *testing.T, root, subdir, summary, detail string) {
	t.Helper()
	dir := filepath.Join(root, subdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("make %s: %v", dir, err)
	}
	for name, body := range map[string]string{
		SummaryFileName:     summary,
		DetailCloneFileName: detail,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
}

func cloneAtScaleScenario() Scenario {
	return Scenario{
		AutomationTool: "virtbench datasource-clone --num-disks 2",
		ProvidesTR:     "TR-VIRT-018",
		ResultFile:     SummaryFileName,
		DetailFile:     DetailCloneFileName,
	}
}

// TR-VIRT-018 was graded from TR-VIRT-013's results because both scenarios wrote
// summary_vm_creation_results.json under one shared root and the collector took
// the first match walking it: "1-disk" sorts before "2-disk", so the clone-at-
// scale run read the single-node run's numbers. Giving each execution its own
// root is what keeps the two apart.
func TestCollectReadsOnlyItsOwnExecution(t *testing.T) {
	workdir := t.TempDir()
	rc := &core.RunCtx{RunID: "20261018-060401", WorkDir: workdir, Logger: slog.New(slog.DiscardHandler)}

	singleNode := rc.ExecutionDir(resultsSubdir, "TR-VIRT-013", "")
	atScale := rc.ExecutionDir(resultsSubdir, "TR-VIRT-018", "")
	writeResults(t, singleNode, singleNodeDir, singleNodeSummary, singleNodeDetail)
	writeResults(t, atScale, atScaleDir, atScaleSummary, atScaleDetail)

	bag := core.NewBag()
	bag.Set("results_root", atScale)
	bundle, err := (collector{sc: cloneAtScaleScenario()}).Collect(context.Background(), rc, bag, stages.RunHandle{ID: "test"})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}

	for _, ref := range bundle.Refs {
		if strings.Contains(ref, "TR-VIRT-013") || strings.Contains(ref, "1-disk") {
			t.Errorf("collected another execution's results: %s", ref)
		}
	}
	if got := string(bundle.Data[DetailCloneFileName]); got != atScaleDetail {
		t.Errorf("detail results = %s\nwant the 2-disk file, not the single-node one", got)
	}
	metrics, err := ClonePercentiles(bundle.Data[DetailCloneFileName], []string{"p99"})
	if err != nil {
		t.Fatalf("clone percentiles: %v", err)
	}
	if len(metrics) != 1 || metrics[0].Value != 29.49 {
		t.Errorf("clone_duration p99 = %+v, want 29.49 from this execution's own samples", metrics)
	}
}

// A root holding two executions' results is the shape that produced the wrong
// grade. Returning either file would be a guess, and a silently misattributed
// measurement is worse than a failed run, so the collector must refuse.
func TestCollectRefusesAmbiguousResults(t *testing.T) {
	shared := t.TempDir()
	writeResults(t, shared, singleNodeDir, singleNodeSummary, singleNodeDetail)
	writeResults(t, shared, atScaleDir, atScaleSummary, atScaleDetail)

	bag := core.NewBag()
	bag.Set("results_root", shared)
	rc := &core.RunCtx{RunID: "20261018-060401", WorkDir: shared, Logger: slog.New(slog.DiscardHandler)}
	bundle, err := (collector{sc: cloneAtScaleScenario()}).Collect(context.Background(), rc, bag, stages.RunHandle{ID: "test"})
	if err == nil {
		// Before the per-execution root this returned the lexically first match:
		// "1-disk" beats "2-disk", so the clone-at-scale grade came from the
		// single-node run's samples.
		t.Fatalf("collect picked one of two executions instead of refusing; it read %v (detail p99 would come from %s)",
			bundle.Refs, string(bundle.Data[DetailCloneFileName]))
	}
	for _, want := range []string{singleNodeDir, atScaleDir} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not name the competing results %q: %v", want, err)
		}
	}
}
