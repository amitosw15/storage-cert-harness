package virtbench

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/safefs"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/stages"
)

// teardownBudget bounds the best-effort cleanup, which runs on a fresh context so
// it still happens when the run ctx was cancelled.
const teardownBudget = 120 * time.Second

// --- Run ---------------------------------------------------------------------

type runner struct{ sc Scenario }

func (r runner) Run(ctx context.Context, rc *core.RunCtx, bag *core.Bag, trs []core.TestRequirement) (stages.RunHandle, error) {
	replay, _ := core.GetAs[bool](bag, "replay")
	root, _ := core.GetAs[string](bag, "results_root")
	if replay {
		rc.Logger.Info("virtbench: replay — skipping exec", "results_root", root)
		return stages.RunHandle{ID: "virtbench-replay"}, nil
	}
	if len(trs) == 0 {
		return stages.RunHandle{}, fmt.Errorf("virtbench: no TRs to run")
	}
	if r.sc.Run != nil {
		return r.sc.Run(ctx, rc, bag, trs)
	}
	args, err := r.sc.BuildArgs(rc, bag, trs[0], root)
	if err != nil {
		return stages.RunHandle{}, err
	}
	prepared := false
	if clone, ok := core.GetAs[*snapshotClone](bag, "snapshot_clone"); ok {
		path, err := clone.prepare(ctx, root)
		if err != nil {
			return stages.RunHandle{}, fmt.Errorf("prepare snapshot clone: %w", err)
		}
		args = setArgument(args, "--vm-template", path)
		prepared = true
		if rc.RecordStorageSelection != nil {
			rc.RecordStorageSelection(core.StorageSelection{TR: trs[0].ID, Variant: trs[0].Variant, StorageClass: clone.Selection.StorageClass, SnapshotClass: clone.Selection.SnapshotClass})
		}
	}
	rc.Logger.Info("virtbench: exec", "cmd", binary, "args", args, "dir", root)
	if err := execVirtbench(ctx, rc, manifestRequest{Directory: root, Args: args, TR: trs[0], Prepared: prepared}); err != nil {
		// drain-nodes exits 1 on a timeout — a gradeable FAIL, not a crash. Keep
		// going to Collect/Parse so the log is still scored (decisions/0013).
		if !r.sc.TolerateRunError {
			return stages.RunHandle{}, fmt.Errorf("virtbench: run failed: %w", err)
		}
		rc.Logger.Warn("virtbench: CLI exited non-zero; grading from its output anyway", "err", err)
	}
	return stages.RunHandle{ID: "virtbench-" + rc.RunID}, nil
}

func runVirtbench(ctx context.Context, rc *core.RunCtx, root string, args []string, tr core.TestRequirement) error {
	rc.Logger.Info("virtbench: exec", "cmd", binary, "args", args, "dir", root)
	if err := execVirtbench(ctx, rc, manifestRequest{Directory: root, Args: args, TR: tr}); err != nil {
		return fmt.Errorf("virtbench: run failed: %w", err)
	}
	return nil
}

// --- Collect -----------------------------------------------------------------

type collector struct{ sc Scenario }

func (c collector) Collect(_ context.Context, rc *core.RunCtx, bag *core.Bag, h stages.RunHandle) (core.LogBundle, error) {
	root, _ := core.GetAs[string](bag, "results_root")
	if root == "" {
		return core.LogBundle{}, fmt.Errorf("virtbench: no results_root in bag")
	}
	if c.sc.Collect != nil {
		replay, _ := core.GetAs[bool](bag, "replay")
		refs, data, err := c.sc.Collect(root, replay)
		if err != nil {
			return core.LogBundle{}, err
		}
		rc.Logger.Info("virtbench: collected results", "paths", refs)
		return core.LogBundle{
			Refs: refs,
			Data: map[string][]byte{c.sc.ResultFile: data},
		}, nil
	}
	path, err := findResult(root, c.sc.ResultFile)
	if err != nil {
		return core.LogBundle{}, err
	}
	data, err := safefs.ReadUnder(root, path)
	if err != nil {
		return core.LogBundle{}, fmt.Errorf("virtbench: read results: %w", err)
	}
	rc.Logger.Info("virtbench: collected results", "path", path)
	refs := []string{path}
	bundle := map[string][]byte{c.sc.ResultFile: data}
	if c.sc.DetailFile != "" {
		// A scenario with DetailParse is graded from these per-VM samples (the
		// summary carries only avg/max/min), so a lookup that cannot name one
		// file must stop the execution. Degrading to summary-only would drop
		// the gated metric, and an ambiguous detail file is the very case this
		// collector must not guess at. Where no DetailParse is set the file is
		// supplementary and stays best-effort.
		dp, err := findResult(root, c.sc.DetailFile)
		switch {
		case err != nil && c.sc.DetailParse != nil:
			return core.LogBundle{}, fmt.Errorf("virtbench: detail results: %w", err)
		case err != nil:
			rc.Logger.Warn("virtbench: detail results not found", "file", c.sc.DetailFile, "err", err)
		default:
			dd, readErr := safefs.ReadUnder(root, dp)
			if readErr != nil && c.sc.DetailParse != nil {
				return core.LogBundle{}, fmt.Errorf("virtbench: read detail results %s: %w", dp, readErr)
			}
			if readErr == nil {
				bundle[c.sc.DetailFile] = dd
				refs = append(refs, dp)
			}
		}
	}
	return core.LogBundle{Refs: refs, Data: bundle}, nil
}

// collectFIOResults keeps every per-VM raw FIO result together with Virtbench's
// run summary, so the parser can reject an incomplete VM range.
func collectFIOResults(root string, replay bool) ([]string, []byte, error) {
	paths, err := findResults(root, FIOFileName)
	if err != nil {
		return nil, nil, err
	}
	results := make([]json.RawMessage, 0, len(paths))
	for _, path := range paths {
		data, err := safefs.ReadUnder(root, path)
		if err != nil {
			return nil, nil, fmt.Errorf("virtbench: read fio result %q: %w", path, err)
		}
		results = append(results, data)
	}
	if replay {
		if len(results) != 1 {
			return nil, nil, fmt.Errorf("virtbench: replay FIO results has %d raw files, want one", len(results))
		}
		return paths, results[0], nil
	}

	summaryPath, err := findResult(root, FIOSummaryFileName)
	if err != nil {
		return nil, nil, err
	}
	summary, err := safefs.ReadUnder(root, summaryPath)
	if err != nil {
		return nil, nil, fmt.Errorf("virtbench: read fio summary: %w", err)
	}
	bundle, err := json.Marshal(fioCollectedResults{Summary: summary, Results: results})
	if err != nil {
		return nil, nil, fmt.Errorf("virtbench: package fio results: %w", err)
	}
	return append(paths, summaryPath), bundle, nil
}

// findResult returns the path to the scenario's result file: root/<name> if
// present, else the one match found walking root (virtbench nests results in a
// <n>-disk/<timestamp>_<prefix>_<range> subdir). root is one execution's own
// directory, so exactly one match is expected; several would mean results from
// more than one execution are in scope, and grading the wrong test silently is
// worse than failing, so refuse rather than choose.
func findResult(root, name string) (string, error) {
	// The walk covers root's own files too, so a copy sitting directly in root
	// is one candidate among the nested ones rather than a short circuit past
	// them: returning it early would skip the check this function exists for.
	found, err := findResults(root, name)
	if err != nil {
		return "", err
	}
	if len(found) > 1 {
		return "", fmt.Errorf("virtbench: %d copies of %s under %s (%s); cannot tell which belongs to this execution",
			len(found), name, root, strings.Join(found, ", "))
	}
	return found[0], nil
}

func findResults(root, name string) ([]string, error) {
	var found []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && d.Name() == name {
			found = append(found, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("virtbench: search %s: %w", root, err)
	}
	if len(found) == 0 {
		return nil, fmt.Errorf("virtbench: %s not found under %s", name, root)
	}
	sort.Strings(found)
	return found, nil
}

// --- Parse -------------------------------------------------------------------

type parser struct{ sc Scenario }

func (p parser) Parse(_ context.Context, _ *core.RunCtx, bag *core.Bag, logs core.LogBundle) ([]core.TestResult, error) {
	trID, _ := core.GetAs[string](bag, "tr_id")
	if trID == "" {
		trID = p.sc.ProvidesTR
	}
	res, err := p.sc.Parse(logs.Data[p.sc.ResultFile], trID)
	if err != nil {
		return nil, err
	}
	if p.sc.DetailParse != nil && len(res) > 0 {
		if dd, ok := logs.Data[p.sc.DetailFile]; ok {
			extra, err := p.sc.DetailParse(dd)
			if err != nil {
				return nil, err
			}
			res[0].Metrics = append(res[0].Metrics, extra...)
		}
	}
	return res, nil
}

// --- Teardown ----------------------------------------------------------------

type teardown struct{ sc Scenario }

// Teardown is a best-effort safety net. Normal runs pass --cleanup(-on-failure)
// so virtbench removes its own namespaces/VMs; the generic sweep only catches the
// case where the process was killed mid-run (drain-nodes deliberately omits
// --cleanup, so its VMs always land here). It runs on a fresh bounded context so
// cleanup still happens when the run ctx was cancelled, and never fails the run.
func (t teardown) Teardown(_ context.Context, rc *core.RunCtx, bag *core.Bag) error {
	if replay, _ := core.GetAs[bool](bag, "replay"); replay {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), teardownBudget)
	defer cancel()
	if clone, ok := core.GetAs[*snapshotClone](bag, "snapshot_clone"); ok {
		defer clone.cleanup(ctx, rc)
	}

	// A scenario adds its own cleanup on top of the generic namespace sweep (drain
	// uncordons the workers it cordoned).
	if t.sc.Teardown != nil {
		if err := t.sc.Teardown(ctx, rc, bag); err != nil {
			rc.Logger.Warn("virtbench: scenario teardown failed", "tool", t.sc.AutomationTool, "err", err)
		}
	}

	prefix, _ := core.GetAs[string](bag, "ns_prefix")
	if prefix == "" {
		prefix = t.sc.NSPrefix
	}
	kubeconfig, _ := core.GetAs[string](bag, "kubeconfig")

	leftovers, err := listNamespacesByPrefix(ctx, kubeconfig, prefix+"-")
	if err != nil {
		rc.Logger.Warn("virtbench: teardown could not list namespaces; verify none leftover", "prefix", prefix+"-", "err", err)
		return nil
	}
	if len(leftovers) == 0 {
		return nil
	}
	rc.Logger.Warn("virtbench: cleaning up leftover namespaces", "namespaces", leftovers)
	for _, ns := range leftovers {
		if err := deleteNamespace(ctx, kubeconfig, ns); err != nil {
			rc.Logger.Warn("virtbench: failed to delete leftover namespace", "namespace", ns, "err", err)
		}
	}
	return nil
}
