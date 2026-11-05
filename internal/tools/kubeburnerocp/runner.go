package kubeburnerocp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/openshift-storage-cert/storage-cert-harness/internal/clustercheck"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/core"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/safefs"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/stages"
	"github.com/openshift-storage-cert/storage-cert-harness/internal/toolprocess"
)

// kboRun records one workload execution (one TR) so Collect/Parse can handle a
// job that bundles several kube-burner TRs.
type kboRun struct {
	Iterations       int // resolved plan count, checked against the PVC-density job summary
	TRID             string
	Workload         string
	Subdir           string
	RunErr           string
	RunCancelled     bool
	Skipped          string
	CleanupNamespace string
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
		if namespace := workloadNamespace(p); namespace != "" {
			exists, err := namespaceExists(ctx, namespace)
			if err != nil {
				runs[i].RunErr = fmt.Errorf("check workload namespace %q: %w", namespace, err).Error()
				continue
			}
			if !exists {
				runs[i].CleanupNamespace = namespace
			} else if rc.Logger != nil {
				rc.Logger.Info("kube-burner-ocp: workload namespace existed before run; teardown will skip namespace cleanup", "tr", tr.ID, "namespace", namespace)
			}
		}

		subdir := filepath.Join(resultsDir, tr.ID)
		if err := safefs.MkdirAll(subdir, 0o755); err != nil {
			runs[i].RunErr = err.Error()
			continue
		}
		args, err := buildCLIArgs(p, sc)
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
			runs[i].RunCancelled = ctx.Err() != nil
		}(i)
	}
	wg.Wait()

	bag.Set("kbo_runs", runs)
	return stages.RunHandle{ID: "kube-burner-ocp"}, nil
}

func namespaceExists(ctx context.Context, namespace string) (bool, error) {
	cli := clustercheck.KubeCLI()
	if cli == "" {
		return false, fmt.Errorf("kubectl or oc not found on PATH")
	}
	out, err := exec.CommandContext(ctx, cli, "get", "namespace", namespace, "--ignore-not-found", "-o", "name").Output() // #nosec G204 -- CLI is resolved by clustercheck and namespace comes from adapter parameters.
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) != "", nil
}

// buildRunCmd runs kube-burner-ocp from the host PATH, writing into resultsDir/<subdir>.
func buildRunCmd(ctx context.Context, rc *core.RunCtx, args []string, resultsDir, subdir string) (*exec.Cmd, error) {
	path, err := exec.LookPath("kube-burner-ocp")
	if err != nil {
		return nil, fmt.Errorf("kube-burner-ocp: kube-burner-ocp not on PATH: %w", err)
	}
	cmd := toolprocess.CommandContext(ctx, path, args...) // #nosec G204 -- path comes from LookPath and args are harness-generated.
	cmd.Dir = filepath.Join(resultsDir, subdir)
	rc.Logger.Info("kube-burner-ocp: running host binary", "path", path)
	return cmd, nil
}

type collector struct{}

func (collector) Collect(_ context.Context, rc *core.RunCtx, bag *core.Bag, _ stages.RunHandle) (core.LogBundle, error) {
	runs, _ := core.GetAs[[]kboRun](bag, "kbo_runs")

	data := map[string][]byte{}
	var refs []string
	// This collector's job spans every kube-burner-ocp TR in the plan, so the
	// references have to stay attributed per TR.
	refsByTR := map[string][]string{}
	for _, r := range runs {
		if r.Skipped != "" {
			continue
		}
		if r.Subdir == "" && r.RunErr != "" {
			rc.Logger.Warn("kube-burner-ocp: run produced no result directory", "tr", r.TRID, "err", r.RunErr)
			continue
		}
		m, files, err := findCollectedMetrics(r.Subdir)
		if err != nil {
			rc.Logger.Warn("kube-burner-ocp: no results collected", "tr", r.TRID, "err", err)
			continue // Parse emits a fail verdict for this TR
		}
		refs = append(refs, files...)
		refsByTR[r.TRID] = append(refsByTR[r.TRID], files...)
		for name, content := range m {
			data[r.TRID+"/"+name] = content
		}
	}
	rc.Logger.Info("kube-burner-ocp: collected results", "trs", len(runs))
	return core.LogBundle{Refs: refs, RefsByTR: refsByTR, Data: data}, nil
}

// findCollectedMetrics returns the metric files under root keyed by base name,
// alongside the path of each one read, so a report can cite the files a verdict
// came from rather than the directory that held them.
func findCollectedMetrics(root string) (map[string][]byte, []string, error) {
	data := make(map[string][]byte)
	var paths []string
	rootFS, err := os.OpenRoot(root)
	if err != nil {
		return nil, nil, err
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
			paths = append(paths, filepath.Join(root, path))
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	if len(data) == 0 {
		return nil, nil, fmt.Errorf("kube-burner-ocp: no collected-metrics under %s", root)
	}
	sort.Strings(paths)
	return data, paths, nil
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
		if r.Workload == WorkloadPVCDensity {
			matched := false
			for _, m := range res.Metrics {
				if m.Name == "pvc_requested_count" && r.Iterations > 0 && m.Value == float64(r.Iterations) {
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

// Teardown removes a namespace created by a workload that failed or was cancelled.
func (teardown) Teardown(ctx context.Context, rc *core.RunCtx, bag *core.Bag) error {
	runs, _ := core.GetAs[[]kboRun](bag, "kbo_runs")
	cleanupCtx := ctx
	var errs []error
	for _, run := range runs {
		if (run.RunErr == "" && !run.RunCancelled) || run.Subdir == "" || run.CleanupNamespace == "" {
			continue
		}
		cli := clustercheck.KubeCLI()
		output, err := exec.CommandContext(cleanupCtx, cli, "delete", "namespace", run.CleanupNamespace, "--ignore-not-found", "--timeout=3m").CombinedOutput() // #nosec G204 -- CLI is resolved by clustercheck and namespace was selected by the adapter.
		if err != nil {
			err = fmt.Errorf("delete workload namespace %q: %w: %s", run.CleanupNamespace, err, strings.TrimSpace(string(output)))
			if rc.Logger != nil {
				rc.Logger.Warn("kube-burner-ocp: timeout cleanup failed", "namespace", run.CleanupNamespace, "err", err)
			}
			errs = append(errs, err)
			continue
		}
	}
	return errors.Join(errs...)
}
