package kubeburnerocp

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/core"
	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/safefs"
	"gitlab.cee.redhat.com/eco-special-projects/storage-cert-harness/internal/stages"
)

// kboRun records one workload execution (one TR) so Collect/Parse can handle a
// job that bundles several kube-burner TRs.
type kboRun struct {
	Iterations int // resolved plan count, checked against the PVC-density job summary
	TRID       string
	Workload   string
	Subdir     string
	RunErr     string
	Skipped    string
}

type runner struct{}

func (runner) Run(ctx context.Context, rc *core.RunCtx, bag *core.Bag, trs []core.TestRequirement) (stages.RunHandle, error) {
	if len(trs) == 0 {
		return stages.RunHandle{}, fmt.Errorf("kube-burner-ocp: no TRs to run")
	}

	resultsDir, _ := core.GetAs[string](bag, "results_dir")
	if resultsDir == "" {
		return stages.RunHandle{}, fmt.Errorf("kube-burner-ocp: results_dir missing (provision first)")
	}
	sc := ""
	if rc.Backend != nil {
		sc = rc.Backend.StorageClass
	}

	// Per-TR errors are isolated (recorded, not fatal); a repeated workload is skipped (shared namespace).
	runs := make([]kboRun, len(trs))
	cmds := make([]*exec.Cmd, len(trs))
	seen := map[string]bool{}
	for i, tr := range trs {
		p := resolveParams([]core.TestRequirement{tr})
		if err := p.validate(); err != nil {
			runs[i] = kboRun{TRID: tr.ID, RunErr: err.Error()}
			continue
		}
		runs[i] = kboRun{TRID: tr.ID, Workload: p.Workload}
		runs[i].Iterations, _ = asInt(p.Raw["iterations"])
		if seen[p.Workload] {
			runs[i].Skipped = "duplicate workload in job; only the first runs (shared namespace)"
			continue
		}
		seen[p.Workload] = true

		subdir := filepath.Join(resultsDir, tr.ID)
		if err := safefs.MkdirAll(subdir, 0o755); err != nil {
			runs[i].RunErr = err.Error()
			continue
		}
		var args []string
		var err error
		if workloadSpecs[p.Workload].customConfig {
			args, err = prepareFourPVCConfig(ctx, rc, subdir, sc, p)
		} else {
			args, err = buildCLIArgs(p, sc)
		}
		if err != nil {
			runs[i].RunErr = err.Error()
			continue
		}
		cmd, err := buildRunCmd(ctx, rc, args, resultsDir, tr.ID)
		if err != nil {
			runs[i].RunErr = err.Error()
			rc.Logger.Warn("kube-burner-ocp: could not build run command", "tr", tr.ID, "err", err)
			continue
		}
		cmd.Stdout = rc.ToolOutput(os.Stdout)
		cmd.Stderr = rc.ToolOutput(os.Stderr)
		cmds[i] = cmd
		runs[i].Subdir = subdir
	}

	var wg sync.WaitGroup
	for i := range runs {
		if cmds[i] == nil {
			continue
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rc.Logger.Info("kube-burner-ocp: running", "tr", runs[i].TRID, "workload", runs[i].Workload)
			if err := cmds[i].Run(); err != nil {
				runs[i].RunErr = err.Error()
				rc.Logger.Warn("kube-burner-ocp: run failed", "tr", runs[i].TRID, "err", err)
			}
		}(i)
	}
	wg.Wait()

	bag.Set("kbo_runs", runs)
	return stages.RunHandle{ID: "kube-burner-ocp"}, nil
}

// buildRunCmd runs kube-burner-ocp from the host PATH, writing into resultsDir/<subdir>.
func buildRunCmd(ctx context.Context, rc *core.RunCtx, args []string, resultsDir, subdir string) (*exec.Cmd, error) {
	path, err := exec.LookPath("kube-burner-ocp")
	if err != nil {
		return nil, fmt.Errorf("kube-burner-ocp: kube-burner-ocp not on PATH: %w", err)
	}
	cmd := exec.CommandContext(ctx, path, args...) // #nosec G204 -- path comes from LookPath and args are harness-generated.
	cmd.Dir = filepath.Join(resultsDir, subdir)
	rc.Logger.Info("kube-burner-ocp: running host binary", "path", path)
	return cmd, nil
}

type collector struct{}

func (collector) Collect(_ context.Context, rc *core.RunCtx, bag *core.Bag, _ stages.RunHandle) (core.LogBundle, error) {
	runs, _ := core.GetAs[[]kboRun](bag, "kbo_runs")

	data := map[string][]byte{}
	var refs []string
	for _, r := range runs {
		if r.Skipped != "" {
			continue
		}
		if r.Subdir == "" && r.RunErr != "" {
			rc.Logger.Warn("kube-burner-ocp: run produced no result directory", "tr", r.TRID, "err", r.RunErr)
			continue
		}
		m, err := findCollectedMetrics(r.Subdir)
		if err != nil {
			rc.Logger.Warn("kube-burner-ocp: no results collected", "tr", r.TRID, "err", err)
			continue // Parse emits a fail verdict for this TR
		}
		refs = append(refs, r.Subdir)
		for name, content := range m {
			data[r.TRID+"/"+name] = content
		}
	}
	rc.Logger.Info("kube-burner-ocp: collected results", "trs", len(runs))
	return core.LogBundle{Refs: refs, Data: data}, nil
}

func findCollectedMetrics(root string) (map[string][]byte, error) {
	data := make(map[string][]byte)
	rootFS, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rootFS.Close() }()
	err = fs.WalkDir(rootFS.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		name := entry.Name()
		if name == "jobSummary.json" || strings.Contains(name, "Quantiles") {
			content, err := rootFS.ReadFile(path)
			if err != nil {
				return err
			}
			data[name] = content
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("kube-burner-ocp: no collected-metrics under %s", root)
	}
	return data, nil
}

type parser struct{}

func (parser) Parse(_ context.Context, _ *core.RunCtx, bag *core.Bag, logs core.LogBundle) ([]core.TestResult, error) {
	runs, _ := core.GetAs[[]kboRun](bag, "kbo_runs")
	var out []core.TestResult
	for _, r := range runs {
		if r.Skipped != "" {
			raw, _ := json.Marshal(map[string]string{"skipped": r.Skipped})
			out = append(out, core.TestResult{TRID: r.TRID, Native: core.OutcomeSkip, Raw: raw})
			continue
		}
		sub := map[string][]byte{}
		prefix := r.TRID + "/"
		for k, v := range logs.Data {
			if strings.HasPrefix(k, prefix) {
				sub[strings.TrimPrefix(k, prefix)] = v
			}
		}

		if _, ok := sub["jobSummary.json"]; !ok {
			reason := "kube-burner produced no jobSummary"
			if r.RunErr != "" {
				reason = r.RunErr
			}
			raw, _ := json.Marshal(map[string]string{"error": reason})
			out = append(out, core.TestResult{TRID: r.TRID, Native: core.OutcomeFail, Raw: raw})
			continue
		}

		res, err := ParseResults(r.Workload, r.TRID, sub)
		if err != nil {
			return nil, err
		}
		if pvcPerIteration := workloadSpecs[r.Workload].pvcPerIteration; pvcPerIteration > 0 {
			expectedPVCs := r.Iterations * pvcPerIteration
			matched := false
			for _, m := range res.Metrics {
				if m.Name == "pvc_requested_count" && expectedPVCs > 0 && m.Value == float64(expectedPVCs) {
					matched = true
				}
			}
			if !matched {
				res.Checks[pvcBoundCheck] = core.OutcomeError
				res.Native = core.OutcomeError
			}
		}
		out = append(out, res)
	}
	return out, nil
}

type teardown struct{}

// Teardown relies on each workload's native gc/cleanup.
func (teardown) Teardown(_ context.Context, _ *core.RunCtx, _ *core.Bag) error {
	return nil
}
